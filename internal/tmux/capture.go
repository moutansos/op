package tmux

import (
	"context"
	"os/exec"
	"strings"

	"github.com/moutansos/op/internal/domain"
)

// PaneCapturer reads the visible contents of panes on a tmux server.
//
// It is deliberately separate from Manager: capturing runs on the dashboard's
// refresh cadence and must never create a session, take a mutation lock, or
// otherwise perturb the server. It resolves the tmux executable once and then
// issues plain read-only commands.
type PaneCapturer struct {
	raw rawTmux
}

// NewPaneCapturer resolves tmux for the configured socket. It does not contact
// the server, so it succeeds even when no session exists yet.
func NewPaneCapturer(config ManagerConfig) (*PaneCapturer, error) {
	executable, err := exec.LookPath("tmux")
	if err != nil {
		return nil, domain.NewError(domain.ErrorCodeDependency, "tmux.capture", "tmux executable was not found", err)
	}
	return &PaneCapturer{raw: rawTmux{executable: executable, socket: config.Socket}}, nil
}

// CapturePane returns the pane's visible screen as plain text.
//
// Escape sequences are excluded, so the result is what a person reading the
// pane would see rather than the byte stream that produced it. Only the visible
// region is captured, never scrollback: a prompt that has scrolled off is no
// longer the pane's current state.
func (c *PaneCapturer) CapturePane(ctx context.Context, paneID string) (string, error) {
	if err := validatePaneID(paneID); err != nil {
		return "", err
	}
	output, err := c.raw.run(ctx, "capture-pane", "-p", "-t", paneID)
	if err != nil {
		return "", err
	}
	return strings.ReplaceAll(output, "\r\n", "\n"), nil
}
