package config

import (
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/moutansos/op/internal/action"
	"github.com/moutansos/op/internal/domain"
)

func Validate(config Config) error {
	if err := requireAbsolutePath("repoDirectory", config.RepoDirectory); err != nil {
		return err
	}
	if strings.TrimSpace(config.PreferredShell) == "" {
		return invalid("preferredShell", "must not be empty")
	}
	if containsControl(config.PreferredShell) {
		return invalid("preferredShell", "must not contain control characters")
	}
	if err := validateTmuxName("tmux.session", config.Tmux.Session); err != nil {
		return err
	}
	if err := validateTmuxName("tmux.dashboardWindow", config.Tmux.DashboardWindow); err != nil {
		return err
	}
	if config.Tmux.Socket != "" {
		if err := requireAbsolutePath("tmux.socket", config.Tmux.Socket); err != nil {
			return err
		}
	}
	if config.Tmux.ShellPaneRows <= 0 {
		return invalid("tmux.shellPaneRows", "must be greater than zero")
	}
	if strings.TrimSpace(config.Tmux.DefaultProfile) == "" {
		return invalid("tmux.defaultProfile", "must not be empty")
	}
	if err := validateProjectOpeners(config.ProjectOpeners, config.Tmux.DefaultProfile); err != nil {
		return err
	}
	if config.Stats.RefreshInterval.Duration <= 0 {
		return invalid("stats.refreshInterval", "must be greater than zero")
	}
	if config.Stats.TmuxRefreshInterval.Duration <= 0 {
		return invalid("stats.tmuxRefreshInterval", "must be greater than zero")
	}
	if err := validateAgents(config.Agents); err != nil {
		return err
	}
	if err := validateNotifications(config.Notifications); err != nil {
		return err
	}
	if err := validateServer(config.Server); err != nil {
		return err
	}
	if err := validateEntries(config.CustomEntries); err != nil {
		return err
	}
	if err := validateCommands(config.CustomCommands); err != nil {
		return err
	}
	return nil
}

func validateProjectOpeners(openers []ProjectOpener, defaultProfile string) error {
	if len(openers) == 0 {
		return invalid("projectOpeners", "must contain at least one opener")
	}
	ids := make(map[string]bool, len(openers))
	defaultFound := false
	for i, opener := range openers {
		prefix := fmt.Sprintf("projectOpeners[%d]", i)
		if opener.ID == "" || opener.ID != strings.TrimSpace(opener.ID) {
			return invalid(prefix+".id", "must not be empty or have surrounding whitespace")
		}
		if containsControl(opener.ID) || strings.Contains(opener.ID, "-:-") {
			return invalid(prefix+".id", "must not contain control characters or gotmux's '-:-' separator")
		}
		if ids[opener.ID] {
			return invalid(prefix+".id", "must be unique")
		}
		ids[opener.ID] = true
		defaultFound = defaultFound || opener.ID == defaultProfile
		if strings.TrimSpace(opener.Name) == "" {
			return invalid(prefix+".name", "must not be empty")
		}
		if containsControl(opener.Name) {
			return invalid(prefix+".name", "must not contain control characters")
		}
		if opener.Mode != domain.ProjectOpenModeTmux && opener.Mode != domain.ProjectOpenModeGUI {
			return invalid(prefix+".mode", "must be tmux or gui")
		}
		if strings.TrimSpace(opener.Command) == "" {
			return invalid(prefix+".command", "must not be empty")
		}
		if strings.ContainsRune(opener.Command, '\x00') || containsControl(opener.Command) {
			return invalid(prefix+".command", "must not contain control characters")
		}
	}
	if !defaultFound {
		return invalid("tmux.defaultProfile", "must match a configured project opener ID")
	}
	return nil
}

func validateTmuxName(field, value string) error {
	if value == "" {
		return invalid(field, "must not be empty")
	}
	if strings.ContainsAny(value, ":.") {
		return invalid(field, "must not contain ':' or '.'")
	}
	if strings.Contains(value, "-:-") {
		return invalid(field, "must not contain gotmux's '-:-' query separator")
	}
	if containsControl(value) {
		return invalid(field, "must not contain control characters")
	}
	return nil
}

func validateAgents(config AgentsConfig) error {
	if config.QuietAfter.Duration <= 0 {
		return invalid("agents.quietAfter", "must be greater than zero")
	}
	if config.IdleAfter.Duration < config.QuietAfter.Duration {
		return invalid("agents.idleAfter", "must not be shorter than agents.quietAfter")
	}
	if config.ScanLines <= 0 {
		return invalid("agents.scanLines", "must be greater than zero")
	}
	names := make(map[string]bool, len(config.Definitions))
	for i, definition := range config.Definitions {
		prefix := fmt.Sprintf("agents.definitions[%d]", i)
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			return invalid(prefix+".name", "must not be empty")
		}
		if containsControl(name) {
			return invalid(prefix+".name", "must not contain control characters")
		}
		key := strings.ToLower(name)
		if names[key] {
			return invalid(prefix+".name", "must be unique")
		}
		names[key] = true
		for j, match := range definition.Match {
			if strings.TrimSpace(match) == "" {
				return invalid(fmt.Sprintf("%s.match[%d]", prefix, j), "must not be empty")
			}
		}
		for field, patterns := range map[string][]string{
			"busyPatterns":     definition.BusyPatterns,
			"promptPatterns":   definition.PromptPatterns,
			"approvalPatterns": definition.ApprovalPatterns,
		} {
			for j, pattern := range patterns {
				if _, err := regexp.Compile(pattern); err != nil {
					return invalid(fmt.Sprintf("%s.%s[%d]", prefix, field, j), "must be a valid regular expression: "+err.Error())
				}
			}
		}
	}
	return nil
}

func validateNotifications(config NotificationsConfig) error {
	if !config.Enabled {
		return nil
	}
	if config.Debounce.Duration < 0 {
		return invalid("notifications.debounce", "must not be negative")
	}
	for i, directory := range config.IgnoreDirectories {
		if err := requireAbsolutePath(fmt.Sprintf("notifications.ignoreDirectories[%d]", i), directory); err != nil {
			return err
		}
	}
	hasOpenCode := strings.TrimSpace(config.OpenCode.BaseURL) != ""
	if !hasOpenCode && !config.Ingest.Enabled {
		return invalid("notifications", "must enable ingest or set notifications.opencode.baseUrl")
	}
	if hasOpenCode {
		if err := requireHTTPURL("notifications.opencode.baseUrl", config.OpenCode.BaseURL); err != nil {
			return err
		}
	}
	if strings.TrimSpace(config.OpenCode.DesktopBaseURL) != "" {
		if err := requireHTTPURL("notifications.opencode.desktopBaseUrl", config.OpenCode.DesktopBaseURL); err != nil {
			return err
		}
	}
	hasUser := strings.TrimSpace(config.OpenCode.Username) != ""
	hasPassword := config.OpenCode.Password != ""
	if hasUser != hasPassword {
		return invalid("notifications.opencode", "username and password must be configured together")
	}
	for i, provider := range config.Providers {
		if err := validateNotificationProvider(fmt.Sprintf("notifications.providers[%d]", i), provider); err != nil {
			return err
		}
	}
	return nil
}

func validateNotificationProvider(prefix string, provider NotificationProviderConfig) error {
	if strings.TrimSpace(provider.Type) == "" {
		return invalid(prefix+".type", "must not be empty")
	}
	switch provider.Type {
	case "discord", "msteams", "webhook", "parent":
	default:
		return invalid(prefix+".type", "must be discord, msteams, webhook, or parent")
	}
	if !provider.Enabled {
		return nil
	}
	switch provider.Type {
	case "discord", "msteams":
		return requireHTTPURL(prefix+".webhookUrl", provider.WebhookURL)
	case "webhook":
		if err := requireHTTPURL(prefix+".url", provider.URL); err != nil {
			return err
		}
		method := strings.ToUpper(strings.TrimSpace(provider.Method))
		if method != "" && method != "GET" && method != "POST" && method != "PUT" {
			return invalid(prefix+".method", "must be GET, POST, or PUT")
		}
	case "parent":
		if err := requireHTTPURL(prefix+".url", provider.URL); err != nil {
			return err
		}
		if provider.MaxHops < 0 {
			return invalid(prefix+".maxHops", "must not be negative")
		}
		if provider.Timeout.Duration < 0 {
			return invalid(prefix+".timeout", "must not be negative")
		}
	}
	return nil
}

func requireHTTPURL(field, value string) error {
	if strings.TrimSpace(value) == "" {
		return invalid(field, "must not be empty")
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" {
		return invalid(field, "must be a valid http or https URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return invalid(field, "must be a valid http or https URL")
	}
	return nil
}

func validateServer(server ServerConfig) error {
	host, portText, err := net.SplitHostPort(server.Listen)
	if err != nil {
		return invalid("server.listen", "must be a host:port address")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return invalid("server.listen", "port must be between 1 and 65535")
	}
	if (server.TLSCertFile == "") != (server.TLSKeyFile == "") {
		return invalid("server", "tlsCertFile and tlsKeyFile must be configured together")
	}
	for field, value := range map[string]string{
		"server.tokenFile":   server.TokenFile,
		"server.tlsCertFile": server.TLSCertFile,
		"server.tlsKeyFile":  server.TLSKeyFile,
	} {
		if value != "" {
			if err := requireAbsolutePath(field, value); err != nil {
				return err
			}
		}
	}
	if !isLoopbackHost(host) {
		if server.TLSCertFile == "" || server.TLSKeyFile == "" {
			return invalid("server", "TLS is required for a non-loopback listener")
		}
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.IsLoopback()
}

func validateEntries(entries []CustomEntry) error {
	names := make(map[string]bool, len(entries))
	for i, entry := range entries {
		prefix := fmt.Sprintf("customEntries[%d]", i)
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			return invalid(prefix+".name", "must not be empty")
		}
		if names[name] {
			return invalid(prefix+".name", "must be unique")
		}
		names[name] = true
		if err := requireAbsolutePath(prefix+".paths.linux", entry.Paths.Linux); err != nil {
			return err
		}
	}
	return nil
}

func validateCommands(commands []CustomCommand) error {
	names := make(map[string]bool, len(commands))
	for i, command := range commands {
		prefix := fmt.Sprintf("customCommands[%d]", i)
		name := strings.TrimSpace(command.Name)
		if name == "" {
			return invalid(prefix+".name", "must not be empty")
		}
		if names[name] {
			return invalid(prefix+".name", "must be unique")
		}
		names[name] = true
		if err := action.ValidateCustomName(command.Name); err != nil {
			return invalid(prefix+".name", err.Error())
		}
		if strings.TrimSpace(command.Command) == "" {
			return invalid(prefix+".command", "must not be empty")
		}
		if strings.ContainsRune(command.Command, '\x00') {
			return invalid(prefix+".command", "must not contain NUL")
		}
	}
	return nil
}

func requireAbsolutePath(field, value string) error {
	if value == "" {
		return invalid(field, "must not be empty")
	}
	if !filepath.IsAbs(value) {
		return invalid(field, "must be an absolute normalized path")
	}
	if value != filepath.Clean(value) {
		return invalid(field, "must be an absolute normalized path")
	}
	return nil
}

func containsControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}

func invalid(field, message string) error {
	return domain.FieldError(domain.ErrorCodeConfig, "config.validate", field, message)
}
