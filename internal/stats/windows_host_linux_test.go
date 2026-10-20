//go:build linux

package stats

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestIntegrationWindowsProcessesAreAttributedToTheirPane(t *testing.T) {
	if os.Getenv("OP_WSL_INTEGRATION") != "1" {
		t.Skip("set OP_WSL_INTEGRATION=1 inside WSL to sample the real Windows host")
	}
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	socket := filepath.Join("/tmp", "op-wsl-"+strconv.Itoa(os.Getpid()))
	t.Cleanup(func() { _ = exec.Command(tmuxPath, "-S", socket, "kill-server").Run(); _ = os.Remove(socket) })
	tmux := func(args ...string) string {
		command := exec.Command(tmuxPath, append([]string{"-S", socket}, args...)...)
		command.Env = append(os.Environ(), "WSLENV="+os.Getenv("WSLENV")+":TMUX:TMUX_PANE")
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	tmux("new-session", "-d", "-s", "wsl", "-x", "100", "-y", "20",
		`powershell.exe -NoProfile -Command "Start-Process -NoNewWindow cmd.exe -ArgumentList '/c ping -n 30 127.0.0.1 > nul'; Start-Sleep 30"`)
	pane := tmux("display-message", "-p", "-t", "wsl", "#{pane_id}")
	server, _ := strconv.Atoi(tmux("display-message", "-p", "#{pid}"))

	source := newWindowsHostSource()
	if source == nil {
		t.Fatal("not running inside WSL")
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		names := map[string]bool{}
		for _, process := range source.Sample(context.Background()).Processes {
			if process.ServerPID == int32(server) && process.Pane == pane {
				names[strings.ToLower(process.Name)] = true
			}
		}
		if names["powershell.exe"] && names["cmd.exe"] && names["ping.exe"] {
			t.Logf("pane %s on server %d: %v", pane, server, names)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("Windows processes for pane %s on server %d = %v, want powershell.exe, cmd.exe, and PING.EXE", pane, server, names)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func TestIntegrationWindowsHostSamplerUnderWSL(t *testing.T) {
	if os.Getenv("OP_WSL_INTEGRATION") != "1" {
		t.Skip("set OP_WSL_INTEGRATION=1 inside WSL to sample the real Windows host")
	}
	source := newWindowsHostSource()
	if source == nil {
		t.Fatal("not running inside WSL")
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		sample := source.Sample(context.Background()).Stats
		if sample.Error != "" {
			t.Fatalf("sample error: %s", sample.Error)
		}
		if sample.CPUAvailable {
			if sample.CPUPercent < 0 || sample.CPUPercent > 100 || sample.LogicalCPUs < 1 ||
				sample.MemoryTotal == 0 || sample.MemoryUsed > sample.MemoryTotal || sample.UptimeSeconds == 0 {
				t.Fatalf("implausible sample: %+v", sample)
			}
			t.Logf("Windows host: %+v", sample)
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no CPU sample within 20s; last = %+v", sample)
		}
		time.Sleep(500 * time.Millisecond)
	}
}
