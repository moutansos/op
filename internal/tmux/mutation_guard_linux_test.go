//go:build linux

package tmux

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanStaleMutationGuardsChecksProcessStartIdentity(t *testing.T) {
	directory, err := mutationGuardDirectory()
	if err != nil {
		t.Fatal(err)
	}
	started, alive := linuxProcessStart(os.Getpid())
	if !alive {
		t.Fatal("test process start identity was unavailable")
	}
	live := filepath.Join(directory, mutationGuardPrefix+"11111111111111111111111111111111")
	stale := filepath.Join(directory, mutationGuardPrefix+"22222222222222222222222222222222")
	defer os.Remove(live)
	defer os.Remove(stale)
	if err := os.WriteFile(live, []byte(fmt.Sprintf("%d %s %s\n", os.Getpid(), started, "11111111111111111111111111111111")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte(fmt.Sprintf("%d %s %s\n", os.Getpid(), "1", "22222222222222222222222222222222")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := cleanStaleMutationGuards(directory); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Fatalf("live guard was removed: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("PID-reuse stale guard was retained: %v", err)
	}
}
