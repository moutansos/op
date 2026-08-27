// Package cli implements the op command-line interface.
package cli

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/moutansos/op/internal/app"
	"github.com/moutansos/op/internal/config"
	"github.com/moutansos/op/internal/domain"
	"github.com/moutansos/op/internal/server"
	"github.com/moutansos/op/internal/tui"
)

const defaultRemoteTimeout = 30 * time.Second

type Version struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	Date    string `json:"date"`
}

type Service interface {
	domain.Service
	ResolveCurrentProject(context.Context) (domain.Project, bool, error)
	AttachOrSwitch(context.Context) error
	AttachOrSwitchTo(context.Context, string) error
}

type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

type Options struct {
	Stdin         io.Reader
	Stdout        io.Writer
	Stderr        io.Writer
	LookupEnv     func(string) string
	Executable    func() (string, error)
	LookPath      func(string) (string, error)
	ReadFile      func(string) ([]byte, error)
	LoadConfig    func(string) (config.LoadResult, error)
	NewService    func(context.Context, config.Config, app.Options) (Service, error)
	RunTUI        func(context.Context, domain.Service, tui.Options) error
	RunTreeTUI    func(context.Context, domain.Service, tui.Options) error
	SelectAction  tui.Selector
	SelectProject tui.ProjectSelector
	RunServer     func(context.Context, domain.Service, server.Options) error
	HTTPClient    HTTPClient
	Signals       func(context.Context) (context.Context, context.CancelFunc)
	Version       Version
}

type runner struct {
	options Options
	config  config.Config
	service Service
	globals globalFlags
}

type globalFlags struct {
	configPath   string
	noTarget     bool
	noRepoUpdate bool
}

// Run executes args and returns a process exit code without calling os.Exit.
func Run(ctx context.Context, args []string, options Options) int {
	options = withDefaults(options)
	global, remaining, err := parseGlobals(args)
	if err != nil {
		return reportError(options.Stderr, err)
	}

	command := ""
	if len(remaining) > 0 {
		command = remaining[0]
	}
	if command == "help" || command == "--help" || command == "-h" {
		writeRootHelp(options.Stdout)
		return 0
	}
	if command == "version" {
		if len(remaining) != 1 {
			return reportError(options.Stderr, usageError("version accepts no arguments"))
		}
		writeVersion(options.Stdout, options.Version)
		return 0
	}

	var loaded config.LoadResult
	if command == "remote" && remoteConnectionIsExplicit(remaining[1:], options.LookupEnv) {
		loaded.Config = config.Defaults()
	} else {
		loaded, err = options.LoadConfig(global.configPath)
		if err != nil {
			if command != "remote" || global.configPath != "" || !domain.IsCode(err, domain.ErrorCodeNotFound) {
				return reportError(options.Stderr, err)
			}
			// Standalone remote use can rely on flags or environment variables
			// without requiring a local op installation.
			loaded.Config = config.Defaults()
		}
	}
	writeWarnings(options.Stderr, loaded.Warnings)
	r := &runner{options: options, config: loaded.Config, globals: global}

	if command == "remote" {
		err = r.runRemote(ctx, remaining[1:])
	} else {
		err = r.runLocal(ctx, remaining)
	}
	if errors.Is(err, errHelp) {
		return 0
	}
	if err != nil {
		return reportError(options.Stderr, err)
	}
	return 0
}

func withDefaults(options Options) Options {
	if options.Stdin == nil {
		options.Stdin = os.Stdin
	}
	if options.Stdout == nil {
		options.Stdout = os.Stdout
	}
	if options.Stderr == nil {
		options.Stderr = os.Stderr
	}
	if options.LookupEnv == nil {
		options.LookupEnv = os.Getenv
	}
	if options.Executable == nil {
		options.Executable = os.Executable
	}
	if options.LookPath == nil {
		options.LookPath = exec.LookPath
	}
	if options.ReadFile == nil {
		options.ReadFile = os.ReadFile
	}
	if options.LoadConfig == nil {
		options.LoadConfig = func(explicit string) (config.LoadResult, error) {
			return config.LocateAndLoad(config.LocateOptions{ExplicitPath: explicit})
		}
	}
	if options.NewService == nil {
		options.NewService = func(ctx context.Context, cfg config.Config, appOptions app.Options) (Service, error) {
			return app.New(ctx, cfg, appOptions)
		}
	}
	if options.RunTUI == nil {
		options.RunTUI = tui.Run
	}
	if options.RunTreeTUI == nil {
		options.RunTreeTUI = tui.RunTree
	}
	if options.SelectAction == nil {
		options.SelectAction = tui.SelectAction
	}
	if options.SelectProject == nil {
		options.SelectProject = tui.SelectProject
	}
	if options.RunServer == nil {
		options.RunServer = runServer
	}
	if options.HTTPClient == nil {
		options.HTTPClient = http.DefaultClient
	}
	if options.Signals == nil {
		options.Signals = func(parent context.Context) (context.Context, context.CancelFunc) {
			return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
		}
	}
	if options.Version.Version == "" {
		options.Version.Version = "dev"
	}
	if options.Version.Commit == "" {
		options.Version.Commit = "unknown"
	}
	if options.Version.Date == "" {
		options.Version.Date = "unknown"
	}
	return options
}

func (r *runner) getService(ctx context.Context, dependencies ...string) (Service, error) {
	if r.service != nil {
		return r.service, nil
	}
	for _, dependency := range dependencies {
		if _, err := r.options.LookPath(dependency); err != nil {
			return nil, domain.ResourceError(domain.ErrorCodeDependency, "cli.dependencies", dependency, dependency+" executable was not found in PATH", err)
		}
	}
	executable, err := r.options.Executable()
	if err != nil {
		return nil, domain.NewError(domain.ErrorCodeDependency, "cli.executable", "locate current executable", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, domain.NewError(domain.ErrorCodeDependency, "cli.executable", "normalize current executable", err)
	}
	baseArgs := []string{shellWord(executable)}
	if r.config.SourcePath != "" {
		baseArgs = append(baseArgs, "--config", shellWord(r.config.SourcePath))
	}
	if r.globals.noRepoUpdate {
		baseArgs = append(baseArgs, "--no-repo-update")
	}
	service, err := r.options.NewService(ctx, r.config, app.Options{
		EnableRepositoryUpdates: !r.globals.noRepoUpdate,
		DashboardCommand:        strings.Join(append(slices.Clone(baseArgs), "dashboard"), " "),
		TreeCommand:             strings.Join(append(baseArgs, "tree"), " "),
		Output:                  r.options.Stdout,
		Error:                   r.options.Stderr,
	})
	if err != nil {
		return nil, err
	}
	r.service = service
	return service, nil
}

func shellWord(value string) string {
	if value != "" && !strings.ContainsAny(value, " \t\r\n'\\\"$`;&|<>()") {
		return value
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func (r *runner) snapshotCachePath() string {
	root := r.options.LookupEnv("XDG_RUNTIME_DIR")
	if root == "" || !filepath.IsAbs(root) {
		cache, err := os.UserCacheDir()
		if err != nil {
			return ""
		}
		root = filepath.Join(cache, "op", "runtime")
	} else {
		root = filepath.Join(root, "op")
	}
	identity := sha256.Sum256([]byte(r.config.Tmux.Socket + "\x00" + r.config.Tmux.Session))
	return filepath.Join(root, fmt.Sprintf("dashboard-%x.json", identity[:8]))
}

func runServer(ctx context.Context, service domain.Service, options server.Options) error {
	srv, err := server.NewServer(service, options)
	if err != nil {
		return err
	}
	defer srv.Close()
	result := make(chan error, 1)
	go func() { result <- srv.ListenAndServe() }()
	select {
	case err := <-result:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-result
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func writeWarnings(writer io.Writer, warnings []config.Warning) {
	for _, warning := range warnings {
		fmt.Fprintf(writer, "warning: %s: %s\n", warning.Path, warning.Message)
	}
}

func writeVersion(writer io.Writer, version Version) {
	fmt.Fprintf(writer, "op %s\ncommit: %s\nbuilt: %s\n", version.Version, version.Commit, version.Date)
}

func reportError(writer io.Writer, err error) int {
	fmt.Fprintf(writer, "error: %v\n", err)
	return exitCode(err)
}

func exitCode(err error) int {
	var usage *cliUsageError
	if errors.As(err, &usage) {
		return 2
	}
	switch domain.CodeOf(err) {
	case domain.ErrorCodeInvalidArgument:
		return 2
	case domain.ErrorCodeConfig:
		return 3
	case domain.ErrorCodeNotFound:
		return 4
	case domain.ErrorCodeAlreadyExists, domain.ErrorCodeConflict:
		return 5
	case domain.ErrorCodeUnauthorized, domain.ErrorCodeForbidden:
		return 6
	case domain.ErrorCodeDependency:
		return 7
	case domain.ErrorCodeTimeout:
		return 124
	case domain.ErrorCodeCanceled:
		return 130
	default:
		return 1
	}
}

func writeRootHelp(writer io.Writer) {
	fmt.Fprint(writer, `Usage: op [--config PATH] [--no-repo-update] [--no-target] [command]

Commands:
  dashboard                         Run the dashboard in the current pane
  tree                              Select a pane from the managed process tree
  serve                             Run the authenticated remote API
  notify install-claude|install-grok|install-codex|install-copilot
                                    Install agent hook plugins for notifications
  projects [--json]                 List local projects
  open <project ID or exact name>   Open a local project window
  clone <url> [--directory NAME] [--open]
  new <name> [--open]
  worktree <project> <branch> [--open]
  remote projects|clone|open|job    Call a remote op server
  version                           Print build information

Running op without a command ensures and attaches to (or switches to) the
managed tmux session. Inside a project window it opens a fuzzy local-action selector.
Use "op <command> --help" for command flags.
`)
}
