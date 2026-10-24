package tui

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/moutansos/op/internal/domain"
)

const cancellationTestTimeout = 5 * time.Second

type contextValueKey struct{}

type blockingOperation struct {
	started  chan struct{}
	contexts chan context.Context
	finished chan error
}

func newBlockingOperation() *blockingOperation {
	return &blockingOperation{
		started:  make(chan struct{}),
		contexts: make(chan context.Context, 1),
		finished: make(chan error, 1),
	}
}

func (o *blockingOperation) block(ctx context.Context) error {
	o.contexts <- ctx
	close(o.started)
	<-ctx.Done()
	err := ctx.Err()
	o.finished <- err
	return err
}

type blockingService struct {
	list     *blockingOperation
	create   *blockingOperation
	clone    *blockingOperation
	worktree *blockingOperation
	open     *blockingOperation
	action   *blockingOperation
	tmux     *blockingOperation
	stats    *blockingOperation
}

type completableActionService struct {
	*blockingService
	contexts chan context.Context
	release  chan struct{}
}

func newCompletableActionService() *completableActionService {
	return &completableActionService{
		blockingService: &blockingService{},
		contexts:        make(chan context.Context, 1),
		release:         make(chan struct{}),
	}
}

func (s *completableActionService) RunProjectAction(ctx context.Context, request domain.RunProjectActionRequest) (domain.RunProjectActionResult, error) {
	s.contexts <- ctx
	select {
	case <-s.release:
		return domain.RunProjectActionResult{
			Project: domain.Project{ID: request.ProjectID},
			Action:  request.Action,
			Started: true,
		}, nil
	case <-ctx.Done():
		return domain.RunProjectActionResult{}, ctx.Err()
	}
}

func (s *blockingService) ListProjects(ctx context.Context) ([]domain.Project, error) {
	if s.list == nil {
		return nil, nil
	}
	return nil, s.list.block(ctx)
}

func (s *blockingService) CreateProject(ctx context.Context, _ domain.CreateProjectRequest) (domain.CreateProjectResult, error) {
	if s.create == nil {
		return domain.CreateProjectResult{}, nil
	}
	return domain.CreateProjectResult{}, s.create.block(ctx)
}

func (s *blockingService) CloneProject(ctx context.Context, _ domain.CloneRequest) (domain.CloneResult, error) {
	if s.clone == nil {
		return domain.CloneResult{}, nil
	}
	return domain.CloneResult{}, s.clone.block(ctx)
}

func (s *blockingService) CreateWorktree(ctx context.Context, _ domain.CreateWorktreeRequest) (domain.CreateWorktreeResult, error) {
	if s.worktree == nil {
		return domain.CreateWorktreeResult{}, nil
	}
	return domain.CreateWorktreeResult{}, s.worktree.block(ctx)
}

func (s *blockingService) OpenProject(ctx context.Context, _ domain.OpenProjectRequest) (domain.OpenProjectResult, error) {
	if s.open == nil {
		return domain.OpenProjectResult{}, nil
	}
	return domain.OpenProjectResult{}, s.open.block(ctx)
}

func (*blockingService) SelectPane(context.Context, domain.SelectPaneRequest) (domain.SelectPaneResult, error) {
	return domain.SelectPaneResult{}, nil
}

func (s *blockingService) RunProjectAction(ctx context.Context, _ domain.RunProjectActionRequest) (domain.RunProjectActionResult, error) {
	if s.action == nil {
		return domain.RunProjectActionResult{}, nil
	}
	return domain.RunProjectActionResult{}, s.action.block(ctx)
}

func (*blockingService) EnsureMainSession(context.Context) (domain.EnsureMainSessionResult, error) {
	return domain.EnsureMainSessionResult{}, nil
}

func (s *blockingService) GetTmuxSnapshot(ctx context.Context) (domain.TmuxSnapshot, error) {
	if s.tmux == nil {
		return domain.TmuxSnapshot{}, nil
	}
	return domain.TmuxSnapshot{}, s.tmux.block(ctx)
}

func (s *blockingService) GetStatsSnapshot(ctx context.Context) (domain.StatsSnapshot, error) {
	if s.stats == nil {
		return domain.StatsSnapshot{}, nil
	}
	return domain.StatsSnapshot{}, s.stats.block(ctx)
}

func cancellationOptions() Options {
	return Options{
		ProjectRefreshInterval: time.Hour,
		TmuxRefreshInterval:    time.Hour,
		StatsRefreshInterval:   time.Hour,
		RefreshTimeout:         time.Hour,
		OperationTimeout:       time.Hour,
	}
}

func receive[T any](t *testing.T, channel <-chan T) T {
	t.Helper()
	timer := time.NewTimer(cancellationTestTimeout)
	defer timer.Stop()
	select {
	case value := <-channel:
		return value
	case <-timer.C:
		t.Fatal("timed out waiting for synchronized test event")
		var zero T
		return zero
	}
}

func commandError(t *testing.T, msg tea.Msg) error {
	t.Helper()
	switch msg := msg.(type) {
	case createFinishedMsg:
		return msg.err
	case cloneFinishedMsg:
		return msg.err
	case worktreeFinishedMsg:
		return msg.err
	case openFinishedMsg:
		return msg.err
	case actionFinishedMsg:
		return msg.err
	case projectsLoadedMsg:
		return msg.err
	case tmuxLoadedMsg:
		return msg.err
	case statsLoadedMsg:
		return msg.err
	default:
		t.Fatalf("unexpected command message %T", msg)
		return nil
	}
}

func TestQuitCancelsPendingMutationCommands(t *testing.T) {
	tests := []struct {
		name    string
		command func(Model) tea.Cmd
	}{
		{
			name:    "create",
			command: func(model Model) tea.Cmd { return model.createProjectCmd("new-project") },
		},
		{
			name:    "clone",
			command: func(model Model) tea.Cmd { return model.cloneProjectCmd("https://example.test/repo.git", "") },
		},
		{
			name:    "worktree",
			command: func(model Model) tea.Cmd { return model.createWorktreeCmd("project", "feature/test", "") },
		},
		{
			name:    "open",
			command: func(model Model) tea.Cmd { return model.openProjectCmd("project", model.options.DefaultProfile) },
		},
		{
			name:    "action",
			command: func(model Model) tea.Cmd { return model.runActionCmd("project", "code") },
		},
	}
	keys := []struct {
		name string
		key  tea.KeyMsg
	}{
		{name: "q", key: tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}},
		{name: "ctrl+c", key: tea.KeyMsg{Type: tea.KeyCtrlC}},
	}

	for _, test := range tests {
		for _, key := range keys {
			t.Run(test.name+"/"+key.name, func(t *testing.T) {
				operation := newBlockingOperation()
				service := &blockingService{}
				switch test.name {
				case "create":
					service.create = operation
				case "clone":
					service.clone = operation
				case "worktree":
					service.worktree = operation
				case "open":
					service.open = operation
				case "action":
					service.action = operation
				}
				parent := context.WithValue(context.Background(), contextValueKey{}, "parent")
				model := NewModel(parent, service, cancellationOptions())
				t.Cleanup(model.cancel)

				result := make(chan tea.Msg, 1)
				go func() { result <- test.command(model)() }()
				operationContext := receive(t, operation.contexts)
				if got := operationContext.Value(contextValueKey{}); got != "parent" {
					t.Fatalf("operation context value = %v, want parent", got)
				}

				_, quit := model.Update(key.key)
				if quit == nil {
					t.Fatal("quit key returned no command")
				}
				if _, ok := quit().(tea.QuitMsg); !ok {
					t.Fatal("quit key did not return tea.Quit")
				}
				if parent.Err() != nil {
					t.Fatalf("quit canceled the caller context: %v", parent.Err())
				}
				if err := receive(t, operation.finished); !errors.Is(err, context.Canceled) {
					t.Fatalf("operation error = %v, want context canceled", err)
				}
				if err := commandError(t, receive(t, result)); !errors.Is(err, context.Canceled) {
					t.Fatalf("command error = %v, want context canceled", err)
				}
			})
		}
	}
}

func TestRefreshCommandsInheritDashboardCancellation(t *testing.T) {
	tests := []struct {
		name    string
		service func(*blockingOperation) *blockingService
		command func(Model) tea.Cmd
	}{
		{name: "projects", service: func(op *blockingOperation) *blockingService { return &blockingService{list: op} }, command: Model.loadProjectsCmd},
		{name: "tmux", service: func(op *blockingOperation) *blockingService { return &blockingService{tmux: op} }, command: Model.loadTmuxCmd},
		{name: "stats", service: func(op *blockingOperation) *blockingService { return &blockingService{stats: op} }, command: Model.loadStatsCmd},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := newBlockingOperation()
			parent := context.WithValue(context.Background(), contextValueKey{}, "parent")
			model := NewModel(parent, test.service(operation), cancellationOptions())
			t.Cleanup(model.cancel)
			result := make(chan tea.Msg, 1)
			go func() { result <- test.command(model)() }()

			operationContext := receive(t, operation.contexts)
			if got := operationContext.Value(contextValueKey{}); got != "parent" {
				t.Fatalf("refresh context value = %v, want parent", got)
			}
			model.quit()
			if parent.Err() != nil {
				t.Fatalf("quit canceled the caller context: %v", parent.Err())
			}
			if err := receive(t, operation.finished); !errors.Is(err, context.Canceled) {
				t.Fatalf("refresh error = %v, want context canceled", err)
			}
			if err := commandError(t, receive(t, result)); !errors.Is(err, context.Canceled) {
				t.Fatalf("command error = %v, want context canceled", err)
			}
		})
	}
}

func TestTerminalExecActionIgnoresOperationTimeoutAndCompletes(t *testing.T) {
	service := newCompletableActionService()
	options := cancellationOptions()
	options.OperationTimeout = time.Nanosecond
	model := NewModel(context.Background(), service, options)
	t.Cleanup(model.cancel)
	command := &serviceExecCommand{
		ctx:     model.ctx,
		service: service,
		request: domain.RunProjectActionRequest{ProjectID: "project", Action: "shell"},
	}
	result := make(chan error, 1)
	go func() { result <- command.Run() }()

	operationContext := receive(t, service.contexts)
	if deadline, ok := operationContext.Deadline(); ok {
		t.Fatalf("terminal action context has deadline %v", deadline)
	}
	close(service.release)
	if err := receive(t, result); err != nil {
		t.Fatalf("Exec Run error = %v, want normal completion", err)
	}
}

func TestTerminalExecActionInheritsModelAndProgramCancellation(t *testing.T) {
	tests := []struct {
		name   string
		cancel func(Model, context.CancelFunc)
	}{
		{name: "model", cancel: func(model Model, _ context.CancelFunc) { model.quit() }},
		{name: "program", cancel: func(_ Model, cancel context.CancelFunc) { cancel() }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			operation := newBlockingOperation()
			service := &blockingService{action: operation}
			parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), contextValueKey{}, "parent"))
			t.Cleanup(cancelParent)
			model := NewModel(parent, service, cancellationOptions())
			t.Cleanup(model.cancel)
			command := &serviceExecCommand{
				ctx:     model.ctx,
				service: service,
				request: domain.RunProjectActionRequest{ProjectID: "project", Action: "shell"},
			}
			result := make(chan error, 1)
			go func() { result <- command.Run() }()

			operationContext := receive(t, operation.contexts)
			if got := operationContext.Value(contextValueKey{}); got != "parent" {
				t.Fatalf("exec context value = %v, want parent", got)
			}
			test.cancel(model, cancelParent)
			if err := receive(t, operation.finished); !errors.Is(err, context.Canceled) {
				t.Fatalf("exec operation error = %v, want context canceled", err)
			}
			if err := receive(t, result); !errors.Is(err, context.Canceled) {
				t.Fatalf("Exec Run error = %v, want context canceled", err)
			}
			if test.name == "model" && parent.Err() != nil {
				t.Fatalf("model cancellation canceled the caller context: %v", parent.Err())
			}
		})
	}
}

func TestNonterminalMutationUsesOperationTimeout(t *testing.T) {
	operation := newBlockingOperation()
	service := &blockingService{create: operation}
	options := cancellationOptions()
	options.OperationTimeout = time.Nanosecond
	model := NewModel(context.Background(), service, options)
	t.Cleanup(model.cancel)
	result := make(chan tea.Msg, 1)
	go func() { result <- model.createProjectCmd("new-project")() }()

	operationContext := receive(t, operation.contexts)
	if _, ok := operationContext.Deadline(); !ok {
		t.Fatal("nonterminal mutation context has no deadline")
	}
	if err := receive(t, operation.finished); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("mutation error = %v, want deadline exceeded", err)
	}
	if err := commandError(t, receive(t, result)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("command error = %v, want deadline exceeded", err)
	}
}

func TestTickCommandsExitWhenDashboardIsCanceled(t *testing.T) {
	model := NewModel(context.Background(), &blockingService{}, cancellationOptions())
	commands := []tea.Cmd{model.projectTickCmd(), model.tmuxTickCmd(), model.statsTickCmd()}
	results := make([]chan tea.Msg, len(commands))
	for index, command := range commands {
		results[index] = make(chan tea.Msg, 1)
		go func(index int, command tea.Cmd) { results[index] <- command() }(index, command)
	}
	model.quit()
	for index, result := range results {
		if msg := receive(t, result); msg != nil {
			t.Fatalf("tick command %d returned %T after cancellation, want nil", index, msg)
		}
	}
}

type gatedReader struct {
	gate <-chan struct{}
	data []byte
	err  error
}

func (r *gatedReader) Read(buffer []byte) (int, error) {
	<-r.gate
	if len(r.data) == 0 {
		if r.err != nil {
			err := r.err
			r.err = nil
			return 0, err
		}
		return 0, io.EOF
	}
	n := copy(buffer, r.data)
	r.data = r.data[n:]
	return n, nil
}

func runForTest(ctx context.Context, service domain.Service, input io.Reader) error {
	return run(ctx, service, cancellationOptions(),
		tea.WithInput(input),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
		tea.WithoutSignalHandler(),
	)
}

func TestRunCancelsPendingCommandsOnNormalQuit(t *testing.T) {
	operation := newBlockingOperation()
	service := &blockingService{list: operation}
	parent := context.Background()
	err := runForTest(parent, service, &gatedReader{gate: operation.started, data: []byte{byte(tea.KeyCtrlC)}})
	if err != nil {
		t.Fatalf("run error = %v", err)
	}
	if parent.Err() != nil {
		t.Fatalf("normal quit canceled caller context: %v", parent.Err())
	}
	if err := receive(t, operation.finished); !errors.Is(err, context.Canceled) {
		t.Fatalf("pending command error = %v, want context canceled", err)
	}
}

func TestRunCancelsPendingCommandsOnExternalCancellation(t *testing.T) {
	operation := newBlockingOperation()
	service := &blockingService{list: operation}
	parent, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runForTest(parent, service, nil) }()
	receive(t, operation.contexts)
	cancel()

	if err := receive(t, result); !errors.Is(err, tea.ErrProgramKilled) || !errors.Is(err, context.Canceled) {
		t.Fatalf("run error = %v, want program killed and context canceled", err)
	}
	if err := receive(t, operation.finished); !errors.Is(err, context.Canceled) {
		t.Fatalf("pending command error = %v, want context canceled", err)
	}
}

func TestRunCancelsPendingCommandsOnProgramError(t *testing.T) {
	operation := newBlockingOperation()
	service := &blockingService{list: operation}
	parent := context.Background()
	readErr := errors.New("input failed")
	err := runForTest(parent, service, &gatedReader{gate: operation.started, err: readErr})
	if !errors.Is(err, tea.ErrProgramKilled) || !errors.Is(err, readErr) {
		t.Fatalf("run error = %v, want program killed and input failure", err)
	}
	if parent.Err() != nil {
		t.Fatalf("program error canceled caller context: %v", parent.Err())
	}
	if err := receive(t, operation.finished); !errors.Is(err, context.Canceled) {
		t.Fatalf("pending command error = %v, want context canceled", err)
	}
}
