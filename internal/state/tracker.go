package state

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/moutansos/op/internal/domain"
	"github.com/moutansos/op/internal/notify"
)

const maxNativeSessions = 1024

type Tracker struct {
	service    domain.Service
	options    Options
	mu         sync.Mutex
	snapshot   Snapshot
	native     map[string]Agent
	paneAgents []Agent
	pending    chan []byte
	client     *http.Client
	running    bool
}

func New(service domain.Service, options Options) (*Tracker, error) {
	if service == nil {
		return nil, errors.New("state: service is required")
	}
	if strings.TrimSpace(options.InstanceID) == "" {
		return nil, errors.New("state: instance ID is required")
	}
	if options.RefreshInterval < 0 || options.HeartbeatInterval < 0 || options.StaleAfter < 0 {
		return nil, errors.New("state: intervals must not be negative")
	}
	if options.RefreshInterval == 0 {
		options.RefreshInterval = 5 * time.Second
	}
	if options.HeartbeatInterval == 0 {
		options.HeartbeatInterval = 30 * time.Second
	}
	if options.StaleAfter == 0 {
		options.StaleAfter = 2 * time.Minute
	}
	if options.ParentURL != "" {
		u, err := url.Parse(options.ParentURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			return nil, errors.New("state: parent URL must be an absolute HTTP(S) endpoint without credentials or fragment")
		}
	}
	var epoch [16]byte
	if _, err := rand.Read(epoch[:]); err != nil {
		return nil, err
	}
	t := &Tracker{service: service, options: options, native: make(map[string]Agent), pending: make(chan []byte, 1), client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	hostname, _ := os.Hostname()
	t.snapshot = Snapshot{Version: 1, InstanceID: options.InstanceID, Hostname: hostname, Epoch: hex.EncodeToString(epoch[:])}
	t.snapshot.Projects.Data = []Project{}
	t.snapshot.Tmux.Data = []Window{}
	t.snapshot.Agents.Data = []Agent{}
	return t, nil
}

// Run samples immediately and blocks until cancellation. Only one Run may be
// active. Dependency calls receive ctx; an uncooperative dependency cannot delay
// Run returning (there is at most one outstanding sampling worker).
func (t *Tracker) Run(ctx context.Context) error {
	t.mu.Lock()
	if t.running {
		t.mu.Unlock()
		return errors.New("state: tracker already started")
	}
	t.running = true
	t.mu.Unlock()
	go t.sampleLoop(ctx)
	if t.options.ParentURL != "" {
		go t.forward(ctx)
	}
	heartbeat := time.NewTicker(t.options.HeartbeatInterval)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-heartbeat.C:
			t.mu.Lock()
			t.publishLocked(now)
			t.mu.Unlock()
		}
	}
}

func (t *Tracker) sampleLoop(ctx context.Context) {
	ticker := time.NewTicker(t.options.RefreshInterval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		t.sample(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func update[T any](s *Section[T], data T, err error, now time.Time) {
	s.AttemptedAt = now
	if err != nil {
		s.Error = err.Error()
		s.Stale = true
		return
	}
	s.Data, s.Available, s.Stale, s.Error, s.UpdatedAt = data, true, false, "", now
}

func (t *Tracker) sample(ctx context.Context) {
	sampleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	projects, pe := t.service.ListProjects(sampleCtx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	sampleCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
	tmux, te := t.service.GetTmuxSnapshot(sampleCtx)
	cancel()
	if ctx.Err() != nil {
		return
	}
	sampleCtx, cancel = context.WithTimeout(ctx, 10*time.Second)
	var stats domain.StatsSnapshot
	var se error
	if collector, ok := t.service.(interface {
		GetStatsForTmux(context.Context, domain.TmuxSnapshot) (domain.StatsSnapshot, error)
	}); ok && te == nil {
		stats, se = collector.GetStatsForTmux(sampleCtx, tmux)
	} else {
		stats, se = t.service.GetStatsSnapshot(sampleCtx)
	}
	cancel()
	if ctx.Err() != nil {
		return
	}
	now := time.Now()
	t.mu.Lock()
	defer t.mu.Unlock()
	ps := make([]Project, 0, len(projects))
	for _, p := range projects {
		path := canonical(p.Path)
		ps = append(ps, Project{ID: t.id("project", path), NativeID: p.ID, Name: p.Name, Path: path, Branch: p.Branch, Kind: p.Kind, GitState: p.GitState, Tags: append([]string(nil), p.Tags...)})
	}
	update(&t.snapshot.Projects, ps, pe, now)
	ws := []Window{}
	if tmux.Session != nil {
		s := tmux.Session
		for _, w := range s.Windows {
			window := Window{ID: t.id("window", s.ID, w.ID), NativeID: w.ID, SessionID: t.id("tmux-session", s.ID), NativeSessionID: s.ID, SessionName: s.Name, SessionAttached: s.Attached, Name: w.Name, Index: w.Index, Active: w.Active, ProjectID: t.projectByNative(w.ProjectID), Profile: w.Profile, Panes: []Pane{}}
			window.Mode = domain.ProjectOpenModeTmux
			for _, p := range w.Panes {
				dir := canonical(p.CurrentPath)
				project := t.projectByDirectory(dir)
				if project == "" {
					project = window.ProjectID
				}
				window.Panes = append(window.Panes, Pane{ID: t.id("pane", s.ID, p.ID), NativeID: p.ID, ProjectID: project, Index: p.Index, PID: p.PID, CurrentCommand: p.CurrentCommand, CurrentPath: dir, Active: p.Active, Dead: p.Dead})
			}
			ws = append(ws, window)
		}
	}
	update(&t.snapshot.Tmux, ws, te, now)
	ae := se
	if ae == nil && stats.AgentsError != "" {
		ae = errors.New(stats.AgentsError)
	}
	if ae == nil {
		t.paneAgents = nil
		for _, a := range stats.Agents {
			agent := Agent{ID: t.id("pane-agent", a.PaneID, a.AgentName), NativeID: a.PaneID, Source: a.AgentName, Provenance: "pane-detector", ForegroundPID: a.ForegroundPID, Activity: a.Activity, Detail: a.Detail, ObservedAt: now}
			for _, w := range t.snapshot.Tmux.Data {
				for _, p := range w.Panes {
					if p.NativeID == a.PaneID {
						agent.PaneID, agent.ProjectID, agent.Directory = p.ID, p.ProjectID, p.CurrentPath
					}
				}
			}
			t.paneAgents = append(t.paneAgents, agent)
		}
	}
	update(&t.snapshot.Agents, t.snapshot.Agents.Data, ae, now)
	update(&t.snapshot.Host, stats.Host, se, now)
	t.publishLocked(now)
}

// Observe consumes native activity independently of notification delivery.
// Out-of-order observations do not overwrite newer state. Native IDs are never
// assumed to be catalog IDs, and sessions are never merged based on directory.
func (t *Tracker) Observe(o notify.Observation) {
	if o.SessionID == "" {
		return
	}
	now := time.Now()
	if o.Timestamp.IsZero() || o.Timestamp.After(now) {
		o.Timestamp = now
	}
	id := t.id("native-session", string(o.Source), o.SessionID)
	directory := canonical(o.ProjectDirectory)
	t.mu.Lock()
	defer t.mu.Unlock()
	if previous, ok := t.native[id]; ok {
		if !o.Timestamp.After(previous.ObservedAt) {
			return
		}
		// Status/resolution events often omit metadata supplied by earlier events.
		if directory == "" {
			directory = previous.Directory
		}
		if o.ProjectID == "" {
			o.ProjectID = previous.NativeProjectID
		}
	}
	if _, ok := t.native[id]; !ok && len(t.native) >= maxNativeSessions {
		var oldest string
		for key, a := range t.native {
			if oldest == "" || a.ObservedAt.Before(t.native[oldest].ObservedAt) {
				oldest = key
			}
		}
		delete(t.native, oldest)
	}
	activity := o.Activity
	if activity == "" {
		activity = domain.AgentActivityUnknown
	}
	var notification *notify.Notification
	if o.Notification != nil {
		copy := *o.Notification
		copy.Choices = append([]notify.Choice(nil), copy.Choices...)
		notification = &copy
	}
	t.native[id] = Agent{ID: id, NativeID: o.SessionID, Source: string(o.Source), Provenance: "native-event", Coverage: o.Coverage, Notification: notification, NativeProjectID: o.ProjectID, Directory: directory, Activity: activity, Detail: o.Detail, ObservedAt: o.Timestamp, Terminated: o.Terminated}
	t.publishLocked(now)
}

func (t *Tracker) projectByNative(id string) string {
	if id != "" {
		for _, p := range t.snapshot.Projects.Data {
			if p.NativeID == id {
				return p.ID
			}
		}
	}
	return ""
}

func (t *Tracker) projectByDirectory(dir string) string {
	best, length := "", -1
	if dir == "" {
		return best
	}
	for _, p := range t.snapshot.Projects.Data {
		if p.Path == "" {
			continue
		}
		rel, err := filepath.Rel(p.Path, dir)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && len(p.Path) > length {
			best, length = p.ID, len(p.Path)
		}
	}
	return best
}

func canonical(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	// Resolve existing ancestors too, so a not-yet-created child under a symlink
	// still correlates to its canonical project.
	root, suffix := abs, ""
	for {
		if resolved, err := filepath.EvalSymlinks(root); err == nil {
			return filepath.Join(resolved, suffix)
		}
		parent := filepath.Dir(root)
		if parent == root {
			return filepath.Clean(abs)
		}
		suffix = filepath.Join(filepath.Base(root), suffix)
		root = parent
	}
}

func (t *Tracker) id(parts ...string) string {
	parts = append([]string{t.options.InstanceID}, parts...)
	for i := range parts {
		parts[i] = base64.RawURLEncoding.EncodeToString([]byte(parts[i]))
	}
	return strings.Join(parts, ".")
}

func freshness[T any](s *Section[T], now time.Time, ttl time.Duration) {
	s.Stale = !s.Available || s.Error != "" || now.Sub(s.UpdatedAt) > ttl
}

func (t *Tracker) refreshLocked(now time.Time) {
	freshness(&t.snapshot.Projects, now, t.options.StaleAfter)
	freshness(&t.snapshot.Tmux, now, t.options.StaleAfter)
	freshness(&t.snapshot.Agents, now, t.options.StaleAfter)
	freshness(&t.snapshot.Host, now, t.options.StaleAfter)
	agents := make([]Agent, 0, len(t.paneAgents)+len(t.native))
	for _, a := range t.paneAgents {
		a.Stale = t.snapshot.Agents.Stale
		if a.Stale {
			a.Activity = domain.AgentActivityUnknown
		}
		agents = append(agents, a)
	}
	for _, a := range t.native {
		a.ProjectID = t.projectByDirectory(a.Directory)
		a.Stale = now.Sub(a.ObservedAt) > t.options.StaleAfter
		if a.Stale {
			a.Activity = domain.AgentActivityUnknown
		}
		agents = append(agents, a)
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	t.snapshot.Agents.Data = agents
}

// Snapshot returns an isolated copy. Freshness is evaluated at read time even
// before Run starts or after it stops; reads do not increment the revision.
func (t *Tracker) Snapshot() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refreshLocked(time.Now())
	copy := t.snapshot
	copy.Projects.Data = append([]Project{}, copy.Projects.Data...)
	for i := range copy.Projects.Data {
		copy.Projects.Data[i].Tags = append([]string(nil), copy.Projects.Data[i].Tags...)
	}
	copy.Tmux.Data = append([]Window{}, copy.Tmux.Data...)
	for i := range copy.Tmux.Data {
		copy.Tmux.Data[i].Panes = append([]Pane{}, copy.Tmux.Data[i].Panes...)
	}
	copy.Agents.Data = append([]Agent{}, copy.Agents.Data...)
	for i := range copy.Agents.Data {
		if n := copy.Agents.Data[i].Notification; n != nil {
			cloned := *n
			cloned.Choices = append([]notify.Choice(nil), n.Choices...)
			copy.Agents.Data[i].Notification = &cloned
		}
	}
	return copy
}

func (t *Tracker) publishLocked(now time.Time) {
	t.refreshLocked(now)
	t.snapshot.Revision++
	t.snapshot.CapturedAt = now
	if t.options.ParentURL == "" {
		return
	}
	e := Envelope{Version: 1, InstanceID: t.options.InstanceID, Epoch: t.snapshot.Epoch, Sequence: t.snapshot.Revision, EventID: t.id(t.snapshot.Epoch, strconv.FormatUint(t.snapshot.Revision, 10)), OccurredAt: now, Type: "state.snapshot", Payload: t.snapshot}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	select {
	case t.pending <- data:
	default:
		select {
		case <-t.pending:
		default:
		}
		t.pending <- data
	}
}
