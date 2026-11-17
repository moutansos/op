package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/moutansos/op/internal/domain"
	"github.com/moutansos/op/internal/serveproc"
	"github.com/moutansos/op/internal/systemd"
)

func (r *runner) runService(ctx context.Context, args []string) error {
	usage := func() {
		fmt.Fprintln(r.options.Stdout, `Usage: op service install [--force] [--no-start]
       op service uninstall|start|stop|restart|status
       op service logs [--follow] [--lines N]

Manages op serve as the systemd user unit `+systemd.UnitName+`. install writes the
unit for this op binary, the loaded config, and the current PATH, then enables
and starts it.`)
	}
	if len(args) == 0 {
		return usageError("service requires a subcommand; run 'op service --help' for usage")
	}
	switch args[0] {
	case "-h", "--help":
		usage()
		return errHelp
	case "install":
		return r.installService(ctx, args[1:], usage)
	case "uninstall":
		return r.uninstallService(ctx, args[1:], usage)
	case "start", "stop", "restart":
		return r.controlService(ctx, args[0], args[1:], usage)
	case "status":
		return r.serviceStatus(ctx, args[1:], usage)
	case "logs":
		return r.serviceLogs(ctx, args[1:], usage)
	default:
		return usageError("unknown service subcommand %q", args[0])
	}
}

func (r *runner) requireSystemd() error {
	if !r.options.SystemdBooted() {
		return domain.NewError(domain.ErrorCodeDependency, "cli.service", "systemd is not running on this system; run op serve under another supervisor", nil)
	}
	if _, err := r.options.LookPath("systemctl"); err != nil {
		return domain.ResourceError(domain.ErrorCodeDependency, "cli.service", "systemctl", "systemctl executable was not found in PATH", err)
	}
	return nil
}

func (r *runner) userUnitPath() (string, error) {
	directory, err := systemd.UserUnitDir(r.options.LookupEnv)
	if err != nil {
		return "", domain.NewError(domain.ErrorCodeDependency, "cli.service", "locate systemd user unit directory", err)
	}
	return filepath.Join(directory, systemd.UnitName), nil
}

func noArguments(name string, args []string, usage func()) error {
	positionals, err := parseFlags(name, args, nil, usage, func(*flag.FlagSet) {})
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return usageError("%s accepts no arguments", name)
	}
	return nil
}

func (r *runner) installService(ctx context.Context, args []string, usage func()) error {
	var force, noStart bool
	positionals, err := parseFlags("service install", args, map[string]optionKind{"--force": boolOption, "--no-start": boolOption}, usage, func(set *flag.FlagSet) {
		set.BoolVar(&force, "force", false, "replace a unit file with different contents")
		set.BoolVar(&noStart, "no-start", false, "enable the unit without starting it")
	})
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return usageError("service install accepts no arguments")
	}
	if err := r.requireSystemd(); err != nil {
		return err
	}
	executable, err := r.options.Executable()
	if err == nil {
		executable, err = filepath.Abs(executable)
	}
	if err != nil {
		return domain.NewError(domain.ErrorCodeDependency, "cli.service", "locate current executable", err)
	}
	if strings.Contains(executable, string(filepath.Separator)+"go-build") {
		fmt.Fprintf(r.options.Stderr, "warning: %s looks like a temporary go run binary; install a built op instead\n", executable)
	}
	if strings.TrimSpace(r.config.Server.Token) == "" && r.config.Server.TokenFile == "" {
		fmt.Fprintln(r.options.Stderr, "warning: the unit cannot see OP_API_TOKEN from this shell; set server.tokenFile or server.token so op serve can start")
	}
	content := systemd.RenderUnit(systemd.UnitSpec{
		Executable: executable,
		ConfigPath: r.config.SourcePath,
		Path:       r.options.LookupEnv("PATH"),
	})
	unitPath, err := r.userUnitPath()
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(unitPath)
	if err == nil && string(existing) != content && !force {
		return domain.ResourceError(domain.ErrorCodeAlreadyExists, "cli.service", unitPath, "a different unit is already installed; rerun with --force to replace it", nil)
	}
	if err := os.MkdirAll(filepath.Dir(unitPath), 0o755); err != nil {
		return domain.ResourceError(domain.ErrorCodeInternal, "cli.service", unitPath, "create unit directory: "+err.Error(), err)
	}
	if err := os.WriteFile(unitPath, []byte(content), 0o644); err != nil {
		return domain.ResourceError(domain.ErrorCodeInternal, "cli.service", unitPath, "write unit: "+err.Error(), err)
	}
	fmt.Fprintf(r.options.Stdout, "Installed %s.\n", unitPath)
	if _, err := r.systemctl(ctx, true, "daemon-reload"); err != nil {
		return err
	}
	if _, err := r.systemctl(ctx, true, "enable", systemd.UnitName); err != nil {
		return err
	}
	if noStart {
		fmt.Fprintf(r.options.Stdout, "Enabled %s; start it with op service start.\n", systemd.UnitName)
		return nil
	}
	if unmanaged := r.unmanagedServers(ctx); len(unmanaged) > 0 {
		fmt.Fprintf(r.options.Stdout, "Enabled %s but did not start it: op serve is already running outside systemd (pid %s).\nStop it, then run op service start.\n", systemd.UnitName, strings.Join(unmanaged, ", "))
		return nil
	}
	// restart also applies a reinstalled unit to an already running service.
	if _, err := r.systemctl(ctx, true, "restart", systemd.UnitName); err != nil {
		return err
	}
	fmt.Fprintf(r.options.Stdout, "Enabled and started %s.\nFollow its logs with op service logs --follow. To keep it running after logout, run: loginctl enable-linger \"$USER\"\n", systemd.UnitName)
	return nil
}

// unmanagedServers lists servers that would hold the listen address the unit needs.
func (r *runner) unmanagedServers(ctx context.Context) []string {
	processes, err := r.findServeProcesses()
	if err != nil {
		return nil
	}
	var pids []string
	for _, process := range processes {
		if r.managingUnit(ctx, process) == "" {
			pids = append(pids, strconv.Itoa(process.PID))
		}
	}
	return pids
}

func (r *runner) uninstallService(ctx context.Context, args []string, usage func()) error {
	if err := noArguments("service uninstall", args, usage); err != nil {
		return err
	}
	if err := r.requireSystemd(); err != nil {
		return err
	}
	unitPath, err := r.userUnitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(unitPath); err != nil {
		return domain.ResourceError(domain.ErrorCodeNotFound, "cli.service", unitPath, "the systemd user unit is not installed", err)
	}
	if _, err := r.systemctl(ctx, true, "disable", "--now", systemd.UnitName); err != nil {
		return err
	}
	if err := os.Remove(unitPath); err != nil {
		return domain.ResourceError(domain.ErrorCodeInternal, "cli.service", unitPath, "remove unit: "+err.Error(), err)
	}
	if _, err := r.systemctl(ctx, true, "daemon-reload"); err != nil {
		return err
	}
	fmt.Fprintf(r.options.Stdout, "Stopped, disabled, and removed %s.\n", systemd.UnitName)
	return nil
}

func (r *runner) controlService(ctx context.Context, action string, args []string, usage func()) error {
	if err := noArguments("service "+action, args, usage); err != nil {
		return err
	}
	if err := r.requireSystemd(); err != nil {
		return err
	}
	var err error
	if action == "restart" {
		err = r.restartUserUnit(ctx, systemd.UnitName)
	} else {
		_, err = r.systemctl(ctx, true, action, systemd.UnitName)
	}
	if err != nil {
		return err
	}
	past := map[string]string{"start": "Started", "stop": "Stopped", "restart": "Restarted"}[action]
	fmt.Fprintf(r.options.Stdout, "%s %s.\n", past, systemd.UnitName)
	return nil
}

func (r *runner) serviceStatus(ctx context.Context, args []string, usage func()) error {
	if err := noArguments("service status", args, usage); err != nil {
		return err
	}
	if err := r.requireSystemd(); err != nil {
		return err
	}
	err := r.options.RunCommand(ctx, "systemctl", []string{"--user", "status", "--no-pager", systemd.UnitName}, r.options.Stdout, r.options.Stderr)
	// A stopped or missing unit exits non-zero after printing its status.
	if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) {
		return nil
	}
	return err
}

func (r *runner) serviceLogs(ctx context.Context, args []string, usage func()) error {
	var follow bool
	lines := 100
	positionals, err := parseFlags("service logs", args, map[string]optionKind{"--follow": boolOption, "-f": boolOption, "--lines": valueOption, "-n": valueOption}, usage, func(set *flag.FlagSet) {
		set.BoolVar(&follow, "follow", false, "keep printing new log entries")
		set.BoolVar(&follow, "f", false, "keep printing new log entries")
		set.IntVar(&lines, "lines", 100, "number of recent entries to show")
		set.IntVar(&lines, "n", 100, "number of recent entries to show")
	})
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return usageError("service logs accepts no arguments")
	}
	if err := r.requireSystemd(); err != nil {
		return err
	}
	journalArgs := []string{"--user", "--unit", systemd.UnitName, "--no-pager", "--lines", strconv.Itoa(lines)}
	if follow {
		journalArgs = append(journalArgs, "--follow")
	}
	err = r.options.RunCommand(ctx, "journalctl", journalArgs, r.options.Stdout, r.options.Stderr)
	if follow && errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	return err
}

var _ ServeProcesses = (*serveproc.Host)(nil)
