//go:build linux

package stats

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// newWindowsHostSource samples the Windows host only when op runs inside WSL.
func newWindowsHostSource() windowsHostSource {
	release, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil || !strings.Contains(strings.ToLower(string(release)), "microsoft") {
		return nil
	}
	return newWindowsHostSampler(func() (windowsHostProcess, error) {
		powershell, err := findPowerShell()
		if err != nil {
			return nil, err
		}
		return startPowerShellSampler(powershell)
	})
}

// findPowerShell prefers PATH, which WSL extends with Windows directories,
// and falls back to the system location for environments such as systemd
// services whose PATH omits them.
func findPowerShell() (string, error) {
	if path, err := exec.LookPath("powershell.exe"); err == nil {
		return path, nil
	}
	matches, _ := filepath.Glob("/mnt/*/[Ww][Ii][Nn][Dd][Oo][Ww][Ss]/[Ss]ystem32/WindowsPowerShell/v1.0/powershell.exe")
	for _, match := range matches {
		if info, err := os.Stat(match); err == nil && info.Mode().IsRegular() {
			return match, nil
		}
	}
	return "", errors.New("powershell.exe was not found on PATH or under /mnt/*/Windows/System32")
}
