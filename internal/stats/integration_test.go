package stats

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/moutansos/op/internal/agents"
	"github.com/moutansos/op/internal/domain"
	tmuxmanager "github.com/moutansos/op/internal/tmux"
)

// TestIntegrationAgentDetection classifies the agents in the caller's own tmux
// server. It is a diagnostic rather than an assertion of behavior: the session
// it inspects is whatever the operator happens to be running, so it verifies
// only that the pipeline produces a plausible classification for every pane and
// prints the result for inspection.
//
// Run with: OP_TMUX_INTEGRATION=1 go test ./internal/stats -run TestIntegration -v
func TestIntegrationAgentDetection(t *testing.T) {
	if os.Getenv("OP_TMUX_INTEGRATION") != "1" {
		t.Skip("set OP_TMUX_INTEGRATION=1 to inspect the live tmux server")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	session := os.Getenv("OP_TMUX_SESSION")
	if session == "" {
		session = "code"
	}

	config := tmuxmanager.ManagerConfig{
		Session:          session,
		DashboardWindow:  "op",
		StartDirectory:   "/tmp",
		DashboardCommand: "op dashboard",
		EditorCommand:    "nvim .",
		PreferredShell:   "bash",
		ShellPaneRows:    20,
		DefaultProfile:   "nvim",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	snapshot, err := tmuxmanager.ReadSnapshot(ctx, config)
	if err != nil {
		t.Fatalf("ReadSnapshot() error = %v", err)
	}
	if snapshot.Session == nil {
		t.Skipf("tmux session %q is not running", session)
	}

	capturer, err := tmuxmanager.NewPaneCapturer(config)
	if err != nil {
		t.Fatalf("NewPaneCapturer() error = %v", err)
	}
	detector, err := agents.New(agents.Options{})
	if err != nil {
		t.Fatalf("agents.New() error = %v", err)
	}
	collector := NewCollector(Options{Detector: detector, Capturer: capturer})

	// Quiescence needs a baseline, so the first sample can only report
	// "starting". Sample twice, spaced far enough apart that a working agent has
	// time to repaint.
	if _, err := collector.Collect(ctx, snapshot); err != nil {
		t.Fatalf("first Collect() error = %v", err)
	}
	time.Sleep(3 * time.Second)
	result, err := collector.Collect(ctx, snapshot)
	if err != nil {
		t.Fatalf("second Collect() error = %v", err)
	}

	t.Logf("pane tree (%d panes):", len(result.Processes))
	for _, process := range result.Processes {
		t.Logf("  %-6s %-14s root=%-7d fg=%-7d %-12s procs=%d",
			process.PaneID, truncateForLog(process.WindowName, 14), process.RootPID,
			process.ForegroundPID, truncateForLog(process.ForegroundCommand, 12), process.ProcessCount)
	}

	t.Logf("agents (%d):", len(result.Agents))
	for _, agent := range result.Agents {
		t.Logf("  %-6s %-10s %-18s quiet=%3ds detail=%q",
			agent.PaneID, agent.AgentName, agent.Activity.String(), agent.QuietSeconds,
			truncateForLog(agent.Detail, 60))

		switch agent.Activity {
		case domain.AgentActivityUnknown,
			domain.AgentActivityStarting,
			domain.AgentActivityWorking,
			domain.AgentActivityAwaitingInput,
			domain.AgentActivityPermissionRequired,
			domain.AgentActivityAwaitingApproval,
			domain.AgentActivityIdle:
		default:
			t.Errorf("pane %s produced an unrecognized activity %q", agent.PaneID, agent.Activity)
		}
		if agent.AgentName == "" {
			t.Errorf("pane %s was classified without an agent name", agent.PaneID)
		}
	}
}

func truncateForLog(value string, width int) string {
	value = strings.ReplaceAll(value, "\n", " ")
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	return string(runes[:width])
}
