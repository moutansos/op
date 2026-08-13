package app

import (
	"context"
	"testing"

	"github.com/moutansos/op/internal/domain"
	tmuxmanager "github.com/moutansos/op/internal/tmux"
)

func TestLazyTmuxSnapshotUsesReadOnlyPathUntilMutation(t *testing.T) {
	manager := new(lazyTestTmux)
	factoryCalls := 0
	readCalls := 0
	lazy := newLazyTmux(func(context.Context) (TmuxManager, error) {
		factoryCalls++
		return manager, nil
	}, func(context.Context) (domain.TmuxSnapshot, error) {
		readCalls++
		return domain.TmuxSnapshot{}, nil
	})

	if _, err := lazy.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot() error = %v", err)
	}
	if factoryCalls != 0 || readCalls != 1 {
		t.Fatalf("after read-only snapshot factory calls = %d, read calls = %d", factoryCalls, readCalls)
	}
	if _, err := lazy.EnsureMainSession(context.Background()); err != nil {
		t.Fatalf("EnsureMainSession() error = %v", err)
	}
	if _, err := lazy.Snapshot(context.Background()); err != nil {
		t.Fatalf("initialized Snapshot() error = %v", err)
	}
	if factoryCalls != 1 || readCalls != 1 || manager.snapshotCalls != 1 {
		t.Fatalf("after mutation factory calls = %d, read calls = %d, manager snapshots = %d", factoryCalls, readCalls, manager.snapshotCalls)
	}
}

type lazyTestTmux struct {
	snapshotCalls int
}

func (*lazyTestTmux) EnsureMainSession(context.Context) (domain.EnsureMainSessionResult, error) {
	return domain.EnsureMainSessionResult{}, nil
}

func (*lazyTestTmux) PrepareAttachOrSwitch(context.Context) (tmuxmanager.AttachPlan, error) {
	return tmuxmanager.AttachPlan{Mode: tmuxmanager.AttachModeInteractive}, nil
}

func (*lazyTestTmux) PrepareAttachOrSwitchTo(context.Context, string) (tmuxmanager.AttachPlan, error) {
	return tmuxmanager.AttachPlan{Mode: tmuxmanager.AttachModeInteractive}, nil
}

func (*lazyTestTmux) ExecuteAttachOrSwitch(context.Context, tmuxmanager.AttachPlan) error { return nil }

func (*lazyTestTmux) OpenProjectWindow(context.Context, tmuxmanager.OpenProjectWindowRequest) (domain.OpenProjectResult, error) {
	return domain.OpenProjectResult{}, nil
}

func (f *lazyTestTmux) Snapshot(context.Context) (domain.TmuxSnapshot, error) {
	f.snapshotCalls++
	return domain.TmuxSnapshot{}, nil
}

func (*lazyTestTmux) CurrentProjectID(context.Context) (string, bool, error) {
	return "", false, nil
}

func (*lazyTestTmux) CurrentProjectName(context.Context) (string, bool, error) {
	return "", false, nil
}
