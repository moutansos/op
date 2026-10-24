//go:build linux

package process

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

func configureProcessCancellation(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}

		// Stop each process before discovering its children so the tree cannot
		// grow behind the traversal. Keep the inherited process group unchanged:
		// terminal actions must remain in the foreground group of their pty.
		pids := stoppedProcessTree(cmd.Process.Pid)
		var rootErr error
		for _, pid := range pids {
			err := syscall.Kill(pid, syscall.SIGKILL)
			if pid == cmd.Process.Pid {
				rootErr = err
			}
		}
		if errors.Is(rootErr, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return rootErr
	}
}

func stoppedProcessTree(pid int) []int {
	if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
		return []int{pid}
	}
	pids := []int{pid}
	for _, child := range childPIDs(pid) {
		pids = append(pids, stoppedProcessTree(child)...)
	}
	return pids
}

func childPIDs(pid int) []int {
	taskRoot := "/proc/" + strconv.Itoa(pid) + "/task"
	tasks, err := os.ReadDir(taskRoot)
	if err != nil {
		return nil
	}
	seen := make(map[int]struct{})
	for _, task := range tasks {
		data, err := os.ReadFile(taskRoot + "/" + task.Name() + "/children")
		if err != nil {
			continue
		}
		for _, field := range strings.Fields(string(data)) {
			child, err := strconv.Atoi(field)
			if err == nil && child > 0 {
				seen[child] = struct{}{}
			}
		}
	}
	pids := make([]int, 0, len(seen))
	for child := range seen {
		pids = append(pids, child)
	}
	return pids
}
