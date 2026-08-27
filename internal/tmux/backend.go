package tmux

import (
	"context"
	"io"
)

type sessionState struct {
	ID       string
	Name     string
	Attached bool
}

type windowState struct {
	ID     string
	Index  int
	Name   string
	Active bool
}

type paneState struct {
	ID             string
	Index          int
	PID            int32
	CurrentCommand string
	CurrentPath    string
	Active         bool
	Dead           bool
	AtTop          bool
	AtBottom       bool
	Height         int
}

type clientState struct {
	Name       string
	ActivePane string
}

type windowCreationVerificationError struct {
	err error
}

func (e *windowCreationVerificationError) Error() string { return e.err.Error() }
func (e *windowCreationVerificationError) Unwrap() error { return e.err }

// tmuxClient is intentionally state-oriented. Command and compatibility types
// never cross this boundary, and tests can model silent mutations.
type tmuxClient interface {
	Session(context.Context, string) (*sessionState, error)
	CreateSession(context.Context, string, string, string) error
	ListWindows(context.Context, string) ([]windowState, error)
	CreateWindow(context.Context, string, string, string, string) (string, error)
	RenameWindow(context.Context, string, string) error
	KillWindow(context.Context, string) error
	WindowExists(context.Context, string) (bool, error)
	SelectWindow(context.Context, string) error
	MoveWindow(context.Context, string, string, int) error
	SwapWindow(context.Context, string, string, int) error

	ListPanes(context.Context, string) ([]paneState, error)
	SplitPane(context.Context, string, string, string) error
	ResizePane(context.Context, string, int) error
	SelectPane(context.Context, string) error
	RespawnPane(context.Context, string, string) error
	KillPane(context.Context, string) error
	PaneExists(context.Context, string) (bool, error)

	SetWindowOption(context.Context, string, string, string) error
	WindowOption(context.Context, string, string) (string, bool, error)
	SetServerOption(context.Context, string, string) error
	ServerOption(context.Context, string) (string, bool, error)
	BindKey(context.Context, string, string, ...string) error
	KeyBinding(context.Context, string, string) (string, bool, error)
	SessionOption(context.Context, string, string) (string, bool, error)

	CurrentWindow(context.Context, string) (string, error)
	CurrentWindowName(context.Context, string) (string, error)
	ListClients(context.Context) ([]clientState, error)
	ClientSession(context.Context, string) (string, error)
	SwitchClient(context.Context, string, string) error
	Attach(context.Context, string, string, io.Writer, io.Writer) error
}
