// Package app coordinates application use cases shared by every front end.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/moutansos/op/internal/action"
	"github.com/moutansos/op/internal/agents"
	"github.com/moutansos/op/internal/config"
	"github.com/moutansos/op/internal/domain"
	gitrepo "github.com/moutansos/op/internal/git"
	"github.com/moutansos/op/internal/process"
	"github.com/moutansos/op/internal/project"
	"github.com/moutansos/op/internal/stats"
	tmuxmanager "github.com/moutansos/op/internal/tmux"
)

const (
	defaultDashboardCommand = "op dashboard"
)

// Catalog is the project discovery and safe-path boundary used by Service.
type Catalog interface {
	List(context.Context) ([]domain.Project, error)
	ResolveByID(context.Context, string) (domain.Project, error)
	ResolveByName(context.Context, string) (domain.Project, error)
	CreatePath(string) (string, error)
	ClonePath(string) (string, error)
	WorktreePath(string) (string, error)
	ValidateRepositoryPath(string) (string, error)
	RepositoryRoot() string
}

// Repository is the git boundary used by Service.
type Repository interface {
	Clone(context.Context, gitrepo.CloneOptions) (gitrepo.CloneResult, error)
	Init(context.Context, string) error
	State(context.Context, string) (domain.GitState, error)
	Pull(context.Context, string) error
	CreateWorktree(context.Context, gitrepo.WorktreeOptions) (gitrepo.WorktreeResult, error)
}

// Launcher is the local process boundary used by Service.
type Launcher interface {
	LaunchProjectOpener(context.Context, string, string) error
	LaunchNvim(context.Context, string) error
	LaunchCode(context.Context, string) error
	LaunchPreferredShell(context.Context, string) error
	RunCustom(context.Context, string, string) error
}

// TmuxManager is the tmux orchestration boundary used by Service.
type TmuxManager interface {
	EnsureMainSession(context.Context) (domain.EnsureMainSessionResult, error)
	PrepareAttachOrSwitch(context.Context) (tmuxmanager.AttachPlan, error)
	PrepareAttachOrSwitchTo(context.Context, string) (tmuxmanager.AttachPlan, error)
	ExecuteAttachOrSwitch(context.Context, tmuxmanager.AttachPlan) error
	OpenProjectWindow(context.Context, tmuxmanager.OpenProjectWindowRequest) (domain.OpenProjectResult, error)
	Snapshot(context.Context) (domain.TmuxSnapshot, error)
	CurrentProjectID(context.Context) (string, bool, error)
	CurrentProjectName(context.Context) (string, bool, error)
}

// StatsCollector samples host and process metrics for a tmux snapshot.
type StatsCollector interface {
	Collect(context.Context, domain.TmuxSnapshot) (domain.StatsSnapshot, error)
}

// Dependencies permits deterministic service tests without exposing concrete
// git, process, tmux, or metrics implementations to front ends.
type Dependencies struct {
	Catalog    Catalog
	Repository Repository
	Launcher   Launcher
	Tmux       TmuxManager
	Stats      StatsCollector
}

// Options controls application policy rather than package-level mechanics.
type Options struct {
	EnableRepositoryUpdates bool
	DashboardCommand        string
	OperationLockDirectory  string
	Output                  io.Writer
	Error                   io.Writer
}

// Service is the concrete domain.Service implementation.
type Service struct {
	config         config.Config
	catalog        Catalog
	repository     Repository
	launcher       Launcher
	tmux           TmuxManager
	stats          StatsCollector
	locks          *keyedLocks
	tmuxLockKey    string
	updateRepos    bool
	projectOpeners map[string]config.ProjectOpener
	customCommands map[string]struct{}
}

var _ domain.Service = (*Service)(nil)

// New constructs the production service and all of its package adapters.
func New(ctx context.Context, cfg config.Config, options Options) (*Service, error) {
	const op = "app.new"
	if err := config.Validate(cfg); err != nil {
		return nil, err
	}
	catalog, err := project.NewCatalog(cfg)
	if err != nil {
		return nil, err
	}
	launcher, err := process.NewLauncher(process.Options{
		PreferredShell: cfg.PreferredShell,
		GUIEditors:     cfg.Actions.GUIEditors,
		OpRoot:         cfg.RootDirectory,
		ProjectOpeners: cfg.ProjectOpeners,
		CustomCommands: cfg.CustomCommands,
	})
	if err != nil {
		return nil, typed(ctx, op, domain.ErrorCodeConfig, "configure process launcher", err)
	}
	startDirectory := cfg.RootDirectory
	if startDirectory == "" {
		startDirectory = cfg.RepoDirectory
	}
	dashboardCommand := options.DashboardCommand
	if dashboardCommand == "" {
		dashboardCommand = defaultDashboardCommand
	}
	defaultOpener, _ := projectOpener(cfg, cfg.Tmux.DefaultProfile)
	tmuxConfig := tmuxmanager.ManagerConfig{
		Session:          cfg.Tmux.Session,
		DashboardWindow:  cfg.Tmux.DashboardWindow,
		Socket:           cfg.Tmux.Socket,
		StartDirectory:   startDirectory,
		DashboardCommand: dashboardCommand,
		EditorCommand:    defaultOpener.Command,
		PreferredShell:   cfg.PreferredShell,
		ShellPaneRows:    cfg.Tmux.ShellPaneRows,
		DefaultProfile:   cfg.Tmux.DefaultProfile,
		Output:           options.Output,
		Error:            options.Error,
	}
	tmux := newLazyTmux(func(callCtx context.Context) (TmuxManager, error) {
		manager, err := tmuxmanager.New(callCtx, tmuxConfig)
		if err != nil {
			return nil, typed(callCtx, op, domain.ErrorCodeDependency, "configure tmux manager", err)
		}
		return manager, nil
	}, func(callCtx context.Context) (domain.TmuxSnapshot, error) {
		return tmuxmanager.ReadSnapshot(callCtx, tmuxConfig)
	})
	statsOptions, err := agentStatsOptions(cfg, tmuxConfig)
	if err != nil {
		return nil, typed(ctx, op, domain.ErrorCodeConfig, "configure agent detection", err)
	}
	return NewWithDependencies(cfg, Dependencies{
		Catalog:    catalog,
		Repository: gitrepo.NewRepository(),
		Launcher:   launcher,
		Tmux:       tmux,
		Stats:      stats.NewCollector(statsOptions),
	}, options)
}

// agentStatsOptions builds the agent-detection half of the stats collector.
//
// A missing tmux binary disables detection rather than failing construction:
// every other op command already degrades gracefully without tmux, and agent
// classification is an enhancement to the dashboard, not a prerequisite for it.
func agentStatsOptions(cfg config.Config, tmuxConfig tmuxmanager.ManagerConfig) (stats.Options, error) {
	if !cfg.Agents.Enabled {
		return stats.Options{}, nil
	}
	detector, err := agents.New(agents.Options{
		Definitions: cfg.AgentDefinitions(),
		QuietAfter:  cfg.Agents.QuietAfter.Duration,
		IdleAfter:   cfg.Agents.IdleAfter.Duration,
		ScanLines:   cfg.Agents.ScanLines,
	})
	if err != nil {
		return stats.Options{}, err
	}
	capturer, err := tmuxmanager.NewPaneCapturer(tmuxConfig)
	if err != nil {
		return stats.Options{}, nil
	}
	return stats.Options{Detector: detector, Capturer: capturer}, nil
}

// NewWithDependencies constructs a service from narrow adapters, primarily for
// tests and alternate local integrations.
func NewWithDependencies(cfg config.Config, dependencies Dependencies, options Options) (*Service, error) {
	const op = "app.new_with_dependencies"
	if err := config.Validate(cfg); err != nil {
		return nil, err
	}
	for name, dependency := range map[string]any{
		"catalog": dependencies.Catalog, "repository": dependencies.Repository,
		"launcher": dependencies.Launcher, "tmux": dependencies.Tmux, "stats": dependencies.Stats,
	} {
		if dependency == nil {
			return nil, domain.FieldError(domain.ErrorCodeInvalidArgument, op, name, "must not be nil")
		}
	}
	commands := make(map[string]struct{}, len(cfg.CustomCommands))
	for _, command := range cfg.CustomCommands {
		commands[command.Name] = struct{}{}
	}
	openers := make(map[string]config.ProjectOpener, len(cfg.ProjectOpeners))
	for _, opener := range cfg.ProjectOpeners {
		openers[opener.ID] = opener
	}
	lockDirectory, err := operationLockDirectory(options.OperationLockDirectory)
	if err != nil {
		return nil, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "operationLockDirectory", err.Error())
	}
	return &Service{
		config: cfg, catalog: dependencies.Catalog, repository: dependencies.Repository,
		launcher: dependencies.Launcher, tmux: dependencies.Tmux, stats: dependencies.Stats,
		locks: newKeyedLocks(lockDirectory), tmuxLockKey: tmuxSessionLockKey(cfg), updateRepos: options.EnableRepositoryUpdates,
		projectOpeners: openers, customCommands: commands,
	}, nil
}

// ListProjects returns a fresh catalog snapshot enriched with repository state.
func (s *Service) ListProjects(ctx context.Context) ([]domain.Project, error) {
	const op = "app.list_projects"
	projects, err := s.catalog.List(ctx)
	if err != nil {
		return nil, typed(ctx, op, domain.ErrorCodeInternal, "list project catalog", err)
	}
	for i := range projects {
		if projects[i].Kind == domain.ProjectKindCustomEntry {
			continue
		}
		state, err := s.repository.State(ctx, projects[i].Path)
		if err != nil {
			return nil, typed(ctx, op, domain.ErrorCodeInternal, "inspect project state", err)
		}
		projects[i].GitState = state
	}
	return projects, nil
}

// CreateProject safely initializes a repository and refreshes it through the catalog.
func (s *Service) CreateProject(ctx context.Context, request domain.CreateProjectRequest) (domain.CreateProjectResult, error) {
	const op = "app.create_project"
	if err := s.validateProfile(op, request.Profile); err != nil {
		return domain.CreateProjectResult{}, err
	}
	path, err := s.catalog.CreatePath(request.Name)
	if err != nil {
		return domain.CreateProjectResult{}, typed(ctx, op, domain.ErrorCodeInvalidArgument, "validate project destination", err)
	}
	release, err := s.locks.acquire(ctx, "path:"+path)
	if err != nil {
		return domain.CreateProjectResult{}, typed(ctx, op, domain.ErrorCodeConflict, "wait for project operation", err)
	}
	defer release()
	if err := requireMissingDestination(op, path); err != nil {
		return domain.CreateProjectResult{}, err
	}
	if err := s.repository.Init(ctx, path); err != nil {
		if domain.IsCode(err, domain.ErrorCodeAlreadyExists) {
			return domain.CreateProjectResult{}, domain.ResourceError(domain.ErrorCodeConflict, op, path, "project destination already exists", err)
		}
		return domain.CreateProjectResult{}, typed(ctx, op, domain.ErrorCodeInternal, "initialize project", err)
	}
	created, err := s.catalog.ResolveByName(ctx, request.Name)
	if err != nil {
		return domain.CreateProjectResult{}, typed(ctx, op, domain.ErrorCodeInternal, "refresh created project", err)
	}
	if filepath.Clean(created.Path) != path {
		return domain.CreateProjectResult{}, domain.ResourceError(domain.ErrorCodeInternal, op, created.Path, "catalog resolved project outside its destination", nil)
	}
	result := domain.CreateProjectResult{Project: created}
	if request.OpenOnFinish {
		opened, err := s.openResolved(ctx, created, request.Profile, false, false, false)
		if err != nil {
			return domain.CreateProjectResult{}, err
		}
		result.Open = &opened
	}
	return result, nil
}

// CloneProject clones directly to its validated final destination, then refreshes the catalog.
func (s *Service) CloneProject(ctx context.Context, request domain.CloneRequest) (domain.CloneResult, error) {
	const op = "app.clone_project"
	if err := s.validateProfile(op, request.Profile); err != nil {
		return domain.CloneResult{}, err
	}
	directory := request.Directory
	if directory == "" {
		var err error
		directory, err = gitrepo.CloneDirectoryName(request.URL)
		if err != nil {
			return domain.CloneResult{}, typed(ctx, op, domain.ErrorCodeInvalidArgument, "derive clone directory", err)
		}
	}
	destination, err := s.catalog.ClonePath(directory)
	if err != nil {
		return domain.CloneResult{}, typed(ctx, op, domain.ErrorCodeInvalidArgument, "validate clone destination", err)
	}
	release, err := s.locks.acquire(ctx, "path:"+destination)
	if err != nil {
		return domain.CloneResult{}, typed(ctx, op, domain.ErrorCodeConflict, "wait for clone operation", err)
	}
	defer release()
	if _, err := s.repository.Clone(ctx, gitrepo.CloneOptions{
		URL: request.URL, ParentDirectory: s.catalog.RepositoryRoot(), Directory: directory,
	}); err != nil {
		return domain.CloneResult{}, typed(ctx, op, domain.ErrorCodeInternal, "clone project", err)
	}
	created, err := s.catalog.ResolveByName(ctx, directory)
	if err != nil {
		return domain.CloneResult{}, typed(ctx, op, domain.ErrorCodeInternal, "refresh cloned project", err)
	}
	if filepath.Clean(created.Path) != destination {
		return domain.CloneResult{}, domain.ResourceError(domain.ErrorCodeInternal, op, created.Path, "catalog resolved clone outside its destination", nil)
	}
	result := domain.CloneResult{Project: created}
	if request.OpenOnFinish {
		opened, err := s.openResolved(ctx, created, request.Profile, false, false, false)
		if err != nil {
			return domain.CloneResult{}, err
		}
		result.Open = &opened
	}
	return result, nil
}

// CreateWorktree creates a repository-root sibling and refreshes it through the catalog.
func (s *Service) CreateWorktree(ctx context.Context, request domain.CreateWorktreeRequest) (domain.CreateWorktreeResult, error) {
	const op = "app.create_worktree"
	if err := s.validateProfile(op, request.Profile); err != nil {
		return domain.CreateWorktreeResult{}, err
	}
	project, err := s.resolveProject(ctx, op, request.ProjectID)
	if err != nil {
		return domain.CreateWorktreeResult{}, err
	}
	source, err := s.catalog.ValidateRepositoryPath(project.Path)
	if err != nil || filepath.Clean(source) != filepath.Clean(project.Path) {
		if err == nil {
			err = errors.New("catalog returned a different repository path")
		}
		return domain.CreateWorktreeResult{}, typed(ctx, op, domain.ErrorCodeInvalidArgument, "validate worktree source", err)
	}
	if filepath.Dir(source) != filepath.Clean(s.catalog.RepositoryRoot()) {
		return domain.CreateWorktreeResult{}, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "projectId", "worktree source must be an immediate child of the repository root")
	}
	directory := request.Directory
	if directory == "" {
		directory = filepath.Base(project.Path) + "-" + strings.ReplaceAll(request.Branch, "/", "-")
	}
	destination, err := s.catalog.WorktreePath(directory)
	if err != nil {
		return domain.CreateWorktreeResult{}, typed(ctx, op, domain.ErrorCodeInvalidArgument, "validate worktree destination", err)
	}
	releaseDestination, err := s.locks.acquire(ctx, "path:"+destination)
	if err != nil {
		return domain.CreateWorktreeResult{}, typed(ctx, op, domain.ErrorCodeConflict, "wait for worktree destination", err)
	}
	defer releaseDestination()
	releaseProject, err := s.locks.acquire(ctx, "project:"+project.ID)
	if err != nil {
		return domain.CreateWorktreeResult{}, typed(ctx, op, domain.ErrorCodeConflict, "wait for project operation", err)
	}
	defer releaseProject()
	createdResult, err := s.repository.CreateWorktree(ctx, gitrepo.WorktreeOptions{
		Repository: source, Branch: request.Branch, Directory: directory,
	})
	if err != nil {
		return domain.CreateWorktreeResult{}, typed(ctx, op, domain.ErrorCodeInternal, "create worktree", err)
	}
	if filepath.Clean(createdResult.Path) != destination {
		return domain.CreateWorktreeResult{}, domain.ResourceError(domain.ErrorCodeInternal, op, createdResult.Path, "git created worktree outside its validated destination", nil)
	}
	created, err := s.catalog.ResolveByName(ctx, directory)
	if err != nil {
		return domain.CreateWorktreeResult{}, typed(ctx, op, domain.ErrorCodeInternal, "refresh created worktree", err)
	}
	if filepath.Clean(created.Path) != destination {
		return domain.CreateWorktreeResult{}, domain.ResourceError(domain.ErrorCodeInternal, op, created.Path, "catalog resolved worktree outside its destination", nil)
	}
	created.Kind = domain.ProjectKindWorktree
	result := domain.CreateWorktreeResult{Project: created}
	if request.OpenOnFinish {
		opened, err := s.openResolved(ctx, created, request.Profile, false, false, false)
		if err != nil {
			return domain.CreateWorktreeResult{}, err
		}
		result.Open = &opened
	}
	return result, nil
}

// OpenProject resolves the stable ID and opens or selects its managed tmux window.
func (s *Service) OpenProject(ctx context.Context, request domain.OpenProjectRequest) (domain.OpenProjectResult, error) {
	const op = "app.open_project"
	if err := s.validateProfile(op, request.Profile); err != nil {
		return domain.OpenProjectResult{}, err
	}
	project, err := s.resolveProject(ctx, op, request.ProjectID)
	if err != nil {
		return domain.OpenProjectResult{}, err
	}
	return s.openResolved(ctx, project, request.Profile, request.NewInstance, request.DeferSelection, true)
}

func (s *Service) openResolved(ctx context.Context, project domain.Project, profile string, newInstance, deferSelection, allowPull bool) (domain.OpenProjectResult, error) {
	const op = "app.open_project"
	if err := s.validateProfile(op, profile); err != nil {
		return domain.OpenProjectResult{}, err
	}
	if profile == "" {
		profile = s.config.Tmux.DefaultProfile
	}
	opener := s.projectOpeners[profile]
	if opener.Mode == domain.ProjectOpenModeGUI && newInstance {
		return domain.OpenProjectResult{}, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "newInstance", "is only supported by tmux project openers")
	}
	release, err := s.locks.acquire(ctx, "project:"+project.ID)
	if err != nil {
		return domain.OpenProjectResult{}, typed(ctx, op, domain.ErrorCodeConflict, "wait for project operation", err)
	}
	defer release()
	if allowPull && s.updateRepos && repositoryUpdatesAllowed(ctx) && project.Kind != domain.ProjectKindCustomEntry {
		state, err := s.repository.State(ctx, project.Path)
		if err != nil {
			return domain.OpenProjectResult{}, typed(ctx, op, domain.ErrorCodeInternal, "inspect project before pull", err)
		}
		project.GitState = state
		if state == domain.GitStateClean {
			if err := s.repository.Pull(ctx, project.Path); err != nil {
				return domain.OpenProjectResult{}, typed(ctx, op, domain.ErrorCodeInternal, "pull project", err)
			}
		}
	}
	if opener.Mode == domain.ProjectOpenModeGUI {
		if err := s.launcher.LaunchProjectOpener(ctx, profile, project.Path); err != nil {
			return domain.OpenProjectResult{}, typed(ctx, op, domain.ErrorCodeInternal, "launch GUI project opener", err)
		}
		return domain.OpenProjectResult{Project: project, Profile: profile, Mode: opener.Mode}, nil
	}
	editorCommand := process.Substitute(opener.Command, project.Path, s.config.RootDirectory)
	// Keep the session lock innermost so every path/project operation uses the
	// same lock order, and hold it through rollback inside OpenProjectWindow.
	releaseTmux, err := s.acquireTmuxMutation(ctx, op)
	if err != nil {
		return domain.OpenProjectResult{}, err
	}
	defer releaseTmux()
	if _, err := s.tmux.EnsureMainSession(ctx); err != nil {
		return domain.OpenProjectResult{}, typed(ctx, op, domain.ErrorCodeDependency, "ensure tmux session", err)
	}
	result, err := s.tmux.OpenProjectWindow(ctx, tmuxmanager.OpenProjectWindowRequest{
		Project: project, Profile: profile, EditorCommand: editorCommand,
		ShellCommand: s.config.PreferredShell, NewInstance: newInstance, DeferSelection: deferSelection,
	})
	if err != nil {
		return domain.OpenProjectResult{}, typed(ctx, op, domain.ErrorCodeDependency, "open tmux project window", err)
	}
	result.Profile = profile
	result.Mode = opener.Mode
	return result, nil
}

// RunProjectAction launches a built-in or configured local action.
func (s *Service) RunProjectAction(ctx context.Context, request domain.RunProjectActionRequest) (domain.RunProjectActionResult, error) {
	const op = "app.run_project_action"
	project, err := s.resolveProject(ctx, op, request.ProjectID)
	if err != nil {
		return domain.RunProjectActionResult{}, err
	}
	if request.Action == "" {
		return domain.RunProjectActionResult{}, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "action", "must not be empty")
	}
	switch action.Classify(request.Action) {
	case action.Nvim:
		err = s.launcher.LaunchNvim(ctx, project.Path)
	case action.Code:
		if !s.config.Actions.GUIEditors {
			return domain.RunProjectActionResult{}, domain.NewError(domain.ErrorCodeForbidden, op, "VS Code actions are disabled", nil)
		}
		err = s.launcher.LaunchCode(ctx, project.Path)
	case action.Shell:
		err = s.launcher.LaunchPreferredShell(ctx, project.Path)
	case action.Worktree:
		branch, found := action.WorktreeBranch(request.Action)
		if !found {
			return domain.RunProjectActionResult{}, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "action", "worktree action requires a branch (worktree:<branch>)")
		}
		if strings.TrimSpace(branch) == "" {
			return domain.RunProjectActionResult{}, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "action", "worktree branch must not be empty")
		}
		_, err = s.CreateWorktree(ctx, domain.CreateWorktreeRequest{ProjectID: project.ID, Branch: branch})
	case action.Custom:
		if _, configured := s.customCommands[request.Action]; configured {
			err = s.launcher.RunCustom(ctx, request.Action, project.Path)
		} else {
			return domain.RunProjectActionResult{}, domain.ResourceError(domain.ErrorCodeNotFound, op, request.Action, "project action is not configured", nil)
		}
	}
	if err != nil {
		return domain.RunProjectActionResult{}, typed(ctx, op, domain.ErrorCodeInternal, "run project action", err)
	}
	return domain.RunProjectActionResult{Project: project, Action: request.Action, Started: true}, nil
}

// EnsureMainSession delegates dashboard reconciliation to the tmux manager.
func (s *Service) EnsureMainSession(ctx context.Context) (domain.EnsureMainSessionResult, error) {
	release, err := s.acquireTmuxMutation(ctx, "app.ensure_main_session")
	if err != nil {
		return domain.EnsureMainSessionResult{}, err
	}
	defer release()
	result, err := s.tmux.EnsureMainSession(ctx)
	if err != nil {
		return domain.EnsureMainSessionResult{}, typed(ctx, "app.ensure_main_session", domain.ErrorCodeDependency, "ensure tmux session", err)
	}
	return result, nil
}

// AttachOrSwitch attaches outside tmux and switches the current client inside
// tmux. The tmux manager owns the environment-sensitive behavior.
func (s *Service) AttachOrSwitch(ctx context.Context) error {
	return s.AttachOrSwitchTo(ctx, "")
}

// AttachOrSwitchTo attaches or switches the invoking client to a window in the
// managed session. An empty window ID targets the dashboard.
func (s *Service) AttachOrSwitchTo(ctx context.Context, windowID string) error {
	release, err := s.acquireTmuxMutation(ctx, "app.attach_or_switch")
	if err != nil {
		return err
	}
	locked := true
	defer func() {
		if locked {
			release()
		}
	}()
	plan, err := s.tmux.PrepareAttachOrSwitchTo(ctx, windowID)
	if err != nil {
		return typed(ctx, "app.attach_or_switch", domain.ErrorCodeDependency, "prepare tmux attach or switch", err)
	}
	if !plan.RequiresSessionLock() {
		release()
		locked = false
	}
	if err := s.tmux.ExecuteAttachOrSwitch(ctx, plan); err != nil {
		return typed(ctx, "app.attach_or_switch", domain.ErrorCodeDependency, "attach or switch tmux session", err)
	}
	return nil
}

// GetTmuxSnapshot delegates immutable tmux state collection.
func (s *Service) GetTmuxSnapshot(ctx context.Context) (domain.TmuxSnapshot, error) {
	result, err := s.tmux.Snapshot(ctx)
	if err != nil {
		return domain.TmuxSnapshot{}, typed(ctx, "app.get_tmux_snapshot", domain.ErrorCodeDependency, "collect tmux snapshot", err)
	}
	return result, nil
}

// GetStatsSnapshot composes the current tmux snapshot with the stats collector.
func (s *Service) GetStatsSnapshot(ctx context.Context) (domain.StatsSnapshot, error) {
	tmuxSnapshot, err := s.GetTmuxSnapshot(ctx)
	if err != nil {
		return domain.StatsSnapshot{}, err
	}
	result, err := s.stats.Collect(ctx, tmuxSnapshot)
	if err != nil {
		return domain.StatsSnapshot{}, typed(ctx, "app.get_stats_snapshot", domain.ErrorCodeInternal, "collect statistics", err)
	}
	return result, nil
}

// ResolveCurrentProject supports CLI targeting by checking the tmux project tag
// first and falling back to an exact current-window name.
func (s *Service) ResolveCurrentProject(ctx context.Context) (domain.Project, bool, error) {
	const op = "app.resolve_current_project"
	id, found, err := s.tmux.CurrentProjectID(ctx)
	if err != nil {
		return domain.Project{}, false, typed(ctx, op, domain.ErrorCodeDependency, "read current project tag", err)
	}
	if found {
		project, err := s.catalog.ResolveByID(ctx, id)
		if err == nil {
			return project, true, nil
		}
		if !domain.IsCode(err, domain.ErrorCodeNotFound) {
			return domain.Project{}, false, typed(ctx, op, domain.ErrorCodeInternal, "resolve tagged project", err)
		}
	}
	name, found, err := s.tmux.CurrentProjectName(ctx)
	if err != nil {
		return domain.Project{}, false, typed(ctx, op, domain.ErrorCodeDependency, "read current window name", err)
	}
	if !found {
		return domain.Project{}, false, nil
	}
	project, err := s.catalog.ResolveByName(ctx, name)
	if err != nil {
		if domain.IsCode(err, domain.ErrorCodeNotFound) {
			return domain.Project{}, false, nil
		}
		return domain.Project{}, false, typed(ctx, op, domain.ErrorCodeInternal, "resolve current window name", err)
	}
	return project, true, nil
}

type repositoryUpdatePolicyKey struct{}

// WithRepositoryUpdates sets request-local permission for repository pulls.
// It cannot enable updates when Service.Options disabled them globally.
func WithRepositoryUpdates(ctx context.Context, allowed bool) context.Context {
	return context.WithValue(ctx, repositoryUpdatePolicyKey{}, allowed)
}

// WithoutRepositoryUpdates is the CLI-friendly equivalent of --no-repo-update.
func WithoutRepositoryUpdates(ctx context.Context) context.Context {
	return WithRepositoryUpdates(ctx, false)
}

func repositoryUpdatesAllowed(ctx context.Context) bool {
	allowed, set := ctx.Value(repositoryUpdatePolicyKey{}).(bool)
	return !set || allowed
}

func (s *Service) acquireTmuxMutation(ctx context.Context, op string) (func(), error) {
	release, err := s.locks.acquire(ctx, s.tmuxLockKey)
	if err != nil {
		return nil, typed(ctx, op, domain.ErrorCodeConflict, "wait for tmux session mutation", err)
	}
	return release, nil
}

func tmuxSessionLockKey(cfg config.Config) string {
	return "tmux-session:" + cfg.Tmux.Socket + "\x00" + cfg.Tmux.Session
}

func (s *Service) resolveProject(ctx context.Context, op, id string) (domain.Project, error) {
	if strings.TrimSpace(id) == "" {
		return domain.Project{}, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "projectId", "must not be empty")
	}
	project, err := s.catalog.ResolveByID(ctx, id)
	if err != nil {
		return domain.Project{}, typed(ctx, op, domain.ErrorCodeInternal, "resolve project ID", err)
	}
	return project, nil
}

func (s *Service) validateProfile(op, profile string) error {
	if profile == "" {
		return nil
	}
	if _, found := s.projectOpeners[profile]; !found {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, "profile", "must match a configured project opener ID")
	}
	return nil
}

func projectOpener(cfg config.Config, id string) (config.ProjectOpener, bool) {
	for _, opener := range cfg.ProjectOpeners {
		if opener.ID == id {
			return opener, true
		}
	}
	return config.ProjectOpener{}, false
}

func requireMissingDestination(op, path string) error {
	_, err := os.Lstat(path)
	if err == nil {
		return domain.ResourceError(domain.ErrorCodeConflict, op, path, "project destination already exists", nil)
	}
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return domain.ResourceError(domain.ErrorCodeInternal, op, path, "inspect project destination", err)
}

func typed(ctx context.Context, op string, fallback domain.ErrorCode, message string, err error) error {
	if err == nil {
		return nil
	}
	var existing *domain.Error
	if errors.As(err, &existing) {
		return err
	}
	if ctx != nil && ctx.Err() != nil {
		return domain.NewError(domain.CodeOf(ctx.Err()), op, message, ctx.Err())
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return domain.NewError(domain.CodeOf(err), op, message, err)
	}
	return domain.NewError(fallback, op, message, err)
}

type keyedLocks struct {
	mu        sync.Mutex
	locks     map[string]*keyLock
	directory string
}

type keyLock struct {
	token chan struct{}
	refs  int
}

func newKeyedLocks(directory string) *keyedLocks {
	return &keyedLocks{locks: make(map[string]*keyLock), directory: directory}
}

func (l *keyedLocks) acquire(ctx context.Context, key string) (func(), error) {
	l.mu.Lock()
	lock := l.locks[key]
	if lock == nil {
		lock = &keyLock{token: make(chan struct{}, 1)}
		lock.token <- struct{}{}
		l.locks[key] = lock
	}
	lock.refs++
	l.mu.Unlock()

	select {
	case <-lock.token:
		if err := os.MkdirAll(l.directory, 0o700); err != nil {
			lock.token <- struct{}{}
			l.releaseRef(key, lock)
			return nil, domain.ResourceError(domain.ErrorCodeInternal, "app.operation_lock", l.directory, "create operation lock directory", err)
		}
		digest := sha256.Sum256([]byte(key))
		path := filepath.Join(l.directory, hex.EncodeToString(digest[:])+".lock")
		fileLock, err := acquirePlatformLock(ctx, path)
		if err != nil {
			lock.token <- struct{}{}
			l.releaseRef(key, lock)
			return nil, err
		}
		return func() {
			fileLock.release()
			lock.token <- struct{}{}
			l.releaseRef(key, lock)
		}, nil
	case <-ctx.Done():
		l.releaseRef(key, lock)
		return nil, ctx.Err()
	}
}

func operationLockDirectory(configured string) (string, error) {
	if configured != "" {
		if !filepath.IsAbs(configured) || filepath.Clean(configured) != configured {
			return "", errors.New("must be an absolute normalized path")
		}
		return configured, nil
	}
	stateRoot := os.Getenv("XDG_STATE_HOME")
	if stateRoot == "" {
		userConfig, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(userConfig, "op", "state", "locks"), nil
	} else if !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot {
		return "", errors.New("XDG_STATE_HOME must be an absolute normalized path")
	}
	return filepath.Join(stateRoot, "op", "locks"), nil
}

type lazyTmux struct {
	mu       sync.Mutex
	factory  func(context.Context) (TmuxManager, error)
	snapshot func(context.Context) (domain.TmuxSnapshot, error)
	manager  TmuxManager
}

func newLazyTmux(factory func(context.Context) (TmuxManager, error), snapshot func(context.Context) (domain.TmuxSnapshot, error)) *lazyTmux {
	return &lazyTmux{factory: factory, snapshot: snapshot}
}

func (l *lazyTmux) get(ctx context.Context) (TmuxManager, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.manager != nil {
		return l.manager, nil
	}
	manager, err := l.factory(ctx)
	if err != nil {
		return nil, err
	}
	l.manager = manager
	return manager, nil
}

func (l *lazyTmux) EnsureMainSession(ctx context.Context) (domain.EnsureMainSessionResult, error) {
	manager, err := l.get(ctx)
	if err != nil {
		return domain.EnsureMainSessionResult{}, err
	}
	return manager.EnsureMainSession(ctx)
}

func (l *lazyTmux) PrepareAttachOrSwitch(ctx context.Context) (tmuxmanager.AttachPlan, error) {
	manager, err := l.get(ctx)
	if err != nil {
		return tmuxmanager.AttachPlan{}, err
	}
	return manager.PrepareAttachOrSwitch(ctx)
}

func (l *lazyTmux) PrepareAttachOrSwitchTo(ctx context.Context, windowID string) (tmuxmanager.AttachPlan, error) {
	manager, err := l.get(ctx)
	if err != nil {
		return tmuxmanager.AttachPlan{}, err
	}
	return manager.PrepareAttachOrSwitchTo(ctx, windowID)
}

func (l *lazyTmux) ExecuteAttachOrSwitch(ctx context.Context, plan tmuxmanager.AttachPlan) error {
	manager, err := l.get(ctx)
	if err != nil {
		return err
	}
	return manager.ExecuteAttachOrSwitch(ctx, plan)
}

func (l *lazyTmux) OpenProjectWindow(ctx context.Context, request tmuxmanager.OpenProjectWindowRequest) (domain.OpenProjectResult, error) {
	manager, err := l.get(ctx)
	if err != nil {
		return domain.OpenProjectResult{}, err
	}
	return manager.OpenProjectWindow(ctx, request)
}

func (l *lazyTmux) Snapshot(ctx context.Context) (domain.TmuxSnapshot, error) {
	l.mu.Lock()
	manager := l.manager
	l.mu.Unlock()
	if manager != nil {
		return manager.Snapshot(ctx)
	}
	return l.snapshot(ctx)
}

func (l *lazyTmux) CurrentProjectID(ctx context.Context) (string, bool, error) {
	manager, err := l.get(ctx)
	if err != nil {
		return "", false, err
	}
	return manager.CurrentProjectID(ctx)
}

func (l *lazyTmux) CurrentProjectName(ctx context.Context) (string, bool, error) {
	manager, err := l.get(ctx)
	if err != nil {
		return "", false, err
	}
	return manager.CurrentProjectName(ctx)
}

func (l *keyedLocks) releaseRef(key string, lock *keyLock) {
	l.mu.Lock()
	lock.refs--
	if lock.refs == 0 {
		delete(l.locks, key)
	}
	l.mu.Unlock()
}
