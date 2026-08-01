package tmux

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moutansos/op/internal/domain"
)

const (
	switchHelperEnv        = "OP_TMUX_SWITCH_HELPER"
	switchSocketEnv        = "OP_TMUX_SWITCH_SOCKET"
	switchResultEnv        = "OP_TMUX_SWITCH_RESULT"
	switchHelperResult     = "ok"
	foreignHelperEnv       = "OP_TMUX_FOREIGN_HELPER"
	foreignSocketEnv       = "OP_TMUX_FOREIGN_MANAGED_SOCKET"
	foreignResultEnv       = "OP_TMUX_FOREIGN_RESULT"
	foreignHelperSuccess   = "rejected"
	outsideAttachHelperEnv = "OP_TMUX_OUTSIDE_ATTACH_HELPER"
	outsideAttachSocketEnv = "OP_TMUX_OUTSIDE_ATTACH_SOCKET"
	outsideAttachReadyEnv  = "OP_TMUX_OUTSIDE_ATTACH_READY"
)

func TestIntegrationDashboardAndProjectPaneLayout(t *testing.T) {
	if os.Getenv("OP_TMUX_INTEGRATION") != "1" {
		t.Skip("set OP_TMUX_INTEGRATION=1 to run isolated tmux integration tests")
	}
	executable, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}

	root := t.TempDir()
	socket := integrationSocket(t, executable)
	projectPath := filepath.Join(root, "project")
	if err := os.Mkdir(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	raw := rawTmux{executable: executable, socket: socket}

	config := ManagerConfig{
		Session: "code", DashboardWindow: "op", Socket: socket, StartDirectory: root,
		DashboardCommand: "sleep 300", EditorCommand: "sleep 300", PreferredShell: "sh",
		ShellPaneRows: 10, DefaultProfile: "integration",
	}
	manager, err := New(context.Background(), config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ensured, err := manager.EnsureMainSession(context.Background())
	if err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	if !ensured.Created || len(ensured.Session.Windows) != 1 {
		t.Fatalf("ensure result = %+v", ensured)
	}
	dashboard := ensured.Session.Windows[0]
	if dashboard.Name != "op" || dashboard.Index != 0 || len(dashboard.Panes) != 1 || dashboard.Panes[0].Dead {
		t.Fatalf("dashboard = %+v", dashboard)
	}
	waitForPaneCommand(t, manager, dashboard.ID, "sleep")
	role, exists, err := manager.client.WindowOption(context.Background(), dashboard.ID, optionRole)
	if err != nil || !exists || strings.TrimSpace(role) != roleDashboard {
		t.Fatalf("dashboard role = %q, %v, %v", role, exists, err)
	}
	managedPaneID, exists, err := manager.client.WindowOption(context.Background(), dashboard.ID, optionDashboardPane)
	if err != nil || !exists || managedPaneID != dashboard.Panes[0].ID {
		t.Fatalf("managed dashboard pane = %q, %v, %v; want %q", managedPaneID, exists, err, dashboard.Panes[0].ID)
	}
	managedPID, exists, err := manager.client.WindowOption(context.Background(), dashboard.ID, optionDashboardPID)
	if err != nil || !exists || managedPID != strconv.FormatInt(int64(dashboard.Panes[0].PID), 10) {
		t.Fatalf("managed dashboard pid = %q, %v, %v; want %d", managedPID, exists, err, dashboard.Panes[0].PID)
	}

	project := domain.Project{ID: "integration-project", Name: "project", Path: projectPath, Kind: domain.ProjectKindRepository}
	opened, err := manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
	if err != nil {
		t.Fatalf("OpenProjectWindow() error = %v", err)
	}
	if opened.Reused || len(opened.Window.Panes) != 2 {
		t.Fatalf("open result = %+v", opened)
	}
	for key, want := range map[string]string{
		optionProjectID: project.ID,
		optionPath:      project.Path,
		optionProfile:   config.DefaultProfile,
		optionOwner:     "1",
	} {
		value, exists, optionErr := manager.client.WindowOption(context.Background(), opened.Window.ID, key)
		if optionErr != nil || !exists || value != want {
			t.Fatalf("project option %s = %q, %v, %v; want %q", key, value, exists, optionErr, want)
		}
	}
	waitForPaneCommand(t, manager, opened.Window.ID, "sleep")
	for _, pane := range opened.Window.Panes {
		if pane.Dead || pane.CurrentPath != projectPath {
			t.Fatalf("project pane = %+v", pane)
		}
	}

	layout, err := raw.run(context.Background(), "list-panes", "-t", opened.Window.ID, "-F", "#{pane_id} #{pane_top} #{pane_height} #{pane_active}")
	if err != nil {
		t.Fatalf("query pane layout: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(layout), "\n")
	if len(lines) != 2 {
		t.Fatalf("pane layout = %q", layout)
	}
	tops := make(map[int]bool)
	activeHeight := 0
	shellHeight := 0
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 4 {
			t.Fatalf("pane layout line = %q", line)
		}
		top, _ := strconv.Atoi(fields[1])
		height, _ := strconv.Atoi(fields[2])
		tops[top] = true
		if fields[3] == "1" {
			activeHeight = height
		} else {
			shellHeight = height
		}
	}
	if len(tops) != 2 || shellHeight != config.ShellPaneRows || activeHeight <= shellHeight {
		t.Fatalf("expected top/bottom layout with shell height %d; got %q", config.ShellPaneRows, layout)
	}

	reused, err := manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
	if err != nil || !reused.Reused || reused.Window.ID != opened.Window.ID {
		t.Fatalf("reuse = %+v, error = %v", reused, err)
	}
	secondEnsure, err := manager.EnsureMainSession(context.Background())
	if err != nil || secondEnsure.Repaired || len(secondEnsure.Session.Windows) != 2 {
		t.Fatalf("idempotent ensure = %+v, error = %v", secondEnsure, err)
	}
	waitForPaneCommand(t, manager, dashboard.ID, "sleep")
}

func TestIntegrationShellStartupReadReceivesNoManagerInput(t *testing.T) {
	manager, raw, _ := newDashboardIntegration(t)
	root := manager.config.StartDirectory
	projectPath := filepath.Join(root, "startup-read-project")
	if err := os.Mkdir(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "startup-read-consumed")
	readScript := "if IFS= read -r value; then printf '%s' \"$value\" > " + shellQuote(marker) + "; fi; sleep 300"
	shellCommand := "exec /bin/sh -c " + shellQuote(readScript)

	opened, err := manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{
		Project:       domain.Project{ID: "startup-read", Name: "startup-read", Path: projectPath},
		EditorCommand: "sleep 300",
		ShellCommand:  shellCommand,
	})
	if err != nil {
		t.Fatalf("OpenProjectWindow() error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("shell startup read consumed manager input: %v", err)
	}

	shellPaneID := ""
	for _, pane := range opened.Window.Panes {
		if pane.CurrentCommand == "sh" {
			shellPaneID = pane.ID
			break
		}
	}
	if shellPaneID == "" {
		t.Fatalf("startup-read shell pane not found: %+v", opened.Window.Panes)
	}
	if _, err := raw.run(context.Background(), "send-keys", "-l", "-t", shellPaneID, "test-sentinel"); err != nil {
		t.Fatalf("send test input: %v", err)
	}
	if _, err := raw.run(context.Background(), "send-keys", "-t", shellPaneID, "Enter"); err != nil {
		t.Fatalf("submit test input: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		value, readErr := os.ReadFile(marker)
		if readErr == nil {
			if string(value) != "test-sentinel" {
				t.Fatalf("startup read consumed %q, want test sentinel", value)
			}
			return
		}
		if !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("shell did not consume external test input after manager returned")
}

func TestIntegrationEditorPaneSurvivesExitAndKeepsLongRunningEditor(t *testing.T) {
	manager, _, _ := newDashboardIntegration(t)
	root := manager.config.StartDirectory
	tests := []struct {
		name        string
		editor      string
		wantCommand string
	}{
		{name: "normal immediate exit", editor: "true", wantCommand: "sh"},
		{name: "immediate failure", editor: "false", wantCommand: "sh"},
		{name: "long running editor", editor: "sleep 300", wantCommand: "sleep"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projectPath := filepath.Join(root, strings.ReplaceAll(test.name, " ", "-"))
			if err := os.Mkdir(projectPath, 0o755); err != nil {
				t.Fatal(err)
			}
			project := domain.Project{ID: test.name, Name: test.name, Path: projectPath}
			opened, err := manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{
				Project: project, EditorCommand: test.editor,
			})
			if err != nil {
				t.Fatalf("OpenProjectWindow() error = %v", err)
			}
			if len(opened.Window.Panes) != 2 {
				t.Fatalf("project panes = %+v", opened.Window.Panes)
			}
			var editorPane *domain.TmuxPane
			for index := range opened.Window.Panes {
				if opened.Window.Panes[index].Active {
					editorPane = &opened.Window.Panes[index]
					break
				}
			}
			if editorPane == nil || editorPane.Dead || editorPane.CurrentPath != projectPath || editorPane.CurrentCommand != test.wantCommand {
				t.Fatalf("surviving editor pane = %+v, want live %q in %q", editorPane, test.wantCommand, projectPath)
			}
			reused, err := manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
			if err != nil || !reused.Reused || reused.Window.ID != opened.Window.ID {
				t.Fatalf("reusing surviving project window = %+v, error = %v", reused, err)
			}
		})
	}
}

func TestIntegrationCreateWindowIdentityAndRollbackWithConcurrentUserWindow(t *testing.T) {
	requireTmuxIntegration(t)
	executable, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	root := t.TempDir()
	firstPath := filepath.Join(root, "first")
	secondPath := filepath.Join(root, "second")
	for _, path := range []string{firstPath, secondPath} {
		if err := os.Mkdir(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	raw := rawTmux{executable: executable, socket: integrationSocket(t, executable)}
	manager, err := New(context.Background(), ManagerConfig{
		Session: "code", DashboardWindow: "op", Socket: raw.socket, StartDirectory: root,
		DashboardCommand: "sleep 300", EditorCommand: "sleep 300", PreferredShell: "sh",
		ShellPaneRows: 10, DefaultProfile: "integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.EnsureMainSession(context.Background()); err != nil {
		t.Fatal(err)
	}

	recording := &integrationIdentityClient{tmuxClient: manager.client, raw: raw, concurrentDirectory: root}
	manager.client = recording
	first := domain.Project{ID: "first-id", Name: "first", Path: firstPath}
	opened, err := manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: first})
	if err != nil {
		t.Fatalf("OpenProjectWindow() error = %v", err)
	}
	if len(recording.created) != 1 || opened.Window.ID != recording.created[0] || opened.Window.ID == recording.concurrent[0] {
		t.Fatalf("returned window = %q, created = %#v, concurrent = %#v", opened.Window.ID, recording.created, recording.concurrent)
	}
	identity, err := raw.run(context.Background(), "display-message", "-p", "-t", opened.Window.ID, "#{window_id}\t#{session_name}\t#{window_name}\t#{pane_current_path}\t#{pane_dead}")
	if err != nil {
		t.Fatalf("query returned window identity: %v", err)
	}
	wantIdentity := strings.Join([]string{opened.Window.ID, "code", "first", firstPath, "0"}, "\t")
	if got := strings.TrimSpace(identity); got != wantIdentity {
		t.Fatalf("returned window identity = %q, want %q", got, wantIdentity)
	}
	for key, want := range map[string]string{
		optionProjectID: first.ID,
		optionPath:      first.Path,
		optionProfile:   "integration",
		optionOwner:     "1",
	} {
		value, exists, optionErr := manager.client.WindowOption(context.Background(), opened.Window.ID, key)
		if optionErr != nil || !exists || value != want {
			t.Fatalf("returned window option %s = %q, %v, %v; want %q", key, value, exists, optionErr, want)
		}
	}

	recording.silentSplit = true
	second := domain.Project{ID: "second-id", Name: "second", Path: secondPath}
	_, err = manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: second})
	assertCode(t, err, domain.ErrorCodeDependency)
	rolledBackID := recording.created[1]
	concurrentID := recording.concurrent[1]
	if len(recording.killed) != 1 || recording.killed[0] != rolledBackID {
		t.Fatalf("rollback targets = %#v, want only %q", recording.killed, rolledBackID)
	}
	windowIDs, err := raw.run(context.Background(), "list-windows", "-t", "code", "-F", "#{window_id}")
	if err != nil {
		t.Fatalf("list windows after rollback: %v", err)
	}
	for _, windowID := range strings.Fields(windowIDs) {
		if windowID == rolledBackID {
			t.Fatalf("created window %q survived rollback: %q", rolledBackID, windowIDs)
		}
	}
	userIdentity, err := raw.run(context.Background(), "display-message", "-p", "-t", concurrentID, "#{window_id}\t#{window_name}\t#{pane_current_path}")
	if err != nil {
		t.Fatalf("concurrent user window %q was killed: %v", concurrentID, err)
	}
	wantUserIdentity := strings.Join([]string{concurrentID, "user-concurrent-2", root}, "\t")
	if got := strings.TrimSpace(userIdentity); got != wantUserIdentity {
		t.Fatalf("concurrent user identity = %q, want %q", got, wantUserIdentity)
	}
}

func TestIntegrationMalformedReturnedWindowIdentityCannotRollbackCanonicalUserWindow(t *testing.T) {
	manager, raw, dashboard := newDashboardIntegration(t)
	if dashboard.ID != "@0" {
		t.Fatalf("isolated dashboard ID = %q, want @0", dashboard.ID)
	}
	root := manager.config.StartDirectory
	projectPath := filepath.Join(root, "project")
	if err := os.Mkdir(projectPath, 0o755); err != nil {
		t.Fatal(err)
	}
	malformed := &integrationMalformedIdentityClient{tmuxClient: manager.client, raw: raw, directory: root}
	manager.client = malformed

	_, err := manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: domain.Project{
		ID: "project", Name: "project", Path: projectPath,
	}})
	assertCode(t, err, domain.ErrorCodeDependency)
	if !strings.Contains(err.Error(), "valid window identity") {
		t.Fatalf("malformed identity error = %v", err)
	}
	if malformed.canonicalID != "@1" {
		t.Fatalf("canonical user window ID = %q, want @1", malformed.canonicalID)
	}
	if len(malformed.killed) != 0 {
		t.Fatalf("malformed identity invoked kill-window for %#v", malformed.killed)
	}
	windows, err := raw.run(context.Background(), "list-windows", "-t", "code", "-F", "#{window_id}\t#{window_name}")
	if err != nil {
		t.Fatalf("list windows after malformed identity: %v", err)
	}
	wantUser := malformed.canonicalID + "\tcanonical-user"
	found := false
	for _, line := range strings.Split(strings.TrimSpace(windows), "\n") {
		if line == wantUser {
			found = true
		}
	}
	if !found {
		t.Fatalf("canonical user window %q was removed: %q", wantUser, windows)
	}
}

func TestIntegrationDashboardPreservesTrackedPaneRunningForegroundAction(t *testing.T) {
	manager, raw, dashboard := newDashboardIntegrationCommand(t, "bash")
	managedPaneID := dashboard.Panes[0].ID
	managedPID := dashboard.Panes[0].PID
	if _, err := raw.run(context.Background(), "send-keys", "-l", "-t", managedPaneID, "sleep 300"); err != nil {
		t.Fatalf("start foreground action: %v", err)
	}
	if _, err := raw.run(context.Background(), "send-keys", "-t", managedPaneID, "Enter"); err != nil {
		t.Fatalf("submit foreground action: %v", err)
	}
	waitForPaneCommand(t, manager, dashboard.ID, "sleep")

	preserved, err := manager.EnsureMainSession(context.Background())
	if err != nil || preserved.Repaired {
		t.Fatalf("foreground action reconciliation = %+v, %v", preserved, err)
	}
	if pane := findSnapshotPane(preserved, dashboard.ID, managedPaneID); pane == nil || pane.Dead || pane.CurrentCommand != "sleep" || pane.PID != managedPID {
		t.Fatalf("foreground action pane was replaced: %+v", pane)
	}
}

func TestIntegrationDashboardRepairsRespawnedProcessWithSamePaneID(t *testing.T) {
	manager, raw, dashboard := newDashboardIntegration(t)
	managedPaneID := dashboard.Panes[0].ID
	originalPID := dashboard.Panes[0].PID
	if _, err := raw.run(context.Background(), "respawn-pane", "-k", "-t", managedPaneID, "tail -f /dev/null"); err != nil {
		t.Fatalf("replace managed process: %v", err)
	}
	waitForPaneCommand(t, manager, dashboard.ID, "tail")
	replacement, err := manager.paneByID(context.Background(), dashboard.ID, managedPaneID)
	if err != nil || replacement == nil || replacement.PID == originalPID {
		t.Fatalf("replacement pane = %+v, %v", replacement, err)
	}

	repaired, err := manager.EnsureMainSession(context.Background())
	if err != nil || !repaired.Repaired {
		t.Fatalf("respawned replacement reconciliation = %+v, %v", repaired, err)
	}
	waitForPaneCommand(t, manager, dashboard.ID, "sleep")
	pane := findSnapshotPane(repaired, dashboard.ID, managedPaneID)
	if pane == nil || pane.PID == replacement.PID || pane.CurrentCommand != "sleep" {
		t.Fatalf("replacement process was trusted: replacement=%+v repaired=%+v", replacement, pane)
	}
	managedPID, exists, optionErr := manager.client.WindowOption(context.Background(), dashboard.ID, optionDashboardPID)
	if optionErr != nil || !exists || managedPID != strconv.FormatInt(int64(pane.PID), 10) {
		t.Fatalf("repaired managed pid = %q, %v, %v; pane=%+v", managedPID, exists, optionErr, pane)
	}
}

func TestIntegrationDashboardPreservesExtraPaneAndRecreatesMissingManagedPane(t *testing.T) {
	manager, raw, dashboard := newDashboardIntegration(t)
	managedPaneID := dashboard.Panes[0].ID
	if _, err := raw.run(context.Background(), "split-window", "-d", "-t", managedPaneID, "sleep 300"); err != nil {
		t.Fatalf("create user pane: %v", err)
	}
	panes, err := manager.client.ListPanes(context.Background(), dashboard.ID)
	if err != nil || len(panes) != 2 {
		t.Fatalf("dashboard panes = %+v, %v", panes, err)
	}
	userPaneID := otherPaneID(t, panes, managedPaneID)

	preserved, err := manager.EnsureMainSession(context.Background())
	if err != nil || preserved.Repaired || findSnapshotPane(preserved, dashboard.ID, userPaneID) == nil {
		t.Fatalf("extra pane preservation = %+v, %v", preserved, err)
	}
	if _, err := raw.run(context.Background(), "kill-pane", "-t", managedPaneID); err != nil {
		t.Fatalf("remove managed pane: %v", err)
	}
	repaired, err := manager.EnsureMainSession(context.Background())
	if err != nil || !repaired.Repaired || findSnapshotPane(repaired, dashboard.ID, userPaneID) == nil {
		t.Fatalf("missing managed pane repair = %+v, %v", repaired, err)
	}
	newManagedPaneID, exists, optionErr := manager.client.WindowOption(context.Background(), dashboard.ID, optionDashboardPane)
	if optionErr != nil || !exists || newManagedPaneID == managedPaneID || newManagedPaneID == userPaneID {
		t.Fatalf("recreated managed pane = %q, %v, %v", newManagedPaneID, exists, optionErr)
	}
	newManagedPID, exists, optionErr := manager.client.WindowOption(context.Background(), dashboard.ID, optionDashboardPID)
	newManagedPane := findSnapshotPane(repaired, dashboard.ID, newManagedPaneID)
	if optionErr != nil || !exists || newManagedPane == nil || newManagedPID != strconv.FormatInt(int64(newManagedPane.PID), 10) {
		t.Fatalf("recreated managed pid = %q, %v, %v; pane=%+v", newManagedPID, exists, optionErr, newManagedPane)
	}
	waitForPaneCommand(t, manager, dashboard.ID, "sleep")
}

func TestIntegrationDashboardRespawnsDeadManagedPaneWithoutDeletingExtraPane(t *testing.T) {
	manager, raw, dashboard := newDashboardIntegration(t)
	managedPaneID := dashboard.Panes[0].ID
	if _, err := raw.run(context.Background(), "split-window", "-d", "-t", managedPaneID, "sleep 300"); err != nil {
		t.Fatalf("create user pane: %v", err)
	}
	panes, err := manager.client.ListPanes(context.Background(), dashboard.ID)
	if err != nil || len(panes) != 2 {
		t.Fatalf("dashboard panes = %+v, %v", panes, err)
	}
	userPaneID := otherPaneID(t, panes, managedPaneID)
	if _, err := raw.run(context.Background(), "set-option", "-w", "-t", dashboard.ID, "remain-on-exit", "on"); err != nil {
		t.Fatalf("enable remain-on-exit: %v", err)
	}
	if _, err := raw.run(context.Background(), "respawn-pane", "-k", "-t", managedPaneID, "false"); err != nil {
		t.Fatalf("stop managed pane: %v", err)
	}
	waitForPaneDead(t, manager, dashboard.ID, managedPaneID)

	repaired, err := manager.EnsureMainSession(context.Background())
	if err != nil || !repaired.Repaired || findSnapshotPane(repaired, dashboard.ID, userPaneID) == nil {
		t.Fatalf("dead managed pane repair = %+v, %v", repaired, err)
	}
	if pane := findSnapshotPane(repaired, dashboard.ID, managedPaneID); pane == nil || pane.Dead {
		t.Fatalf("managed pane %s was not respawned in place: %+v", managedPaneID, pane)
	}
	waitForPaneCommand(t, manager, dashboard.ID, "sleep")
}

func TestIntegrationDashboardRecoversAfterSolePaneExits(t *testing.T) {
	manager, raw, dashboard := newDashboardIntegration(t)
	if _, err := raw.run(context.Background(), "kill-pane", "-t", dashboard.Panes[0].ID); err != nil {
		t.Fatalf("remove sole dashboard pane: %v", err)
	}

	repaired, err := manager.EnsureMainSession(context.Background())
	if err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	if !repaired.Created || !repaired.Repaired || len(repaired.Session.Windows) != 1 || len(repaired.Session.Windows[0].Panes) != 1 {
		t.Fatalf("sole dashboard recovery = %+v", repaired)
	}
	window := repaired.Session.Windows[0]
	managedPaneID, exists, optionErr := manager.client.WindowOption(context.Background(), window.ID, optionDashboardPane)
	if optionErr != nil || !exists || managedPaneID != window.Panes[0].ID {
		t.Fatalf("recovered managed pane = %q, %v, %v; window=%+v", managedPaneID, exists, optionErr, window)
	}
	managedPID, exists, optionErr := manager.client.WindowOption(context.Background(), window.ID, optionDashboardPID)
	if optionErr != nil || !exists || managedPID != strconv.FormatInt(int64(window.Panes[0].PID), 10) {
		t.Fatalf("recovered managed pid = %q, %v, %v; window=%+v", managedPID, exists, optionErr, window)
	}
	waitForPaneCommand(t, manager, window.ID, "sleep")
}

func TestIntegrationReadSnapshotDoesNotBootstrapConfiguredSocket(t *testing.T) {
	requireTmuxIntegration(t)
	executable, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	root := t.TempDir()
	socket := integrationSocket(t, executable)
	raw := rawTmux{executable: executable, socket: socket}
	config := ManagerConfig{
		Session: "code", DashboardWindow: "op", Socket: socket, StartDirectory: root,
		DashboardCommand: "sleep 300", EditorCommand: "sleep 300", PreferredShell: "sh",
		ShellPaneRows: 10, DefaultProfile: "integration",
	}

	snapshot, err := ReadSnapshot(context.Background(), config)
	if err != nil || snapshot.Session != nil {
		t.Fatalf("ReadSnapshot() = %+v, %v; want empty snapshot", snapshot, err)
	}
	if _, err := os.Stat(socket); !os.IsNotExist(err) {
		t.Fatalf("read-only snapshot created socket %q: %v", socket, err)
	}
	if _, err := raw.run(context.Background(), "has-session", "-t", config.Session); err == nil {
		t.Fatal("read-only snapshot created the managed session")
	}

	manager, err := New(context.Background(), config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	ensured, err := manager.EnsureMainSession(context.Background())
	if err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	if len(ensured.Session.Windows) != 1 || ensured.Session.Windows[0].Name != config.DashboardWindow {
		t.Fatalf("ensure windows = %+v; want exactly one dashboard and no bootstrap orphan", ensured.Session.Windows)
	}
	windows, err := raw.run(context.Background(), "list-windows", "-t", config.Session, "-F", "#{window_name}")
	windowNames := strings.Fields(windows)
	if err != nil || len(windowNames) != 1 || windowNames[0] != config.DashboardWindow {
		t.Fatalf("real tmux windows = %q, %v; want exactly %q", windows, err, config.DashboardWindow)
	}
}

func TestIntegrationAttachOrSwitchTargetsInvokingClient(t *testing.T) {
	requireTmuxIntegration(t)
	executable, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script is required to allocate an attached tmux client PTY")
	}

	root := t.TempDir()
	raw := rawTmux{executable: executable, socket: integrationSocket(t, executable)}
	if _, err := raw.run(context.Background(), "new-session", "-d", "-s", "external", "-n", "outside"); err != nil {
		t.Fatalf("create external session: %v", err)
	}
	if _, err := raw.run(context.Background(), "new-session", "-d", "-s", "observer", "-n", "watch"); err != nil {
		t.Fatalf("create observer session: %v", err)
	}
	externalPane, err := raw.run(context.Background(), "display-message", "-p", "-t", "external:outside", "#{pane_id}")
	if err != nil {
		t.Fatalf("query external pane: %v", err)
	}
	externalPane = strings.TrimSpace(externalPane)
	observerPane, err := raw.run(context.Background(), "display-message", "-p", "-t", "observer:watch", "#{pane_id}")
	if err != nil {
		t.Fatalf("query observer pane: %v", err)
	}
	observerPane = strings.TrimSpace(observerPane)

	manager, err := New(context.Background(), ManagerConfig{
		Session: "code", DashboardWindow: "op", Socket: raw.socket, StartDirectory: root,
		DashboardCommand: "sleep 300", EditorCommand: "sleep 300", PreferredShell: "sh",
		ShellPaneRows: 10, DefaultProfile: "integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.EnsureMainSession(context.Background()); err != nil {
		t.Fatal(err)
	}

	startAttachedClient(t, script, executable, raw.socket, "external")
	startAttachedClient(t, script, executable, raw.socket, "observer")
	before := waitForClientsAtPanes(t, raw, map[string]string{
		externalPane: "external",
		observerPane: "observer",
	})
	invokingClient := before[externalPane].Name
	observerClient := before[observerPane].Name
	if invokingClient == observerClient {
		t.Fatalf("both PTYs resolved to client %q", invokingClient)
	}

	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(root, "switch-result")
	helperCommand := strings.Join([]string{
		switchHelperEnv + "=1",
		switchSocketEnv + "=" + shellQuote(raw.socket),
		switchResultEnv + "=" + shellQuote(resultPath),
		shellQuote(testExecutable),
		"-test.run=^TestIntegrationAttachOrSwitchHelper$",
	}, " ")
	if _, err := raw.run(context.Background(), "send-keys", "-l", "-t", externalPane, helperCommand); err != nil {
		t.Fatalf("send switch helper command: %v", err)
	}
	if _, err := raw.run(context.Background(), "send-keys", "-t", externalPane, "Enter"); err != nil {
		t.Fatalf("start switch helper command: %v", err)
	}

	result := waitForSwitchResult(t, resultPath)
	if result != switchHelperResult {
		clients, _ := integrationClients(raw)
		t.Fatalf("AttachOrSwitch() in attached client: %s; clients=%+v", result, clients)
	}
	after := waitForNamedClientSessions(t, raw, map[string]string{
		invokingClient: "code",
		observerClient: "observer",
	})
	if after[observerClient].ActivePane != observerPane {
		t.Fatalf("observer active pane = %q, want %q", after[observerClient].ActivePane, observerPane)
	}
	paneLocation, err := raw.run(context.Background(), "display-message", "-p", "-t", externalPane, "#{session_name}:#{pane_id}")
	if err != nil {
		t.Fatalf("query original pane after switch: %v", err)
	}
	if got, want := strings.TrimSpace(paneLocation), "external:"+externalPane; got != want {
		t.Fatalf("original pane location = %q, want %q", got, want)
	}
}

func TestIntegrationPreparedOutsideAttachAllowsConcurrentMutation(t *testing.T) {
	requireTmuxIntegration(t)
	_, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script is required to allocate an attached tmux client PTY")
	}
	manager, raw, dashboard := newDashboardIntegration(t)
	root := manager.config.StartDirectory
	if _, err := manager.client.CreateWindow(context.Background(), manager.config.Session, "active-before-attach", root, "sleep 300"); err != nil {
		t.Fatal(err)
	}
	windows, err := manager.client.ListWindows(context.Background(), manager.config.Session)
	if err != nil {
		t.Fatal(err)
	}
	for _, window := range windows {
		if window.Name == "active-before-attach" {
			if err := manager.client.SelectWindow(context.Background(), window.ID); err != nil {
				t.Fatal(err)
			}
		}
	}

	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ready := filepath.Join(root, "outside-attach-ready")
	helperCommand := strings.Join([]string{
		outsideAttachHelperEnv + "=1",
		outsideAttachSocketEnv + "=" + shellQuote(raw.socket),
		outsideAttachReadyEnv + "=" + shellQuote(ready),
		"TMUX= TMUX_PANE=",
		shellQuote(testExecutable),
		"-test.run=^TestIntegrationPreparedOutsideAttachHelper$",
	}, " ")
	command := exec.Command(script, "-q", "-c", helperCommand, "/dev/null")
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	done := make(chan error, 1)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { done <- command.Wait() }()
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		select {
		case <-done:
		case <-time.After(time.Second):
		}
	})
	waitForSwitchResult(t, ready)
	dashboardPane := dashboard.Panes[0].ID
	waitForClientsAtPanes(t, raw, map[string]string{dashboardPane: manager.config.Session})

	if _, err := manager.client.CreateWindow(context.Background(), manager.config.Session, "created-during-attach", root, "sleep 300"); err != nil {
		t.Fatalf("mutation while outside attach was running: %v", err)
	}
	select {
	case err := <-done:
		t.Fatalf("outside attach exited during concurrent mutation: %v", err)
	default:
	}
}

func TestIntegrationPreparedOutsideAttachHelper(t *testing.T) {
	if os.Getenv(outsideAttachHelperEnv) != "1" {
		t.Skip("integration helper process")
	}
	socket := os.Getenv(outsideAttachSocketEnv)
	manager, err := New(context.Background(), integrationConfig(filepath.Dir(socket), socket))
	if err != nil {
		t.Fatal(err)
	}
	manager.lookupEnv = func(string) string { return "" }
	plan, err := manager.PrepareAttachOrSwitch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.RequiresSessionLock() {
		t.Fatal("outside attach unexpectedly requires session lock")
	}
	if err := os.WriteFile(os.Getenv(outsideAttachReadyEnv), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := manager.ExecuteAttachOrSwitch(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationAttachOrSwitchHelper(t *testing.T) {
	if os.Getenv(switchHelperEnv) != "1" {
		t.Skip("integration helper process")
	}
	socket := os.Getenv(switchSocketEnv)
	resultPath := os.Getenv(switchResultEnv)
	manager, err := New(context.Background(), ManagerConfig{
		Session: "code", DashboardWindow: "op", Socket: socket, StartDirectory: filepath.Dir(socket),
		DashboardCommand: "sleep 300", EditorCommand: "sleep 300", PreferredShell: "sh",
		ShellPaneRows: 10, DefaultProfile: "integration",
	})
	if err == nil {
		err = executeAttachOrSwitch(context.Background(), manager)
	}
	result := switchHelperResult
	if err != nil {
		result = err.Error()
	}
	if writeErr := os.WriteFile(resultPath, []byte(result), 0o600); writeErr != nil {
		t.Fatalf("write helper result: %v", writeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationForeignServerCallerCannotTargetManagedServer(t *testing.T) {
	requireTmuxIntegration(t)
	executable, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	script, err := exec.LookPath("script")
	if err != nil {
		t.Skip("script is required to allocate attached tmux client PTYs")
	}

	root := t.TempDir()
	managed := rawTmux{executable: executable, socket: integrationSocket(t, executable)}
	foreign := rawTmux{executable: executable, socket: integrationSocket(t, executable)}
	if _, err := managed.run(context.Background(), "new-session", "-d", "-s", "external", "-n", "outside"); err != nil {
		t.Fatalf("create managed external session: %v", err)
	}
	if _, err := foreign.run(context.Background(), "new-session", "-d", "-s", "foreign", "-n", "foreign-project"); err != nil {
		t.Fatalf("create foreign session: %v", err)
	}
	managedPane := integrationPaneID(t, managed, "external:outside")
	foreignPane := integrationPaneID(t, foreign, "foreign:foreign-project")
	if managedPane != foreignPane {
		t.Fatalf("test requires colliding pane IDs, managed=%q foreign=%q", managedPane, foreignPane)
	}

	manager, err := New(context.Background(), ManagerConfig{
		Session: "code", DashboardWindow: "op", Socket: managed.socket, StartDirectory: root,
		DashboardCommand: "sleep 300", EditorCommand: "sleep 300", PreferredShell: "sh",
		ShellPaneRows: 10, DefaultProfile: "integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.EnsureMainSession(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := managed.run(context.Background(), "new-window", "-d", "-t", "code", "-n", "managed-project"); err != nil {
		t.Fatalf("create managed project window: %v", err)
	}
	if _, err := managed.run(context.Background(), "set-option", "-w", "-t", "code:managed-project", optionProjectID, "managed-project-id"); err != nil {
		t.Fatalf("tag managed project window: %v", err)
	}
	if _, err := managed.run(context.Background(), "select-window", "-t", "code:managed-project"); err != nil {
		t.Fatalf("select managed project window: %v", err)
	}

	startAttachedClient(t, script, executable, managed.socket, "external")
	startAttachedClient(t, script, executable, foreign.socket, "foreign")
	managedClients := waitForClientsAtPanes(t, managed, map[string]string{managedPane: "external"})
	foreignClients := waitForClientsAtPanes(t, foreign, map[string]string{foreignPane: "foreign"})
	managedClient := managedClients[managedPane].Name
	foreignClient := foreignClients[foreignPane].Name

	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(root, "foreign-result")
	helperCommand := strings.Join([]string{
		foreignHelperEnv + "=1",
		foreignSocketEnv + "=" + shellQuote(managed.socket),
		foreignResultEnv + "=" + shellQuote(resultPath),
		shellQuote(testExecutable),
		"-test.run=^TestIntegrationForeignServerCallerHelper$",
	}, " ")
	if _, err := foreign.run(context.Background(), "send-keys", "-l", "-t", foreignPane, helperCommand); err != nil {
		t.Fatalf("send foreign helper command: %v", err)
	}
	if _, err := foreign.run(context.Background(), "send-keys", "-t", foreignPane, "Enter"); err != nil {
		t.Fatalf("start foreign helper command: %v", err)
	}
	if result := waitForSwitchResult(t, resultPath); result != foreignHelperSuccess {
		t.Fatalf("foreign caller result = %q", result)
	}

	managedAfter := waitForNamedClientSessions(t, managed, map[string]string{managedClient: "external"})
	foreignAfter := waitForNamedClientSessions(t, foreign, map[string]string{foreignClient: "foreign"})
	if managedAfter[managedClient].ActivePane != managedPane {
		t.Fatalf("managed client moved to pane %q, want %q", managedAfter[managedClient].ActivePane, managedPane)
	}
	if foreignAfter[foreignClient].ActivePane != foreignPane {
		t.Fatalf("foreign client moved to pane %q, want %q", foreignAfter[foreignClient].ActivePane, foreignPane)
	}
	activeWindow, err := managed.run(context.Background(), "display-message", "-p", "-t", "code", "#{window_name}")
	if err != nil || strings.TrimSpace(activeWindow) != "managed-project" {
		t.Fatalf("managed active window = %q, %v; want managed-project", activeWindow, err)
	}
}

func TestIntegrationForeignServerCallerHelper(t *testing.T) {
	if os.Getenv(foreignHelperEnv) != "1" {
		t.Skip("integration helper process")
	}
	socket := os.Getenv(foreignSocketEnv)
	resultPath := os.Getenv(foreignResultEnv)
	manager, err := New(context.Background(), ManagerConfig{
		Session: "code", DashboardWindow: "op", Socket: socket, StartDirectory: filepath.Dir(socket),
		DashboardCommand: "sleep 300", EditorCommand: "sleep 300", PreferredShell: "sh",
		ShellPaneRows: 10, DefaultProfile: "integration",
	})
	if err == nil {
		checks := []func() error{
			func() error { _, _, callErr := manager.CurrentProjectID(context.Background()); return callErr },
			func() error { _, _, callErr := manager.CurrentProjectName(context.Background()); return callErr },
			func() error { return executeAttachOrSwitch(context.Background(), manager) },
		}
		for _, check := range checks {
			if callErr := check(); !domain.IsCode(callErr, domain.ErrorCodeConflict) {
				err = fmt.Errorf("caller-sensitive operation error = %v, want conflict", callErr)
				break
			}
		}
	}
	result := foreignHelperSuccess
	if err != nil {
		result = err.Error()
	}
	if writeErr := os.WriteFile(resultPath, []byte(result), 0o600); writeErr != nil {
		t.Fatalf("write helper result: %v", writeErr)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationCommandStartupRejectsMissingAndImmediateExit(t *testing.T) {
	requireTmuxIntegration(t)
	for _, command := range []string{"op-command-that-does-not-exist", "true"} {
		t.Run(command, func(t *testing.T) {
			executable, err := exec.LookPath("tmux")
			if err != nil {
				t.Skip("tmux is not installed")
			}
			root := t.TempDir()
			raw := rawTmux{executable: executable, socket: integrationSocket(t, executable)}
			manager, err := New(context.Background(), ManagerConfig{
				Session: "code", DashboardWindow: "op", Socket: raw.socket, StartDirectory: root,
				DashboardCommand: command, EditorCommand: "sleep 300", PreferredShell: "sh",
				ShellPaneRows: 10, DefaultProfile: "integration",
			})
			if err != nil {
				assertCode(t, err, domain.ErrorCodeDependency)
				return
			}
			manager.startupWait = 400 * time.Millisecond
			_, err = manager.EnsureMainSession(context.Background())
			assertCode(t, err, domain.ErrorCodeDependency)
		})
	}
}

func TestIntegrationDashboardUsesOccupiedBaseIndex(t *testing.T) {
	requireTmuxIntegration(t)
	executable, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	root := t.TempDir()
	raw := rawTmux{executable: executable, socket: integrationSocket(t, executable)}
	manager, err := New(context.Background(), ManagerConfig{
		Session: "code", DashboardWindow: "op", Socket: raw.socket, StartDirectory: root,
		DashboardCommand: "sleep 300", EditorCommand: "sleep 300", PreferredShell: "sh",
		ShellPaneRows: 10, DefaultProfile: "integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.run(context.Background(), "set-option", "-t", "code", "base-index", "1"); err != nil {
		t.Fatal(err)
	}
	if _, err := raw.run(context.Background(), "new-window", "-d", "-t", "code:1", "-n", "user"); err != nil {
		t.Fatal(err)
	}
	result, err := manager.EnsureMainSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	indices := make(map[string]int)
	for _, window := range result.Session.Windows {
		indices[window.Name] = window.Index
	}
	if indices["op"] != 1 || indices["user"] != 0 {
		t.Fatalf("window indices = %#v", indices)
	}
}

func TestIntegrationHostileProjectPathReturnsTypedError(t *testing.T) {
	if os.Getenv("OP_TMUX_INTEGRATION") != "1" {
		t.Skip("set OP_TMUX_INTEGRATION=1 to run isolated tmux integration tests")
	}
	executable, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}

	root := t.TempDir()
	socket := integrationSocket(t, executable)
	manager, err := New(context.Background(), ManagerConfig{
		Session: "code", DashboardWindow: "op", Socket: socket, StartDirectory: root,
		DashboardCommand: "sleep 300", EditorCommand: "sleep 300", PreferredShell: "sh",
		ShellPaneRows: 10, DefaultProfile: "integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.EnsureMainSession(context.Background()); err != nil {
		t.Fatal(err)
	}

	before, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: domain.Project{
		ID: "hostile", Name: "hostile", Path: filepath.Join(root, "we-:-ird"),
	}})
	assertCode(t, err, domain.ErrorCodeInvalidArgument)
	after, snapshotErr := manager.Snapshot(context.Background())
	if snapshotErr != nil {
		t.Fatal(snapshotErr)
	}
	if len(after.Session.Windows) != len(before.Session.Windows) {
		t.Fatalf("hostile path changed windows: before=%d after=%d", len(before.Session.Windows), len(after.Session.Windows))
	}
}

func waitForPaneCommand(t *testing.T, manager *Manager, windowID, command string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err := manager.Snapshot(context.Background())
		if err == nil && snapshot.Session != nil {
			for _, window := range snapshot.Session.Windows {
				if window.ID != windowID {
					continue
				}
				for _, pane := range window.Panes {
					if pane.CurrentCommand == command && !pane.Dead {
						return
					}
				}
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("window %s did not run %s within timeout", windowID, fmt.Sprintf("%q", command))
}

type integrationClientState struct {
	Name       string
	Session    string
	ActivePane string
}

type integrationIdentityClient struct {
	tmuxClient
	raw                 rawTmux
	concurrentDirectory string
	silentSplit         bool
	created             []string
	concurrent          []string
	killed              []string
}

type integrationMalformedIdentityClient struct {
	tmuxClient
	raw         rawTmux
	directory   string
	canonicalID string
	killed      []string
}

func (c *integrationMalformedIdentityClient) CreateWindow(ctx context.Context, session, _, _, _ string) (string, error) {
	output, err := c.raw.run(ctx, "new-window", "-d", "-P", "-F", "#{window_id}", "-t", session, "-n", "canonical-user", "-c", c.directory)
	if err != nil {
		return "", err
	}
	c.canonicalID, err = createdWindowID(output)
	if err != nil {
		return "", err
	}
	return "@0" + strings.TrimPrefix(c.canonicalID, "@"), nil
}

func (c *integrationMalformedIdentityClient) KillWindow(ctx context.Context, windowID string) error {
	c.killed = append(c.killed, windowID)
	return c.tmuxClient.KillWindow(ctx, windowID)
}

func (c *integrationIdentityClient) CreateWindow(ctx context.Context, session, name, directory, shellCommand string) (string, error) {
	windowID, err := c.tmuxClient.CreateWindow(ctx, session, name, directory, shellCommand)
	if err != nil {
		return "", err
	}
	c.created = append(c.created, windowID)
	userName := fmt.Sprintf("user-concurrent-%d", len(c.created))
	output, err := c.raw.run(ctx, "new-window", "-d", "-P", "-F", "#{window_id}", "-t", session, "-n", userName, "-c", c.concurrentDirectory)
	if err != nil {
		return "", err
	}
	userWindowID, err := createdWindowID(output)
	if err != nil {
		return "", err
	}
	c.concurrent = append(c.concurrent, userWindowID)
	return windowID, nil
}

func (c *integrationIdentityClient) KillWindow(ctx context.Context, windowID string) error {
	c.killed = append(c.killed, windowID)
	return c.tmuxClient.KillWindow(ctx, windowID)
}

func (c *integrationIdentityClient) SplitPane(ctx context.Context, paneID, directory, shellCommand string) error {
	if c.silentSplit {
		return nil
	}
	return c.tmuxClient.SplitPane(ctx, paneID, directory, shellCommand)
}

func integrationPaneID(t *testing.T, raw rawTmux, target string) string {
	t.Helper()
	paneID, err := raw.run(context.Background(), "display-message", "-p", "-t", target, "#{pane_id}")
	if err != nil {
		t.Fatalf("query pane %s: %v", target, err)
	}
	return strings.TrimSpace(paneID)
}

func startAttachedClient(t *testing.T, script, executable, socket, session string) {
	t.Helper()
	client := exec.Command(script, "-q", "-c", shellQuote(executable)+" -S "+shellQuote(socket)+" attach-session -t "+shellQuote(session), "/dev/null")
	client.Env = append(os.Environ(), "TMUX=", "TMUX_PANE=")
	client.Stdout = io.Discard
	client.Stderr = io.Discard
	clientInput, err := client.StdinPipe()
	if err != nil {
		t.Fatalf("open attached tmux client input: %v", err)
	}
	if err := client.Start(); err != nil {
		t.Fatalf("start attached tmux client: %v", err)
	}
	t.Cleanup(func() {
		_ = clientInput.Close()
		if client.Process != nil {
			_ = client.Process.Kill()
		}
		_ = client.Wait()
	})
}

func waitForClientsAtPanes(t *testing.T, raw rawTmux, want map[string]string) map[string]integrationClientState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last []integrationClientState
	for time.Now().Before(deadline) {
		clients, err := integrationClients(raw)
		if err == nil {
			last = clients
			byPane := make(map[string]integrationClientState, len(clients))
			for _, client := range clients {
				byPane[client.ActivePane] = client
			}
			matched := true
			for pane, session := range want {
				if byPane[pane].Session != session {
					matched = false
					break
				}
			}
			if matched {
				return byPane
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("clients = %+v, want pane sessions %+v", last, want)
	return nil
}

func waitForNamedClientSessions(t *testing.T, raw rawTmux, want map[string]string) map[string]integrationClientState {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var last []integrationClientState
	for time.Now().Before(deadline) {
		clients, err := integrationClients(raw)
		if err == nil {
			last = clients
			byName := make(map[string]integrationClientState, len(clients))
			for _, client := range clients {
				byName[client.Name] = client
			}
			matched := true
			for name, session := range want {
				if byName[name].Session != session {
					matched = false
					break
				}
			}
			if matched {
				return byName
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("clients = %+v, want named sessions %+v", last, want)
	return nil
}

func integrationClients(raw rawTmux) ([]integrationClientState, error) {
	output, err := raw.run(context.Background(), "list-clients", "-F", "#{client_name}\t#{client_session}\t#{pane_id}")
	if err != nil {
		return nil, err
	}
	output = strings.TrimSpace(output)
	if output == "" {
		return nil, nil
	}
	lines := strings.Split(output, "\n")
	clients := make([]integrationClientState, 0, len(lines))
	for _, line := range lines {
		fields := strings.Split(strings.TrimSuffix(line, "\r"), "\t")
		if len(fields) != 3 {
			return nil, fmt.Errorf("unexpected list-clients output")
		}
		clients = append(clients, integrationClientState{Name: fields[0], Session: fields[1], ActivePane: fields[2]})
	}
	return clients, nil
}

func waitForSwitchResult(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		result, err := os.ReadFile(path)
		if err == nil {
			return string(result)
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("switch helper did not write %s", path)
	return ""
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func newDashboardIntegration(t *testing.T) (*Manager, rawTmux, domain.TmuxWindow) {
	t.Helper()
	return newDashboardIntegrationCommand(t, "sleep 300")
}

func newDashboardIntegrationCommand(t *testing.T, dashboardCommand string) (*Manager, rawTmux, domain.TmuxWindow) {
	t.Helper()
	requireTmuxIntegration(t)
	executable, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	root := t.TempDir()
	raw := rawTmux{executable: executable, socket: integrationSocket(t, executable)}
	manager, err := New(context.Background(), ManagerConfig{
		Session: "code", DashboardWindow: "op", Socket: raw.socket, StartDirectory: root,
		DashboardCommand: dashboardCommand, EditorCommand: "sleep 300", PreferredShell: "sh",
		ShellPaneRows: 10, DefaultProfile: "integration",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.EnsureMainSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Session.Windows) != 1 || len(result.Session.Windows[0].Panes) != 1 {
		t.Fatalf("initial dashboard = %+v", result)
	}
	return manager, raw, result.Session.Windows[0]
}

func otherPaneID(t *testing.T, panes []paneState, excluded string) string {
	t.Helper()
	for _, pane := range panes {
		if pane.ID != excluded {
			return pane.ID
		}
	}
	t.Fatalf("no pane other than %s in %+v", excluded, panes)
	return ""
}

func findSnapshotPane(result domain.EnsureMainSessionResult, windowID, paneID string) *domain.TmuxPane {
	for _, window := range result.Session.Windows {
		if window.ID != windowID {
			continue
		}
		for i := range window.Panes {
			if window.Panes[i].ID == paneID {
				return &window.Panes[i]
			}
		}
	}
	return nil
}

func waitForPaneDead(t *testing.T, manager *Manager, windowID, paneID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		pane, err := manager.paneByID(context.Background(), windowID, paneID)
		if err == nil && pane != nil && pane.Dead {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("pane %s did not become dead within timeout", paneID)
}

func requireTmuxIntegration(t *testing.T) {
	t.Helper()
	if os.Getenv("OP_TMUX_INTEGRATION") != "1" {
		t.Skip("set OP_TMUX_INTEGRATION=1 to run isolated tmux integration tests")
	}
}
