//go:build !windows

package serveproc

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// RestartSignal asks a running op serve to restart in place. SIGHUP is avoided
// because closing the terminal that hosts op serve also sends it.
const RestartSignal = syscall.SIGUSR1

// CheckFlag makes op serve validate its startup without listening.
const CheckFlag = "--check"

// NotifyRestart delivers restart requests until stop is called. Callers that
// re-execute should not call stop, so a repeated request during shutdown is
// absorbed instead of killing the process.
func NotifyRestart() (<-chan os.Signal, func()) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, RestartSignal)
	return signals, func() { signal.Stop(signals) }
}

// CheckReplacement runs the replacement binary with the current command line
// and CheckFlag, so a broken build or configuration is rejected while the
// current server keeps running.
func CheckReplacement(ctx context.Context) error {
	path, err := executablePath()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, append(slices.Clone(os.Args[1:]), CheckFlag)...).CombinedOutput()
	if err == nil {
		return nil
	}
	if detail := strings.TrimSpace(string(output)); detail != "" {
		return errors.New(strings.TrimPrefix(detail, "error: "))
	}
	return err
}

// Reexec replaces the current process with a fresh copy of the same command
// line, picking up a rebuilt binary at the original path.
func Reexec() error {
	path, err := executablePath()
	if err != nil {
		return err
	}
	return syscall.Exec(path, os.Args, NextGenerationEnv(os.Environ()))
}

// executablePath prefers the path the process was started with so a symlink
// that now points at a new build is followed again.
func executablePath() (string, error) {
	if len(os.Args) > 0 && os.Args[0] != "" {
		argument := os.Args[0]
		if strings.ContainsRune(argument, filepath.Separator) {
			if _, err := os.Stat(argument); err == nil {
				return argument, nil
			}
		} else if path, err := exec.LookPath(argument); err == nil {
			return path, nil
		}
	}
	return os.Executable()
}
