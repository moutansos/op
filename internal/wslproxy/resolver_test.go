//go:build !windows

package wslproxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const resolverHelperEnv = "OP_WSL_PROXY_RESOLVER_TEST_HELPER"

func TestResolverRunsValidatedELFWithExactArguments(t *testing.T) {
	candidate := currentResolverELF(t)
	symlink := filepath.Join(t.TempDir(), "linked-op")
	if err := os.Symlink(candidate, symlink); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	payload := []string{"", "plain", "'\";$()", "雪", `trailing\`, "trailing/"}
	command := resolverCommand(symlink, helperArguments(payload...)...)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("resolver failed: %v", err)
	}
	want := strings.Join(payload, "\n") + "\n"
	if string(output) != want {
		t.Fatalf("output = %q, want %q", output, want)
	}
}

func TestResolverRejectsCandidatesWithoutExecutingThem(t *testing.T) {
	directory := t.TempDir()
	current := currentResolverELF(t)
	sideEffect := filepath.Join(directory, "candidate-executed")
	hanging := writeResolverFile(t, directory, "interactive-op", fmt.Sprintf("#!/bin/sh\nprintf executed > %q\nsleep 30\n", sideEffect))
	tests := []struct {
		name      string
		candidate func(*testing.T) string
		message   string
	}{
		{name: "missing", candidate: func(*testing.T) string { return filepath.Join(directory, "missing") }, message: "not an executable file"},
		{name: "exe", candidate: func(t *testing.T) string {
			link := filepath.Join(directory, "op.ExE")
			if err := os.Symlink(current, link); err != nil {
				t.Fatalf("create symlink: %v", err)
			}
			return link
		}, message: "Windows .exe"},
		{name: "PE without extension", candidate: func(t *testing.T) string { return writeResolverFile(t, directory, "pe-op", "MZnot-linux") }, message: "Windows PE"},
		{name: "interactive candidate", candidate: func(*testing.T) string { return hanging }, message: "not an ELF"},
		{name: "1Password name", candidate: func(t *testing.T) string {
			onePassword := filepath.Join(directory, "one-password")
			if err := os.MkdirAll(onePassword, 0o755); err != nil {
				t.Fatalf("create 1Password directory: %v", err)
			}
			link := filepath.Join(onePassword, "op")
			if err := os.Symlink("/bin/true", link); err != nil {
				t.Fatalf("create symlink: %v", err)
			}
			return link
		}, message: "project marker"},
		{name: "symlink to PE", candidate: func(t *testing.T) string {
			target := writeResolverFile(t, directory, "pe-target", "MZnot-linux")
			link := filepath.Join(directory, "pe-link")
			if err := os.Symlink(target, link); err != nil {
				t.Fatalf("create symlink: %v", err)
			}
			return link
		}, message: "Windows PE"},
		{name: "trailing slash", candidate: func(*testing.T) string { return current + "/" }, message: "not an executable file"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			command := resolverCommandContext(ctx, test.candidate(t))
			var stderr bytes.Buffer
			command.Stderr = &stderr
			err := command.Run()
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) || exitErr.ExitCode() != 126 {
				t.Fatalf("error = %v, want exit 126; stderr = %q", err, stderr.String())
			}
			if !strings.Contains(stderr.String(), test.message) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.message)
			}
		})
	}
	if _, err := os.Stat(sideEffect); !os.IsNotExist(err) {
		t.Fatalf("unrelated candidate was executed: %v", err)
	}
}

func TestResolverUsesHomeFallbackWithoutLoadingProfiles(t *testing.T) {
	home := t.TempDir()
	localBin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(localBin, 0o755); err != nil {
		t.Fatalf("create local bin: %v", err)
	}
	if err := os.Symlink(currentResolverELF(t), filepath.Join(localBin, "op")); err != nil {
		t.Fatalf("create fallback op: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".profile"), []byte("printf 'PROFILE-NOISE\\n'\n"), 0o644); err != nil {
		t.Fatalf("write profile: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".zshrc"), []byte("export PATH=/zsh-only/path:$PATH\nprintf 'ZSH-NOISE\\n'\n"), 0o644); err != nil {
		t.Fatalf("write zshrc: %v", err)
	}

	pathDirectory := t.TempDir()
	sideEffect := filepath.Join(pathDirectory, "path-op-executed")
	writeResolverFile(t, pathDirectory, "op", fmt.Sprintf("#!/bin/sh\nprintf executed > %q\nsleep 30\n", sideEffect))
	payload := []string{"json", "雪"}
	command := resolverCommand("", helperArguments(payload...)...)
	command.Env = setTestEnvironment(command.Env, "HOME", home)
	command.Env = setTestEnvironment(command.Env, "PATH", pathDirectory+":/usr/bin:/bin")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("resolver fallback failed: %v", err)
	}
	if got, want := string(output), "json\n雪\n"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
	if _, err := os.Stat(sideEffect); !os.IsNotExist(err) {
		t.Fatalf("PATH candidate was executed: %v", err)
	}
}

func TestResolverExecutionHelper(t *testing.T) {
	if os.Getenv(resolverHelperEnv) != "1" {
		t.Skip("resolver helper process")
	}
	separator := -1
	for index, argument := range os.Args {
		if argument == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		os.Exit(2)
	}
	for _, argument := range os.Args[separator+1:] {
		fmt.Println(argument)
	}
	os.Exit(0)
}

func resolverCommand(candidate string, arguments ...string) *exec.Cmd {
	return resolverCommandContext(context.Background(), candidate, arguments...)
}

func resolverCommandContext(ctx context.Context, candidate string, arguments ...string) *exec.Cmd {
	commandArgs := []string{"-c", ResolverScript, resolverName, candidate}
	commandArgs = append(commandArgs, arguments...)
	command := exec.CommandContext(ctx, "/bin/sh", commandArgs...)
	command.Env = setTestEnvironment(os.Environ(), resolverHelperEnv, "1")
	command.Env = setTestEnvironment(command.Env, "OP_WSL_PROXY_ACTIVE", "1")
	return command
}

func helperArguments(payload ...string) []string {
	arguments := []string{"-test.run=^TestResolverExecutionHelper$", "--"}
	return append(arguments, payload...)
}

func currentResolverELF(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("locate test executable: %v", err)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatalf("read test executable: %v", err)
	}
	if !bytes.Contains(content, []byte(BinaryMarker)) {
		t.Fatalf("test ELF does not contain %q", BinaryMarker)
	}
	return executable
}

func writeResolverFile(t *testing.T, directory, name, content string) string {
	t.Helper()
	filename := filepath.Join(directory, name)
	if err := os.WriteFile(filename, []byte(content), 0o755); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return filename
}

func setTestEnvironment(environment []string, name, value string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if found && key == name {
			continue
		}
		result = append(result, entry)
	}
	return append(result, name+"="+value)
}
