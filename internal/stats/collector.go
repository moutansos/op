package stats

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"

	"github.com/moutansos/op/internal/agents"
	"github.com/moutansos/op/internal/domain"
)

const defaultMaxProcessesPerPane = 1024

var errProcessGone = errors.New("process no longer exists")

type hostMetrics interface {
	CPUPercent(context.Context) (float64, error)
	VirtualMemory(context.Context) (used, total uint64, err error)
	LoadAverage(context.Context) ([3]float64, error)
	Uptime(context.Context) (uint64, error)
}

type processFactory interface {
	Snapshot(context.Context) (processSnapshot, error)
}

type processSnapshot interface {
	Open(int32) (processMetrics, error)
	Children(int32) []processMetrics
}

type processMetrics interface {
	PID() int32
	Times(context.Context) (user, system float64, err error)
	RSS(context.Context) (uint64, error)
	CreateTime(context.Context) (milliseconds int64, err error)
}

type processSampleKey struct {
	paneID     string
	pid        int32
	createTime int64
}

// foregroundResolver identifies the process owning a pane terminal's input.
type foregroundResolver func(panePID int32) agents.Foreground

// Collector samples host metrics and process trees rooted at tmux panes.
// Calls are serialized because CPU deltas depend on the preceding completed sample.
type Collector struct {
	mu                      sync.Mutex
	host                    hostMetrics
	processes               processFactory
	now                     func() time.Time
	maxProcessesPerPane     int
	previousProcessCPU      map[processSampleKey]float64
	previousProcessSampleAt time.Time

	foreground foregroundResolver
	detector   *agents.Detector
	capturer   agents.Capturer
}

// Options configures optional collector capabilities.
type Options struct {
	// Detector enables agent classification. When nil, snapshots carry no
	// agent states.
	Detector *agents.Detector
	// Capturer reads pane contents for the detector. Detection is inert
	// without it, because quiescence cannot be observed from process state.
	Capturer agents.Capturer
}

// NewCollector constructs a collector backed by gopsutil.
func NewCollector(options Options) *Collector {
	collector := newCollector(gopsutilHost{}, gopsutilProcessFactory{}, time.Now, defaultMaxProcessesPerPane)
	collector.foreground = agents.ResolveForeground
	collector.detector = options.Detector
	collector.capturer = options.Capturer
	return collector
}

func newCollector(host hostMetrics, processes processFactory, now func() time.Time, maxProcessesPerPane int) *Collector {
	if maxProcessesPerPane < 1 {
		maxProcessesPerPane = 1
	}
	return &Collector{
		host:                host,
		processes:           processes,
		now:                 now,
		maxProcessesPerPane: maxProcessesPerPane,
		previousProcessCPU:  make(map[processSampleKey]float64),
	}
}

// Collect returns a new snapshot. Metric failures are represented by zero values and
// availability flags so a short-lived or inaccessible process cannot block refreshes.
func (c *Collector) Collect(ctx context.Context, tmux domain.TmuxSnapshot) (domain.StatsSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return domain.StatsSnapshot{}, err
	}

	capturedAt := c.now()
	host, err := c.collectHost(ctx)
	if err != nil {
		return domain.StatsSnapshot{}, err
	}

	processTable, processErr := c.processes.Snapshot(ctx)
	if processErr != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return domain.StatsSnapshot{}, ctxErr
		}
		processTable = unavailableProcessSnapshot{err: processErr}
	}

	processes := make([]domain.PaneProcessStats, 0, paneCount(tmux))
	foregrounds := make(map[string]agents.Foreground, paneCount(tmux))
	currentCPU := make(map[processSampleKey]float64)
	elapsed := capturedAt.Sub(c.previousProcessSampleAt).Seconds()
	if c.previousProcessSampleAt.IsZero() || elapsed <= 0 {
		elapsed = 0
	}

	if tmux.Session != nil {
		for _, window := range tmux.Session.Windows {
			for _, pane := range window.Panes {
				stats, foreground, err := c.collectPane(ctx, processTable, capturedAt, elapsed, window.Name, pane, currentCPU)
				if err != nil {
					return domain.StatsSnapshot{}, err
				}
				processes = append(processes, stats)
				foregrounds[pane.ID] = foreground
			}
		}
	}

	c.previousProcessCPU = currentCPU
	c.previousProcessSampleAt = capturedAt

	return domain.StatsSnapshot{
		CapturedAt: capturedAt,
		Host:       host,
		Processes:  processes,
		Agents:     c.collectAgents(ctx, capturedAt, tmux, foregrounds),
	}, nil
}

// collectAgents classifies the panes that hold recognized agents. It reuses the
// foreground processes already resolved while walking the pane trees, so no pane
// is inspected twice. Panes without a recognized agent are never captured, which
// keeps tmux invocations proportional to the number of agents rather than to the
// number of panes.
func (c *Collector) collectAgents(
	ctx context.Context,
	capturedAt time.Time,
	tmux domain.TmuxSnapshot,
	foregrounds map[string]agents.Foreground,
) []domain.PaneAgentState {
	if c.detector == nil || tmux.Session == nil {
		return nil
	}
	panes := make([]agents.Pane, 0, len(foregrounds))
	for _, window := range tmux.Session.Windows {
		for _, pane := range window.Panes {
			panes = append(panes, agents.Pane{
				PaneID:     pane.ID,
				WindowName: window.Name,
				RootPID:    pane.PID,
				Command:    pane.CurrentCommand,
				Dead:       pane.Dead,
				Foreground: foregrounds[pane.ID],
			})
		}
	}
	return c.detector.Classify(ctx, capturedAt, panes, c.capturer)
}

func (c *Collector) collectHost(ctx context.Context) (domain.HostStats, error) {
	var stats domain.HostStats

	if value, err := c.host.CPUPercent(ctx); err == nil && finite(value) {
		stats.CPUPercent = value
	}
	if err := ctx.Err(); err != nil {
		return domain.HostStats{}, err
	}

	if used, total, err := c.host.VirtualMemory(ctx); err == nil {
		stats.MemoryUsed = used
		stats.MemoryTotal = total
	}
	if err := ctx.Err(); err != nil {
		return domain.HostStats{}, err
	}

	if average, err := c.host.LoadAverage(ctx); err == nil {
		for i, value := range average {
			if finite(value) {
				stats.LoadAverage[i] = value
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return domain.HostStats{}, err
	}

	if uptime, err := c.host.Uptime(ctx); err == nil {
		stats.UptimeSeconds = uptime
	}
	if err := ctx.Err(); err != nil {
		return domain.HostStats{}, err
	}

	return stats, nil
}

func (c *Collector) collectPane(
	ctx context.Context,
	processes processSnapshot,
	capturedAt time.Time,
	elapsed float64,
	windowName string,
	pane domain.TmuxPane,
	currentCPU map[processSampleKey]float64,
) (domain.PaneProcessStats, agents.Foreground, error) {
	stats := domain.PaneProcessStats{
		WindowName: windowName,
		PaneID:     pane.ID,
		RootPID:    pane.PID,
		Command:    pane.CurrentCommand,
		Dead:       pane.Dead,
	}
	if pane.Dead || pane.PID <= 0 {
		return stats, agents.Foreground{}, nil
	}

	// The foreground process group owns the pane's input and is often several
	// forks below the pane's root shell, so it is read from the terminal rather
	// than inferred from the tree walked below.
	var foreground agents.Foreground
	if c.foreground != nil {
		foreground = c.foreground(pane.PID)
		if foreground.Valid {
			stats.ForegroundPID = foreground.PID
			stats.ForegroundCommand = foreground.Command
		}
	}
	if stats.ForegroundCommand == "" {
		stats.ForegroundCommand = pane.CurrentCommand
	}

	root, err := processes.Open(pane.PID)
	if err != nil {
		stats.Dead = errors.Is(err, errProcessGone)
		if ctxErr := ctx.Err(); ctxErr != nil {
			return domain.PaneProcessStats{}, agents.Foreground{}, ctxErr
		}
		return stats, foreground, nil
	}

	queue := []processMetrics{root}
	queued := map[int32]struct{}{root.PID(): {}}
	visited := make(map[int32]struct{}, c.maxProcessesPerPane)
	var cpuDelta float64
	var cpuAvailable bool

	for len(queue) > 0 && len(visited) < c.maxProcessesPerPane {
		if err := ctx.Err(); err != nil {
			return domain.PaneProcessStats{}, agents.Foreground{}, err
		}

		proc := queue[0]
		queue = queue[1:]
		pid := proc.PID()
		if _, ok := visited[pid]; ok {
			continue
		}
		visited[pid] = struct{}{}

		createTime, createErr := proc.CreateTime(ctx)
		if pid == pane.PID && createErr == nil && createTime > 0 {
			startedAt := time.UnixMilli(createTime)
			if !startedAt.After(capturedAt) {
				stats.UptimeSeconds = uint64(capturedAt.Sub(startedAt) / time.Second)
			}
		}
		if pid == pane.PID && errors.Is(createErr, errProcessGone) {
			stats.Dead = true
		}

		if rss, rssErr := proc.RSS(ctx); rssErr == nil {
			stats.ResidentBytes += rss
		} else if pid == pane.PID && errors.Is(rssErr, errProcessGone) {
			stats.Dead = true
		}

		if user, system, timesErr := proc.Times(ctx); timesErr == nil {
			total := user + system
			if finite(total) && total >= 0 {
				key := processSampleKey{paneID: pane.ID, pid: pid, createTime: createTime}
				currentCPU[key] = total
				if previous, ok := c.previousProcessCPU[key]; ok && total >= previous {
					cpuAvailable = true
					cpuDelta += total - previous
				}
			}
		} else if pid == pane.PID && errors.Is(timesErr, errProcessGone) {
			stats.Dead = true
		}

		for _, child := range processes.Children(pid) {
			if child == nil {
				continue
			}
			if len(visited)+len(queue) >= c.maxProcessesPerPane {
				break
			}
			childPID := child.PID()
			if _, ok := visited[childPID]; ok {
				continue
			}
			if _, ok := queued[childPID]; ok {
				continue
			}
			queued[childPID] = struct{}{}
			queue = append(queue, child)
		}
	}

	if err := ctx.Err(); err != nil {
		return domain.PaneProcessStats{}, agents.Foreground{}, err
	}
	if elapsed > 0 && cpuAvailable {
		stats.CPUPercent = cpuDelta / elapsed * 100
		stats.CPUAvailable = true
	}
	stats.ProcessCount = len(visited)
	return stats, foreground, nil
}

type unavailableProcessSnapshot struct {
	err error
}

func (s unavailableProcessSnapshot) Open(int32) (processMetrics, error) {
	return nil, s.err
}

func (unavailableProcessSnapshot) Children(int32) []processMetrics {
	return nil
}

func paneCount(snapshot domain.TmuxSnapshot) int {
	if snapshot.Session == nil {
		return 0
	}
	count := 0
	for _, window := range snapshot.Session.Windows {
		count += len(window.Panes)
	}
	return count
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}
