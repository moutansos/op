package stats

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/moutansos/op/internal/domain"
)

const (
	windowsStatsPrefix       = "op-windows-stats v1 "
	windowsStatsResponseWait = 300 * time.Millisecond
	windowsStatsRetryAfter   = 30 * time.Second
)

// windowsHostScript runs in one long-lived Windows PowerShell process and
// prints one line of raw counters per input line. Native calls keep each
// sample to a few milliseconds; CIM performance classes take most of a second.
// The process exits when its input closes, including when op exits.
const windowsHostScript = `$ErrorActionPreference = 'Stop'
Add-Type -Namespace OpStats -Name Native -MemberDefinition @'
[StructLayout(LayoutKind.Sequential)]
public struct MemoryStatus {
    public uint Length; public uint MemoryLoad;
    public ulong TotalPhys; public ulong AvailPhys;
    public ulong TotalPageFile; public ulong AvailPageFile;
    public ulong TotalVirtual; public ulong AvailVirtual; public ulong AvailExtendedVirtual;
}
[DllImport("kernel32.dll", SetLastError = true)]
public static extern bool GetSystemTimes(out long idle, out long kernel, out long user);
[DllImport("kernel32.dll", SetLastError = true)]
public static extern bool GlobalMemoryStatusEx(ref MemoryStatus status);
[DllImport("kernel32.dll")]
public static extern ulong GetTickCount64();
'@
$cpus = [Environment]::ProcessorCount
while ($null -ne [Console]::In.ReadLine()) {
    $idle = 0L; $kernel = 0L; $user = 0L
    [void][OpStats.Native]::GetSystemTimes([ref]$idle, [ref]$kernel, [ref]$user)
    $memory = New-Object OpStats.Native+MemoryStatus
    $memory.Length = [Runtime.InteropServices.Marshal]::SizeOf($memory)
    [void][OpStats.Native]::GlobalMemoryStatusEx([ref]$memory)
    $uptime = [OpStats.Native]::GetTickCount64()
    [Console]::Out.WriteLine("` + windowsStatsPrefix + `cpus=$cpus idle=$idle kernel=$kernel user=$user memTotal=$($memory.TotalPhys) memAvail=$($memory.AvailPhys) uptimeMs=$uptime")
    [Console]::Out.Flush()
}
`

type windowsHostSource interface {
	Sample(context.Context) domain.WindowsHostStats
}

// windowsHostProcess is one running sampler process.
type windowsHostProcess interface {
	// Request asks for one more line of counters.
	Request() error
	// Lines delivers output lines and is closed when the process exits.
	Lines() <-chan string
	// Err reports why the process exited once Lines is closed.
	Err() error
	Close()
}

type windowsCounters struct {
	cpus               int
	idle, kernel, user int64
	memTotal, memAvail uint64
	uptimeMilliseconds uint64
}

func parseWindowsCounters(line string) (windowsCounters, error) {
	fields, found := strings.CutPrefix(strings.TrimRight(line, "\r\n"), windowsStatsPrefix)
	if !found {
		return windowsCounters{}, fmt.Errorf("unexpected output %q", line)
	}
	values := make(map[string]string)
	for _, field := range strings.Fields(fields) {
		key, value, ok := strings.Cut(field, "=")
		if ok {
			values[key] = value
		}
	}
	var counters windowsCounters
	var err error
	signed := func(key string) int64 {
		if err != nil {
			return 0
		}
		var value int64
		value, err = strconv.ParseInt(values[key], 10, 64)
		if err != nil {
			err = fmt.Errorf("parse %s: %w", key, err)
		}
		return value
	}
	unsigned := func(key string) uint64 {
		if err != nil {
			return 0
		}
		var value uint64
		value, err = strconv.ParseUint(values[key], 10, 64)
		if err != nil {
			err = fmt.Errorf("parse %s: %w", key, err)
		}
		return value
	}
	counters.cpus = int(signed("cpus"))
	counters.idle = signed("idle")
	counters.kernel = signed("kernel")
	counters.user = signed("user")
	counters.memTotal = unsigned("memTotal")
	counters.memAvail = unsigned("memAvail")
	counters.uptimeMilliseconds = unsigned("uptimeMs")
	if err != nil {
		return windowsCounters{}, err
	}
	return counters, nil
}

// windowsHostSampler keeps a PowerShell process running and asks it for
// counters on each Sample. It never blocks a stats refresh for longer than
// responseWait: until a reply arrives the previous sample is returned.
type windowsHostSampler struct {
	start        func() (windowsHostProcess, error)
	now          func() time.Time
	responseWait time.Duration
	retryAfter   time.Duration

	mu         sync.Mutex
	process    windowsHostProcess
	generation int
	pending    bool
	replies    chan struct{}
	nextStart  time.Time
	previous   *windowsCounters
	latest     domain.WindowsHostStats
}

func newWindowsHostSampler(start func() (windowsHostProcess, error)) *windowsHostSampler {
	return &windowsHostSampler{
		start:        start,
		now:          time.Now,
		responseWait: windowsStatsResponseWait,
		retryAfter:   windowsStatsRetryAfter,
		replies:      make(chan struct{}, 1),
	}
}

func (s *windowsHostSampler) Sample(ctx context.Context) domain.WindowsHostStats {
	s.mu.Lock()
	if s.process == nil {
		if s.now().Before(s.nextStart) {
			latest := s.latest
			s.mu.Unlock()
			return latest
		}
		if err := s.startLocked(); err != nil {
			latest := s.latest
			s.mu.Unlock()
			return latest
		}
	}
	if !s.pending {
		select {
		case <-s.replies:
		default:
		}
		if err := s.process.Request(); err != nil {
			s.failLocked(s.generation, err)
			latest := s.latest
			s.mu.Unlock()
			return latest
		}
		s.pending = true
	}
	s.mu.Unlock()

	timer := time.NewTimer(s.responseWait)
	defer timer.Stop()
	select {
	case <-s.replies:
	case <-timer.C:
	case <-ctx.Done():
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.latest
}

func (s *windowsHostSampler) startLocked() error {
	process, err := s.start()
	if err != nil {
		s.failLocked(s.generation, err)
		return err
	}
	s.process, s.pending, s.previous = process, false, nil
	if s.latest.Error != "" {
		s.latest = domain.WindowsHostStats{}
	}
	go s.read(s.generation, process)
	return nil
}

func (s *windowsHostSampler) read(generation int, process windowsHostProcess) {
	for line := range process.Lines() {
		if !strings.HasPrefix(line, windowsStatsPrefix) {
			continue
		}
		counters, err := parseWindowsCounters(line)
		s.mu.Lock()
		if generation != s.generation {
			s.mu.Unlock()
			return
		}
		if err != nil {
			s.failLocked(generation, err)
			s.mu.Unlock()
			return
		}
		s.recordLocked(counters)
		s.mu.Unlock()
		select {
		case s.replies <- struct{}{}:
		default:
		}
	}
	err := process.Err()
	if err == nil {
		err = errors.New("PowerShell exited")
	}
	s.mu.Lock()
	s.failLocked(generation, err)
	s.mu.Unlock()
}

func (s *windowsHostSampler) recordLocked(counters windowsCounters) {
	s.pending = false
	stats := domain.WindowsHostStats{
		Ready:         true,
		LogicalCPUs:   counters.cpus,
		MemoryTotal:   counters.memTotal,
		UptimeSeconds: counters.uptimeMilliseconds / 1000,
	}
	if counters.memAvail <= counters.memTotal {
		stats.MemoryUsed = counters.memTotal - counters.memAvail
	}
	if previous := s.previous; previous != nil {
		idle := counters.idle - previous.idle
		total := (counters.kernel - previous.kernel) + (counters.user - previous.user)
		if total > 0 && idle >= 0 && idle <= total {
			stats.CPUPercent = float64(total-idle) / float64(total) * 100
			stats.CPUAvailable = true
		}
	}
	s.previous = &counters
	s.latest = stats
}

func (s *windowsHostSampler) failLocked(generation int, err error) {
	if generation != s.generation {
		return
	}
	s.generation++
	if s.process != nil {
		s.process.Close()
	}
	s.process, s.pending, s.previous = nil, false, nil
	s.nextStart = s.now().Add(s.retryAfter)
	s.latest = domain.WindowsHostStats{Error: "Windows host stats unavailable: " + err.Error()}
}

// encodePowerShellCommand returns the -EncodedCommand form of script, which
// avoids quoting it through WSL interop's Windows command-line conversion.
func encodePowerShellCommand(script string) string {
	units := utf16.Encode([]rune(script))
	encoded := make([]byte, len(units)*2)
	for index, unit := range units {
		binary.LittleEndian.PutUint16(encoded[index*2:], unit)
	}
	return base64.StdEncoding.EncodeToString(encoded)
}

type execWindowsHostProcess struct {
	command *exec.Cmd
	stdin   io.WriteCloser
	lines   chan string
	stderr  *limitedBuffer
	err     error
	once    sync.Once
}

func startPowerShellSampler(powershell string) (windowsHostProcess, error) {
	command := exec.Command(powershell,
		"-NoLogo", "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encodePowerShellCommand(windowsHostScript))
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	process := &execWindowsHostProcess{command: command, stdin: stdin, lines: make(chan string, 4), stderr: &limitedBuffer{limit: 4096}}
	command.Stderr = process.stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			process.lines <- scanner.Text()
		}
		process.err = command.Wait()
		if detail := strings.TrimSpace(process.stderr.String()); detail != "" {
			process.err = errors.New(lastLine(detail))
		}
		close(process.lines)
	}()
	return process, nil
}

func (p *execWindowsHostProcess) Request() error {
	_, err := p.stdin.Write([]byte("\n"))
	return err
}

func (p *execWindowsHostProcess) Lines() <-chan string { return p.lines }
func (p *execWindowsHostProcess) Err() error           { return p.err }

func (p *execWindowsHostProcess) Close() {
	p.once.Do(func() {
		_ = p.stdin.Close()
		if p.command.Process != nil {
			_ = p.command.Process.Kill()
		}
	})
}

type limitedBuffer struct {
	mu     sync.Mutex
	limit  int
	buffer bytes.Buffer
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.limit - b.buffer.Len(); room > 0 {
		b.buffer.Write(data[:min(len(data), room)])
	}
	return len(data), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func lastLine(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}
