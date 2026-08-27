package tui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/moutansos/op/internal/domain"
)

func TestSnapshotCacheRoundTripAndFreshness(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	path := filepath.Join(t.TempDir(), "op", "snapshot.json")
	want := cachedDashboardSnapshot{
		Version: snapshotCacheVersion, PublishedAt: now,
		Tmux: domain.TmuxSnapshot{CapturedAt: now, Session: &domain.TmuxSession{
			Name: "code", Windows: []domain.TmuxWindow{{Panes: []domain.TmuxPane{{ID: "%7"}}}},
		}},
		Stats: domain.StatsSnapshot{CapturedAt: now, Agents: []domain.PaneAgentState{{
			PaneID: "%7", Activity: domain.AgentActivityAwaitingInput, QuietSeconds: 12,
		}}},
	}
	if err := writeSnapshotCache(path, want); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat cache: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache permissions = %o", info.Mode().Perm())
	}
	got, ok := readSnapshotCache(path, 10*time.Second, now.Add(time.Second))
	if !ok || got.Stats.Agents[0].Activity != domain.AgentActivityAwaitingInput {
		t.Fatalf("read cache = %+v, valid = %v", got, ok)
	}
	if _, ok := readSnapshotCache(path, 10*time.Second, now.Add(11*time.Second)); ok {
		t.Fatal("stale cache was accepted")
	}
}

func TestTreeModelStartsFromCachedAgentState(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "snapshot.json")
	snapshot := cachedDashboardSnapshot{
		Version: snapshotCacheVersion, PublishedAt: now,
		Tmux: domain.TmuxSnapshot{CapturedAt: now, Session: &domain.TmuxSession{
			Name: "code", Windows: []domain.TmuxWindow{{Panes: []domain.TmuxPane{{ID: "%7"}}}},
		}},
		Stats: domain.StatsSnapshot{CapturedAt: now, Agents: []domain.PaneAgentState{{
			PaneID: "%7", Activity: domain.AgentActivityAwaitingInput, QuietSeconds: 12,
		}}},
	}
	if err := writeSnapshotCache(path, snapshot); err != nil {
		t.Fatalf("write cache: %v", err)
	}
	model := newTreeModel(t.Context(), &fakeService{}, Options{SnapshotCachePath: path})
	if !model.haveTmux || !model.haveStats {
		t.Fatal("tree model did not hydrate cached snapshots")
	}
	if badge, _ := agentBadge(model.stats.Agents[0]); badge != "● input 12s" {
		t.Fatalf("cached badge = %q", badge)
	}
}

func TestTreeModelKeepsCacheThroughFirstLiveBaseline(t *testing.T) {
	cached := []domain.PaneAgentState{{PaneID: "%7", Activity: domain.AgentActivityAwaitingInput, QuietSeconds: 12}}
	current := []domain.PaneAgentState{{PaneID: "%7", Activity: domain.AgentActivityStarting}}
	got := preserveStartingAgents(current, cached)
	if got[0].Activity != domain.AgentActivityAwaitingInput || got[0].QuietSeconds != 12 {
		t.Fatalf("preserved agents = %+v", got)
	}
}
