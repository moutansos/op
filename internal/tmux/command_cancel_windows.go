//go:build windows

package tmux

import (
	"os/exec"
	"time"
)

func configureCommandCancellation(command *exec.Cmd, interactive bool) {
	command.WaitDelay = 100 * time.Millisecond
	if interactive {
		command.WaitDelay = 500 * time.Millisecond
	}
}
