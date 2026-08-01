package tmux

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"unicode"

	"github.com/moutansos/op/internal/domain"
)

func validateTmuxName(op, field, value string) error {
	if strings.TrimSpace(value) == "" {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, field, "must not be empty")
	}
	if strings.ContainsAny(value, ":.") {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, field, "must not contain ':' or '.'")
	}
	if unsafeGotmuxValue(value) {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, field, "must not contain controls or gotmux's '-:-' separator")
	}
	return nil
}

func validateGotmuxValue(op, field, value string) error {
	if strings.ContainsRune(value, '\x00') || unsafeGotmuxValue(value) {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, op, field, "must not contain controls or gotmux's '-:-' separator")
	}
	return nil
}

func unsafeGotmuxValue(value string) bool {
	return strings.Contains(value, "-:-") || strings.IndexFunc(value, unicode.IsControl) >= 0
}

func normalizeProjectName(op, name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", domain.FieldError(domain.ErrorCodeInvalidArgument, op, "project.name", "must not be empty")
	}
	if unsafeGotmuxValue(name) {
		return "", domain.FieldError(domain.ErrorCodeInvalidArgument, op, "project.name", "must not contain controls or gotmux's '-:-' separator")
	}

	name = strings.NewReplacer(":", "-", ".", "-").Replace(name)
	name = strings.Trim(name, " -")
	if name == "" {
		return "", domain.FieldError(domain.ErrorCodeInvalidArgument, op, "project.name", "has no usable tmux window characters")
	}
	return name, nil
}

func collisionName(base, projectID string, windows []taggedWindow) (string, error) {
	available := func(candidate string) bool {
		for _, window := range windows {
			if window.Name == candidate && window.ProjectID != projectID {
				return false
			}
		}
		return true
	}
	if available(base) {
		return base, nil
	}

	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(projectID)))[:8]
	candidate := base + "-" + hash
	if available(candidate) {
		return candidate, nil
	}
	for suffix := 2; suffix < 10_000; suffix++ {
		candidate = fmt.Sprintf("%s-%s-%d", base, hash, suffix)
		if available(candidate) {
			return candidate, nil
		}
	}
	return "", domain.NewError(domain.ErrorCodeConflict, "tmux.open_project_window", "could not allocate a collision-safe window name", nil)
}

func instanceName(base string, windows []taggedWindow) (string, error) {
	used := make(map[string]bool, len(windows))
	for _, window := range windows {
		used[window.Name] = true
	}
	for suffix := 2; suffix < 10_000; suffix++ {
		candidate := fmt.Sprintf("%s-%d", base, suffix)
		if !used[candidate] {
			return candidate, nil
		}
	}
	return "", domain.NewError(domain.ErrorCodeConflict, "tmux.open_project_window", "could not allocate another window instance", nil)
}
