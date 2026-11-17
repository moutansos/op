//go:build linux

package serveproc

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Host reads processes from a procfs tree.
type Host struct {
	ProcRoot     string
	UID          int
	Self         int
	PollInterval time.Duration
	Kill         func(pid int, signal syscall.Signal) error
	// StatusDir holds rejected-restart reports written by servers.
	StatusDir string
}

// New returns a Host for the current user's processes.
func New(statusDir string) *Host {
	return &Host{ProcRoot: "/proc", UID: os.Getuid(), Self: os.Getpid(), PollInterval: 100 * time.Millisecond, Kill: syscall.Kill, StatusDir: statusDir}
}

// List returns the current user's processes accepted by match, ordered by PID.
func (h *Host) List(match func(Process) bool) ([]Process, error) {
	entries, err := os.ReadDir(h.ProcRoot)
	if err != nil {
		return nil, err
	}
	var processes []Process
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == h.Self || !h.ownedByUser(pid) || !h.alive(pid) {
			continue
		}
		process, ok := h.read(pid)
		if ok && match(process) {
			processes = append(processes, process)
		}
	}
	slices.SortFunc(processes, func(a, b Process) int { return a.PID - b.PID })
	return processes, nil
}

func (h *Host) path(pid int, name ...string) string {
	return filepath.Join(append([]string{h.ProcRoot, strconv.Itoa(pid)}, name...)...)
}

func (h *Host) ownedByUser(pid int) bool {
	info, err := os.Stat(h.path(pid))
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == h.UID
}

func (h *Host) read(pid int) (Process, bool) {
	cmdline, err := os.ReadFile(h.path(pid, "cmdline"))
	if err != nil || len(cmdline) == 0 {
		return Process{}, false
	}
	process := Process{PID: pid, Args: strings.Split(strings.TrimSuffix(string(cmdline), "\x00"), "\x00")}
	if exe, err := os.Readlink(h.path(pid, "exe")); err == nil {
		process.Executable = strings.TrimSuffix(exe, " (deleted)")
	}
	process.Generation = h.generation(pid)
	if cgroup, err := os.ReadFile(h.path(pid, "cgroup")); err == nil {
		process.Unit, process.UserUnit = unitFromCgroup(string(cgroup))
	}
	return process, true
}

func (h *Host) generation(pid int) string {
	environ, err := os.ReadFile(h.path(pid, "environ"))
	if err != nil {
		return ""
	}
	return generationFromEnviron(environ)
}

// alive treats zombies as exited: their image is gone even before the parent reaps them.
func (h *Host) alive(pid int) bool {
	stat, err := os.ReadFile(h.path(pid, "stat"))
	if err != nil {
		return false
	}
	end := bytes.LastIndexByte(stat, ')')
	if end < 0 || end+2 >= len(stat) {
		return false
	}
	state := stat[end+2]
	return state != 'Z' && state != 'X'
}

// listening reports whether the process holds a listening TCP socket.
func (h *Host) listening(pid int) bool {
	fds, err := os.ReadDir(h.path(pid, "fd"))
	if err != nil {
		return false
	}
	inodes := make(map[string]bool)
	for _, fd := range fds {
		target, err := os.Readlink(h.path(pid, "fd", fd.Name()))
		if err != nil {
			continue
		}
		if inode, found := strings.CutPrefix(target, "socket:["); found {
			inodes[strings.TrimSuffix(inode, "]")] = true
		}
	}
	if len(inodes) == 0 {
		return false
	}
	for _, table := range []string{"tcp", "tcp6"} {
		file, err := os.Open(h.path(pid, "net", table))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) > 9 && fields[3] == "0A" && inodes[fields[9]] {
				file.Close()
				return true
			}
		}
		file.Close()
	}
	return false
}

// Restart signals the process to re-execute itself and waits until the
// replacement image is listening again.
func (h *Host) Restart(ctx context.Context, process Process) error {
	clearRestartError(h.StatusDir, process.PID)
	if err := h.Kill(process.PID, RestartSignal); err != nil {
		return fmt.Errorf("signal pid %d: %w", process.PID, err)
	}
	restarted := false
	for {
		if !h.alive(process.PID) {
			if restarted {
				return ErrExitedAfterRestart
			}
			return ErrExited
		}
		if !restarted {
			if err := readRestartError(h.StatusDir, process.PID); err != nil {
				clearRestartError(h.StatusDir, process.PID)
				return err
			}
			restarted = h.generation(process.PID) != process.Generation
		}
		if restarted && h.listening(process.PID) {
			return nil
		}
		select {
		case <-ctx.Done():
			if restarted {
				return fmt.Errorf("restarted but not listening yet: %w", ctx.Err())
			}
			return fmt.Errorf("still checking the replacement or shutting down: %w", ctx.Err())
		case <-time.After(h.PollInterval):
		}
	}
}
