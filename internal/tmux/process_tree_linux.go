//go:build linux

package tmux

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func processTreeContainsCommand(ctx context.Context, rootPID int, command string) (bool, error) {
	expected := shellWords(command)
	if len(expected) > 0 && expected[0] == "exec" {
		expected = expected[1:]
	}
	if len(expected) == 0 {
		return false, errors.New("dashboard command does not identify an executable")
	}

	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, fmt.Errorf("read /proc: %w", err)
	}
	children := make(map[int][]int)
	commands := make(map[int][]string)
	rootFound := false
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || !entry.IsDir() {
			continue
		}
		stat, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) || errors.Is(err, os.ErrPermission) {
				continue
			}
			return false, fmt.Errorf("read process %d stat: %w", pid, err)
		}
		closeParen := strings.LastIndexByte(string(stat), ')')
		if closeParen < 0 {
			continue
		}
		fields := strings.Fields(string(stat[closeParen+1:]))
		if len(fields) < 2 {
			continue
		}
		parentPID, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		children[parentPID] = append(children[parentPID], pid)
		if pid == rootPID {
			rootFound = true
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "cmdline"))
		if err == nil {
			commands[pid] = splitCommandLine(cmdline)
		}
	}
	if !rootFound {
		return false, nil
	}

	queue := []int{rootPID}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		if commandLineMatches(commands[pid], expected) {
			return true, nil
		}
		queue = append(queue, children[pid]...)
	}
	return false, nil
}

func splitCommandLine(value []byte) []string {
	parts := strings.Split(strings.TrimRight(string(value), "\x00"), "\x00")
	if len(parts) == 1 && parts[0] == "" {
		return nil
	}
	return parts
}

func commandLineMatches(actual, expected []string) bool {
	if len(actual) < len(expected) || len(expected) == 0 {
		return false
	}
	if filepath.Base(actual[0]) != filepath.Base(expected[0]) {
		return false
	}
	for index := 1; index < len(expected); index++ {
		if actual[index] != expected[index] {
			return false
		}
	}
	return true
}
