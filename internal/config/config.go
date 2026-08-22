package config

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/moutansos/op/internal/agents"
	"github.com/moutansos/op/internal/domain"
)

const FileName = "config.json"

// AgentDefinitions converts the configured agent profiles into detector
// definitions, falling back to op's built-in profiles when none are configured.
func (c Config) AgentDefinitions() []agents.Definition {
	if len(c.Agents.Definitions) == 0 {
		return agents.Builtins()
	}
	definitions := make([]agents.Definition, 0, len(c.Agents.Definitions))
	for _, definition := range c.Agents.Definitions {
		definitions = append(definitions, agents.Definition{
			Name:             definition.Name,
			Match:            definition.Match,
			BusyPatterns:     definition.BusyPatterns,
			PromptPatterns:   definition.PromptPatterns,
			ApprovalPatterns: definition.ApprovalPatterns,
		})
	}
	return definitions
}

type Config struct {
	RepoDirectory  string          `json:"repoDirectory"`
	PreferredShell string          `json:"preferredShell"`
	Tmux           TmuxConfig      `json:"tmux"`
	Stats          StatsConfig     `json:"stats"`
	Agents         AgentsConfig    `json:"agents"`
	Server         ServerConfig    `json:"server"`
	Actions        ActionsConfig   `json:"actions"`
	ProjectOpeners []ProjectOpener `json:"projectOpeners"`
	CustomEntries  []CustomEntry   `json:"customEntries"`
	CustomCommands []CustomCommand `json:"customCommands"`

	SourcePath    string `json:"-"`
	RootDirectory string `json:"-"`
}

type TmuxConfig struct {
	Session         string `json:"session"`
	DashboardWindow string `json:"dashboardWindow"`
	Socket          string `json:"socket"`
	ShellPaneRows   int    `json:"shellPaneRows"`
	DefaultProfile  string `json:"defaultProfile"`
}

type StatsConfig struct {
	RefreshInterval     Duration `json:"refreshInterval"`
	TmuxRefreshInterval Duration `json:"tmuxRefreshInterval"`
}

// AgentsConfig controls detection of interactive agents running in tmux panes.
//
// Detection reads pane contents, so it is a privacy-relevant capability and can
// be switched off wholesale.
type AgentsConfig struct {
	Enabled bool `json:"enabled"`
	// QuietAfter is how long a pane must paint nothing before its screen is
	// treated as settled rather than as a gap between frames. It should stay
	// below stats.refreshInterval's useful resolution.
	QuietAfter Duration `json:"quietAfter"`
	// IdleAfter is how long an unrecognized quiet pane stays quiet before it is
	// reported idle instead of assumed to be running a silent task.
	IdleAfter Duration `json:"idleAfter"`
	// ScanLines is how many trailing non-empty lines are pattern matched.
	ScanLines int `json:"scanLines"`
	// Definitions replaces the built-in agent profiles when non-empty.
	Definitions []AgentDefinition `json:"definitions"`
}

// AgentDefinition teaches op to recognize one agent. Pattern lists are unioned
// with op's generic patterns rather than replacing them.
type AgentDefinition struct {
	Name string `json:"name"`
	// Match holds command names compared case-insensitively against the pane's
	// foreground process.
	Match []string `json:"match"`
	// BusyPatterns are regular expressions that only appear while the agent is
	// mid-task, such as an "esc to interrupt" affordance.
	BusyPatterns []string `json:"busyPatterns,omitempty"`
	// PromptPatterns are regular expressions that appear when the agent is
	// offering an input line.
	PromptPatterns []string `json:"promptPatterns,omitempty"`
	// ApprovalPatterns are regular expressions that appear when the agent is
	// blocked on an explicit confirmation.
	ApprovalPatterns []string `json:"approvalPatterns,omitempty"`
}

type ServerConfig struct {
	Enabled     bool   `json:"enabled"`
	Listen      string `json:"listen"`
	TokenFile   string `json:"tokenFile"`
	TLSCertFile string `json:"tlsCertFile"`
	TLSKeyFile  string `json:"tlsKeyFile"`
}

type ActionsConfig struct {
	GUIEditors bool `json:"guiEditors"`
}

// ProjectOpener configures one way to open a project selected from the
// dashboard. GUI commands run directly unless RunInPreferredShell is enabled.
type ProjectOpener struct {
	ID                  string                 `json:"id"`
	Name                string                 `json:"name"`
	Mode                domain.ProjectOpenMode `json:"mode"`
	Command             string                 `json:"command"`
	RunInPreferredShell bool                   `json:"runInPreferredShell,omitempty"`
}

type CustomEntry struct {
	Name  string     `json:"name"`
	Paths EntryPaths `json:"paths"`
}

type EntryPaths struct {
	Windows string `json:"win,omitempty"`
	Linux   string `json:"linux"`
}

type CustomCommand struct {
	Name                string `json:"name"`
	Command             string `json:"command"`
	RunInPreferredShell bool   `json:"runInPreferredShell"`
	Global              bool   `json:"global,omitempty"`
}

// Duration marshals durations as the same strings accepted by time.ParseDuration.
type Duration struct{ time.Duration }

func NewDuration(value time.Duration) Duration { return Duration{Duration: value} }

func (d Duration) MarshalJSON() ([]byte, error) { return json.Marshal(d.String()) }

func (d *Duration) UnmarshalJSON(data []byte) error {
	var value string
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("duration must be a string: %w", err)
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return err
	}
	d.Duration = parsed
	return nil
}

func Defaults() Config {
	return Config{
		RepoDirectory:  "~/source/repos",
		PreferredShell: "zsh",
		Tmux: TmuxConfig{
			Session:         "code",
			DashboardWindow: "op",
			ShellPaneRows:   20,
			DefaultProfile:  "nvim",
		},
		Stats: StatsConfig{
			RefreshInterval:     NewDuration(2 * time.Second),
			TmuxRefreshInterval: NewDuration(5 * time.Second),
		},
		Agents: AgentsConfig{
			Enabled:     true,
			QuietAfter:  NewDuration(agents.DefaultQuietAfter),
			IdleAfter:   NewDuration(agents.DefaultIdleAfter),
			ScanLines:   agents.DefaultScanLines,
			Definitions: make([]AgentDefinition, 0),
		},
		Server: ServerConfig{
			Listen:    "127.0.0.1:8787",
			TokenFile: "~/.config/op/server-token",
		},
		Actions:        ActionsConfig{GUIEditors: false},
		ProjectOpeners: []ProjectOpener{{ID: "nvim", Name: "Neovim in tmux", Mode: domain.ProjectOpenModeTmux, Command: "nvim ."}},
		CustomEntries:  make([]CustomEntry, 0),
		CustomCommands: make([]CustomCommand, 0),
	}
}

type WarningCode string

const (
	WarningUnknownField    WarningCode = "unknown_field"
	WarningDeprecatedField WarningCode = "deprecated_field"
	WarningMigration       WarningCode = "migration"
)

type Warning struct {
	Code    WarningCode `json:"code"`
	Path    string      `json:"path"`
	Message string      `json:"message"`
}

type LoadResult struct {
	Config   Config
	Warnings []Warning
}
