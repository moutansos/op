package tui

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/moutansos/op/internal/domain"
)

const snapshotCacheVersion = 1

type cachedDashboardSnapshot struct {
	Version     int                  `json:"version"`
	PublishedAt time.Time            `json:"publishedAt"`
	Tmux        domain.TmuxSnapshot  `json:"tmux"`
	Stats       domain.StatsSnapshot `json:"stats"`
}

func readSnapshotCache(path string, maxAge time.Duration, now time.Time) (cachedDashboardSnapshot, bool) {
	if path == "" {
		return cachedDashboardSnapshot{}, false
	}
	file, err := os.Open(path)
	if err != nil {
		return cachedDashboardSnapshot{}, false
	}
	defer file.Close()
	var snapshot cachedDashboardSnapshot
	decoder := json.NewDecoder(io.LimitReader(file, 8<<20))
	if err := decoder.Decode(&snapshot); err != nil {
		return cachedDashboardSnapshot{}, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return cachedDashboardSnapshot{}, false
	}
	if snapshot.Version != snapshotCacheVersion || snapshot.PublishedAt.IsZero() || snapshot.Tmux.CapturedAt.IsZero() || snapshot.Stats.CapturedAt.IsZero() {
		return cachedDashboardSnapshot{}, false
	}
	if snapshot.PublishedAt.After(now.Add(5*time.Second)) || now.Sub(snapshot.PublishedAt) > maxAge {
		return cachedDashboardSnapshot{}, false
	}
	return snapshot, true
}

func writeSnapshotCache(path string, snapshot cachedDashboardSnapshot) error {
	if path == "" {
		return nil
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".snapshot-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if err := json.NewEncoder(file).Encode(snapshot); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
