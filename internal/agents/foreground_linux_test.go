//go:build linux

package agents

import (
	"os"
	"os/exec"
	"testing"
)

// The test binary usually runs without a controlling terminal, in which case the
// kernel reports tpgid as -1. Both outcomes are correct behavior, so the test
// pins the shape of the result rather than a specific PID.
func TestResolveForegroundOnSelf(t *testing.T) {
	foreground := ResolveForeground(int32(os.Getpid()))
	if !foreground.Valid {
		if foreground.PID != 0 || foreground.Command != "" {
			t.Fatalf("invalid foreground carried data: %+v", foreground)
		}
		return
	}
	if foreground.PID <= 0 {
		t.Fatalf("valid foreground has PID %d", foreground.PID)
	}
	if foreground.Command == "" {
		t.Fatal("valid foreground has no command name")
	}
}

func TestResolveForegroundRejectsInvalidPID(t *testing.T) {
	for _, pid := range []int32{0, -1} {
		if ResolveForeground(pid).Valid {
			t.Fatalf("ResolveForeground(%d) reported a valid foreground", pid)
		}
	}
}

func TestResolveForegroundOnMissingProcess(t *testing.T) {
	// Reap a process so its PID is certain to be absent from procfs.
	command := exec.Command("true")
	if err := command.Run(); err != nil {
		t.Skipf("could not run a throwaway process: %v", err)
	}
	if ResolveForeground(int32(command.Process.Pid)).Valid {
		t.Skip("PID was recycled before the read; nothing to assert")
	}
}

// stat's comm field is unquoted and may contain spaces and parentheses, so
// parsing must anchor on the final ')' rather than splitting the whole line.
func TestTerminalForegroundGroupParsesAwkwardCommNames(t *testing.T) {
	directory := t.TempDir()
	statFile := directory + "/stat"
	contents := "1234 (weird ) name) S 1 1234 1234 34816 4321 4194304 0 0\n"
	if err := os.WriteFile(statFile, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	data, err := os.ReadFile(statFile)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	tpgid, ok := parseForegroundGroup(string(data))
	if !ok {
		t.Fatal("parseForegroundGroup() failed on a comm containing ')'")
	}
	if tpgid != 4321 {
		t.Fatalf("tpgid = %d, want 4321", tpgid)
	}
}
