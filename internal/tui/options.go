package tui

import (
	"time"

	actionpolicy "github.com/moutansos/op/internal/action"
)

// Action is an additional configured project action shown in the action picker.
// ID is passed unchanged to domain.Service.RunProjectAction.
type Action struct {
	Name string
	ID   string
}

// Options configures dashboard behavior. Zero values use dashboard defaults.
type Options struct {
	DefaultProfile         string
	GUIEditors             bool
	ProjectRefreshInterval time.Duration
	TmuxRefreshInterval    time.Duration
	StatsRefreshInterval   time.Duration
	RefreshTimeout         time.Duration
	// OperationTimeout bounds open, create, clone, worktree, and nonterminal
	// actions. Terminal-handoff actions run until they exit or the dashboard's
	// context is canceled.
	OperationTimeout time.Duration
	Actions          []Action
}

func (o Options) withDefaults() Options {
	if o.DefaultProfile == "" {
		o.DefaultProfile = "nvim"
	}
	if o.ProjectRefreshInterval <= 0 {
		o.ProjectRefreshInterval = 5 * time.Second
	}
	if o.TmuxRefreshInterval <= 0 {
		o.TmuxRefreshInterval = 5 * time.Second
	}
	if o.StatsRefreshInterval <= 0 {
		o.StatsRefreshInterval = 2 * time.Second
	}
	if o.RefreshTimeout <= 0 {
		o.RefreshTimeout = 4 * time.Second
	}
	if o.OperationTimeout <= 0 {
		o.OperationTimeout = 30 * time.Minute
	}
	return o
}

func (o Options) projectActions() []Action {
	actions := []Action{
		{Name: "Neovim", ID: actionpolicy.NvimID},
		{Name: "Shell", ID: actionpolicy.ShellID},
		{Name: "Worktree", ID: actionpolicy.WorktreeID},
	}
	if o.GUIEditors {
		actions = append(actions, Action{Name: "VS Code", ID: actionpolicy.CodeID})
	}
	for _, action := range o.Actions {
		if action.Name != "" && action.ID != "" && actionpolicy.ValidateCustomName(action.ID) == nil {
			actions = append(actions, action)
		}
	}
	return actions
}
