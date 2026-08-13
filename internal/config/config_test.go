package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/moutansos/op/internal/domain"
)

func TestDefaultsAreCanonical(t *testing.T) {
	config := Defaults()
	if config.RepoDirectory != "~/source/repos" || config.PreferredShell != "zsh" {
		t.Fatalf("unexpected top-level defaults: %#v", config)
	}
	if config.Tmux.Session != "code" || config.Tmux.DashboardWindow != "op" || config.Tmux.ShellPaneRows != 20 || config.Tmux.DefaultProfile != "nvim" {
		t.Fatalf("unexpected tmux defaults: %#v", config.Tmux)
	}
	if config.Stats.RefreshInterval.Duration != 2*time.Second || config.Stats.TmuxRefreshInterval.Duration != 5*time.Second {
		t.Fatalf("unexpected stats defaults: %#v", config.Stats)
	}
	if config.Server.Listen != "127.0.0.1:8787" || config.Server.Enabled || config.Actions.GUIEditors {
		t.Fatalf("unexpected server/action defaults: %#v %#v", config.Server, config.Actions)
	}
	if len(config.ProjectOpeners) != 1 || config.ProjectOpeners[0].ID != "nvim" || config.ProjectOpeners[0].Mode != domain.ProjectOpenModeTmux || config.ProjectOpeners[0].Command != "nvim ." {
		t.Fatalf("unexpected project opener defaults: %#v", config.ProjectOpeners)
	}
	if config.CustomEntries == nil || config.CustomCommands == nil {
		t.Fatal("collection defaults must be non-nil for canonical JSON arrays")
	}
}

func TestDurationJSONRoundTrip(t *testing.T) {
	data, err := json.Marshal(NewDuration(1500 * time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `"1.5s"` {
		t.Fatalf("unexpected duration JSON: %s", data)
	}
	var duration Duration
	if err := json.Unmarshal([]byte(`"250ms"`), &duration); err != nil {
		t.Fatal(err)
	}
	if duration.Duration != 250*time.Millisecond {
		t.Fatalf("unexpected parsed duration: %s", duration.Duration)
	}
}

func TestMigrateLegacyConfiguration(t *testing.T) {
	data := []byte(`{
		"repoDirectory": "$REPOS",
		"wslRepoDirectory": "/mnt/c/repos",
		"isServer": false,
		"preferedShell": "fish",
		"customEntries": [{"name":"dotfiles","paths":{"win":"$env:LOCALAPPDATA/dotfiles","linux":"${HOME}/dotfiles"}}],
		"customCommands": [{"name":"agent","command":"agent --root {{oproot}} {{path}}","runInPreferredShell":true}]
	}`)
	config, warnings, err := Migrate(data)
	if err != nil {
		t.Fatal(err)
	}
	if config.PreferredShell != "fish" || !config.Actions.GUIEditors {
		t.Fatalf("legacy fields were not mapped: %#v", config)
	}
	if len(config.CustomCommands) != 1 || config.CustomCommands[0].Command != "agent --root {{oproot}} {{path}}" || !config.CustomCommands[0].RunInPreferredShell {
		t.Fatalf("custom command changed during migration: %#v", config.CustomCommands)
	}
	if got := warningPaths(warnings); !reflect.DeepEqual(got, []string{"isServer", "preferedShell", "wslRepoDirectory"}) {
		t.Fatalf("unexpected migration warnings: %v", got)
	}

	lookup := mapLookup(map[string]string{
		"REPOS":        "/srv/repos",
		"HOME":         "/home/tester",
		"LOCALAPPDATA": `C:\Users\tester\AppData\Local`,
	})
	if err := Expand(&config, ExpandOptions{BaseDirectory: "/config", HomeDirectory: "/home/tester", LookupEnv: lookup}); err != nil {
		t.Fatal(err)
	}
	if config.RepoDirectory != "/srv/repos" || config.CustomEntries[0].Paths.Linux != "/home/tester/dotfiles" {
		t.Fatalf("legacy paths were not expanded: %#v", config)
	}
	if config.CustomEntries[0].Paths.Windows != `C:\Users\tester\AppData\Local/dotfiles` {
		t.Fatalf("PowerShell environment syntax was not translated: %q", config.CustomEntries[0].Paths.Windows)
	}
	if config.CustomCommands[0].Command != "agent --root {{oproot}} {{path}}" {
		t.Fatalf("command placeholders were expanded unexpectedly: %q", config.CustomCommands[0].Command)
	}
}

func TestMigrateLegacyDefaultProfileToFixedNvimOpener(t *testing.T) {
	config, _, err := Migrate([]byte(`{"tmux":{"defaultProfile":"editor"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if config.Tmux.DefaultProfile != "editor" || len(config.ProjectOpeners) != 1 || config.ProjectOpeners[0].ID != "editor" || config.ProjectOpeners[0].Command != "nvim ." {
		t.Fatalf("legacy profile migration = %#v %#v", config.Tmux, config.ProjectOpeners)
	}
}

func TestCanonicalFieldsTakePrecedenceOverLegacy(t *testing.T) {
	config, warnings, err := Migrate([]byte(`{
		"preferredShell":"zsh",
		"preferedShell":"bash",
		"isServer":true,
		"actions":{"guiEditors":true}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if config.PreferredShell != "zsh" || !config.Actions.GUIEditors {
		t.Fatalf("canonical fields did not win: %#v", config)
	}
	if len(warnings) != 2 || !strings.Contains(warnings[0].Message+warnings[1].Message, "takes precedence") {
		t.Fatalf("expected precedence warnings, got %#v", warnings)
	}
}

func TestLegacyServerModeDisablesGUIEditors(t *testing.T) {
	config, warnings, err := Migrate([]byte(`{"isServer":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if config.Actions.GUIEditors {
		t.Fatal("legacy isServer=true must disable GUI editors")
	}
	if got := warningPaths(warnings); !reflect.DeepEqual(got, []string{"isServer"}) {
		t.Fatalf("unexpected warnings: %v", got)
	}
}

func TestMigrateReportsUnknownFieldsAtEveryLevel(t *testing.T) {
	_, warnings, err := Migrate([]byte(`{
		"mystery":1,
		"tmux":{"session":"code","extra":true},
		"customEntries":[{"name":"x","paths":{"linux":"/x","plan9":"/y"},"alias":"y"}],
		"customCommands":[{"name":"x","command":"x","unexpected":false}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"customCommands[0].unexpected",
		"customEntries[0].alias",
		"customEntries[0].paths.plan9",
		"mystery",
		"tmux.extra",
	}
	if got := warningPaths(warnings); !reflect.DeepEqual(got, want) {
		t.Fatalf("warning paths = %v, want %v", got, want)
	}
	for _, warning := range warnings {
		if warning.Code != WarningUnknownField {
			t.Fatalf("unexpected warning code: %#v", warning)
		}
	}
}

func TestMigrateRejectsMalformedJSONAndDuration(t *testing.T) {
	for _, data := range []string{
		`{"repoDirectory":`,
		`{"stats":{"refreshInterval":"soon"}}`,
		`{"stats":{"refreshInterval":2}}`,
		`[]`,
		`null`,
	} {
		_, _, err := Migrate([]byte(data))
		if err == nil || !domain.IsCode(err, domain.ErrorCodeConfig) {
			t.Fatalf("Migrate(%q) error = %v", data, err)
		}
	}
}

func TestExpandPathsAndDoesNotEvaluateInput(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	config := Defaults()
	config.RepoDirectory = `$ROOT/$NAME/${LEAF}/$env:PS`
	config.Tmux.Socket = "./run/tmux.sock"
	config.Server.TokenFile = "~/tokens/op"
	config.CustomEntries = []CustomEntry{
		{Name: "literal", Paths: EntryPaths{Linux: `$(touch SHOULD_NOT_EXIST)`}},
		{Name: "windows", Paths: EntryPaths{Linux: "$ROOT/project", Windows: "$env:UNSET/path"}},
	}
	config.CustomCommands = []CustomCommand{{Name: "command", Command: `echo $ROOT {{path}} {{oproot}}`}}
	lookup := mapLookup(map[string]string{"ROOT": filepath.Join(base, "repos"), "NAME": "one", "LEAF": "two", "PS": "three"})
	if err := Expand(&config, ExpandOptions{BaseDirectory: base, HomeDirectory: home, LookupEnv: lookup}); err != nil {
		t.Fatal(err)
	}
	wantRepo := filepath.Join(base, "repos", "one", "two", "three")
	if config.RepoDirectory != wantRepo {
		t.Fatalf("repoDirectory = %q, want %q", config.RepoDirectory, wantRepo)
	}
	if config.Tmux.Socket != filepath.Join(base, "run", "tmux.sock") || config.Server.TokenFile != filepath.Join(home, "tokens", "op") {
		t.Fatalf("relative/tilde paths were not normalized: %#v", config)
	}
	if config.CustomEntries[0].Paths.Linux != filepath.Join(base, `$(touch SHOULD_NOT_EXIST)`) {
		t.Fatalf("shell expression was not retained literally: %q", config.CustomEntries[0].Paths.Linux)
	}
	if _, err := os.Stat(filepath.Join(base, "SHOULD_NOT_EXIST")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("configuration input appears to have been evaluated: %v", err)
	}
	if config.CustomEntries[1].Paths.Windows != "$env:UNSET/path" {
		t.Fatalf("unused unresolved Windows path changed: %q", config.CustomEntries[1].Paths.Windows)
	}
	if config.CustomCommands[0].Command != `echo $ROOT {{path}} {{oproot}}` {
		t.Fatalf("command was expanded: %q", config.CustomCommands[0].Command)
	}
}

func TestExpandRejectsMissingEnvironmentAndUnsupportedHome(t *testing.T) {
	for _, value := range []string{"$MISSING/repos", "~other/repos"} {
		config := Defaults()
		config.RepoDirectory = value
		err := Expand(&config, ExpandOptions{BaseDirectory: "/config", HomeDirectory: "/home/tester", LookupEnv: mapLookup(nil)})
		if err == nil || !domain.IsCode(err, domain.ErrorCodeConfig) {
			t.Fatalf("Expand(%q) error = %v", value, err)
		}
	}
}

func TestLoadCanonicalExample(t *testing.T) {
	path := filepath.Join("..", "..", "config.example.json")
	result, err := LoadWithOptions(path, ExpandOptions{
		HomeDirectory: "/home/tester",
		LookupEnv:     mapLookup(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Warnings) != 0 {
		t.Fatalf("canonical example produced warnings: %#v", result.Warnings)
	}
	if !filepath.IsAbs(result.Config.SourcePath) || result.Config.RootDirectory != filepath.Dir(result.Config.SourcePath) {
		t.Fatalf("source metadata is not normalized: %#v", result.Config)
	}
	if result.Config.RepoDirectory != "/home/tester/source/repos" || result.Config.CustomEntries[0].Paths.Linux != "/home/tester/.config/nvim" {
		t.Fatalf("example paths not normalized: %#v", result.Config)
	}
	if got := result.Config.CustomCommands[0].Command; !strings.Contains(got, "{{path}}") || !strings.Contains(got, "{{oproot}}") {
		t.Fatalf("example placeholders were not retained: %q", got)
	}
}

func TestLoadResolvesRelativePathsFromConfigDirectory(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, FileName)
	data := []byte(`{
		"repoDirectory":"repos",
		"server":{"tokenFile":"secrets/token"},
		"customEntries":[{"name":"notes","paths":{"linux":"notes"}}]
	}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := LoadWithOptions(path, ExpandOptions{HomeDirectory: "/home/tester", LookupEnv: mapLookup(nil)})
	if err != nil {
		t.Fatal(err)
	}
	if result.Config.RepoDirectory != filepath.Join(root, "repos") ||
		result.Config.Server.TokenFile != filepath.Join(root, "secrets", "token") ||
		result.Config.CustomEntries[0].Paths.Linux != filepath.Join(root, "notes") {
		t.Fatalf("relative paths did not use config root: %#v", result.Config)
	}
}

func TestValidateAcceptsCanonicalExpandedConfig(t *testing.T) {
	config := validConfig(t)
	if err := Validate(config); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name  string
		field string
		edit  func(*Config)
	}{
		{name: "relative repo", field: "repoDirectory", edit: func(c *Config) { c.RepoDirectory = "repos" }},
		{name: "empty shell", field: "preferredShell", edit: func(c *Config) { c.PreferredShell = "" }},
		{name: "colon session", field: "tmux.session", edit: func(c *Config) { c.Tmux.Session = "bad:name" }},
		{name: "dot session", field: "tmux.session", edit: func(c *Config) { c.Tmux.Session = "bad.name" }},
		{name: "gotmux separator", field: "tmux.dashboardWindow", edit: func(c *Config) { c.Tmux.DashboardWindow = "bad-:-name" }},
		{name: "newline window", field: "tmux.dashboardWindow", edit: func(c *Config) { c.Tmux.DashboardWindow = "bad\nname" }},
		{name: "rows", field: "tmux.shellPaneRows", edit: func(c *Config) { c.Tmux.ShellPaneRows = 0 }},
		{name: "duration", field: "stats.refreshInterval", edit: func(c *Config) { c.Stats.RefreshInterval = NewDuration(0) }},
		{name: "no openers", field: "projectOpeners", edit: func(c *Config) { c.ProjectOpeners = nil }},
		{name: "unknown default opener", field: "tmux.defaultProfile", edit: func(c *Config) { c.Tmux.DefaultProfile = "missing" }},
		{name: "duplicate opener", field: "projectOpeners[1].id", edit: func(c *Config) { c.ProjectOpeners = append(c.ProjectOpeners, c.ProjectOpeners[0]) }},
		{name: "invalid opener mode", field: "projectOpeners[0].mode", edit: func(c *Config) { c.ProjectOpeners[0].Mode = "terminal" }},
		{name: "listen", field: "server.listen", edit: func(c *Config) { c.Server.Listen = "localhost" }},
		{name: "tls pair", field: "server", edit: func(c *Config) { c.Server.TLSCertFile = filepath.Join(t.TempDir(), "cert") }},
		{name: "non-loopback tls", field: "server", edit: func(c *Config) { c.Server.Listen = "0.0.0.0:8787" }},
		{name: "entry path", field: "customEntries[0].paths.linux", edit: func(c *Config) { c.CustomEntries[0].Paths.Linux = "relative" }},
		{name: "duplicate entry", field: "customEntries[1].name", edit: func(c *Config) { c.CustomEntries = append(c.CustomEntries, c.CustomEntries[0]) }},
		{name: "empty command", field: "customCommands[0].command", edit: func(c *Config) { c.CustomCommands[0].Command = "" }},
		{name: "duplicate command", field: "customCommands[1].name", edit: func(c *Config) { c.CustomCommands = append(c.CustomCommands, c.CustomCommands[0]) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig(t)
			test.edit(&config)
			err := Validate(config)
			var typed *domain.Error
			if !errors.As(err, &typed) || typed.Code != domain.ErrorCodeConfig || typed.Field != test.field {
				t.Fatalf("Validate() error = %#v, want config field %q", err, test.field)
			}
		})
	}
}

func TestValidateRejectsReservedCustomCommandNames(t *testing.T) {
	for _, name := range []string{"nvim", "code", "shell", "worktree", "worktree:feature", "worktree:"} {
		t.Run(name, func(t *testing.T) {
			config := validConfig(t)
			config.CustomCommands[0].Name = name
			err := Validate(config)
			var typed *domain.Error
			if !errors.As(err, &typed) || typed.Code != domain.ErrorCodeConfig || typed.Field != "customCommands[0].name" || !strings.Contains(typed.Message, "reserved built-in action") {
				t.Fatalf("Validate() error = %#v, want reserved customCommands[0].name error", err)
			}
		})
	}
}

func TestValidateAcceptsCaseDistinctAndDescriptiveCustomCommandNames(t *testing.T) {
	config := validConfig(t)
	config.CustomCommands = []CustomCommand{
		{Name: "Nvim", Command: "first"},
		{Name: "Open shell logs", Command: "second"},
		{Name: "my-worktree:feature", Command: "third"},
		{Name: "worktree helper", Command: "fourth"},
		{Name: "v2", Command: "fifth"},
		{Name: "3-way", Command: "sixth"},
		{Name: "03", Command: "seventh"},
	}
	if err := Validate(config); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestLoadRejectsInvalidCustomCommandsInCanonicalAndMigratedConfig(t *testing.T) {
	tests := []struct {
		name string
		data string
	}{
		{
			name: "canonical",
			data: `{"repoDirectory":"/repos","customCommands":[{"name":"shell","command":"custom-shell"}]}`,
		},
		{
			name: "migrated legacy fields",
			data: `{"repoDirectory":"/repos","preferedShell":"zsh","customCommands":[{"name":"worktree:feature","command":"custom-worktree"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), FileName)
			if err := os.WriteFile(path, []byte(test.data), 0o600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadWithOptions(path, ExpandOptions{HomeDirectory: "/home/tester", LookupEnv: mapLookup(nil)})
			var typed *domain.Error
			if !errors.As(err, &typed) || typed.Code != domain.ErrorCodeConfig || typed.Field != "customCommands[0].name" {
				t.Fatalf("LoadWithOptions() error = %#v, want config customCommands[0].name error", err)
			}
		})
	}
}

func TestValidateNonLoopbackWithAuthenticationAndTLS(t *testing.T) {
	config := validConfig(t)
	config.Server.Listen = "192.0.2.10:8787"
	config.Server.TLSCertFile = filepath.Join(t.TempDir(), "cert.pem")
	config.Server.TLSKeyFile = filepath.Join(t.TempDir(), "key.pem")
	if err := Validate(config); err != nil {
		t.Fatal(err)
	}
}

func TestValidateNonLoopbackAllowsEnvironmentTokenAtServeTime(t *testing.T) {
	config := validConfig(t)
	config.Server.Listen = "192.0.2.10:8787"
	config.Server.TokenFile = ""
	config.Server.TLSCertFile = filepath.Join(t.TempDir(), "cert.pem")
	config.Server.TLSKeyFile = filepath.Join(t.TempDir(), "key.pem")
	if err := Validate(config); err != nil {
		t.Fatal(err)
	}
}

func validConfig(t *testing.T) Config {
	t.Helper()
	root := t.TempDir()
	config := Defaults()
	config.RepoDirectory = filepath.Join(root, "repos")
	config.Server.TokenFile = filepath.Join(root, "token")
	config.CustomEntries = []CustomEntry{{Name: "dotfiles", Paths: EntryPaths{Linux: filepath.Join(root, "dotfiles")}}}
	config.CustomCommands = []CustomCommand{{Name: "agent", Command: "agent {{path}}", RunInPreferredShell: true}}
	return config
}

func warningPaths(warnings []Warning) []string {
	paths := make([]string, len(warnings))
	for i, warning := range warnings {
		paths[i] = warning.Path
	}
	return paths
}

func mapLookup(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}
