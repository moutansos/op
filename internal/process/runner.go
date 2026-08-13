package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/moutansos/op/internal/action"
	"github.com/moutansos/op/internal/config"
	"github.com/moutansos/op/internal/domain"
)

// Command is a structured local process invocation. It is never interpreted by
// a shell unless Launcher explicitly wraps a configured custom command.
type Command struct {
	Directory string
	Name      string
	Args      []string
}

// CommandRunner is the process boundary used by Launcher.
type CommandRunner interface {
	Run(context.Context, Command) error
}

type execCommandRunner struct {
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

func (r execCommandRunner) Run(ctx context.Context, command Command) error {
	cmd := exec.CommandContext(ctx, command.Name, command.Args...)
	configureProcessCancellation(cmd)
	cmd.Dir = command.Directory
	cmd.Stdin = r.stdin
	cmd.Stdout = r.stdout
	cmd.Stderr = r.stderr
	return cmd.Run()
}

type Options struct {
	PreferredShell string
	GUIEditors     bool
	OpRoot         string
	ProjectOpeners []config.ProjectOpener
	CustomCommands []config.CustomCommand
}

// Launcher exposes only built-in actions and commands selected from its
// configuration. It intentionally has no public arbitrary-command method.
type Launcher struct {
	runner         CommandRunner
	preferredShell Command
	guiEditors     bool
	opRoot         string
	openers        map[string]config.ProjectOpener
	commands       map[string]config.CustomCommand
	commandNames   []string
}

func NewLauncher(options Options) (*Launcher, error) {
	return NewLauncherWithRunner(options, execCommandRunner{
		stdin:  os.Stdin,
		stdout: os.Stdout,
		stderr: os.Stderr,
	})
}

func NewLauncherWithRunner(options Options, runner CommandRunner) (*Launcher, error) {
	const op = "process.configure"
	if runner == nil {
		return nil, domain.NewError(domain.ErrorCodeInvalidArgument, op, "command runner must not be nil", nil)
	}
	if options.PreferredShell == "" || options.PreferredShell != strings.TrimSpace(options.PreferredShell) || containsControl(options.PreferredShell) {
		return nil, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "preferredShell", "must be an executable and optional arguments without surrounding whitespace or control characters")
	}
	shellWords, err := splitCommandLine(options.PreferredShell)
	if err != nil {
		return nil, domain.NewError(domain.ErrorCodeInvalidArgument, op, "invalid preferred shell command", err)
	}
	if options.OpRoot != "" && (!filepath.IsAbs(options.OpRoot) || options.OpRoot != filepath.Clean(options.OpRoot)) {
		return nil, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "opRoot", "must be an absolute normalized path")
	}

	commands := make(map[string]config.CustomCommand, len(options.CustomCommands))
	commandNames := make([]string, 0, len(options.CustomCommands))
	for i, command := range options.CustomCommands {
		prefix := fmt.Sprintf("customCommands[%d]", i)
		if command.Name == "" || strings.TrimSpace(command.Command) == "" {
			return nil, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "customCommands", "command names and values must not be empty")
		}
		if err := action.ValidateCustomName(command.Name); err != nil {
			return nil, domain.FieldError(domain.ErrorCodeInvalidArgument, op, prefix+".name", err.Error())
		}
		if _, exists := commands[command.Name]; exists {
			return nil, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "customCommands", "command names must be unique")
		}
		if strings.Contains(command.Command, "{{oproot}}") && options.OpRoot == "" {
			return nil, domain.FieldError(domain.ErrorCodeInvalidArgument, op, "opRoot", "is required by a configured command")
		}
		commands[command.Name] = command
		commandNames = append(commandNames, command.Name)
	}
	configuredOpeners := options.ProjectOpeners
	if len(configuredOpeners) == 0 {
		configuredOpeners = config.Defaults().ProjectOpeners
	}
	openers := make(map[string]config.ProjectOpener, len(configuredOpeners))
	for _, opener := range configuredOpeners {
		openers[opener.ID] = opener
	}

	return &Launcher{
		runner:         runner,
		preferredShell: Command{Name: shellWords[0], Args: append([]string(nil), shellWords[1:]...)},
		guiEditors:     options.GUIEditors,
		opRoot:         options.OpRoot,
		openers:        openers,
		commands:       commands,
		commandNames:   commandNames,
	}, nil
}

// LaunchProjectOpener starts a configured GUI project opener.
func (l *Launcher) LaunchProjectOpener(ctx context.Context, id, path string) error {
	const op = "process.project_opener"
	opener, found := l.openers[id]
	if !found {
		return domain.ResourceError(domain.ErrorCodeNotFound, op, id, "configured project opener not found", nil)
	}
	if opener.Mode != domain.ProjectOpenModeGUI {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, "profile", "must identify a GUI project opener")
	}
	if err := validateWorkingDirectory(op, path); err != nil {
		return err
	}
	expanded := Substitute(opener.Command, path, l.opRoot)
	if opener.RunInPreferredShell {
		args := append([]string(nil), l.preferredShell.Args...)
		args = append(args, preferredShellArgs(l.preferredShell.Name, expanded)...)
		return l.runCommand(ctx, op, Command{Directory: path, Name: l.preferredShell.Name, Args: args})
	}
	words, err := splitCommandLine(expanded)
	if err != nil {
		return domain.NewError(domain.ErrorCodeInvalidArgument, op, "invalid direct project opener command", err)
	}
	return l.runCommand(ctx, op, Command{Directory: path, Name: words[0], Args: words[1:]})
}

func (l *Launcher) LaunchNvim(ctx context.Context, path string) error {
	const op = "process.nvim"
	if err := validateWorkingDirectory(op, path); err != nil {
		return err
	}
	args := append([]string(nil), l.preferredShell.Args...)
	args = append(args, persistentPreferredShellArgs(l.preferredShell, "nvim .")...)
	return l.runCommand(ctx, op, Command{
		Directory: path,
		Name:      l.preferredShell.Name,
		Args:      args,
	})
}

func (l *Launcher) LaunchCode(ctx context.Context, path string) error {
	if !l.guiEditors {
		return domain.NewError(domain.ErrorCodeForbidden, "process.code", "VS Code actions are disabled", nil)
	}
	return l.run(ctx, "process.code", path, "code", ".")
}

func (l *Launcher) LaunchPreferredShell(ctx context.Context, path string) error {
	if err := validateWorkingDirectory("process.shell", path); err != nil {
		return err
	}
	return l.runCommand(ctx, "process.shell", Command{
		Directory: path,
		Name:      l.preferredShell.Name,
		Args:      append([]string(nil), l.preferredShell.Args...),
	})
}

// CustomCommandNames returns the configured local action names without
// exposing their command text.
func (l *Launcher) CustomCommandNames() []string {
	return append([]string(nil), l.commandNames...)
}

// RunCustom runs a previously configured command by name in path.
func (l *Launcher) RunCustom(ctx context.Context, name, path string) error {
	const op = "process.custom"
	command, found := l.commands[name]
	if !found {
		return domain.ResourceError(domain.ErrorCodeNotFound, op, name, "configured command not found", nil)
	}
	if err := validateWorkingDirectory(op, path); err != nil {
		return err
	}
	expanded := Substitute(command.Command, path, l.opRoot)

	if command.RunInPreferredShell {
		// Shell interpretation is opt-in, and the shell remains available in the
		// project directory after the configured command exits.
		args := append([]string(nil), l.preferredShell.Args...)
		args = append(args, persistentPreferredShellArgs(l.preferredShell, expanded)...)
		return l.runCommand(ctx, op, Command{
			Directory: path,
			Name:      l.preferredShell.Name,
			Args:      args,
		})
	}
	words, err := splitCommandLine(expanded)
	if err != nil {
		return domain.NewError(domain.ErrorCodeInvalidArgument, op, "invalid direct custom command", err)
	}
	return l.runCommand(ctx, op, Command{Directory: path, Name: words[0], Args: words[1:]})
}

// Substitute replaces the two supported placeholders with shell-safe paths.
func Substitute(command, path, opRoot string) string {
	command = strings.ReplaceAll(command, "{{path}}", quoteForShell(path))
	return strings.ReplaceAll(command, "{{oproot}}", quoteForShell(opRoot))
}

func (l *Launcher) run(ctx context.Context, op, directory, name string, args ...string) error {
	if err := validateWorkingDirectory(op, directory); err != nil {
		return err
	}
	return l.runCommand(ctx, op, Command{Directory: directory, Name: name, Args: args})
}

func (l *Launcher) runCommand(ctx context.Context, op string, command Command) error {
	if err := l.runner.Run(ctx, command); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return domain.ResourceError(domain.CodeOf(ctxErr), op, command.Directory, "run command", ctxErr)
		}
		code := domain.ErrorCodeInternal
		if errors.Is(err, exec.ErrNotFound) {
			code = domain.ErrorCodeDependency
		}
		return domain.ResourceError(code, op, command.Name, "run command", err)
	}
	return nil
}

func validateWorkingDirectory(op, path string) error {
	if path == "" || !filepath.IsAbs(path) || path != filepath.Clean(path) {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, "path", "must be an absolute normalized working directory")
	}
	return nil
}

func preferredShellArgs(shell, command string) []string {
	switch strings.ToLower(filepath.Base(shell)) {
	case "pwsh", "pwsh.exe", "powershell", "powershell.exe":
		return []string{"-NoExit", "-Command", command}
	default:
		return []string{"-ic", command}
	}
}

func persistentPreferredShellArgs(shell Command, command string) []string {
	args := preferredShellArgs(shell.Name, command)
	if len(args) == 0 || args[0] != "-ic" {
		return args
	}
	restart := append([]string{shell.Name}, shell.Args...)
	for index := range restart {
		restart[index] = quoteForShell(restart[index])
	}
	args[len(args)-1] += "; exec " + strings.Join(restart, " ")
	return args
}

func quoteForShell(value string) string {
	if value != "" && strings.IndexFunc(value, func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("/._-+:,@%=", r))
	}) < 0 {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func splitCommandLine(command string) ([]string, error) {
	var words []string
	var word strings.Builder
	var quote rune
	escaped := false
	haveWord := false

	flush := func() {
		if haveWord {
			words = append(words, word.String())
			word.Reset()
			haveWord = false
		}
	}
	for _, current := range command {
		if escaped {
			if quote == '"' && !strings.ContainsRune("$`\"\\\n", current) {
				word.WriteRune('\\')
			}
			word.WriteRune(current)
			haveWord = true
			escaped = false
			continue
		}
		if quote == '\'' {
			if current == '\'' {
				quote = 0
			} else {
				word.WriteRune(current)
				haveWord = true
			}
			continue
		}
		if current == '\\' {
			escaped = true
			haveWord = true
			continue
		}
		if quote == '"' {
			if current == '"' {
				quote = 0
			} else {
				word.WriteRune(current)
				haveWord = true
			}
			continue
		}
		switch current {
		case '\'', '"':
			quote = current
			haveWord = true
		case ' ', '\t':
			flush()
		case '|', '&', ';', '<', '>', '(', ')', '\n', '\r':
			return nil, fmt.Errorf("shell operator %q requires runInPreferredShell", current)
		default:
			word.WriteRune(current)
			haveWord = true
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unterminated quote or escape")
	}
	flush()
	if len(words) == 0 || words[0] == "" {
		return nil, fmt.Errorf("command is empty")
	}
	return words, nil
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}
