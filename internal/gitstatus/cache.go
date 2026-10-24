// Package gitstatus caches repository state so front ends never wait on a
// complete git status pass over every project.
package gitstatus

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/moutansos/op/internal/domain"
	gitrepo "github.com/moutansos/op/internal/git"
)

const (
	fileVersion         = 1
	defaultConcurrency  = 4
	defaultProbeTimeout = 2 * time.Minute
	retention           = 30 * 24 * time.Hour
	maxFileSize         = 8 << 20
	persistEvery        = time.Second
)

// Prober inspects one repository.
type Prober interface {
	State(context.Context, string) (gitrepo.State, error)
}

// Entry is the last observed state of one repository path.
type Entry struct {
	Branch    string          `json:"branch,omitempty"`
	Git       domain.GitState `json:"git"`
	CheckedAt time.Time       `json:"checkedAt"`
}

type Options struct {
	// Path persists entries so separate op processes share one cache. An
	// empty path keeps the cache in memory only.
	Path         string
	Concurrency  int
	ProbeTimeout time.Duration
	Now          func() time.Time
	// Lock serializes background passes across processes so that a second
	// refresher waits and then reuses the first one's results.
	Lock func(context.Context) (func(), error)
}

type Cache struct {
	prober       Prober
	path         string
	concurrency  int
	probeTimeout time.Duration
	now          func() time.Time
	lock         func(context.Context) (func(), error)

	mu      sync.Mutex
	entries map[string]Entry
}

type cacheFile struct {
	Version int              `json:"version"`
	Entries map[string]Entry `json:"entries"`
}

func New(prober Prober, options Options) *Cache {
	if options.Concurrency <= 0 {
		options.Concurrency = defaultConcurrency
	}
	if options.ProbeTimeout <= 0 {
		options.ProbeTimeout = defaultProbeTimeout
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Lock == nil {
		options.Lock = func(context.Context) (func(), error) { return func() {}, nil }
	}
	return &Cache{
		prober:       prober,
		path:         options.Path,
		concurrency:  options.Concurrency,
		probeTimeout: options.ProbeTimeout,
		now:          options.Now,
		lock:         options.Lock,
		entries:      make(map[string]Entry),
	}
}

// DefaultPath is the shared cache location for the current user, or empty
// when no user cache directory is available.
func DefaultPath() string {
	directory, err := os.UserCacheDir()
	if err != nil || !filepath.IsAbs(directory) {
		return ""
	}
	return filepath.Join(directory, "op", "git-status.json")
}

// Lookup returns the cached entries for paths, including entries written by
// other op processes.
func (c *Cache) Lookup(paths []string) map[string]Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mergeFileLocked()
	result := make(map[string]Entry, len(paths))
	for _, path := range paths {
		if entry, found := c.entries[path]; found {
			result[path] = entry
		}
	}
	return result
}

// Record stores a state that was observed outside a refresh.
func (c *Cache) Record(path string, state gitrepo.State) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[path] = Entry{Branch: state.Branch, Git: state.Git, CheckedAt: c.now()}
	c.persistLocked()
}

// Refresh probes every path whose entry is missing or at least maxAge old.
// It returns when all probes finish or ctx ends; completed probes are kept
// and persisted either way.
func (c *Cache) Refresh(ctx context.Context, paths []string, maxAge time.Duration) {
	c.mu.Lock()
	c.mergeFileLocked()
	now := c.now()
	stale := make([]string, 0, len(paths))
	for _, path := range paths {
		if entry, found := c.entries[path]; !found || now.Sub(entry.CheckedAt) >= maxAge {
			stale = append(stale, path)
		}
	}
	c.mu.Unlock()
	if len(stale) == 0 || ctx.Err() != nil {
		return
	}

	work := make(chan string)
	updated := false
	lastPersist := c.now()
	var group sync.WaitGroup
	for worker := 0; worker < min(c.concurrency, len(stale)); worker++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for path := range work {
				probeCtx, cancel := context.WithTimeout(ctx, c.probeTimeout)
				state, err := c.prober.State(probeCtx, path)
				cancel()
				if ctx.Err() != nil {
					continue
				}
				entry := Entry{Git: domain.GitStateUnknown, CheckedAt: c.now()}
				if err == nil {
					entry.Branch, entry.Git = state.Branch, state.Git
				}
				c.mu.Lock()
				c.entries[path] = entry
				updated = true
				if entry.CheckedAt.Sub(lastPersist) >= persistEvery {
					c.persistLocked()
					lastPersist = entry.CheckedAt
				}
				c.mu.Unlock()
			}
		}()
	}
feed:
	for _, path := range stale {
		select {
		case work <- path:
		case <-ctx.Done():
			break feed
		}
	}
	close(work)
	group.Wait()

	c.mu.Lock()
	defer c.mu.Unlock()
	if updated {
		c.persistLocked()
	}
}

// Run refreshes the paths returned by list every interval until ctx ends.
// Entries refreshed recently by another process are not probed again.
func (c *Cache) Run(ctx context.Context, interval time.Duration, list func(context.Context) ([]string, error)) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if release, err := c.lock(ctx); err == nil {
			if paths, err := list(ctx); err == nil {
				c.Refresh(ctx, paths, interval)
			}
			release()
		}
		timer.Reset(interval)
	}
}

func (c *Cache) mergeFileLocked() {
	if c.path == "" {
		return
	}
	stored, ok := readFile(c.path)
	if !ok {
		return
	}
	for path, entry := range stored {
		if current, found := c.entries[path]; !found || entry.CheckedAt.After(current.CheckedAt) {
			c.entries[path] = entry
		}
	}
}

func (c *Cache) persistLocked() {
	if c.path == "" {
		return
	}
	c.mergeFileLocked()
	cutoff := c.now().Add(-retention)
	for path, entry := range c.entries {
		if entry.CheckedAt.Before(cutoff) {
			delete(c.entries, path)
		}
	}
	_ = writeFile(c.path, cacheFile{Version: fileVersion, Entries: c.entries})
}

func readFile(path string) (map[string]Entry, bool) {
	file, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer file.Close()
	var stored cacheFile
	decoder := json.NewDecoder(io.LimitReader(file, maxFileSize))
	if err := decoder.Decode(&stored); err != nil || stored.Version != fileVersion {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, false
	}
	return stored.Entries, true
}

func writeFile(path string, contents cacheFile) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(directory, ".git-status-*")
	if err != nil {
		return err
	}
	temporary := file.Name()
	defer os.Remove(temporary)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if err := json.NewEncoder(file).Encode(contents); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}
