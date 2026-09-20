package state

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moutansos/op/internal/agents"
	"github.com/moutansos/op/internal/domain"
	"github.com/moutansos/op/internal/notify"
)

type fakeService struct {
	domain.Service
	projects                      []domain.Project
	tmux                          domain.TmuxSnapshot
	stats                         domain.StatsSnapshot
	projectErr, tmuxErr, statsErr error
}

func (f *fakeService) ListProjects(context.Context) ([]domain.Project, error) {
	return f.projects, f.projectErr
}
func (f *fakeService) GetTmuxSnapshot(context.Context) (domain.TmuxSnapshot, error) {
	return f.tmux, f.tmuxErr
}
func (f *fakeService) GetStatsSnapshot(context.Context) (domain.StatsSnapshot, error) {
	return f.stats, f.statsErr
}
func tracker(t *testing.T, f domain.Service, opts Options) *Tracker {
	t.Helper()
	if opts.InstanceID == "" {
		opts.InstanceID = "test/instance"
	}
	got, err := New(f, opts)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestSamplingRetainsFailuresAndRemovesOnSuccess(t *testing.T) {
	f := &fakeService{projects: []domain.Project{{ID: "repo", Path: t.TempDir()}}}
	f.tmux.Session = &domain.TmuxSession{ID: "$1", Windows: []domain.TmuxWindow{
		{ID: "@1", ProjectID: "repo", Profile: "dev", Panes: []domain.TmuxPane{{ID: "%1"}}},
		{ID: "@2", ProjectID: "repo", Profile: "review", Panes: []domain.TmuxPane{{ID: "%2"}}},
	}}
	f.stats.Agents = []domain.PaneAgentState{{PaneID: "%1", AgentName: "claude", Activity: domain.AgentActivityStarting}}
	x := tracker(t, f, Options{})
	x.sample(context.Background())
	s := x.Snapshot()
	if len(s.Tmux.Data) != 2 || s.Tmux.Data[0].Profile != "dev" || s.Tmux.Data[1].Profile != "review" {
		t.Fatalf("windows: %+v", s.Tmux)
	}
	if s.Agents.Data[0].Activity != domain.AgentActivityStarting || s.Agents.Data[0].ProjectID != s.Projects.Data[0].ID {
		t.Fatalf("agents: %+v", s.Agents)
	}
	// The existing service's detector supplies subsequent baseline samples.
	f.stats.Agents[0].Activity = domain.AgentActivityWorking
	x.sample(context.Background())
	if x.Snapshot().Agents.Data[0].Activity != domain.AgentActivityWorking {
		t.Fatal("baseline did not advance")
	}
	f.projectErr, f.tmuxErr = errors.New("catalog offline"), errors.New("tmux offline")
	f.stats.AgentsError = "capture unavailable"
	f.stats.Host.MemoryUsed = 123
	x.sample(context.Background())
	s = x.Snapshot()
	if !s.Projects.Stale || !s.Tmux.Stale || !s.Agents.Stale || len(s.Tmux.Data) != 2 || len(s.Projects.Data) != 1 {
		t.Fatalf("failure lost data: %+v", s)
	}
	if s.Agents.Data[0].Activity != domain.AgentActivityUnknown || s.Host.Stale || s.Host.Data.MemoryUsed != 123 {
		t.Fatalf("section isolation: %+v", s)
	}
	f.projectErr, f.tmuxErr = nil, nil
	f.projects, f.tmux.Session, f.stats.Agents = nil, nil, nil
	f.stats.AgentsError = ""
	x.sample(context.Background())
	s = x.Snapshot()
	if len(s.Projects.Data) != 0 || len(s.Tmux.Data) != 0 || len(s.Agents.Data) != 0 || s.Agents.Stale {
		t.Fatalf("removal: %+v", s)
	}
	if s.Revision <= 1 {
		t.Fatal("revision did not advance")
	}
}

func TestNativeCorrelationStalenessAndIsolation(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	if err := os.Mkdir(worktree, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	f := &fakeService{projects: []domain.Project{{ID: "repo", Path: root}, {ID: "wt", Path: worktree, Kind: domain.ProjectKindWorktree}}}
	x := tracker(t, f, Options{StaleAfter: time.Second})
	x.sample(context.Background())
	now := time.Now()
	o := notify.Observation{Source: notify.SourceOpenCode, SessionID: "same", ProjectID: "repo", ProjectDirectory: filepath.Join(alias, "worktree", "new-child"), Timestamp: now, Activity: domain.AgentActivityWorking}
	x.Observe(o)
	o.Source = notify.SourceClaudeCode
	x.Observe(o)
	s := x.Snapshot()
	if len(s.Agents.Data) != 2 || s.Agents.Data[0].ID == s.Agents.Data[1].ID {
		t.Fatalf("identity collision: %+v", s.Agents)
	}
	for _, a := range s.Agents.Data {
		if a.ProjectID != s.Projects.Data[1].ID || a.NativeProjectID != "repo" {
			t.Fatalf("wrong worktree correlation: %+v", a)
		}
	}
	// Caller mutation must not affect tracker state.
	s.Projects.Data[0].Path = "corrupted"
	if x.Snapshot().Projects.Data[0].Path != root {
		t.Fatal("snapshot aliases tracker")
	}
	o.Timestamp = now.Add(-time.Hour)
	o.Activity = domain.AgentActivityIdle
	x.Observe(o)
	for _, a := range x.Snapshot().Agents.Data {
		if a.Activity != domain.AgentActivityWorking {
			t.Fatal("older event overwrote state")
		}
	}
	x.mu.Lock()
	x.refreshLocked(now.Add(2 * time.Second))
	for _, a := range x.snapshot.Agents.Data {
		if !a.Stale || a.Terminated || a.Activity != domain.AgentActivityUnknown {
			t.Fatalf("stale != terminated: %+v", a)
		}
	}
	x.mu.Unlock()
	// A sibling with a common textual prefix is not an ancestor match.
	o.SessionID = "unrelated"
	o.Timestamp = time.Now()
	o.ProjectDirectory = root + "-sibling"
	x.Observe(o)
	for _, a := range x.Snapshot().Agents.Data {
		if a.NativeID == "unrelated" && a.ProjectID != "" {
			t.Fatal("matched sibling directory")
		}
	}
}

func TestNativeBoundedAndExplicitTermination(t *testing.T) {
	x := tracker(t, &fakeService{}, Options{})
	for i := 0; i < maxNativeSessions+10; i++ {
		x.Observe(notify.Observation{Source: notify.SourceCodex, SessionID: time.Unix(int64(i), 0).String()})
	}
	if len(x.native) != maxNativeSessions {
		t.Fatalf("native size = %d", len(x.native))
	}
	x.Observe(notify.Observation{SessionID: "done", Terminated: true})
	for _, a := range x.Snapshot().Agents.Data {
		if a.NativeID == "done" && !a.Terminated {
			t.Fatal("explicit termination missing")
		}
	}
}

func TestForwardRetriesStableBytesAndRecoversLatest(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	requests := make(chan int, 20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RequestURI() != "/configured/endpoint?key=yes" || r.Header.Get("Authorization") != "Bearer secret" {
			t.Errorf("incorrect request: %s %s", r.URL, r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, body)
		n := len(bodies)
		mu.Unlock()
		if n < 3 {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(204)
		}
		requests <- n
	}))
	defer server.Close()
	x := tracker(t, &fakeService{}, Options{ParentURL: server.URL + "/configured/endpoint?key=yes", ParentToken: "secret", HeartbeatInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go x.forward(ctx)
	x.Observe(notify.Observation{SessionID: "before"})
	select {
	case <-requests:
	case <-time.After(2 * time.Second):
		t.Fatal("initial send timed out")
	}
	for i := 0; i < 20; i++ {
		x.Observe(notify.Observation{SessionID: "after", Activity: domain.AgentActivityWorking})
	}
	for {
		select {
		case n := <-requests:
			if n >= 4 {
				goto recovered
			}
		case <-time.After(3 * time.Second):
			t.Fatal("recovery timed out")
		}
	}
recovered:
	mu.Lock()
	defer mu.Unlock()
	if string(bodies[0]) != string(bodies[1]) || string(bodies[1]) != string(bodies[2]) {
		t.Fatal("retry bytes changed")
	}
	var first, last Envelope
	if err := json.Unmarshal(bodies[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(bodies[3], &last); err != nil {
		t.Fatal(err)
	}
	if first.Version != 1 || first.Type != "state.snapshot" || first.EventID == last.EventID || last.Sequence <= first.Sequence || last.Sequence != last.Payload.Revision || len(last.Payload.Agents.Data) != 2 {
		t.Fatalf("bad recovery: %+v", last)
	}
}

type blockedService struct {
	domain.Service
	started chan struct{}
	release chan struct{}
}

func (f *blockedService) ListProjects(context.Context) ([]domain.Project, error) {
	close(f.started)
	<-f.release
	return nil, nil
}
func (f *blockedService) GetTmuxSnapshot(context.Context) (domain.TmuxSnapshot, error) {
	return domain.TmuxSnapshot{}, nil
}
func (f *blockedService) GetStatsSnapshot(context.Context) (domain.StatsSnapshot, error) {
	return domain.StatsSnapshot{}, nil
}

func TestRunCancelsBlockedSamplerAndHeartbeat(t *testing.T) {
	f := &blockedService{started: make(chan struct{}), release: make(chan struct{})}
	defer close(f.release)
	x := tracker(t, f, Options{HeartbeatInterval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- x.Run(ctx) }()
	<-f.started
	deadline := time.After(time.Second)
	for x.Snapshot().Revision == 0 {
		select {
		case <-deadline:
			t.Fatal("blocked sampler stopped heartbeat")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run did not cancel")
	}
}

func TestNewValidationAndEpoch(t *testing.T) {
	if _, err := New(&fakeService{}, Options{}); err == nil {
		t.Fatal("missing ID accepted")
	}
	if _, err := New(&fakeService{}, Options{InstanceID: "ok", ParentURL: "relative"}); err == nil {
		t.Fatal("relative URL accepted")
	}
	a, b := tracker(t, &fakeService{}, Options{}), tracker(t, &fakeService{}, Options{})
	if a.Snapshot().Epoch == b.Snapshot().Epoch {
		t.Fatal("epoch reused")
	}
	if !a.Snapshot().Projects.Stale || a.Snapshot().Projects.Available {
		t.Fatal("unsampled section marked available")
	}
}

type detectorService struct {
	fakeService
	detector *agents.Detector
	calls    atomic.Int32
}

func (s *detectorService) CapturePane(context.Context, string) (string, error) {
	if s.calls.Load() == 1 {
		return "Initial screen", nil
	}
	return "Task output", nil
}

func (s *detectorService) GetStatsSnapshot(ctx context.Context) (domain.StatsSnapshot, error) {
	s.calls.Add(1)
	return domain.StatsSnapshot{Agents: s.detector.Classify(ctx, time.Now(), []agents.Pane{{PaneID: "%1", Command: "claude", Foreground: agents.Foreground{PID: 123, Command: "claude", Valid: true}}}, s)}, nil
}

func TestRunMaintainsRealDetectorBaselineWithoutDashboard(t *testing.T) {
	d, err := agents.New(agents.Options{QuietAfter: time.Millisecond, IdleAfter: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	f := &detectorService{detector: d}
	x := tracker(t, f, Options{RefreshInterval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- x.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for {
		s := x.Snapshot()
		if len(s.Agents.Data) == 1 && s.Agents.Data[0].Activity == domain.AgentActivityIdle {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("detector did not settle: %+v", s.Agents)
		case <-time.After(5 * time.Millisecond):
		}
	}
	if f.calls.Load() < 3 {
		t.Fatal("detector was not repeatedly sampled")
	}
	cancel()
	<-done
}

func TestNativeResolutionPreservesCorrelationAndClearsAttention(t *testing.T) {
	x := tracker(t, &fakeService{}, Options{})
	now := time.Now().Add(-time.Second)
	x.Observe(notify.Observation{Source: notify.SourceClaudeCode, SessionID: "s", ProjectID: "native-project", ProjectDirectory: "/project", Timestamp: now, Activity: domain.AgentActivityPermissionRequired, Detail: "Allow?", Notification: &notify.Notification{Type: notify.TypePermission}})
	x.Observe(notify.Observation{Source: notify.SourceClaudeCode, SessionID: "s", Timestamp: now.Add(time.Millisecond), Activity: domain.AgentActivityWorking})
	a := x.Snapshot().Agents.Data[0]
	if a.NativeProjectID != "native-project" || a.Directory != "/project" || a.Notification != nil || a.Detail != "" || a.Activity != domain.AgentActivityWorking {
		t.Fatalf("bad resolution: %+v", a)
	}
	y := tracker(t, &fakeService{}, Options{InstanceID: "second"})
	y.Observe(notify.Observation{Source: notify.SourceClaudeCode, SessionID: "s"})
	if a.ID == y.Snapshot().Agents.Data[0].ID {
		t.Fatal("identical local session IDs collided across instances")
	}
}
