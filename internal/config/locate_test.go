package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/moutansos/op/internal/domain"
)

func TestExtractConfigPath(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		wantPath  string
		wantArgs  []string
		wantError bool
	}{
		{name: "none", args: []string{"serve"}, wantArgs: []string{"serve"}},
		{name: "separate", args: []string{"serve", "--config", "local.json", "--json"}, wantPath: "local.json", wantArgs: []string{"serve", "--json"}},
		{name: "equals", args: []string{"--config=/tmp/op.json", "open", "one"}, wantPath: "/tmp/op.json", wantArgs: []string{"open", "one"}},
		{name: "separator", args: []string{"serve", "--", "--config", "child.json"}, wantArgs: []string{"serve", "--", "--config", "child.json"}},
		{name: "missing", args: []string{"--config"}, wantError: true},
		{name: "empty", args: []string{"--config="}, wantError: true},
		{name: "duplicate", args: []string{"--config", "one", "--config=two"}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, args, err := ExtractConfigPath(test.args)
			if test.wantError {
				if err == nil || !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
					t.Fatalf("ExtractConfigPath() error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if path != test.wantPath || !reflect.DeepEqual(args, test.wantArgs) {
				t.Fatalf("ExtractConfigPath() = %q, %v; want %q, %v", path, args, test.wantPath, test.wantArgs)
			}
		})
	}
}

func TestLocateSearchOrder(t *testing.T) {
	root := t.TempDir()
	userDir := filepath.Join(root, "user")
	repoDir := filepath.Join(root, "repo")
	explicit := filepath.Join(root, "explicit.json")
	userConfig := filepath.Join(userDir, "op", FileName)
	repoConfig := filepath.Join(repoDir, FileName)
	for _, directory := range []string{filepath.Dir(userConfig), repoDir} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{explicit, userConfig, repoConfig} {
		if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	path, err := Locate(LocateOptions{ExplicitPath: explicit, UserConfigDir: userDir, RepositoryRoot: repoDir})
	if err != nil || path != explicit {
		t.Fatalf("explicit Locate() = %q, %v", path, err)
	}
	path, err = Locate(LocateOptions{UserConfigDir: userDir, RepositoryRoot: repoDir})
	if err != nil || path != userConfig {
		t.Fatalf("user Locate() = %q, %v", path, err)
	}
	if err := os.Remove(userConfig); err != nil {
		t.Fatal(err)
	}
	path, err = Locate(LocateOptions{UserConfigDir: userDir, RepositoryRoot: repoDir})
	if err != nil || path != repoConfig {
		t.Fatalf("repo Locate() = %q, %v", path, err)
	}
}

func TestLocateExplicitMissingDoesNotFallBack(t *testing.T) {
	root := t.TempDir()
	repoConfig := filepath.Join(root, FileName)
	if err := os.WriteFile(repoConfig, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Locate(LocateOptions{ExplicitPath: filepath.Join(root, "missing.json"), RepositoryRoot: root, UserConfigDir: root})
	if err == nil || !domain.IsCode(err, domain.ErrorCodeNotFound) {
		t.Fatalf("Locate() error = %v", err)
	}
}

func TestLocateMissingReportsAttempts(t *testing.T) {
	root := t.TempDir()
	_, err := Locate(LocateOptions{UserConfigDir: filepath.Join(root, "user"), RepositoryRoot: filepath.Join(root, "repo")})
	var typed *domain.Error
	if !errors.As(err, &typed) || typed.Code != domain.ErrorCodeNotFound {
		t.Fatalf("Locate() error = %#v", err)
	}
	if typed.Message == "" {
		t.Fatal("missing-config error has no search diagnostics")
	}
}

func TestLocateRejectsDirectoryCandidate(t *testing.T) {
	root := t.TempDir()
	directory := filepath.Join(root, "not-a-file")
	if err := os.Mkdir(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Locate(LocateOptions{ExplicitPath: directory})
	if err == nil || !domain.IsCode(err, domain.ErrorCodeConfig) {
		t.Fatalf("Locate() error = %v", err)
	}
}
