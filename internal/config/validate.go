package config

import (
	"fmt"
	"net"
	"net/netip"
	"path/filepath"
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
