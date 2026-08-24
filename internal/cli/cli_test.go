package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moutansos/op/internal/app"
	"github.com/moutansos/op/internal/config"
	"github.com/moutansos/op/internal/domain"
	"github.com/moutansos/op/internal/server"
	"github.com/moutansos/op/internal/tui"
)

type fakeService struct {
	projects        []domain.Project
	listCalls       int
	current         domain.Project
	currentFound    bool
	ensureCalls     int
	attachCalls     int
	attachWindowID  string
	resolveCalls    int
	actionCalls     int
	actionRequest   domain.RunProjectActionRequest
	cloneRequest    domain.CloneRequest
	openRequest     domain.OpenProjectRequest
	openResult      domain.OpenProjectResult
	createRequest   domain.CreateProjectRequest
	worktreeRequest domain.CreateWorktreeRequest
	ensureResult    domain.EnsureMainSessionResult
	err             error
}

func (f *fakeService) ListProjects(context.Context) ([]domain.Project, error) {
	f.listCalls++
	return f.projects, f.err
}

func (f *fakeService) CreateProject(_ context.Context, request domain.CreateProjectRequest) (domain.CreateProjectResult, error) {
	f.createRequest = request
	return domain.CreateProjectResult{Project: domain.Project{ID: request.Name, Name: request.Name, Path: "/repos/" + request.Name}}, f.err
}

func (f *fakeService) CloneProject(_ context.Context, request domain.CloneRequest) (domain.CloneResult, error) {
	f.cloneRequest = request
	return domain.CloneResult{Project: domain.Project{ID: "clone", Name: "clone", Path: "/repos/clone"}}, f.err
}

func (f *fakeService) CreateWorktree(_ context.Context, request domain.CreateWorktreeRequest) (domain.CreateWorktreeResult, error) {
	f.worktreeRequest = request
	return domain.CreateWorktreeResult{Project: domain.Project{ID: "worktree", Name: "worktree", Path: "/repos/worktree"}}, f.err
}

func (f *fakeService) OpenProject(_ context.Context, request domain.OpenProjectRequest) (domain.OpenProjectResult, error) {
	f.openRequest = request
	if f.openResult.Project.ID != "" {
		return f.openResult, f.err
	}
	return domain.OpenProjectResult{
		Project: domain.Project{ID: request.ProjectID, Name: "alpha"}, Profile: request.Profile,
		Mode: domain.ProjectOpenModeTmux, Window: domain.TmuxWindow{ID: "@alpha", Name: "alpha"},
	}, f.err
}

func (f *fakeService) SelectPane(context.Context, domain.SelectPaneRequest) (domain.SelectPaneResult, error) {
	return domain.SelectPaneResult{}, f.err
}

func (f *fakeService) RunProjectAction(_ context.Context, request domain.RunProjectActionRequest) (domain.RunProjectActionResult, error) {
	f.actionCalls++
	f.actionRequest = request
	return domain.RunProjectActionResult{Project: f.current, Action: request.Action, Started: true}, f.err
}

func (f *fakeService) EnsureMainSession(context.Context) (domain.EnsureMainSessionResult, error) {
	f.ensureCalls++
	return f.ensureResult, f.err
}

func (f *fakeService) GetTmuxSnapshot(context.Context) (domain.TmuxSnapshot, error) {
	return domain.TmuxSnapshot{}, f.err
}

func (f *fakeService) GetStatsSnapshot(context.Context) (domain.StatsSnapshot, error) {
	return domain.StatsSnapshot{}, f.err
}

func (f *fakeService) ResolveCurrentProject(context.Context) (domain.Project, bool, error) {
	f.resolveCalls++
	return f.current, f.currentFound, f.err
}

func (f *fakeService) AttachOrSwitch(context.Context) error {
	f.attachCalls++
	return f.err
}

func (f *fakeService) AttachOrSwitchTo(_ context.Context, windowID string) error {
	f.attachCalls++
	f.attachWindowID = windowID
	return f.err
}

type testRuntime struct {
	service                 *fakeService
	stdin                   *strings.Reader
	stdout                  bytes.Buffer
	stderr                  bytes.Buffer
	configPath              string
	appOptions              app.Options
	lookup                  map[string]string
	tuiCalls                int
	tuiOptions              tui.Options
	serverCalls             int
	serverOptions           server.Options
	http                    HTTPClient
	warnings                []config.Warning
	customCommands          []config.CustomCommand
	projectOpeners          []config.ProjectOpener
	guiEditors              bool
	selectedAction          string
	selectedProject         string
	selectorCalls           int
	selectorTitle           string
	selectorActions         []tui.Action
	selectorStreams         bool
	selectorErr             error
	projectSelectorCalls    int
	projectSelectorTitle    string
	projectSelectorProjects []domain.Project
	serviceCalls            int
	lookPath                func(string) (string, error)
}

func newTestRuntime() *testRuntime {
	return &testRuntime{
		service:         &fakeService{},
		stdin:           strings.NewReader(""),
		lookup:          make(map[string]string),
		customCommands:  []config.CustomCommand{{Name: "opencode", Command: "opencode {{path}}"}},
		selectedAction:  "opencode",
		selectedProject: "",
	}
}

func (r *testRuntime) options() Options {
	cfg := config.Defaults()
	cfg.RepoDirectory = "/repos"
	cfg.RootDirectory = "/config"
	cfg.SourcePath = "/config/config.json"
	cfg.Server.TokenFile = "/token"
	cfg.CustomCommands = r.customCommands
	if r.projectOpeners != nil {
		cfg.ProjectOpeners = r.projectOpeners
		cfg.Tmux.DefaultProfile = r.projectOpeners[0].ID
	}
	cfg.Actions.GUIEditors = r.guiEditors
	return Options{
		Stdin:  r.stdin,
		Stdout: &r.stdout,
		Stderr: &r.stderr,
		LookupEnv: func(name string) string {
			return r.lookup[name]
		},
		Executable: func() (string, error) { return "/opt/op", nil },
		LookPath: func(name string) (string, error) {
			if r.lookPath != nil {
				return r.lookPath(name)
			}
			return "/usr/bin/" + name, nil
		},
		ReadFile: func(string) ([]byte, error) { return []byte("file-token\n"), nil },
		LoadConfig: func(path string) (config.LoadResult, error) {
			r.configPath = path
			return config.LoadResult{Config: cfg, Warnings: r.warnings}, nil
		},
		NewService: func(_ context.Context, _ config.Config, options app.Options) (Service, error) {
			r.serviceCalls++
			r.appOptions = options
			return r.service, nil
		},
		RunTUI: func(_ context.Context, _ domain.Service, options tui.Options) error {
			r.tuiCalls++
			r.tuiOptions = options
			return nil
		},
		SelectAction: func(_ context.Context, title string, actions []tui.Action, input io.Reader, output io.Writer) (*tui.Action, error) {
			r.selectorCalls++
			r.selectorTitle = title
			r.selectorActions = append([]tui.Action(nil), actions...)
			r.selectorStreams = input == r.stdin && output == &r.stdout
			if r.selectorErr != nil {
				return nil, r.selectorErr
			}
			for _, action := range actions {
				if action.ID == r.selectedAction {
					selected := action
					return &selected, nil
				}
			}
			return nil, nil
		},
		SelectProject: func(_ context.Context, title string, projects []domain.Project, input io.Reader, output io.Writer) (*domain.Project, error) {
			r.projectSelectorCalls++
			r.projectSelectorTitle = title
			r.projectSelectorProjects = append([]domain.Project(nil), projects...)
			r.selectorStreams = input == r.stdin && output == &r.stdout
			if r.selectorErr != nil {
				return nil, r.selectorErr
			}
			for _, project := range projects {
				if project.ID == r.selectedProject {
					selected := project
					return &selected, nil
				}
			}
			return nil, nil
		},
		RunServer: func(_ context.Context, _ domain.Service, options server.Options) error {
			r.serverCalls++
			r.serverOptions = options
			return nil
		},
		Signals: func(ctx context.Context) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		},
		HTTPClient: r.http,
	}
}

func TestDefaultDispatchSelectsAndOpensProjectFromRandomShell(t *testing.T) {
	for _, insideTmux := range []bool{false, true} {
		t.Run(map[bool]string{false: "outside tmux", true: "unrecognized tmux window"}[insideTmux], func(t *testing.T) {
			runtime := newTestRuntime()
			project := domain.Project{ID: "p1", Name: "alpha", Path: "/repos/alpha", Tags: []string{"api"}}
			runtime.service.projects = []domain.Project{project}
			runtime.selectedProject = project.ID
			runtime.service.openResult = domain.OpenProjectResult{
				Project: project, Profile: "nvim", Mode: domain.ProjectOpenModeTmux,
				Window: domain.TmuxWindow{ID: "@7", Name: "alpha"},
			}
			if insideTmux {
				runtime.lookup["TMUX"] = "inside"
			}

			code := Run(context.Background(), []string{"--config", "custom.json", "--no-repo-update"}, runtime.options())
			if code != 0 {
				t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
			}
			if runtime.configPath != "custom.json" || runtime.projectSelectorCalls != 1 || runtime.projectSelectorTitle != "open project" || !runtime.selectorStreams {
				t.Fatalf("selector calls=%d title=%q config=%q streams=%v", runtime.projectSelectorCalls, runtime.projectSelectorTitle, runtime.configPath, runtime.selectorStreams)
			}
			if len(runtime.projectSelectorProjects) != 1 || runtime.projectSelectorProjects[0].ID != project.ID || runtime.projectSelectorProjects[0].Path != project.Path {
				t.Fatalf("selector projects = %+v", runtime.projectSelectorProjects)
			}
			if runtime.service.openRequest.ProjectID != project.ID || !runtime.service.openRequest.DeferSelection || runtime.service.openRequest.Profile != "" || runtime.service.openRequest.NewInstance {
				t.Fatalf("open request = %+v", runtime.service.openRequest)
			}
			if runtime.service.attachCalls != 1 || runtime.service.attachWindowID != "@7" || runtime.service.actionCalls != 0 {
				t.Fatalf("attach calls=%d target=%q actions=%d", runtime.service.attachCalls, runtime.service.attachWindowID, runtime.service.actionCalls)
			}
			if runtime.appOptions.EnableRepositoryUpdates || runtime.appOptions.DashboardCommand != "/opt/op --config /config/config.json --no-repo-update dashboard" {
				t.Fatalf("app options = %+v", runtime.appOptions)
			}
		})
	}
}

func TestDefaultDispatchRunsDashboardInInvokingPaneWhenRequested(t *testing.T) {
	runtime := newTestRuntime()
	runtime.service.ensureResult.StartDashboard = true
	code := Run(context.Background(), []string{"--no-target"}, runtime.options())
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if runtime.service.ensureCalls != 1 || runtime.service.attachCalls != 0 || runtime.tuiCalls != 1 {
		t.Fatalf("ensure=%d attach=%d tui=%d", runtime.service.ensureCalls, runtime.service.attachCalls, runtime.tuiCalls)
	}
}

func TestTargetedDefaultChoosesConfiguredActionAndNoTargetOverrides(t *testing.T) {
	runtime := newTestRuntime()
	runtime.lookup["TMUX"] = "inside"
	runtime.service.current = domain.Project{ID: "p1", Name: "alpha"}
	runtime.service.currentFound = true
	if code := Run(context.Background(), nil, runtime.options()); code != 0 {
		t.Fatalf("target exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if runtime.service.actionRequest != (domain.RunProjectActionRequest{ProjectID: "p1", Action: "opencode"}) {
		t.Fatalf("action request = %+v", runtime.service.actionRequest)
	}
	if runtime.selectorCalls != 1 || runtime.selectorTitle != "actions for alpha" || !runtime.selectorStreams {
		t.Fatalf("selector calls=%d title=%q direct streams=%v", runtime.selectorCalls, runtime.selectorTitle, runtime.selectorStreams)
	}
	if runtime.service.ensureCalls != 0 || runtime.service.attachCalls != 0 {
		t.Fatalf("target unexpectedly entered session flow")
	}

	runtime = newTestRuntime()
	runtime.lookup["TMUX"] = "inside"
	runtime.service.currentFound = true
	if code := Run(context.Background(), []string{"--no-target"}, runtime.options()); code != 0 {
		t.Fatalf("no-target exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if runtime.service.resolveCalls != 0 || runtime.service.ensureCalls != 1 || runtime.service.attachCalls != 1 {
		t.Fatalf("resolve=%d ensure=%d attach=%d", runtime.service.resolveCalls, runtime.service.ensureCalls, runtime.service.attachCalls)
	}
}

func TestRandomShellProjectSelectionCancelDoesNotMutateTmux(t *testing.T) {
	runtime := newTestRuntime()
	runtime.service.projects = []domain.Project{{ID: "p1", Name: "alpha", Path: "/repos/alpha"}}
	if code := Run(context.Background(), nil, runtime.options()); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if runtime.projectSelectorCalls != 1 || runtime.service.openRequest.ProjectID != "" || runtime.service.attachCalls != 0 || runtime.service.ensureCalls != 0 {
		t.Fatalf("selector=%d open=%+v attach=%d ensure=%d", runtime.projectSelectorCalls, runtime.service.openRequest, runtime.service.attachCalls, runtime.service.ensureCalls)
	}
}

func TestRandomShellGUIProjectOpenDoesNotAttachTmux(t *testing.T) {
	runtime := newTestRuntime()
	project := domain.Project{ID: "p1", Name: "alpha", Path: "/repos/alpha"}
	runtime.service.projects = []domain.Project{project}
	runtime.selectedProject = project.ID
	runtime.service.openResult = domain.OpenProjectResult{Project: project, Profile: "vscode", Mode: domain.ProjectOpenModeGUI}
	if code := Run(context.Background(), nil, runtime.options()); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if runtime.service.attachCalls != 0 || !strings.Contains(runtime.stdout.String(), "Opened alpha with vscode") {
		t.Fatalf("attach=%d stdout=%q", runtime.service.attachCalls, runtime.stdout.String())
	}
}

func TestTargetedActionSelectionDistinguishesBuiltInAndCaseDistinctCustomCommand(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "built-in exact ID", want: "nvim"},
		{name: "custom exact ID and name", want: "Nvim"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := newTestRuntime()
			runtime.customCommands = []config.CustomCommand{{Name: "Nvim", Command: "custom-nvim"}}
			runtime.lookup["TMUX"] = "inside"
			runtime.service.current = domain.Project{ID: "p1", Name: "alpha"}
			runtime.service.currentFound = true
			runtime.selectedAction = test.want

			if code := Run(context.Background(), nil, runtime.options()); code != 0 {
				t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
			}
			if runtime.service.actionCalls != 1 || runtime.service.actionRequest.Action != test.want {
				t.Fatalf("action calls=%d request=%+v, want %q", runtime.service.actionCalls, runtime.service.actionRequest, test.want)
			}
		})
	}
}

func TestTargetedActionSelectorIncludesConfiguredActionsAndGatesGUIAction(t *testing.T) {
	runtime := newTestRuntime()
	runtime.guiEditors = true
	runtime.customCommands = []config.CustomCommand{{Name: "Nvim", Command: "custom-nvim"}, {Name: "3", Command: "numeric"}}
	runtime.selectedAction = ""
	runtime.lookup["TMUX"] = "inside"
	runtime.service.current = domain.Project{ID: "p1", Name: "alpha"}
	runtime.service.currentFound = true

	if code := Run(context.Background(), nil, runtime.options()); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if runtime.service.actionCalls != 0 {
		t.Fatalf("canceled selector dispatched %+v", runtime.service.actionRequest)
	}
	got := make([]string, len(runtime.selectorActions))
	for index, action := range runtime.selectorActions {
		got[index] = action.ID
	}
	if want := []string{"nvim", "shell", "code", "Nvim", "3"}; !slices.Equal(got, want) {
		t.Fatalf("selector actions = %v, want %v", got, want)
	}
	labels := make([]string, len(runtime.selectorActions))
	for index, action := range runtime.selectorActions {
		labels[index] = action.Name
	}
	if want := []string{"nvim", "cd-here", "vs-code", "nvim", "3"}; !slices.Equal(labels, want) {
		t.Fatalf("selector labels = %v, want %v", labels, want)
	}
}

func TestTargetedActionSelectorOmitsGUIActionWhenDisabledAndCancels(t *testing.T) {
	runtime := newTestRuntime()
	runtime.selectedAction = ""
	runtime.lookup["TMUX"] = "inside"
	runtime.service.current = domain.Project{ID: "p1", Name: "alpha"}
	runtime.service.currentFound = true

	if code := Run(context.Background(), nil, runtime.options()); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if runtime.service.actionCalls != 0 {
		t.Fatalf("canceled selection dispatched %+v", runtime.service.actionRequest)
	}
	for _, action := range runtime.selectorActions {
		if action.ID == "code" {
			t.Fatalf("disabled GUI action appeared in selector: %#v", runtime.selectorActions)
		}
	}
}

func TestDashboardUsesConfigOptionsWithoutAttaching(t *testing.T) {
	runtime := newTestRuntime()
	if code := Run(context.Background(), []string{"dashboard"}, runtime.options()); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if runtime.tuiCalls != 1 || runtime.service.ensureCalls != 0 || runtime.service.attachCalls != 0 {
		t.Fatalf("tui=%d ensure=%d attach=%d", runtime.tuiCalls, runtime.service.ensureCalls, runtime.service.attachCalls)
	}
	if len(runtime.tuiOptions.ProjectOpeners) != 1 || runtime.tuiOptions.ProjectOpeners[0].ID != "nvim" || runtime.tuiOptions.StatsRefreshInterval != 2*time.Second {
		t.Fatalf("tui options = %+v", runtime.tuiOptions)
	}
}

func TestDashboardReceivesCallerCancellation(t *testing.T) {
	runtime := newTestRuntime()
	options := runtime.options()
	started := make(chan context.Context, 1)
	options.RunTUI = func(ctx context.Context, _ domain.Service, _ tui.Options) error {
		started <- ctx
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan int, 1)
	go func() { result <- Run(ctx, []string{"dashboard"}, options) }()

	dashboardCtx := <-started
	cancel()
	select {
	case <-dashboardCtx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("dashboard context was not canceled")
	}
	select {
	case code := <-result:
		if code != 130 {
			t.Fatalf("exit = %d, want canceled exit 130; stderr = %s", code, runtime.stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("CLI did not return after dashboard cancellation")
	}
}

func TestInterspersedCloneFlagsAndWarningOutput(t *testing.T) {
	runtime := newTestRuntime()
	runtime.warnings = []config.Warning{{Path: "preferedShell", Message: "use preferredShell"}}
	code := Run(context.Background(), []string{"clone", "https://example.test/repo.git", "--open", "--directory", "custom"}, runtime.options())
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	request := runtime.service.cloneRequest
	if request.URL != "https://example.test/repo.git" || request.Directory != "custom" || !request.OpenOnFinish {
		t.Fatalf("clone request = %+v", request)
	}
	if !strings.Contains(runtime.stderr.String(), "warning: preferedShell: use preferredShell") {
		t.Fatalf("stderr = %q", runtime.stderr.String())
	}
}

func TestLocalCommandDispatchAndFlags(t *testing.T) {
	project := domain.Project{ID: "project-id", Name: "alpha", Path: "/repos/alpha"}
	tests := []struct {
		name   string
		args   []string
		assert func(*testing.T, *testRuntime)
	}{
		{
			name: "projects json",
			args: []string{"projects", "--json"},
			assert: func(t *testing.T, runtime *testRuntime) {
				if runtime.service.listCalls != 1 || !strings.Contains(runtime.stdout.String(), `"id": "project-id"`) {
					t.Fatalf("list calls=%d stdout=%q", runtime.service.listCalls, runtime.stdout.String())
				}
			},
		},
		{
			name: "open",
			args: []string{"open", "alpha", "--profile", "shell", "--new-instance"},
			assert: func(t *testing.T, runtime *testRuntime) {
				want := domain.OpenProjectRequest{ProjectID: "project-id", Profile: "shell", NewInstance: true}
				if runtime.service.openRequest != want {
					t.Fatalf("open request = %+v", runtime.service.openRequest)
				}
			},
		},
		{
			name: "new",
			args: []string{"new", "fresh", "--open", "--profile=editor"},
			assert: func(t *testing.T, runtime *testRuntime) {
				want := domain.CreateProjectRequest{Name: "fresh", OpenOnFinish: true, Profile: "editor"}
				if runtime.service.createRequest != want {
					t.Fatalf("create request = %+v", runtime.service.createRequest)
				}
			},
		},
		{
			name: "worktree",
			args: []string{"worktree", "project-id", "feature/cli", "--directory", "alpha-cli", "--open"},
			assert: func(t *testing.T, runtime *testRuntime) {
				want := domain.CreateWorktreeRequest{ProjectID: "project-id", Branch: "feature/cli", Directory: "alpha-cli", OpenOnFinish: true}
				if runtime.service.worktreeRequest != want {
					t.Fatalf("worktree request = %+v", runtime.service.worktreeRequest)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := newTestRuntime()
			runtime.service.projects = []domain.Project{project}
			if code := Run(context.Background(), test.args, runtime.options()); code != 0 {
				t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
			}
			test.assert(t, runtime)
		})
	}
}

func TestLocalCommandsCheckDependenciesBeforeConstructingService(t *testing.T) {
	runtime := newTestRuntime()
	runtime.lookPath = func(name string) (string, error) {
		if name == "git" {
			return "", errors.New("not found")
		}
		return "/usr/bin/" + name, nil
	}
	code := Run(context.Background(), []string{"projects"}, runtime.options())
	if code != 7 || runtime.serviceCalls != 0 {
		t.Fatalf("exit=%d service calls=%d stderr=%q", code, runtime.serviceCalls, runtime.stderr.String())
	}
	if !strings.Contains(runtime.stderr.String(), "git executable was not found") {
		t.Fatalf("stderr = %q", runtime.stderr.String())
	}
}

func TestNonTmuxCommandsDoNotRequireTmux(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "read-only projects", args: []string{"projects"}},
		{name: "new without open", args: []string{"new", "fresh"}},
		{name: "clone without open", args: []string{"clone", "https://example.test/repo.git"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime := newTestRuntime()
			runtime.lookPath = func(name string) (string, error) {
				if name == "tmux" {
					return "", errors.New("not available on this platform")
				}
				return "/usr/bin/" + name, nil
			}
			if code := Run(context.Background(), test.args, runtime.options()); code != 0 {
				t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
			}
		})
	}
}

func TestGUIProjectOpenerDoesNotRequireTmux(t *testing.T) {
	runtime := newTestRuntime()
	runtime.projectOpeners = []config.ProjectOpener{{
		ID: "vscode", Name: "VS Code", Mode: domain.ProjectOpenModeGUI, Command: "code {{path}}",
	}}
	runtime.service.projects = []domain.Project{{ID: "project", Name: "project"}}
	runtime.lookPath = func(name string) (string, error) {
		if name == "tmux" {
			return "", errors.New("not available")
		}
		return "/usr/bin/" + name, nil
	}
	if code := Run(context.Background(), []string{"open", "project"}, runtime.options()); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if runtime.service.openRequest.Profile != "" {
		t.Fatalf("open request = %+v", runtime.service.openRequest)
	}
}

func TestDoubleDashProtectsLeadingDashPositionals(t *testing.T) {
	runtime := newTestRuntime()
	if code := Run(context.Background(), []string{"clone", "--directory", "repo", "--", "-repository"}, runtime.options()); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if runtime.service.cloneRequest.URL != "-repository" || runtime.service.cloneRequest.Directory != "repo" {
		t.Fatalf("clone request = %+v", runtime.service.cloneRequest)
	}
}

func TestProfileRequiresOpen(t *testing.T) {
	for _, args := range [][]string{
		{"new", "fresh", "--profile", "shell"},
		{"clone", "https://example.test/repo.git", "--profile", "shell"},
		{"worktree", "project", "branch", "--profile", "shell"},
	} {
		runtime := newTestRuntime()
		runtime.service.projects = []domain.Project{{ID: "project"}}
		if code := Run(context.Background(), args, runtime.options()); code != 2 {
			t.Fatalf("Run(%v) exit = %d, stderr = %s", args, code, runtime.stderr.String())
		}
		if !strings.Contains(runtime.stderr.String(), "profile") || !strings.Contains(runtime.stderr.String(), "requires --open") {
			t.Fatalf("Run(%v) stderr = %q", args, runtime.stderr.String())
		}
	}
}

func TestVersionAndHelpDoNotLoadConfigOrDependencies(t *testing.T) {
	for _, args := range [][]string{{"version"}, {"--help"}} {
		runtime := newTestRuntime()
		options := runtime.options()
		options.LoadConfig = func(string) (config.LoadResult, error) {
			t.Fatal("informational command loaded config")
			return config.LoadResult{}, nil
		}
		options.LookPath = func(string) (string, error) {
			t.Fatal("informational command checked dependencies")
			return "", nil
		}
		if code := Run(context.Background(), args, options); code != 0 {
			t.Fatalf("Run(%v) exit = %d", args, code)
		}
	}
}

func TestConfigAndUsageErrorsHaveStableExitCodes(t *testing.T) {
	runtime := newTestRuntime()
	options := runtime.options()
	options.LoadConfig = func(string) (config.LoadResult, error) {
		return config.LoadResult{}, domain.FieldError(domain.ErrorCodeConfig, "config.load", "repoDirectory", "invalid")
	}
	if code := Run(context.Background(), []string{"projects"}, options); code != 3 {
		t.Fatalf("config exit = %d, stderr = %q", code, runtime.stderr.String())
	}

	runtime = newTestRuntime()
	if code := Run(context.Background(), []string{"open"}, runtime.options()); code != 2 {
		t.Fatalf("usage exit = %d, stderr = %q", code, runtime.stderr.String())
	}
}

type httpClientFunc func(*http.Request) (*http.Response, error)

func (f httpClientFunc) Do(request *http.Request) (*http.Response, error) { return f(request) }

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestRemoteCloneSendsBearerPayloadAndPrintsJSON(t *testing.T) {
	runtime := newTestRuntime()
	runtime.http = httpClientFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.String() != "https://host.test/v1/projects/clone" {
			t.Fatalf("request = %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer command-token" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		var payload domain.CloneRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.URL != "ssh://git/owner/repo" || payload.Directory != "repo-dir" || !payload.OpenOnFinish {
			t.Fatalf("payload = %+v", payload)
		}
		return jsonResponse(http.StatusAccepted, `{"id":"job-1","status":"queued"}`), nil
	})
	args := []string{"remote", "clone", "ssh://git/owner/repo", "--open", "--directory", "repo-dir", "--base-url", "https://host.test", "--token", "command-token"}
	if code := Run(context.Background(), args, runtime.options()); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if !strings.Contains(runtime.stdout.String(), "\"id\": \"job-1\"") {
		t.Fatalf("stdout = %q", runtime.stdout.String())
	}
}

func TestRemoteOpenSendsEncodedProjectAndOptions(t *testing.T) {
	runtime := newTestRuntime()
	runtime.http = httpClientFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodPost || request.URL.EscapedPath() != "/v1/projects/project%2Fid/open" {
			t.Fatalf("request = %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer token" || request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("headers = %#v", request.Header)
		}
		var payload struct {
			Profile     string `json:"profile"`
			NewInstance bool   `json:"newInstance"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Profile != "shell" || !payload.NewInstance {
			t.Fatalf("payload = %+v", payload)
		}
		return jsonResponse(http.StatusOK, `{"project":{"id":"project/id"},"window":{},"reused":false}`), nil
	})
	args := []string{"remote", "open", "project/id", "--profile", "shell", "--new-instance", "--base-url", "https://host.test", "--token", "token"}
	if code := Run(context.Background(), args, runtime.options()); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
}

func TestRemoteFailureMapsStatusToExitCode(t *testing.T) {
	runtime := newTestRuntime()
	runtime.http = httpClientFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusUnauthorized, `{"error":{"code":"unauthorized","message":"bad token"}}`), nil
	})
	code := Run(context.Background(), []string{"remote", "projects", "--token", "bad"}, runtime.options())
	if code != 6 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if !strings.Contains(runtime.stderr.String(), "HTTP 401: bad token") {
		t.Fatalf("stderr = %q", runtime.stderr.String())
	}
}

func TestRemoteUsesStandaloneServerConfigWhenConnectionIsOmitted(t *testing.T) {
	runtime := newTestRuntime()
	runtime.http = httpClientFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "http://127.0.0.1:8787/v1/projects" || request.Header.Get("Authorization") != "Bearer file-token" {
			t.Fatalf("request URL=%s authorization=%q", request.URL, request.Header.Get("Authorization"))
		}
		return jsonResponse(http.StatusOK, `{"projects":[]}`), nil
	})
	if code := Run(context.Background(), []string{"remote", "projects"}, runtime.options()); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
}

func TestExplicitRemoteConnectionDoesNotLoadMalformedLocalConfig(t *testing.T) {
	runtime := newTestRuntime()
	runtime.http = httpClientFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusOK, `{"projects":[]}`), nil
	})
	options := runtime.options()
	options.LoadConfig = func(string) (config.LoadResult, error) {
		t.Fatal("explicit remote connection loaded unrelated local config")
		return config.LoadResult{}, domain.FieldError(domain.ErrorCodeConfig, "config.load", "tmux.session", "malformed")
	}
	code := Run(context.Background(), []string{"remote", "projects", "--base-url", "https://host.test", "--token", "token"}, options)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
}

func TestRemoteHelpDoesNotRequireToken(t *testing.T) {
	runtime := newTestRuntime()
	options := runtime.options()
	options.ReadFile = func(string) ([]byte, error) {
		t.Fatal("help attempted to read token file")
		return nil, nil
	}
	if code := Run(context.Background(), []string{"remote", "--help"}, options); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if !strings.Contains(runtime.stdout.String(), "Usage: op remote") {
		t.Fatalf("stdout = %q", runtime.stdout.String())
	}
}

func TestServePrefersEnvironmentToken(t *testing.T) {
	runtime := newTestRuntime()
	runtime.lookup["OP_API_TOKEN"] = "environment-token"
	if code := Run(context.Background(), []string{"serve"}, runtime.options()); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if runtime.serverCalls != 1 || runtime.serverOptions.Token != "environment-token" || runtime.serverOptions.ListenAddress != "127.0.0.1:8787" {
		t.Fatalf("server calls=%d options=%+v", runtime.serverCalls, runtime.serverOptions)
	}
}

func TestServeFallsBackToConfiguredTokenFile(t *testing.T) {
	runtime := newTestRuntime()
	if code := Run(context.Background(), []string{"serve"}, runtime.options()); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if runtime.serverOptions.Token != "file-token" {
		t.Fatalf("token = %q", runtime.serverOptions.Token)
	}
}

func TestServeWiresNotificationIngestWhenEnabled(t *testing.T) {
	runtime := newTestRuntime()
	runtime.lookup["OP_API_TOKEN"] = "environment-token"
	options := runtime.options()
	options.LoadConfig = func(string) (config.LoadResult, error) {
		cfg := config.Defaults()
		cfg.RepoDirectory = "/repos"
		cfg.Notifications.Enabled = true
		cfg.Notifications.Ingest.Enabled = true
		return config.LoadResult{Config: cfg}, nil
	}
	if code := Run(context.Background(), []string{"serve"}, options); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if runtime.serverOptions.NotifyIngest == nil {
		t.Fatal("expected notification ingest to be attached to serve")
	}
}

func TestNotifyInstallClaudeWritesPlugin(t *testing.T) {
	runtime := newTestRuntime()
	target := t.TempDir()
	if code := Run(context.Background(), []string{"notify", "install-claude", "--target", target}, runtime.options()); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if _, err := os.Stat(filepath.Join(target, "scripts", "forward.sh")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(runtime.stdout.String(), "Installed claude plugin") {
		t.Fatalf("stdout = %q", runtime.stdout.String())
	}
}
