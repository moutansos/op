package gitstatus

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/moutansos/op/internal/domain"
	gitrepo "github.com/moutansos/op/internal/git"
)

type fakeProber struct {
	mu     sync.Mutex
	calls  map[string]int
	states map[string]gitrepo.State
	errs   map[string]error
	block  chan struct{}
}

func newFakeProber() *fakeProber {
	return &fakeProber{calls: map[string]int{}, states: map[string]gitrepo.State{}, errs: map[string]error{}}
}

func (f *fakeProber) State(ctx context.Context, path string) (gitrepo.State, error) {
	f.mu.Lock()
	f.calls[path]++
	state, err, block := f.states[path], f.errs[path], f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return gitrepo.State{Git: domain.GitStateUnknown}, ctx.Err()
		}
	}
	return state, err
}

func (f *fakeProber) callCount(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[path]
}

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func TestRefreshProbesOnlyMissingOrStaleEntries(t *testing.T) {
	prober := newFakeProber()
	prober.states["/repos/a"] = gitrepo.State{Branch: "main", Git: domain.GitStateClean}
	prober.states["/repos/b"] = gitrepo.State{Branch: "dev", Git: domain.GitStateDirty}
	now := &clock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	cache := New(prober, Options{Now: now.Now})
	paths := []string{"/repos/a", "/repos/b"}

	cache.Refresh(context.Background(), paths, time.Minute)
	now.Advance(30 * time.Second)
	cache.Refresh(context.Background(), paths, time.Minute)
	if prober.callCount("/repos/a") != 1 || prober.callCount("/repos/b") != 1 {
		t.Fatalf("fresh entries were probed again: %v", prober.calls)
	}
	now.Advance(30 * time.Second)
	cache.Refresh(context.Background(), paths, time.Minute)
	if prober.callCount("/repos/a") != 2 {
		t.Fatalf("stale entry was not probed again: %v", prober.calls)
	}

	entries := cache.Lookup(append(paths, "/repos/missing"))
	if len(entries) != 2 || entries["/repos/b"].Branch != "dev" || entries["/repos/b"].Git != domain.GitStateDirty {
		t.Fatalf("Lookup() = %#v", entries)
	}
}

func TestRefreshRecordsFailuresAsUnknown(t *testing.T) {
	prober := newFakeProber()
	prober.errs["/repos/broken"] = errors.New("fatal: bad object")
	cache := New(prober, Options{})
	cache.Refresh(context.Background(), []string{"/repos/broken"}, time.Minute)
	entry, found := cache.Lookup([]string{"/repos/broken"})["/repos/broken"]
	if !found || entry.Git != domain.GitStateUnknown || entry.CheckedAt.IsZero() {
		t.Fatalf("failed probe entry = %#v, %v", entry, found)
	}
}

func TestRefreshStopsAtDeadlineAndKeepsCompletedResults(t *testing.T) {
	prober := newFakeProber()
	prober.block = make(chan struct{})
	cache := New(prober, Options{Concurrency: 2})
	cache.Record("/repos/known", gitrepo.State{Branch: "main", Git: domain.GitStateClean})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	cache.Refresh(ctx, []string{"/repos/slow-1", "/repos/slow-2", "/repos/slow-3"}, time.Minute)
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("Refresh() ignored its deadline for %s", elapsed)
	}
	entries := cache.Lookup([]string{"/repos/known", "/repos/slow-1"})
	if _, found := entries["/repos/slow-1"]; found {
		t.Fatalf("interrupted probe was recorded: %#v", entries)
	}
	if entries["/repos/known"].Git != domain.GitStateClean {
		t.Fatalf("existing entry lost: %#v", entries)
	}
}

func TestCachesShareStateThroughTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "op", "git-status.json")
	firstProber := newFakeProber()
	firstProber.states["/repos/a"] = gitrepo.State{Branch: "main", Git: domain.GitStateClean}
	first := New(firstProber, Options{Path: path})
	first.Refresh(context.Background(), []string{"/repos/a"}, time.Minute)

	secondProber := newFakeProber()
	second := New(secondProber, Options{Path: path})
	second.Refresh(context.Background(), []string{"/repos/a"}, time.Minute)
	if secondProber.callCount("/repos/a") != 0 {
		t.Fatal("second process probed a repository the first had just refreshed")
	}
	if entry := second.Lookup([]string{"/repos/a"})["/repos/a"]; entry.Branch != "main" || entry.Git != domain.GitStateClean {
		t.Fatalf("shared entry = %#v", entry)
	}

	second.Record("/repos/b", gitrepo.State{Branch: "dev", Git: domain.GitStateDirty})
	if entry := first.Lookup([]string{"/repos/b"})["/repos/b"]; entry.Git != domain.GitStateDirty {
		t.Fatalf("first process did not see second process entry: %#v", entry)
	}
}

func TestConcurrentBackgroundRefreshersProbeEachRepositoryOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "git-status.json")
	prober := newFakeProber()
	prober.block = make(chan struct{})
	var shared sync.Mutex
	lock := func(context.Context) (func(), error) {
		shared.Lock()
		return shared.Unlock, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	list := func(context.Context) ([]string, error) { return []string{"/repos/a", "/repos/b"}, nil }
	var group sync.WaitGroup
	for range 2 {
		cache := New(prober, Options{Path: path, Lock: lock})
		group.Add(1)
		go func() {
			defer group.Done()
			cache.Run(ctx, time.Hour, list)
		}()
	}
	time.Sleep(20 * time.Millisecond)
	close(prober.block)

	deadline := time.Now().Add(2 * time.Second)
	for len(New(nil, Options{Path: path}).Lookup([]string{"/repos/a", "/repos/b"})) < 2 {
		if time.Now().After(deadline) {
			t.Fatal("background refreshers did not publish results")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	group.Wait()
	if prober.callCount("/repos/a") != 1 || prober.callCount("/repos/b") != 1 {
		t.Fatalf("probe calls = %v, want each repository once", prober.calls)
	}
}

func TestRunRefreshesUntilCanceled(t *testing.T) {
	prober := newFakeProber()
	prober.states["/repos/a"] = gitrepo.State{Git: domain.GitStateClean}
	cache := New(prober, Options{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		cache.Run(ctx, time.Millisecond, func(context.Context) ([]string, error) { return []string{"/repos/a"}, nil })
	}()
	deadline := time.Now().Add(2 * time.Second)
	for prober.callCount("/repos/a") < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("Run() probed %d times, want repeated refreshes", prober.callCount("/repos/a"))
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
}
