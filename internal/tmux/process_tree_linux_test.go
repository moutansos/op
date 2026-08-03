//go:build linux

package tmux

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

func TestProcessTreeContainsCommand(t *testing.T) {
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatalf("start child: %v", err)
	}
	t.Cleanup(func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	})

	found, err := processTreeContainsCommand(context.Background(), os.Getpid(), "sleep 30")
	if err != nil || !found {
		t.Fatalf("running child found = %v, %v", found, err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatalf("kill child: %v", err)
	}
	if err := child.Wait(); err == nil {
		t.Fatal("killed child unexpectedly exited successfully")
	}
	found, err = processTreeContainsCommand(context.Background(), os.Getpid(), "sleep 30")
	if err != nil || found {
		t.Fatalf("exited child found = %v, %v", found, err)
	}
}

func TestCommandLineMatchesDashboardInvocation(t *testing.T) {
	actual := []string{"/opt/op", "--config", "/config dir/config.json", "dashboard"}
	if !commandLineMatches(actual, actual) {
		t.Fatal("exact dashboard invocation did not match")
	}
	if commandLineMatches(actual, []string{"op", "dashboard"}) {
		t.Fatal("different dashboard arguments matched")
	}
}
