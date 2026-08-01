//go:build !windows

package tmux

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/moutansos/op/internal/domain"
)

func TestRawExactWindowExistenceUsesExplicitSocketAndExactID(t *testing.T) {
	root := t.TempDir()
	arguments := filepath.Join(root, "arguments")
	executable := writeExecutable(t, root, "tmux-query", "#!/bin/sh\nprintf '%s\\n' \"$@\" > '"+arguments+"'\nprintf '@1\\n@12\\n'")
	raw := rawTmux{executable: executable, socket: filepath.Join(root, "tmux.sock")}

	exists, err := raw.exactIDExists(context.Background(), "@1", "list-windows", "-a", "-F", "#{window_id}")
	if err != nil || !exists {
		t.Fatalf("exact window @1 existence = %v, %v", exists, err)
	}
	exists, err = raw.exactIDExists(context.Background(), "@2", "list-windows", "-a", "-F", "#{window_id}")
	if err != nil || exists {
		t.Fatalf("exact window @2 existence = %v, %v", exists, err)
	}
	data, err := os.ReadFile(arguments)
	if err != nil {
		t.Fatalf("read query arguments: %v", err)
	}
	want := strings.Join([]string{"-S", raw.socket, "list-windows", "-a", "-F", "#{window_id}", ""}, "\n")
	if string(data) != want {
		t.Fatalf("raw query arguments = %q, want %q", data, want)
	}
}

func TestRawExactWindowExistenceStopsStalledCommandOnContextDeadline(t *testing.T) {
	root := t.TempDir()
	executable := writeExecutable(t, root, "tmux-stall", "#!/bin/sh\nexec sleep 30")
	raw := rawTmux{executable: executable, socket: filepath.Join(root, "tmux.sock")}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()

	_, err := raw.exactIDExists(ctx, "@1", "list-windows", "-a", "-F", "#{window_id}")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled query error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled query exceeded context bound: %v", elapsed)
	}
}

func TestRawRunCancellationKillsProcessTreeAndPreventsLateMutation(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "mutated")
	executable := writeExecutable(t, root, "tmux-late-mutation", "#!/bin/sh\nsleep 0.2\nprintf changed > '"+marker+"'\n")
	raw := rawTmux{executable: executable}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	_, err := raw.run(ctx, "new-window")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stalled mutation error = %v, want deadline exceeded", err)
	}
	time.Sleep(300 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mutation completed after cancellation: %v", err)
	}
}

func TestRawRunIncludesStderr(t *testing.T) {
	root := t.TempDir()
	executable := writeExecutable(t, root, "tmux-error", "#!/bin/sh\nprintf 'backend detail\\n' >&2\nexit 7\n")
	_, err := (rawTmux{executable: executable}).run(context.Background(), "list-sessions")
	if err == nil || !strings.Contains(err.Error(), "backend detail") {
		t.Fatalf("raw error = %v, want stderr detail", err)
	}
}

func TestRawSingleFieldPreservesDelimiterWhitespaceAndEmbeddedNewline(t *testing.T) {
	root := t.TempDir()
	executable := writeExecutable(t, root, "tmux-field", "#!/bin/sh\nprintf '  path-:-with\\ttab\\nline two\\n'\n")
	value, err := (rawTmux{executable: executable}).field(context.Background(), "%1", "pane_current_path")
	if err != nil {
		t.Fatal(err)
	}
	if want := "  path-:-with\ttab\nline two"; value != want {
		t.Fatalf("single field = %q, want %q", value, want)
	}
}

func TestNewStopsStalledInitializationWithTypedDeadline(t *testing.T) {
	root := t.TempDir()
	writeExecutable(t, root, "tmux", "#!/bin/sh\nexec /bin/sleep 30\n")
	t.Setenv("PATH", root)
	config := testConfig()
	config.Socket = filepath.Join(root, "tmux.sock")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()

	_, err := New(ctx, config)
	if !errors.Is(err, context.DeadlineExceeded) || !domain.IsCode(err, domain.ErrorCodeTimeout) {
		t.Fatalf("New() error = %v, want typed deadline", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("New() exceeded context bound: %v", elapsed)
	}
}

func TestRawInteractiveCancellationReturnsDeadline(t *testing.T) {
	root := t.TempDir()
	executable := writeExecutable(t, root, "tmux-attach", "#!/bin/sh\ntrap 'exit 0' TERM\nexec 2>/dev/null\nwhile :; do sleep 1; done\n")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()

	err := (rawTmux{executable: executable}).runInteractive(ctx, io.Discard, new(bytes.Buffer), "attach-session", "-t", "$1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("interactive error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("interactive cancellation took %v", elapsed)
	}
}

func writeExecutable(t *testing.T, directory, name, contents string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("write test executable: %v", err)
	}
	return path
}
