package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/moutansos/op/internal/config"
	"github.com/moutansos/op/internal/domain"
	"github.com/moutansos/op/internal/serveproc"
	"github.com/moutansos/op/internal/server"
)

type fakeServeProcesses struct {
	processes []serveproc.Process
	listErr   error
	restartFn func(serveproc.Process) error
	restarted []int
}

func (f *fakeServeProcesses) List(match func(serveproc.Process) bool) ([]serveproc.Process, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	var matched []serveproc.Process
	for _, process := range f.processes {
		if match(process) {
			matched = append(matched, process)
		}
	}
	return matched, nil
}

func (f *fakeServeProcesses) Restart(_ context.Context, process serveproc.Process) error {
	f.restarted = append(f.restarted, process.PID)
	if f.restartFn != nil {
		return f.restartFn(process)
	}
	return nil
}

type fakeCommands struct {
	calls   []string
	outputs map[string]string
	errs    map[string]error
}

func (f *fakeCommands) run(_ context.Context, name string, args []string, stdout, _ io.Writer) error {
	call := strings.Join(append([]string{name}, args...), " ")
	f.calls = append(f.calls, call)
	io.WriteString(stdout, f.outputs[call])
	return f.errs[call]
}

type restartRuntime struct {
	*testRuntime
	processes *fakeServeProcesses
	commands  *fakeCommands
	booted    bool
	unitDir   string
}

func newRestartRuntime(t *testing.T) *restartRuntime {
	runtime := &restartRuntime{
		testRuntime: newTestRuntime(),
		processes:   &fakeServeProcesses{},
		commands:    &fakeCommands{outputs: map[string]string{}, errs: map[string]error{}},
		booted:      true,
	}
	xdg := t.TempDir()
	runtime.lookup["XDG_CONFIG_HOME"] = xdg
	runtime.lookup["PATH"] = "/home/me/bin:/usr/bin"
	runtime.unitDir = filepath.Join(xdg, "systemd", "user")
	return runtime
}

func (r *restartRuntime) options() Options {
	options := r.testRuntime.options()
	options.ServeProcesses = r.processes
	options.RunCommand = r.commands.run
	options.SystemdBooted = func() bool { return r.booted }
	return options
}

func (r *restartRuntime) run(args ...string) int {
	return Run(context.Background(), args, r.options())
}

func serveProcess(pid int, args ...string) serveproc.Process {
	return serveproc.Process{PID: pid, Args: args, Executable: "/opt/op"}
}

func TestIsServeInvocation(t *testing.T) {
	tests := []struct {
		args []string
		want bool
	}{
		{[]string{"op", "serve"}, true},
		{[]string{"/home/me/.local/bin/op", "--config", "/c.json", "--no-repo-update", "serve"}, true},
		{[]string{"op", "--config=/c.json", "serve"}, true},
		{[]string{"op", "dashboard"}, false},
		{[]string{"op", "open", "serve"}, false},
		{[]string{"python", "-m", "serve"}, false},
		{[]string{"op"}, false},
	}
	for _, test := range tests {
		process := serveproc.Process{Args: test.args}
		if got := isServeInvocation(process, "op"); got != test.want {
			t.Errorf("isServeInvocation(%q) = %v", test.args, got)
		}
	}
	// A renamed argv[0] still matches by executable.
	if !isServeInvocation(serveproc.Process{Args: []string{"op-dev", "serve"}, Executable: "/opt/op"}, "op") {
		t.Fatal("expected executable name match")
	}
}

func TestRestartSignalsUnmanagedServers(t *testing.T) {
	runtime := newRestartRuntime(t)
	runtime.processes.processes = []serveproc.Process{
		serveProcess(10, "op", "serve"),
		serveProcess(11, "op", "dashboard"),
		serveProcess(12, "/opt/op", "--config", "/other.json", "serve"),
	}
	options := runtime.options()
	options.LoadConfig = func(string) (config.LoadResult, error) {
		return config.LoadResult{}, errors.New("restart must not load config")
	}
	if code := Run(context.Background(), []string{"restart"}, options); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if !slices.Equal(runtime.processes.restarted, []int{10, 12}) || len(runtime.commands.calls) != 0 {
		t.Fatalf("restarted = %v, commands = %q", runtime.processes.restarted, runtime.commands.calls)
	}
	if !strings.Contains(runtime.stdout.String(), "Restarted op serve (pid 12) in place.") {
		t.Fatalf("stdout = %q", runtime.stdout.String())
	}
}

func TestRestartUsesSystemctlForUserUnitMainProcess(t *testing.T) {
	runtime := newRestartRuntime(t)
	managed := serveProcess(10, "op", "serve")
	managed.Unit, managed.UserUnit = "op.service", true
	inTmuxUnit := serveProcess(20, "op", "serve")
	inTmuxUnit.Unit, inTmuxUnit.UserUnit = "tmux.service", true
	runtime.processes.processes = []serveproc.Process{managed, inTmuxUnit}
	runtime.commands.outputs["systemctl --user show --property=MainPID --value op.service"] = "10\n"
	runtime.commands.outputs["systemctl --user show --property=MainPID --value tmux.service"] = "5\n"
	if code := runtime.run("restart"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if !slices.Contains(runtime.commands.calls, "systemctl --user restart op.service") || slices.Contains(runtime.commands.calls, "systemctl --user restart tmux.service") {
		t.Fatalf("commands = %q", runtime.commands.calls)
	}
	if !slices.Equal(runtime.processes.restarted, []int{20}) {
		t.Fatalf("restarted = %v", runtime.processes.restarted)
	}
}

func TestRestartChecksUnitReplacementBeforeRestarting(t *testing.T) {
	runtime := newRestartRuntime(t)
	managed := serveProcess(10, "/opt/op", "--config", "/c.json", "serve")
	managed.Unit, managed.UserUnit = "op.service", true
	runtime.processes.processes = []serveproc.Process{managed}
	runtime.commands.outputs["systemctl --user show --property=MainPID --value op.service"] = "10"
	runtime.commands.outputs["/opt/op --config /c.json serve --check"] = "error: config.decode: bad JSON\n"
	runtime.commands.errs["/opt/op --config /c.json serve --check"] = errors.New("exit status 3")
	if code := runtime.run("restart"); code != 3 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if slices.Contains(runtime.commands.calls, "systemctl --user restart op.service") {
		t.Fatalf("commands = %q", runtime.commands.calls)
	}
	if !strings.Contains(runtime.stderr.String(), "kept op.service running because the replacement failed its startup check: config.decode: bad JSON") {
		t.Fatalf("stderr = %q", runtime.stderr.String())
	}
}

func TestRestartReportsRejectedInPlaceRestart(t *testing.T) {
	runtime := newRestartRuntime(t)
	runtime.processes.processes = []serveproc.Process{serveProcess(10, "op", "serve")}
	runtime.processes.restartFn = func(serveproc.Process) error {
		return fmt.Errorf("%w: config.decode: bad JSON", serveproc.ErrRestartRejected)
	}
	if code := runtime.run("restart"); code != 3 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if !strings.Contains(runtime.stderr.String(), "op serve (pid 10) kept running because the replacement failed its startup check: config.decode: bad JSON") {
		t.Fatalf("stderr = %q", runtime.stderr.String())
	}
}

func TestRestartRefusesSystemUnits(t *testing.T) {
	runtime := newRestartRuntime(t)
	process := serveProcess(10, "op", "serve")
	process.Unit = "op.service"
	runtime.processes.processes = []serveproc.Process{process}
	runtime.commands.outputs["systemctl show --property=MainPID --value op.service"] = "10"
	if code := runtime.run("restart"); code != 6 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if !strings.Contains(runtime.stderr.String(), "sudo systemctl restart op.service") || len(runtime.processes.restarted) != 0 {
		t.Fatalf("stderr = %q", runtime.stderr.String())
	}
}

func TestRestartExplainsServersThatStopped(t *testing.T) {
	runtime := newRestartRuntime(t)
	runtime.processes.processes = []serveproc.Process{serveProcess(10, "op", "--config", "/my config.json", "serve")}
	runtime.processes.restartFn = func(serveproc.Process) error { return serveproc.ErrExited }
	if code := runtime.run("restart"); code != 1 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(runtime.stderr.String(), "Start it again with: op --config '/my config.json' serve") {
		t.Fatalf("stderr = %q", runtime.stderr.String())
	}
}

func TestRestartWithoutServersStartsInstalledUnitOrFails(t *testing.T) {
	runtime := newRestartRuntime(t)
	if code := runtime.run("restart"); code != 4 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if err := os.MkdirAll(runtime.unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtime.unitDir, "op.service"), []byte("[Service]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runtime.run("restart"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if !slices.Equal(runtime.commands.calls, []string{"systemctl --user daemon-reload", "systemctl --user restart op.service"}) {
		t.Fatalf("commands = %q", runtime.commands.calls)
	}
}

func TestRestartReportsUnsupportedPlatforms(t *testing.T) {
	runtime := newRestartRuntime(t)
	runtime.processes.listErr = serveproc.ErrUnsupported
	if code := runtime.run("restart"); code != 7 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
}

func pendingRestartRequest() func() (<-chan os.Signal, func()) {
	return func() (<-chan os.Signal, func()) {
		requests := make(chan os.Signal, 1)
		requests <- os.Interrupt
		return requests, func() {}
	}
}

func TestServeReexecsAfterReplacementPassesCheck(t *testing.T) {
	runtime := newTestRuntime()
	options := runtime.options()
	reexecs := 0
	options.RestartRequests = pendingRestartRequest()
	options.CheckReplacement = func(context.Context) error { return nil }
	options.Reexec = func() error { reexecs++; return nil }
	options.RunServer = func(ctx context.Context, _ domain.Service, _ server.Options) error {
		<-ctx.Done()
		return nil
	}
	if code := Run(context.Background(), []string{"serve"}, options); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if reexecs != 1 {
		t.Fatalf("reexecs = %d", reexecs)
	}
}

func TestServeKeepsServingWhenReplacementFailsCheck(t *testing.T) {
	runtime := newTestRuntime()
	runtimeDir := t.TempDir()
	runtime.lookup["XDG_RUNTIME_DIR"] = runtimeDir
	report := serveproc.RestartErrorPath(filepath.Join(runtimeDir, "op"), os.Getpid())
	options := runtime.options()
	options.RestartRequests = pendingRestartRequest()
	options.CheckReplacement = func(context.Context) error { return errors.New("config.decode: bad JSON") }
	options.Reexec = func() error { t.Error("unexpected reexec"); return nil }
	options.RunServer = func(ctx context.Context, _ domain.Service, _ server.Options) error {
		// Serve until the rejection is recorded; the request must not stop the server.
		for {
			if _, err := os.Stat(report); err == nil {
				return ctx.Err()
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Millisecond):
			}
		}
	}
	if code := Run(context.Background(), []string{"serve"}, options); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if data, _ := os.ReadFile(report); string(data) != "config.decode: bad JSON" {
		t.Fatalf("report = %q", data)
	}
	if !strings.Contains(runtime.stderr.String(), "restart aborted, still serving: config.decode: bad JSON") {
		t.Fatalf("stderr = %q", runtime.stderr.String())
	}
}

func TestServeDoesNotReexecWithoutRequest(t *testing.T) {
	runtime := newTestRuntime()
	options := runtime.options()
	options.RestartRequests = func() (<-chan os.Signal, func()) { return make(chan os.Signal), func() {} }
	options.Reexec = func() error { t.Fatal("unexpected reexec"); return nil }
	if code := Run(context.Background(), []string{"serve"}, options); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
}

func TestServeCheckValidatesWithoutListening(t *testing.T) {
	runtime := newTestRuntime()
	if code := Run(context.Background(), []string{"serve", "--check"}, runtime.options()); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if runtime.serverCalls != 0 || !strings.Contains(runtime.stdout.String(), "configuration OK") {
		t.Fatalf("server calls = %d, stdout = %q", runtime.serverCalls, runtime.stdout.String())
	}

	runtime = newTestRuntime()
	options := runtime.options()
	options.ReadFile = func(string) ([]byte, error) { return []byte("\n"), nil }
	if code := Run(context.Background(), []string{"serve", "--check"}, options); code != 3 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
}

func TestServiceInstallWritesUnitAndStartsIt(t *testing.T) {
	runtime := newRestartRuntime(t)
	if code := runtime.run("service", "install"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	unit, err := os.ReadFile(filepath.Join(runtime.unitDir, "op.service"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ExecStart=/opt/op --config /config/config.json serve\n", "Environment=PATH=/home/me/bin:/usr/bin\n"} {
		if !strings.Contains(string(unit), want) {
			t.Fatalf("unit missing %q:\n%s", want, unit)
		}
	}
	want := []string{"systemctl --user daemon-reload", "systemctl --user enable op.service", "systemctl --user restart op.service"}
	if !slices.Equal(runtime.commands.calls, want) {
		t.Fatalf("commands = %q", runtime.commands.calls)
	}
}

func TestServiceInstallDoesNotStartOverUnmanagedServer(t *testing.T) {
	runtime := newRestartRuntime(t)
	runtime.processes.processes = []serveproc.Process{serveProcess(42, "op", "serve")}
	if code := runtime.run("service", "install"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if slices.Contains(runtime.commands.calls, "systemctl --user restart op.service") || !strings.Contains(runtime.stdout.String(), "(pid 42)") {
		t.Fatalf("commands = %q, stdout = %q", runtime.commands.calls, runtime.stdout.String())
	}
}

func TestServiceInstallRequiresForceToReplaceDifferentUnit(t *testing.T) {
	runtime := newRestartRuntime(t)
	if err := os.MkdirAll(runtime.unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runtime.unitDir, "op.service")
	if err := os.WriteFile(path, []byte("# customized\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := runtime.run("service", "install", "--no-start"); code != 5 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if code := runtime.run("service", "install", "--no-start", "--force"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "ExecStart=") {
		t.Fatalf("unit = %s", data)
	}
	if slices.Contains(runtime.commands.calls, "systemctl --user restart op.service") {
		t.Fatalf("commands = %q", runtime.commands.calls)
	}
}

func TestServiceUninstallStopsAndRemovesUnit(t *testing.T) {
	runtime := newRestartRuntime(t)
	if code := runtime.run("service", "uninstall"); code != 4 {
		t.Fatalf("exit = %d", code)
	}
	if code := runtime.run("service", "install", "--no-start"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	runtime.commands.calls = nil
	if code := runtime.run("service", "uninstall"); code != 0 {
		t.Fatalf("exit = %d, stderr = %s", code, runtime.stderr.String())
	}
	if _, err := os.Stat(filepath.Join(runtime.unitDir, "op.service")); !os.IsNotExist(err) {
		t.Fatalf("unit still present: %v", err)
	}
	if !slices.Equal(runtime.commands.calls, []string{"systemctl --user disable --now op.service", "systemctl --user daemon-reload"}) {
		t.Fatalf("commands = %q", runtime.commands.calls)
	}
}

func TestServiceRequiresSystemd(t *testing.T) {
	runtime := newRestartRuntime(t)
	runtime.booted = false
	if code := runtime.run("service", "start"); code != 7 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(runtime.stderr.String(), "systemd is not running") {
		t.Fatalf("stderr = %q", runtime.stderr.String())
	}
}

func TestServiceControlAndLogsCommands(t *testing.T) {
	runtime := newRestartRuntime(t)
	for _, args := range [][]string{{"service", "stop"}, {"service", "logs", "-f", "-n", "5"}} {
		if code := runtime.run(args...); code != 0 {
			t.Fatalf("%q exit = %d, stderr = %s", args, code, runtime.stderr.String())
		}
	}
	want := []string{"systemctl --user stop op.service", "journalctl --user --unit op.service --no-pager --lines 5 --follow"}
	if !slices.Equal(runtime.commands.calls, want) {
		t.Fatalf("commands = %q", runtime.commands.calls)
	}
}

func TestSystemctlFailureIncludesStderr(t *testing.T) {
	runtime := newRestartRuntime(t)
	runtime.commands.errs["systemctl --user start op.service"] = errors.New("exit status 5")
	if code := runtime.run("service", "start"); code != 7 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(runtime.stderr.String(), "systemctl --user start op.service failed: exit status 5") {
		t.Fatalf("stderr = %q", runtime.stderr.String())
	}
}
