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

func TestCollectorFindsAgentsAmongAPanesWindowsProcesses(t *testing.T) {
	detector, err := agents.New(agents.Options{})
	if err != nil {
		t.Fatal(err)
	}
	started := time.Unix(1_700_000_000, 0)
	server := fakeProcess(50, started, 1, 0)
	windowsShell := fakeProcess(100, started, 1, 0)
	linuxAgent := fakeProcess(200, started, 1, 0)
	plainShell := fakeProcess(300, started, 1, 0)
	server.children = []*fakeProcessMetrics{windowsShell, linuxAgent, plainShell}
	clock := &fakeClock{times: []time.Time{started.Add(10 * time.Second)}}
	collector := newCollector(fakeHostMetrics{}, fakeProcessFactory{processes: map[int32]*fakeProcessMetrics{50: server}}, clock.Now, 20)
	collector.detector, collector.capturer = detector, staticCapture("ready")
	collector.foreground = func(pid int32) agents.Foreground {
		names := map[int32]string{100: "pwsh.exe", 200: "claude", 300: "zsh"}
		return agents.Foreground{PID: pid, Command: names[pid], Valid: true}
	}
	collector.windows = fakeWindowsSource{sample: windowsHostSample{Processes: []windowsProcess{
		{PID: 7000, ParentPID: 1, Created: 1, Name: "pwsh.exe", Pane: "%1", ServerPID: 50},
		{PID: 7001, ParentPID: 7000, Created: 2, Name: "opencode.exe", Pane: "%1", ServerPID: 50},
		{PID: 7002, ParentPID: 7001, Created: 3, Name: "opencode.exe", Pane: "%1", ServerPID: 50},
		// A Linux agent pane keeps its own foreground even with Windows children.
		{PID: 8000, ParentPID: 1, Created: 1, Name: "codex.exe", Pane: "%2", ServerPID: 50},
		{PID: 9000, ParentPID: 1, Created: 1, Name: "git.exe", Pane: "%3", ServerPID: 50},
	}}}

	snapshot, err := collector.Collect(context.Background(), tmuxSnapshot(
		domain.TmuxPane{ID: "%1", PID: 100, CurrentCommand: "init"},
		domain.TmuxPane{ID: "%2", PID: 200, CurrentCommand: "claude"},
		domain.TmuxPane{ID: "%3", PID: 300, CurrentCommand: "zsh"},
	))
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	found := make(map[string]domain.PaneAgentState)
	for _, state := range snapshot.Agents {
		found[state.PaneID] = state
	}
	if windows := found["%1"]; windows.AgentName != "opencode" || windows.ForegroundPID != 7001 || windows.ForegroundCommand != "opencode.exe" {
		t.Errorf("Windows agent pane = %+v, want opencode at the outermost opencode.exe", windows)
	}
	if linux := found["%2"]; linux.AgentName != "claude" || linux.ForegroundPID != 200 {
		t.Errorf("Linux agent pane = %+v, want claude at its Linux foreground", linux)
	}
	if shell, ok := found["%3"]; ok || len(snapshot.Agents) != 2 {
		t.Errorf("plain shell pane was classified: %+v (agents %d)", shell, len(snapshot.Agents))
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
