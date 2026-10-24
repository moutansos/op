//go:build !linux

package tmux

import (
	"context"
	"io"
)

// Native Windows does not support the local tmux runtime. Keep the adapter
// buildable for remote-only commands without pretending to provide /proc-based
// queued-mutation ownership on unsupported platforms.
func (r rawTmux) runMutation(ctx context.Context, args ...string) (string, error) {
	return r.run(ctx, args...)
}

func (r rawTmux) runSessionCreation(ctx context.Context, args ...string) (string, error) {
	return r.run(ctx, args...)
}

func (r rawTmux) runInteractiveMutation(ctx context.Context, output, errorOutput io.Writer, args ...string) error {
	return r.runInteractive(ctx, output, errorOutput, args...)
}
