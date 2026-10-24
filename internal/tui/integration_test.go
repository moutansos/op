package tui

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/moutansos/op/internal/agents"
	"github.com/moutansos/op/internal/stats"
	tmuxmanager "github.com/moutansos/op/internal/tmux"
)

// TestIntegrationRenderLiveTree renders the caller's own tmux server through the
// real collector and prints the resulting tree. It exists so the panel can be
// eyeballed against a session that actually has agents in it, which no fixture
// can stand in for.
//
// Run with: OP_TMUX_INTEGRATION=1 go test ./internal/tui -run TestIntegration -v
func TestIntegrationRenderLiveTree(t *testing.T) {
	if os.Getenv("OP_TMUX_INTEGRATION") != "1" {
		t.Skip("set OP_TMUX_INTEGRATION=1 to render the live tmux server")
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
	collector := stats.NewCollector(stats.Options{Detector: detector, Capturer: capturer})

	// Quiescence needs a baseline, so classification only becomes meaningful on
	// the second sample.
	if _, err := collector.Collect(ctx, snapshot); err != nil {
		t.Fatalf("first Collect() error = %v", err)
	}
	time.Sleep(3 * time.Second)
	sample, err := collector.Collect(ctx, snapshot)
	if err != nil {
		t.Fatalf("second Collect() error = %v", err)
	}

	model := Model{haveTmux: true, tmux: snapshot, haveStats: true, stats: sample}
	t.Logf("\n%s", model.renderTmux(96, 0))
	if summary := model.waitingAgentSummary(); summary != "" {
		t.Logf("status line: %s", summary)
	}
}
