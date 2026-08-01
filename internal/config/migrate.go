package config

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/moutansos/op/internal/domain"
)

type rawConfig struct {
	RepoDirectory  *string           `json:"repoDirectory"`
	PreferredShell *string           `json:"preferredShell"`
	Tmux           *rawTmuxConfig    `json:"tmux"`
	Stats          *rawStatsConfig   `json:"stats"`
	Server         *rawServerConfig  `json:"server"`
	Actions        *rawActionsConfig `json:"actions"`
	CustomEntries  *[]CustomEntry    `json:"customEntries"`
	CustomCommands *[]CustomCommand  `json:"customCommands"`

	PreferredShellLegacy *string `json:"preferedShell"`
	WSLRepoDirectory     *string `json:"wslRepoDirectory"`
	IsServer             *bool   `json:"isServer"`
}

type rawTmuxConfig struct {
	Session         *string `json:"session"`
	DashboardWindow *string `json:"dashboardWindow"`
	Socket          *string `json:"socket"`
	ShellPaneRows   *int    `json:"shellPaneRows"`
	DefaultProfile  *string `json:"defaultProfile"`
}

type rawStatsConfig struct {
	RefreshInterval     *Duration `json:"refreshInterval"`
	TmuxRefreshInterval *Duration `json:"tmuxRefreshInterval"`
}

type rawServerConfig struct {
	Enabled     *bool   `json:"enabled"`
	Listen      *string `json:"listen"`
	TokenFile   *string `json:"tokenFile"`
	TLSCertFile *string `json:"tlsCertFile"`
	TLSKeyFile  *string `json:"tlsKeyFile"`
}

type rawActionsConfig struct {
	GUIEditors *bool `json:"guiEditors"`
}

// Migrate applies defaults and maps legacy fields into the canonical in-memory shape.
// It does not expand or validate paths; Load performs the complete pipeline.
func Migrate(data []byte) (Config, []Warning, error) {
	var raw rawConfig
	if err := json.Unmarshal(data, &raw); err != nil {
		return Config{}, nil, domain.NewError(domain.ErrorCodeConfig, "config.decode", "decode configuration JSON", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return Config{}, nil, domain.NewError(domain.ErrorCodeConfig, "config.decode", "configuration must be a JSON object", err)
	}
	if fields == nil {
		return Config{}, nil, domain.NewError(domain.ErrorCodeConfig, "config.decode", "configuration must be a JSON object", nil)
	}

	warnings := unknownFieldWarnings(fields)
	config := Defaults()
	if raw.RepoDirectory != nil {
		config.RepoDirectory = *raw.RepoDirectory
	}
	if raw.PreferredShell != nil {
		config.PreferredShell = *raw.PreferredShell
	} else if raw.PreferredShellLegacy != nil {
		config.PreferredShell = *raw.PreferredShellLegacy
	}
	if raw.PreferredShellLegacy != nil {
		message := "preferedShell is deprecated; use preferredShell"
		if raw.PreferredShell != nil {
			message += "; canonical value takes precedence"
		}
		warnings = append(warnings, Warning{Code: WarningDeprecatedField, Path: "preferedShell", Message: message})
	}
	if raw.WSLRepoDirectory != nil {
		warnings = append(warnings, Warning{
			Code:    WarningMigration,
			Path:    "wslRepoDirectory",
			Message: "wslRepoDirectory is ignored; op now runs in Linux or WSL beside tmux",
		})
	}

	applyTmux(&config.Tmux, raw.Tmux)
	applyStats(&config.Stats, raw.Stats)
	applyServer(&config.Server, raw.Server)
	applyActions(&config.Actions, raw.Actions)
	if raw.IsServer != nil {
		if raw.Actions == nil || raw.Actions.GUIEditors == nil {
			config.Actions.GUIEditors = !*raw.IsServer
		}
		message := "isServer is deprecated; use actions.guiEditors"
		if raw.Actions != nil && raw.Actions.GUIEditors != nil {
			message += "; canonical value takes precedence"
		}
		warnings = append(warnings, Warning{Code: WarningDeprecatedField, Path: "isServer", Message: message})
	}
	if raw.CustomEntries != nil {
		config.CustomEntries = *raw.CustomEntries
		if config.CustomEntries == nil {
			config.CustomEntries = make([]CustomEntry, 0)
		}
	}
	if raw.CustomCommands != nil {
		config.CustomCommands = *raw.CustomCommands
		if config.CustomCommands == nil {
			config.CustomCommands = make([]CustomCommand, 0)
		}
	}

	sort.SliceStable(warnings, func(i, j int) bool {
		if warnings[i].Path == warnings[j].Path {
			return warnings[i].Code < warnings[j].Code
		}
		return warnings[i].Path < warnings[j].Path
	})
	return config, warnings, nil
}

func applyTmux(target *TmuxConfig, raw *rawTmuxConfig) {
	if raw == nil {
		return
	}
	if raw.Session != nil {
		target.Session = *raw.Session
	}
	if raw.DashboardWindow != nil {
		target.DashboardWindow = *raw.DashboardWindow
	}
	if raw.Socket != nil {
		target.Socket = *raw.Socket
	}
	if raw.ShellPaneRows != nil {
		target.ShellPaneRows = *raw.ShellPaneRows
	}
	if raw.DefaultProfile != nil {
		target.DefaultProfile = *raw.DefaultProfile
	}
}

func applyStats(target *StatsConfig, raw *rawStatsConfig) {
	if raw == nil {
		return
	}
	if raw.RefreshInterval != nil {
		target.RefreshInterval = *raw.RefreshInterval
	}
	if raw.TmuxRefreshInterval != nil {
		target.TmuxRefreshInterval = *raw.TmuxRefreshInterval
	}
}

func applyServer(target *ServerConfig, raw *rawServerConfig) {
	if raw == nil {
		return
	}
	if raw.Enabled != nil {
		target.Enabled = *raw.Enabled
	}
	if raw.Listen != nil {
		target.Listen = *raw.Listen
	}
	if raw.TokenFile != nil {
		target.TokenFile = *raw.TokenFile
	}
	if raw.TLSCertFile != nil {
		target.TLSCertFile = *raw.TLSCertFile
	}
	if raw.TLSKeyFile != nil {
		target.TLSKeyFile = *raw.TLSKeyFile
	}
}

func applyActions(target *ActionsConfig, raw *rawActionsConfig) {
	if raw != nil && raw.GUIEditors != nil {
		target.GUIEditors = *raw.GUIEditors
	}
}

func unknownFieldWarnings(root map[string]json.RawMessage) []Warning {
	var warnings []Warning
	collectUnknown(root, "", set("repoDirectory", "preferredShell", "tmux", "stats", "server", "actions", "customEntries", "customCommands", "preferedShell", "wslRepoDirectory", "isServer"), &warnings)
	collectObjectUnknown(root["tmux"], "tmux", set("session", "dashboardWindow", "socket", "shellPaneRows", "defaultProfile"), &warnings)
	collectObjectUnknown(root["stats"], "stats", set("refreshInterval", "tmuxRefreshInterval"), &warnings)
	collectObjectUnknown(root["server"], "server", set("enabled", "listen", "tokenFile", "tlsCertFile", "tlsKeyFile"), &warnings)
	collectObjectUnknown(root["actions"], "actions", set("guiEditors"), &warnings)
	collectArrayUnknown(root["customEntries"], "customEntries", set("name", "paths"), "paths", set("win", "linux"), &warnings)
	collectArrayUnknown(root["customCommands"], "customCommands", set("name", "command", "runInPreferredShell", "global"), "", nil, &warnings)
	return warnings
}

func collectObjectUnknown(data json.RawMessage, path string, allowed map[string]bool, warnings *[]Warning) {
	if len(data) == 0 || string(data) == "null" {
		return
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) == nil {
		collectUnknown(object, path, allowed, warnings)
	}
}

func collectArrayUnknown(data json.RawMessage, path string, allowed map[string]bool, child string, childAllowed map[string]bool, warnings *[]Warning) {
	if len(data) == 0 || string(data) == "null" {
		return
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(data, &items) != nil {
		return
	}
	for i, item := range items {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		collectUnknown(item, itemPath, allowed, warnings)
		if child != "" {
			collectObjectUnknown(item[child], itemPath+"."+child, childAllowed, warnings)
		}
	}
}

func collectUnknown(object map[string]json.RawMessage, path string, allowed map[string]bool, warnings *[]Warning) {
	for key := range object {
		if allowed[key] {
			continue
		}
		field := key
		if path != "" {
			field = path + "." + key
		}
		*warnings = append(*warnings, Warning{Code: WarningUnknownField, Path: field, Message: fmt.Sprintf("unknown configuration field %q is ignored", field)})
	}
}

func set(values ...string) map[string]bool {
	result := make(map[string]bool, len(values))
	for _, value := range values {
		result[value] = true
	}
	return result
}
