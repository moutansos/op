//go:build windows && (amd64 || arm64)

package wslproxy

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"

	"golang.org/x/sys/windows"
)

type windowsHost struct{}

var _ Host = windowsHost{}

// NewWindowsHost returns the native process boundary used by cmd/op on Windows.
func NewWindowsHost() Host { return windowsHost{} }

func (windowsHost) LookupEnv(name string) (string, bool) { return os.LookupEnv(name) }
func (windowsHost) Environ() []string                    { return os.Environ() }
func (windowsHost) WorkingDirectory() (string, error)    { return os.Getwd() }

func (windowsHost) AbsolutePath(name string) (string, error) {
	return filepath.Abs(name)
}

func (windowsHost) WSLExecutable() (string, error) {
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		return "", fmt.Errorf("locate the Windows system directory: %w", err)
	}
	if !filepath.IsAbs(systemDirectory) {
		return "", fmt.Errorf("Windows system directory is not absolute: %s", systemDirectory)
	}
	executable := filepath.Join(filepath.Clean(systemDirectory), "wsl.exe")
	if !filepath.IsAbs(executable) {
		return "", fmt.Errorf("trusted WSL path is not absolute: %s", executable)
	}
	info, err := os.Stat(executable)
	if err != nil {
		return "", fmt.Errorf("WSL is unavailable at %s: %w", executable, err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("trusted WSL path is not a regular file: %s", executable)
	}
	return executable, nil
}

func (windowsHost) Execute(invocation Invocation) (Result, error) {
	command := exec.Command(invocation.Executable, invocation.Args...)
	command.Env = invocation.Env
	if invocation.Capture {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		err := command.Run()
		return Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, err
	}

	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	// Both processes share the console. WSL receives Ctrl+C while the Go
	// runtime handles it here and waits for WSL's translated exit status.
	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)
	return Result{}, command.Run()
}
