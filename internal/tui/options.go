package tui

import (
	"time"

	"github.com/moutansos/op/internal/domain"
)

// Action is an item in the current-project selector used outside the dashboard.
type Action struct {
	Name        string
	ID          string
	Description string
	Search      string
}

// ProjectOpener is a configured dashboard destination for a selected project.
type ProjectOpener struct {
	ID   string
	Name string
	Mode domain.ProjectOpenMode
}

// Options configures dashboard behavior. Zero values use dashboard defaults.
type Options struct {
	DefaultProfile         string
	ProjectOpeners         []ProjectOpener
	ProjectRefreshInterval time.Duration
	TmuxRefreshInterval    time.Duration
	StatsRefreshInterval   time.Duration
	RefreshTimeout         time.Duration
	// OperationTimeout bounds open, create, clone, and worktree operations.
	OperationTimeout time.Duration
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

func (o Options) projectOpeners() []ProjectOpener {
	openers := make([]ProjectOpener, 0, len(o.ProjectOpeners))
	for _, opener := range o.ProjectOpeners {
		if opener.ID != "" && opener.Name != "" && (opener.Mode == domain.ProjectOpenModeTmux || opener.Mode == domain.ProjectOpenModeGUI) {
			openers = append(openers, opener)
		}
	}
	if len(openers) == 0 {
		openers = append(openers, ProjectOpener{ID: o.DefaultProfile, Name: "Neovim in tmux", Mode: domain.ProjectOpenModeTmux})
	}
	return openers
}
