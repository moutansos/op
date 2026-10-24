//go:build !windows

package tmux

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func configureCommandCancellation(command *exec.Cmd, interactive bool) {
	// A tmux client can pass its output descriptor to the server. If the server
	// is stopped, bound pipe draining even after the client process is gone.
	command.WaitDelay = 100 * time.Millisecond
	if interactive {
		command.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
		// Let the tmux client restore terminal state before a forced stop.
		command.Cancel = func() error {
			err := command.Process.Signal(syscall.SIGTERM)
			if errors.Is(err, os.ErrProcessDone) {
				return os.ErrProcessDone
			}
			return err
		}
		command.WaitDelay = 500 * time.Millisecond
		return
	}

	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	command.Cancel = func() error {
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
