// Package wslproxy delegates the native Windows command to the Linux op in WSL.
package wslproxy

import (
	"errors"
	"fmt"
	"io"
	"path"
	"runtime"
	"strings"

	"github.com/moutansos/op/internal/config"
)

const (
	// BinaryMarker identifies this project's Linux ELF without executing it.
	BinaryMarker = "github.com/moutansos/op:wsl-proxy-linux-elf:v1"
	resolverName = "op-wsl-resolver"
)

var retainedBinaryMarker = BinaryMarker

// KeepLinuxBinaryMarker retains BinaryMarker in non-Windows release binaries.
//
//go:noinline
func KeepLinuxBinaryMarker() {
	runtime.KeepAlive(retainedBinaryMarker)
}

// Invocation describes one direct wsl.exe execution. Capture is used only for
// wslpath; the final command inherits the console streams in the Windows host.
type Invocation struct {
	Executable string
	Args       []string
	Env        []string
	Capture    bool
}

// Result contains captured command output. Final delegated commands do not capture it.
type Result struct {
	Stdout []byte
	Stderr []byte
}

// Host isolates native process and environment access for Linux unit tests.
type Host interface {
	LookupEnv(string) (string, bool)
	Environ() []string
	WorkingDirectory() (string, error)
	AbsolutePath(string) (string, error)
	WSLExecutable() (string, error)
	Execute(Invocation) (Result, error)
}

// Run validates and delegates args, returning a process exit code without calling os.Exit.
func Run(args []string, host Host, stderr io.Writer) int {
	if active, _ := host.LookupEnv("OP_WSL_PROXY_ACTIVE"); active != "" {
		return report(stderr, 125, "recursive Windows-to-WSL delegation was blocked; Linux PATH resolved op back to op.exe")
	}
	if _, _, err := config.ExtractConfigPath(args); err != nil {
		return report(stderr, 2, "%v", err)
	}

	distro, _ := host.LookupEnv("OP_WSL_DISTRO")
	linuxOP, _ := host.LookupEnv("OP_WSL_OP")
	if linuxOP != "" && !path.IsAbs(linuxOP) {
		return report(stderr, 2, "OP_WSL_OP must be an absolute Linux path, got %q", linuxOP)
	}
	wsl, err := host.WSLExecutable()
	if err != nil {
		return report(stderr, 7, "%v", err)
	}
	cwd, err := host.WorkingDirectory()
	if err != nil {
		return report(stderr, 1, "determine the Windows working directory: %v", err)
	}

	environment := proxyEnvironment(host.Environ())
	translated, err := translateConfigArgs(args, func(value string) (string, error) {
		return host.AbsolutePath(value)
	}, func(value string) (string, error) {
		invocation := Invocation{
			Executable: wsl,
			Args:       append(wslOptions(distro), "--exec", "wslpath", "-a", "-u", value),
			Env:        environment,
			Capture:    true,
		}
		result, runErr := host.Execute(invocation)
		if runErr != nil {
			detail := strings.TrimSpace(string(result.Stderr))
			if detail != "" {
				return "", fmt.Errorf("wslpath failed for %q: %s: %w", value, detail, runErr)
			}
			return "", fmt.Errorf("wslpath failed for %q: %w", value, runErr)
		}
		translated := strings.TrimRight(string(result.Stdout), "\r\n")
		if translated == "" || !path.IsAbs(translated) || strings.ContainsAny(translated, "\r\n") {
			return "", fmt.Errorf("wslpath returned an invalid Linux path for %q", value)
		}
		return translated, nil
	})
	if err != nil {
		return report(stderr, 2, "%v", err)
	}

	commandArgs := append(wslOptions(distro), "--cd", cwd, "--exec", "/bin/sh", "-c", ResolverScript, resolverName, linuxOP)
	commandArgs = append(commandArgs, translated...)
	_, err = host.Execute(Invocation{Executable: wsl, Args: commandArgs, Env: environment})
	if err == nil {
		return 0
	}
	var exited interface{ ExitCode() int }
	if errors.As(err, &exited) {
		return mapExitCode(exited.ExitCode())
	}
	return report(stderr, 1, "start WSL delegation: %v", err)
}

func wslOptions(distribution string) []string {
	if distribution == "" {
		return nil
	}
	return []string{"--distribution", distribution}
}

func translateConfigArgs(args []string, absolute, translate func(string) (string, error)) ([]string, error) {
	if _, _, err := config.ExtractConfigPath(args); err != nil {
		return nil, err
	}
	result := append([]string(nil), args...)
	for index := 0; index < len(result); index++ {
		argument := result[index]
		if argument == "--" {
			break
		}
		valueIndex := -1
		value := ""
		equals := false
		switch {
		case argument == "--config":
			index++
			valueIndex = index
			value = result[index]
		case strings.HasPrefix(argument, "--config="):
			valueIndex = index
			value = strings.TrimPrefix(argument, "--config=")
			equals = true
		default:
			continue
		}
		if isLinuxAbsoluteConfigPath(value) {
			continue
		}
		windowsPath, err := absolute(value)
		if err != nil {
			return nil, fmt.Errorf("resolve Windows config path %q: %w", value, err)
		}
		linuxPath, err := translate(windowsPath)
		if err != nil {
			return nil, err
		}
		if equals {
			result[valueIndex] = "--config=" + linuxPath
		} else {
			result[valueIndex] = linuxPath
		}
	}
	return result, nil
}

func isLinuxAbsoluteConfigPath(value string) bool {
	return strings.HasPrefix(value, "/") && !strings.HasPrefix(value, "//")
}

func proxyEnvironment(environment []string) []string {
	result := append([]string(nil), environment...)
	wslEnv := normalizeWSLEnv(environmentValue(result, "WSLENV"))
	result = setEnvironmentValue(result, "WSLENV", wslEnv)
	return setEnvironmentValue(result, "OP_WSL_PROXY_ACTIVE", "1")
}

func normalizeWSLEnv(value string) string {
	owned := []string{"OP_API_TOKEN", "OP_REMOTE_URL", "OP_WSL_PROXY_ACTIVE"}
	entries := make([]string, 0)
	if value != "" {
		for _, entry := range strings.Split(value, ":") {
			name := strings.SplitN(entry, "/", 2)[0]
			isOwned := false
			for _, candidate := range owned {
				if strings.EqualFold(name, candidate) {
					isOwned = true
					break
				}
			}
			if !isOwned {
				entries = append(entries, entry)
			}
		}
	}
	for _, name := range owned {
		entries = append(entries, name+"/u")
	}
	return strings.Join(entries, ":")
}

func environmentValue(environment []string, name string) string {
	for index := len(environment) - 1; index >= 0; index-- {
		key, value, found := strings.Cut(environment[index], "=")
		if found && strings.EqualFold(key, name) {
			return value
		}
	}
	return ""
}

func setEnvironmentValue(environment []string, name, value string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if found && strings.EqualFold(key, name) {
			continue
		}
		result = append(result, entry)
	}
	return append(result, name+"="+value)
}

func mapExitCode(code int) int {
	if code == 130 || int64(code) == int64(-1073741510) || uint64(code) == uint64(0xc000013a) {
		return 130
	}
	if code < 0 {
		return 1
	}
	return code
}

func report(writer io.Writer, code int, format string, args ...any) int {
	fmt.Fprintf(writer, "error: wsl proxy: "+format+"\n", args...)
	return code
}
