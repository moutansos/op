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

// ProjectOpenMode identifies whether a project profile opens in the managed
// tmux workspace or in a separate graphical application.
type ProjectOpenMode string

const (
	ProjectOpenModeTmux ProjectOpenMode = "tmux"
	ProjectOpenModeGUI  ProjectOpenMode = "gui"
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
	ProjectID      string `json:"projectId"`
	Profile        string `json:"profile,omitempty"`
	NewInstance    bool   `json:"newInstance,omitempty"`
	DeferSelection bool   `json:"-"`
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
	Project Project         `json:"project"`
	Profile string          `json:"profile"`
	Mode    ProjectOpenMode `json:"mode"`
	Window  TmuxWindow      `json:"window,omitempty"`
	Reused  bool            `json:"reused"`
}

type RunProjectActionResult struct {
	Project Project `json:"project"`
	Action  string  `json:"action"`
	Started bool    `json:"started"`
}

type SelectPaneRequest struct {
	PaneID string `json:"paneId"`
}

type SelectPaneResult struct {
	Window TmuxWindow `json:"window"`
	Pane   TmuxPane   `json:"pane"`
}

type EnsureMainSessionResult struct {
	Session        TmuxSession `json:"session"`
	Created        bool        `json:"created"`
	Repaired       bool        `json:"repaired"`
	StartDashboard bool        `json:"startDashboard,omitempty"`
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
	Agents     []PaneAgentState   `json:"agents,omitempty"`
}

// AgentActivity describes what an interactive agent running in a pane is doing.
// Classification is observational: it is derived from the pane's foreground
// process and the bytes the agent has painted on its terminal, because agents
// that multiplex stdin and network sockets in one event loop are
// indistinguishable at the kernel level whether they block on a human or on an
// API response.
type AgentActivity string

const (
	// AgentActivityUnknown means the pane could not be classified, usually
	// because the foreground process or the pane contents were unreadable.
	AgentActivityUnknown AgentActivity = "unknown"
	// AgentActivityStarting means the agent was observed too recently to have
	// a quiescence baseline yet.
	AgentActivityStarting AgentActivity = "starting"
	// AgentActivityWorking means the agent is painting new output.
	AgentActivityWorking AgentActivity = "working"
	// AgentActivityAwaitingInput means the agent has gone quiet on a prompt and
	// is waiting for the operator to type something.
	AgentActivityAwaitingInput AgentActivity = "awaiting_input"
	// AgentActivityPermissionRequired means the agent is blocked on an explicit
	// permission prompt, such as access outside the workspace.
	AgentActivityPermissionRequired AgentActivity = "permission_required"
	// AgentActivityAwaitingApproval means the agent is blocked on an explicit
	// confirmation dialog, such as a tool-use confirmation.
	AgentActivityAwaitingApproval AgentActivity = "awaiting_approval"
	// AgentActivityIdle means the agent has been quiet long enough that it is
	// unlikely to be mid-task, but no prompt was recognized.
	AgentActivityIdle AgentActivity = "idle"
)

// NeedsAttention reports whether the activity represents an agent that has
// stopped making progress and is blocked on the operator.
func (a AgentActivity) NeedsAttention() bool {
	return a == AgentActivityAwaitingInput || a == AgentActivityPermissionRequired || a == AgentActivityAwaitingApproval
}

// String renders the activity for display.
func (a AgentActivity) String() string {
	switch a {
	case AgentActivityStarting:
		return "starting"
	case AgentActivityWorking:
		return "working"
	case AgentActivityAwaitingInput:
		return "awaiting input"
	case AgentActivityPermissionRequired:
		return "permission required"
	case AgentActivityAwaitingApproval:
		return "awaiting approval"
	case AgentActivityIdle:
		return "idle"
	default:
		return "unknown"
	}
}

// PaneAgentState is the classification of a single agent-bearing pane.
type PaneAgentState struct {
	PaneID     string `json:"paneId"`
	WindowName string `json:"windowName"`
	// AgentName is the configured definition name that matched the pane.
	AgentName string `json:"agentName"`
	// ForegroundPID is the leader of the terminal's foreground process group,
	// which is the process actually able to read from the pane.
	ForegroundPID     int32         `json:"foregroundPid,omitempty"`
	ForegroundCommand string        `json:"foregroundCommand,omitempty"`
	Activity          AgentActivity `json:"activity"`
	// Detail is the recognized prompt line that drove the classification.
	Detail string `json:"detail,omitempty"`
	// QuietSeconds is how long the pane has painted no new output.
	QuietSeconds uint64 `json:"quietSeconds"`
	// ChangedAt is when the pane last painted new output.
	ChangedAt time.Time `json:"changedAt"`
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
	// ProcessCount is the number of live processes in the pane's tree.
	ProcessCount int `json:"processCount,omitempty"`
	// ForegroundPID is the leader of the pane terminal's foreground process
	// group. It is the process that owns the pane's input, which is not
	// necessarily a direct child of the pane's root shell.
	ForegroundPID     int32  `json:"foregroundPid,omitempty"`
	ForegroundCommand string `json:"foregroundCommand,omitempty"`
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
