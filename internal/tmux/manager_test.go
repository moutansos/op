package tmux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moutansos/op/internal/domain"
)

func TestEnsureMainSessionCreatesAndVerifiesDashboard(t *testing.T) {
	fake := newFakeClient()
	manager := testManager(fake)

	result, err := manager.EnsureMainSession(context.Background())
	if err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	if !result.Created || !result.Repaired {
		t.Fatalf("EnsureMainSession() = %+v, want created and repaired", result)
	}
	window := fake.onlyWindow(t)
	if window.Name != "op" || window.Index != 0 {
		t.Fatalf("dashboard window = %+v", window)
	}
	if got := fake.options[window.ID][optionRole]; got != roleDashboard {
		t.Fatalf("dashboard role = %q", got)
	}
	paneID := fake.panes[window.ID][0].ID
	if got := fake.panes[window.ID][0].CurrentCommand; got != "op" {
		t.Fatalf("dashboard command = %q", got)
	}
	if got := fake.options[window.ID][optionDashboardPane]; got != paneID {
		t.Fatalf("managed dashboard pane = %q, want %q", got, paneID)
	}
	if got := fake.options[window.ID][optionDashboardPID]; got != strconv.FormatInt(int64(fake.panes[window.ID][0].PID), 10) {
		t.Fatalf("managed dashboard pid = %q", got)
	}
	if len(result.Session.Windows) != 1 || result.Session.Windows[0].Panes[0].Dead {
		t.Fatalf("session snapshot = %+v", result.Session)
	}
	if got := fake.serverOptions["focus-events"]; got != "on" {
		t.Fatalf("focus-events = %q, want on", got)
	}
}

func TestEnsureMainSessionRejectsUnobservedFocusEventsMutation(t *testing.T) {
	fake := newFakeClient()
	fake.silent["set-server-option"] = true

	_, err := testManager(fake).EnsureMainSession(context.Background())
	assertCode(t, err, domain.ErrorCodeDependency)
}

func TestEnsureMainSessionRejectsSilentSessionCreation(t *testing.T) {
	fake := newFakeClient()
	fake.silent["create-session"] = true
	_, err := testManager(fake).EnsureMainSession(context.Background())
	assertCode(t, err, domain.ErrorCodeDependency)
	if len(fake.windows) != 0 {
		t.Fatalf("silent creation unexpectedly changed state")
	}
}

func TestEnsureMainSessionRejectsUnobservedDirectDashboardStart(t *testing.T) {
	fake := newFakeClient()
	fake.silent["start-command"] = true

	_, err := testManager(fake).EnsureMainSession(context.Background())
	assertCode(t, err, domain.ErrorCodeDependency)
	if len(fake.windows) != 0 {
		t.Fatalf("failed dashboard startup was not rolled back: %#v", fake.windows)
	}
}

func TestEnsureMainSessionRejectsWrongForegroundCommand(t *testing.T) {
	fake := newFakeClient()
	fake.startupCommandOverride = "python"

	_, err := testManager(fake).EnsureMainSession(context.Background())
	assertCode(t, err, domain.ErrorCodeDependency)
	if len(fake.windows) != 0 {
		t.Fatalf("failed dashboard startup was not rolled back: %#v", fake.windows)
	}
}

func TestEnsureMainSessionFailedStartupDoesNotPoisonRetry(t *testing.T) {
	for _, failure := range []string{"silent", "wrong-command"} {
		t.Run(failure, func(t *testing.T) {
			fake := newFakeClient()
			if failure == "silent" {
				fake.silent["start-command"] = true
			} else {
				fake.startupCommandOverride = "python"
			}
			manager := testManager(fake)
			_, err := manager.EnsureMainSession(context.Background())
			assertCode(t, err, domain.ErrorCodeDependency)
			if len(fake.windows) != 0 {
				t.Fatalf("failed startup left dashboard state: windows=%#v options=%#v", fake.windows, fake.options)
			}

			fake.silent["start-command"] = false
			fake.startupCommandOverride = ""
			result, err := manager.EnsureMainSession(context.Background())
			if err != nil {
				t.Fatalf("retry error = %v", err)
			}
			dashboard := fake.windowNamed("op")
			pane := fake.panes[dashboard.ID][0]
			if !result.Repaired || fake.options[dashboard.ID][optionDashboardPane] != pane.ID || fake.options[dashboard.ID][optionDashboardPID] != strconv.FormatInt(int64(pane.PID), 10) {
				t.Fatalf("retry result=%+v options=%#v pane=%+v", result, fake.options[dashboard.ID], pane)
			}
		})
	}
}

func TestEnsureMainSessionDoesNotInjectIntoUntaggedNamedWindow(t *testing.T) {
	fake := newFakeClient()
	fake.addSession("code")
	user := fake.addWindow("user-op", 0, "op")
	fake.panes[user.ID][0].CurrentCommand = "python"

	manager := testManager(fake)
	manager.dashboardProcessAlive = func(context.Context, int, string) (bool, error) { return false, nil }
	_, err := manager.EnsureMainSession(context.Background())
	assertCode(t, err, domain.ErrorCodeDependency)
	if len(fake.windows) != 1 || fake.options[user.ID][optionRole] != "" || fake.panes[user.ID][0].CurrentCommand != "python" {
		t.Fatalf("ambiguous named window was mutated: windows=%#v options=%#v panes=%#v", fake.windows, fake.options, fake.panes)
	}
}

func TestEnsureMainSessionAdoptsRunningNamedDashboardWithoutDuplicate(t *testing.T) {
	fake := newFakeClient()
	fake.addSession("code")
	dashboard := fake.addWindow("legacy-op", 0, "op")
	pane := fake.panes[dashboard.ID][0]
	pane.CurrentCommand = "bash"
	originalPID := pane.PID
	manager := testManager(fake)
	manager.dashboardProcessAlive = func(_ context.Context, rootPID int, command string) (bool, error) {
		return rootPID == int(originalPID) && command == "op dashboard", nil
	}

	result, err := manager.EnsureMainSession(context.Background())
	if err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	if len(result.Session.Windows) != 1 || fake.options[dashboard.ID][optionRole] != roleDashboard {
		t.Fatalf("running dashboard was not adopted: result=%+v options=%#v", result, fake.options)
	}
	if pane.PID != originalPID || pane.CurrentCommand != "bash" {
		t.Fatalf("running wrapped dashboard was restarted: pane=%+v, original PID=%d", pane, originalPID)
	}
	if fake.options[dashboard.ID][optionDashboardPane] != pane.ID || fake.options[dashboard.ID][optionDashboardPID] != strconv.FormatInt(int64(pane.PID), 10) {
		t.Fatalf("adopted identity = %#v, pane=%+v", fake.options[dashboard.ID], pane)
	}
}

func TestEnsureMainSessionStartsDashboardInSoleUntrackedCallerPane(t *testing.T) {
	fake := newFakeClient()
	fake.addSession("code")
	dashboard := fake.addWindow("legacy-op", 0, "op")
	pane := fake.panes[dashboard.ID][0]
	originalPID := pane.PID
	manager := testManager(fake)
	manager.dashboardProcessAlive = func(context.Context, int, string) (bool, error) { return false, nil }
	manager.lookupEnv = callerEnvironment("/tmp/tmux/default,123,0", pane.ID)

	result, err := manager.EnsureMainSession(context.Background())
	if err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	if !result.Repaired || result.Created || !result.StartDashboard || pane.PID != originalPID || pane.CurrentCommand != "sh" {
		t.Fatalf("untracked caller pane adoption = %+v; pane=%+v, original PID=%d", result, pane, originalPID)
	}
	if fake.options[dashboard.ID][optionDashboardPane] != pane.ID || fake.options[dashboard.ID][optionDashboardPID] != strconv.FormatInt(int64(pane.PID), 10) {
		t.Fatalf("restarted identity = %#v, pane=%+v", fake.options[dashboard.ID], pane)
	}
}

func TestEnsureMainSessionStartsDashboardInTrackedIdleCallerPane(t *testing.T) {
	fake := managedFake()
	dashboard := fake.windowNamed("op")
	pane := fake.panes[dashboard.ID][0]
	originalPID := pane.PID
	manager := testManager(fake)
	manager.dashboardProcessAlive = func(context.Context, int, string) (bool, error) { return false, nil }
	manager.lookupEnv = callerEnvironment("/tmp/tmux/default,123,0", pane.ID)

	result, err := manager.EnsureMainSession(context.Background())
	if err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	if !result.StartDashboard || !result.Repaired || pane.PID != originalPID || pane.CurrentCommand != "sh" {
		t.Fatalf("tracked caller pane restart = %+v; pane=%+v, original PID=%d", result, pane, originalPID)
	}
}

func TestEnsureMainSessionDoesNotChooseCallerFromAmbiguousUntrackedDashboardPanes(t *testing.T) {
	fake := newFakeClient()
	fake.addSession("code")
	dashboard := fake.addWindow("legacy-op", 0, "op")
	first := fake.panes[dashboard.ID][0]
	if err := fake.SplitPane(context.Background(), first.ID, "/repo", "zsh"); err != nil {
		t.Fatal(err)
	}
	manager := testManager(fake)
	manager.dashboardProcessAlive = func(context.Context, int, string) (bool, error) { return false, nil }
	manager.lookupEnv = callerEnvironment("/tmp/tmux/default,123,0", first.ID)

	_, err := manager.EnsureMainSession(context.Background())
	assertCode(t, err, domain.ErrorCodeDependency)
	if fake.options[dashboard.ID][optionDashboardPane] != "" || first.CurrentCommand != "sh" {
		t.Fatalf("ambiguous caller pane was adopted: options=%#v panes=%#v", fake.options[dashboard.ID], fake.panes[dashboard.ID])
	}
}

func TestEnsureMainSessionRejectsAmbiguousOwnedNamedDashboard(t *testing.T) {
	fake := newFakeClient()
	fake.addSession("code")
	dashboard := fake.addWindow("owned-op", 0, "op")
	fake.options[dashboard.ID][optionOwner] = "1"
	fake.panes[dashboard.ID][0].CurrentCommand = "python"

	manager := testManager(fake)
	manager.dashboardProcessAlive = func(context.Context, int, string) (bool, error) { return false, nil }
	_, err := manager.EnsureMainSession(context.Background())
	assertCode(t, err, domain.ErrorCodeDependency)
	if fake.options[dashboard.ID][optionRole] != "" || fake.options[dashboard.ID][optionDashboardPane] != "" || fake.panes[dashboard.ID][0].CurrentCommand != "python" {
		t.Fatalf("ambiguous owned dashboard was mutated: options=%#v panes=%#v", fake.options, fake.panes)
	}
}

func TestEnsureMainSessionRejectsAmbiguousRoleDashboard(t *testing.T) {
	fake := newFakeClient()
	fake.addSession("code")
	dashboard := fake.addWindow("role-op", 0, "op")
	fake.options[dashboard.ID][optionRole] = roleDashboard
	fake.options[dashboard.ID][optionOwner] = "1"
	fake.panes[dashboard.ID][0].CurrentCommand = "nvim"

	manager := testManager(fake)
	manager.dashboardProcessAlive = func(context.Context, int, string) (bool, error) { return false, nil }
	_, err := manager.EnsureMainSession(context.Background())
	assertCode(t, err, domain.ErrorCodeDependency)
	if fake.options[dashboard.ID][optionDashboardPane] != "" || fake.options[dashboard.ID][optionDashboardPID] != "" || fake.panes[dashboard.ID][0].CurrentCommand != "nvim" {
		t.Fatalf("ambiguous role dashboard was mutated: options=%#v panes=%#v", fake.options, fake.panes)
	}
}

func TestEnsureMainSessionRepairsRespawnedUnrelatedProcessWithSamePaneID(t *testing.T) {
	fake := managedFake()
	dashboard := fake.windowNamed("op")
	managed := fake.panes[dashboard.ID][0]
	originalPID := managed.PID
	if err := fake.RespawnPane(context.Background(), managed.ID, ""); err != nil {
		t.Fatal(err)
	}
	replacementPID := managed.PID
	managed.CurrentCommand = "python"

	result, err := testManager(fake).EnsureMainSession(context.Background())
	if err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	if !result.Repaired || managed.PID == replacementPID || managed.PID == originalPID || managed.CurrentCommand != "op" {
		t.Fatalf("replacement was trusted: result=%+v pane=%+v", result, managed)
	}
	if managed.CurrentCommand != "op" {
		t.Fatalf("dashboard startup command = %q", managed.CurrentCommand)
	}
	if got := fake.options[dashboard.ID][optionDashboardPID]; got != strconv.FormatInt(int64(managed.PID), 10) {
		t.Fatalf("managed pid = %q, want %d", got, managed.PID)
	}
}

func TestEnsureMainSessionRejectsSilentRespawnOfLiveUnrelatedProcess(t *testing.T) {
	fake := managedFake()
	dashboard := fake.windowNamed("op")
	managed := fake.panes[dashboard.ID][0]
	trackedPID := fake.options[dashboard.ID][optionDashboardPID]
	if err := fake.RespawnPane(context.Background(), managed.ID, ""); err != nil {
		t.Fatal(err)
	}
	replacementPID := managed.PID
	managed.CurrentCommand = "python"
	fake.silent["respawn-pane"] = true

	_, err := testManager(fake).EnsureMainSession(context.Background())
	assertCode(t, err, domain.ErrorCodeDependency)
	if managed.PID != replacementPID || managed.CurrentCommand != "python" {
		t.Fatalf("silent respawn changed unrelated process: %+v", managed)
	}
	if got := fake.options[dashboard.ID][optionDashboardPID]; got != trackedPID {
		t.Fatalf("tracking changed from %q to %q", trackedPID, got)
	}
}

func TestEnsureMainSessionPreservesManagedDashboardRunningForegroundChild(t *testing.T) {
	fake := managedFake()
	dashboard := fake.windowNamed("op")
	managed := fake.panes[dashboard.ID][0]
	managed.CurrentCommand = "nvim"

	result, err := testManager(fake).EnsureMainSession(context.Background())
	if err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	if result.Repaired || fake.panes[dashboard.ID][0] != managed {
		t.Fatalf("foreground child was interrupted: result=%+v panes=%#v", result, fake.panes[dashboard.ID])
	}
}

func TestEnsureMainSessionPreservesExtraDashboardPanes(t *testing.T) {
	fake := managedFake()
	dashboard := fake.windowNamed("op")
	managed := fake.panes[dashboard.ID][0]
	if err := fake.SplitPane(context.Background(), managed.ID, "/user", "zsh"); err != nil {
		t.Fatal(err)
	}
	userPane := fake.panes[dashboard.ID][1]

	result, err := testManager(fake).EnsureMainSession(context.Background())
	if err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	if result.Repaired || fake.windows[dashboard.ID] == nil || len(fake.panes[dashboard.ID]) != 2 {
		t.Fatalf("extra pane changed dashboard: result=%+v windows=%#v panes=%#v", result, fake.windows, fake.panes[dashboard.ID])
	}
	if _, pane, err := fake.findPane(userPane.ID); err != nil || pane != userPane {
		t.Fatalf("user pane %s was not preserved", userPane.ID)
	}
}

func TestEnsureMainSessionRepairsDeadManagedPaneWithoutDeletingExtraPanes(t *testing.T) {
	fake := managedFake()
	dashboard := fake.windowNamed("op")
	managed := fake.panes[dashboard.ID][0]
	if err := fake.SplitPane(context.Background(), managed.ID, "/user", "zsh"); err != nil {
		t.Fatal(err)
	}
	userPane := fake.panes[dashboard.ID][1]
	managed.Dead = true

	result, err := testManager(fake).EnsureMainSession(context.Background())
	if err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	if !result.Repaired || managed.Dead || len(fake.panes[dashboard.ID]) != 2 {
		t.Fatalf("dead pane repair = %+v, panes=%#v", result, fake.panes[dashboard.ID])
	}
	if _, _, err := fake.findPane(userPane.ID); err != nil {
		t.Fatalf("user pane %s was deleted", userPane.ID)
	}
	if managed.CurrentCommand != "op" {
		t.Fatalf("respawn command = %q", managed.CurrentCommand)
	}
}

func TestEnsureMainSessionRecreatesMissingManagedPaneWithoutDeletingExtraPanes(t *testing.T) {
	fake := managedFake()
	dashboard := fake.windowNamed("op")
	managed := fake.panes[dashboard.ID][0]
	if err := fake.SplitPane(context.Background(), managed.ID, "/user", "zsh"); err != nil {
		t.Fatal(err)
	}
	userPane := fake.panes[dashboard.ID][1]
	fake.panes[dashboard.ID] = []*paneState{userPane}

	result, err := testManager(fake).EnsureMainSession(context.Background())
	if err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	newManagedID := fake.options[dashboard.ID][optionDashboardPane]
	if !result.Repaired || newManagedID == managed.ID || len(fake.panes[dashboard.ID]) != 2 {
		t.Fatalf("missing pane repair = %+v, option=%q panes=%#v", result, newManagedID, fake.panes[dashboard.ID])
	}
	if _, _, err := fake.findPane(userPane.ID); err != nil {
		t.Fatalf("user pane %s was deleted", userPane.ID)
	}
	if got := fake.panes[dashboard.ID][1].CurrentCommand; got != "op" {
		t.Fatalf("recreated pane command = %q", got)
	}
	newManaged := fake.panes[dashboard.ID][1]
	if got := fake.options[dashboard.ID][optionDashboardPID]; got != strconv.FormatInt(int64(newManaged.PID), 10) {
		t.Fatalf("recreated pane pid = %q, want %d", got, newManaged.PID)
	}
}

func TestEnsureMainSessionFailedSplitStartupDoesNotAccumulatePanes(t *testing.T) {
	for _, failure := range []string{"startup", "tracking"} {
		t.Run(failure, func(t *testing.T) {
			fake := managedFake()
			dashboard := fake.windowNamed("op")
			managed := fake.panes[dashboard.ID][0]
			if err := fake.SplitPane(context.Background(), managed.ID, "/user", "zsh"); err != nil {
				t.Fatal(err)
			}
			userPane := fake.panes[dashboard.ID][1]
			fake.panes[dashboard.ID] = []*paneState{userPane}
			if failure == "startup" {
				fake.startupCommandOverride = "python"
			} else {
				fake.silent["set-option"] = true
			}
			originalID := fake.options[dashboard.ID][optionDashboardPane]
			originalPID := fake.options[dashboard.ID][optionDashboardPID]

			for attempt := 0; attempt < 2; attempt++ {
				_, err := testManager(fake).EnsureMainSession(context.Background())
				assertCode(t, err, domain.ErrorCodeDependency)
				if len(fake.panes[dashboard.ID]) != 1 || fake.panes[dashboard.ID][0].ID != userPane.ID {
					t.Fatalf("attempt %d accumulated or removed panes: %#v", attempt+1, fake.panes[dashboard.ID])
				}
				if fake.options[dashboard.ID][optionDashboardPane] != originalID || fake.options[dashboard.ID][optionDashboardPID] != originalPID {
					t.Fatalf("attempt %d poisoned tracking: %#v", attempt+1, fake.options[dashboard.ID])
				}
			}
		})
	}
}

func TestEnsureMainSessionRecoversMissingSoleDashboardWindow(t *testing.T) {
	fake := managedFake()
	dashboard := fake.windowNamed("op")
	user := fake.addWindow("user", 1, "user")
	delete(fake.windows, dashboard.ID)
	delete(fake.panes, dashboard.ID)
	delete(fake.options, dashboard.ID)

	result, err := testManager(fake).EnsureMainSession(context.Background())
	if err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	replacement := fake.windowNamed("op")
	if !result.Repaired || replacement == nil || len(fake.panes[replacement.ID]) != 1 || fake.windows[user.ID] == nil {
		t.Fatalf("sole dashboard recovery = %+v, windows=%#v panes=%#v", result, fake.windows, fake.panes)
	}
}

func TestOpenProjectWindowRejectsSilentWindowCreation(t *testing.T) {
	fake := managedFake()
	fake.silent["create-window"] = true
	project := domain.Project{ID: "project", Name: "project", Path: "/repos/project"}
	_, err := testManager(fake).OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
	assertCode(t, err, domain.ErrorCodeDependency)
	if !strings.Contains(err.Error(), "valid window identity") {
		t.Fatalf("silent creation error = %v, want identity verification failure", err)
	}
	if len(fake.windows) != 1 {
		t.Fatalf("silent window creation changed state: %#v", fake.windows)
	}
}

func TestOpenProjectWindowMalformedIdentityCannotRollbackCanonicalUserWindow(t *testing.T) {
	for _, test := range []struct {
		returnedID  string
		canonicalID string
	}{
		{returnedID: "@00", canonicalID: "@0"},
		{returnedID: "@01", canonicalID: "@1"},
	} {
		t.Run(test.returnedID, func(t *testing.T) {
			fake := managedFake()
			user := fake.addWindow(strings.TrimPrefix(test.canonicalID, "@"), 1, "canonical-user")
			fake.createWindowResult = test.returnedID
			project := domain.Project{ID: "project", Name: "project", Path: "/repos/project"}

			_, err := testManager(fake).OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
			assertCode(t, err, domain.ErrorCodeDependency)
			if !strings.Contains(err.Error(), "valid window identity") {
				t.Fatalf("malformed identity error = %v", err)
			}
			if len(fake.killedWindowIDs) != 0 {
				t.Fatalf("malformed identity invoked kill-window for %#v", fake.killedWindowIDs)
			}
			if fake.windows[test.canonicalID] != user {
				t.Fatalf("canonical user window %q was removed: %#v", test.canonicalID, fake.windows)
			}
		})
	}
}

func TestOpenProjectWindowVerifiesReturnedWindowState(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*fakeClient, *windowState)
	}{
		{name: "expected session", mutate: func(fake *fakeClient, window *windowState) {
			delete(fake.windows, window.ID)
		}},
		{name: "name", mutate: func(_ *fakeClient, window *windowState) {
			window.Name = "wrong"
		}},
		{name: "path", mutate: func(fake *fakeClient, window *windowState) {
			fake.panes[window.ID][0].CurrentPath = "/wrong"
		}},
		{name: "live pane", mutate: func(fake *fakeClient, window *windowState) {
			fake.panes[window.ID][0].Dead = true
		}},
		{name: "positive pane PID", mutate: func(fake *fakeClient, window *windowState) {
			fake.panes[window.ID][0].PID = 0
		}},
		{name: "one initial pane", mutate: func(fake *fakeClient, window *windowState) {
			fake.panes[window.ID] = append(fake.panes[window.ID], &paneState{ID: "%999", PID: 1999, CurrentPath: "/repos/project"})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := managedFake()
			fake.createdWindowHook = test.mutate
			project := domain.Project{ID: "project", Name: "project", Path: "/repos/project"}

			_, err := testManager(fake).OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
			assertCode(t, err, domain.ErrorCodeDependency)
			if len(fake.killedWindowIDs) != 1 || fake.killedWindowIDs[0] != fake.lastCreatedWindowID {
				t.Fatalf("rollback targets = %#v, want only %q", fake.killedWindowIDs, fake.lastCreatedWindowID)
			}
			if fake.windowNamed("op") == nil {
				t.Fatal("verification rollback removed the dashboard")
			}
		})
	}
}

func TestOpenProjectWindowUsesReturnedIdentityDuringConcurrentUserCreation(t *testing.T) {
	fake := managedFake()
	fake.concurrentWindowOnCreate = true
	project := domain.Project{ID: "project", Name: "project", Path: "/repos/project"}

	result, err := testManager(fake).OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
	if err != nil {
		t.Fatalf("OpenProjectWindow() error = %v", err)
	}
	if result.Window.ID != fake.lastCreatedWindowID || result.Window.ID == fake.concurrentWindowID {
		t.Fatalf("result window = %q, created = %q, concurrent = %q", result.Window.ID, fake.lastCreatedWindowID, fake.concurrentWindowID)
	}
	if fake.windows[fake.concurrentWindowID] == nil || fake.options[fake.concurrentWindowID][optionProjectID] != "" {
		t.Fatalf("concurrent user window was claimed: window=%+v options=%#v", fake.windows[fake.concurrentWindowID], fake.options[fake.concurrentWindowID])
	}
}

func TestOpenProjectWindowRollsBackReturnedIdentityNotConcurrentUserWindow(t *testing.T) {
	fake := managedFake()
	fake.concurrentWindowOnCreate = true
	fake.silent["split-pane"] = true
	project := domain.Project{ID: "project", Name: "project", Path: "/repos/project"}

	_, err := testManager(fake).OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
	assertCode(t, err, domain.ErrorCodeDependency)
	if fake.windows[fake.lastCreatedWindowID] != nil {
		t.Fatalf("created window %q survived rollback", fake.lastCreatedWindowID)
	}
	if fake.windows[fake.concurrentWindowID] == nil {
		t.Fatalf("concurrent user window %q was killed", fake.concurrentWindowID)
	}
	if len(fake.killedWindowIDs) != 1 || fake.killedWindowIDs[0] != fake.lastCreatedWindowID {
		t.Fatalf("killed windows = %#v, want only %q", fake.killedWindowIDs, fake.lastCreatedWindowID)
	}
}

func TestEnsureMainSessionRollsBackReturnedIdentityNotConcurrentUserWindow(t *testing.T) {
	fake := newFakeClient()
	fake.addSession("code")
	existing := fake.addWindow("existing", 0, "user")
	fake.concurrentWindowOnCreate = true
	fake.silent["start-command"] = true

	_, err := testManager(fake).EnsureMainSession(context.Background())
	assertCode(t, err, domain.ErrorCodeDependency)
	if fake.windows[fake.lastCreatedWindowID] != nil {
		t.Fatalf("created dashboard %q survived rollback", fake.lastCreatedWindowID)
	}
	if fake.windows[existing.ID] == nil || fake.windows[fake.concurrentWindowID] == nil {
		t.Fatalf("rollback removed user windows: %#v", fake.windows)
	}
	if len(fake.killedWindowIDs) != 1 || fake.killedWindowIDs[0] != fake.lastCreatedWindowID {
		t.Fatalf("killed windows = %#v, want only %q", fake.killedWindowIDs, fake.lastCreatedWindowID)
	}
}

func TestEnsureMainSessionReconcilesDeadPaneAndBaseIndexConflict(t *testing.T) {
	fake := newFakeClient()
	fake.addSession("code")
	user := fake.addWindow("user", 1, "user")
	dashboard := fake.addWindow("op", 2, "op")
	fake.options[dashboard.ID][optionRole] = roleDashboard + "\n"
	fake.options[dashboard.ID][optionOwner] = "1"
	fake.options[dashboard.ID][optionDashboardPane] = fake.panes[dashboard.ID][0].ID
	fake.options[dashboard.ID][optionDashboardPID] = strconv.FormatInt(int64(fake.panes[dashboard.ID][0].PID), 10)
	fake.panes[dashboard.ID][0].Dead = true
	fake.sessionOptions["base-index"] = "1\n"

	result, err := testManager(fake).EnsureMainSession(context.Background())
	if err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	if !result.Repaired || result.Created {
		t.Fatalf("EnsureMainSession() = %+v", result)
	}
	if fake.windows[dashboard.ID].Index != 1 || fake.windows[user.ID].Index != 2 {
		t.Fatalf("base-index swap not verified: dashboard=%d user=%d", fake.windows[dashboard.ID].Index, fake.windows[user.ID].Index)
	}
	if fake.panes[dashboard.ID][0].Dead {
		t.Fatal("dashboard pane remained dead")
	}
	if got := fake.panes[dashboard.ID][0].CurrentCommand; got != "op" {
		t.Fatalf("respawn command = %q", got)
	}
}

func TestEnsureMainSessionRejectsSilentRespawn(t *testing.T) {
	fake := managedFake()
	dashboard := fake.windowNamed("op")
	fake.panes[dashboard.ID][0].Dead = true
	fake.silent["respawn-pane"] = true

	_, err := testManager(fake).EnsureMainSession(context.Background())
	assertCode(t, err, domain.ErrorCodeDependency)
}

func TestEnsureMainSessionRestartsDashboardInIdleManagedShell(t *testing.T) {
	fake := managedFake()
	dashboard := fake.windowNamed("op")
	pane := fake.panes[dashboard.ID][0]
	originalPID := pane.PID
	pane.CurrentCommand = "zsh"
	manager := testManager(fake)
	manager.dashboardProcessAlive = func(context.Context, int, string) (bool, error) { return false, nil }

	result, err := manager.EnsureMainSession(context.Background())
	if err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	if !result.Repaired || pane.PID == originalPID || pane.CurrentCommand != "op" {
		t.Fatalf("idle dashboard repair = %+v; pane = %+v", result, pane)
	}
	if got := fake.options[dashboard.ID][optionDashboardPID]; got != strconv.FormatInt(int64(pane.PID), 10) {
		t.Fatalf("managed dashboard pid = %q, want %d", got, pane.PID)
	}
}

func TestEnsureMainSessionDoesNotReplacePaneWhenProcessInspectionFails(t *testing.T) {
	fake := managedFake()
	dashboard := fake.windowNamed("op")
	pane := fake.panes[dashboard.ID][0]
	originalPID := pane.PID
	manager := testManager(fake)
	manager.dashboardProcessAlive = func(context.Context, int, string) (bool, error) {
		return false, errors.New("process inspection unavailable")
	}

	_, err := manager.EnsureMainSession(context.Background())
	assertCode(t, err, domain.ErrorCodeDependency)
	if pane.PID != originalPID {
		t.Fatalf("dashboard pane was replaced after uncertain inspection: %+v", pane)
	}
}

func TestOpenProjectWindowBuildsLayoutReusesAndCreatesInstances(t *testing.T) {
	fake := managedFake()
	manager := testManager(fake)
	project := domain.Project{ID: "project-1", Name: "repo.one", Path: "/repos/repo.one", Kind: domain.ProjectKindRepository}

	first, err := manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{
		Project: project, ShellCommand: "bash --noprofile",
	})
	if err != nil {
		t.Fatalf("OpenProjectWindow() error = %v", err)
	}
	if first.Reused || first.Window.Name != "repo-one" || len(first.Window.Panes) != 2 {
		t.Fatalf("first result = %+v", first)
	}
	window := fake.windows[first.Window.ID]
	panes := fake.panes[window.ID]
	if panes[0].CurrentPath != project.Path || panes[1].CurrentPath != project.Path {
		t.Fatalf("pane paths = %+v", panes)
	}
	if panes[1].Height != 20 || !panes[0].Active || panes[1].Active {
		t.Fatalf("pane layout = %+v", panes)
	}
	if got := fake.splitShell[panes[1].ID]; got != "bash --noprofile" {
		t.Fatalf("multiword shell command = %q", got)
	}
	if panes[1].CurrentCommand != "bash" {
		t.Fatalf("shell command = %q", panes[1].CurrentCommand)
	}
	createdWords := shellWords(fake.createdWindowShell)
	if len(createdWords) != 4 || createdWords[0] != "exec" || createdWords[1] != "zsh" || createdWords[2] != "-ic" || createdWords[3] != "nvim .; exec 'zsh'" {
		t.Fatalf("editor pane command words = %#v from %q", createdWords, fake.createdWindowShell)
	}
	if fake.options[window.ID][optionProjectID] != project.ID || fake.options[window.ID][optionPath] != project.Path || fake.options[window.ID][optionOwner] != "1" {
		t.Fatalf("project tags = %#v", fake.options[window.ID])
	}

	reused, err := manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
	if err != nil || !reused.Reused || reused.Window.ID != first.Window.ID {
		t.Fatalf("reuse = %+v, err = %v", reused, err)
	}
	instance, err := manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project, NewInstance: true})
	if err != nil {
		t.Fatalf("new instance error = %v", err)
	}
	if instance.Reused || instance.Window.Name != "repo-one-2" {
		t.Fatalf("new instance = %+v", instance)
	}
}

func TestSelectPaneSelectsWindowAndPane(t *testing.T) {
	fake := managedFake()
	project := fake.addWindow("project", 1, "notifier")
	first := fake.panes[project.ID][0]
	fake.nextPane++
	second := &paneState{
		ID: fmt.Sprintf("%%%d", fake.nextPane), Index: 1, PID: int32(1000 + fake.nextPane),
		CurrentCommand: "claude", CurrentPath: "/repo", Height: 20,
	}
	fake.panes[project.ID] = append(fake.panes[project.ID], second)

	window, pane, err := testManager(fake).SelectPane(context.Background(), second.ID)
	if err != nil {
		t.Fatalf("SelectPane() error = %v", err)
	}
	if !project.Active {
		t.Fatal("project window was not selected")
	}
	if first.Active || !second.Active {
		t.Fatalf("pane active flags = first=%v second=%v", first.Active, second.Active)
	}
	if pane.ID != second.ID || !pane.Active || window.ID != project.ID {
		t.Fatalf("result window=%+v pane=%+v", window, pane)
	}
}

func TestSelectPaneRejectsInvalidMissingAndDeadPanes(t *testing.T) {
	fake := managedFake()
	window := fake.addWindow("project", 1, "notifier")
	fake.panes[window.ID][0].Dead = true
	deadID := fake.panes[window.ID][0].ID

	_, _, err := testManager(fake).SelectPane(context.Background(), "")
	assertCode(t, err, domain.ErrorCodeInvalidArgument)
	_, _, err = testManager(fake).SelectPane(context.Background(), "not-a-pane")
	assertCode(t, err, domain.ErrorCodeInvalidArgument)
	_, _, err = testManager(fake).SelectPane(context.Background(), "%999")
	assertCode(t, err, domain.ErrorCodeNotFound)
	_, _, err = testManager(fake).SelectPane(context.Background(), deadID)
	assertCode(t, err, domain.ErrorCodeConflict)
}

func TestSelectPaneRejectsUnobservedSelection(t *testing.T) {
	fake := managedFake()
	window := fake.addWindow("project", 1, "notifier")
	fake.nextPane++
	target := &paneState{
		ID: fmt.Sprintf("%%%d", fake.nextPane), Index: 1, PID: int32(1000 + fake.nextPane),
		CurrentCommand: "claude", CurrentPath: "/repo",
	}
	fake.panes[window.ID] = append(fake.panes[window.ID], target)
	fake.silent["select-pane"] = true
	_, _, err := testManager(fake).SelectPane(context.Background(), target.ID)
	assertCode(t, err, domain.ErrorCodeDependency)
	if target.Active {
		t.Fatal("silent select-pane unexpectedly marked the pane active")
	}
}

func TestOpenProjectWindowRecreatesDeadOwnedTaggedWindow(t *testing.T) {
	fake := managedFake()
	project := domain.Project{ID: "project", Name: "project", Path: "/repos/project"}
	dead := fake.addWindow("dead-project", 1, "project")
	fake.options[dead.ID][optionProjectID] = project.ID
	fake.options[dead.ID][optionOwner] = "1"
	fake.panes[dead.ID][0].Dead = true

	result, err := testManager(fake).OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
	if err != nil {
		t.Fatalf("OpenProjectWindow() error = %v", err)
	}
	if result.Reused || result.Window.ID == dead.ID || fake.windows[dead.ID] != nil {
		t.Fatalf("dead reuse result = %+v, windows = %#v", result, fake.windows)
	}
}

func TestOpenProjectWindowReusesOnlyMatchingProfile(t *testing.T) {
	fake := managedFake()
	manager := testManager(fake)
	project := domain.Project{ID: "project", Name: "project", Path: "/repos/project"}

	first, err := manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project, Profile: "nvim"})
	if err != nil {
		t.Fatalf("first OpenProjectWindow() error = %v", err)
	}
	second, err := manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project, Profile: "opencode", EditorCommand: "opencode ."})
	if err != nil {
		t.Fatalf("second OpenProjectWindow() error = %v", err)
	}
	if second.Reused || second.Window.ID == first.Window.ID {
		t.Fatalf("different profile reused first window: first=%+v second=%+v", first, second)
	}
	reused, err := manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project, Profile: "opencode", EditorCommand: "opencode ."})
	if err != nil || !reused.Reused || reused.Window.ID != second.Window.ID {
		t.Fatalf("matching profile reuse = %+v, err = %v", reused, err)
	}
}

func TestOpenProjectWindowUsesCollisionSafeName(t *testing.T) {
	fake := managedFake()
	other := fake.addWindow("other", 1, "same-name")
	fake.options[other.ID][optionProjectID] = "other-project"
	project := domain.Project{ID: "this-project", Name: "same.name", Path: "/repos/same.name"}

	result, err := testManager(fake).OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
	if err != nil {
		t.Fatalf("OpenProjectWindow() error = %v", err)
	}
	if result.Window.Name == "same-name" || !strings.HasPrefix(result.Window.Name, "same-name-") {
		t.Fatalf("collision-safe name = %q", result.Window.Name)
	}
}

func TestOpenProjectWindowRollsBackOnlyCreatedWindowOnSilentMutations(t *testing.T) {
	tests := []string{"split-pane", "resize-pane", "select-pane", "set-option", "select-window"}
	for _, mutation := range tests {
		t.Run(mutation, func(t *testing.T) {
			fake := managedFake()
			dashboardID := fake.windowNamed("op").ID
			fake.silent[mutation] = true
			project := domain.Project{ID: "project", Name: "project", Path: "/repos/project"}

			_, err := testManager(fake).OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
			assertCode(t, err, domain.ErrorCodeDependency)
			if len(fake.windows) != 1 || fake.windows[dashboardID] == nil {
				t.Fatalf("rollback touched pre-existing windows: %#v", fake.windows)
			}
		})
	}
}

func TestOpenProjectWindowComposesRollbackFailure(t *testing.T) {
	fake := managedFake()
	fake.silent["split-pane"] = true
	fake.killError = errors.New("rollback refused")
	project := domain.Project{ID: "project", Name: "project", Path: "/repos/project"}

	_, err := testManager(fake).OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
	assertCode(t, err, domain.ErrorCodeDependency)
	if !strings.Contains(err.Error(), "shell pane split was not observable") || !strings.Contains(err.Error(), "rollback refused") {
		t.Fatalf("composed error = %v", err)
	}
}

func TestRollbackWindowToleratesSoleSessionDisappearance(t *testing.T) {
	fake := managedFake()
	dashboard := fake.windowNamed("op")
	fake.killWindowHook = func(context.Context) error {
		return errors.New("tmux kill-window: exit status 1: no server running on test socket")
	}
	if err := testManager(fake).rollbackWindow(context.Background(), dashboard.ID); err != nil {
		t.Fatalf("rollbackWindow() error = %v", err)
	}
}

func TestOpenProjectWindowRollbackDetachesWithBoundedCleanup(t *testing.T) {
	fake := managedFake()
	fake.silent["split-pane"] = true
	ctx, cancel := context.WithCancel(context.Background())
	fake.createdWindowHook = func(*fakeClient, *windowState) { cancel() }
	var cleanupDeadline time.Time
	fake.windowExistsHook = func(ctx context.Context, _ string) (bool, error) {
		var ok bool
		cleanupDeadline, ok = ctx.Deadline()
		if !ok {
			return false, errors.New("rollback context has no deadline")
		}
		if err := ctx.Err(); err != nil {
			return false, fmt.Errorf("rollback context started canceled: %w", err)
		}
		<-ctx.Done()
		return false, ctx.Err()
	}
	manager := testManager(fake)
	started := time.Now()
	project := domain.Project{ID: "project", Name: "project", Path: "/repos/project"}

	_, err := manager.OpenProjectWindow(ctx, OpenProjectWindowRequest{Project: project})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("rollback error = %v, want cleanup deadline exceeded", err)
	}
	if cleanupDeadline.IsZero() || cleanupDeadline.Sub(started) > manager.cleanupTimeout+10*time.Millisecond {
		t.Fatalf("cleanup deadline = %v, started = %v, timeout = %v", cleanupDeadline, started, manager.cleanupTimeout)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded rollback took %v", elapsed)
	}
}

func TestEnsureMainSessionPaneRollbackUsesBoundedRawVerification(t *testing.T) {
	fake := managedFake()
	dashboard := fake.windowNamed("op")
	managedPaneID := fake.options[dashboard.ID][optionDashboardPane]
	extra := &paneState{ID: "%extra", Index: 1, PID: 2000, CurrentPath: "/repo", AtBottom: true, Height: 20}
	fake.panes[dashboard.ID] = append(fake.panes[dashboard.ID], extra)
	for index, pane := range fake.panes[dashboard.ID] {
		if pane.ID == managedPaneID {
			fake.panes[dashboard.ID] = append(fake.panes[dashboard.ID][:index], fake.panes[dashboard.ID][index+1:]...)
			break
		}
	}
	fake.silent["start-command"] = true
	fake.paneExistsHook = func(ctx context.Context, _ string) (bool, error) {
		if _, ok := ctx.Deadline(); !ok {
			return false, errors.New("pane rollback context has no deadline")
		}
		<-ctx.Done()
		return false, ctx.Err()
	}
	manager := testManager(fake)
	started := time.Now()

	_, err := manager.EnsureMainSession(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("pane rollback error = %v, want cleanup deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded pane rollback took %v", elapsed)
	}
}

func TestOpenProjectWindowRejectsHostileValuesWithoutMutation(t *testing.T) {
	tests := []domain.Project{
		{ID: "id", Name: "we-:-ird", Path: "/repos/good"},
		{ID: "id", Name: "line\nbreak", Path: "/repos/good"},
		{ID: "id", Name: "good", Path: "/repos/we-:-ird"},
		{ID: "id\nbreak", Name: "good", Path: "/repos/good"},
	}
	for _, project := range tests {
		t.Run(fmt.Sprintf("%q", project), func(t *testing.T) {
			fake := managedFake()
			_, err := testManager(fake).OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
			assertCode(t, err, domain.ErrorCodeInvalidArgument)
			if len(fake.windows) != 1 {
				t.Fatalf("hostile project mutated windows: %#v", fake.windows)
			}
		})
	}
}

func TestSnapshotTrimsOnlyTagLineEndingsAndMapsPaneState(t *testing.T) {
	fake := managedFake()
	window := fake.windowNamed("op")
	fake.options[window.ID][optionProjectID] = " project-id \n"
	fake.options[window.ID][optionProfile] = " nvim \n"
	manager := testManager(fake)
	manager.now = func() time.Time { return time.Unix(123, 0) }

	snapshot, err := manager.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	got := snapshot.Session.Windows[0]
	if got.ProjectID != " project-id " || got.Profile != " nvim " || !snapshot.CapturedAt.Equal(time.Unix(123, 0)) {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestCurrentProjectTargeting(t *testing.T) {
	fake := managedFake()
	window := fake.windowNamed("op")
	fake.options[window.ID][optionProjectID] = "project-id\n"
	fake.currentWindow = window.ID
	manager := testManager(fake)
	manager.lookupEnv = func(key string) string {
		if key == "TMUX" {
			return "/tmp/tmux-1000/default,123,0"
		}
		return "%1"
	}

	id, found, err := manager.CurrentProjectID(context.Background())
	if err != nil || !found || id != "project-id" {
		t.Fatalf("CurrentProjectID() = %q, %v, %v", id, found, err)
	}
	name, found, err := manager.CurrentProjectName(context.Background())
	if err != nil || !found || name != "op" {
		t.Fatalf("CurrentProjectName() = %q, %v, %v", name, found, err)
	}
	manager.lookupEnv = func(string) string { return "" }
	if _, found, err := manager.CurrentProjectID(context.Background()); err != nil || found {
		t.Fatalf("outside CurrentProjectID() found=%v err=%v", found, err)
	}
}

func TestCurrentProjectNameCanTargetAnotherTmuxSession(t *testing.T) {
	fake := managedFake()
	fake.currentWindow = "@external"
	fake.currentWindowName = "project-in-another-session"
	manager := testManager(fake)
	manager.lookupEnv = func(key string) string {
		if key == "TMUX" {
			return "/tmp/tmux-1000/default,123,0"
		}
		return "%99"
	}

	name, found, err := manager.CurrentProjectName(context.Background())
	if err != nil || !found || name != fake.currentWindowName {
		t.Fatalf("CurrentProjectName() = %q, %v, %v", name, found, err)
	}
}

func TestAttachOrSwitchUsesExplicitWritersAndVerifiesSwitch(t *testing.T) {
	fake := managedFake()
	dashboard := fake.windowNamed("op")
	project := fake.addWindow("project", 1, "project")
	dashboard.Active = false
	project.Active = true
	output := new(bytes.Buffer)
	errorOutput := new(bytes.Buffer)
	manager := testManager(fake)
	manager.config.Output = output
	manager.config.Error = errorOutput
	manager.lookupEnv = func(string) string { return "" }
	if err := executeAttachOrSwitch(context.Background(), manager); err != nil {
		t.Fatalf("AttachOrSwitch() attach error = %v", err)
	}
	if fake.attachOutput != output || fake.attachError != errorOutput {
		t.Fatal("attach did not receive explicit output and error writers")
	}
	if dashboard.Active || !project.Active {
		t.Fatal("outside attach changed the shared active window")
	}
	if fake.attachSession != fake.session.ID || fake.attachWindow != dashboard.ID {
		t.Fatalf("attach target = %q:%q, want %q:%q", fake.attachSession, fake.attachWindow, fake.session.ID, dashboard.ID)
	}

	const invokingPane = "%99"
	const invokingClient = "/dev/pts/99"
	fake.clients[invokingClient] = &clientState{Name: invokingClient, ActivePane: invokingPane}
	fake.clients["/dev/pts/100"] = &clientState{Name: "/dev/pts/100", ActivePane: "%100"}
	fake.clientSessions[invokingClient] = "external"
	fake.clientSessions["/dev/pts/100"] = "observer"
	fake.paneSessions[invokingPane] = "external"
	manager.lookupEnv = func(key string) string {
		if key == "TMUX" {
			return "/tmp/tmux-1000/default,123,0"
		}
		return invokingPane
	}
	if err := executeAttachOrSwitch(context.Background(), manager); err != nil {
		t.Fatalf("AttachOrSwitch() switch error = %v", err)
	}
	if fake.clientSessions[invokingClient] != "code" {
		t.Fatalf("invoking client session = %q", fake.clientSessions[invokingClient])
	}
	if fake.clientSessions["/dev/pts/100"] != "observer" {
		t.Fatalf("observer client session = %q", fake.clientSessions["/dev/pts/100"])
	}
	if fake.clients[invokingClient].ActivePane != fake.panes[dashboard.ID][0].ID {
		t.Fatalf("invoking client active pane = %q", fake.clients[invokingClient].ActivePane)
	}
	if fake.clients["/dev/pts/100"].ActivePane != "%100" {
		t.Fatalf("observer client active pane = %q", fake.clients["/dev/pts/100"].ActivePane)
	}
	if fake.paneSessions[invokingPane] != "external" {
		t.Fatalf("invoking pane moved to %q", fake.paneSessions[invokingPane])
	}
}

func TestAttachOrSwitchCanTargetProjectWindow(t *testing.T) {
	fake := managedFake()
	project := fake.addWindow("project", 1, "project")
	manager := testManager(fake)
	manager.lookupEnv = func(string) string { return "" }
	plan, err := manager.PrepareAttachOrSwitchTo(context.Background(), project.ID)
	if err != nil {
		t.Fatalf("PrepareAttachOrSwitchTo() error = %v", err)
	}
	if err := manager.ExecuteAttachOrSwitch(context.Background(), plan); err != nil {
		t.Fatalf("ExecuteAttachOrSwitch() error = %v", err)
	}
	if fake.attachWindow != project.ID {
		t.Fatalf("attach window = %q, want %q", fake.attachWindow, project.ID)
	}
}

func TestAttachOrSwitchRejectsMissingOrAmbiguousInvokingClient(t *testing.T) {
	for _, test := range []struct {
		name    string
		clients []clientState
		code    domain.ErrorCode
	}{
		{name: "missing", clients: []clientState{{Name: "observer", ActivePane: "%100"}}, code: domain.ErrorCodeNotFound},
		{name: "ambiguous", clients: []clientState{{Name: "first", ActivePane: "%99"}, {Name: "second", ActivePane: "%99"}}, code: domain.ErrorCodeConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := managedFake()
			dashboard := fake.windowNamed("op")
			project := fake.addWindow("project", 1, "project")
			dashboard.Active = false
			project.Active = true
			for i := range test.clients {
				client := test.clients[i]
				fake.clients[client.Name] = &client
				fake.clientSessions[client.Name] = "external"
			}
			manager := testManager(fake)
			manager.lookupEnv = func(key string) string {
				if key == "TMUX" {
					return "/tmp/tmux-1000/default,123,0"
				}
				return "%99"
			}

			err := executeAttachOrSwitch(context.Background(), manager)
			assertCode(t, err, test.code)
			for _, client := range test.clients {
				if strings.Contains(err.Error(), client.Name) {
					t.Fatalf("error exposed client identity: %v", err)
				}
			}
			if dashboard.Active || !project.Active {
				t.Fatal("dashboard was selected before client identity was resolved")
			}
			for name, session := range fake.clientSessions {
				if session != "external" {
					t.Fatalf("client %q switched to %q", name, session)
				}
			}
		})
	}
}

func TestAttachOrSwitchDetectsSilentTargetedSwitch(t *testing.T) {
	fake := managedFake()
	const clientName = "/dev/pts/99"
	fake.clients[clientName] = &clientState{Name: clientName, ActivePane: "%99"}
	fake.clientSessions[clientName] = "external"
	fake.silent["switch-client"] = true
	manager := testManager(fake)
	manager.lookupEnv = func(key string) string {
		if key == "TMUX" {
			return "/tmp/tmux-1000/default,123,0"
		}
		return "%99"
	}

	assertCode(t, executeAttachOrSwitch(context.Background(), manager), domain.ErrorCodeDependency)
	if fake.clientSessions[clientName] != "external" {
		t.Fatalf("client session = %q", fake.clientSessions[clientName])
	}
}

func TestCallerTargetingAcceptsMatchingExplicitSocket(t *testing.T) {
	fake := managedFake()
	window := fake.windowNamed("op")
	fake.currentWindow = window.ID
	fake.options[window.ID][optionProjectID] = "project-id"
	fake.clients["client"] = &clientState{Name: "client", ActivePane: fake.panes[window.ID][0].ID}
	fake.clientSessions["client"] = "external"
	manager := testManager(fake)
	manager.config.Socket = "/tmp/op-managed.sock"
	manager.lookupEnv = callerEnvironment("/tmp/op-managed.sock,123,0", fake.panes[window.ID][0].ID)

	id, found, err := manager.CurrentProjectID(context.Background())
	if err != nil || !found || id != "project-id" {
		t.Fatalf("CurrentProjectID() = %q, %v, %v", id, found, err)
	}
	name, found, err := manager.CurrentProjectName(context.Background())
	if err != nil || !found || name != "op" {
		t.Fatalf("CurrentProjectName() = %q, %v, %v", name, found, err)
	}
	if err := executeAttachOrSwitch(context.Background(), manager); err != nil {
		t.Fatalf("AttachOrSwitch() error = %v", err)
	}
	if fake.clientSessions["client"] != "code" {
		t.Fatalf("matching-socket client remained in %q", fake.clientSessions["client"])
	}
}

func TestCallerTargetingParsesSocketPathsContainingCommas(t *testing.T) {
	fake := managedFake()
	window := fake.windowNamed("op")
	fake.currentWindow = window.ID
	fake.options[window.ID][optionProjectID] = "project-id"
	manager := testManager(fake)
	manager.config.Socket = "/tmp/op,sockets/managed.sock"
	manager.lookupEnv = callerEnvironment("/tmp/op,sockets/managed.sock,123,0", fake.panes[window.ID][0].ID)

	id, found, err := manager.CurrentProjectID(context.Background())
	if err != nil || !found || id != "project-id" {
		t.Fatalf("CurrentProjectID() = %q, %v, %v", id, found, err)
	}
}

func TestCallerTargetingRejectsForeignSocketBeforeBackendAccess(t *testing.T) {
	for _, test := range []struct {
		name    string
		panicOn string
		call    func(*Manager) error
	}{
		{name: "project ID", panicOn: "current-window", call: func(manager *Manager) error {
			_, _, err := manager.CurrentProjectID(context.Background())
			return err
		}},
		{name: "project name", panicOn: "current-window-name", call: func(manager *Manager) error {
			_, _, err := manager.CurrentProjectName(context.Background())
			return err
		}},
		{name: "attach or switch", panicOn: "session", call: func(manager *Manager) error {
			return executeAttachOrSwitch(context.Background(), manager)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fake := managedFake()
			dashboard := fake.windowNamed("op")
			project := fake.addWindow("project", 1, "project")
			dashboard.Active = false
			project.Active = true
			fake.currentWindow = dashboard.ID
			fake.options[dashboard.ID][optionProjectID] = "colliding-project"
			collidingPane := fake.panes[dashboard.ID][0].ID
			fake.clients["colliding-client"] = &clientState{Name: "colliding-client", ActivePane: collidingPane}
			fake.clientSessions["colliding-client"] = "external"
			fake.panicOn = test.panicOn
			manager := testManager(fake)
			manager.config.Socket = "/tmp/managed.sock"
			manager.lookupEnv = callerEnvironment("/tmp/foreign.sock,456,0", collidingPane)

			err := test.call(manager)
			assertCode(t, err, domain.ErrorCodeConflict)
			if !strings.Contains(err.Error(), "configured tmux.socket") {
				t.Fatalf("error = %v, want clear socket mismatch", err)
			}
			if dashboard.Active || !project.Active {
				t.Fatal("foreign caller mutated managed window selection")
			}
			if fake.clientSessions["colliding-client"] != "external" {
				t.Fatalf("colliding managed client switched to %q", fake.clientSessions["colliding-client"])
			}
		})
	}
}

func TestCallerTargetingRejectsMalformedEnvironmentBeforeBackendAccess(t *testing.T) {
	for _, test := range []struct {
		tmuxValue string
		paneID    string
		message   string
	}{
		{tmuxValue: "inside", paneID: "%1", message: "TMUX does not identify a valid tmux server"},
		{tmuxValue: "/tmp/socket,123", paneID: "%1", message: "TMUX does not identify a valid tmux server"},
		{tmuxValue: ",123,0", paneID: "%1", message: "TMUX does not identify a valid tmux server"},
		{tmuxValue: "/tmp/socket,pid,0", paneID: "%1", message: "TMUX does not identify a valid tmux server"},
		{tmuxValue: "/tmp/socket,123,pane", paneID: "%1", message: "TMUX does not identify a valid tmux server"},
		{tmuxValue: "/tmp/socket,123,0", paneID: "not-a-pane", message: "TMUX_PANE does not identify a valid tmux pane"},
	} {
		t.Run(test.tmuxValue+"/"+test.paneID, func(t *testing.T) {
			fake := managedFake()
			fake.panicOn = "current-window"
			manager := testManager(fake)
			manager.lookupEnv = callerEnvironment(test.tmuxValue, test.paneID)

			_, _, err := manager.CurrentProjectID(context.Background())
			assertCode(t, err, domain.ErrorCodeDependency)
			if !strings.Contains(err.Error(), test.message) {
				t.Fatalf("error = %v, want malformed TMUX detail", err)
			}
		})
	}
}

func TestCallerPaneRequiresCanonicalPaneID(t *testing.T) {
	manager := testManager(managedFake())
	for _, paneID := range []string{"%0", "%1", "%10"} {
		manager.lookupEnv = callerEnvironment("/tmp/socket,123,0", paneID)
		got, inside, err := manager.callerPane("test")
		if err != nil || !inside || got != paneID {
			t.Fatalf("callerPane(%q) = %q, %v, %v", paneID, got, inside, err)
		}
	}
	for _, paneID := range []string{"%", "%00", "%01", "%+1", "%-1", "%1x", "1"} {
		manager.lookupEnv = callerEnvironment("/tmp/socket,123,0", paneID)
		if _, _, err := manager.callerPane("test"); !domain.IsCode(err, domain.ErrorCodeDependency) {
			t.Fatalf("callerPane(%q) error = %v, want dependency error", paneID, err)
		}
	}
}

func TestNormalizeSocketPathResolvesExistingSymlinkAncestor(t *testing.T) {
	root := t.TempDir()
	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	linkDirectory := filepath.Join(root, "link")
	if err := os.Symlink(realDirectory, linkDirectory); err != nil {
		t.Fatal(err)
	}

	got, err := normalizeSocketPath(filepath.Join(linkDirectory, "missing.sock"))
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(realDirectory, "missing.sock")
	if got != want {
		t.Fatalf("normalizeSocketPath() = %q, want %q", got, want)
	}
}

func callerEnvironment(tmuxValue, paneID string) func(string) string {
	return func(key string) string {
		switch key {
		case "TMUX":
			return tmuxValue
		case "TMUX_PANE":
			return paneID
		default:
			return ""
		}
	}
}

func TestManagerRecoversBackendPanicAsTypedError(t *testing.T) {
	fake := managedFake()
	fake.panicOn = "list-windows"
	_, err := testManager(fake).Snapshot(context.Background())
	assertCode(t, err, domain.ErrorCodeInternal)
}

func TestOpenProjectWindowRollsBackCreatedWindowOnBackendPanic(t *testing.T) {
	fake := managedFake()
	manager := testManager(fake)
	project := domain.Project{ID: "project", Name: "project", Path: "/repos/project"}
	fake.panicOn = "list-panes"

	_, err := manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
	assertCode(t, err, domain.ErrorCodeInternal)
	if len(fake.windows) != 1 || fake.windowNamed("op") == nil {
		t.Fatalf("panic rollback touched pre-existing windows: %#v", fake.windows)
	}
}

func TestManagerConfigAndNameValidation(t *testing.T) {
	for _, name := range []string{"bad:name", "bad.name", "bad-:-name", "bad\nname"} {
		config := testConfig()
		config.Session = name
		if err := validateConfig(config); !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
			t.Fatalf("validateConfig(%q) error = %v", name, err)
		}
	}
	for input, want := range map[string]string{"space name": "space name", `quote'name`: `quote'name`, "repo:name": "repo-name", "repo.name": "repo-name"} {
		got, err := normalizeProjectName("test", input)
		if err != nil || got != want {
			t.Fatalf("normalizeProjectName(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}

func TestCommandExecutableParsesQuotedExecutable(t *testing.T) {
	for command, want := range map[string]string{
		`exec '/path with spaces/op' dashboard`:             "op",
		`'/path with spaces/op' dashboard`:                  "op",
		`exec '/path/with '\''quote/op' dashboard`:          "op",
		`exec '/mnt/c/Program Files/PowerShell/7/pwsh.exe'`: "pwsh.exe",
	} {
		if got := commandExecutable(command); got != want {
			t.Fatalf("commandExecutable(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestCommandBaseNameStripsWindowsAndPOSIXPaths(t *testing.T) {
	for executable, want := range map[string]string{
		`C:\Tools\pwsh.exe`:                          "pwsh.exe",
		`/mnt/c/Program Files/PowerShell/7/pwsh.exe`: "pwsh.exe",
		"pwsh.exe": "pwsh.exe",
	} {
		if got := commandBaseName(executable); got != want {
			t.Fatalf("commandBaseName(%q) = %q, want %q", executable, got, want)
		}
	}
}

func TestPaneCommandMatchesWSLInteropAndPowerShellAliases(t *testing.T) {
	expected := map[string]bool{"pwsh.exe": true}
	for _, observed := range []string{"pwsh.exe", "pwsh", "PWSH.EXE", "init", "wslrelay"} {
		if !paneCommandMatches(observed, expected) {
			t.Fatalf("paneCommandMatches(%q, pwsh.exe) = false", observed)
		}
	}
	if paneCommandMatches("init", map[string]bool{"zsh": true}) {
		t.Fatal("WSL interop comm matched a POSIX shell")
	}
	if paneCommandMatches("nvim", expected) {
		t.Fatal("unrelated command matched pwsh.exe")
	}
}

func TestDashboardWrapsWindowsExeShellWithPOSIX(t *testing.T) {
	command, err := buildPersistentShellCommand(linuxPersistentShell("pwsh.exe"), "op dashboard")
	if err != nil {
		t.Fatalf("buildPersistentShellCommand() error = %v", err)
	}
	words := shellWords(command)
	if !slices.Contains(words, "sh") || !slices.Contains(words, "-ic") || slices.Contains(words, "-NoExit") || slices.Contains(words, "pwsh.exe") {
		t.Fatalf("dashboard wrapper words = %#v", words)
	}
}

func TestLinuxPersistentShellUsesPOSIXForWindowsExe(t *testing.T) {
	if got := linuxPersistentShell("pwsh.exe"); got != "sh" {
		t.Fatalf("linuxPersistentShell(pwsh.exe) = %q, want sh", got)
	}
	if got := linuxPersistentShell("/usr/bin/pwsh.exe -NoLogo"); got != "sh" {
		t.Fatalf("linuxPersistentShell(pwsh.exe path) = %q, want sh", got)
	}
	if got := linuxPersistentShell("pwsh"); got != "pwsh" {
		t.Fatalf("linuxPersistentShell(pwsh) = %q, want pwsh", got)
	}
	if got := linuxPersistentShell("zsh -l"); got != "zsh -l" {
		t.Fatalf("linuxPersistentShell(zsh) = %q, want zsh -l", got)
	}
}

func TestOpenProjectWindowAcceptsWSLInteropForeground(t *testing.T) {
	fake := managedFake()
	fake.startupCommandOverride = "init"
	manager := testManager(fake)
	manager.config.PreferredShell = "pwsh.exe"
	project := domain.Project{ID: "project-1", Name: "repo.one", Path: "/repos/repo.one", Kind: domain.ProjectKindRepository}

	result, err := manager.OpenProjectWindow(context.Background(), OpenProjectWindowRequest{Project: project})
	if err != nil {
		t.Fatalf("OpenProjectWindow() error = %v", err)
	}
	if result.Reused || len(result.Window.Panes) != 2 {
		t.Fatalf("result = %+v", result)
	}
	panes := fake.panes[result.Window.ID]
	if panes[1].CurrentCommand != "init" {
		t.Fatalf("shell pane command = %q", panes[1].CurrentCommand)
	}
	if got := fake.splitShell[panes[1].ID]; got != "pwsh.exe" {
		t.Fatalf("split shell command = %q", got)
	}
}

func TestBuildEditorPaneCommandQuotesAndPreservesPreferredShellArguments(t *testing.T) {
	command, err := buildEditorPaneCommand(`'/opt/shell dir/zsh' -l "argument value" "argument\q" '$HOME'`, `nvim 'file name'`)
	if err != nil {
		t.Fatalf("buildEditorPaneCommand() error = %v", err)
	}
	words := shellWords(command)
	want := []string{
		"exec", "/opt/shell dir/zsh", "-l", "argument value", `argument\q`, "$HOME", "-ic",
		`nvim 'file name'; exec '/opt/shell dir/zsh' '-l' 'argument value' 'argument\q' '$HOME'`,
	}
	if fmt.Sprint(words) != fmt.Sprint(want) {
		t.Fatalf("wrapper words = %#v, want %#v; command = %q", words, want, command)
	}
}

func TestBuildEditorPaneCommandUsesNoExitForPowerShellFamily(t *testing.T) {
	for _, shell := range []string{"pwsh", "/usr/bin/pwsh.exe -NoLogo", `C:\\Tools\\PowerShell.exe -NoProfile`} {
		t.Run(shell, func(t *testing.T) {
			command, err := buildEditorPaneCommand(shell, "nvim .")
			if err != nil {
				t.Fatalf("buildEditorPaneCommand() error = %v", err)
			}
			words := shellWords(command)
			if !slices.Contains(words, "-NoExit") || !slices.Contains(words, "-Command") || slices.Contains(words, "-ic") || words[len(words)-1] != "nvim ." {
				t.Fatalf("PowerShell wrapper words = %#v", words)
			}
		})
	}
}

func TestBuildEditorPaneCommandRejectsMalformedPreferredShell(t *testing.T) {
	for _, shell := range []string{"zsh && bash", `zsh "unterminated`} {
		if _, err := buildEditorPaneCommand(shell, "nvim ."); err == nil {
			t.Fatalf("buildEditorPaneCommand(%q) succeeded", shell)
		}
	}
}

func TestBuildPersistentDashboardShellCommandLeavesShellAfterExit(t *testing.T) {
	command, err := buildPersistentShellCommand(`'/opt/shell dir/zsh' -l`, `'/opt/op dir/op' --config '/config dir/config.json' dashboard`)
	if err != nil {
		t.Fatalf("buildPersistentShellCommand() error = %v", err)
	}
	words := shellWords(command)
	want := []string{
		"exec", "/opt/shell dir/zsh", "-l", "-ic",
		`'/opt/op dir/op' --config '/config dir/config.json' dashboard; exec '/opt/shell dir/zsh' '-l'`,
	}
	if !slices.Equal(words, want) {
		t.Fatalf("wrapper words = %#v, want %#v; command = %q", words, want, command)
	}
}

func assertCode(t *testing.T, err error, code domain.ErrorCode) {
	t.Helper()
	if err == nil || !domain.IsCode(err, code) {
		t.Fatalf("error = %v, want code %q", err, code)
	}
}

func testConfig() ManagerConfig {
	return ManagerConfig{
		Session: "code", DashboardWindow: "op", StartDirectory: "/repo",
		DashboardCommand: "op dashboard", EditorCommand: "nvim .",
		PreferredShell: "zsh", ShellPaneRows: 20, DefaultProfile: "nvim",
	}
}

func testManager(fake *fakeClient) *Manager {
	manager := newManager(testConfig(), fake, false)
	manager.dashboardProcessAlive = func(context.Context, int, string) (bool, error) { return true, nil }
	manager.startupWait = 15 * time.Millisecond
	manager.startupPoll = time.Millisecond
	manager.startupStable = 0
	manager.cleanupTimeout = 20 * time.Millisecond
	return manager
}

func managedFake() *fakeClient {
	fake := newFakeClient()
	fake.addSession("code")
	dashboard := fake.addWindow("dashboard", 0, "op")
	fake.options[dashboard.ID][optionRole] = roleDashboard
	fake.options[dashboard.ID][optionOwner] = "1"
	fake.options[dashboard.ID][optionDashboardPane] = fake.panes[dashboard.ID][0].ID
	fake.options[dashboard.ID][optionDashboardPID] = strconv.FormatInt(int64(fake.panes[dashboard.ID][0].PID), 10)
	return fake
}

type fakeClient struct {
	session                  *sessionState
	windows                  map[string]*windowState
	windowOrder              []string
	panes                    map[string][]*paneState
	options                  map[string]map[string]string
	serverOptions            map[string]string
	bindings                 map[string]string
	sessionOptions           map[string]string
	splitShell               map[string]string
	silent                   map[string]bool
	panicOn                  string
	nextWindow               int
	nextPane                 int
	currentWindow            string
	currentWindowName        string
	clients                  map[string]*clientState
	clientSessions           map[string]string
	paneSessions             map[string]string
	attachOutput             io.Writer
	attachError              io.Writer
	attachSession            string
	attachWindow             string
	startupCommandOverride   string
	killError                error
	killWindowHook           func(context.Context) error
	windowExistsHook         func(context.Context, string) (bool, error)
	paneExistsHook           func(context.Context, string) (bool, error)
	concurrentWindowOnCreate bool
	concurrentWindowID       string
	lastCreatedWindowID      string
	killedWindowIDs          []string
	createdWindowHook        func(*fakeClient, *windowState)
	createWindowResult       string
	createdWindowShell       string
}

func newFakeClient() *fakeClient {
	return &fakeClient{
		windows: make(map[string]*windowState), panes: make(map[string][]*paneState),
		options: make(map[string]map[string]string), serverOptions: make(map[string]string), sessionOptions: make(map[string]string), bindings: make(map[string]string),
		splitShell: make(map[string]string), silent: make(map[string]bool),
		clients: make(map[string]*clientState), clientSessions: make(map[string]string), paneSessions: make(map[string]string),
	}
}

func TestEnsureTreeBindingUsesPrefixTAndRestoresSpace(t *testing.T) {
	client := newFakeClient()
	client.bindings["prefix:Space"] = "bind-key -T prefix Space switch-client -T op"
	manager := newManager(ManagerConfig{TreeCommand: "/opt/op tree"}, client, false)

	if err := manager.ensureTreeBinding(context.Background()); err != nil {
		t.Fatalf("ensure tree binding: %v", err)
	}
	if got := client.bindings["prefix:Space"]; !strings.Contains(got, "next-layout") {
		t.Fatalf("Space binding = %q, want next-layout", got)
	}
	if got := client.bindings["prefix:T"]; !strings.Contains(got, "display-popup -E -w 80% -h 80% /opt/op tree") {
		t.Fatalf("T binding = %q", got)
	}
}

func (f *fakeClient) check(operation string) {
	if f.panicOn == operation {
		panic("fake gotmux panic")
	}
}

func (f *fakeClient) addSession(name string) { f.session = &sessionState{ID: "$1", Name: name} }

func (f *fakeClient) addWindow(id string, index int, name string) *windowState {
	window := &windowState{ID: "@" + id, Index: index, Name: name, Active: len(f.windows) == 0}
	f.windows[window.ID] = window
	f.windowOrder = append(f.windowOrder, window.ID)
	f.options[window.ID] = make(map[string]string)
	f.nextPane++
	f.panes[window.ID] = []*paneState{{
		ID: fmt.Sprintf("%%%d", f.nextPane), Index: 0, PID: int32(1000 + f.nextPane),
		CurrentCommand: "sh", CurrentPath: "/repo", Active: true, AtTop: true, AtBottom: true, Height: 40,
	}}
	if index >= f.nextWindow {
		f.nextWindow = index + 1
	}
	return window
}

func (f *fakeClient) onlyWindow(t *testing.T) *windowState {
	t.Helper()
	if len(f.windows) != 1 {
		t.Fatalf("windows = %#v, want one", f.windows)
	}
	for _, window := range f.windows {
		return window
	}
	panic("unreachable")
}

func (f *fakeClient) windowNamed(name string) *windowState {
	for _, window := range f.windows {
		if window.Name == name {
			return window
		}
	}
	return nil
}

func (f *fakeClient) Session(_ context.Context, name string) (*sessionState, error) {
	f.check("session")
	if f.session == nil || f.session.Name != name {
		return nil, nil
	}
	copy := *f.session
	return &copy, nil
}

func (f *fakeClient) CreateSession(_ context.Context, name, _, shellCommand string) error {
	f.check("create-session")
	if f.silent["create-session"] {
		return nil
	}
	f.addSession(name)
	window := f.addWindow("initial", 0, "shell")
	f.startCommand(f.panes[window.ID][0], shellCommand)
	return nil
}

func (f *fakeClient) ListWindows(_ context.Context, _ string) ([]windowState, error) {
	f.check("list-windows")
	result := make([]windowState, 0, len(f.windows))
	for _, windowID := range f.windowOrder {
		if window := f.windows[windowID]; window != nil {
			result = append(result, *window)
		}
	}
	return result, nil
}

func (f *fakeClient) CreateWindow(_ context.Context, _, name, directory, shellCommand string) (string, error) {
	f.check("create-window")
	if f.createWindowResult != "" {
		return f.createWindowResult, nil
	}
	if f.silent["create-window"] {
		return "", nil
	}
	if f.concurrentWindowOnCreate {
		user := f.addWindow(strconv.Itoa(f.nextWindow), f.nextWindow, "user-concurrent")
		f.panes[user.ID][0].CurrentPath = "/user"
		f.concurrentWindowID = user.ID
	}
	window := f.addWindow(strconv.Itoa(f.nextWindow), f.nextWindow, name)
	f.panes[window.ID][0].CurrentPath = directory
	f.createdWindowShell = shellCommand
	f.startCommand(f.panes[window.ID][0], shellCommand)
	f.lastCreatedWindowID = window.ID
	if f.createdWindowHook != nil {
		f.createdWindowHook(f, window)
	}
	return window.ID, nil
}

func (f *fakeClient) RenameWindow(_ context.Context, windowID, name string) error {
	f.check("rename-window")
	if !f.silent["rename-window"] {
		f.windows[windowID].Name = name
	}
	return nil
}

func (f *fakeClient) KillWindow(ctx context.Context, windowID string) error {
	f.check("kill-window")
	f.killedWindowIDs = append(f.killedWindowIDs, windowID)
	if f.killWindowHook != nil {
		return f.killWindowHook(ctx)
	}
	if f.killError != nil {
		return f.killError
	}
	if f.silent["kill-window"] {
		return nil
	}
	delete(f.windows, windowID)
	delete(f.panes, windowID)
	delete(f.options, windowID)
	return nil
}

func (f *fakeClient) WindowExists(ctx context.Context, windowID string) (bool, error) {
	if f.windowExistsHook != nil {
		return f.windowExistsHook(ctx, windowID)
	}
	return f.windows[windowID] != nil, nil
}

func (f *fakeClient) SelectWindow(_ context.Context, windowID string) error {
	f.check("select-window")
	if f.silent["select-window"] {
		return nil
	}
	for _, window := range f.windows {
		window.Active = window.ID == windowID
	}
	f.currentWindow = windowID
	return nil
}

func (f *fakeClient) MoveWindow(_ context.Context, windowID, _ string, index int) error {
	f.check("move-window")
	if !f.silent["move-window"] {
		f.windows[windowID].Index = index
	}
	return nil
}

func (f *fakeClient) SwapWindow(_ context.Context, windowID, _ string, index int) error {
	f.check("swap-window")
	if f.silent["swap-window"] {
		return nil
	}
	old := f.windows[windowID].Index
	for _, window := range f.windows {
		if window.ID != windowID && window.Index == index {
			window.Index = old
		}
	}
	f.windows[windowID].Index = index
	return nil
}

func (f *fakeClient) ListPanes(_ context.Context, windowID string) ([]paneState, error) {
	f.check("list-panes")
	result := make([]paneState, 0, len(f.panes[windowID]))
	for _, pane := range f.panes[windowID] {
		result = append(result, *pane)
	}
	return result, nil
}

func (f *fakeClient) SplitPane(_ context.Context, paneID, directory, shellCommand string) error {
	f.check("split-pane")
	if f.silent["split-pane"] {
		return nil
	}
	windowID, _, err := f.findPane(paneID)
	if err != nil {
		return err
	}
	for _, pane := range f.panes[windowID] {
		pane.Active = false
		pane.AtTop = true
		pane.AtBottom = false
	}
	f.nextPane++
	created := &paneState{ID: fmt.Sprintf("%%%d", f.nextPane), Index: len(f.panes[windowID]), PID: int32(1000 + f.nextPane), CurrentCommand: "sh", CurrentPath: directory, Active: true, AtBottom: true, Height: 10}
	if shellCommand != "" {
		f.startCommand(created, shellCommand)
	}
	f.panes[windowID] = append(f.panes[windowID], created)
	f.splitShell[created.ID] = shellCommand
	return nil
}

func (f *fakeClient) ResizePane(_ context.Context, paneID string, rows int) error {
	f.check("resize-pane")
	if f.silent["resize-pane"] {
		return nil
	}
	_, pane, err := f.findPane(paneID)
	if err == nil {
		pane.Height = rows
	}
	return err
}

func (f *fakeClient) SelectPane(_ context.Context, paneID string) error {
	f.check("select-pane")
	if f.silent["select-pane"] {
		return nil
	}
	windowID, _, err := f.findPane(paneID)
	if err != nil {
		return err
	}
	for _, pane := range f.panes[windowID] {
		pane.Active = pane.ID == paneID
	}
	return nil
}

func (f *fakeClient) RespawnPane(_ context.Context, paneID, shellCommand string) error {
	f.check("respawn-pane")
	if f.silent["respawn-pane"] {
		return nil
	}
	_, pane, err := f.findPane(paneID)
	if err == nil {
		f.nextPane++
		pane.PID = int32(1000 + f.nextPane)
		pane.Dead = false
		f.startCommand(pane, shellCommand)
		if pane.CurrentCommand == "" {
			pane.CurrentCommand = "sh"
		}
	}
	return err
}

func (f *fakeClient) startCommand(pane *paneState, command string) {
	if command == "" || f.silent["start-command"] {
		return
	}
	pane.CurrentCommand = foregroundExecutable(command)
	if f.startupCommandOverride != "" {
		pane.CurrentCommand = f.startupCommandOverride
	}
}

func foregroundExecutable(command string) string {
	words := shellWords(command)
	for index, word := range words {
		if (word == "-ic" || word == "-Command") && index+1 < len(words) {
			return commandExecutable(words[index+1])
		}
	}
	return commandExecutable(command)
}

func (f *fakeClient) KillPane(_ context.Context, paneID string) error {
	f.check("kill-pane")
	if f.silent["kill-pane"] {
		return nil
	}
	windowID, _, err := f.findPane(paneID)
	if err != nil {
		return err
	}
	panes := f.panes[windowID]
	kept := panes[:0]
	for _, pane := range panes {
		if pane.ID != paneID {
			pane.Index = len(kept)
			kept = append(kept, pane)
		}
	}
	f.panes[windowID] = kept
	if len(kept) == 1 {
		kept[0].Active = true
		kept[0].AtTop = true
		kept[0].AtBottom = true
	}
	return nil
}

func (f *fakeClient) PaneExists(ctx context.Context, paneID string) (bool, error) {
	if f.paneExistsHook != nil {
		return f.paneExistsHook(ctx, paneID)
	}
	for _, panes := range f.panes {
		for _, pane := range panes {
			if pane.ID == paneID {
				return true, nil
			}
		}
	}
	return false, nil
}

func (f *fakeClient) SetWindowOption(_ context.Context, windowID, key, value string) error {
	f.check("set-option")
	if !f.silent["set-option"] {
		f.options[windowID][key] = value
	}
	return nil
}

func (f *fakeClient) WindowOption(_ context.Context, windowID, key string) (string, bool, error) {
	f.check("window-option")
	value, exists := f.options[windowID][key]
	return value, exists, nil
}

func (f *fakeClient) SetServerOption(_ context.Context, key, value string) error {
	f.check("set-server-option")
	if !f.silent["set-server-option"] {
		f.serverOptions[key] = value
	}
	return nil
}

func (f *fakeClient) ServerOption(_ context.Context, key string) (string, bool, error) {
	f.check("server-option")
	value, exists := f.serverOptions[key]
	return value, exists, nil
}

func (f *fakeClient) BindKey(_ context.Context, table, key string, command ...string) error {
	f.check("bind-key")
	if !f.silent["bind-key"] {
		f.bindings[table+":"+key] = "bind-key -T " + table + " " + key + " " + strings.Join(command, " ")
	}
	return nil
}

func (f *fakeClient) KeyBinding(_ context.Context, table, key string) (string, bool, error) {
	f.check("key-binding")
	value, exists := f.bindings[table+":"+key]
	return value, exists, nil
}

func (f *fakeClient) SessionOption(_ context.Context, _, key string) (string, bool, error) {
	f.check("session-option")
	value, exists := f.sessionOptions[key]
	return value, exists, nil
}

func (f *fakeClient) CurrentWindow(_ context.Context, _ string) (string, error) {
	f.check("current-window")
	return f.currentWindow, nil
}

func (f *fakeClient) CurrentWindowName(_ context.Context, _ string) (string, error) {
	f.check("current-window-name")
	if f.currentWindowName != "" {
		return f.currentWindowName, nil
	}
	if window := f.windows[f.currentWindow]; window != nil {
		return window.Name, nil
	}
	return "", nil
}

func (f *fakeClient) ListClients(_ context.Context) ([]clientState, error) {
	f.check("list-clients")
	clients := make([]clientState, 0, len(f.clients))
	for _, client := range f.clients {
		clients = append(clients, *client)
	}
	return clients, nil
}

func (f *fakeClient) ClientSession(_ context.Context, clientName string) (string, error) {
	f.check("client-session")
	return f.clientSessions[clientName], nil
}

func (f *fakeClient) SwitchClient(_ context.Context, clientName, session string) error {
	f.check("switch-client")
	if !f.silent["switch-client"] {
		targetSession := session
		if f.session != nil && session == f.session.ID {
			targetSession = f.session.Name
		}
		f.clientSessions[clientName] = targetSession
		if client := f.clients[clientName]; client != nil && f.session != nil && targetSession == f.session.Name {
			for windowID, window := range f.windows {
				if !window.Active {
					continue
				}
				for _, pane := range f.panes[windowID] {
					if pane.Active {
						client.ActivePane = pane.ID
						return nil
					}
				}
			}
		}
	}
	return nil
}

func (f *fakeClient) Attach(_ context.Context, sessionID, windowID string, output, errorOutput io.Writer) error {
	f.check("attach")
	f.attachSession = sessionID
	f.attachWindow = windowID
	f.attachOutput = output
	f.attachError = errorOutput
	return nil
}

func executeAttachOrSwitch(ctx context.Context, manager *Manager) error {
	plan, err := manager.PrepareAttachOrSwitch(ctx)
	if err != nil {
		return err
	}
	return manager.ExecuteAttachOrSwitch(ctx, plan)
}

func (f *fakeClient) findPane(paneID string) (string, *paneState, error) {
	for windowID, panes := range f.panes {
		for _, pane := range panes {
			if pane.ID == paneID {
				return windowID, pane, nil
			}
		}
	}
	return "", nil, errors.New("pane not found")
}
