package wslproxy

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeHost struct {
	lookup      map[string]string
	environment []string
	cwd         string
	cwdErr      error
	wsl         string
	wslErr      error
	absoluteErr error
	absolute    []string
	results     []Result
	errors      []error
	calls       []Invocation
}

func (host *fakeHost) LookupEnv(name string) (string, bool) {
	value, found := host.lookup[name]
	return value, found
}

func (host *fakeHost) Environ() []string                 { return append([]string(nil), host.environment...) }
func (host *fakeHost) WorkingDirectory() (string, error) { return host.cwd, host.cwdErr }
func (host *fakeHost) WSLExecutable() (string, error)    { return host.wsl, host.wslErr }

func (host *fakeHost) AbsolutePath(name string) (string, error) {
	host.absolute = append(host.absolute, name)
	if host.absoluteErr != nil {
		return "", host.absoluteErr
	}
	if (len(name) >= 3 && name[1] == ':') || strings.HasPrefix(name, `\`) {
		return name, nil
	}
	return strings.TrimRight(host.cwd, `\/`) + `\` + name, nil
}

func (host *fakeHost) Execute(invocation Invocation) (Result, error) {
	host.calls = append(host.calls, invocation)
	index := len(host.calls) - 1
	var result Result
	if index < len(host.results) {
		result = host.results[index]
	}
	if index < len(host.errors) {
		return result, host.errors[index]
	}
	return result, nil
}

type exitError int

func (err exitError) Error() string { return "exit" }
func (err exitError) ExitCode() int { return int(err) }

func TestRunPlansDelegationWithoutReencodingArguments(t *testing.T) {
	arguments := []string{"open", "", `$(touch nope)`, "雪", `trailing\\`, "trailing/", "--", "--config", `C:\\not-translated.json`}
	host := &fakeHost{
		lookup: map[string]string{
			"OP_WSL_DISTRO": "Ubuntu Dev; harmless",
			"OP_WSL_OP":     "/home/ben/bin/op",
		},
		environment: []string{
			"PATH=windows-path",
			`SystemRoot=C:\attacker`,
			"OP_API_TOKEN=secret",
			"OP_REMOTE_URL=https://op.example/雪",
			"WSLENV=EXISTING/p:OP_API_TOKEN/w",
		},
		cwd: `C:\Users\Ben\source folder`,
		wsl: `C:\Windows\System32\wsl.exe`,
	}
	var stderr bytes.Buffer
	if code := Run(arguments, host, &stderr); code != 0 {
		t.Fatalf("Run() code = %d, stderr = %q", code, stderr.String())
	}
	if len(host.calls) != 1 {
		t.Fatalf("Execute calls = %d, want 1", len(host.calls))
	}
	invocation := host.calls[0]
	wantPrefix := []string{
		"--distribution", "Ubuntu Dev; harmless",
		"--cd", `C:\Users\Ben\source folder`,
		"--exec", "/bin/sh", "-c", ResolverScript, resolverName, "/home/ben/bin/op",
	}
	wantArgs := append(wantPrefix, arguments...)
	if !reflect.DeepEqual(invocation.Args, wantArgs) {
		t.Fatalf("args = %#v, want %#v", invocation.Args, wantArgs)
	}
	if invocation.Executable != host.wsl || invocation.Capture {
		t.Fatalf("invocation = %#v", invocation)
	}
	if strings.Contains(invocation.Executable, "attacker") {
		t.Fatalf("mutable SystemRoot influenced executable: %q", invocation.Executable)
	}
	if got := environmentValue(invocation.Env, "WSLENV"); got != "EXISTING/p:OP_API_TOKEN/u:OP_REMOTE_URL/u:OP_WSL_PROXY_ACTIVE/u" {
		t.Fatalf("WSLENV = %q", got)
	}
	if got := environmentValue(invocation.Env, "OP_WSL_PROXY_ACTIVE"); got != "1" {
		t.Fatalf("OP_WSL_PROXY_ACTIVE = %q", got)
	}
	if got := environmentValue(invocation.Env, "OP_API_TOKEN"); got != "secret" {
		t.Fatalf("OP_API_TOKEN = %q", got)
	}
}

func TestRunTranslatesWindowsConfigFormsAnywhereBeforeSeparator(t *testing.T) {
	host := &fakeHost{
		lookup:      map[string]string{"OP_WSL_DISTRO": "Debian"},
		environment: []string{"WSLENV=KEEP/u"},
		cwd:         `D:\work`,
		wsl:         `C:\Windows\System32\wsl.exe`,
		results: []Result{
			{Stdout: []byte("/mnt/c/Users/Ben/config one.json\r\n")},
			{},
		},
	}
	arguments := []string{"projects", "--config", `C:\Users\Ben\config one.json`, "--json"}
	var stderr bytes.Buffer
	if code := Run(arguments, host, &stderr); code != 0 {
		t.Fatalf("Run() code = %d, stderr = %q", code, stderr.String())
	}
	if len(host.calls) != 2 {
		t.Fatalf("Execute calls = %d, want 2", len(host.calls))
	}
	wantTranslate := []string{"--distribution", "Debian", "--exec", "wslpath", "-a", "-u", `C:\Users\Ben\config one.json`}
	if !reflect.DeepEqual(host.calls[0].Args, wantTranslate) || !host.calls[0].Capture {
		t.Fatalf("translation invocation = %#v", host.calls[0])
	}
	final := host.calls[1].Args
	wantTail := []string{"projects", "--config", "/mnt/c/Users/Ben/config one.json", "--json"}
	if !reflect.DeepEqual(final[len(final)-len(wantTail):], wantTail) {
		t.Fatalf("delegated tail = %#v, want %#v", final[len(final)-len(wantTail):], wantTail)
	}
}

func TestRunTranslatesRelativeConfigAgainstWindowsCWD(t *testing.T) {
	host := &fakeHost{
		lookup: map[string]string{}, cwd: `D:\work\project`, wsl: `C:\Windows\System32\wsl.exe`,
		results: []Result{{Stdout: []byte("/mnt/d/work/project/config.json\n")}, {}},
	}
	var stderr bytes.Buffer
	if code := Run([]string{"projects", "--config=config.json"}, host, &stderr); code != 0 {
		t.Fatalf("Run() code = %d, stderr = %q", code, stderr.String())
	}
	wantPath := `D:\work\project\config.json`
	if got := host.calls[0].Args[len(host.calls[0].Args)-1]; got != wantPath {
		t.Fatalf("wslpath input = %q, want %q", got, wantPath)
	}
	wantConfig := "--config=/mnt/d/work/project/config.json"
	if got := host.calls[1].Args[len(host.calls[1].Args)-1]; got != wantConfig {
		t.Fatalf("delegated config = %q, want %q", got, wantConfig)
	}
}

func TestTranslateConfigArgs(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		want      []string
		wantAbs   []string
		wantCalls []string
		wantErr   bool
	}{
		{name: "drive rooted with spaces", args: []string{"open", `--config=C:\Users\Ben\config files\op.json`}, want: []string{"open", "--config=/translated/"}, wantAbs: []string{`C:\Users\Ben\config files\op.json`}, wantCalls: []string{`ABS:C:\Users\Ben\config files\op.json`}},
		{name: "drive root", args: []string{`--config=C:\`}, want: []string{"--config=/translated/"}, wantAbs: []string{`C:\`}, wantCalls: []string{`ABS:C:\`}},
		{name: "drive relative", args: []string{`--config=C:config.json`}, want: []string{"--config=/translated/"}, wantAbs: []string{`C:config.json`}, wantCalls: []string{`ABS:C:config.json`}},
		{name: "forward UNC", args: []string{`--config=//server/share/config.json`}, want: []string{"--config=/translated/"}, wantAbs: []string{`//server/share/config.json`}, wantCalls: []string{`ABS://server/share/config.json`}},
		{name: "backslash UNC", args: []string{`--config=\\server\share\config.json`}, want: []string{"--config=/translated/"}, wantAbs: []string{`\\server\share\config.json`}, wantCalls: []string{`ABS:\\server\share\config.json`}},
		{name: "backslash rooted", args: []string{`--config=\config\op.json`}, want: []string{"--config=/translated/"}, wantAbs: []string{`\config\op.json`}, wantCalls: []string{`ABS:\config\op.json`}},
		{name: "Linux absolute", args: []string{"--config", "/home/ben/config.json"}, want: []string{"--config", "/home/ben/config.json"}},
		{name: "relative", args: []string{"--config", "config.json"}, want: []string{"--config", "/translated/"}, wantAbs: []string{"config.json"}, wantCalls: []string{"ABS:config.json"}},
		{name: "relative spaces", args: []string{"--config", "config files/op.json"}, want: []string{"--config", "/translated/"}, wantAbs: []string{"config files/op.json"}, wantCalls: []string{"ABS:config files/op.json"}},
		{name: "after separator", args: []string{"open", "--", "--config", `C:\config.json`}, want: []string{"open", "--", "--config", `C:\config.json`}},
		{name: "missing", args: []string{"--config"}, wantErr: true},
		{name: "empty", args: []string{"--config="}, wantErr: true},
		{name: "duplicate", args: []string{"--config", "one", "--config=two"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var absolutes []string
			var calls []string
			got, err := translateConfigArgs(test.args, func(value string) (string, error) {
				absolutes = append(absolutes, value)
				return "ABS:" + value, nil
			}, func(value string) (string, error) {
				calls = append(calls, value)
				return "/translated/", nil
			})
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("args = %#v, want %#v", got, test.want)
			}
			if !reflect.DeepEqual(calls, test.wantCalls) {
				t.Fatalf("translation calls = %#v, want %#v", calls, test.wantCalls)
			}
			if !reflect.DeepEqual(absolutes, test.wantAbs) {
				t.Fatalf("absolute calls = %#v, want %#v", absolutes, test.wantAbs)
			}
		})
	}
}

func TestNormalizeWSLEnvReplacesOwnedModifiersAndPreservesOthers(t *testing.T) {
	input := "KEEP/p:OP_API_TOKEN/p:Other/l:op_remote_url/u:OP_WSL_PROXY_ACTIVE:KEEP_TWO/w:OP_API_TOKEN/w"
	want := "KEEP/p:Other/l:KEEP_TWO/w:OP_API_TOKEN/u:OP_REMOTE_URL/u:OP_WSL_PROXY_ACTIVE/u"
	if got := normalizeWSLEnv(input); got != want {
		t.Fatalf("normalizeWSLEnv() = %q, want %q", got, want)
	}
}

func TestRunRejectsRecursionAndInvalidConfigurationBeforeHostAccess(t *testing.T) {
	tests := []struct {
		name   string
		lookup map[string]string
		args   []string
		code   int
		text   string
	}{
		{name: "recursion", lookup: map[string]string{"OP_WSL_PROXY_ACTIVE": "1"}, code: 125, text: "recursive"},
		{name: "relative Linux op", lookup: map[string]string{"OP_WSL_OP": `C:\op.exe`}, code: 2, text: "absolute Linux path"},
		{name: "config parser", lookup: map[string]string{}, args: []string{"--config"}, code: 2, text: "requires a path"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host := &fakeHost{lookup: test.lookup, wslErr: errors.New("must not be reached")}
			var stderr bytes.Buffer
			if code := Run(test.args, host, &stderr); code != test.code {
				t.Fatalf("code = %d, want %d", code, test.code)
			}
			if !strings.Contains(stderr.String(), test.text) {
				t.Fatalf("stderr = %q, want %q", stderr.String(), test.text)
			}
			if len(host.calls) != 0 {
				t.Fatalf("Execute calls = %d, want 0", len(host.calls))
			}
		})
	}
}

func TestRunReportsHostAndTranslationFailures(t *testing.T) {
	t.Run("WSL missing", func(t *testing.T) {
		host := &fakeHost{lookup: map[string]string{}, wslErr: errors.New("System32 wsl.exe missing")}
		var stderr bytes.Buffer
		if code := Run(nil, host, &stderr); code != 7 || !strings.Contains(stderr.String(), "wsl.exe missing") {
			t.Fatalf("code/stderr = %d/%q", code, stderr.String())
		}
	})

	t.Run("wslpath failure", func(t *testing.T) {
		host := &fakeHost{
			lookup: map[string]string{}, cwd: `C:\work`, wsl: `C:\Windows\System32\wsl.exe`,
			results: []Result{{Stderr: []byte("conversion failed\r\n")}},
			errors:  []error{exitError(1)},
		}
		var stderr bytes.Buffer
		if code := Run([]string{"--config", `C:\bad\config.json`}, host, &stderr); code != 2 || !strings.Contains(stderr.String(), "conversion failed") {
			t.Fatalf("code/stderr = %d/%q", code, stderr.String())
		}
	})
}

func TestRunPreservesAndMapsDelegatedExitCodes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{name: "success", want: 0},
		{name: "ordinary", err: exitError(42), want: 42},
		{name: "Linux SIGINT", err: exitError(130), want: 130},
		{name: "Windows control C", err: exitError(0xc000013a), want: 130},
		{name: "signed Windows control C", err: exitError(-1073741510), want: 130},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			host := &fakeHost{
				lookup: map[string]string{}, cwd: `C:\work`, wsl: `C:\Windows\System32\wsl.exe`,
				errors: []error{test.err},
			}
			if code := Run(nil, host, &bytes.Buffer{}); code != test.want {
				t.Fatalf("code = %d, want %d", code, test.want)
			}
		})
	}
}
