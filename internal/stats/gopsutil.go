package stats

import (
	"context"
	"errors"
	"os"
	"sort"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"
)

type gopsutilHost struct{}

func (gopsutilHost) CPUPercent(ctx context.Context) (float64, error) {
	percent, err := cpu.PercentWithContext(ctx, 0, false)
	if err != nil || len(percent) == 0 {
		return 0, err
	}
	return percent[0], nil
}

func (gopsutilHost) VirtualMemory(ctx context.Context) (uint64, uint64, error) {
	memory, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return 0, 0, err
	}
	return memory.Used, memory.Total, nil
}

func (gopsutilHost) LoadAverage(ctx context.Context) ([3]float64, error) {
	average, err := load.AvgWithContext(ctx)
	if err != nil {
		return [3]float64{}, err
	}
	return [3]float64{average.Load1, average.Load5, average.Load15}, nil
}

func (gopsutilHost) Uptime(ctx context.Context) (uint64, error) {
	return host.UptimeWithContext(ctx)
}

type gopsutilProcessFactory struct{}

func (gopsutilProcessFactory) Snapshot(ctx context.Context) (processSnapshot, error) {
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, normalizeProcessError(err)
	}

	result := &gopsutilProcessSnapshot{
		processes: make(map[int32]processMetrics, len(processes)),
		children:  make(map[int32][]processMetrics),
	}
	for _, proc := range processes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		metric := gopsutilProcess{process: proc}
		result.processes[proc.Pid] = metric
		parentPID, err := proc.PpidWithContext(ctx)
		if err == nil {
			result.children[parentPID] = append(result.children[parentPID], metric)
		} else if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
	}
	for parentPID := range result.children {
		sort.Slice(result.children[parentPID], func(i, j int) bool {
			return result.children[parentPID][i].PID() < result.children[parentPID][j].PID()
		})
	}
	return result, nil
}

type gopsutilProcessSnapshot struct {
	processes map[int32]processMetrics
	children  map[int32][]processMetrics
}

func (s *gopsutilProcessSnapshot) Open(pid int32) (processMetrics, error) {
	proc, ok := s.processes[pid]
	if !ok {
		return nil, errProcessGone
	}
	return proc, nil
}

func (s *gopsutilProcessSnapshot) Children(pid int32) []processMetrics {
	return s.children[pid]
}

type gopsutilProcess struct {
	process *process.Process
}

func (p gopsutilProcess) PID() int32 {
	return p.process.Pid
}

func (p gopsutilProcess) Times(ctx context.Context) (float64, float64, error) {
	times, err := p.process.TimesWithContext(ctx)
	if err != nil {
		return 0, 0, normalizeProcessError(err)
	}
	return times.User, times.System, nil
}

func (p gopsutilProcess) RSS(ctx context.Context) (uint64, error) {
	memory, err := p.process.MemoryInfoWithContext(ctx)
	if err != nil {
		return 0, normalizeProcessError(err)
	}
	return memory.RSS, nil
}

func (p gopsutilProcess) CreateTime(ctx context.Context) (int64, error) {
	created, err := p.process.CreateTimeWithContext(ctx)
	return created, normalizeProcessError(err)
}

func normalizeProcessError(err error) error {
	if errors.Is(err, process.ErrorProcessNotRunning) || errors.Is(err, os.ErrNotExist) {
		return errProcessGone
	}
	return err
}
