package app_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moutansos/op/internal/app"
	"github.com/moutansos/op/internal/config"
	"github.com/moutansos/op/internal/domain"
	gitrepo "github.com/moutansos/op/internal/git"
	tmuxmanager "github.com/moutansos/op/internal/tmux"
)

func TestServiceSatisfiesDomainService(t *testing.T) {
	var _ domain.Service = new(app.Service)
	_ = newTestService(t, testConfig(), testDependencies())
}

func TestServiceConstructorsRejectReservedCustomCommandNames(t *testing.T) {
	tests := []struct {
		name      string
		construct func(config.Config) error
	}{
		{
			name: "production",
			construct: func(cfg config.Config) error {
				_, err := app.New(context.Background(), cfg, app.Options{})
				return err
			},
		},
		{
			name: "dependencies",
			construct: func(cfg config.Config) error {
				_, err := app.NewWithDependencies(cfg, testDependencies(), app.Options{})
				return err
			},
		},
	}
	for _, test := range tests {
		for _, commandName := range []string{"nvim", "worktree:feature"} {
			t.Run(test.name+"/"+commandName, func(t *testing.T) {
				cfg := testConfig()
				cfg.CustomCommands = []config.CustomCommand{{Name: commandName, Command: "custom"}}
				err := test.construct(cfg)
				var typed *domain.Error
				if !errors.As(err, &typed) || typed.Code != domain.ErrorCodeConfig || typed.Field != "customCommands[0].name" {
					t.Fatalf("constructor error = %#v, want config customCommands[0].name error", err)
				}
			})
		}
	}
}

func TestCreateProjectUsesSafePathRefreshesAndOpens(t *testing.T) {
	catalog := newFakeCatalog()
	created := domain.Project{ID: "created-id", Name: "new-project", Path: "/repos/new-project", Kind: domain.ProjectKindRepository}
	catalog.byName[created.Name] = created
	repository := &fakeRepository{}
	tmux := &fakeTmux{}
	service := newTestService(t, testConfig(), dependencies(catalog, repository, &fakeLauncher{}, tmux, &fakeStats{}))

	result, err := service.CreateProject(context.Background(), domain.CreateProjectRequest{
		Name: "new-project", OpenOnFinish: true,
	})
	if err != nil {
		t.Fatalf("CreateProject() error = %v", err)
	}
	if repository.initPath != created.Path {
		t.Fatalf("Init path = %q, want %q", repository.initPath, created.Path)
	}
	if catalog.createNames[0] != created.Name || catalog.resolveNames[0] != created.Name {
		t.Fatalf("catalog calls create=%v resolve=%v", catalog.createNames, catalog.resolveNames)
	}
	if result.Project.ID != created.ID || result.Project.Path != created.Path || result.Open == nil {
		t.Fatalf("CreateProject() = %+v", result)
	}
	if tmux.openRequests[0].Project.ID != created.ID || tmux.openRequests[0].Profile != "default" {
		t.Fatalf("OpenProjectWindow request = %+v", tmux.openRequests[0])
	}
}

func TestCloneProjectClonesDirectlyToValidatedFinalDestination(t *testing.T) {
	catalog := newFakeCatalog()
	cloned := domain.Project{ID: "clone-id", Name: "repo", Path: "/repos/repo", Kind: domain.ProjectKindRepository}
	catalog.byName[cloned.Name] = cloned
	repository := &fakeRepository{}
	tmux := &fakeTmux{}
	service := newTestService(t, testConfig(), dependencies(catalog, repository, &fakeLauncher{}, tmux, &fakeStats{}))

	result, err := service.CloneProject(context.Background(), domain.CloneRequest{
		URL: "https://example.com/org/repo.git", OpenOnFinish: true, Profile: "default",
	})
	if err != nil {
		t.Fatalf("CloneProject() error = %v", err)
	}
	if len(repository.cloneOptions) != 1 {
		t.Fatalf("Clone calls = %d", len(repository.cloneOptions))
	}
	options := repository.cloneOptions[0]
	if options.ParentDirectory != "/repos" || options.Directory != "repo" {
		t.Fatalf("Clone options = %+v", options)
	}
	if filepath.Base(options.ParentDirectory) == ".tmp" || options.Directory != cloned.Name {
		t.Fatalf("clone did not use final destination: %+v", options)
	}
	if result.Project.ID != cloned.ID || result.Open == nil || tmux.openRequests[0].Profile != "default" {
		t.Fatalf("CloneProject() = %+v; tmux request = %+v", result, tmux.openRequests[0])
	}
}

func TestCreateProjectRejectsExistingDestinationAndDuplicateCreateConflicts(t *testing.T) {
	root := t.TempDir()
	catalog := newFakeCatalog()
	catalog.root = root
	created := domain.Project{ID: "created-id", Name: "project", Path: filepath.Join(root, "project"), Kind: domain.ProjectKindRepository}
	catalog.byName[created.Name] = created
	repository := &fakeRepository{init: func(path string) error { return os.Mkdir(path, 0o755) }}
	service := newTestService(t, testConfig(), dependencies(catalog, repository, &fakeLauncher{}, &fakeTmux{}, &fakeStats{}))

	if _, err := service.CreateProject(context.Background(), domain.CreateProjectRequest{Name: created.Name}); err != nil {
		t.Fatalf("first CreateProject() error = %v", err)
	}
	_, err := service.CreateProject(context.Background(), domain.CreateProjectRequest{Name: created.Name})
	if !domain.IsCode(err, domain.ErrorCodeConflict) {
		t.Fatalf("duplicate CreateProject() error = %v, want conflict", err)
	}
	if repository.initCalls != 1 {
		t.Fatalf("Init() calls = %d, want 1", repository.initCalls)
	}
}

func TestCreateProjectRejectsPreexistingDestinationBeforeGit(t *testing.T) {
	root := t.TempDir()
	catalog := newFakeCatalog()
	catalog.root = root
	if err := os.Mkdir(filepath.Join(root, "existing"), 0o755); err != nil {
		t.Fatal(err)
	}
	repository := &fakeRepository{}
	service := newTestService(t, testConfig(), dependencies(catalog, repository, &fakeLauncher{}, &fakeTmux{}, &fakeStats{}))

	_, err := service.CreateProject(context.Background(), domain.CreateProjectRequest{Name: "existing"})
	if !domain.IsCode(err, domain.ErrorCodeConflict) {
		t.Fatalf("CreateProject() error = %v, want conflict", err)
	}
	if repository.initCalls != 0 {
		t.Fatalf("Init() calls = %d, want 0", repository.initCalls)
	}
}

func TestUnknownProfileIsRejectedBeforeMutation(t *testing.T) {
	repository := &fakeRepository{}
	service := newTestService(t, testConfig(), dependencies(newFakeCatalog(), repository, &fakeLauncher{}, &fakeTmux{}, &fakeStats{}))
	_, err := service.CloneProject(context.Background(), domain.CloneRequest{
		URL: "https://example.com/repository.git", Profile: "unknown-layout",
	})
	if !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
		t.Fatalf("CloneProject() error = %v, want invalid_argument", err)
	}
	if repository.operationCount() != 0 {
		t.Fatalf("git operation count = %d, want 0", repository.operationCount())
	}
}

func TestUnsafeDestinationsStopBeforeGit(t *testing.T) {
	unsafe := domain.FieldError(domain.ErrorCodeInvalidArgument, "project.path", "directory", "unsafe")
	tests := []struct {
		name string
		run  func(*app.Service) error
		set  func(*fakeCatalog)
	}{
		{
			name: "create",
			run: func(service *app.Service) error {
				_, err := service.CreateProject(context.Background(), domain.CreateProjectRequest{Name: "../bad"})
				return err
			},
			set: func(catalog *fakeCatalog) { catalog.createErr = unsafe },
		},
		{
			name: "clone",
			run: func(service *app.Service) error {
				_, err := service.CloneProject(context.Background(), domain.CloneRequest{URL: "https://example.com/repo", Directory: "../bad"})
				return err
			},
			set: func(catalog *fakeCatalog) { catalog.cloneErr = unsafe },
		},
		{
			name: "worktree source",
			run: func(service *app.Service) error {
				_, err := service.CreateWorktree(context.Background(), domain.CreateWorktreeRequest{ProjectID: "p1", Branch: "feature"})
				return err
			},
			set: func(catalog *fakeCatalog) { catalog.validateErr = unsafe },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := newFakeCatalog()
			catalog.byID["p1"] = domain.Project{ID: "p1", Name: "repo", Path: "/repos/repo"}
			test.set(catalog)
			repository := &fakeRepository{}
			service := newTestService(t, testConfig(), dependencies(catalog, repository, &fakeLauncher{}, &fakeTmux{}, &fakeStats{}))
			err := test.run(service)
			if !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
				t.Fatalf("error = %v, want invalid_argument", err)
			}
			if repository.operationCount() != 0 {
				t.Fatalf("git operation count = %d, want 0", repository.operationCount())
			}
		})
	}
}

func TestWorktreeRejectsNestedCatalogSourceBeforeGit(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.byID["nested"] = domain.Project{ID: "nested", Name: "repo", Path: "/repos/group/repo"}
	repository := &fakeRepository{}
	service := newTestService(t, testConfig(), dependencies(catalog, repository, &fakeLauncher{}, &fakeTmux{}, &fakeStats{}))

	_, err := service.CreateWorktree(context.Background(), domain.CreateWorktreeRequest{ProjectID: "nested", Branch: "feature"})
	if !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
		t.Fatalf("CreateWorktree() error = %v, want invalid_argument", err)
	}
	if repository.operationCount() != 0 {
		t.Fatalf("git operation count = %d, want 0", repository.operationCount())
	}
}

func TestCreateWorktreeValidatesResultRefreshesAndOpens(t *testing.T) {
	catalog := newFakeCatalog()
	source := domain.Project{ID: "p1", Name: "repo", Path: "/repos/repo", Kind: domain.ProjectKindRepository}
	created := domain.Project{ID: "w1", Name: "repo-feature-one", Path: "/repos/repo-feature-one", Kind: domain.ProjectKindRepository}
	catalog.byID[source.ID] = source
	catalog.byName[created.Name] = created
	repository := &fakeRepository{worktreeResult: gitrepo.WorktreeResult{Path: created.Path, Directory: created.Name, Branch: "feature/one"}}
	tmux := &fakeTmux{}
	service := newTestService(t, testConfig(), dependencies(catalog, repository, &fakeLauncher{}, tmux, &fakeStats{}))

	result, err := service.CreateWorktree(context.Background(), domain.CreateWorktreeRequest{
		ProjectID: source.ID, Branch: "feature/one", OpenOnFinish: true,
	})
	if err != nil {
		t.Fatalf("CreateWorktree() error = %v", err)
	}
	if repository.worktreeOptions.Directory != created.Name || repository.worktreeOptions.Repository != source.Path {
		t.Fatalf("worktree options = %+v", repository.worktreeOptions)
	}
	if result.Project.Kind != domain.ProjectKindWorktree || result.Open == nil {
		t.Fatalf("CreateWorktree() = %+v", result)
	}
	if tmux.openRequests[0].Project.Kind != domain.ProjectKindWorktree {
		t.Fatalf("opened project = %+v", tmux.openRequests[0].Project)
	}
}

func TestSameDestinationCloneOperationsAreSerialized(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.byName["repo"] = domain.Project{ID: "p1", Name: "repo", Path: "/repos/repo"}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	var active atomic.Int32
	var maximum atomic.Int32
	repository := &fakeRepository{clone: func(_ context.Context, options gitrepo.CloneOptions) (gitrepo.CloneResult, error) {
		current := active.Add(1)
		for current > maximum.Load() && !maximum.CompareAndSwap(maximum.Load(), current) {
		}
		entered <- struct{}{}
		<-release
		active.Add(-1)
		return gitrepo.CloneResult{Directory: options.Directory, Path: filepath.Join(options.ParentDirectory, options.Directory)}, nil
	}}
	service := newTestService(t, testConfig(), dependencies(catalog, repository, &fakeLauncher{}, &fakeTmux{}, &fakeStats{}))
	request := domain.CloneRequest{URL: "https://example.com/repo", Directory: "repo"}
	errorsSeen := make(chan error, 2)
	go func() { _, err := service.CloneProject(context.Background(), request); errorsSeen <- err }()
	<-entered
	go func() { _, err := service.CloneProject(context.Background(), request); errorsSeen <- err }()

	select {
	case <-entered:
		t.Fatal("second clone entered git before first clone completed")
	case <-time.After(30 * time.Millisecond):
	}
	release <- struct{}{}
	<-entered
	release <- struct{}{}
	for range 2 {
		if err := <-errorsSeen; err != nil {
			t.Fatalf("CloneProject() error = %v", err)
		}
	}
	if maximum.Load() != 1 {
		t.Fatalf("maximum concurrent clones = %d, want 1", maximum.Load())
	}
}

func TestSameDestinationOperationsAreSerializedAcrossServices(t *testing.T) {
	root := t.TempDir()
	catalog := newFakeCatalog()
	catalog.root = root
	catalog.byName["repo"] = domain.Project{ID: "p1", Name: "repo", Path: filepath.Join(root, "repo")}
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	repository := &fakeRepository{clone: func(_ context.Context, options gitrepo.CloneOptions) (gitrepo.CloneResult, error) {
		entered <- struct{}{}
		<-release
		return gitrepo.CloneResult{Directory: options.Directory, Path: filepath.Join(options.ParentDirectory, options.Directory)}, nil
	}}
	options := app.Options{OperationLockDirectory: filepath.Join(t.TempDir(), "locks")}
	deps := dependencies(catalog, repository, &fakeLauncher{}, &fakeTmux{}, &fakeStats{})
	first := newTestServiceWithOptions(t, testConfig(), deps, options)
	second := newTestServiceWithOptions(t, testConfig(), deps, options)
	request := domain.CloneRequest{URL: "https://example.com/repo", Directory: "repo"}
	errorsSeen := make(chan error, 2)
	go func() { _, err := first.CloneProject(context.Background(), request); errorsSeen <- err }()
	<-entered
	go func() { _, err := second.CloneProject(context.Background(), request); errorsSeen <- err }()

	select {
	case <-entered:
		t.Fatal("second service entered git while the first held the operation lock")
	case <-time.After(30 * time.Millisecond):
	}
	release <- struct{}{}
	<-entered
	release <- struct{}{}
	for range 2 {
		if err := <-errorsSeen; err != nil {
			t.Fatalf("CloneProject() error = %v", err)
		}
	}
}

func TestDifferentProjectTmuxMutationsAreSerializedAcrossServices(t *testing.T) {
	catalog := newFakeCatalog()
	firstProject := domain.Project{ID: "p1", Name: "one", Path: "/repos/one"}
	secondProject := domain.Project{ID: "p2", Name: "two", Path: "/repos/two"}
	catalog.byID[firstProject.ID] = firstProject
	catalog.byID[secondProject.ID] = secondProject
	tmux := newSerialTmux()
	options := app.Options{OperationLockDirectory: filepath.Join(t.TempDir(), "locks")}
	deps := dependencies(catalog, &fakeRepository{}, &fakeLauncher{}, tmux, &fakeStats{})
	first := newTestServiceWithOptions(t, testConfig(), deps, options)
	second := newTestServiceWithOptions(t, testConfig(), deps, options)
	errorsSeen := make(chan error, 2)

	go func() {
		_, err := first.OpenProject(context.Background(), domain.OpenProjectRequest{ProjectID: firstProject.ID})
		errorsSeen <- err
	}()
	<-tmux.entered
	go func() {
		_, err := second.OpenProject(context.Background(), domain.OpenProjectRequest{ProjectID: secondProject.ID})
		errorsSeen <- err
	}()

	select {
	case <-tmux.entered:
		t.Fatal("different-project tmux mutation entered while the first service held the session lock")
	case <-time.After(30 * time.Millisecond):
	}
	tmux.release <- struct{}{}
	select {
	case <-tmux.openEntered:
	case <-time.After(time.Second):
		t.Fatal("first service did not advance to opening its project window")
	}
	select {
	case <-tmux.entered:
		t.Fatal("second ensure entered while the first service was opening its project window")
	case <-time.After(30 * time.Millisecond):
	}
	tmux.openRelease <- struct{}{}
	select {
	case <-tmux.entered:
	case <-time.After(time.Second):
		t.Fatal("second service deadlocked waiting for the tmux session lock")
	}
	tmux.release <- struct{}{}
	select {
	case <-tmux.openEntered:
	case <-time.After(time.Second):
		t.Fatal("second service did not advance to opening its project window")
	}
	tmux.openRelease <- struct{}{}
	for range 2 {
		select {
		case err := <-errorsSeen:
			if err != nil {
				t.Fatalf("OpenProject() error = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent different-project opens did not complete")
		}
	}
	if tmux.maximum.Load() != 1 {
		t.Fatalf("maximum concurrent tmux transactions = %d, want 1", tmux.maximum.Load())
	}
}

func TestTmuxSessionLockWaitHonorsContextCancellation(t *testing.T) {
	catalog := newFakeCatalog()
	firstProject := domain.Project{ID: "p1", Name: "one", Path: "/repos/one"}
	secondProject := domain.Project{ID: "p2", Name: "two", Path: "/repos/two"}
	catalog.byID[firstProject.ID] = firstProject
	catalog.byID[secondProject.ID] = secondProject
	tmux := newSerialTmux()
	options := app.Options{OperationLockDirectory: filepath.Join(t.TempDir(), "locks")}
	deps := dependencies(catalog, &fakeRepository{}, &fakeLauncher{}, tmux, &fakeStats{})
	first := newTestServiceWithOptions(t, testConfig(), deps, options)
	second := newTestServiceWithOptions(t, testConfig(), deps, options)
	firstDone := make(chan error, 1)
	go func() {
		_, err := first.OpenProject(context.Background(), domain.OpenProjectRequest{ProjectID: firstProject.ID})
		firstDone <- err
	}()
	<-tmux.entered

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := second.OpenProject(ctx, domain.OpenProjectRequest{ProjectID: secondProject.ID})
	if !domain.IsCode(err, domain.ErrorCodeTimeout) {
		t.Fatalf("canceled lock wait error = %v, want timeout", err)
	}
	tmux.release <- struct{}{}
	select {
	case <-tmux.openEntered:
	case <-time.After(time.Second):
		t.Fatal("first service did not advance after canceled waiter")
	}
	tmux.openRelease <- struct{}{}
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("first OpenProject() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first open deadlocked after canceled waiter")
	}
}

func TestTmuxBackendCancellationReleasesCrossProcessSessionLock(t *testing.T) {
	tmux := newSerialTmux()
	options := app.Options{OperationLockDirectory: filepath.Join(t.TempDir(), "locks")}
	deps := dependencies(newFakeCatalog(), &fakeRepository{}, &fakeLauncher{}, tmux, &fakeStats{})
	first := newTestServiceWithOptions(t, testConfig(), deps, options)
	second := newTestServiceWithOptions(t, testConfig(), deps, options)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	_, err := first.EnsureMainSession(ctx)
	if !domain.IsCode(err, domain.ErrorCodeTimeout) {
		t.Fatalf("stalled backend error = %v, want timeout", err)
	}
	<-tmux.entered
	secondDone := make(chan error, 1)
	go func() {
		_, callErr := second.EnsureMainSession(context.Background())
		secondDone <- callErr
	}()
	select {
	case <-tmux.entered:
	case <-time.After(time.Second):
		t.Fatal("second service did not acquire session lock after backend cancellation")
	}
	tmux.release <- struct{}{}
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatalf("second EnsureMainSession() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("second service did not release session lock")
	}
}

func TestOutsideAttachReleasesSessionLockBeforeInteractiveExecution(t *testing.T) {
	project := domain.Project{ID: "p1", Name: "one", Path: "/repos/one"}
	catalog := newFakeCatalog()
	catalog.byID[project.ID] = project
	tmux := newPlannedAttachTmux(tmuxmanager.AttachModeInteractive)
	options := app.Options{OperationLockDirectory: filepath.Join(t.TempDir(), "locks")}
	deps := dependencies(catalog, &fakeRepository{}, &fakeLauncher{}, tmux, &fakeStats{})
	first := newTestServiceWithOptions(t, testConfig(), deps, options)
	second := newTestServiceWithOptions(t, testConfig(), deps, options)
	attachDone := make(chan error, 1)
	go func() { attachDone <- first.AttachOrSwitch(context.Background()) }()
	<-tmux.attachEntered

	openDone := make(chan error, 1)
	go func() {
		_, err := second.OpenProject(context.Background(), domain.OpenProjectRequest{ProjectID: project.ID})
		openDone <- err
	}()
	select {
	case <-tmux.mutationEntered:
	case <-time.After(time.Second):
		t.Fatal("project mutation could not acquire session lock while outside attach was running")
	}
	tmux.mutationRelease <- struct{}{}
	select {
	case <-tmux.openEntered:
	case <-time.After(time.Second):
		t.Fatal("project open did not run while outside attach was active")
	}
	if err := <-openDone; err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	select {
	case err := <-attachDone:
		t.Fatalf("interactive attach ended before release: %v", err)
	default:
	}
	tmux.attachRelease <- struct{}{}
	if err := <-attachDone; err != nil {
		t.Fatalf("AttachOrSwitch() error = %v", err)
	}
}

func TestInsideSwitchRetainsSessionLockThroughExecution(t *testing.T) {
	project := domain.Project{ID: "p1", Name: "one", Path: "/repos/one"}
	catalog := newFakeCatalog()
	catalog.byID[project.ID] = project
	tmux := newPlannedAttachTmux(tmuxmanager.AttachModeSwitch)
	options := app.Options{OperationLockDirectory: filepath.Join(t.TempDir(), "locks")}
	deps := dependencies(catalog, &fakeRepository{}, &fakeLauncher{}, tmux, &fakeStats{})
	first := newTestServiceWithOptions(t, testConfig(), deps, options)
	second := newTestServiceWithOptions(t, testConfig(), deps, options)
	attachDone := make(chan error, 1)
	go func() { attachDone <- first.AttachOrSwitch(context.Background()) }()
	<-tmux.attachEntered

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := second.OpenProject(ctx, domain.OpenProjectRequest{ProjectID: project.ID})
	if !domain.IsCode(err, domain.ErrorCodeTimeout) {
		t.Fatalf("concurrent open error = %v, want timeout", err)
	}
	select {
	case <-tmux.mutationEntered:
		t.Fatal("project mutation entered while inside switch held session lock")
	default:
	}
	tmux.attachRelease <- struct{}{}
	if err := <-attachDone; err != nil {
		t.Fatalf("AttachOrSwitch() error = %v", err)
	}
}

func TestOpenProjectPullPolicyAndCleanliness(t *testing.T) {
	tests := []struct {
		name      string
		state     gitrepo.State
		ctx       func() context.Context
		enabled   bool
		wantState int
		wantPull  int
	}{
		{name: "clean and enabled", state: gitrepo.State{Branch: "main", Git: domain.GitStateClean}, enabled: true, wantState: 1, wantPull: 1},
		{name: "dirty", state: gitrepo.State{Branch: "main", Git: domain.GitStateDirty}, enabled: true, wantState: 1},
		{name: "raw folder", state: gitrepo.State{Git: domain.GitStateNotRepository}, enabled: true, wantState: 1},
		{name: "globally disabled", state: gitrepo.State{Branch: "main", Git: domain.GitStateClean}},
		{name: "request disabled", state: gitrepo.State{Branch: "main", Git: domain.GitStateClean}, enabled: true, ctx: func() context.Context { return app.WithoutRepositoryUpdates(context.Background()) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := newFakeCatalog()
			project := domain.Project{ID: "p1", Name: "repo", Path: "/repos/repo", Kind: domain.ProjectKindRepository}
			catalog.byID[project.ID] = project
			repository := &fakeRepository{state: test.state}
			tmux := &fakeTmux{}
			service := newTestServiceWithOptions(t, testConfig(), dependencies(catalog, repository, &fakeLauncher{}, tmux, &fakeStats{}), app.Options{EnableRepositoryUpdates: test.enabled})
			ctx := context.Background()
			if test.ctx != nil {
				ctx = test.ctx()
			}
			if _, err := service.OpenProject(ctx, domain.OpenProjectRequest{ProjectID: project.ID, NewInstance: true}); err != nil {
				t.Fatalf("OpenProject() error = %v", err)
			}
			if repository.stateCalls != test.wantState || repository.pullCalls != test.wantPull {
				t.Fatalf("State calls = %d, Pull calls = %d; want %d, %d", repository.stateCalls, repository.pullCalls, test.wantState, test.wantPull)
			}
			request := tmux.openRequests[0]
			if request.Profile != "default" || request.EditorCommand != "nvim ." || request.ShellCommand != "zsh" || !request.NewInstance {
				t.Fatalf("tmux request = %+v", request)
			}
		})
	}
}

func TestOpenProjectDispatchesConfiguredGUIOpenerWithoutTmux(t *testing.T) {
	cfg := testConfig()
	cfg.ProjectOpeners = append(cfg.ProjectOpeners, config.ProjectOpener{
		ID: "vscode", Name: "VS Code", Mode: domain.ProjectOpenModeGUI, Command: "code {{path}}",
	})
	catalog := newFakeCatalog()
	project := domain.Project{ID: "p1", Name: "repo", Path: "/repos/repo", Kind: domain.ProjectKindRepository}
	catalog.byID[project.ID] = project
	launcher := &fakeLauncher{}
	tmux := &fakeTmux{}
	service := newTestService(t, cfg, dependencies(catalog, &fakeRepository{}, launcher, tmux, &fakeStats{}))

	result, err := service.OpenProject(context.Background(), domain.OpenProjectRequest{ProjectID: project.ID, Profile: "vscode"})
	if err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	if result.Mode != domain.ProjectOpenModeGUI || result.Profile != "vscode" || result.Window.ID != "" {
		t.Fatalf("OpenProject() = %+v", result)
	}
	if !reflect.DeepEqual(launcher.openerIDs, []string{"vscode"}) || !reflect.DeepEqual(launcher.openerPaths, []string{project.Path}) {
		t.Fatalf("GUI launcher calls = ids %v paths %v", launcher.openerIDs, launcher.openerPaths)
	}
	if tmux.ensureCalls != 0 || len(tmux.openRequests) != 0 {
		t.Fatalf("GUI opener used tmux: ensure=%d opens=%d", tmux.ensureCalls, len(tmux.openRequests))
	}

	_, err = service.OpenProject(context.Background(), domain.OpenProjectRequest{ProjectID: project.ID, Profile: "vscode", NewInstance: true})
	if !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
		t.Fatalf("GUI NewInstance error = %v, want invalid_argument", err)
	}
}

func TestOpenProjectSubstitutesPathsInTmuxOpenerCommand(t *testing.T) {
	cfg := testConfig()
	cfg.ProjectOpeners[0].Command = "editor {{path}} --root {{oproot}}"
	catalog := newFakeCatalog()
	project := domain.Project{ID: "p1", Name: "repo", Path: "/repos/project path"}
	catalog.byID[project.ID] = project
	tmux := &fakeTmux{}
	service := newTestService(t, cfg, dependencies(catalog, &fakeRepository{}, &fakeLauncher{}, tmux, &fakeStats{}))

	if _, err := service.OpenProject(context.Background(), domain.OpenProjectRequest{ProjectID: project.ID}); err != nil {
		t.Fatalf("OpenProject() error = %v", err)
	}
	if got, want := tmux.openRequests[0].EditorCommand, "editor '/repos/project path' --root /config"; got != want {
		t.Fatalf("EditorCommand = %q, want %q", got, want)
	}
}

func TestRunProjectActionGatesAndDispatchesActions(t *testing.T) {
	cfg := testConfig()
	cfg.CustomCommands = []config.CustomCommand{
		{Name: "opencode", Command: "opencode ."},
		{Name: "Nvim", Command: "case-distinct"},
		{Name: "Open shell logs", Command: "descriptive"},
	}
	catalog := newFakeCatalog()
	project := domain.Project{ID: "p1", Name: "repo", Path: "/repos/repo"}
	catalog.byID[project.ID] = project
	launcher := &fakeLauncher{}
	service := newTestService(t, cfg, dependencies(catalog, &fakeRepository{}, launcher, &fakeTmux{}, &fakeStats{}))

	for _, action := range []string{"nvim", "shell", "opencode", "Nvim", "Open shell logs"} {
		result, err := service.RunProjectAction(context.Background(), domain.RunProjectActionRequest{ProjectID: project.ID, Action: action})
		if err != nil || !result.Started || result.Action != action {
			t.Fatalf("RunProjectAction(%q) = %+v, %v", action, result, err)
		}
	}
	if launcher.nvimPaths[0] != project.Path || launcher.shellPaths[0] != project.Path ||
		!reflect.DeepEqual(launcher.customNames, []string{"opencode", "Nvim", "Open shell logs"}) {
		t.Fatalf("launcher calls = %+v", launcher)
	}
	_, err := service.RunProjectAction(context.Background(), domain.RunProjectActionRequest{ProjectID: project.ID, Action: "code"})
	if !domain.IsCode(err, domain.ErrorCodeForbidden) || len(launcher.codePaths) != 0 {
		t.Fatalf("disabled code error = %v, launches = %v", err, launcher.codePaths)
	}
	_, err = service.RunProjectAction(context.Background(), domain.RunProjectActionRequest{ProjectID: project.ID, Action: "missing"})
	if !domain.IsCode(err, domain.ErrorCodeNotFound) {
		t.Fatalf("missing action error = %v", err)
	}
	_, err = service.RunProjectAction(context.Background(), domain.RunProjectActionRequest{ProjectID: project.ID, Action: "worktree"})
	if !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
		t.Fatalf("worktree without branch error = %v", err)
	}
}

func TestRunProjectActionCreatesWorktreeWhenBranchIsPresent(t *testing.T) {
	catalog := newFakeCatalog()
	project := domain.Project{ID: "p1", Name: "repo", Path: "/repos/repo"}
	created := domain.Project{ID: "w1", Name: "repo-feature", Path: "/repos/repo-feature"}
	catalog.byID[project.ID] = project
	catalog.byName[created.Name] = created
	repository := &fakeRepository{worktreeResult: gitrepo.WorktreeResult{Path: created.Path}}
	service := newTestService(t, testConfig(), dependencies(catalog, repository, &fakeLauncher{}, &fakeTmux{}, &fakeStats{}))

	result, err := service.RunProjectAction(context.Background(), domain.RunProjectActionRequest{ProjectID: project.ID, Action: "worktree:feature"})
	if err != nil || !result.Started || repository.worktreeOptions.Branch != "feature" {
		t.Fatalf("RunProjectAction() = %+v, %v; options = %+v", result, err, repository.worktreeOptions)
	}
}

func TestTmuxDelegationAndStatsComposition(t *testing.T) {
	tmuxSnapshot := domain.TmuxSnapshot{CapturedAt: time.Unix(10, 0), Session: &domain.TmuxSession{ID: "$1", Name: "code"}}
	statsSnapshot := domain.StatsSnapshot{CapturedAt: time.Unix(11, 0), Host: domain.HostStats{CPUPercent: 12}}
	tmux := &fakeTmux{
		ensureResult: domain.EnsureMainSessionResult{Created: true, Session: *tmuxSnapshot.Session},
		snapshot:     tmuxSnapshot,
	}
	collector := &fakeStats{result: statsSnapshot}
	service := newTestService(t, testConfig(), dependencies(newFakeCatalog(), &fakeRepository{}, &fakeLauncher{}, tmux, collector))

	ensured, err := service.EnsureMainSession(context.Background())
	if err != nil || !ensured.Created || tmux.ensureCalls != 1 {
		t.Fatalf("EnsureMainSession() = %+v, %v; calls = %d", ensured, err, tmux.ensureCalls)
	}
	if err := service.AttachOrSwitch(context.Background()); err != nil || tmux.attachCalls != 1 {
		t.Fatalf("AttachOrSwitch() error = %v; calls = %d", err, tmux.attachCalls)
	}
	if err := service.AttachOrSwitchTo(context.Background(), "@project"); err != nil || tmux.attachWindowID != "@project" {
		t.Fatalf("AttachOrSwitchTo() error = %v; target = %q", err, tmux.attachWindowID)
	}
	tmux.selectPaneWindow = domain.TmuxWindow{ID: "@2", Name: "notifier"}
	tmux.selectPane = domain.TmuxPane{ID: "%46", Active: true}
	selected, err := service.SelectPane(context.Background(), domain.SelectPaneRequest{PaneID: "%46"})
	if err != nil || tmux.selectPaneID != "%46" || selected.Pane.ID != "%46" || selected.Window.Name != "notifier" {
		t.Fatalf("SelectPane() = %+v, %v; pane = %q", selected, err, tmux.selectPaneID)
	}
	snapshot, err := service.GetTmuxSnapshot(context.Background())
	if err != nil || snapshot.Session.Name != "code" {
		t.Fatalf("GetTmuxSnapshot() = %+v, %v", snapshot, err)
	}
	gotStats, err := service.GetStatsSnapshot(context.Background())
	if err != nil || gotStats.Host.CPUPercent != 12 {
		t.Fatalf("GetStatsSnapshot() = %+v, %v", gotStats, err)
	}
	if collector.input.Session == nil || collector.input.Session.ID != "$1" || tmux.snapshotCalls != 2 {
		t.Fatalf("stats input = %+v; tmux snapshot calls = %d", collector.input, tmux.snapshotCalls)
	}
}

func TestErrorsAreTypedAndExistingTypesArePreserved(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want domain.ErrorCode
	}{
		{name: "raw tmux", err: errors.New("socket failed"), want: domain.ErrorCodeDependency},
		{name: "typed tmux", err: domain.NewError(domain.ErrorCodeConflict, "tmux.test", "busy", nil), want: domain.ErrorCodeConflict},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tmux := &fakeTmux{snapshotErr: test.err}
			service := newTestService(t, testConfig(), dependencies(newFakeCatalog(), &fakeRepository{}, &fakeLauncher{}, tmux, &fakeStats{}))
			_, err := service.GetTmuxSnapshot(context.Background())
			if !domain.IsCode(err, test.want) {
				t.Fatalf("error = %v, want %s", err, test.want)
			}
			var typed *domain.Error
			if !errors.As(err, &typed) {
				t.Fatalf("error type = %T, want *domain.Error", err)
			}
		})
	}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	tmux := &fakeTmux{snapshotErr: context.Canceled}
	service := newTestService(t, testConfig(), dependencies(newFakeCatalog(), &fakeRepository{}, &fakeLauncher{}, tmux, &fakeStats{}))
	_, err := service.GetTmuxSnapshot(canceled)
	if !domain.IsCode(err, domain.ErrorCodeCanceled) {
		t.Fatalf("canceled error = %v", err)
	}
}

func TestResolveCurrentProjectUsesTagBeforeExactWindowName(t *testing.T) {
	catalog := newFakeCatalog()
	tagged := domain.Project{ID: "tagged", Name: "tagged-project", Path: "/repos/tagged"}
	named := domain.Project{ID: "named", Name: "window-name", Path: "/repos/named"}
	catalog.byID[tagged.ID] = tagged
	catalog.byName[named.Name] = named
	tmux := &fakeTmux{currentID: tagged.ID, currentIDFound: true, currentName: named.Name, currentNameFound: true}
	service := newTestService(t, testConfig(), dependencies(catalog, &fakeRepository{}, &fakeLauncher{}, tmux, &fakeStats{}))

	project, found, err := service.ResolveCurrentProject(context.Background())
	if err != nil || !found || project.ID != tagged.ID {
		t.Fatalf("ResolveCurrentProject() = %+v, %v, %v", project, found, err)
	}
	if tmux.currentNameCalls != 0 {
		t.Fatalf("CurrentProjectName calls = %d, want 0", tmux.currentNameCalls)
	}

	tmux.currentIDFound = false
	project, found, err = service.ResolveCurrentProject(context.Background())
	if err != nil || !found || project.ID != named.ID {
		t.Fatalf("name fallback = %+v, %v, %v", project, found, err)
	}

	tmux.currentID = "stale-project"
	tmux.currentIDFound = true
	project, found, err = service.ResolveCurrentProject(context.Background())
	if err != nil || !found || project.ID != named.ID {
		t.Fatalf("stale tag fallback = %+v, %v, %v", project, found, err)
	}
}

func TestListProjectsComposesCatalogAndGitState(t *testing.T) {
	catalog := newFakeCatalog()
	catalog.projects = []domain.Project{
		{ID: "repo", Name: "repo", Path: "/repos/repo", Kind: domain.ProjectKindRepository, GitState: domain.GitStateUnknown},
		{ID: "custom", Name: "custom", Path: "/custom", Kind: domain.ProjectKindCustomEntry, GitState: domain.GitStateUnknown},
	}
	repository := &fakeRepository{state: gitrepo.State{Branch: "feature/filter", Git: domain.GitStateDirty}}
	service := newTestService(t, testConfig(), dependencies(catalog, repository, &fakeLauncher{}, &fakeTmux{}, &fakeStats{}))
	projects, err := service.ListProjects(context.Background())
	if err != nil {
		t.Fatalf("ListProjects() error = %v", err)
	}
	if projects[0].Branch != "feature/filter" || projects[0].GitState != domain.GitStateDirty || projects[1].GitState != domain.GitStateUnknown || repository.stateCalls != 1 {
		t.Fatalf("ListProjects() = %+v; state calls = %d", projects, repository.stateCalls)
	}
}

func TestProductionServiceConstructsTmuxLazily(t *testing.T) {
	cfg := testConfig()
	cfg.RepoDirectory = t.TempDir()
	cfg.RootDirectory = t.TempDir()
	cfg.SourcePath = filepath.Join(cfg.RootDirectory, "config.json")
	cfg.Server.TokenFile = filepath.Join(cfg.RootDirectory, "token")
	t.Setenv("PATH", t.TempDir())

	service, err := app.New(context.Background(), cfg, app.Options{OperationLockDirectory: filepath.Join(t.TempDir(), "locks")})
	if err != nil {
		t.Fatalf("New() error = %v; tmux must not be constructed yet", err)
	}
	projects, err := service.ListProjects(context.Background())
	if err != nil || len(projects) != 0 {
		t.Fatalf("ListProjects() = %#v, %v", projects, err)
	}
}

func testConfig() config.Config {
	cfg := config.Defaults()
	cfg.RepoDirectory = "/repos"
	cfg.RootDirectory = "/config"
	cfg.SourcePath = "/config/config.json"
	cfg.Server.TokenFile = "/config/token"
	cfg.Tmux.DefaultProfile = "default"
	cfg.ProjectOpeners[0].ID = "default"
	return cfg
}

func newTestService(t *testing.T, cfg config.Config, dependencies app.Dependencies) *app.Service {
	t.Helper()
	return newTestServiceWithOptions(t, cfg, dependencies, app.Options{})
}

func newTestServiceWithOptions(t *testing.T, cfg config.Config, dependencies app.Dependencies, options app.Options) *app.Service {
	t.Helper()
	if options.OperationLockDirectory == "" {
		options.OperationLockDirectory = filepath.Join(t.TempDir(), "locks")
	}
	service, err := app.NewWithDependencies(cfg, dependencies, options)
	if err != nil {
		t.Fatalf("NewWithDependencies() error = %v", err)
	}
	return service
}

func testDependencies() app.Dependencies {
	return dependencies(newFakeCatalog(), &fakeRepository{}, &fakeLauncher{}, &fakeTmux{}, &fakeStats{})
}

func dependencies(catalog app.Catalog, repository app.Repository, launcher app.Launcher, tmux app.TmuxManager, stats app.StatsCollector) app.Dependencies {
	return app.Dependencies{Catalog: catalog, Repository: repository, Launcher: launcher, Tmux: tmux, Stats: stats}
}

type fakeCatalog struct {
	mu             sync.Mutex
	root           string
	projects       []domain.Project
	byID           map[string]domain.Project
	byName         map[string]domain.Project
	createNames    []string
	cloneNames     []string
	worktreeNames  []string
	resolveNames   []string
	createErr      error
	cloneErr       error
	worktreeErr    error
	validateErr    error
	listErr        error
	resolveIDErr   error
	resolveNameErr error
}

func newFakeCatalog() *fakeCatalog {
	return &fakeCatalog{root: "/repos", byID: make(map[string]domain.Project), byName: make(map[string]domain.Project)}
}

func (f *fakeCatalog) List(context.Context) ([]domain.Project, error) {
	return append([]domain.Project(nil), f.projects...), f.listErr
}

func (f *fakeCatalog) ResolveByID(_ context.Context, id string) (domain.Project, error) {
	if f.resolveIDErr != nil {
		return domain.Project{}, f.resolveIDErr
	}
	project, found := f.byID[id]
	if !found {
		return domain.Project{}, domain.ResourceError(domain.ErrorCodeNotFound, "fake.resolve_id", id, "not found", nil)
	}
	return project, nil
}

func (f *fakeCatalog) ResolveByName(_ context.Context, name string) (domain.Project, error) {
	f.mu.Lock()
	f.resolveNames = append(f.resolveNames, name)
	f.mu.Unlock()
	if f.resolveNameErr != nil {
		return domain.Project{}, f.resolveNameErr
	}
	project, found := f.byName[name]
	if !found {
		return domain.Project{}, domain.ResourceError(domain.ErrorCodeNotFound, "fake.resolve_name", name, "not found", nil)
	}
	return project, nil
}

func (f *fakeCatalog) CreatePath(name string) (string, error) {
	f.createNames = append(f.createNames, name)
	return filepath.Join(f.root, name), f.createErr
}

func (f *fakeCatalog) ClonePath(name string) (string, error) {
	f.cloneNames = append(f.cloneNames, name)
	return filepath.Join(f.root, name), f.cloneErr
}

func (f *fakeCatalog) WorktreePath(name string) (string, error) {
	f.worktreeNames = append(f.worktreeNames, name)
	return filepath.Join(f.root, name), f.worktreeErr
}

func (f *fakeCatalog) ValidateRepositoryPath(path string) (string, error) {
	return path, f.validateErr
}

func (f *fakeCatalog) RepositoryRoot() string { return f.root }

type fakeRepository struct {
	mu              sync.Mutex
	clone           func(context.Context, gitrepo.CloneOptions) (gitrepo.CloneResult, error)
	cloneOptions    []gitrepo.CloneOptions
	cloneErr        error
	init            func(string) error
	initPath        string
	initCalls       int
	initErr         error
	state           gitrepo.State
	stateErr        error
	stateCalls      int
	pullCalls       int
	pullErr         error
	worktreeOptions gitrepo.WorktreeOptions
	worktreeResult  gitrepo.WorktreeResult
	worktreeErr     error
}

func (f *fakeRepository) Clone(ctx context.Context, options gitrepo.CloneOptions) (gitrepo.CloneResult, error) {
	f.mu.Lock()
	f.cloneOptions = append(f.cloneOptions, options)
	f.mu.Unlock()
	if f.clone != nil {
		return f.clone(ctx, options)
	}
	return gitrepo.CloneResult{Directory: options.Directory, Path: filepath.Join(options.ParentDirectory, options.Directory)}, f.cloneErr
}

func (f *fakeRepository) Init(_ context.Context, path string) error {
	f.mu.Lock()
	f.initPath = path
	f.initCalls++
	f.mu.Unlock()
	if f.init != nil {
		return f.init(path)
	}
	return f.initErr
}

func (f *fakeRepository) State(context.Context, string) (gitrepo.State, error) {
	f.mu.Lock()
	f.stateCalls++
	f.mu.Unlock()
	return f.state, f.stateErr
}

func (f *fakeRepository) Pull(context.Context, string) error {
	f.mu.Lock()
	f.pullCalls++
	f.mu.Unlock()
	return f.pullErr
}

func (f *fakeRepository) CreateWorktree(_ context.Context, options gitrepo.WorktreeOptions) (gitrepo.WorktreeResult, error) {
	f.mu.Lock()
	f.worktreeOptions = options
	f.mu.Unlock()
	return f.worktreeResult, f.worktreeErr
}

func (f *fakeRepository) operationCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := len(f.cloneOptions) + f.stateCalls + f.pullCalls
	if f.initPath != "" {
		count++
	}
	if f.worktreeOptions.Repository != "" {
		count++
	}
	return count
}

type fakeLauncher struct {
	openerIDs   []string
	openerPaths []string
	nvimPaths   []string
	codePaths   []string
	shellPaths  []string
	customNames []string
	err         error
}

func (f *fakeLauncher) LaunchProjectOpener(_ context.Context, id, path string) error {
	f.openerIDs = append(f.openerIDs, id)
	f.openerPaths = append(f.openerPaths, path)
	return f.err
}

func (f *fakeLauncher) LaunchNvim(_ context.Context, path string) error {
	f.nvimPaths = append(f.nvimPaths, path)
	return f.err
}
func (f *fakeLauncher) LaunchCode(_ context.Context, path string) error {
	f.codePaths = append(f.codePaths, path)
	return f.err
}
func (f *fakeLauncher) LaunchPreferredShell(_ context.Context, path string) error {
	f.shellPaths = append(f.shellPaths, path)
	return f.err
}
func (f *fakeLauncher) RunCustom(_ context.Context, name, _ string) error {
	f.customNames = append(f.customNames, name)
	return f.err
}

type fakeTmux struct {
	mu               sync.Mutex
	ensureCalls      int
	ensureResult     domain.EnsureMainSessionResult
	ensureErr        error
	openRequests     []tmuxmanager.OpenProjectWindowRequest
	openErr          error
	snapshotCalls    int
	snapshot         domain.TmuxSnapshot
	snapshotErr      error
	currentID        string
	currentIDFound   bool
	currentIDErr     error
	currentName      string
	currentNameFound bool
	currentNameErr   error
	currentNameCalls int
	attachCalls      int
	attachWindowID   string
	attachErr        error
	selectPaneID     string
	selectPaneWindow domain.TmuxWindow
	selectPane       domain.TmuxPane
	selectPaneErr    error
}

type serialTmux struct {
	entered     chan struct{}
	release     chan struct{}
	openEntered chan struct{}
	openRelease chan struct{}
	active      atomic.Int32
	maximum     atomic.Int32
}

type plannedAttachTmux struct {
	mode            tmuxmanager.AttachMode
	attachEntered   chan struct{}
	attachRelease   chan struct{}
	mutationEntered chan struct{}
	mutationRelease chan struct{}
	openEntered     chan struct{}
}

func newPlannedAttachTmux(mode tmuxmanager.AttachMode) *plannedAttachTmux {
	return &plannedAttachTmux{
		mode: mode, attachEntered: make(chan struct{}, 1), attachRelease: make(chan struct{}),
		mutationEntered: make(chan struct{}, 1), mutationRelease: make(chan struct{}), openEntered: make(chan struct{}, 1),
	}
}

func (f *plannedAttachTmux) PrepareAttachOrSwitch(context.Context) (tmuxmanager.AttachPlan, error) {
	return tmuxmanager.AttachPlan{Mode: f.mode}, nil
}

func (f *plannedAttachTmux) PrepareAttachOrSwitchTo(context.Context, string) (tmuxmanager.AttachPlan, error) {
	return tmuxmanager.AttachPlan{Mode: f.mode}, nil
}

func (f *plannedAttachTmux) ExecuteAttachOrSwitch(ctx context.Context, _ tmuxmanager.AttachPlan) error {
	f.attachEntered <- struct{}{}
	select {
	case <-f.attachRelease:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *plannedAttachTmux) EnsureMainSession(ctx context.Context) (domain.EnsureMainSessionResult, error) {
	f.mutationEntered <- struct{}{}
	select {
	case <-f.mutationRelease:
		return domain.EnsureMainSessionResult{}, nil
	case <-ctx.Done():
		return domain.EnsureMainSessionResult{}, ctx.Err()
	}
}

func (f *plannedAttachTmux) OpenProjectWindow(_ context.Context, request tmuxmanager.OpenProjectWindowRequest) (domain.OpenProjectResult, error) {
	f.openEntered <- struct{}{}
	return domain.OpenProjectResult{Project: request.Project}, nil
}

func (*plannedAttachTmux) SelectPane(context.Context, string) (domain.TmuxWindow, domain.TmuxPane, error) {
	return domain.TmuxWindow{}, domain.TmuxPane{}, nil
}

func (*plannedAttachTmux) Snapshot(context.Context) (domain.TmuxSnapshot, error) {
	return domain.TmuxSnapshot{}, nil
}

func (*plannedAttachTmux) CurrentProjectID(context.Context) (string, bool, error) {
	return "", false, nil
}

func (*plannedAttachTmux) CurrentProjectName(context.Context) (string, bool, error) {
	return "", false, nil
}

func newSerialTmux() *serialTmux {
	return &serialTmux{
		entered: make(chan struct{}, 2), release: make(chan struct{}),
		openEntered: make(chan struct{}, 2), openRelease: make(chan struct{}),
	}
}

func (f *serialTmux) EnsureMainSession(ctx context.Context) (domain.EnsureMainSessionResult, error) {
	current := f.active.Add(1)
	for current > f.maximum.Load() && !f.maximum.CompareAndSwap(f.maximum.Load(), current) {
	}
	f.entered <- struct{}{}
	select {
	case <-f.release:
		return domain.EnsureMainSessionResult{}, nil
	case <-ctx.Done():
		f.active.Add(-1)
		return domain.EnsureMainSessionResult{}, ctx.Err()
	}
}

func (f *serialTmux) OpenProjectWindow(ctx context.Context, request tmuxmanager.OpenProjectWindowRequest) (domain.OpenProjectResult, error) {
	f.openEntered <- struct{}{}
	select {
	case <-f.openRelease:
		f.active.Add(-1)
		return domain.OpenProjectResult{Project: request.Project}, nil
	case <-ctx.Done():
		f.active.Add(-1)
		return domain.OpenProjectResult{}, ctx.Err()
	}
}

func (f *serialTmux) PrepareAttachOrSwitch(context.Context) (tmuxmanager.AttachPlan, error) {
	return tmuxmanager.AttachPlan{Mode: tmuxmanager.AttachModeInteractive}, nil
}

func (f *serialTmux) PrepareAttachOrSwitchTo(context.Context, string) (tmuxmanager.AttachPlan, error) {
	return tmuxmanager.AttachPlan{Mode: tmuxmanager.AttachModeInteractive}, nil
}

func (f *serialTmux) ExecuteAttachOrSwitch(ctx context.Context, _ tmuxmanager.AttachPlan) error {
	f.entered <- struct{}{}
	select {
	case <-f.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *serialTmux) SelectPane(context.Context, string) (domain.TmuxWindow, domain.TmuxPane, error) {
	return domain.TmuxWindow{}, domain.TmuxPane{}, nil
}

func (f *serialTmux) Snapshot(context.Context) (domain.TmuxSnapshot, error) {
	return domain.TmuxSnapshot{}, nil
}

func (f *serialTmux) CurrentProjectID(context.Context) (string, bool, error) {
	return "", false, nil
}

func (f *serialTmux) CurrentProjectName(context.Context) (string, bool, error) {
	return "", false, nil
}

func (f *fakeTmux) PrepareAttachOrSwitch(context.Context) (tmuxmanager.AttachPlan, error) {
	return tmuxmanager.AttachPlan{Mode: tmuxmanager.AttachModeInteractive}, nil
}

func (f *fakeTmux) PrepareAttachOrSwitchTo(_ context.Context, windowID string) (tmuxmanager.AttachPlan, error) {
	f.attachWindowID = windowID
	return tmuxmanager.AttachPlan{Mode: tmuxmanager.AttachModeInteractive}, nil
}

func (f *fakeTmux) ExecuteAttachOrSwitch(context.Context, tmuxmanager.AttachPlan) error {
	f.attachCalls++
	return f.attachErr
}

func (f *fakeTmux) EnsureMainSession(context.Context) (domain.EnsureMainSessionResult, error) {
	f.ensureCalls++
	return f.ensureResult, f.ensureErr
}

func (f *fakeTmux) OpenProjectWindow(_ context.Context, request tmuxmanager.OpenProjectWindowRequest) (domain.OpenProjectResult, error) {
	f.mu.Lock()
	f.openRequests = append(f.openRequests, request)
	reused := len(f.openRequests) > 1
	f.mu.Unlock()
	return domain.OpenProjectResult{Project: request.Project, Window: domain.TmuxWindow{ID: "@1"}, Reused: reused}, f.openErr
}

func (f *fakeTmux) SelectPane(_ context.Context, paneID string) (domain.TmuxWindow, domain.TmuxPane, error) {
	f.selectPaneID = paneID
	return f.selectPaneWindow, f.selectPane, f.selectPaneErr
}

func (f *fakeTmux) Snapshot(context.Context) (domain.TmuxSnapshot, error) {
	f.snapshotCalls++
	return f.snapshot, f.snapshotErr
}

func (f *fakeTmux) CurrentProjectID(context.Context) (string, bool, error) {
	return f.currentID, f.currentIDFound, f.currentIDErr
}

func (f *fakeTmux) CurrentProjectName(context.Context) (string, bool, error) {
	f.currentNameCalls++
	return f.currentName, f.currentNameFound, f.currentNameErr
}

type fakeStats struct {
	input  domain.TmuxSnapshot
	result domain.StatsSnapshot
	err    error
}

func (f *fakeStats) Collect(_ context.Context, snapshot domain.TmuxSnapshot) (domain.StatsSnapshot, error) {
	f.input = snapshot
	return f.result, f.err
}
