package stats

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"
)

type fakeWindowsProcess struct {
	mu       sync.Mutex
	replies  []string
	silent   bool
	requests int
	lines    chan string
	err      error
	closed   bool
}

func newFakeWindowsProcess(replies ...string) *fakeWindowsProcess {
	return &fakeWindowsProcess{replies: replies, lines: make(chan string, 16)}
}

func (p *fakeWindowsProcess) Request() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests++
	if p.silent || len(p.replies) == 0 {
		return nil
	}
	p.lines <- p.replies[0]
	p.replies = p.replies[1:]
	return nil
}

func (p *fakeWindowsProcess) Lines() <-chan string { return p.lines }
func (p *fakeWindowsProcess) Err() error           { return p.err }

func (p *fakeWindowsProcess) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
}

func (p *fakeWindowsProcess) exit(err error) {
	p.err = err
	close(p.lines)
}

func counterLine(idle, kernel, user int64) string {
	return fmt.Sprintf("%scpus=20 idle=%d kernel=%d user=%d memTotal=34359738368 memAvail=8589934592 uptimeMs=90061000",
		windowsStatsPrefix, idle, kernel, user)
}

func testSampler(process *fakeWindowsProcess) (*windowsHostSampler, *int) {
	starts := 0
	sampler := newWindowsHostSampler(func() (windowsHostProcess, error) {
		starts++
		return process, nil
	})
	sampler.responseWait = time.Second
	return sampler, &starts
}

func TestParseWindowsCounters(t *testing.T) {
	counters, err := parseWindowsCounters(counterLine(1000, 3000, 500) + "\r\n")
	if err != nil {
		t.Fatalf("parseWindowsCounters() error = %v", err)
	}
	want := windowsCounters{cpus: 20, idle: 1000, kernel: 3000, user: 500, memTotal: 32 << 30, memAvail: 8 << 30, uptimeMilliseconds: 90061000}
	if counters != want {
		t.Fatalf("counters = %+v, want %+v", counters, want)
	}
	for _, line := range []string{"noise", windowsStatsPrefix + "cpus=20 idle=x", windowsStatsPrefix + "cpus=20"} {
		if _, err := parseWindowsCounters(line); err == nil {
			t.Errorf("parseWindowsCounters(%q) accepted invalid input", line)
		}
	}
}

func TestWindowsSamplerComputesCPUFromConsecutiveSamples(t *testing.T) {
	process := newFakeWindowsProcess(counterLine(1000, 3000, 500), counterLine(1300, 3600, 900))
	sampler, starts := testSampler(process)

	first := sampler.Sample(context.Background())
	if !first.Ready || first.CPUAvailable || first.Error != "" {
		t.Fatalf("first sample = %+v, want ready without CPU", first)
	}
	if first.LogicalCPUs != 20 || first.MemoryTotal != 32<<30 || first.MemoryUsed != 24<<30 || first.UptimeSeconds != 90061 {
		t.Fatalf("first sample = %+v", first)
	}
	second := sampler.Sample(context.Background())
	// kernel+user grew by 1000, of which 300 was idle.
	if !second.CPUAvailable || second.CPUPercent != 70 {
		t.Fatalf("second sample = %+v, want 70%% CPU", second)
	}
	if *starts != 1 || process.requests != 2 {
		t.Fatalf("starts = %d, requests = %d", *starts, process.requests)
	}
}

func TestWindowsSamplerDoesNotBlockWhileStarting(t *testing.T) {
	process := newFakeWindowsProcess()
	process.silent = true
	sampler, _ := testSampler(process)
	sampler.responseWait = 20 * time.Millisecond

	started := time.Now()
	sample := sampler.Sample(context.Background())
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Sample() blocked for %s", elapsed)
	}
	if sample.Ready || sample.Error != "" {
		t.Fatalf("sample while starting = %+v", sample)
	}
	sampler.Sample(context.Background())
	if process.requests != 1 {
		t.Fatalf("requests = %d; an unanswered request must not be repeated", process.requests)
	}

	process.lines <- counterLine(1000, 3000, 500)
	deadline := time.Now().Add(2 * time.Second)
	for !sampler.Sample(context.Background()).Ready {
		if time.Now().After(deadline) {
			t.Fatal("late reply was never recorded")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestWindowsSamplerReportsFailureAndRetriesLater(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	processes := []*fakeWindowsProcess{newFakeWindowsProcess(), newFakeWindowsProcess(counterLine(1, 2, 3))}
	processes[0].silent = true
	starts := 0
	sampler := newWindowsHostSampler(func() (windowsHostProcess, error) {
		process := processes[starts]
		starts++
		return process, nil
	})
	sampler.now = func() time.Time { return now }
	sampler.responseWait = 10 * time.Millisecond

	sampler.Sample(context.Background())
	processes[0].exit(errors.New("exit status 1"))
	deadline := time.Now().Add(2 * time.Second)
	var failed bool
	for !failed {
		failed = strings.Contains(sampler.Sample(context.Background()).Error, "exit status 1")
		if !failed && time.Now().After(deadline) {
			t.Fatalf("failure was not reported: %+v", sampler.Sample(context.Background()))
		}
	}
	if starts != 1 || !processes[0].closed {
		t.Fatalf("starts = %d, closed = %v; must wait before restarting", starts, processes[0].closed)
	}

	now = now.Add(windowsStatsRetryAfter)
	sampler.responseWait = time.Second
	if sample := sampler.Sample(context.Background()); !sample.Ready || sample.Error != "" || starts != 2 {
		t.Fatalf("sample after retry = %+v, starts = %d", sample, starts)
	}
}

func TestWindowsSamplerStartFailure(t *testing.T) {
	sampler := newWindowsHostSampler(func() (windowsHostProcess, error) {
		return nil, errors.New("powershell.exe was not found")
	})
	if sample := sampler.Sample(context.Background()); sample.Ready || !strings.Contains(sample.Error, "powershell.exe was not found") {
		t.Fatalf("sample = %+v", sample)
	}
}

func TestEncodePowerShellCommandIsUTF16LittleEndianBase64(t *testing.T) {
	decoded, err := base64.StdEncoding.DecodeString(encodePowerShellCommand("Write-Output 'é'"))
	if err != nil || len(decoded)%2 != 0 {
		t.Fatalf("decoded = %v, %v", decoded, err)
	}
	units := make([]uint16, len(decoded)/2)
	for index := range units {
		units[index] = binary.LittleEndian.Uint16(decoded[index*2:])
	}
	if got := string(utf16.Decode(units)); got != "Write-Output 'é'" {
		t.Fatalf("round trip = %q", got)
	}
	if !strings.Contains(windowsHostScript, windowsStatsPrefix) {
		t.Fatal("script does not emit the protocol prefix")
	}
}
