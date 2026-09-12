// Package state maintains instance-local, full-replacement state snapshots.
// Forwarding coalesces pending snapshots to the latest value; it is not an event
// history. Receivers should deduplicate eventId and order sequence within epoch.
package state

import (
	"time"

	"github.com/moutansos/op/internal/domain"
	"github.com/moutansos/op/internal/notify"
)

type Options struct {
	InstanceID        string
	RefreshInterval   time.Duration
	HeartbeatInterval time.Duration
	StaleAfter        time.Duration
	ParentURL         string
	ParentToken       string
}

// Section retains its last successful data on error. Available means a sample
// has succeeded at least once, not that the dependency is currently reachable.
type Section[T any] struct {
	Data        T         `json:"data"`
	Available   bool      `json:"available"`
	Stale       bool      `json:"stale"`
	AttemptedAt time.Time `json:"attemptedAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Error       string    `json:"error,omitempty"`
}

type Snapshot struct {
	Version    int                       `json:"version"`
	InstanceID string                    `json:"instanceId"`
	Hostname   string                    `json:"hostname"`
	Epoch      string                    `json:"epoch"`
	Revision   uint64                    `json:"revision"`
	CapturedAt time.Time                 `json:"capturedAt"`
	Projects   Section[[]Project]        `json:"projects"`
	Tmux       Section[[]Window]         `json:"tmux"`
	Agents     Section[[]Agent]          `json:"agents"`
	Host       Section[domain.HostStats] `json:"host"`
}

type Project struct {
	ID       string             `json:"id"`
	NativeID string             `json:"nativeId"`
	Name     string             `json:"name"`
	Path     string             `json:"path"`
	Branch   string             `json:"branch,omitempty"`
	Kind     domain.ProjectKind `json:"kind"`
	GitState domain.GitState    `json:"gitState"`
	Tags     []string           `json:"tags,omitempty"`
}

type Window struct {
	Mode            domain.ProjectOpenMode `json:"mode"`
	ID              string                 `json:"id"`
	NativeID        string                 `json:"nativeId"`
	SessionID       string                 `json:"sessionId"`
	NativeSessionID string                 `json:"nativeSessionId"`
	SessionName     string                 `json:"sessionName"`
	SessionAttached bool                   `json:"sessionAttached"`
	Name            string                 `json:"name"`
	Index           int                    `json:"index"`
	Active          bool                   `json:"active"`
	ProjectID       string                 `json:"projectId,omitempty"`
	Profile         string                 `json:"profile,omitempty"`
	Panes           []Pane                 `json:"panes"`
}

type Pane struct {
	ID             string `json:"id"`
	NativeID       string `json:"nativeId"`
	ProjectID      string `json:"projectId,omitempty"`
	Index          int    `json:"index"`
	PID            int32  `json:"pid"`
	CurrentCommand string `json:"currentCommand"`
	CurrentPath    string `json:"currentPath"`
	Active         bool   `json:"active"`
	Dead           bool   `json:"dead"`
}

type Agent struct {
	ID              string               `json:"id"`
	NativeID        string               `json:"nativeId"`
	Source          string               `json:"source"`
	Provenance      string               `json:"provenance"`
	Coverage        string               `json:"coverage,omitempty"`
	Notification    *notify.Notification `json:"notification,omitempty"`
	ProjectID       string               `json:"projectId,omitempty"`
	NativeProjectID string               `json:"nativeProjectId,omitempty"`
	Directory       string               `json:"directory,omitempty"`
	PaneID          string               `json:"paneId,omitempty"`
	ForegroundPID   int32                `json:"foregroundPid,omitempty"`
	Activity        domain.AgentActivity `json:"activity"`
	Detail          string               `json:"detail,omitempty"`
	ObservedAt      time.Time            `json:"observedAt"`
	Stale           bool                 `json:"stale"`
	Terminated      bool                 `json:"terminated"`
}

type Envelope struct {
	Version    int       `json:"version"`
	InstanceID string    `json:"instanceId"`
	Epoch      string    `json:"epoch"`
	Sequence   uint64    `json:"sequence"`
	EventID    string    `json:"eventId"`
	OccurredAt time.Time `json:"occurredAt"`
	Type       string    `json:"type"`
	Payload    Snapshot  `json:"payload"`
}
