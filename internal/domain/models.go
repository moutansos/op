package domain

import "time"

// ProjectKind identifies how a project entered the catalog.
type ProjectKind string

const (
	ProjectKindRepository  ProjectKind = "repository"
	ProjectKindWorktree    ProjectKind = "worktree"
	ProjectKindCustomEntry ProjectKind = "custom_entry"
)

// GitState is the inexpensive repository state exposed to front ends.
type GitState string

const (
	GitStateUnknown       GitState = "unknown"
	GitStateClean         GitState = "clean"
	GitStateDirty         GitState = "dirty"
	GitStateNotRepository GitState = "not_repository"
)

// Project is the stable catalog representation shared by every front end.
type Project struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Path     string      `json:"path"`
	Kind     ProjectKind `json:"kind"`
	GitState GitState    `json:"gitState"`
	Tags     []string    `json:"tags,omitempty"`
}

type CreateProjectRequest struct {
	Name         string `json:"name"`
	OpenOnFinish bool   `json:"openOnFinish,omitempty"`
	Profile      string `json:"profile,omitempty"`
}

type CloneRequest struct {
	URL          string `json:"url"`
	Directory    string `json:"directory,omitempty"`
	OpenOnFinish bool   `json:"openOnFinish,omitempty"`
	Profile      string `json:"profile,omitempty"`
}

type CreateWorktreeRequest struct {
	ProjectID    string `json:"projectId"`
	Branch       string `json:"branch"`
	Directory    string `json:"directory,omitempty"`
	OpenOnFinish bool   `json:"openOnFinish,omitempty"`
	Profile      string `json:"profile,omitempty"`
}

type OpenProjectRequest struct {
	ProjectID   string `json:"projectId"`
	Profile     string `json:"profile,omitempty"`
	NewInstance bool   `json:"newInstance,omitempty"`
}

type RunProjectActionRequest struct {
	ProjectID string `json:"projectId"`
	Action    string `json:"action"`
}

type CreateProjectResult struct {
	Project Project            `json:"project"`
	Open    *OpenProjectResult `json:"open,omitempty"`
}

type CloneResult struct {
	Project Project            `json:"project"`
	Open    *OpenProjectResult `json:"open,omitempty"`
}

type CreateWorktreeResult struct {
	Project Project            `json:"project"`
	Open    *OpenProjectResult `json:"open,omitempty"`
}

type OpenProjectResult struct {
	Project Project    `json:"project"`
	Window  TmuxWindow `json:"window"`
	Reused  bool       `json:"reused"`
}

type RunProjectActionResult struct {
	Project Project `json:"project"`
	Action  string  `json:"action"`
	Started bool    `json:"started"`
}

type EnsureMainSessionResult struct {
	Session  TmuxSession `json:"session"`
	Created  bool        `json:"created"`
	Repaired bool        `json:"repaired"`
}

// TmuxSnapshot deliberately contains no gotmux types.
type TmuxSnapshot struct {
	CapturedAt time.Time    `json:"capturedAt"`
	Session    *TmuxSession `json:"session,omitempty"`
}

type TmuxSession struct {
	ID       string       `json:"id"`
	Name     string       `json:"name"`
	Attached bool         `json:"attached"`
	Windows  []TmuxWindow `json:"windows"`
}

type TmuxWindow struct {
	ID        string     `json:"id"`
	Index     int        `json:"index"`
	Name      string     `json:"name"`
	Active    bool       `json:"active"`
	ProjectID string     `json:"projectId,omitempty"`
	Profile   string     `json:"profile,omitempty"`
	Panes     []TmuxPane `json:"panes"`
}

type TmuxPane struct {
	ID             string `json:"id"`
	Index          int    `json:"index"`
	PID            int32  `json:"pid"`
	CurrentCommand string `json:"currentCommand"`
	CurrentPath    string `json:"currentPath"`
	Active         bool   `json:"active"`
	Dead           bool   `json:"dead"`
}

type StatsSnapshot struct {
	CapturedAt time.Time          `json:"capturedAt"`
	Host       HostStats          `json:"host"`
	Processes  []PaneProcessStats `json:"processes"`
}

type HostStats struct {
	CPUPercent    float64    `json:"cpuPercent"`
	MemoryUsed    uint64     `json:"memoryUsed"`
	MemoryTotal   uint64     `json:"memoryTotal"`
	LoadAverage   [3]float64 `json:"loadAverage"`
	UptimeSeconds uint64     `json:"uptimeSeconds"`
}

type PaneProcessStats struct {
	WindowName    string  `json:"windowName"`
	PaneID        string  `json:"paneId"`
	RootPID       int32   `json:"rootPid"`
	Command       string  `json:"command"`
	CPUPercent    float64 `json:"cpuPercent"`
	CPUAvailable  bool    `json:"cpuAvailable"`
	ResidentBytes uint64  `json:"residentBytes"`
	UptimeSeconds uint64  `json:"uptimeSeconds"`
	Dead          bool    `json:"dead"`
}

type JobStatus string

const (
	JobStatusQueued    JobStatus = "queued"
	JobStatusRunning   JobStatus = "running"
	JobStatusSucceeded JobStatus = "succeeded"
	JobStatusFailed    JobStatus = "failed"
	JobStatusCanceled  JobStatus = "canceled"
)

type Job struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Status     JobStatus      `json:"status"`
	CreatedAt  time.Time      `json:"createdAt"`
	StartedAt  *time.Time     `json:"startedAt,omitempty"`
	FinishedAt *time.Time     `json:"finishedAt,omitempty"`
	ProjectID  string         `json:"projectId,omitempty"`
	Result     map[string]any `json:"result,omitempty"`
	Error      *Error         `json:"error,omitempty"`
}
