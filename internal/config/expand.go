package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/moutansos/op/internal/domain"
)

var environmentVariable = regexp.MustCompile(`\$env:([A-Za-z_][A-Za-z0-9_]*)|\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

type ExpandOptions struct {
	BaseDirectory string
	HomeDirectory string
	LookupEnv     func(string) (string, bool)
}

// Expand normalizes every runtime path. Custom command strings are intentionally untouched.
func Expand(config *Config, options ExpandOptions) error {
	if config == nil {
		return domain.NewError(domain.ErrorCodeConfig, "config.expand", "configuration is nil", nil)
	}
	options, err := completeExpandOptions(options)
	if err != nil {
		return err
	}

	if config.RepoDirectory, err = expandPath(config.RepoDirectory, options, true); err != nil {
		return pathExpansionError("repoDirectory", err)
	}
	if config.Tmux.Socket != "" {
		if config.Tmux.Socket, err = expandPath(config.Tmux.Socket, options, true); err != nil {
			return pathExpansionError("tmux.socket", err)
		}
	}
	paths := []*struct {
		field string
		value *string
	}{
		{field: "server.tokenFile", value: &config.Server.TokenFile},
		{field: "server.tlsCertFile", value: &config.Server.TLSCertFile},
		{field: "server.tlsKeyFile", value: &config.Server.TLSKeyFile},
	}
	for _, path := range paths {
		if *path.value == "" {
			continue
		}
		*path.value, err = expandPath(*path.value, options, true)
		if err != nil {
			return pathExpansionError(path.field, err)
		}
	}
	for i := range config.Notifications.IgnoreDirectories {
		if config.Notifications.IgnoreDirectories[i] == "" {
			continue
		}
		config.Notifications.IgnoreDirectories[i], err = expandPath(config.Notifications.IgnoreDirectories[i], options, true)
		if err != nil {
			return pathExpansionError(fmt.Sprintf("notifications.ignoreDirectories[%d]", i), err)
		}
	}
	for i := range config.CustomEntries {
		entry := &config.CustomEntries[i]
		if entry.Paths.Linux != "" {
			entry.Paths.Linux, err = expandPath(entry.Paths.Linux, options, true)
			if err != nil {
				return pathExpansionError(fmt.Sprintf("customEntries[%d].paths.linux", i), err)
			}
		}
		if entry.Paths.Windows != "" {
			entry.Paths.Windows, err = expandPath(entry.Paths.Windows, options, false)
			if err != nil {
				return pathExpansionError(fmt.Sprintf("customEntries[%d].paths.win", i), err)
			}
		}
	}
	return nil
}

// ExpandPath expands a runtime path using the current user and environment.
func ExpandPath(value, baseDirectory string) (string, error) {
	options, err := completeExpandOptions(ExpandOptions{BaseDirectory: baseDirectory})
	if err != nil {
		return "", err
	}
	return expandPath(value, options, true)
}

func completeExpandOptions(options ExpandOptions) (ExpandOptions, error) {
	var err error
	if options.BaseDirectory == "" {
		options.BaseDirectory, err = os.Getwd()
		if err != nil {
			return options, domain.NewError(domain.ErrorCodeConfig, "config.expand", "determine base directory", err)
		}
	}
	options.BaseDirectory, err = filepath.Abs(options.BaseDirectory)
	if err != nil {
		return options, domain.NewError(domain.ErrorCodeConfig, "config.expand", "normalize base directory", err)
	}
	if options.HomeDirectory == "" {
		options.HomeDirectory, err = os.UserHomeDir()
		if err != nil {
			return options, domain.NewError(domain.ErrorCodeConfig, "config.expand", "determine home directory", err)
		}
	}
	if options.LookupEnv == nil {
		options.LookupEnv = os.LookupEnv
	}
	return options, nil
}

func expandPath(value string, options ExpandOptions, strictEnvironment bool) (string, error) {
	expanded, err := expandEnvironment(value, options.LookupEnv, strictEnvironment)
	if err != nil {
		return "", err
	}
	if expanded == "~" {
		expanded = options.HomeDirectory
	} else if strings.HasPrefix(expanded, "~/") || strings.HasPrefix(expanded, `~\`) {
		expanded = filepath.Join(options.HomeDirectory, expanded[2:])
	} else if strings.HasPrefix(expanded, "~") {
		return "", fmt.Errorf("user-specific home expansion is not supported: %q", value)
	}

	// Windows-only custom paths are retained when they are not meaningful to filepath on Linux.
	if !strictEnvironment && (isWindowsAbsolute(expanded) || strings.Contains(expanded, `$`)) {
		return expanded, nil
	}
	if !filepath.IsAbs(expanded) {
		expanded = filepath.Join(options.BaseDirectory, expanded)
	}
	absolute, err := filepath.Abs(expanded)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func expandEnvironment(value string, lookup func(string) (string, bool), strict bool) (string, error) {
	var missing string
	expanded := environmentVariable.ReplaceAllStringFunc(value, func(match string) string {
		parts := environmentVariable.FindStringSubmatch(match)
		name := parts[1]
		if name == "" {
			name = parts[2]
		}
		if name == "" {
			name = parts[3]
		}
		if replacement, ok := lookup(name); ok {
			return replacement
		}
		if strict && missing == "" {
			missing = name
		}
		return match
	})
	if missing != "" {
		return "", fmt.Errorf("environment variable %q is not set", missing)
	}
	return expanded, nil
}

func isWindowsAbsolute(path string) bool {
	return len(path) >= 3 && ((path[0] >= 'A' && path[0] <= 'Z') || (path[0] >= 'a' && path[0] <= 'z')) && path[1] == ':' && (path[2] == '\\' || path[2] == '/') || strings.HasPrefix(path, `\\`)
}

func pathExpansionError(field string, err error) error {
	return domain.FieldError(domain.ErrorCodeConfig, "config.expand", field, err.Error())
}
