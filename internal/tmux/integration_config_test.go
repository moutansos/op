package tmux

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

const integrationSocketDirectory = "/tmp"

func integrationSocket(t *testing.T, tmuxExecutable string) string {
	t.Helper()
	info, err := os.Stat(integrationSocketDirectory)
	if err != nil || !info.IsDir() || !filepath.IsAbs(integrationSocketDirectory) {
		t.Fatalf("short tmux socket directory is unavailable: %v", err)
	}
	file, err := os.CreateTemp(integrationSocketDirectory, "op-tmux-")
	if err != nil {
		t.Fatalf("reserve tmux socket path: %v", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		t.Fatalf("close tmux socket reservation: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("release tmux socket reservation: %v", err)
	}
	if len(path) > 80 {
		t.Fatalf("tmux socket path is unexpectedly long: %q", path)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = exec.CommandContext(ctx, tmuxExecutable, "-S", path, "kill-server").Run()
		if ctx.Err() != nil {
			t.Errorf("kill tmux server at %q: %v", path, ctx.Err())
			return
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove tmux socket %q: %v", path, err)
		}
	})
	return path
}

func integrationConfig(root, socket string) ManagerConfig {
	return ManagerConfig{
		Session: "code", DashboardWindow: "op", Socket: socket, StartDirectory: root,
		DashboardCommand: "sleep 300", EditorCommand: "sleep 300", PreferredShell: "sh",
		ShellPaneRows: 10, DefaultProfile: "integration",
	}
}
