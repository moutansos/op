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
	for _, line := range strings.Split(p.replies[0], "\n") {
		p.lines <- line
	}
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

	first := sampler.Sample(context.Background()).Stats
	if !first.Ready || first.CPUAvailable || first.Error != "" {
		t.Fatalf("first sample = %+v, want ready without CPU", first)
	}
	if first.LogicalCPUs != 20 || first.MemoryTotal != 32<<30 || first.MemoryUsed != 24<<30 || first.UptimeSeconds != 90061 {
		t.Fatalf("first sample = %+v", first)
	}
	second := sampler.Sample(context.Background()).Stats
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
	sample := sampler.Sample(context.Background()).Stats
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
	for !sampler.Sample(context.Background()).Stats.Ready {
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
		failed = strings.Contains(sampler.Sample(context.Background()).Stats.Error, "exit status 1")
		if !failed && time.Now().After(deadline) {
			t.Fatalf("failure was not reported: %+v", sampler.Sample(context.Background()))
		}
	}
	if starts != 1 || !processes[0].closed {
		t.Fatalf("starts = %d, closed = %v; must wait before restarting", starts, processes[0].closed)
	}

	now = now.Add(windowsStatsRetryAfter)
	sampler.responseWait = time.Second
	if sample := sampler.Sample(context.Background()).Stats; !sample.Ready || sample.Error != "" || starts != 2 {
		t.Fatalf("sample after retry = %+v, starts = %d", sample, starts)
	}
}

func TestWindowsSamplerStartFailure(t *testing.T) {
	sampler := newWindowsHostSampler(func() (windowsHostProcess, error) {
		return nil, errors.New("powershell.exe was not found")
	})
	if sample := sampler.Sample(context.Background()).Stats; sample.Ready || !strings.Contains(sample.Error, "powershell.exe was not found") {
		t.Fatalf("sample = %+v", sample)
	}
}

// processLine builds a sampler process line; created is a Windows FILETIME.
func processLine(pid, parent uint32, created, cpu int64, workingSet uint64, pane, tmux, name string) string {
	return fmt.Sprintf("%s%d\t%d\t%d\t%d\t%d\t%s\t%s\t%s", windowsProcessPrefix, pid, parent, created, cpu, workingSet, pane, tmux, name)
}

func TestParseWindowsProcess(t *testing.T) {
	process, err := parseWindowsProcess(processLine(30912, 32348, 134369020732969198, 20304062500, 215977984, "%14", "/tmp/tmux-1000/default,393,2", "opencode.exe") + "\r")
	if err != nil {
		t.Fatalf("parseWindowsProcess() error = %v", err)
	}
	if process.PID != 30912 || process.ParentPID != 32348 || process.Created != 134369020732969198 || process.cpuTime != 20304062500 ||
		process.WorkingSet != 215977984 || process.Pane != "%14" || process.ServerPID != 393 || process.Name != "opencode.exe" {
		t.Fatalf("process = %+v", process)
	}
	if spaced, err := parseWindowsProcess(processLine(1, 2, 3, 4, 5, "%1", "/tmp/my,odd dir/sock,77,0", "My App.exe")); err != nil || spaced.ServerPID != 77 || spaced.Name != "My App.exe" {
		t.Fatalf("spaced process = %+v, %v", spaced, err)
	}
	for _, line := range []string{
		processLine(1, 2, 3, 4, 5, "%1", "no-commas", "a.exe"),
		processLine(1, 2, 3, 4, 5, "%1", "/sock,x,0", "a.exe"),
		windowsProcessPrefix + "1\t2\t3",
		windowsProcessPrefix + "x\t2\t3\t4\t5\t%1\t/s,1,0\ta.exe",
	} {
		if _, err := parseWindowsProcess(line); err == nil {
			t.Errorf("parseWindowsProcess(%q) accepted invalid input", line)
		}
	}
}

func TestWindowsSamplerReportsPaneProcessesWithCPU(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	created := now.Add(-time.Hour).UnixMilli()*10000 + fileTimeUnixEpoch
	tmux := "/tmp/tmux-1000/default,393,0"
	process := newFakeWindowsProcess(
		processLine(10, 1, created, 1_000_000, 64<<20, "%1", tmux, "pwsh.exe")+"\n"+counterLine(1000, 3000, 500),
		"malformed process line\n"+processLine(10, 1, created, 6_000_000, 70<<20, "%1", tmux, "pwsh.exe")+"\n"+
			processLine(11, 10, created+1, 0, 1<<20, "%1", tmux, "child.exe")+"\n"+counterLine(1300, 3600, 900),
	)
	sampler, _ := testSampler(process)
	sampler.now = func() time.Time { return now }

	first := sampler.Sample(context.Background())
	if len(first.Processes) != 1 || first.Processes[0].CPUAvailable || first.Processes[0].Uptime != 3600 {
		t.Fatalf("first processes = %+v", first.Processes)
	}
	now = now.Add(2 * time.Second)
	second := sampler.Sample(context.Background())
	if len(second.Processes) != 2 {
		t.Fatalf("second processes = %+v", second.Processes)
	}
	// 5,000,000 x 100 ns = 0.5 s of CPU over 2 s is 25% of one logical CPU.
	if shell := second.Processes[0]; !shell.CPUAvailable || shell.CPUPercent != 25 || shell.WorkingSet != 70<<20 {
		t.Fatalf("shell = %+v", shell)
	}
	if child := second.Processes[1]; child.CPUAvailable {
		t.Fatalf("new child has CPU before a second sample: %+v", child)
	}
}

func TestWindowsProcessTreeOrdersByParentAndRejectsReusedParentPIDs(t *testing.T) {
	processes := []windowsProcess{
		{PID: 30, ParentPID: 20, Created: 300, Name: "node.exe"},
		{PID: 20, ParentPID: 10, Created: 200, Name: "opencode.exe"},
		{PID: 10, ParentPID: 1, Created: 100, Name: "pwsh.exe"},
		{PID: 25, ParentPID: 10, Created: 250, Name: "git.exe"},
		// PID 10 was reused: this process is older than the current PID 10.
		{PID: 40, ParentPID: 10, Created: 50, Name: "orphan.exe"},
	}
	var got []string
	for _, process := range windowsProcessTree(processes) {
		got = append(got, fmt.Sprintf("%d:%s", process.Depth, process.Name))
	}
	want := []string{"0:orphan.exe", "0:pwsh.exe", "1:opencode.exe", "2:node.exe", "1:git.exe"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("tree = %v, want %v", got, want)
	}
	cycle := windowsProcessTree([]windowsProcess{{PID: 1, ParentPID: 2, Created: 1}, {PID: 2, ParentPID: 1, Created: 1}})
	if len(cycle) != 2 {
		t.Fatalf("processes in a parent cycle = %+v, want both shown", cycle)
	}
}

func TestEncodedSamplerFitsWindowsCommandLine(t *testing.T) {
	// CreateProcess limits the whole command line to 32,767 characters.
	if length := len(encodePowerShellCommand(windowsHostScript)); length > 30000 {
		t.Fatalf("encoded sampler is %d characters", length)
	}
	if !strings.Contains(windowsHostScript, `"op-windows-proc v1\t"`) || windowsProcessPrefix != "op-windows-proc v1\t" {
		t.Fatal("script and parser disagree on the process line prefix")
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
