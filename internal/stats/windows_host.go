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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/moutansos/op/internal/domain"
)

const (
	windowsStatsPrefix       = "op-windows-stats v1 "
	windowsProcessPrefix     = "op-windows-proc v1\t"
	windowsStatsResponseWait = 300 * time.Millisecond
	windowsStatsRetryAfter   = 30 * time.Second
	// fileTimeUnixEpoch is the Unix epoch as a Windows FILETIME.
	fileTimeUnixEpoch = 116444736000000000
)

// windowsHostScript runs in one long-lived Windows PowerShell process. For
// each input line it prints one line per Windows process started from a tmux
// pane, then one line of host counters. Process times and memory come from a
// single NtQuerySystemInformation call; a process is opened once, when first
// seen, to read TMUX and TMUX_PANE from its environment block. The process
// exits when its input closes, including when op exits.
const windowsHostScript = `$ErrorActionPreference = 'Stop'
Add-Type -TypeDefinition @'
using System;
using System.Collections.Generic;
using System.Runtime.InteropServices;
using System.Text;
public static class OpSampler {
    [StructLayout(LayoutKind.Sequential)]
    struct MemoryStatus {
        public uint Length; public uint MemoryLoad; public ulong TotalPhys; public ulong AvailPhys;
        public ulong TotalPageFile; public ulong AvailPageFile; public ulong TotalVirtual; public ulong AvailVirtual; public ulong AvailExtendedVirtual;
    }
    [StructLayout(LayoutKind.Sequential)]
    struct BasicInformation {
        public IntPtr ExitStatus; public IntPtr PebBaseAddress; public IntPtr AffinityMask;
        public IntPtr BasePriority; public IntPtr UniqueProcessId; public IntPtr InheritedFromUniqueProcessId;
    }
    [DllImport("kernel32.dll")] static extern bool GetSystemTimes(out long idle, out long kernel, out long user);
    [DllImport("kernel32.dll")] static extern bool GlobalMemoryStatusEx(ref MemoryStatus status);
    [DllImport("kernel32.dll")] static extern ulong GetTickCount64();
    [DllImport("kernel32.dll")] static extern bool CloseHandle(IntPtr handle);
    [DllImport("kernel32.dll")] static extern IntPtr OpenProcess(uint access, bool inherit, uint processId);
    [DllImport("kernel32.dll")] static extern bool ReadProcessMemory(IntPtr process, IntPtr address, byte[] buffer, IntPtr size, out IntPtr read);
    [DllImport("ntdll.dll")] static extern int NtQuerySystemInformation(int informationClass, IntPtr buffer, int length, out int returned);
    [DllImport("ntdll.dll")] static extern int NtQueryInformationProcess(IntPtr process, int informationClass, ref BasicInformation information, int length, out int returned);

    public static string Host() {
        long idle, kernel, user;
        GetSystemTimes(out idle, out kernel, out user);
        var memory = new MemoryStatus();
        memory.Length = (uint)Marshal.SizeOf(typeof(MemoryStatus));
        GlobalMemoryStatusEx(ref memory);
        return "` + windowsStatsPrefix + `cpus=" + Environment.ProcessorCount + " idle=" + idle + " kernel=" + kernel + " user=" + user +
            " memTotal=" + memory.TotalPhys + " memAvail=" + memory.AvailPhys + " uptimeMs=" + GetTickCount64();
    }

    class Identity { public string Tmux; public string Pane; }
    static readonly Dictionary<string, Identity> identities = new Dictionary<string, Identity>();
    static IntPtr buffer = IntPtr.Zero;
    static int bufferSize = 1 << 20;

    public static List<string> Processes() {
        var lines = new List<string>();
        if (IntPtr.Size != 8) return lines;
        int returned;
        while (true) {
            if (buffer == IntPtr.Zero) buffer = Marshal.AllocHGlobal(bufferSize);
            int status = NtQuerySystemInformation(5, buffer, bufferSize, out returned);
            if (status == 0) break;
            if (status != unchecked((int)0xC0000004) || bufferSize >= (64 << 20)) return lines;
            Marshal.FreeHGlobal(buffer);
            buffer = IntPtr.Zero;
            bufferSize = Math.Max(bufferSize * 2, returned + (1 << 16));
        }
        var seen = new HashSet<string>();
        for (int offset = 0; ; ) {
            IntPtr entry = IntPtr.Add(buffer, offset);
            long creation = Marshal.ReadInt64(entry, 32);
            long cpu = Marshal.ReadInt64(entry, 40) + Marshal.ReadInt64(entry, 48);
            int nameBytes = Marshal.ReadInt16(entry, 56) & 0xFFFF;
            IntPtr name = Marshal.ReadIntPtr(entry, 64);
            uint id = (uint)Marshal.ReadIntPtr(entry, 80).ToInt64();
            uint parent = (uint)Marshal.ReadIntPtr(entry, 88).ToInt64();
            long workingSet = Marshal.ReadIntPtr(entry, 144).ToInt64();
            if (id != 0 && name != IntPtr.Zero) {
                string key = id + ":" + creation;
                seen.Add(key);
                Identity identity;
                if (!identities.TryGetValue(key, out identity)) {
                    identity = ReadIdentity(id);
                    identities[key] = identity;
                }
                if (identity != null) {
                    lines.Add("op-windows-proc v1\t" + id + "\t" + parent + "\t" + creation + "\t" + cpu + "\t" + workingSet + "\t" +
                        identity.Pane + "\t" + identity.Tmux + "\t" + Marshal.PtrToStringUni(name, nameBytes / 2));
                }
            }
            int next = Marshal.ReadInt32(entry, 0);
            if (next == 0) break;
            offset += next;
        }
        var stale = new List<string>();
        foreach (string key in identities.Keys) if (!seen.Contains(key)) stale.Add(key);
        foreach (string key in stale) identities.Remove(key);
        return lines;
    }

    static Identity ReadIdentity(uint id) {
        IntPtr process = OpenProcess(0x1010, false, id);
        if (process == IntPtr.Zero) return null;
        try {
            var information = new BasicInformation();
            int returned;
            if (NtQueryInformationProcess(process, 0, ref information, Marshal.SizeOf(typeof(BasicInformation)), out returned) != 0) return null;
            IntPtr parameters = ReadPointer(process, information.PebBaseAddress, 0x20);
            IntPtr environment = ReadPointer(process, parameters, 0x80);
            long size = (long)ReadPointer(process, parameters, 0x3F0);
            if (environment == IntPtr.Zero || size <= 0 || size > (1 << 20)) return null;
            var block = new byte[size];
            IntPtr read;
            if (!ReadProcessMemory(process, environment, block, new IntPtr(size), out read)) return null;
            string tmux = null, pane = null;
            foreach (string variable in Encoding.Unicode.GetString(block, 0, (int)read).Split('\0')) {
                if (variable.StartsWith("TMUX=", StringComparison.Ordinal)) tmux = variable.Substring(5);
                else if (variable.StartsWith("TMUX_PANE=", StringComparison.Ordinal)) pane = variable.Substring(10);
            }
            if (string.IsNullOrEmpty(tmux) || string.IsNullOrEmpty(pane) || (tmux + pane).IndexOfAny(new[] { '\t', '\r', '\n' }) >= 0) return null;
            return new Identity { Tmux = tmux, Pane = pane };
        } finally {
            CloseHandle(process);
        }
    }

    static IntPtr ReadPointer(IntPtr process, IntPtr address, int offset) {
        if (address == IntPtr.Zero) return IntPtr.Zero;
        var value = new byte[8];
        IntPtr read;
        if (!ReadProcessMemory(process, IntPtr.Add(address, offset), value, new IntPtr(8), out read) || read.ToInt64() != 8) return IntPtr.Zero;
        return new IntPtr(BitConverter.ToInt64(value, 0));
    }
}
'@
while ($null -ne [Console]::In.ReadLine()) {
    try { foreach ($line in [OpSampler]::Processes()) { [Console]::Out.WriteLine($line) } } catch { }
    [Console]::Out.WriteLine([OpSampler]::Host())
    [Console]::Out.Flush()
}
`

// windowsHostSample is the latest Windows host reading.
type windowsHostSample struct {
	Stats domain.WindowsHostStats
	// Processes are Windows programs started from tmux panes.
	Processes []windowsProcess
}

type windowsHostSource interface {
	Sample(context.Context) windowsHostSample
}

// windowsProcess is one Windows program started from a tmux pane.
type windowsProcess struct {
	PID          uint32
	ParentPID    uint32
	Created      int64
	Name         string
	Pane         string
	ServerPID    int32
	CPUPercent   float64
	CPUAvailable bool
	WorkingSet   uint64
	Uptime       uint64
	cpuTime      int64
}

// windowsHostProcess is one running sampler process.
type windowsHostProcess interface {
	// Request asks for one more sample.
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

// parseWindowsProcess reads one process line. TMUX is "socket,serverPID,session";
// only the server PID is kept, because pane IDs are unique per server.
func parseWindowsProcess(line string) (windowsProcess, error) {
	fields, found := strings.CutPrefix(strings.TrimRight(line, "\r\n"), windowsProcessPrefix)
	if !found {
		return windowsProcess{}, fmt.Errorf("unexpected output %q", line)
	}
	parts := strings.SplitN(fields, "\t", 8)
	if len(parts) != 8 {
		return windowsProcess{}, fmt.Errorf("process line has %d fields", len(parts))
	}
	var process windowsProcess
	var err error
	parse := func(text string, bits int, signed bool) uint64 {
		if err != nil {
			return 0
		}
		var value uint64
		if signed {
			var number int64
			number, err = strconv.ParseInt(text, 10, bits)
			value = uint64(number)
		} else {
			value, err = strconv.ParseUint(text, 10, bits)
		}
		return value
	}
	process.PID = uint32(parse(parts[0], 32, false))
	process.ParentPID = uint32(parse(parts[1], 32, false))
	process.Created = int64(parse(parts[2], 64, true))
	process.cpuTime = int64(parse(parts[3], 64, true))
	process.WorkingSet = parse(parts[4], 64, false)
	if err != nil {
		return windowsProcess{}, fmt.Errorf("parse process line: %w", err)
	}
	tmux := strings.Split(parts[6], ",")
	if len(tmux) < 3 {
		return windowsProcess{}, fmt.Errorf("unexpected TMUX value %q", parts[6])
	}
	server, serverErr := strconv.ParseInt(tmux[len(tmux)-2], 10, 32)
	if serverErr != nil || server <= 0 {
		return windowsProcess{}, fmt.Errorf("unexpected TMUX value %q", parts[6])
	}
	process.Pane, process.ServerPID, process.Name = parts[5], int32(server), parts[7]
	return process, nil
}

type windowsProcessKey struct {
	pid     uint32
	created int64
}

// windowsHostSampler keeps a PowerShell process running and asks it for a
// sample on each call. It never blocks a stats refresh for longer than
// responseWait: until a reply arrives the previous sample is returned.
type windowsHostSampler struct {
	start        func() (windowsHostProcess, error)
	now          func() time.Time
	responseWait time.Duration
	retryAfter   time.Duration

	mu               sync.Mutex
	process          windowsHostProcess
	generation       int
	pending          bool
	replies          chan struct{}
	nextStart        time.Time
	previous         *windowsCounters
	previousCPU      map[windowsProcessKey]int64
	previousSampleAt time.Time
	latest           windowsHostSample
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

func (s *windowsHostSampler) Sample(ctx context.Context) windowsHostSample {
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
	s.process, s.pending, s.previous, s.previousCPU = process, false, nil, nil
	if s.latest.Stats.Error != "" {
		s.latest = windowsHostSample{}
	}
	go s.read(s.generation, process)
	return nil
}

func (s *windowsHostSampler) read(generation int, process windowsHostProcess) {
	var batch []windowsProcess
	for line := range process.Lines() {
		if strings.HasPrefix(line, windowsProcessPrefix) {
			// A malformed process line only loses that process.
			if parsed, err := parseWindowsProcess(line); err == nil {
				batch = append(batch, parsed)
			}
			continue
		}
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
		s.recordLocked(counters, batch)
		s.mu.Unlock()
		batch = nil
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

func (s *windowsHostSampler) recordLocked(counters windowsCounters, processes []windowsProcess) {
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

	// Process CPU is a share of one logical CPU, like Linux pane processes.
	sampledAt := s.now()
	elapsed := sampledAt.Sub(s.previousSampleAt).Seconds()
	currentCPU := make(map[windowsProcessKey]int64, len(processes))
	unixMilliseconds := sampledAt.UnixMilli()
	for index := range processes {
		process := &processes[index]
		key := windowsProcessKey{pid: process.PID, created: process.Created}
		currentCPU[key] = process.cpuTime
		if previous, found := s.previousCPU[key]; found && elapsed > 0 && process.cpuTime >= previous {
			process.CPUPercent = float64(process.cpuTime-previous) / (elapsed * 1e7) * 100
			process.CPUAvailable = true
		}
		if createdMilliseconds := (process.Created - fileTimeUnixEpoch) / 10000; createdMilliseconds > 0 && createdMilliseconds <= unixMilliseconds {
			process.Uptime = uint64((unixMilliseconds - createdMilliseconds) / 1000)
		}
	}
	s.previousCPU, s.previousSampleAt = currentCPU, sampledAt
	s.latest = windowsHostSample{Stats: stats, Processes: processes}
}

func (s *windowsHostSampler) failLocked(generation int, err error) {
	if generation != s.generation {
		return
	}
	s.generation++
	if s.process != nil {
		s.process.Close()
	}
	s.process, s.pending, s.previous, s.previousCPU = nil, false, nil, nil
	s.nextStart = s.now().Add(s.retryAfter)
	s.latest = windowsHostSample{Stats: domain.WindowsHostStats{Error: "Windows host stats unavailable: " + err.Error()}}
}

// windowsProcessTree orders one pane's Windows processes as a tree. Roots are
// processes whose parent is not in the set; a parent must predate its child,
// which guards against Windows reusing a dead parent's PID.
func windowsProcessTree(processes []windowsProcess) []domain.WindowsProcessStats {
	byPID := make(map[uint32]windowsProcess, len(processes))
	for _, process := range processes {
		byPID[process.PID] = process
	}
	children := make(map[uint32][]windowsProcess)
	var roots []windowsProcess
	for _, process := range processes {
		parent, found := byPID[process.ParentPID]
		if found && parent.PID != process.PID && parent.Created <= process.Created {
			children[parent.PID] = append(children[parent.PID], process)
		} else {
			roots = append(roots, process)
		}
	}
	byCreation := func(list []windowsProcess) {
		sort.Slice(list, func(i, j int) bool {
			if list[i].Created != list[j].Created {
				return list[i].Created < list[j].Created
			}
			return list[i].PID < list[j].PID
		})
	}
	byCreation(roots)
	result := make([]domain.WindowsProcessStats, 0, len(processes))
	visited := make(map[uint32]bool, len(processes))
	var walk func(windowsProcess, int)
	walk = func(process windowsProcess, depth int) {
		if visited[process.PID] {
			return
		}
		visited[process.PID] = true
		result = append(result, domain.WindowsProcessStats{
			PID: process.PID, ParentPID: process.ParentPID, Name: process.Name, Depth: depth,
			CPUPercent: process.CPUPercent, CPUAvailable: process.CPUAvailable,
			WorkingSetBytes: process.WorkingSet, UptimeSeconds: process.Uptime,
		})
		list := children[process.PID]
		byCreation(list)
		for _, child := range list {
			walk(child, depth+1)
		}
	}
	for _, root := range roots {
		walk(root, 0)
	}
	// Anything left belongs to a parent cycle; show it rather than drop it.
	rest := make([]windowsProcess, 0)
	for _, process := range processes {
		if !visited[process.PID] {
			rest = append(rest, process)
		}
	}
	byCreation(rest)
	for _, process := range rest {
		walk(process, 0)
	}
	return result
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
	process := &execWindowsHostProcess{command: command, stdin: stdin, lines: make(chan string, 64), stderr: &limitedBuffer{limit: 4096}}
	command.Stderr = process.stderr
	if err := command.Start(); err != nil {
		return nil, err
	}
	go func() {
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 64*1024), 1<<20)
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
