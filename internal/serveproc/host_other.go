//go:build !linux

package serveproc

import "context"

// Host is unavailable outside Linux because discovery relies on procfs.
type Host struct{}

// New returns a Host whose operations report ErrUnsupported.
func New(string) *Host { return &Host{} }

// List reports ErrUnsupported.
func (*Host) List(func(Process) bool) ([]Process, error) { return nil, ErrUnsupported }

// Restart reports ErrUnsupported.
func (*Host) Restart(context.Context, Process) error { return ErrUnsupported }
