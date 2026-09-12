package stats

import (
	"context"
	"testing"
	"time"

	"github.com/moutansos/op/internal/agents"
	"github.com/moutansos/op/internal/domain"
)

type staticCapture string

func (s staticCapture) CapturePane(context.Context, string) (string, error) { return string(s), nil }

func TestCollectorReportsUnavailableDetection(t *testing.T) {
	c := newCollector(fakeHostMetrics{}, fakeProcessFactory{}, time.Now, 10)
	snapshot, err := c.Collect(context.Background(), domain.TmuxSnapshot{})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.AgentsError == "" {
		t.Fatal("disabled detection reported as a successful empty sample")
	}
}

func TestCollectorForgetsBaselineWhenTmuxSessionDisappears(t *testing.T) {
	detector, err := agents.New(agents.Options{})
	if err != nil {
		t.Fatal(err)
	}
	c := &Collector{detector: detector, capturer: staticCapture("Hello")}
	now := time.Now()
	tmux := tmuxSnapshot(domain.TmuxPane{ID: "%1", PID: 123, CurrentCommand: "claude"})
	foregrounds := map[string]agents.Foreground{"%1": {PID: 123, Command: "claude", Valid: true}}
	states := c.collectAgents(context.Background(), now, tmux, foregrounds)
	if len(states) != 1 {
		t.Fatalf("states: %+v", states)
	}
	c.capturer = staticCapture("New output")
	c.collectAgents(context.Background(), now.Add(time.Second), tmux, foregrounds)
	c.collectAgents(context.Background(), now.Add(2*time.Second), domain.TmuxSnapshot{}, nil)
	states = c.collectAgents(context.Background(), now.Add(3*time.Second), tmux, foregrounds)
	if len(states) != 1 || states[0].Activity != domain.AgentActivityStarting {
		t.Fatalf("reused pane inherited old baseline: %+v", states)
	}
}
