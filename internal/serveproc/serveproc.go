// Package serveproc finds running op serve processes and restarts them in place.
package serveproc

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// GenerationEnv is incremented on every in-place restart so an observer can
// tell that a process has replaced its image.
const GenerationEnv = "OP_SERVE_GENERATION"

var (
	// ErrUnsupported reports that process discovery is unavailable on this platform.
	ErrUnsupported = errors.New("finding running op serve processes is only supported on Linux")
	// ErrExited reports that the process exited instead of re-executing, which
	// is what builds without in-place restart support do.
	ErrExited = errors.New("process exited instead of restarting in place")
	// ErrExitedAfterRestart reports that the replacement process failed to start.
	ErrExitedAfterRestart = errors.New("process restarted but exited before listening")
	// ErrRestartRejected reports that the replacement failed its startup check
	// and the original server kept running.
	ErrRestartRejected = errors.New("restart rejected")
)

// RestartErrorPath is where a server records why it rejected a restart, so
// op restart can report it. It is empty when statusDir is unknown.
func RestartErrorPath(statusDir string, pid int) string {
	if statusDir == "" {
		return ""
	}
	return filepath.Join(statusDir, "serve-"+strconv.Itoa(pid)+".restart-error")
}

// WriteRestartError records a rejected restart for RestartErrorPath.
func WriteRestartError(statusDir string, pid int, cause error) error {
	path := RestartErrorPath(statusDir, pid)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(statusDir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(cause.Error()), 0o600)
}

func readRestartError(statusDir string, pid int) error {
	path := RestartErrorPath(statusDir, pid)
	if path == "" {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return fmt.Errorf("%w: %s", ErrRestartRejected, strings.TrimSpace(string(data)))
}

func clearRestartError(statusDir string, pid int) {
	if path := RestartErrorPath(statusDir, pid); path != "" {
		_ = os.Remove(path)
	}
}

// Process is a running op serve candidate.
type Process struct {
	PID        int
	Args       []string
	Executable string
	Generation string
	// Unit is the systemd service whose cgroup contains the process, if any.
	Unit string
	// UserUnit reports that Unit belongs to the user's systemd manager.
	UserUnit bool
}

// NextGenerationEnv returns env with GenerationEnv incremented.
func NextGenerationEnv(env []string) []string {
	result := make([]string, 0, len(env)+1)
	generation := 0
	for _, entry := range env {
		if value, found := strings.CutPrefix(entry, GenerationEnv+"="); found {
			generation, _ = strconv.Atoi(value)
			continue
		}
		result = append(result, entry)
	}
	return append(result, GenerationEnv+"="+strconv.Itoa(generation+1))
}

func generationFromEnviron(data []byte) string {
	for _, entry := range strings.Split(string(data), "\x00") {
		if value, found := strings.CutPrefix(entry, GenerationEnv+"="); found {
			return value
		}
	}
	return ""
}

// unitFromCgroup returns the systemd service that owns a /proc/PID/cgroup
// listing, and whether it is managed by a user manager (user@UID.service).
func unitFromCgroup(data string) (string, bool) {
	for _, line := range strings.Split(data, "\n") {
		parts := strings.SplitN(line, ":", 3)
		if len(parts) != 3 {
			continue
		}
		if (parts[0] != "0" || parts[1] != "") && parts[1] != "name=systemd" {
			continue
		}
		segments := strings.Split(strings.Trim(parts[2], "/"), "/")
		leaf := segments[len(segments)-1]
		if !strings.HasSuffix(leaf, ".service") {
			return "", false
		}
		user := slices.ContainsFunc(segments[:len(segments)-1], func(segment string) bool {
			return strings.HasPrefix(segment, "user@") && strings.HasSuffix(segment, ".service")
		})
		return leaf, user
	}
	return "", false
}
