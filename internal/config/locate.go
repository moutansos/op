package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/moutansos/op/internal/domain"
)

type LocateOptions struct {
	ExplicitPath   string
	UserConfigDir  string
	RepositoryRoot string
}

// ExtractConfigPath removes one global --config flag from arguments.
func ExtractConfigPath(args []string) (string, []string, error) {
	remaining := make([]string, 0, len(args))
	var path string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			remaining = append(remaining, args[i:]...)
			break
		}
		var value string
		switch {
		case arg == "--config":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				return "", nil, domain.FieldError(domain.ErrorCodeInvalidArgument, "config.flag", "--config", "requires a path")
			}
			i++
			value = args[i]
		case strings.HasPrefix(arg, "--config="):
			value = strings.TrimPrefix(arg, "--config=")
		default:
			remaining = append(remaining, arg)
			continue
		}
		if strings.TrimSpace(value) == "" {
			return "", nil, domain.FieldError(domain.ErrorCodeInvalidArgument, "config.flag", "--config", "requires a non-empty path")
		}
		if path != "" {
			return "", nil, domain.FieldError(domain.ErrorCodeInvalidArgument, "config.flag", "--config", "may only be specified once")
		}
		path = value
	}
	return path, remaining, nil
}

// Locate searches an explicit path, the platform user config directory, then the repository root.
func Locate(options LocateOptions) (string, error) {
	if options.ExplicitPath != "" {
		path, err := absolutePath(options.ExplicitPath)
		if err != nil {
			return "", domain.ResourceError(domain.ErrorCodeConfig, "config.locate", options.ExplicitPath, "resolve explicit config path", err)
		}
		if err := requireConfigFile(path); err != nil {
			return "", err
		}
		return path, nil
	}

	userDir := options.UserConfigDir
	if userDir == "" {
		var err error
		userDir, err = os.UserConfigDir()
		if err != nil {
			return "", domain.NewError(domain.ErrorCodeConfig, "config.locate", "determine user config directory", err)
		}
	}
	repoRoot := options.RepositoryRoot
	if repoRoot == "" {
		var err error
		repoRoot, err = os.Getwd()
		if err != nil {
			return "", domain.NewError(domain.ErrorCodeConfig, "config.locate", "determine repository fallback directory", err)
		}
	}

	candidates := []string{
		filepath.Join(userDir, "op", FileName),
		filepath.Join(repoRoot, FileName),
	}
	attempted := make([]string, 0, len(candidates))
	seen := make(map[string]bool, len(candidates))
	for _, candidate := range candidates {
		path, err := absolutePath(candidate)
		if err != nil {
			return "", domain.ResourceError(domain.ErrorCodeConfig, "config.locate", candidate, "resolve config candidate", err)
		}
		if seen[path] {
			continue
		}
		seen[path] = true
		attempted = append(attempted, path)
		info, err := os.Stat(path)
		if err == nil {
			if !info.Mode().IsRegular() {
				return "", domain.ResourceError(domain.ErrorCodeConfig, "config.locate", path, "config path is not a regular file", nil)
			}
			return path, nil
		}
		if !os.IsNotExist(err) {
			return "", domain.ResourceError(domain.ErrorCodeConfig, "config.locate", path, "inspect config candidate", err)
		}
	}

	return "", domain.ResourceError(
		domain.ErrorCodeNotFound,
		"config.locate",
		FileName,
		fmt.Sprintf("configuration file not found; tried %s", strings.Join(attempted, ", ")),
		nil,
	)
}

func requireConfigFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		code := domain.ErrorCodeConfig
		if os.IsNotExist(err) {
			code = domain.ErrorCodeNotFound
		}
		return domain.ResourceError(code, "config.locate", path, "explicit configuration file is unavailable", err)
	}
	if !info.Mode().IsRegular() {
		return domain.ResourceError(domain.ErrorCodeConfig, "config.locate", path, "config path is not a regular file", nil)
	}
	return nil
}

func absolutePath(path string) (string, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}
