package config

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/moutansos/op/internal/domain"
)

type rawConfig struct {
	RepoDirectory  *string                 `json:"repoDirectory"`
	PreferredShell *string                 `json:"preferredShell"`
	Tmux           *rawTmuxConfig          `json:"tmux"`
	Stats          *rawStatsConfig         `json:"stats"`
	Agents         *rawAgentsConfig        `json:"agents"`
	Notifications  *rawNotificationsConfig `json:"notifications"`
	Server         *rawServerConfig        `json:"server"`
	Actions        *rawActionsConfig       `json:"actions"`
	ProjectOpeners *[]ProjectOpener        `json:"projectOpeners"`
	CustomEntries  *[]CustomEntry          `json:"customEntries"`
	CustomCommands *[]CustomCommand        `json:"customCommands"`

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

type rawAgentsConfig struct {
	Enabled     *bool              `json:"enabled"`
	QuietAfter  *Duration          `json:"quietAfter"`
	IdleAfter   *Duration          `json:"idleAfter"`
	ScanLines   *int               `json:"scanLines"`
	Definitions *[]AgentDefinition `json:"definitions"`
}

type rawNotificationsConfig struct {
	Enabled           *bool                           `json:"enabled"`
	Debounce          *Duration                       `json:"debounce"`
	IgnoreDirectories *[]string                       `json:"ignoreDirectories"`
	OpenCode          *rawNotificationsOpenCodeConfig `json:"opencode"`
	Ingest            *rawNotificationsIngestConfig   `json:"ingest"`
	Providers         *[]NotificationProviderConfig   `json:"providers"`
}

type rawNotificationsOpenCodeConfig struct {
	BaseURL        *string `json:"baseUrl"`
	DesktopBaseURL *string `json:"desktopBaseUrl"`
	Username       *string `json:"username"`
	Password       *string `json:"password"`
}

type rawNotificationsIngestConfig struct {
	Enabled *bool `json:"enabled"`
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
	applyAgents(&config.Agents, raw.Agents)
	applyNotifications(&config.Notifications, raw.Notifications)
	applyServer(&config.Server, raw.Server)
	applyActions(&config.Actions, raw.Actions)
	if raw.ProjectOpeners != nil {
		config.ProjectOpeners = *raw.ProjectOpeners
		if config.ProjectOpeners == nil {
			config.ProjectOpeners = make([]ProjectOpener, 0)
		}
	} else if config.Tmux.DefaultProfile != "nvim" {
		// Before projectOpeners existed, defaultProfile was only a label for the
		// fixed Neovim layout. Preserve that configured label during migration.
		config.ProjectOpeners[0].ID = config.Tmux.DefaultProfile
	}
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

func applyAgents(target *AgentsConfig, raw *rawAgentsConfig) {
	if raw == nil {
		return
	}
	if raw.Enabled != nil {
		target.Enabled = *raw.Enabled
	}
	if raw.QuietAfter != nil {
		target.QuietAfter = *raw.QuietAfter
	}
	if raw.IdleAfter != nil {
		target.IdleAfter = *raw.IdleAfter
	}
	if raw.ScanLines != nil {
		target.ScanLines = *raw.ScanLines
	}
	if raw.Definitions != nil {
		target.Definitions = *raw.Definitions
		if target.Definitions == nil {
			target.Definitions = make([]AgentDefinition, 0)
		}
	}
}

func applyNotifications(target *NotificationsConfig, raw *rawNotificationsConfig) {
	if raw == nil {
		return
	}
	if raw.Enabled != nil {
		target.Enabled = *raw.Enabled
	}
	if raw.Debounce != nil {
		target.Debounce = *raw.Debounce
	}
	if raw.IgnoreDirectories != nil {
		target.IgnoreDirectories = *raw.IgnoreDirectories
		if target.IgnoreDirectories == nil {
			target.IgnoreDirectories = make([]string, 0)
		}
	}
	if raw.OpenCode != nil {
		if raw.OpenCode.BaseURL != nil {
			target.OpenCode.BaseURL = *raw.OpenCode.BaseURL
		}
		if raw.OpenCode.DesktopBaseURL != nil {
			target.OpenCode.DesktopBaseURL = *raw.OpenCode.DesktopBaseURL
		}
		if raw.OpenCode.Username != nil {
			target.OpenCode.Username = *raw.OpenCode.Username
		}
		if raw.OpenCode.Password != nil {
			target.OpenCode.Password = *raw.OpenCode.Password
		}
	}
	if raw.Ingest != nil && raw.Ingest.Enabled != nil {
		target.Ingest.Enabled = *raw.Ingest.Enabled
	}
	if raw.Providers != nil {
		target.Providers = *raw.Providers
		if target.Providers == nil {
			target.Providers = make([]NotificationProviderConfig, 0)
		}
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
	collectUnknown(root, "", set("repoDirectory", "preferredShell", "tmux", "stats", "agents", "notifications", "server", "actions", "projectOpeners", "customEntries", "customCommands", "preferedShell", "wslRepoDirectory", "isServer"), &warnings)
	collectObjectUnknown(root["tmux"], "tmux", set("session", "dashboardWindow", "socket", "shellPaneRows", "defaultProfile"), &warnings)
	collectObjectUnknown(root["stats"], "stats", set("refreshInterval", "tmuxRefreshInterval"), &warnings)
	collectObjectUnknown(root["agents"], "agents", set("enabled", "quietAfter", "idleAfter", "scanLines", "definitions"), &warnings)
	collectAgentDefinitionUnknown(root["agents"], &warnings)
	collectObjectUnknown(root["notifications"], "notifications", set("enabled", "debounce", "ignoreDirectories", "opencode", "ingest", "providers"), &warnings)
	collectObjectUnknown(notificationsObject(root["notifications"])["opencode"], "notifications.opencode", set("baseUrl", "desktopBaseUrl", "username", "password"), &warnings)
	collectObjectUnknown(notificationsObject(root["notifications"])["ingest"], "notifications.ingest", set("enabled"), &warnings)
	collectArrayUnknown(notificationsObject(root["notifications"])["providers"], "notifications.providers", set("type", "enabled", "webhookUrl", "url", "method", "headers", "token", "maxHops", "timeout"), "", nil, &warnings)
	collectObjectUnknown(root["server"], "server", set("enabled", "listen", "tokenFile", "tlsCertFile", "tlsKeyFile"), &warnings)
	collectObjectUnknown(root["actions"], "actions", set("guiEditors"), &warnings)
	collectArrayUnknown(root["projectOpeners"], "projectOpeners", set("id", "name", "mode", "command", "runInPreferredShell"), "", nil, &warnings)
	collectArrayUnknown(root["customEntries"], "customEntries", set("name", "paths"), "paths", set("win", "linux"), &warnings)
	collectArrayUnknown(root["customCommands"], "customCommands", set("name", "command", "runInPreferredShell", "global"), "", nil, &warnings)
	return warnings
}

// collectAgentDefinitionUnknown reaches one level into the agents object because
// its definitions are nested rather than living at the configuration root.
func collectAgentDefinitionUnknown(data json.RawMessage, warnings *[]Warning) {
	if len(data) == 0 || string(data) == "null" {
		return
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil {
		return
	}
	collectArrayUnknown(
		object["definitions"],
		"agents.definitions",
		set("name", "match", "busyPatterns", "promptPatterns", "approvalPatterns"),
		"", nil, warnings,
	)
}

func notificationsObject(data json.RawMessage) map[string]json.RawMessage {
	if len(data) == 0 || string(data) == "null" {
		return map[string]json.RawMessage{}
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || object == nil {
		return map[string]json.RawMessage{}
	}
	return object
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
