//go:build !linux

package agents

// ResolveForeground reports no foreground process on platforms without procfs.
// Detection then falls back to the pane's tmux-reported current command, which
// still names the agent but cannot supply its PID.
func ResolveForeground(int32) Foreground {
	return Foreground{}
}
