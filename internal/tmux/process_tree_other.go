//go:build !linux

package tmux

import "context"

func processTreeContainsCommand(context.Context, int, string) (bool, error) {
	// Full local tmux orchestration runs on Linux/WSL. Preserve a tracked pane on
	// other build targets rather than risk replacing an uninspectable process.
	return true, nil
}
