//go:build linux

package serveproc

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type fakeProc struct {
	t    *testing.T
	root string
}

func (f fakeProc) write(pid int, name, content string) {
	f.t.Helper()
	path := filepath.Join(f.root, strconv.Itoa(pid), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f fakeProc) link(pid int, name, target string) {
	f.t.Helper()
	path := filepath.Join(f.root, strconv.Itoa(pid), name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	_ = os.Remove(path)
	if err := os.Symlink(target, path); err != nil {
		f.t.Fatal(err)
	}
}

func (f fakeProc) process(pid int, state string, args ...string) {
	f.write(pid, "stat", strconv.Itoa(pid)+" (op) "+state+" 1 2 3")
	f.write(pid, "cmdline", strings.Join(args, "\x00")+"\x00")
	f.write(pid, "environ", "HOME=/home/me\x00")
	f.write(pid, "cgroup", "0::/user.slice/user-1000.slice/user@1000.service/app.slice/op.service\n")
	f.link(pid, "exe", "/usr/local/bin/op (deleted)")
}

func (f fakeProc) listen(pid int) {
	f.link(pid, "fd/3", "socket:[4242]")
	f.write(pid, "net/tcp", "  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode\n"+
		"   0: 0100007F:224B 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 4242 1\n")
}

func newFakeHost(t *testing.T) (*Host, fakeProc) {
	root := t.TempDir()
	return &Host{ProcRoot: root, UID: os.Getuid(), Self: 1, PollInterval: time.Millisecond}, fakeProc{t: t, root: root}
}

func TestListReadsMatchingProcesses(t *testing.T) {
	host, proc := newFakeHost(t)
	proc.process(20, "S", "op", "serve")
	proc.process(10, "S", "op", "--config", "/c.json", "serve")
	proc.process(30, "Z", "op", "serve")
	proc.process(1, "S", "op", "serve")
	proc.process(40, "S", "vim")
	processes, err := host.List(func(process Process) bool { return process.Args[0] == "op" })
	if err != nil {
		t.Fatal(err)
	}
	if len(processes) != 2 || processes[0].PID != 10 || processes[1].PID != 20 {
		t.Fatalf("processes = %+v", processes)
	}
	first := processes[0]
	if first.Executable != "/usr/local/bin/op" || first.Unit != "op.service" || !first.UserUnit || len(first.Args) != 4 {
		t.Fatalf("process = %+v", first)
	}
}

func TestRestartWaitsForNewImageToListen(t *testing.T) {
	host, proc := newFakeHost(t)
	proc.process(10, "S", "op", "serve")
	proc.listen(10)
	host.Kill = func(pid int, signal syscall.Signal) error {
		if pid != 10 || signal != RestartSignal {
			t.Errorf("kill(%d, %v)", pid, signal)
		}
		proc.write(10, "environ", GenerationEnv+"=1\x00")
		return nil
	}
	// The old listener is still present before the image changes; Restart must
	// not report success until the generation advances.
	if err := host.Restart(context.Background(), Process{PID: 10}); err != nil {
		t.Fatal(err)
	}
}

func TestRestartReportsProcessesThatExit(t *testing.T) {
	host, proc := newFakeHost(t)
	proc.process(10, "S", "op", "serve")
	host.Kill = func(int, syscall.Signal) error {
		return os.RemoveAll(filepath.Join(proc.root, "10"))
	}
	if err := host.Restart(context.Background(), Process{PID: 10}); !errors.Is(err, ErrExited) {
		t.Fatalf("err = %v", err)
	}

	proc.process(11, "S", "op", "serve")
	exited := make(chan error, 1)
	host.Kill = func(int, syscall.Signal) error {
		proc.write(11, "environ", GenerationEnv+"=1\x00")
		// The replacement image fails shortly after it starts.
		time.AfterFunc(20*time.Millisecond, func() {
			exited <- os.WriteFile(filepath.Join(proc.root, "11", "stat"), []byte("11 (op) Z 1 2 3"), 0o644)
		})
		return nil
	}
	if err := host.Restart(context.Background(), Process{PID: 11}); !errors.Is(err, ErrExitedAfterRestart) {
		t.Fatalf("err = %v", err)
	}
	if err := <-exited; err != nil {
		t.Fatal(err)
	}
}

func TestRestartTimesOutWhileShuttingDown(t *testing.T) {
	host, proc := newFakeHost(t)
	proc.process(10, "S", "op", "serve")
	host.Kill = func(int, syscall.Signal) error { return nil }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	err := host.Restart(ctx, Process{PID: 10})
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "shutting down") {
		t.Fatalf("err = %v", err)
	}
}

func TestRestartReportsRejectedReplacement(t *testing.T) {
	host, proc := newFakeHost(t)
	host.StatusDir = t.TempDir()
	proc.process(10, "S", "op", "serve")
	if err := WriteRestartError(host.StatusDir, 10, errors.New("stale")); err != nil {
		t.Fatal(err)
	}
	host.Kill = func(int, syscall.Signal) error {
		if _, err := os.Stat(RestartErrorPath(host.StatusDir, 10)); !os.IsNotExist(err) {
			t.Error("stale report was not cleared before signalling")
		}
		return WriteRestartError(host.StatusDir, 10, errors.New("config.decode: bad JSON"))
	}
	err := host.Restart(context.Background(), Process{PID: 10})
	if !errors.Is(err, ErrRestartRejected) || !strings.Contains(err.Error(), "bad JSON") {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(RestartErrorPath(host.StatusDir, 10)); !os.IsNotExist(statErr) {
		t.Fatal("report was not consumed")
	}
}
