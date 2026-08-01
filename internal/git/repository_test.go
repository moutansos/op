package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/moutansos/op/internal/domain"
	gitrepo "github.com/moutansos/op/internal/git"
)

type commandResponse struct {
	output []byte
	err    error
}

type fakeRunner struct {
	commands  []gitrepo.Command
	responses []commandResponse
}

func (f *fakeRunner) Run(_ context.Context, command gitrepo.Command) ([]byte, error) {
	f.commands = append(f.commands, command)
	if len(f.responses) == 0 {
		return nil, nil
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	return response.output, response.err
}

func TestCloneDirectoryName(t *testing.T) {
	tests := map[string]string{
		"https":            "https://github.com/example/project.git",
		"ssh":              "ssh://git@github.com/example/project.git",
		"scp":              "git@github.com:example/project.git",
		"scp current user": "github.com:example/project.git",
		"trailing slash":   "https://github.com/example/project/",
		"encoded name":     "https://github.com/example/project%20name.git",
	}
	for name, cloneURL := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := gitrepo.CloneDirectoryName(cloneURL)
			if err != nil {
				t.Fatalf("CloneDirectoryName() error = %v", err)
			}
			want := "project"
			if name == "encoded name" {
				want = "project name"
			}
			if got != want {
				t.Fatalf("CloneDirectoryName() = %q, want %q", got, want)
			}
		})
	}
}

func TestCloneDirectoryNameRejectsUnsafeURLs(t *testing.T) {
	for _, cloneURL := range []string{
		"",
		"http://github.com/example/project.git",
		"file:///tmp/project.git",
		"https://github.com",
		"https://github.com/example/project.git?upload-pack=evil",
		"ftp:example/project.git",
		"https:example/project.git",
		"../project.git",
		"C:\\project.git",
	} {
		t.Run(cloneURL, func(t *testing.T) {
			_, err := gitrepo.CloneDirectoryName(cloneURL)
			if !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
				t.Fatalf("CloneDirectoryName() error = %v, want invalid argument", err)
			}
		})
	}
}

func TestCloneConstructsShellFreeCommand(t *testing.T) {
	parent := t.TempDir()
	runner := &fakeRunner{}
	repository := gitrepo.NewRepositoryWithRunner(runner)

	result, err := repository.Clone(context.Background(), gitrepo.CloneOptions{
		URL:             "git@github.com:example/project.git",
		ParentDirectory: parent,
	})
	if err != nil {
		t.Fatalf("Clone() error = %v", err)
	}
	wantCommand := gitrepo.Command{
		Directory: parent,
		Name:      "git",
		Args:      []string{"clone", "--", "git@github.com:example/project.git", "project"},
	}
	assertCommands(t, runner.commands, []gitrepo.Command{wantCommand})
	if result.Directory != "project" || result.Path != filepath.Join(parent, "project") {
		t.Fatalf("Clone() result = %#v", result)
	}
}

func TestCloneRejectsNestedDestination(t *testing.T) {
	runner := &fakeRunner{}
	repository := gitrepo.NewRepositoryWithRunner(runner)
	_, err := repository.Clone(context.Background(), gitrepo.CloneOptions{
		URL:             "https://github.com/example/project.git",
		ParentDirectory: t.TempDir(),
		Directory:       "nested/project",
	})
	if !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
		t.Fatalf("Clone() error = %v, want invalid argument", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("Clone() ran commands after validation failure: %#v", runner.commands)
	}
}

func TestFailedCloneRemovesOnlyItsOwnedDestinationAndCanRetry(t *testing.T) {
	parent := t.TempDir()
	runner := &fakeRunner{responses: []commandResponse{
		{err: errors.New("clone interrupted")},
		{},
	}}
	repository := gitrepo.NewRepositoryWithRunner(runner)
	options := gitrepo.CloneOptions{
		URL:             "https://github.com/example/project.git",
		ParentDirectory: parent,
		Directory:       "project",
	}
	destination := filepath.Join(parent, options.Directory)

	if _, err := repository.Clone(context.Background(), options); err == nil {
		t.Fatal("first Clone() error = nil, want failure")
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed clone destination still exists: %v", err)
	}
	if _, err := repository.Clone(context.Background(), options); err != nil {
		t.Fatalf("retry Clone() error = %v", err)
	}
	if info, err := os.Stat(destination); err != nil || !info.IsDir() {
		t.Fatalf("successful retry destination = %v, %v", info, err)
	}
}

func TestCanceledCloneRemovesItsDestination(t *testing.T) {
	parent := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &fakeRunner{responses: []commandResponse{{err: context.Canceled}}}
	destination := filepath.Join(parent, "project")
	_, err := gitrepo.NewRepositoryWithRunner(runner).Clone(ctx, gitrepo.CloneOptions{
		URL: "https://github.com/example/project.git", ParentDirectory: parent, Directory: "project",
	})
	if !domain.IsCode(err, domain.ErrorCodeCanceled) {
		t.Fatalf("Clone() error = %v, want canceled", err)
	}
	if _, err := os.Lstat(destination); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled clone destination still exists: %v", err)
	}
}

func TestCloneDoesNotRemovePreexistingDestination(t *testing.T) {
	parent := t.TempDir()
	destination := filepath.Join(parent, "project")
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(destination, "keep")
	if err := os.WriteFile(marker, []byte("owned elsewhere"), 0o644); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	_, err := gitrepo.NewRepositoryWithRunner(runner).Clone(context.Background(), gitrepo.CloneOptions{
		URL: "https://github.com/example/project.git", ParentDirectory: parent, Directory: "project",
	})
	if !domain.IsCode(err, domain.ErrorCodeAlreadyExists) {
		t.Fatalf("Clone() error = %v, want already_exists", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("preexisting destination was modified: %v", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("Clone() commands = %#v, want none", runner.commands)
	}
}

func TestInitCommand(t *testing.T) {
	parent := t.TempDir()
	runner := &fakeRunner{}
	repository := gitrepo.NewRepositoryWithRunner(runner)
	path := filepath.Join(parent, "new-project")

	if err := repository.Init(context.Background(), path); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	assertCommands(t, runner.commands, []gitrepo.Command{{
		Directory: parent,
		Name:      "git",
		Args:      []string{"init", "--", "new-project"},
	}})
}

func TestInitRejectsExistingDestinationBeforeGit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{}
	err := gitrepo.NewRepositoryWithRunner(runner).Init(context.Background(), path)
	if !domain.IsCode(err, domain.ErrorCodeAlreadyExists) {
		t.Fatalf("Init() error = %v, want already_exists", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("Init() commands = %#v, want none", runner.commands)
	}
}

func TestStateUsesPorcelainForNormalAndLinkedWorktrees(t *testing.T) {
	for name, output := range map[string][]byte{
		"clean repository":   nil,
		"dirty repository":   []byte(" M README.md\n"),
		"linked worktree":    nil,
		"untracked contents": []byte("?? new.txt\n"),
	} {
		t.Run(name, func(t *testing.T) {
			runner := &fakeRunner{responses: []commandResponse{{output: output}}}
			repository := gitrepo.NewRepositoryWithRunner(runner)
			path := filepath.Join(t.TempDir(), "worktree")

			state, err := repository.State(context.Background(), path)
			if err != nil {
				t.Fatalf("State() error = %v", err)
			}
			want := domain.GitStateClean
			if len(output) != 0 {
				want = domain.GitStateDirty
			}
			if state != want {
				t.Fatalf("State() = %q, want %q", state, want)
			}
			assertCommands(t, runner.commands, []gitrepo.Command{{
				Directory: path,
				Name:      "git",
				Args:      []string{"status", "--porcelain"},
			}})
		})
	}
}

func TestStateRecognizesNonRepository(t *testing.T) {
	runner := &fakeRunner{responses: []commandResponse{{
		output: []byte("fatal: not a git repository (or any parent up to mount point)"),
		err:    errors.New("exit status 128"),
	}}}
	state, err := gitrepo.NewRepositoryWithRunner(runner).State(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("State() error = %v", err)
	}
	if state != domain.GitStateNotRepository {
		t.Fatalf("State() = %q, want %q", state, domain.GitStateNotRepository)
	}
}

func TestPullOnlyRunsForCleanWorktree(t *testing.T) {
	path := t.TempDir()
	runner := &fakeRunner{responses: []commandResponse{{}, {}}}
	repository := gitrepo.NewRepositoryWithRunner(runner)

	if err := repository.Pull(context.Background(), path); err != nil {
		t.Fatalf("Pull() error = %v", err)
	}
	assertCommands(t, runner.commands, []gitrepo.Command{
		{Directory: path, Name: "git", Args: []string{"status", "--porcelain"}},
		{Directory: path, Name: "git", Args: []string{"pull", "--ff-only"}},
	})
}

func TestPullRejectsDirtyWorktreeWithoutPulling(t *testing.T) {
	path := t.TempDir()
	runner := &fakeRunner{responses: []commandResponse{{output: []byte(" M file.go\n")}}}
	err := gitrepo.NewRepositoryWithRunner(runner).Pull(context.Background(), path)
	if !domain.IsCode(err, domain.ErrorCodeConflict) {
		t.Fatalf("Pull() error = %v, want conflict", err)
	}
	if len(runner.commands) != 1 || !reflect.DeepEqual(runner.commands[0].Args, []string{"status", "--porcelain"}) {
		t.Fatalf("dirty Pull() commands = %#v", runner.commands)
	}
}

func TestCreateWorktreeCreatesSiblingAndBranchInOneCommand(t *testing.T) {
	parent := t.TempDir()
	repositoryPath := filepath.Join(parent, "project")
	runner := &fakeRunner{}
	repository := gitrepo.NewRepositoryWithRunner(runner)

	result, err := repository.CreateWorktree(context.Background(), gitrepo.WorktreeOptions{
		Repository: repositoryPath,
		Branch:     "feature/use-fakes",
	})
	if err != nil {
		t.Fatalf("CreateWorktree() error = %v", err)
	}
	destination := filepath.Join(parent, "project-feature-use-fakes")
	assertCommands(t, runner.commands, []gitrepo.Command{{
		Directory: repositoryPath,
		Name:      "git",
		Args:      []string{"worktree", "add", "-b", "feature/use-fakes", "--", destination},
	}})
	if result.Path != destination || result.Directory != "project-feature-use-fakes" || result.Branch != "feature/use-fakes" {
		t.Fatalf("CreateWorktree() result = %#v", result)
	}
}

func TestCreateWorktreeRejectsUnsafeInputs(t *testing.T) {
	for _, options := range []gitrepo.WorktreeOptions{
		{Repository: "/repos/project", Branch: "-force"},
		{Repository: "/repos/project", Branch: "feature", Directory: "../outside"},
		{Repository: "/repos/project", Branch: "bad..branch"},
	} {
		runner := &fakeRunner{}
		_, err := gitrepo.NewRepositoryWithRunner(runner).CreateWorktree(context.Background(), options)
		if !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
			t.Fatalf("CreateWorktree(%#v) error = %v", options, err)
		}
		if len(runner.commands) != 0 {
			t.Fatalf("CreateWorktree(%#v) ran %#v", options, runner.commands)
		}
	}
}

func TestCommandFailuresAreTypedAndIncludeDiagnostics(t *testing.T) {
	path := t.TempDir()
	runner := &fakeRunner{responses: []commandResponse{{output: []byte("fatal: network unavailable"), err: errors.New("exit status 1")}}}
	err := gitrepo.NewRepositoryWithRunner(runner).Init(context.Background(), filepath.Join(path, "project"))
	if !domain.IsCode(err, domain.ErrorCodeInternal) {
		t.Fatalf("Init() error = %v, want internal", err)
	}
	if got := err.Error(); !contains(got, "fatal: network unavailable") {
		t.Fatalf("Init() error = %q, want command diagnostics", got)
	}
}

func TestMissingGitIsDependencyError(t *testing.T) {
	runner := &fakeRunner{responses: []commandResponse{{err: exec.ErrNotFound}}}
	err := gitrepo.NewRepositoryWithRunner(runner).Init(context.Background(), filepath.Join(t.TempDir(), "project"))
	if !domain.IsCode(err, domain.ErrorCodeDependency) {
		t.Fatalf("Init() error = %v, want dependency error", err)
	}
}

func assertCommands(t *testing.T, got, want []gitrepo.Command) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
}

func contains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
