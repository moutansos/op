//go:build linux

package process

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const execRunnerTestTimeout = 5 * time.Second

type markerWriter struct {
	mu     sync.Mutex
	buffer bytes.Buffer
	marker string
	found  chan string
	once   sync.Once
}

func newMarkerWriter(marker string) *markerWriter {
	return &markerWriter{marker: marker, found: make(chan string, 1)}
}

func (w *markerWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buffer.Write(data)
	text := w.buffer.String()
	if index := strings.Index(text, w.marker); index >= 0 {
		line := text[index:]
		if end := strings.IndexByte(line, '\n'); end >= 0 {
			line = line[:end]
		}
		w.once.Do(func() { w.found <- line })
	}
	return n, err
}

func TestExecCommandRunnerCancellationTerminatesImmediateProcess(t *testing.T) {
	ready := newMarkerWriter("exec-runner-ready")
	ctx, cancel := context.WithCancel(context.Background())
	runner := execCommandRunner{stdout: ready, stderr: io.Discard}
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(ctx, helperCommand(t, "wait"))
	}()

	receiveMarker(t, ready.found)
	cancel()
	if err := receiveProcessResult(t, result); err == nil {
		t.Fatal("canceled process returned no error")
	}
}

func TestExecCommandRunnerCancellationTerminatesDescendants(t *testing.T) {
	ready := newMarkerWriter("descendant-pid=")
	ctx, cancel := context.WithCancel(context.Background())
	runner := execCommandRunner{stdout: ready, stderr: io.Discard}
	result := make(chan error, 1)
	go func() {
		result <- runner.Run(ctx, helperCommand(t, "spawn"))
	}()

	marker := receiveMarker(t, ready.found)
	pid, err := strconv.Atoi(strings.TrimPrefix(marker, "descendant-pid="))
	if err != nil {
		t.Fatalf("parse descendant marker %q: %v", marker, err)
	}
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	cancel()
	if err := receiveProcessResult(t, result); err == nil {
		t.Fatal("canceled process tree returned no error")
	}
	if !waitForProcessExit(pid, execRunnerTestTimeout) {
		t.Fatalf("descendant process %d survived cancellation", pid)
	}
}

func TestExecRunnerHelperProcess(t *testing.T) {
	mode := helperMode(os.Args)
	if mode == "" {
		return
	}
	switch mode {
	case "wait", "descendant":
		fmt.Fprintln(os.Stdout, "exec-runner-ready")
		time.Sleep(time.Hour)
	case "spawn":
		command := exec.Command(os.Args[0], "-test.run=^TestExecRunnerHelperProcess$", "--", "descendant")
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		if err := command.Start(); err != nil {
			t.Fatalf("start descendant: %v", err)
		}
		fmt.Fprintf(os.Stdout, "descendant-pid=%d\n", command.Process.Pid)
		time.Sleep(time.Hour)
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
}

func helperCommand(t *testing.T, mode string) Command {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test executable: %v", err)
	}
	return Command{
		Directory: t.TempDir(),
		Name:      executable,
		Args:      []string{"-test.run=^TestExecRunnerHelperProcess$", "--", mode},
	}
}

func helperMode(args []string) string {
	for index, arg := range args {
		if arg == "--" && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

func receiveMarker(t *testing.T, channel <-chan string) string {
	t.Helper()
	select {
	case marker := <-channel:
		return marker
	case <-time.After(execRunnerTestTimeout):
		t.Fatal("timed out waiting for helper process marker")
		return ""
	}
}

func receiveProcessResult(t *testing.T, channel <-chan error) error {
	t.Helper()
	select {
	case err := <-channel:
		return err
	case <-time.After(execRunnerTestTimeout):
		t.Fatal("timed out waiting for canceled process")
		return nil
	}
}

func waitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
		if errors.Is(err, os.ErrNotExist) {
			return true
		}
		if err == nil {
			closeName := bytes.LastIndexByte(data, ')')
			if closeName >= 0 {
				fields := strings.Fields(string(data[closeName+1:]))
				if len(fields) > 0 && fields[0] == "Z" {
					return true
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}
