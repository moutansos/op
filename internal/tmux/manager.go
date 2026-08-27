package tmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/moutansos/op/internal/domain"
)

const (
	optionRole            = "@op-role"
	roleDashboard         = "dashboard"
	optionProjectID       = "@op-project-id"
	optionPath            = "@op-project-path"
	optionProfile         = "@op-profile"
	optionOwner           = "@op-owned"
	optionDashboardPane   = "@op-dashboard-pane"
	optionDashboardPID    = "@op-dashboard-pid"
	defaultCleanupTimeout = 2 * time.Second
)

// ManagerConfig contains only values needed to orchestrate tmux.
type ManagerConfig struct {
	Session          string
	DashboardWindow  string
	Socket           string
	StartDirectory   string
	DashboardCommand string
	TreeCommand      string
	EditorCommand    string
	PreferredShell   string
	ShellPaneRows    int
	DefaultProfile   string
	Output           io.Writer
	Error            io.Writer
}

// OpenProjectWindowRequest contains a resolved project. Callers must resolve a
// stable project ID through the catalog before invoking the manager.
type OpenProjectWindowRequest struct {
	Project        domain.Project
	Profile        string
	EditorCommand  string
	ShellCommand   string
	NewInstance    bool
	DeferSelection bool
}

type Manager struct {
	config                ManagerConfig
	client                tmuxClient
	bootstrapped          bool
	dashboardPaneCommand  string
	dashboardProcessAlive func(context.Context, int, string) (bool, error)
	lookupEnv             func(string) string
	now                   func() time.Time
	startupWait           time.Duration
	startupPoll           time.Duration
	startupStable         time.Duration
	cleanupTimeout        time.Duration
}

// New initializes the tmux adapter. A configured socket is bootstrapped before
// the first state query so callers always receive a usable mutating client.
func New(ctx context.Context, config ManagerConfig) (*Manager, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	client, bootstrapped, err := newCommandClient(ctx, config)
	if err != nil {
		return nil, err
	}
	return newManager(config, client, bootstrapped), nil
}

// ReadSnapshot probes and reads a managed session without starting a tmux
// server or creating a session when either is absent.
func ReadSnapshot(ctx context.Context, config ManagerConfig) (domain.TmuxSnapshot, error) {
	if err := validateConfig(config); err != nil {
		return domain.TmuxSnapshot{}, err
	}
	client, found, err := newReadOnlyCommandClient(ctx, config)
	if err != nil {
		return domain.TmuxSnapshot{}, err
	}
	if !found {
		return domain.TmuxSnapshot{CapturedAt: time.Now()}, nil
	}
	return newManager(config, client, false).Snapshot(ctx)
}

func newManager(config ManagerConfig, client tmuxClient, bootstrapped bool) *Manager {
	if config.Output == nil {
		config.Output = os.Stdout
	}
	if config.Error == nil {
		config.Error = os.Stderr
	}
	if config.TreeCommand == "" {
		config.TreeCommand = "op tree"
	}
	dashboardPaneCommand, _ := buildPersistentShellCommand(config.PreferredShell, config.DashboardCommand)
	return &Manager{
		config:                config,
		client:                client,
		bootstrapped:          bootstrapped,
		dashboardPaneCommand:  dashboardPaneCommand,
		dashboardProcessAlive: processTreeContainsCommand,
		lookupEnv:             os.Getenv,
		now:                   time.Now,
		startupWait:           2 * time.Second,
		startupPoll:           20 * time.Millisecond,
		startupStable:         75 * time.Millisecond,
		cleanupTimeout:        defaultCleanupTimeout,
	}
}

func validateConfig(config ManagerConfig) error {
	const op = "tmux.new"
	if err := validateTmuxName(op, "session", config.Session); err != nil {
		return err
	}
	if err := validateTmuxName(op, "dashboardWindow", config.DashboardWindow); err != nil {
		return err
	}
	for field, value := range map[string]string{
		"socket":           config.Socket,
		"startDirectory":   config.StartDirectory,
		"dashboardCommand": config.DashboardCommand,
		"editorCommand":    config.EditorCommand,
		"preferredShell":   config.PreferredShell,
		"defaultProfile":   config.DefaultProfile,
	} {
		if err := validateGotmuxValue(op, field, value); err != nil {
			return err
		}
	}
	if strings.TrimSpace(config.DashboardCommand) == "" {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, "dashboardCommand", "must not be empty")
	}
	if strings.TrimSpace(config.EditorCommand) == "" {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, "editorCommand", "must not be empty")
	}
	if strings.TrimSpace(config.PreferredShell) == "" {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, "preferredShell", "must not be empty")
	}
	if strings.TrimSpace(config.DefaultProfile) == "" {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, "defaultProfile", "must not be empty")
	}
	if config.ShellPaneRows <= 0 {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, "shellPaneRows", "must be greater than zero")
	}
	if _, err := buildPersistentShellCommand(config.PreferredShell, config.DashboardCommand); err != nil {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, "preferredShell", err.Error())
	}
	return nil
}

// EnsureMainSession creates or reconciles the dashboard without deleting user
// windows. All successful mutations are followed by a fresh state query.
func (m *Manager) EnsureMainSession(ctx context.Context) (result domain.EnsureMainSessionResult, err error) {
	defer m.recoverError("tmux.ensure_main_session", &err)
	if err = contextError(ctx, "tmux.ensure_main_session"); err != nil {
		return result, err
	}

	session, err := m.client.Session(ctx, m.config.Session)
	if err != nil {
		return result, m.failure("tmux.ensure_main_session", "query session", err)
	}
	created := m.bootstrapped
	m.bootstrapped = false
	if session == nil {
		if err := m.client.CreateSession(ctx, m.config.Session, m.config.StartDirectory, m.dashboardPaneCommand); err != nil {
			return result, m.failure("tmux.ensure_main_session", "create session", err)
		}
		created = true
		session, err = m.client.Session(ctx, m.config.Session)
		if err != nil || session == nil {
			return result, m.verification("tmux.ensure_main_session", "session creation was not observable", err)
		}
	}
	if err := m.enableFocusEvents(ctx); err != nil {
		return result, err
	}
	if err := m.ensureTreeBinding(ctx); err != nil {
		return result, err
	}

	windows, err := m.taggedWindows(ctx)
	if err != nil {
		return result, err
	}
	dashboard, _, err := m.findDashboard(ctx, windows)
	if err != nil {
		return result, err
	}
	repaired := false
	startDashboardInCaller := false
	dashboardCreated := false
	if dashboard == nil {
		if created && len(windows) > 0 {
			dashboard = &windows[0]
		} else {
			dashboard, err = m.createDashboardWindow(ctx)
			if err != nil {
				return result, err
			}
		}
		dashboardCreated = true
		repaired = true
	}
	rollbackDashboard := dashboardCreated
	rollbackPaneID := ""
	defer func() {
		if rollbackPaneID == "" && !rollbackDashboard {
			return
		}
		var cleanupErr error
		cleanupCtx, cancelCleanup := m.cleanupContext(ctx)
		defer cancelCleanup()
		if rollbackPaneID != "" {
			cleanupErr = m.killAndVerifyPane(cleanupCtx, "tmux.ensure_main_session", rollbackPaneID)
		}
		if rollbackDashboard {
			windowErr := m.rollbackWindow(cleanupCtx, dashboard.ID)
			if windowErr != nil {
				windowErr = m.failure("tmux.ensure_main_session", "rollback unstarted dashboard window", windowErr)
				cleanupErr = errors.Join(cleanupErr, windowErr)
			}
		}
		if cleanupErr != nil {
			if err == nil {
				err = cleanupErr
			} else {
				err = errors.Join(err, cleanupErr)
			}
		}
	}()

	panes, err := m.client.ListPanes(ctx, dashboard.ID)
	if err != nil {
		return result, m.failure("tmux.ensure_main_session", "list dashboard panes", err)
	}
	if dashboardCreated && len(panes) != 1 {
		return result, m.verification("tmux.ensure_main_session", "new dashboard did not have one pane", nil)
	}

	managedPaneID, paneIDExists, err := m.client.WindowOption(ctx, dashboard.ID, optionDashboardPane)
	if err != nil {
		return result, m.failure("tmux.ensure_main_session", "read managed dashboard pane", err)
	}
	managedPaneID = trimLineEndings(managedPaneID)
	managedPIDValue, pidExists, err := m.client.WindowOption(ctx, dashboard.ID, optionDashboardPID)
	if err != nil {
		return result, m.failure("tmux.ensure_main_session", "read managed dashboard pid", err)
	}
	managedPID, parseErr := strconv.ParseInt(trimLineEndings(managedPIDValue), 10, 32)
	trackingComplete := paneIDExists && pidExists && managedPaneID != "" && parseErr == nil && managedPID > 0

	var dashboardPane *paneState
	startDashboard := dashboardCreated
	if trackingComplete {
		needsRespawn := false
		for i := range panes {
			if panes[i].ID == managedPaneID {
				dashboardPane = &panes[i]
				break
			}
		}
		if dashboardPane != nil && !dashboardPane.Dead && int64(dashboardPane.PID) == managedPID {
			running, processErr := m.dashboardProcessAlive(ctx, int(dashboardPane.PID), m.config.DashboardCommand)
			if processErr != nil {
				return result, m.failure("tmux.ensure_main_session", "inspect dashboard process", processErr)
			}
			needsRespawn = !running
		} else if dashboardPane != nil {
			needsRespawn = true
		}
		if dashboardPane != nil && needsRespawn {
			callerPaneID, insideTmux, callerErr := m.callerPane("tmux.ensure_main_session")
			if callerErr == nil && insideTmux && callerPaneID == dashboardPane.ID && !dashboardPane.Dead {
				startDashboardInCaller = true
				repaired = true
			} else {
				wasLive := !dashboardPane.Dead
				preRespawnPID := dashboardPane.PID
				if err := m.client.RespawnPane(ctx, dashboardPane.ID, m.dashboardPaneCommand); err != nil {
					return result, m.failure("tmux.ensure_main_session", "respawn dashboard pane", err)
				}
				verified, err := m.paneByID(ctx, dashboard.ID, dashboardPane.ID)
				if err != nil || verified == nil || verified.Dead || verified.PID <= 0 || (wasLive && verified.PID == preRespawnPID) {
					return result, m.verification("tmux.ensure_main_session", "dashboard respawn was not observable", err)
				}
				dashboardPane = verified
				if err := m.verifyPaneCommand(ctx, dashboard.ID, dashboardPane.ID, m.config.DashboardCommand); err != nil {
					return result, err
				}
				startDashboard = false
				repaired = true
			}
		}
	} else if dashboardCreated {
		dashboardPane = &panes[0]
	} else {
		var candidates []*paneState
		for i := range panes {
			if panes[i].Dead || panes[i].PID <= 0 {
				continue
			}
			running, processErr := m.dashboardProcessAlive(ctx, int(panes[i].PID), m.config.DashboardCommand)
			if processErr != nil {
				return result, m.failure("tmux.ensure_main_session", "inspect untracked dashboard process", processErr)
			}
			if running {
				candidates = append(candidates, &panes[i])
			}
		}
		if len(candidates) == 0 && len(panes) == 1 {
			callerPaneID, insideTmux, callerErr := m.callerPane("tmux.ensure_main_session")
			if callerErr == nil && insideTmux && callerPaneID == panes[0].ID {
				dashboardPane = &panes[0]
				startDashboardInCaller = true
				repaired = true
			}
		}
		if dashboardPane == nil {
			if len(candidates) != 1 {
				return result, m.verification("tmux.ensure_main_session", "dashboard pane identity is ambiguous; refusing to replace an untracked pane", nil)
			}
			dashboardPane = candidates[0]
			repaired = true
		}
	}
	if trackingComplete && dashboardPane == nil {
		before := panes
		if len(panes) == 0 {
			return result, m.verification("tmux.ensure_main_session", "dashboard window has no pane available for repair", nil)
		}
		if err := m.client.SplitPane(ctx, panes[0].ID, m.config.StartDirectory, m.dashboardPaneCommand); err != nil {
			return result, m.failure("tmux.ensure_main_session", "recreate managed dashboard pane", err)
		}
		panes, err = m.client.ListPanes(ctx, dashboard.ID)
		if err != nil {
			return result, m.failure("tmux.ensure_main_session", "verify recreated dashboard pane", err)
		}
		dashboardPane = newPane(before, panes)
		if dashboardPane == nil || dashboardPane.Dead {
			return result, m.verification("tmux.ensure_main_session", "managed dashboard pane recreation was not observable", nil)
		}
		rollbackPaneID = dashboardPane.ID
		if err := m.verifyPaneCommand(ctx, dashboard.ID, dashboardPane.ID, m.config.DashboardCommand); err != nil {
			return result, err
		}
		startDashboard = false
		repaired = true
	}
	if startDashboard {
		if err := m.verifyPaneCommand(ctx, dashboard.ID, dashboardPane.ID, m.config.DashboardCommand); err != nil {
			return result, err
		}
	}
	dashboardPane, err = m.paneByID(ctx, dashboard.ID, dashboardPane.ID)
	if err != nil || dashboardPane == nil || dashboardPane.Dead || dashboardPane.PID <= 0 {
		return result, m.verification("tmux.ensure_main_session", "verified dashboard pane identity was not observable", err)
	}
	if !trackingComplete || managedPaneID != dashboardPane.ID || managedPID != int64(dashboardPane.PID) {
		if err := m.setAndVerifyWindowOption(ctx, dashboard.ID, optionDashboardPID, strconv.FormatInt(int64(dashboardPane.PID), 10)); err != nil {
			return result, err
		}
		if err := m.setAndVerifyWindowOption(ctx, dashboard.ID, optionDashboardPane, dashboardPane.ID); err != nil {
			return result, err
		}
		repaired = true
	}
	rollbackPaneID = ""
	rollbackDashboard = false

	if dashboard.Name != m.config.DashboardWindow {
		if err := m.client.RenameWindow(ctx, dashboard.ID, m.config.DashboardWindow); err != nil {
			return result, m.failure("tmux.ensure_main_session", "rename dashboard window", err)
		}
		verified, err := m.windowByID(ctx, dashboard.ID)
		if err != nil || verified == nil || verified.Name != m.config.DashboardWindow {
			return result, m.verification("tmux.ensure_main_session", "dashboard rename was not observable", err)
		}
		dashboard.Name = verified.Name
		repaired = true
	}
	if dashboard.Role != roleDashboard {
		if err := m.setAndVerifyWindowOption(ctx, dashboard.ID, optionRole, roleDashboard); err != nil {
			return result, err
		}
		repaired = true
	}
	if dashboard.Owner != "1" {
		if err := m.setAndVerifyWindowOption(ctx, dashboard.ID, optionOwner, "1"); err != nil {
			return result, err
		}
		repaired = true
	}
	if err := m.positionDashboard(ctx, dashboard.ID); err != nil {
		return result, err
	}

	snapshot, err := m.Snapshot(ctx)
	if err != nil {
		return result, err
	}
	if snapshot.Session == nil {
		return result, m.verification("tmux.ensure_main_session", "session disappeared after reconciliation", nil)
	}
	result = domain.EnsureMainSessionResult{
		Session: *snapshot.Session, Created: created, Repaired: repaired, StartDashboard: startDashboardInCaller,
	}
	return result, nil
}

// OpenProjectWindow selects an existing tagged window unless NewInstance is
// requested. Only a window created by this call is rolled back on failure.
func (m *Manager) OpenProjectWindow(ctx context.Context, request OpenProjectWindowRequest) (result domain.OpenProjectResult, err error) {
	const op = "tmux.open_project_window"
	defer m.recoverError(op, &err)
	if err = contextError(ctx, op); err != nil {
		return result, err
	}
	if err := validateProjectRequest(request); err != nil {
		return result, err
	}

	profile := request.Profile
	if profile == "" {
		profile = m.config.DefaultProfile
	}
	editorCommand := request.EditorCommand
	if editorCommand == "" {
		editorCommand = m.config.EditorCommand
	}
	shellCommand := request.ShellCommand
	if shellCommand == "" {
		shellCommand = m.config.PreferredShell
	}
	for field, value := range map[string]string{"profile": profile, "editorCommand": editorCommand, "shellCommand": shellCommand} {
		if err := validateGotmuxValue(op, field, value); err != nil {
			return result, err
		}
	}
	editorPaneCommand, err := buildEditorPaneCommand(m.config.PreferredShell, editorCommand)
	if err != nil {
		return result, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "preferredShell", err.Error())
	}

	if session, queryErr := m.client.Session(ctx, m.config.Session); queryErr != nil {
		return result, m.failure(op, "query session", queryErr)
	} else if session == nil {
		return result, domain.ResourceError(domain.ErrorCodeNotFound, op, m.config.Session, "managed session does not exist", nil)
	}

	windows, err := m.taggedWindows(ctx)
	if err != nil {
		return result, err
	}
	if !request.NewInstance {
		var unhealthyOwned []string
		for _, window := range windows {
			if window.ProjectID == request.Project.ID && (window.Profile == profile || window.Profile == "" && profile == m.config.DefaultProfile) {
				healthy, healthErr := m.projectWindowHealthy(ctx, window.ID)
				if healthErr != nil {
					return result, healthErr
				}
				if healthy {
					if request.DeferSelection {
						window, mapErr := m.snapshotWindow(ctx, window.ID)
						if mapErr != nil {
							return result, mapErr
						}
						return domain.OpenProjectResult{Project: request.Project, Profile: profile, Mode: domain.ProjectOpenModeTmux, Window: window, Reused: true}, nil
					}
					selected, selectErr := m.selectWindow(ctx, window.ID)
					if selectErr != nil {
						return result, selectErr
					}
					return domain.OpenProjectResult{Project: request.Project, Profile: profile, Mode: domain.ProjectOpenModeTmux, Window: selected, Reused: true}, nil
				}
				if window.Owner == "1" {
					unhealthyOwned = append(unhealthyOwned, window.ID)
				}
			}
		}
		for _, windowID := range unhealthyOwned {
			if err := m.killAndVerifyWindow(ctx, op, windowID); err != nil {
				return result, err
			}
		}
		if len(unhealthyOwned) > 0 {
			windows, err = m.taggedWindows(ctx)
			if err != nil {
				return result, err
			}
		}
	}

	baseName, err := normalizeProjectName(op, request.Project.Name)
	if err != nil {
		return result, err
	}
	windowName, err := collisionName(baseName, request.Project.ID, windows)
	if err != nil {
		return result, err
	}
	if request.NewInstance {
		windowName, err = instanceName(windowName, windows)
		if err != nil {
			return result, err
		}
	}

	createdID, err := m.createWindow(ctx, op, "create project window", windowName, request.Project.Path, editorPaneCommand)
	if err != nil {
		return result, err
	}
	rollback := true
	defer func() {
		if rollback {
			cleanupCtx, cancelCleanup := m.cleanupContext(ctx)
			defer cancelCleanup()
			if rollbackErr := m.rollbackWindow(cleanupCtx, createdID); rollbackErr != nil {
				rollbackFailure := m.failure(op, "rollback created project window", rollbackErr)
				if err == nil {
					err = rollbackFailure
				} else {
					err = errors.Join(err, rollbackFailure)
				}
			}
		}
	}()
	_, editorPane, err := m.verifyCreatedWindow(ctx, op, createdID, windowName, request.Project.Path)
	if err != nil {
		return result, err
	}
	if err := m.verifyPaneCommands(ctx, createdID, editorPane.ID, editorCommand, m.config.PreferredShell); err != nil {
		return result, err
	}

	splitShell := shellCommand
	if err := m.client.SplitPane(ctx, editorPane.ID, request.Project.Path, splitShell); err != nil {
		return result, m.failure(op, "split shell pane", err)
	}
	afterSplit, err := m.client.ListPanes(ctx, createdID)
	if err != nil {
		return result, m.failure(op, "verify shell pane split", err)
	}
	shellPane := newPane([]paneState{*editorPane}, afterSplit)
	if shellPane == nil || shellPane.Dead || len(afterSplit) != 2 {
		return result, m.verification(op, "shell pane split was not observable", nil)
	}
	if err := m.verifyPaneCommand(ctx, createdID, shellPane.ID, shellCommand); err != nil {
		return result, err
	}
	if err := m.client.ResizePane(ctx, shellPane.ID, m.config.ShellPaneRows); err != nil {
		return result, m.failure(op, "resize shell pane", err)
	}
	resized, err := m.paneByID(ctx, createdID, shellPane.ID)
	if err != nil || resized == nil || resized.Dead || resized.Height != m.config.ShellPaneRows {
		return result, m.verification(op, "shell pane resize was not observable", err)
	}
	if err := m.client.SelectPane(ctx, editorPane.ID); err != nil {
		return result, m.failure(op, "reselect editor pane", err)
	}
	selectedPane, err := m.paneByID(ctx, createdID, editorPane.ID)
	if err != nil || selectedPane == nil || selectedPane.Dead || !selectedPane.Active {
		return result, m.verification(op, "editor pane selection was not observable", err)
	}

	for key, value := range map[string]string{
		optionProjectID: request.Project.ID,
		optionPath:      request.Project.Path,
		optionProfile:   profile,
		optionOwner:     "1",
	} {
		if err := m.setAndVerifyWindowOption(ctx, createdID, key, value); err != nil {
			return result, err
		}
	}
	var selected domain.TmuxWindow
	if request.DeferSelection {
		selected, err = m.snapshotWindow(ctx, createdID)
	} else {
		selected, err = m.selectWindow(ctx, createdID)
	}
	if err != nil {
		return result, err
	}
	rollback = false
	return domain.OpenProjectResult{Project: request.Project, Profile: profile, Mode: domain.ProjectOpenModeTmux, Window: selected, Reused: false}, nil
}

func (m *Manager) snapshotWindow(ctx context.Context, windowID string) (domain.TmuxWindow, error) {
	snapshot, err := m.Snapshot(ctx)
	if err != nil {
		return domain.TmuxWindow{}, err
	}
	if snapshot.Session != nil {
		for _, window := range snapshot.Session.Windows {
			if window.ID == windowID {
				return window, nil
			}
		}
	}
	return domain.TmuxWindow{}, m.verification("tmux.open_project_window", "project window disappeared", nil)
}

func validateProjectRequest(request OpenProjectWindowRequest) error {
	const op = "tmux.open_project_window"
	for field, value := range map[string]string{
		"project.id":   request.Project.ID,
		"project.path": request.Project.Path,
	} {
		if strings.TrimSpace(value) == "" {
			return domain.FieldError(domain.ErrorCodeInvalidArgument, op, field, "must not be empty")
		}
		if err := validateGotmuxValue(op, field, value); err != nil {
			return err
		}
	}
	_, err := normalizeProjectName(op, request.Project.Name)
	return err
}

// SelectProjectWindow selects the first window carrying projectID.
func (m *Manager) SelectProjectWindow(ctx context.Context, projectID string) (window domain.TmuxWindow, err error) {
	const op = "tmux.select_project_window"
	defer m.recoverError(op, &err)
	if strings.TrimSpace(projectID) == "" {
		return window, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "projectID", "must not be empty")
	}
	if err := validateGotmuxValue(op, "projectID", projectID); err != nil {
		return window, err
	}
	windows, err := m.taggedWindows(ctx)
	if err != nil {
		return window, err
	}
	for _, candidate := range windows {
		if candidate.ProjectID == projectID {
			return m.selectWindow(ctx, candidate.ID)
		}
	}
	return window, domain.ResourceError(domain.ErrorCodeNotFound, op, projectID, "project window is not open", nil)
}

func (m *Manager) SelectPane(ctx context.Context, paneID string) (window domain.TmuxWindow, pane domain.TmuxPane, err error) {
	const op = "tmux.select_pane"
	defer m.recoverError(op, &err)
	if strings.TrimSpace(paneID) == "" {
		return window, pane, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "paneID", "must not be empty")
	}
	if err := validatePaneID(paneID); err != nil {
		return window, pane, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "paneID", err.Error())
	}
	snapshot, err := m.Snapshot(ctx)
	if err != nil {
		return window, pane, err
	}
	if snapshot.Session == nil {
		return window, pane, domain.ResourceError(domain.ErrorCodeNotFound, op, paneID, "managed session does not exist", nil)
	}
	var found bool
	for _, candidate := range snapshot.Session.Windows {
		for _, candidatePane := range candidate.Panes {
			if candidatePane.ID != paneID {
				continue
			}
			if candidatePane.Dead {
				return window, pane, domain.ResourceError(domain.ErrorCodeConflict, op, paneID, "pane is dead", nil)
			}
			window = candidate
			found = true
			break
		}
		if found {
			break
		}
	}
	if !found {
		return window, pane, domain.ResourceError(domain.ErrorCodeNotFound, op, paneID, "pane was not found", nil)
	}
	if _, err := m.selectWindow(ctx, window.ID); err != nil {
		return window, pane, err
	}
	if err := m.client.SelectPane(ctx, paneID); err != nil {
		return window, pane, m.failure(op, "select pane", err)
	}
	selected, err := m.paneByID(ctx, window.ID, paneID)
	if err != nil || selected == nil || selected.Dead || !selected.Active {
		return window, pane, m.verification(op, "pane selection was not observable", err)
	}
	selectedWindow, err := m.snapshotWindow(ctx, window.ID)
	if err != nil {
		return window, pane, err
	}
	return selectedWindow, mapPane(*selected), nil
}

// Snapshot returns immutable application models and trims option output.
func (m *Manager) Snapshot(ctx context.Context) (snapshot domain.TmuxSnapshot, err error) {
	const op = "tmux.snapshot"
	defer m.recoverError(op, &err)
	snapshot.CapturedAt = m.now()
	session, err := m.client.Session(ctx, m.config.Session)
	if err != nil {
		return snapshot, m.failure(op, "query session", err)
	}
	if session == nil {
		return snapshot, nil
	}

	windows, err := m.client.ListWindows(ctx, m.config.Session)
	if err != nil {
		return snapshot, m.failure(op, "list windows", err)
	}
	result := domain.TmuxSession{ID: session.ID, Name: session.Name, Attached: session.Attached, Windows: make([]domain.TmuxWindow, 0, len(windows))}
	for _, window := range windows {
		projectID, _, optionErr := m.client.WindowOption(ctx, window.ID, optionProjectID)
		if optionErr != nil {
			return snapshot, m.failure(op, "read project window tag", optionErr)
		}
		profile, _, optionErr := m.client.WindowOption(ctx, window.ID, optionProfile)
		if optionErr != nil {
			return snapshot, m.failure(op, "read project profile tag", optionErr)
		}
		panes, paneErr := m.client.ListPanes(ctx, window.ID)
		if paneErr != nil {
			return snapshot, m.failure(op, "list panes", paneErr)
		}
		mapped := domain.TmuxWindow{
			ID: window.ID, Index: window.Index, Name: window.Name, Active: window.Active,
			ProjectID: trimLineEndings(projectID), Profile: trimLineEndings(profile),
			Panes: make([]domain.TmuxPane, 0, len(panes)),
		}
		for _, pane := range panes {
			mapped.Panes = append(mapped.Panes, mapPane(pane))
		}
		result.Windows = append(result.Windows, mapped)
	}
	sort.Slice(result.Windows, func(i, j int) bool { return result.Windows[i].Index < result.Windows[j].Index })
	snapshot.Session = &result
	return snapshot, nil
}

// CurrentProjectID returns the tag on the caller's current tmux window.
func (m *Manager) CurrentProjectID(ctx context.Context) (projectID string, found bool, err error) {
	const op = "tmux.current_project_id"
	defer m.recoverError(op, &err)
	windowID, found, err := m.currentWindow(ctx, op)
	if err != nil || !found {
		return "", false, err
	}
	value, exists, err := m.client.WindowOption(ctx, windowID, optionProjectID)
	if err != nil {
		return "", false, m.failure(op, "read current project tag", err)
	}
	value = trimLineEndings(value)
	return value, exists && value != "", nil
}

// CurrentProjectName supports legacy name-based targeting when no project tag
// is present. The catalog remains responsible for resolving the name.
func (m *Manager) CurrentProjectName(ctx context.Context) (name string, found bool, err error) {
	const op = "tmux.current_project_name"
	defer m.recoverError(op, &err)
	paneID, insideTmux, err := m.callerPane(op)
	if err != nil || !insideTmux {
		return "", false, err
	}
	name, err = m.client.CurrentWindowName(ctx, paneID)
	if err != nil {
		return "", false, m.failure(op, "query current window name", err)
	}
	name = trimLineEndings(name)
	return name, name != "", nil
}

type AttachMode uint8

const (
	AttachModeInteractive AttachMode = iota + 1
	AttachModeSwitch
)

// AttachPlan captures targets while the application session lock is held.
// Interactive execution can release that lock before blocking in tmux.
type AttachPlan struct {
	Mode       AttachMode
	sessionID  string
	windowID   string
	clientName string
}

func (p AttachPlan) RequiresSessionLock() bool { return p.Mode == AttachModeSwitch }

// PrepareAttachOrSwitch validates the caller/server relationship and resolves
// exact immutable targets without changing tmux state.
func (m *Manager) PrepareAttachOrSwitch(ctx context.Context) (plan AttachPlan, err error) {
	return m.PrepareAttachOrSwitchTo(ctx, "")
}

// PrepareAttachOrSwitchTo resolves a target window in the managed session.
// An empty window ID targets the dashboard.
func (m *Manager) PrepareAttachOrSwitchTo(ctx context.Context, windowID string) (plan AttachPlan, err error) {
	const op = "tmux.attach_or_switch"
	defer m.recoverError(op, &err)
	paneID, insideTmux, err := m.callerPane(op)
	if err != nil {
		return plan, err
	}
	session, err := m.client.Session(ctx, m.config.Session)
	if err != nil {
		return plan, m.failure(op, "query session", err)
	}
	if session == nil {
		return plan, domain.ResourceError(domain.ErrorCodeNotFound, op, m.config.Session, "managed session does not exist", nil)
	}
	windows, err := m.taggedWindows(ctx)
	if err != nil {
		return plan, err
	}
	dashboard, _, err := m.findDashboard(ctx, windows)
	if err != nil {
		return plan, err
	}
	if dashboard == nil {
		return plan, domain.ResourceError(domain.ErrorCodeNotFound, op, m.config.DashboardWindow, "dashboard window does not exist", nil)
	}
	targetID := windowID
	if targetID == "" {
		targetID = dashboard.ID
	} else {
		target, targetErr := m.windowByID(ctx, targetID)
		if targetErr != nil || target == nil {
			return plan, domain.ResourceError(domain.ErrorCodeNotFound, op, targetID, "target window does not exist", targetErr)
		}
	}
	plan = AttachPlan{Mode: AttachModeInteractive, sessionID: session.ID, windowID: targetID}
	clientName := ""
	if insideTmux {
		clients, queryErr := m.client.ListClients(ctx)
		if queryErr != nil {
			return AttachPlan{}, m.failure(op, "list tmux clients", queryErr)
		}
		for _, client := range clients {
			if client.ActivePane != paneID {
				continue
			}
			if clientName != "" {
				return AttachPlan{}, domain.NewError(domain.ErrorCodeConflict, op, "invoking tmux client is ambiguous", nil)
			}
			clientName = client.Name
		}
		if clientName == "" {
			return AttachPlan{}, domain.NewError(domain.ErrorCodeNotFound, op, "invoking tmux client was not found", nil)
		}
		plan.Mode = AttachModeSwitch
		plan.clientName = clientName
	}
	return plan, nil
}

// ExecuteAttachOrSwitch performs the prepared operation. Callers must retain
// the session lock for switch plans and release it for interactive plans.
func (m *Manager) ExecuteAttachOrSwitch(ctx context.Context, plan AttachPlan) (err error) {
	const op = "tmux.attach_or_switch"
	defer m.recoverError(op, &err)
	if plan.sessionID == "" || plan.windowID == "" {
		return domain.NewError(domain.ErrorCodeInvalidArgument, op, "prepared target is incomplete", nil)
	}
	if plan.Mode == AttachModeInteractive {
		if err := m.client.Attach(ctx, plan.sessionID, plan.windowID, m.config.Output, m.config.Error); err != nil {
			return m.failure(op, "attach session", err)
		}
		return nil
	}
	if plan.Mode != AttachModeSwitch || plan.clientName == "" {
		return domain.NewError(domain.ErrorCodeInvalidArgument, op, "invalid prepared attach mode", nil)
	}
	if _, err := m.selectWindow(ctx, plan.windowID); err != nil {
		return err
	}
	if err := m.client.SwitchClient(ctx, plan.clientName, plan.sessionID); err != nil {
		return m.failure(op, "switch client", err)
	}
	current, err := m.client.ClientSession(ctx, plan.clientName)
	if err != nil || strings.TrimSpace(current) != m.config.Session {
		return m.verification(op, "client switch was not observable", err)
	}
	return nil
}

type taggedWindow struct {
	windowState
	ProjectID string
	Role      string
	Owner     string
	Profile   string
}

func (m *Manager) taggedWindows(ctx context.Context) ([]taggedWindow, error) {
	windows, err := m.client.ListWindows(ctx, m.config.Session)
	if err != nil {
		return nil, m.failure("tmux.list_windows", "list windows", err)
	}
	result := make([]taggedWindow, 0, len(windows))
	for _, window := range windows {
		projectID, _, err := m.client.WindowOption(ctx, window.ID, optionProjectID)
		if err != nil {
			return nil, m.failure("tmux.list_windows", "read project tag", err)
		}
		role, _, err := m.client.WindowOption(ctx, window.ID, optionRole)
		if err != nil {
			return nil, m.failure("tmux.list_windows", "read role tag", err)
		}
		owner, _, err := m.client.WindowOption(ctx, window.ID, optionOwner)
		if err != nil {
			return nil, m.failure("tmux.list_windows", "read ownership tag", err)
		}
		profile, _, err := m.client.WindowOption(ctx, window.ID, optionProfile)
		if err != nil {
			return nil, m.failure("tmux.list_windows", "read profile tag", err)
		}
		result = append(result, taggedWindow{
			windowState: window,
			ProjectID:   trimLineEndings(projectID),
			Role:        trimLineEndings(role),
			Owner:       trimLineEndings(owner),
			Profile:     trimLineEndings(profile),
		})
	}
	return result, nil
}

func (m *Manager) findDashboard(_ context.Context, windows []taggedWindow) (*taggedWindow, bool, error) {
	for i := range windows {
		if windows[i].Role == roleDashboard {
			return &windows[i], true, nil
		}
	}
	for i := range windows {
		if windows[i].Name != m.config.DashboardWindow {
			continue
		}
		if windows[i].ProjectID == "" {
			return &windows[i], windows[i].Owner == "1", nil
		}
	}
	return nil, false, nil
}

func (m *Manager) createDashboardWindow(ctx context.Context) (dashboard *taggedWindow, err error) {
	const op = "tmux.ensure_main_session"
	createdID, err := m.createWindow(ctx, op, "create dashboard window", m.config.DashboardWindow, m.config.StartDirectory, m.dashboardPaneCommand)
	if err != nil {
		return nil, err
	}
	rollback := true
	defer func() {
		if !rollback {
			return
		}
		cleanupCtx, cancelCleanup := m.cleanupContext(ctx)
		defer cancelCleanup()
		if rollbackErr := m.rollbackWindow(cleanupCtx, createdID); rollbackErr != nil {
			rollbackFailure := m.failure(op, "rollback unverified dashboard window", rollbackErr)
			if err == nil {
				err = rollbackFailure
			} else {
				err = errors.Join(err, rollbackFailure)
			}
		}
	}()
	window, _, err := m.verifyCreatedWindow(ctx, op, createdID, m.config.DashboardWindow, m.config.StartDirectory)
	if err != nil {
		return nil, err
	}
	rollback = false
	return &taggedWindow{windowState: *window}, nil
}

func (m *Manager) positionDashboard(ctx context.Context, dashboardID string) error {
	baseIndex := 0
	value, exists, err := m.client.SessionOption(ctx, m.config.Session, "base-index")
	if err != nil {
		return m.failure("tmux.ensure_main_session", "read base-index", err)
	}
	if exists {
		if parsed, parseErr := strconv.Atoi(trimLineEndings(value)); parseErr == nil {
			baseIndex = parsed
		}
	}
	windows, err := m.client.ListWindows(ctx, m.config.Session)
	if err != nil {
		return m.failure("tmux.ensure_main_session", "list windows before dashboard move", err)
	}
	var dashboard *windowState
	var occupant *windowState
	for i := range windows {
		if windows[i].ID == dashboardID {
			dashboard = &windows[i]
		}
		if windows[i].Index == baseIndex {
			occupant = &windows[i]
		}
	}
	if dashboard == nil {
		return m.verification("tmux.ensure_main_session", "dashboard disappeared before positioning", nil)
	}
	if dashboard.Index == baseIndex {
		return nil
	}
	if occupant != nil {
		err = m.client.SwapWindow(ctx, dashboardID, m.config.Session, baseIndex)
	} else {
		err = m.client.MoveWindow(ctx, dashboardID, m.config.Session, baseIndex)
	}
	if err != nil {
		return m.failure("tmux.ensure_main_session", "position dashboard window", err)
	}
	verified, err := m.windowByID(ctx, dashboardID)
	if err != nil || verified == nil || verified.Index != baseIndex {
		return m.verification("tmux.ensure_main_session", "dashboard positioning was not observable", err)
	}
	return nil
}

func (m *Manager) setAndVerifyWindowOption(ctx context.Context, windowID, key, value string) error {
	if err := validateGotmuxValue("tmux.set_window_option", key, value); err != nil {
		return err
	}
	if err := m.client.SetWindowOption(ctx, windowID, key, value); err != nil {
		return m.failure("tmux.set_window_option", "set "+key, err)
	}
	actual, exists, err := m.client.WindowOption(ctx, windowID, key)
	if err != nil || !exists || trimLineEndings(actual) != value {
		return m.verification("tmux.set_window_option", "window option mutation was not observable for "+key, err)
	}
	return nil
}

func (m *Manager) enableFocusEvents(ctx context.Context) error {
	const key = "focus-events"
	value, exists, err := m.client.ServerOption(ctx, key)
	if err != nil {
		return m.failure("tmux.ensure_main_session", "read focus-events", err)
	}
	if exists && trimLineEndings(value) == "on" {
		return nil
	}
	if err := m.client.SetServerOption(ctx, key, "on"); err != nil {
		return m.failure("tmux.ensure_main_session", "enable focus-events", err)
	}
	value, exists, err = m.client.ServerOption(ctx, key)
	if err != nil || !exists || trimLineEndings(value) != "on" {
		return m.verification("tmux.ensure_main_session", "focus-events mutation was not observable", err)
	}
	return nil
}

func (m *Manager) ensureTreeBinding(ctx context.Context) error {
	// Restore tmux's default Space behavior if an earlier op version replaced it.
	current, exists, err := m.client.KeyBinding(ctx, "prefix", "Space")
	if err != nil {
		return m.failure("tmux.ensure_main_session", "read legacy tree key binding", err)
	}
	if exists && strings.Contains(current, "switch-client -T op") {
		if err := m.client.BindKey(ctx, "prefix", "Space", "next-layout"); err != nil {
			return m.failure("tmux.ensure_main_session", "restore layout key binding", err)
		}
	}

	bindings := []struct {
		table, key string
		command    []string
		contains   []string
	}{
		{table: "prefix", key: "T", command: []string{"display-popup", "-E", "-w", "80%", "-h", "80%", m.config.TreeCommand}, contains: []string{"display-popup", m.config.TreeCommand}},
	}
	for _, binding := range bindings {
		current, exists, err := m.client.KeyBinding(ctx, binding.table, binding.key)
		if err != nil {
			return m.failure("tmux.ensure_main_session", "read tree key binding", err)
		}
		if exists && containsAll(current, binding.contains) {
			continue
		}
		if err := m.client.BindKey(ctx, binding.table, binding.key, binding.command...); err != nil {
			return m.failure("tmux.ensure_main_session", "install tree key binding", err)
		}
		current, exists, err = m.client.KeyBinding(ctx, binding.table, binding.key)
		if err != nil || !exists || !containsAll(current, binding.contains) {
			return m.verification("tmux.ensure_main_session", "tree key binding mutation was not observable", err)
		}
	}
	return nil
}

func containsAll(value string, fragments []string) bool {
	for _, fragment := range fragments {
		if !strings.Contains(value, fragment) {
			return false
		}
	}
	return true
}

func (m *Manager) verifyPaneCommand(ctx context.Context, windowID, paneID, command string) error {
	return m.verifyPaneCommands(ctx, windowID, paneID, command)
}

func (m *Manager) verifyPaneCommands(ctx context.Context, windowID, paneID string, commands ...string) error {
	expected := make(map[string]bool, len(commands))
	for _, command := range commands {
		if executable := commandExecutable(command); executable != "" {
			expected[executable] = true
		}
	}
	if len(expected) == 0 {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, "tmux.send_command", "command", "must identify an executable")
	}
	expectedNames := make([]string, 0, len(expected))
	for executable := range expected {
		expectedNames = append(expectedNames, executable)
	}
	sort.Strings(expectedNames)
	deadline := time.NewTimer(m.startupWait)
	defer deadline.Stop()
	var stableSince time.Time
	lastCommand := ""
	for {
		pane, err := m.paneByID(ctx, windowID, paneID)
		if err != nil {
			return err
		}
		if pane == nil {
			return m.verification("tmux.send_command", "pane disappeared while starting "+strings.Join(expectedNames, " or "), nil)
		}
		lastCommand = pane.CurrentCommand
		if pane.Dead {
			return m.verification("tmux.send_command", "pane exited while starting "+strings.Join(expectedNames, " or "), nil)
		}
		now := time.Now()
		if expected[pane.CurrentCommand] {
			if stableSince.IsZero() {
				stableSince = now
			}
			if now.Sub(stableSince) >= m.startupStable {
				return nil
			}
		} else {
			stableSince = time.Time{}
		}

		poll := time.NewTimer(m.startupPoll)
		select {
		case <-ctx.Done():
			poll.Stop()
			return contextError(ctx, "tmux.send_command")
		case <-deadline.C:
			poll.Stop()
			return m.verification("tmux.send_command", fmt.Sprintf("expected foreground command %q, observed %q", strings.Join(expectedNames, " or "), lastCommand), nil)
		case <-poll.C:
		}
	}
}

func (m *Manager) projectWindowHealthy(ctx context.Context, windowID string) (bool, error) {
	panes, err := m.client.ListPanes(ctx, windowID)
	if err != nil {
		return false, m.failure("tmux.open_project_window", "inspect tagged project window", err)
	}
	if len(panes) != 2 {
		return false, nil
	}
	sort.Slice(panes, func(i, j int) bool { return panes[i].Index < panes[j].Index })
	return !panes[0].Dead && !panes[1].Dead &&
		panes[0].AtTop && panes[1].AtBottom && panes[1].Height == m.config.ShellPaneRows, nil
}

func (m *Manager) killAndVerifyWindow(ctx context.Context, op, windowID string) error {
	if err := m.client.KillWindow(ctx, windowID); err != nil {
		return m.failure(op, "remove unhealthy owned window", err)
	}
	window, err := m.windowByID(ctx, windowID)
	if err != nil {
		return err
	}
	if window != nil {
		return m.verification(op, "owned window removal was not observable", nil)
	}
	return nil
}

func (m *Manager) killAndVerifyPane(ctx context.Context, op, paneID string) error {
	if err := m.client.KillPane(ctx, paneID); err != nil {
		return m.failure(op, "rollback newly created dashboard pane", err)
	}
	exists, err := m.client.PaneExists(ctx, paneID)
	if err != nil {
		return m.failure(op, "verify dashboard pane rollback", err)
	}
	if exists {
		return m.verification(op, "dashboard pane rollback was not observable", nil)
	}
	return nil
}

func (m *Manager) selectWindow(ctx context.Context, windowID string) (domain.TmuxWindow, error) {
	if err := m.client.SelectWindow(ctx, windowID); err != nil {
		return domain.TmuxWindow{}, m.failure("tmux.select_project_window", "select window", err)
	}
	window, err := m.windowByID(ctx, windowID)
	if err != nil || window == nil || !window.Active {
		return domain.TmuxWindow{}, m.verification("tmux.select_project_window", "window selection was not observable", err)
	}
	snapshot, err := m.Snapshot(ctx)
	if err != nil {
		return domain.TmuxWindow{}, err
	}
	if snapshot.Session == nil {
		return domain.TmuxWindow{}, m.verification("tmux.select_project_window", "managed session disappeared after selection", nil)
	}
	for _, candidate := range snapshot.Session.Windows {
		if candidate.ID == windowID {
			return candidate, nil
		}
	}
	return domain.TmuxWindow{}, m.verification("tmux.select_project_window", "selected window disappeared", nil)
}

func (m *Manager) createWindow(ctx context.Context, op, action, name, directory, shellCommand string) (string, error) {
	windowID, err := m.client.CreateWindow(ctx, m.config.Session, name, directory, shellCommand)
	if err != nil {
		var verificationErr *windowCreationVerificationError
		if errors.As(err, &verificationErr) {
			return "", m.verification(op, action+" did not return a valid window identity", err)
		}
		return "", m.failure(op, action, err)
	}
	if err := validateWindowID(windowID); err != nil {
		return "", m.verification(op, action+" did not return a valid window identity", err)
	}
	return windowID, nil
}

func (m *Manager) verifyCreatedWindow(ctx context.Context, op, windowID, name, directory string) (*windowState, *paneState, error) {
	window, err := m.windowByID(ctx, windowID)
	if err != nil {
		return nil, nil, err
	}
	if window == nil {
		return nil, nil, m.verification(op, "created window identity was not observable in the expected session", nil)
	}
	if window.Name != name {
		return nil, nil, m.verification(op, "created window did not have the expected name", nil)
	}
	panes, err := m.client.ListPanes(ctx, windowID)
	if err != nil {
		return nil, nil, m.failure(op, "inspect created window pane", err)
	}
	if len(panes) != 1 || panes[0].Dead || panes[0].PID <= 0 {
		return nil, nil, m.verification(op, "created window did not have one live initial pane", nil)
	}
	if directory != "" && panes[0].CurrentPath != directory {
		return nil, nil, m.verification(op, "created window initial pane did not have the expected path", nil)
	}
	return window, &panes[0], nil
}

func (m *Manager) windowByID(ctx context.Context, id string) (*windowState, error) {
	windows, err := m.client.ListWindows(ctx, m.config.Session)
	if err != nil {
		return nil, m.failure("tmux.query_window", "list windows", err)
	}
	for i := range windows {
		if windows[i].ID == id {
			return &windows[i], nil
		}
	}
	return nil, nil
}

func (m *Manager) paneByID(ctx context.Context, windowID, paneID string) (*paneState, error) {
	panes, err := m.client.ListPanes(ctx, windowID)
	if err != nil {
		return nil, m.failure("tmux.query_pane", "list panes", err)
	}
	for i := range panes {
		if panes[i].ID == paneID {
			return &panes[i], nil
		}
	}
	return nil, nil
}

func (m *Manager) rollbackWindow(ctx context.Context, windowID string) error {
	if err := m.client.KillWindow(ctx, windowID); err != nil {
		if tmuxServerAbsent(err) {
			return nil
		}
		return err
	}
	exists, err := m.client.WindowExists(ctx, windowID)
	if err != nil {
		if tmuxServerAbsent(err) {
			return nil
		}
		return err
	}
	if exists {
		return fmt.Errorf("created window %s remained after rollback", windowID)
	}
	return nil
}

func tmuxServerAbsent(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "no server running") ||
		(strings.Contains(message, "error connecting to") && strings.Contains(message, "No such file or directory"))
}

func (m *Manager) cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), m.cleanupTimeout)
}

func (m *Manager) currentWindow(ctx context.Context, op string) (string, bool, error) {
	paneID, insideTmux, err := m.callerPane(op)
	if err != nil || !insideTmux {
		return "", false, err
	}
	windowID, err := m.client.CurrentWindow(ctx, paneID)
	if err != nil {
		return "", false, m.failure(op, "query current window", err)
	}
	windowID = strings.TrimSpace(windowID)
	return windowID, windowID != "", nil
}

func (m *Manager) callerPane(op string) (string, bool, error) {
	tmuxValue := m.lookupEnv("TMUX")
	if tmuxValue == "" {
		return "", false, nil
	}
	callerSocket, err := parseTmuxSocket(tmuxValue)
	if err != nil {
		return "", false, domain.NewError(domain.ErrorCodeDependency, op, "TMUX does not identify a valid tmux server", err)
	}
	if m.config.Socket != "" {
		configuredSocket, normalizeErr := normalizeSocketPath(m.config.Socket)
		if normalizeErr != nil {
			return "", false, domain.NewError(domain.ErrorCodeDependency, op, "normalize configured tmux socket", normalizeErr)
		}
		callerSocket, normalizeErr = normalizeSocketPath(callerSocket)
		if normalizeErr != nil {
			return "", false, domain.NewError(domain.ErrorCodeDependency, op, "normalize invoking tmux socket", normalizeErr)
		}
		if callerSocket != configuredSocket {
			return "", false, domain.NewError(domain.ErrorCodeConflict, op, "invoking tmux server does not match configured tmux.socket", nil)
		}
	}
	paneID := m.lookupEnv("TMUX_PANE")
	if err := validatePaneID(paneID); err != nil {
		return "", false, domain.NewError(domain.ErrorCodeDependency, op, "TMUX_PANE does not identify a valid tmux pane", err)
	}
	return paneID, true, nil
}

func parseTmuxSocket(value string) (string, error) {
	lastComma := strings.LastIndexByte(value, ',')
	if lastComma < 0 {
		return "", errors.New("missing pane index")
	}
	secondLastComma := strings.LastIndexByte(value[:lastComma], ',')
	if secondLastComma <= 0 {
		return "", errors.New("missing socket path or server PID")
	}
	pid, err := strconv.ParseUint(value[secondLastComma+1:lastComma], 10, 64)
	if err != nil || pid == 0 {
		return "", errors.New("invalid server PID")
	}
	if _, err := strconv.ParseUint(value[lastComma+1:], 10, 64); err != nil {
		return "", errors.New("invalid pane index")
	}
	return value[:secondLastComma], nil
}

func normalizeSocketPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	absolute = filepath.Clean(absolute)
	candidate := absolute
	remainder := ""
	for {
		resolved, resolveErr := filepath.EvalSymlinks(candidate)
		if resolveErr == nil {
			return filepath.Clean(filepath.Join(resolved, remainder)), nil
		}
		if !errors.Is(resolveErr, os.ErrNotExist) {
			return "", resolveErr
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			return absolute, nil
		}
		remainder = filepath.Join(filepath.Base(candidate), remainder)
		candidate = parent
	}
}

func newPane(before, after []paneState) *paneState {
	known := make(map[string]bool, len(before))
	for _, pane := range before {
		known[pane.ID] = true
	}
	for i := range after {
		if !known[after[i].ID] {
			return &after[i]
		}
	}
	return nil
}

func mapPane(pane paneState) domain.TmuxPane {
	return domain.TmuxPane{
		ID: pane.ID, Index: pane.Index, PID: pane.PID,
		CurrentCommand: pane.CurrentCommand, CurrentPath: pane.CurrentPath,
		Active: pane.Active, Dead: pane.Dead,
	}
}

func isSingleToken(command string) bool {
	return command != "" && !strings.ContainsAny(command, " \t\r\n")
}

func commandExecutable(command string) string {
	fields := shellWords(command)
	if len(fields) == 0 {
		return ""
	}
	executable := fields[0]
	if executable == "exec" && len(fields) > 1 {
		executable = fields[1]
	}
	if slash := strings.LastIndexByte(executable, '/'); slash >= 0 {
		executable = executable[slash+1:]
	}
	return executable
}

func buildEditorPaneCommand(preferredShell, editorCommand string) (string, error) {
	return buildPersistentShellCommand(preferredShell, editorCommand)
}

func buildPersistentShellCommand(preferredShell, foregroundCommand string) (string, error) {
	shell, err := parseDirectCommand(preferredShell)
	if err != nil {
		return "", fmt.Errorf("invalid preferred shell command: %w", err)
	}
	command := append([]string{"exec"}, shell...)
	if isPowerShell(shell[0]) {
		command = append(command, "-NoExit", "-Command", foregroundCommand)
	} else {
		restart := quoteShellWords(shell)
		command = append(command, "-ic", foregroundCommand+"; exec "+restart)
	}
	return command[0] + " " + quoteShellWords(command[1:]), nil
}

func quoteShellWords(words []string) string {
	quoted := make([]string, len(words))
	for index, word := range words {
		quoted[index] = quoteTmuxArgument(word)
	}
	return strings.Join(quoted, " ")
}

func isPowerShell(executable string) bool {
	if separator := strings.LastIndexAny(executable, `/\\`); separator >= 0 {
		executable = executable[separator+1:]
	}
	switch strings.ToLower(executable) {
	case "pwsh", "pwsh.exe", "powershell", "powershell.exe":
		return true
	default:
		return false
	}
}

func parseDirectCommand(command string) ([]string, error) {
	var words []string
	var word strings.Builder
	var quote byte
	escaped := false
	started := false
	flush := func() {
		if started {
			words = append(words, word.String())
			word.Reset()
			started = false
		}
	}
	for index := 0; index < len(command); index++ {
		character := command[index]
		if escaped {
			if quote == '"' && !doubleQuoteEscapes(character) {
				word.WriteByte('\\')
			}
			word.WriteByte(character)
			started = true
			escaped = false
			continue
		}
		if quote != 0 {
			if character == quote {
				quote = 0
			} else if quote == '"' && character == '\\' {
				escaped = true
			} else {
				word.WriteByte(character)
			}
			started = true
			continue
		}
		switch character {
		case '\'', '"':
			quote = character
			started = true
		case '\\':
			escaped = true
			started = true
		case ' ', '\t':
			flush()
		case ';', '|', '&', '<', '>', '(', ')', '\r', '\n':
			return nil, fmt.Errorf("shell operator %q is not allowed", character)
		default:
			word.WriteByte(character)
			started = true
		}
	}
	if escaped || quote != 0 {
		return nil, errors.New("unterminated quote or escape")
	}
	flush()
	if len(words) == 0 || words[0] == "" {
		return nil, errors.New("command is empty")
	}
	return words, nil
}

func shellWords(command string) []string {
	var words []string
	var word strings.Builder
	var quote byte
	escaped := false
	started := false
	flush := func() {
		if started {
			words = append(words, word.String())
			word.Reset()
			started = false
		}
	}
	for i := 0; i < len(command); i++ {
		char := command[i]
		if escaped {
			if quote == '"' && !doubleQuoteEscapes(char) {
				word.WriteByte('\\')
			}
			word.WriteByte(char)
			started = true
			escaped = false
			continue
		}
		switch quote {
		case '\'':
			if char == '\'' {
				quote = 0
			} else {
				word.WriteByte(char)
			}
			started = true
			continue
		case '"':
			if char == '"' {
				quote = 0
			} else if char == '\\' {
				escaped = true
			} else {
				word.WriteByte(char)
			}
			started = true
			continue
		}
		switch char {
		case '\'', '"':
			quote = char
			started = true
		case '\\':
			escaped = true
			started = true
		case ' ', '\t', '\r', '\n':
			flush()
		default:
			word.WriteByte(char)
			started = true
		}
	}
	if escaped {
		word.WriteByte('\\')
	}
	flush()
	return words
}

func doubleQuoteEscapes(character byte) bool {
	return character == '$' || character == '`' || character == '"' || character == '\\' || character == '\n'
}

func trimLineEndings(value string) string {
	return strings.TrimRight(value, "\r\n")
}

func contextError(ctx context.Context, op string) error {
	if err := ctx.Err(); err != nil {
		return domain.NewError(domain.CodeOf(err), op, "operation canceled", err)
	}
	return nil
}

func (m *Manager) failure(op, action string, err error) error {
	if err == nil {
		return nil
	}
	if contextErr := contextErrorFrom(err, op); contextErr != nil {
		return contextErr
	}
	return domain.NewError(domain.ErrorCodeDependency, op, action+": "+err.Error(), err)
}

func (m *Manager) verification(op, message string, err error) error {
	return domain.NewError(domain.ErrorCodeDependency, op, message, err)
}

func contextErrorFrom(err error, op string) error {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return domain.NewError(domain.CodeOf(err), op, "operation canceled", err)
	}
	return nil
}

func (m *Manager) recoverError(op string, err *error) {
	if recovered := recover(); recovered != nil {
		panicErr := domain.NewError(domain.ErrorCodeInternal, op, "tmux adapter panic was recovered", fmt.Errorf("%v", recovered))
		if *err == nil {
			*err = panicErr
		} else {
			*err = errors.Join(panicErr, *err)
		}
	}
}
