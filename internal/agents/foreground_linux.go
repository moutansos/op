//go:build linux

package agents

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ResolveForeground identifies the process that owns a pane terminal's input.
//
// It reads tpgid, field 8 of /proc/<pid>/stat, which the kernel maintains as the
// foreground process group of the terminal the process is attached to. That is
// strictly better than walking the pane's child tree: an agent is frequently a
// grandchild of the pane's shell, several forks deep, and only one process group
// at a time can read from the terminal. The group leader's PID equals the group
// ID, so tpgid addresses the leader directly.
//
// Ownership is a property of the terminal, not of the process consulted, so
// reading it from the pane's root shell is sufficient and costs two file reads
// regardless of how large the pane's process tree is.
func ResolveForeground(panePID int32) Foreground {
	if panePID <= 0 {
		return Foreground{}
	}
	tpgid, ok := terminalForegroundGroup(int(panePID))
	if !ok || tpgid <= 0 {
		return Foreground{}
	}
	foreground := Foreground{PID: int32(tpgid), Valid: true}
	if comm, err := os.ReadFile(procPath(tpgid, "comm")); err == nil {
		foreground.Command = strings.TrimSpace(string(comm))
	}
	if cmdline, err := os.ReadFile(procPath(tpgid, "cmdline")); err == nil {
		foreground.Args = splitNulls(cmdline)
	}
	if foreground.Command == "" && len(foreground.Args) > 0 {
		foreground.Command = filepath.Base(foreground.Args[0])
	}
	return foreground
}

func terminalForegroundGroup(pid int) (int, bool) {
	data, err := os.ReadFile(procPath(pid, "stat"))
	if err != nil {
		return 0, false
	}
	return parseForegroundGroup(string(data))
}

// parseForegroundGroup extracts tpgid, field 8 of a /proc/<pid>/stat line.
//
// The comm field is unquoted and may itself contain spaces and parentheses, so
// parsing anchors on the final ')' rather than splitting the whole line.
func parseForegroundGroup(stat string) (int, bool) {
	closing := strings.LastIndexByte(stat, ')')
	if closing < 0 {
		return 0, false
	}
	// Fields after comm resume at stat field 3 (state), so field 8 is the sixth
	// entry of the remainder.
	fields := strings.Fields(stat[closing+1:])
	if len(fields) < 6 {
		return 0, false
	}
	tpgid, err := strconv.Atoi(fields[5])
	if err != nil {
		return 0, false
	}
	return tpgid, true
}

func procPath(pid int, name string) string {
	return filepath.Join("/proc", strconv.Itoa(pid), name)
}

func splitNulls(data []byte) []string {
	parts := strings.Split(string(data), "\x00")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}
