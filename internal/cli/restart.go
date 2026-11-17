package cli

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/moutansos/op/internal/domain"
	"github.com/moutansos/op/internal/serveproc"
	"github.com/moutansos/op/internal/systemd"
)

const defaultRestartTimeout = 30 * time.Second

// ServeProcesses finds running op serve processes and restarts them in place.
type ServeProcesses interface {
	List(match func(serveproc.Process) bool) ([]serveproc.Process, error)
	Restart(context.Context, serveproc.Process) error
}

func runCommand(ctx context.Context, name string, args []string, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func (r *runner) runRestart(ctx context.Context, args []string) error {
	timeout := defaultRestartTimeout
	usage := func() {
		fmt.Fprintln(r.options.Stdout, `Usage: op restart [--timeout DURATION]

Restarts every running op serve owned by you so it picks up a rebuilt binary
and configuration changes. Servers run by a systemd user unit are restarted
with systemctl; others restart in place in their terminal or tmux pane.`)
	}
	positionals, err := parseFlags("restart", args, map[string]optionKind{"--timeout": valueOption}, usage, func(set *flag.FlagSet) {
		set.DurationVar(&timeout, "timeout", defaultRestartTimeout, "wait for each server to restart")
	})
	if err != nil {
		return err
	}
	if len(positionals) != 0 {
		return usageError("restart accepts no arguments")
	}
	if timeout <= 0 {
		return usageError("--timeout must be positive")
	}
	processes, err := r.findServeProcesses()
	if err != nil {
		return err
	}
	if len(processes) == 0 {
		return r.startInstalledService(ctx)
	}
	restartedUnits := make(map[string]bool)
	var failures []error
	for _, process := range processes {
		if unit := r.managingUnit(ctx, process); unit != "" {
			if !process.UserUnit {
				failures = append(failures, domain.ResourceError(domain.ErrorCodeForbidden, "cli.restart", unit,
					fmt.Sprintf("op serve (pid %d) is run by system unit %s; run: sudo systemctl restart %s", process.PID, unit, unit), nil))
				continue
			}
			if restartedUnits[unit] {
				continue
			}
			restartedUnits[unit] = true
			if err := r.checkServeCommand(ctx, process, unit); err != nil {
				failures = append(failures, err)
				continue
			}
			if err := r.restartUserUnit(ctx, unit); err != nil {
				failures = append(failures, err)
				continue
			}
			fmt.Fprintf(r.options.Stdout, "Restarted systemd user unit %s (was pid %d).\n", unit, process.PID)
			continue
		}
		restartCtx, cancel := context.WithTimeout(ctx, timeout)
		err := r.options.ServeProcesses.Restart(restartCtx, process)
		cancel()
		if err != nil {
			failures = append(failures, describeRestartFailure(process, err))
			continue
		}
		fmt.Fprintf(r.options.Stdout, "Restarted op serve (pid %d) in place.\n", process.PID)
	}
	return errors.Join(failures...)
}

func (r *runner) findServeProcesses() ([]serveproc.Process, error) {
	executable, err := r.options.Executable()
	if err != nil {
		return nil, domain.NewError(domain.ErrorCodeDependency, "cli.restart", "locate current executable", err)
	}
	name := filepath.Base(executable)
	processes, err := r.options.ServeProcesses.List(func(process serveproc.Process) bool {
		return isServeInvocation(process, name)
	})
	if errors.Is(err, serveproc.ErrUnsupported) {
		return nil, domain.NewError(domain.ErrorCodeDependency, "cli.restart", err.Error(), err)
	}
	if err != nil {
		return nil, domain.NewError(domain.ErrorCodeInternal, "cli.restart", "list processes: "+err.Error(), err)
	}
	return processes, nil
}

// isServeInvocation matches processes running this program's serve command,
// ignoring other programs that happen to be invoked with "serve".
func isServeInvocation(process serveproc.Process, name string) bool {
	if len(process.Args) < 2 {
		return false
	}
	if filepath.Base(process.Args[0]) != name && filepath.Base(process.Executable) != name {
		return false
	}
	_, remaining, err := parseGlobals(process.Args[1:])
	return err == nil && len(remaining) > 0 && remaining[0] == "serve"
}

// managingUnit returns the systemd unit whose main process is the server.
// Servers merely inside a unit's cgroup, such as a pane of a tmux server
// started by a unit, are restarted in place instead of restarting the unit.
func (r *runner) managingUnit(ctx context.Context, process serveproc.Process) string {
	if process.Unit == "" {
		return ""
	}
	mainPID, err := r.systemctl(ctx, process.UserUnit, "show", "--property=MainPID", "--value", process.Unit)
	if err != nil || mainPID != strconv.Itoa(process.PID) {
		return ""
	}
	return process.Unit
}

// checkServeCommand runs a unit's command line with --check so a broken build
// or config does not replace a working server. In-place restarts run the same
// check inside the server.
func (r *runner) checkServeCommand(ctx context.Context, process serveproc.Process, unit string) error {
	if !filepath.IsAbs(process.Args[0]) {
		return nil
	}
	var output bytes.Buffer
	args := append(slices.Clone(process.Args[1:]), serveproc.CheckFlag)
	if err := r.options.RunCommand(ctx, process.Args[0], args, &output, &output); err != nil {
		detail := strings.TrimPrefix(strings.TrimSpace(output.String()), "error: ")
		if detail == "" {
			detail = err.Error()
		}
		return domain.ResourceError(domain.ErrorCodeConfig, "cli.restart", unit, "kept "+unit+" running because the replacement failed its startup check: "+detail, err)
	}
	return nil
}

func (r *runner) restartUserUnit(ctx context.Context, unit string) error {
	// Pick up a regenerated unit file along with the new binary.
	if _, err := r.systemctl(ctx, true, "daemon-reload"); err != nil {
		return err
	}
	_, err := r.systemctl(ctx, true, "restart", unit)
	return err
}

// startInstalledService covers a stopped or crashed op.service, which restart
// starts again.
func (r *runner) startInstalledService(ctx context.Context) error {
	notRunning := domain.ResourceError(domain.ErrorCodeNotFound, "cli.restart", "op serve", "no running op serve was found; start one with op serve or op service install", nil)
	if !r.options.SystemdBooted() || !r.userUnitInstalled() {
		return notRunning
	}
	if err := r.restartUserUnit(ctx, systemd.UnitName); err != nil {
		return err
	}
	fmt.Fprintf(r.options.Stdout, "op serve was not running; started systemd user unit %s.\n", systemd.UnitName)
	return nil
}

func (r *runner) userUnitInstalled() bool {
	path, err := r.userUnitPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(path)
	return err == nil
}

func (r *runner) systemctl(ctx context.Context, user bool, args ...string) (string, error) {
	if user {
		args = append([]string{"--user"}, args...)
	}
	var stdout, stderr bytes.Buffer
	if err := r.options.RunCommand(ctx, "systemctl", args, &stdout, &stderr); err != nil {
		message := "systemctl " + strings.Join(args, " ") + " failed"
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			message += ": " + detail
		} else {
			message += ": " + err.Error()
		}
		return "", domain.NewError(domain.ErrorCodeDependency, "cli.systemd", message, err)
	}
	return strings.TrimSpace(stdout.String()), nil
}

func describeRestartFailure(process serveproc.Process, err error) error {
	command := make([]string, len(process.Args))
	for index, arg := range process.Args {
		command[index] = shellWord(arg)
	}
	code := domain.CodeOf(err)
	var message string
	switch {
	case errors.Is(err, serveproc.ErrRestartRejected):
		code = domain.ErrorCodeConfig
		detail := strings.TrimPrefix(err.Error(), serveproc.ErrRestartRejected.Error()+": ")
		message = fmt.Sprintf("op serve (pid %d) kept running because the replacement failed its startup check: %s", process.PID, detail)
	case errors.Is(err, serveproc.ErrExited):
		message = fmt.Sprintf("op serve (pid %d) exited instead of restarting; builds without restart support do this, otherwise check its output. Start it again with: %s", process.PID, strings.Join(command, " "))
	case errors.Is(err, serveproc.ErrExitedAfterRestart):
		message = fmt.Sprintf("op serve (pid %d) restarted but exited before listening; check its output", process.PID)
	default:
		message = fmt.Sprintf("op serve (pid %d): %v", process.PID, err)
	}
	return domain.ResourceError(code, "cli.restart", strconv.Itoa(process.PID), message, err)
}
