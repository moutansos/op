package stats

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/moutansos/op/internal/domain"
)

func TestCollectorAggregatesProcessTreeAndCPUDeltas(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	root := fakeProcess(100, started, 10, 1.0)
	childA := fakeProcess(101, started.Add(time.Second), 20, 2.0)
	childB := fakeProcess(102, started.Add(2*time.Second), 30, 3.0)
	grandchild := fakeProcess(103, started.Add(3*time.Second), 40, 4.0)
	root.children = []*fakeProcessMetrics{childA, childB}
	childA.children = []*fakeProcessMetrics{grandchild}
	childB.children = []*fakeProcessMetrics{grandchild}

	clock := &fakeClock{times: []time.Time{started.Add(10 * time.Second), started.Add(12 * time.Second)}}
	collector := newCollector(fakeHostMetrics{}, fakeProcessFactory{processes: map[int32]*fakeProcessMetrics{100: root}}, clock.Now, 20)
	tmux := tmuxSnapshot(domain.TmuxPane{ID: "%1", PID: 100, CurrentCommand: "nvim"})

	first, err := collector.Collect(context.Background(), tmux)
	if err != nil {
		t.Fatalf("first Collect() error = %v", err)
	}
	assertSingleProcess(t, first, domain.PaneProcessStats{
		WindowName:    "project",
		PaneID:        "%1",
		RootPID:       100,
		Command:       "nvim",
		ResidentBytes: 100,
		UptimeSeconds: 10,
	})
	if first.Processes[0].CPUAvailable {
		t.Fatal("first process CPU sample should be unavailable")
	}

	root.cpu = 1.5
	childA.cpu = 2.25
	childB.cpu = 4.0
	grandchild.cpu = 4.25
	second, err := collector.Collect(context.Background(), tmux)
	if err != nil {
		t.Fatalf("second Collect() error = %v", err)
	}
	want := domain.PaneProcessStats{
		WindowName:    "project",
		PaneID:        "%1",
		RootPID:       100,
		Command:       "nvim",
		CPUPercent:    100,
		CPUAvailable:  true,
		ResidentBytes: 100,
		UptimeSeconds: 12,
	}
	assertSingleProcess(t, second, want)
	if grandchild.timesCalls != 2 {
		t.Fatalf("shared descendant Times() calls = %d, want 2", grandchild.timesCalls)
	}
}

func TestCollectorDoesNotBridgeCPUAcrossDisappearance(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	root := fakeProcess(200, started, 10, 1)
	child := fakeProcess(201, started, 20, 1)
	root.children = []*fakeProcessMetrics{child}
	clock := &fakeClock{times: []time.Time{
		started.Add(10 * time.Second),
		started.Add(11 * time.Second),
		started.Add(12 * time.Second),
	}}
	collector := newCollector(fakeHostMetrics{}, fakeProcessFactory{processes: map[int32]*fakeProcessMetrics{200: root}}, clock.Now, 10)
	tmux := tmuxSnapshot(domain.TmuxPane{ID: "%2", PID: 200})

	if _, err := collector.Collect(context.Background(), tmux); err != nil {
		t.Fatalf("first Collect() error = %v", err)
	}
	root.cpu = 1.5
	child.timesErr = errProcessGone
	second, err := collector.Collect(context.Background(), tmux)
	if err != nil {
		t.Fatalf("second Collect() error = %v", err)
	}
	if got := second.Processes[0].CPUPercent; got != 50 {
		t.Fatalf("CPU after disappearance = %v, want 50", got)
	}
	if !second.Processes[0].CPUAvailable {
		t.Fatal("CPU after disappearance should be available from the surviving process")
	}

	root.cpu = 2
	child.cpu = 10
	child.timesErr = nil
	third, err := collector.Collect(context.Background(), tmux)
	if err != nil {
		t.Fatalf("third Collect() error = %v", err)
	}
	if got := third.Processes[0].CPUPercent; got != 50 {
		t.Fatalf("CPU after reappearance = %v, want 50", got)
	}
	if !third.Processes[0].CPUAvailable {
		t.Fatal("CPU after reappearance should be available")
	}
}

func TestCollectorDoesNotBridgeCPUAcrossPIDReuse(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	root := fakeProcess(250, started, 10, 1)
	clock := &fakeClock{times: []time.Time{
		started.Add(10 * time.Second),
		started.Add(11 * time.Second),
		started.Add(12 * time.Second),
	}}
	collector := newCollector(fakeHostMetrics{}, fakeProcessFactory{processes: map[int32]*fakeProcessMetrics{250: root}}, clock.Now, 10)
	tmux := tmuxSnapshot(domain.TmuxPane{ID: "%reuse", PID: 250})

	if _, err := collector.Collect(context.Background(), tmux); err != nil {
		t.Fatalf("first Collect() error = %v", err)
	}
	root.created = started.Add(500 * time.Millisecond).UnixMilli()
	root.cpu = 10
	reused, err := collector.Collect(context.Background(), tmux)
	if err != nil {
		t.Fatalf("reused Collect() error = %v", err)
	}
	if reused.Processes[0].CPUAvailable {
		t.Fatalf("CPU for reused PID = %v%%, want unavailable", reused.Processes[0].CPUPercent)
	}

	root.cpu = 10.5
	later, err := collector.Collect(context.Background(), tmux)
	if err != nil {
		t.Fatalf("later Collect() error = %v", err)
	}
	if !later.Processes[0].CPUAvailable || later.Processes[0].CPUPercent != 50 {
		t.Fatalf("later CPU = %v%% available=%v, want 50%% available", later.Processes[0].CPUPercent, later.Processes[0].CPUAvailable)
	}
}

func TestCollectorEnumeratesProcessesOncePerSnapshot(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	firstRoot := fakeProcess(600, started, 1, 1)
	secondRoot := fakeProcess(700, started, 1, 1)
	for pid := int32(601); pid < 700; pid++ {
		firstRoot.children = append(firstRoot.children, fakeProcess(pid, started, 1, 1))
	}
	var snapshotCalls int
	factory := fakeProcessFactory{
		processes:     map[int32]*fakeProcessMetrics{600: firstRoot, 700: secondRoot},
		snapshotCalls: &snapshotCalls,
	}
	collector := newCollector(fakeHostMetrics{}, factory, func() time.Time { return started.Add(time.Minute) }, 50)

	_, err := collector.Collect(context.Background(), tmuxSnapshot(
		domain.TmuxPane{ID: "%11", PID: 600},
		domain.TmuxPane{ID: "%12", PID: 700},
	))
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if snapshotCalls != 1 {
		t.Fatalf("process enumerations = %d, want exactly 1", snapshotCalls)
	}
}

func TestCollectorBoundsBreadthFirstTraversal(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	root := fakeProcess(1, started, 1, 1)
	childA := fakeProcess(2, started, 2, 2)
	childB := fakeProcess(3, started, 4, 3)
	childC := fakeProcess(4, started, 8, 4)
	grandchild := fakeProcess(5, started, 16, 5)
	root.children = []*fakeProcessMetrics{childA, childB, childC}
	childA.children = []*fakeProcessMetrics{grandchild}

	collector := newCollector(
		fakeHostMetrics{},
		fakeProcessFactory{processes: map[int32]*fakeProcessMetrics{1: root}},
		func() time.Time { return started.Add(time.Minute) },
		3,
	)
	snapshot, err := collector.Collect(context.Background(), tmuxSnapshot(domain.TmuxPane{ID: "%3", PID: 1}))
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if got := snapshot.Processes[0].ResidentBytes; got != 7 {
		t.Fatalf("ResidentBytes = %d, want root and first two children (7)", got)
	}
	if childC.timesCalls != 0 || grandchild.timesCalls != 0 {
		t.Fatalf("processes beyond bound were sampled: child=%d grandchild=%d", childC.timesCalls, grandchild.timesCalls)
	}
}

func TestCollectorToleratesProcessFailures(t *testing.T) {
	permissionDenied := errors.New("permission denied")
	started := time.Unix(1_700_000_000, 0)
	vanished := fakeProcess(300, started, 0, 0)
	vanished.createErr = errProcessGone
	vanished.rssErr = errProcessGone
	vanished.timesErr = errProcessGone
	vanished.childrenErr = errProcessGone
	inaccessible := fakeProcess(301, started, 0, 0)
	inaccessible.createErr = permissionDenied
	inaccessible.rssErr = permissionDenied
	inaccessible.timesErr = permissionDenied
	inaccessible.childrenErr = permissionDenied

	factory := fakeProcessFactory{
		processes:  map[int32]*fakeProcessMetrics{300: vanished, 301: inaccessible},
		openErrors: map[int32]error{302: errProcessGone, 303: permissionDenied},
	}
	collector := newCollector(fakeHostMetrics{}, factory, func() time.Time { return started.Add(time.Minute) }, 10)
	tmux := tmuxSnapshot(
		domain.TmuxPane{ID: "%4", PID: 300},
		domain.TmuxPane{ID: "%5", PID: 301},
		domain.TmuxPane{ID: "%6", PID: 302},
		domain.TmuxPane{ID: "%7", PID: 303},
		domain.TmuxPane{ID: "%8", PID: 304, Dead: true},
	)

	snapshot, err := collector.Collect(context.Background(), tmux)
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	wantDead := []bool{true, false, true, false, true}
	for i, want := range wantDead {
		if got := snapshot.Processes[i].Dead; got != want {
			t.Errorf("Processes[%d].Dead = %v, want %v", i, got, want)
		}
	}
}

func TestCollectorToleratesProcessEnumerationFailure(t *testing.T) {
	permissionDenied := errors.New("permission denied")
	collector := newCollector(
		fakeHostMetrics{},
		fakeProcessFactory{snapshotErr: permissionDenied},
		time.Now,
		10,
	)

	snapshot, err := collector.Collect(context.Background(), tmuxSnapshot(domain.TmuxPane{ID: "%inaccessible", PID: 800}))
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if snapshot.Processes[0].Dead {
		t.Fatal("permission failure marked pane process dead")
	}
	if snapshot.Processes[0].CPUAvailable {
		t.Fatal("permission failure produced an available CPU sample")
	}
}

func TestCollectorCopiesHostValuesAndToleratesUnsupportedMetrics(t *testing.T) {
	capturedAt := time.Unix(1_700_000_000, 123)
	host := fakeHostMetrics{
		cpu:    37.5,
		used:   4 << 30,
		total:  16 << 30,
		load:   [3]float64{1.25, 0.75, 0.5},
		uptime: 9876,
	}
	collector := newCollector(host, fakeProcessFactory{}, func() time.Time { return capturedAt }, 10)

	snapshot, err := collector.Collect(context.Background(), domain.TmuxSnapshot{})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if snapshot.CapturedAt != capturedAt {
		t.Fatalf("CapturedAt = %v, want %v", snapshot.CapturedAt, capturedAt)
	}
	want := domain.HostStats{
		CPUPercent:    37.5,
		MemoryUsed:    4 << 30,
		MemoryTotal:   16 << 30,
		LoadAverage:   [3]float64{1.25, 0.75, 0.5},
		UptimeSeconds: 9876,
	}
	if snapshot.Host != want {
		t.Fatalf("Host = %#v, want %#v", snapshot.Host, want)
	}

	unsupported := errors.New("not supported")
	collector = newCollector(fakeHostMetrics{
		cpu:       math.NaN(),
		memoryErr: unsupported,
		loadErr:   unsupported,
		uptimeErr: unsupported,
	}, fakeProcessFactory{}, func() time.Time { return capturedAt }, 10)
	snapshot, err = collector.Collect(context.Background(), domain.TmuxSnapshot{})
	if err != nil {
		t.Fatalf("Collect() with unsupported metrics error = %v", err)
	}
	if snapshot.Host != (domain.HostStats{}) {
		t.Fatalf("unsupported Host = %#v, want zero value", snapshot.Host)
	}
}

func TestCollectorSnapshotsDoNotShareMutableStorage(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	root := fakeProcess(400, started, 10, 1)
	collector := newCollector(
		fakeHostMetrics{},
		fakeProcessFactory{processes: map[int32]*fakeProcessMetrics{400: root}},
		func() time.Time { return started.Add(time.Minute) },
		10,
	)
	tmux := tmuxSnapshot(domain.TmuxPane{ID: "%9", PID: 400, CurrentCommand: "shell"})

	first, err := collector.Collect(context.Background(), tmux)
	if err != nil {
		t.Fatalf("first Collect() error = %v", err)
	}
	first.Processes[0].Command = "mutated"
	first.Processes = append(first.Processes, domain.PaneProcessStats{PaneID: "fake"})

	second, err := collector.Collect(context.Background(), tmux)
	if err != nil {
		t.Fatalf("second Collect() error = %v", err)
	}
	if len(second.Processes) != 1 || second.Processes[0].Command != "shell" {
		t.Fatalf("second Processes = %#v; prior snapshot mutation leaked", second.Processes)
	}
}

func TestCollectorConcurrentCallsAreSafe(t *testing.T) {
	started := time.Unix(1_700_000_000, 0)
	root := fakeProcess(500, started, 10, 1)
	collector := newCollector(
		fakeHostMetrics{},
		fakeProcessFactory{processes: map[int32]*fakeProcessMetrics{500: root}},
		func() time.Time { return started.Add(time.Minute) },
		10,
	)
	tmux := tmuxSnapshot(domain.TmuxPane{ID: "%10", PID: 500})

	const calls = 32
	var wg sync.WaitGroup
	errs := make(chan error, calls)
	for range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshot, err := collector.Collect(context.Background(), tmux)
			if err == nil && len(snapshot.Processes) != 1 {
				err = errors.New("snapshot did not contain one pane")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestCollectorReturnsContextCancellationWithoutAdvancingSample(t *testing.T) {
	collector := newCollector(fakeHostMetrics{}, fakeProcessFactory{}, time.Now, 10)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := collector.Collect(ctx, domain.TmuxSnapshot{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Collect() error = %v, want context.Canceled", err)
	}
	if !collector.previousProcessSampleAt.IsZero() {
		t.Fatal("canceled collection advanced sample state")
	}
}

type fakeHostMetrics struct {
	cpu       float64
	used      uint64
	total     uint64
	load      [3]float64
	uptime    uint64
	cpuErr    error
	memoryErr error
	loadErr   error
	uptimeErr error
}

func (h fakeHostMetrics) CPUPercent(context.Context) (float64, error) {
	return h.cpu, h.cpuErr
}

func (h fakeHostMetrics) VirtualMemory(context.Context) (uint64, uint64, error) {
	return h.used, h.total, h.memoryErr
}

func (h fakeHostMetrics) LoadAverage(context.Context) ([3]float64, error) {
	return h.load, h.loadErr
}

func (h fakeHostMetrics) Uptime(context.Context) (uint64, error) {
	return h.uptime, h.uptimeErr
}

type fakeProcessFactory struct {
	processes     map[int32]*fakeProcessMetrics
	openErrors    map[int32]error
	snapshotCalls *int
	snapshotErr   error
}

func (f fakeProcessFactory) Snapshot(ctx context.Context) (processSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.snapshotCalls != nil {
		(*f.snapshotCalls)++
	}
	if f.snapshotErr != nil {
		return nil, f.snapshotErr
	}
	processes := make(map[int32]*fakeProcessMetrics)
	queue := make([]*fakeProcessMetrics, 0, len(f.processes))
	for _, proc := range f.processes {
		queue = append(queue, proc)
	}
	for len(queue) > 0 {
		proc := queue[0]
		queue = queue[1:]
		if _, ok := processes[proc.pid]; ok {
			continue
		}
		processes[proc.pid] = proc
		queue = append(queue, proc.children...)
	}
	return fakeProcessSnapshot{processes: processes, openErrors: f.openErrors}, nil
}

type fakeProcessSnapshot struct {
	processes  map[int32]*fakeProcessMetrics
	openErrors map[int32]error
}

func (f fakeProcessSnapshot) Open(pid int32) (processMetrics, error) {
	if err := f.openErrors[pid]; err != nil {
		return nil, err
	}
	proc, ok := f.processes[pid]
	if !ok {
		return nil, errProcessGone
	}
	return proc, nil
}

func (f fakeProcessSnapshot) Children(pid int32) []processMetrics {
	proc, ok := f.processes[pid]
	if !ok {
		return nil
	}
	children := make([]processMetrics, len(proc.children))
	for i, child := range proc.children {
		children[i] = child
	}
	return children
}

type fakeProcessMetrics struct {
	pid         int32
	created     int64
	rss         uint64
	cpu         float64
	children    []*fakeProcessMetrics
	createErr   error
	rssErr      error
	timesErr    error
	childrenErr error
	timesCalls  int
}

func fakeProcess(pid int32, created time.Time, rss uint64, cpu float64) *fakeProcessMetrics {
	return &fakeProcessMetrics{pid: pid, created: created.UnixMilli(), rss: rss, cpu: cpu}
}

func (p *fakeProcessMetrics) PID() int32 {
	return p.pid
}

func (p *fakeProcessMetrics) Times(context.Context) (float64, float64, error) {
	p.timesCalls++
	return p.cpu, 0, p.timesErr
}

func (p *fakeProcessMetrics) RSS(context.Context) (uint64, error) {
	return p.rss, p.rssErr
}

func (p *fakeProcessMetrics) CreateTime(context.Context) (int64, error) {
	return p.created, p.createErr
}

type fakeClock struct {
	times []time.Time
	next  int
}

func (c *fakeClock) Now() time.Time {
	result := c.times[c.next]
	c.next++
	return result
}

func tmuxSnapshot(panes ...domain.TmuxPane) domain.TmuxSnapshot {
	return domain.TmuxSnapshot{Session: &domain.TmuxSession{Windows: []domain.TmuxWindow{{
		Name:  "project",
		Panes: panes,
	}}}}
}

func assertSingleProcess(t *testing.T, snapshot domain.StatsSnapshot, want domain.PaneProcessStats) {
	t.Helper()
	if len(snapshot.Processes) != 1 {
		t.Fatalf("len(Processes) = %d, want 1", len(snapshot.Processes))
	}
	if snapshot.Processes[0] != want {
		t.Fatalf("Processes[0] = %#v, want %#v", snapshot.Processes[0], want)
	}
}
