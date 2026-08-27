package process_test

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"testing"

	"github.com/moutansos/op/internal/config"
	"github.com/moutansos/op/internal/domain"
	processrunner "github.com/moutansos/op/internal/process"
)

type fakeRunner struct {
	commands []processrunner.Command
	err      error
}

func (f *fakeRunner) Run(_ context.Context, command processrunner.Command) error {
	f.commands = append(f.commands, command)
	return f.err
}

func TestBuiltInLaunchCommands(t *testing.T) {
	path := "/repos/project"
	runner := &fakeRunner{}
	launcher := newLauncher(t, runner, processrunner.Options{
		PreferredShell: "/usr/bin/zsh",
		GUIEditors:     true,
	})

	if err := launcher.LaunchNvim(context.Background(), path); err != nil {
		t.Fatalf("LaunchNvim() error = %v", err)
	}
	if err := launcher.LaunchCode(context.Background(), path); err != nil {
		t.Fatalf("LaunchCode() error = %v", err)
	}
	if err := launcher.LaunchPreferredShell(context.Background(), path); err != nil {
		t.Fatalf("LaunchPreferredShell() error = %v", err)
	}
	want := []processrunner.Command{
		{Directory: path, Name: "/usr/bin/zsh", Args: []string{"-ic", "nvim .; exec /usr/bin/zsh"}},
		{Directory: path, Name: "code", Args: []string{"."}},
		{Directory: path, Name: "/usr/bin/zsh"},
	}
	if !reflect.DeepEqual(runner.commands, want) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, want)
	}
}

func TestConfiguredGUIProjectOpenersRunDirectlyOrInPreferredShell(t *testing.T) {
	runner := &fakeRunner{}
	launcher := newLauncher(t, runner, processrunner.Options{
		PreferredShell: "/usr/bin/zsh",
		OpRoot:         "/opt/op root",
		ProjectOpeners: []config.ProjectOpener{
			{ID: "vscode", Name: "VS Code", Mode: domain.ProjectOpenModeGUI, Command: "code {{path}}"},
			{ID: "visual-studio", Name: "Visual Studio", Mode: domain.ProjectOpenModeGUI, Command: "open-vs {{path}} {{oproot}}", RunInPreferredShell: true},
		},
	})

	for _, id := range []string{"vscode", "visual-studio"} {
		if err := launcher.LaunchProjectOpener(context.Background(), id, "/repos/project path"); err != nil {
			t.Fatalf("LaunchProjectOpener(%q) error = %v", id, err)
		}
	}
	want := []processrunner.Command{
		{Directory: "/repos/project path", Name: "code", Args: []string{"/repos/project path"}},
		{Directory: "/repos/project path", Name: "/usr/bin/zsh", Args: []string{"-ic", "open-vs '/repos/project path' '/opt/op root'"}},
	}
	if !reflect.DeepEqual(runner.commands, want) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, want)
	}
}

func TestPreferredShellSupportsExecutableArguments(t *testing.T) {
	runner := &fakeRunner{}
	launcher := newLauncher(t, runner, processrunner.Options{
		PreferredShell: `/usr/bin/zsh -l --no-rcs`,
		CustomCommands: []config.CustomCommand{{
			Name:                "status",
			Command:             "git status && printf done",
			RunInPreferredShell: true,
		}},
	})

	if err := launcher.LaunchPreferredShell(context.Background(), "/repos/project"); err != nil {
		t.Fatalf("LaunchPreferredShell() error = %v", err)
	}
	if err := launcher.LaunchNvim(context.Background(), "/repos/project"); err != nil {
		t.Fatalf("LaunchNvim() error = %v", err)
	}
	if err := launcher.RunCustom(context.Background(), "status", "/repos/project"); err != nil {
		t.Fatalf("RunCustom() error = %v", err)
	}
	want := []processrunner.Command{
		{Directory: "/repos/project", Name: "/usr/bin/zsh", Args: []string{"-l", "--no-rcs"}},
		{Directory: "/repos/project", Name: "/usr/bin/zsh", Args: []string{"-l", "--no-rcs", "-ic", "nvim .; exec /usr/bin/zsh -l --no-rcs"}},
		{Directory: "/repos/project", Name: "/usr/bin/zsh", Args: []string{"-l", "--no-rcs", "-ic", "git status && printf done; exec /usr/bin/zsh -l --no-rcs"}},
	}
	if !reflect.DeepEqual(runner.commands, want) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, want)
	}
}

func TestPreferredShellArgumentsAreParsedWithoutEvaluation(t *testing.T) {
	runner := &fakeRunner{}
	launcher := newLauncher(t, runner, processrunner.Options{PreferredShell: `zsh "argument with spaces" "argument\q" '$HOME'`})

	if err := launcher.LaunchPreferredShell(context.Background(), "/repos/project"); err != nil {
		t.Fatalf("LaunchPreferredShell() error = %v", err)
	}
	want := []processrunner.Command{{
		Directory: "/repos/project",
		Name:      "zsh",
		Args:      []string{"argument with spaces", `argument\q`, "$HOME"},
	}}
	if !reflect.DeepEqual(runner.commands, want) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, want)
	}
}

func TestPreferredShellRejectsShellOperators(t *testing.T) {
	for _, shell := range []string{"zsh; other", "zsh && other", " zsh"} {
		_, err := processrunner.NewLauncherWithRunner(processrunner.Options{PreferredShell: shell}, &fakeRunner{})
		if !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
			t.Fatalf("NewLauncherWithRunner(%q) error = %v, want invalid argument", shell, err)
		}
	}
}

func TestCodeIsNotLaunchedWhenDisabled(t *testing.T) {
	runner := &fakeRunner{}
	launcher := newLauncher(t, runner, processrunner.Options{PreferredShell: "zsh"})
	err := launcher.LaunchCode(context.Background(), "/repos/project")
	if !domain.IsCode(err, domain.ErrorCodeForbidden) {
		t.Fatalf("LaunchCode() error = %v, want forbidden", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("LaunchCode() ran commands: %#v", runner.commands)
	}
}

func TestCustomCommandSubstitutionAndDirectExecution(t *testing.T) {
	runner := &fakeRunner{}
	launcher := newLauncher(t, runner, processrunner.Options{
		PreferredShell: "zsh",
		OpRoot:         "/opt/op root",
		CustomCommands: []config.CustomCommand{{
			Name:    "inspect",
			Command: "tool --project {{path}} --root={{oproot}}",
		}},
	})

	if err := launcher.RunCustom(context.Background(), "inspect", "/repos/project one"); err != nil {
		t.Fatalf("RunCustom() error = %v", err)
	}
	want := []processrunner.Command{{
		Directory: "/repos/project one",
		Name:      "tool",
		Args:      []string{"--project", "/repos/project one", "--root=/opt/op root"},
	}}
	if !reflect.DeepEqual(runner.commands, want) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, want)
	}
}

func TestCustomCommandRunsInPreferredShell(t *testing.T) {
	runner := &fakeRunner{}
	launcher := newLauncher(t, runner, processrunner.Options{
		PreferredShell: "/usr/bin/zsh",
		OpRoot:         "/opt/op root",
		CustomCommands: []config.CustomCommand{{
			Name:                "opencode",
			Command:             "cd {{oproot}} && opencode {{path}}",
			RunInPreferredShell: true,
		}},
	})

	if err := launcher.RunCustom(context.Background(), "opencode", "/repos/project one"); err != nil {
		t.Fatalf("RunCustom() error = %v", err)
	}
	wantCommand := "cd '/opt/op root' && opencode '/repos/project one'; exec /usr/bin/zsh"
	want := []processrunner.Command{{
		Directory: "/repos/project one",
		Name:      "/usr/bin/zsh",
		Args:      []string{"-ic", wantCommand},
	}}
	if !reflect.DeepEqual(runner.commands, want) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, want)
	}
}

func TestPowerShellUsesNativeInteractiveArguments(t *testing.T) {
	runner := &fakeRunner{}
	launcher := newLauncher(t, runner, processrunner.Options{
		PreferredShell: "pwsh",
		CustomCommands: []config.CustomCommand{{
			Name:                "status",
			Command:             "Get-Location",
			RunInPreferredShell: true,
		}},
	})
	if err := launcher.LaunchNvim(context.Background(), "/repos/project"); err != nil {
		t.Fatalf("LaunchNvim() error = %v", err)
	}
	if err := launcher.RunCustom(context.Background(), "status", "/repos/project"); err != nil {
		t.Fatalf("RunCustom() error = %v", err)
	}
	want := []processrunner.Command{
		{Directory: "/repos/project", Name: "pwsh", Args: []string{"-NoExit", "-Command", "nvim ."}},
		{Directory: "/repos/project", Name: "pwsh", Args: []string{"-NoExit", "-Command", "Get-Location"}},
	}
	if !reflect.DeepEqual(runner.commands, want) {
		t.Fatalf("commands = %#v, want %#v", runner.commands, want)
	}
}

func TestPowerShellExeUsesNativeInteractiveArguments(t *testing.T) {
	for _, shell := range []string{"pwsh.exe", "/usr/bin/pwsh.exe", `'/mnt/c/Program Files/PowerShell/7/pwsh.exe'`} {
		t.Run(shell, func(t *testing.T) {
			runner := &fakeRunner{}
			launcher := newLauncher(t, runner, processrunner.Options{PreferredShell: shell})
			if err := launcher.LaunchNvim(context.Background(), "/repos/project"); err != nil {
				t.Fatalf("LaunchNvim() error = %v", err)
			}
			if len(runner.commands) != 1 {
				t.Fatalf("commands = %#v", runner.commands)
			}
			got := runner.commands[0]
			if len(got.Args) < 2 || got.Args[len(got.Args)-3] != "-NoExit" || got.Args[len(got.Args)-2] != "-Command" || got.Args[len(got.Args)-1] != "nvim ." {
				t.Fatalf("args = %#v", got.Args)
			}
		})
	}
}

func TestDirectCustomCommandRejectsShellOperators(t *testing.T) {
	runner := &fakeRunner{}
	launcher := newLauncher(t, runner, processrunner.Options{
		PreferredShell: "zsh",
		CustomCommands: []config.CustomCommand{{Name: "unsafe-direct", Command: "first && second"}},
	})
	err := launcher.RunCustom(context.Background(), "unsafe-direct", "/repos/project")
	if !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
		t.Fatalf("RunCustom() error = %v, want invalid argument", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("RunCustom() ran commands: %#v", runner.commands)
	}
}

func TestUnknownCustomCommandDoesNotExposeCommandExecution(t *testing.T) {
	runner := &fakeRunner{}
	launcher := newLauncher(t, runner, processrunner.Options{
		PreferredShell: "zsh",
		CustomCommands: []config.CustomCommand{{Name: "allowed", Command: "tool"}},
	})
	err := launcher.RunCustom(context.Background(), "rm -rf /", "/repos/project")
	if !domain.IsCode(err, domain.ErrorCodeNotFound) {
		t.Fatalf("RunCustom() error = %v, want not found", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("RunCustom() ran commands: %#v", runner.commands)
	}
}

func TestCustomCommandNamesPreserveConfigurationOrderWithoutCommandText(t *testing.T) {
	launcher := newLauncher(t, &fakeRunner{}, processrunner.Options{
		PreferredShell: "zsh",
		CustomCommands: []config.CustomCommand{
			{Name: "zeta", Command: "secret --token value"},
			{Name: "alpha", Command: "other"},
		},
	})
	if got, want := launcher.CustomCommandNames(), []string{"zeta", "alpha"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("CustomCommandNames() = %#v, want %#v", got, want)
	}
}

func TestLauncherRejectsReservedCustomCommandNames(t *testing.T) {
	for _, name := range []string{"nvim", "code", "shell", "worktree", "worktree:feature", "worktree:"} {
		t.Run(name, func(t *testing.T) {
			_, err := processrunner.NewLauncherWithRunner(processrunner.Options{
				PreferredShell: "zsh",
				CustomCommands: []config.CustomCommand{{Name: name, Command: "custom"}},
			}, &fakeRunner{})
			var typed *domain.Error
			if !errors.As(err, &typed) || typed.Code != domain.ErrorCodeInvalidArgument || typed.Field != "customCommands[0].name" {
				t.Fatalf("NewLauncherWithRunner() error = %#v, want customCommands[0].name invalid argument", err)
			}
		})
	}
}

func TestLauncherAcceptsCaseDistinctAndDescriptiveCustomCommandNames(t *testing.T) {
	runner := &fakeRunner{}
	launcher := newLauncher(t, runner, processrunner.Options{
		PreferredShell: "zsh",
		CustomCommands: []config.CustomCommand{
			{Name: "Nvim", Command: "case-distinct"},
			{Name: "Open shell logs", Command: "descriptive"},
			{Name: "3-way", Command: "mixed-digits"},
			{Name: "03", Command: "numeric"},
		},
	})
	for _, name := range []string{"Nvim", "Open shell logs", "3-way", "03"} {
		if err := launcher.RunCustom(context.Background(), name, "/repos/project"); err != nil {
			t.Fatalf("RunCustom(%q) error = %v", name, err)
		}
	}
	if got, want := runner.commands, []processrunner.Command{
		{Directory: "/repos/project", Name: "case-distinct", Args: []string{}},
		{Directory: "/repos/project", Name: "descriptive", Args: []string{}},
		{Directory: "/repos/project", Name: "mixed-digits", Args: []string{}},
		{Directory: "/repos/project", Name: "numeric", Args: []string{}},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
}

func TestSubstituteQuotesShellMetacharacters(t *testing.T) {
	got := processrunner.Substitute("run {{path}} --root {{oproot}}", "/repo/it's here", "/opt/op")
	want := "run '/repo/it'\"'\"'s here' --root /opt/op"
	if got != want {
		t.Fatalf("Substitute() = %q, want %q", got, want)
	}
}

func TestLaunchFailureIsTyped(t *testing.T) {
	runner := &fakeRunner{err: exec.ErrNotFound}
	launcher := newLauncher(t, runner, processrunner.Options{PreferredShell: "zsh"})
	err := launcher.LaunchNvim(context.Background(), "/repos/project")
	if !domain.IsCode(err, domain.ErrorCodeDependency) {
		t.Fatalf("LaunchNvim() error = %v, want dependency error", err)
	}
	if !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("LaunchNvim() error does not retain cause: %v", err)
	}
}

func TestLaunchUsesContextFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runner := &fakeRunner{err: context.Canceled}
	launcher := newLauncher(t, runner, processrunner.Options{PreferredShell: "zsh"})
	err := launcher.LaunchPreferredShell(ctx, "/repos/project")
	if !domain.IsCode(err, domain.ErrorCodeCanceled) {
		t.Fatalf("LaunchPreferredShell() error = %v, want canceled", err)
	}
}

func TestLaunchRejectsRelativeWorkingDirectory(t *testing.T) {
	runner := &fakeRunner{}
	launcher := newLauncher(t, runner, processrunner.Options{PreferredShell: "zsh"})
	err := launcher.LaunchNvim(context.Background(), "relative/project")
	if !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
		t.Fatalf("LaunchNvim() error = %v, want invalid argument", err)
	}
	if len(runner.commands) != 0 {
		t.Fatalf("LaunchNvim() ran commands: %#v", runner.commands)
	}
}

func newLauncher(t *testing.T, runner processrunner.CommandRunner, options processrunner.Options) *processrunner.Launcher {
	t.Helper()
	launcher, err := processrunner.NewLauncherWithRunner(options, runner)
	if err != nil {
		t.Fatalf("NewLauncherWithRunner() error = %v", err)
	}
	return launcher
}
