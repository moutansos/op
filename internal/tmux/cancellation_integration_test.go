//go:build linux

package tmux

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/moutansos/op/internal/domain"
)

const (
	sigkillOwnerHelperEnv = "OP_TMUX_SIGKILL_OWNER_HELPER"
	sigkillOwnerSocketEnv = "OP_TMUX_SIGKILL_OWNER_SOCKET"
	sigkillOwnerReadyEnv  = "OP_TMUX_SIGKILL_OWNER_READY"
)

func TestIntegrationReadsExistingDelimiterLikeNamesAndPaths(t *testing.T) {
	requireTmuxIntegration(t)
	executable, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	root := t.TempDir()
	path := filepath.Join(root, " path-:-with\ttab")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := rawTmux{executable: executable, socket: integrationSocket(t, executable)}
	if _, err := raw.run(context.Background(), "new-session", "-d", "-s", "code", "-n", " legacy-:-window ", "-c", path); err != nil {
		t.Fatal(err)
	}
	manager, err := New(context.Background(), integrationConfig(root, raw.socket))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Session == nil || len(snapshot.Session.Windows) != 1 || len(snapshot.Session.Windows[0].Panes) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	window := snapshot.Session.Windows[0]
	if window.Name != " legacy-:-window " || window.Panes[0].CurrentPath != path {
		t.Fatalf("delimiter-like state changed: window=%q path=%q", window.Name, window.Panes[0].CurrentPath)
	}
	quotedPath := filepath.Join(root, "quoted' path; still-one")
	if err := os.Mkdir(quotedPath, 0o755); err != nil {
		t.Fatal(err)
	}
	const quotedName = "guarded' name; still-one"
	createdID, err := manager.client.CreateWindow(context.Background(), "code", quotedName, quotedPath, "sleep 300")
	if err != nil {
		t.Fatalf("guarded mutation with quoted arguments: %v", err)
	}
	windows, err := manager.client.ListWindows(context.Background(), "code")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range windows {
		if candidate.ID != createdID {
			continue
		}
		panes, paneErr := manager.client.ListPanes(context.Background(), createdID)
		if paneErr != nil || len(panes) != 1 || candidate.Name != quotedName || panes[0].CurrentPath != quotedPath {
			t.Fatalf("quoted guarded mutation = %+v, panes=%+v, error=%v", candidate, panes, paneErr)
		}
		found = true
	}
	if !found {
		t.Fatalf("created window %s was not returned", createdID)
	}
}

func TestIntegrationFalseMutationGuardFailsClosed(t *testing.T) {
	requireTmuxIntegration(t)
	executable, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	root := t.TempDir()
	raw := rawTmux{executable: executable, socket: integrationSocket(t, executable)}
	if _, err := raw.run(context.Background(), "new-session", "-d", "-s", "anchor", "sleep 300"); err != nil {
		t.Fatal(err)
	}
	guard := filepath.Join(root, "false-guard")
	result := guard + ".result"
	nonce := strings.Repeat("0", 32)
	started, alive := linuxProcessStart(os.Getpid())
	if !alive {
		t.Fatal("test owner identity unavailable")
	}
	if err := os.WriteFile(guard, []byte(fmt.Sprintf("%d %s %s\n", os.Getpid(), started, nonce)), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(result, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	falseArgs := guardedTmuxArgs("false", tmuxCommandString([]string{"new-window", "-d", "-t", "anchor", "-n", "must-not-appear"}), guard, result, mutationAckAfter, "", false)
	if _, err := raw.run(context.Background(), falseArgs...); err != nil {
		t.Fatalf("queue false guard: %v", err)
	}
	status, err := awaitMutationResult(context.Background(), result, mutationAckAfter)
	if err != nil || status != 125 {
		t.Fatalf("false guard acknowledgement = %d, %v; want 125", status, err)
	}
	windows, err := raw.run(context.Background(), "list-windows", "-t", "anchor", "-F", "#{window_name}")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(windows, "must-not-appear") {
		t.Fatalf("false guard executed mutation: %q", windows)
	}
}

func TestIntegrationStoppedServerHonorsDeadlinesAndPreventsLateMutation(t *testing.T) {
	manager, raw, _ := newDashboardIntegration(t)
	pidValue, err := raw.run(context.Background(), "display-message", "-p", "#{pid}")
	if err != nil {
		t.Fatal(err)
	}
	serverPID, err := strconv.Atoi(strings.TrimSpace(pidValue))
	if err != nil {
		t.Fatal(err)
	}
	session, err := manager.client.Session(context.Background(), manager.config.Session)
	if err != nil || session == nil {
		t.Fatalf("query session before stop: %+v, %v", session, err)
	}
	manager.lookupEnv = func(string) string { return "" }

	runStopped := func(t *testing.T, call func(context.Context) error) error {
		t.Helper()
		if err := syscall.Kill(serverPID, syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		resumed := false
		defer func() {
			if !resumed {
				_ = syscall.Kill(serverPID, syscall.SIGCONT)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
		defer cancel()
		started := time.Now()
		err := call(ctx)
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("stopped-server operation exceeded deadline bound: %v", elapsed)
		}
		if resumeErr := syscall.Kill(serverPID, syscall.SIGCONT); resumeErr != nil {
			t.Fatal(resumeErr)
		}
		resumed = true
		probeCtx, probeCancel := context.WithTimeout(context.Background(), time.Second)
		defer probeCancel()
		if _, probeErr := raw.run(probeCtx, "has-session", "-t", manager.config.Session); probeErr != nil {
			t.Fatalf("server did not recover after SIGCONT: %v", probeErr)
		}
		return err
	}

	for _, test := range []struct {
		name string
		call func(context.Context) error
	}{
		{name: "ensure", call: func(ctx context.Context) error { _, err := manager.EnsureMainSession(ctx); return err }},
		{name: "snapshot", call: func(ctx context.Context) error { _, err := manager.Snapshot(ctx); return err }},
		{name: "open", call: func(ctx context.Context) error {
			_, err := manager.OpenProjectWindow(ctx, OpenProjectWindowRequest{Project: domain.Project{ID: "stopped", Name: "stopped", Path: manager.config.StartDirectory}})
			return err
		}},
		{name: "attach-manager", call: func(ctx context.Context) error { return executeAttachOrSwitch(ctx, manager) }},
		{name: "attach", call: func(ctx context.Context) error {
			return raw.runInteractiveMutation(ctx, io.Discard, io.Discard, "attach-session", "-t", session.ID)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := runStopped(t, test.call)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("error = %v, want deadline exceeded", err)
			}
			if test.name != "attach" && !domain.IsCode(err, domain.ErrorCodeTimeout) {
				t.Fatalf("error = %v, want typed timeout", err)
			}
		})
	}

	windowsBefore, err := manager.client.ListWindows(context.Background(), manager.config.Session)
	if err != nil {
		t.Fatal(err)
	}
	err = runStopped(t, func(ctx context.Context) error {
		_, createErr := manager.client.CreateWindow(ctx, manager.config.Session, "must-not-appear", manager.config.StartDirectory, "sleep 300")
		return createErr
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stopped mutation error = %v, want deadline exceeded", err)
	}
	time.Sleep(100 * time.Millisecond)
	windowsAfter, err := manager.client.ListWindows(context.Background(), manager.config.Session)
	if err != nil {
		t.Fatal(err)
	}
	if len(windowsAfter) != len(windowsBefore) {
		t.Fatalf("mutation completed after cancellation: before=%+v after=%+v", windowsBefore, windowsAfter)
	}
	err = runStopped(t, func(ctx context.Context) error {
		return manager.client.CreateSession(ctx, "must-not-appear", manager.config.StartDirectory, "sleep 300")
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stopped session creation error = %v, want deadline exceeded", err)
	}
	lateSession, err := manager.client.Session(context.Background(), "must-not-appear")
	if err != nil || lateSession != nil {
		t.Fatalf("session completed after cancellation: %+v, %v", lateSession, err)
	}
}

func TestIntegrationConfiguredSocketBootstrapCannotCompleteAfterCancellation(t *testing.T) {
	requireTmuxIntegration(t)
	executable, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	root := t.TempDir()
	raw := rawTmux{executable: executable, socket: integrationSocket(t, executable)}
	if _, err := raw.run(context.Background(), "new-session", "-d", "-s", "anchor"); err != nil {
		t.Fatal(err)
	}
	serverPID := integrationServerPID(t, raw)
	stopServer := func() func() {
		if err := syscall.Kill(serverPID, syscall.SIGSTOP); err != nil {
			t.Fatal(err)
		}
		resumed := false
		return func() {
			if !resumed {
				_ = syscall.Kill(serverPID, syscall.SIGCONT)
				resumed = true
			}
		}
	}

	resume := stopServer()
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	_, err = New(ctx, integrationConfig(root, raw.socket))
	cancel()
	resume()
	if !errors.Is(err, context.DeadlineExceeded) || !domain.IsCode(err, domain.ErrorCodeTimeout) {
		t.Fatalf("stopped New() error = %v, want typed deadline", err)
	}
	if _, err := raw.run(context.Background(), "has-session", "-t", "code"); err == nil {
		t.Fatal("configured-socket initialization created session after cancellation")
	}

	resume = stopServer()
	ctx, cancel = context.WithTimeout(context.Background(), 75*time.Millisecond)
	_, err = raw.runSessionCreation(ctx, "new-session", "-d", "-s", "late-bootstrap")
	cancel()
	resume()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stopped bootstrap mutation error = %v, want deadline", err)
	}
	if _, err := raw.run(context.Background(), "has-session", "-t", "late-bootstrap"); err == nil {
		t.Fatal("queued bootstrap created session after cancellation")
	}
}

func TestIntegrationSoleWindowRollbackUsesDispatchAcknowledgement(t *testing.T) {
	requireTmuxIntegration(t)
	executable, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	root := t.TempDir()
	raw := rawTmux{executable: executable, socket: integrationSocket(t, executable)}
	if _, err := raw.run(context.Background(), "new-session", "-d", "-s", "anchor", "sleep 300"); err != nil {
		t.Fatal(err)
	}
	windowID, err := raw.run(context.Background(), "display-message", "-p", "-t", "anchor", "#{window_id}")
	if err != nil {
		t.Fatal(err)
	}
	manager := newManager(integrationConfig(root, raw.socket), &commandClient{raw: raw}, false)
	started := time.Now()
	if err := manager.rollbackWindow(context.Background(), strings.TrimSpace(windowID)); err != nil {
		t.Fatalf("rollbackWindow() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("sole-window rollback waited for impossible post-destroy acknowledgement: %v", elapsed)
	}
	if _, err := os.Stat(raw.socket); err == nil {
		if _, err := raw.run(context.Background(), "has-session", "-t", "anchor"); err == nil {
			t.Fatal("sole session survived rollback")
		}
	}
}

func TestIntegrationSIGKILLGuardOwnerFailsClosedAndCleansStaleGuard(t *testing.T) {
	requireTmuxIntegration(t)
	executable, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	root := t.TempDir()
	raw := rawTmux{executable: executable, socket: integrationSocket(t, executable)}
	if _, err := raw.run(context.Background(), "new-session", "-d", "-s", "anchor"); err != nil {
		t.Fatal(err)
	}
	serverPID := integrationServerPID(t, raw)
	if err := syscall.Kill(serverPID, syscall.SIGSTOP); err != nil {
		t.Fatal(err)
	}
	resumed := false
	defer func() {
		if !resumed {
			_ = syscall.Kill(serverPID, syscall.SIGCONT)
		}
	}()

	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(root, "owner-ready")
	command := exec.Command(testExecutable, "-test.run=^TestIntegrationSIGKILLGuardOwnerHelper$")
	command.Env = append(os.Environ(),
		sigkillOwnerHelperEnv+"=1",
		sigkillOwnerSocketEnv+"="+raw.socket,
		sigkillOwnerReadyEnv+"="+ready,
	)
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waitForSwitchResult(t, ready)
	childPID := waitForChildProcess(t, command.Process.Pid)
	guard := waitForOwnerGuard(t, command.Process.Pid)
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	deadline := time.Now().Add(time.Second)
	for linuxProcessExists(childPID) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if linuxProcessExists(childPID) {
		t.Fatalf("tmux client %d survived owner SIGKILL while server remained stopped", childPID)
	}
	if _, err := os.Stat(guard); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect guard after owner SIGKILL: %v", err)
	}
	if err := syscall.Kill(serverPID, syscall.SIGCONT); err != nil {
		t.Fatal(err)
	}
	resumed = true
	if _, err := raw.run(context.Background(), "has-session", "-t", "anchor"); err != nil {
		t.Fatalf("server did not resume: %v", err)
	}
	if _, err := raw.run(context.Background(), "has-session", "-t", "must-not-appear"); err == nil {
		t.Fatal("SIGKILL-stale guard allowed queued session bootstrap")
	}
	directory := filepath.Dir(guard)
	if err := cleanStaleMutationGuards(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(guard); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale guard was not cleaned: %v", err)
	}
}

func TestIntegrationSIGKILLGuardOwnerHelper(t *testing.T) {
	if os.Getenv(sigkillOwnerHelperEnv) != "1" {
		t.Skip("integration helper process")
	}
	executable, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv(sigkillOwnerReadyEnv), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := rawTmux{executable: executable, socket: os.Getenv(sigkillOwnerSocketEnv)}
	_, err = raw.runSessionCreation(context.Background(), "new-session", "-d", "-s", "must-not-appear", "sleep 300")
	if err != nil {
		t.Fatal(err)
	}
}

func waitForChildProcess(t *testing.T, parentPID int) int {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir("/proc")
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}
			value, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
			if err != nil {
				continue
			}
			closing := strings.LastIndex(string(value), ") ")
			if closing < 0 {
				continue
			}
			fields := strings.Fields(string(value)[closing+2:])
			if len(fields) > 1 && fields[1] == strconv.Itoa(parentPID) {
				return pid
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("tmux child process was not observed")
	return 0
}

func waitForOwnerGuard(t *testing.T, ownerPID int) string {
	t.Helper()
	directory, err := mutationGuardDirectory()
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		entries, _ := os.ReadDir(directory)
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".result") {
				continue
			}
			path := filepath.Join(directory, entry.Name())
			record, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			pid, _, _, err := parseMutationGuardRecord(string(record))
			if err == nil && pid == ownerPID {
				return path
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("owner mutation guard was not observed")
	return ""
}

func linuxProcessExists(pid int) bool {
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
}

func integrationServerPID(t *testing.T, raw rawTmux) int {
	t.Helper()
	value, err := raw.run(context.Background(), "display-message", "-p", "#{pid}")
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}
