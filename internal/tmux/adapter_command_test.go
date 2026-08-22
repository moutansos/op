package tmux

import (
	"strings"
	"testing"
)

func TestPaneRecordArgsRequestsEveryFieldInOneQuery(t *testing.T) {
	got := strings.Join(paneRecordArgs("@6"), " ")
	want := "list-panes -t @6 -F " + strings.Join([]string{
		"#{pane_id}", "#{pane_index}", "#{pane_pid}", "#{pane_active}", "#{pane_dead}",
		"#{pane_at_top}", "#{pane_at_bottom}", "#{pane_height}",
		"#{pane_current_command}", "#{pane_current_path}",
	}, paneRecordSeparator)
	if got != want {
		t.Fatalf("paneRecordArgs = %q, want %q", got, want)
	}
}

func TestParsePaneRecords(t *testing.T) {
	record := func(values ...string) string { return strings.Join(values, paneRecordSeparator) }

	t.Run("full window", func(t *testing.T) {
		output := record("%4", "0", "812", "1", "0", "1", "0", "24", "nvim", "/home/ben/src") + "\n" +
			record("%5", "1", "813", "0", "1", "0", "1", "10", "sh", "/tmp") + "\n"
		panes, err := parsePaneRecords(output)
		if err != nil {
			t.Fatalf("parsePaneRecords() error = %v", err)
		}
		want := []paneState{
			{ID: "%4", Index: 0, PID: 812, CurrentCommand: "nvim", CurrentPath: "/home/ben/src", Active: true, AtTop: true, Height: 24},
			{ID: "%5", Index: 1, PID: 813, CurrentCommand: "sh", CurrentPath: "/tmp", Dead: true, AtBottom: true, Height: 10},
		}
		if len(panes) != len(want) {
			t.Fatalf("panes = %+v, want %+v", panes, want)
		}
		for i := range want {
			if panes[i] != want[i] {
				t.Fatalf("pane %d = %+v, want %+v", i, panes[i], want[i])
			}
		}
	})

	t.Run("separator in trailing path stays in path", func(t *testing.T) {
		panes, err := parsePaneRecords(record("%4", "0", "812", "1", "0", "1", "1", "24", "sh", "/tmp/od"+paneRecordSeparator+"d"))
		if err != nil {
			t.Fatalf("parsePaneRecords() error = %v", err)
		}
		if len(panes) != 1 || panes[0].CurrentPath != "/tmp/od"+paneRecordSeparator+"d" || panes[0].Index != 0 {
			t.Fatalf("panes = %+v", panes)
		}
	})

	t.Run("empty output", func(t *testing.T) {
		panes, err := parsePaneRecords("")
		if err != nil || len(panes) != 0 {
			t.Fatalf("parsePaneRecords(\"\") = %+v, %v", panes, err)
		}
	})

	for _, test := range []struct {
		name   string
		output string
	}{
		{name: "short record", output: record("%4", "0", "812", "1", "0")},
		{name: "malformed identity", output: record("x4", "0", "812", "1", "0", "1", "1", "24", "sh", "/tmp")},
		{name: "invalid bool", output: record("%4", "0", "812", "1", "", "1", "1", "24", "sh", "/tmp")},
		{name: "invalid int", output: record("%4", "x", "812", "1", "0", "1", "1", "24", "sh", "/tmp")},
		{name: "duplicate identity", output: record("%4", "0", "812", "1", "0", "1", "1", "24", "sh", "/tmp") + "\n" + record("%4", "1", "813", "0", "0", "1", "1", "24", "sh", "/tmp")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if panes, err := parsePaneRecords(test.output); err == nil {
				t.Fatalf("parsePaneRecords(%q) = %+v, want error", test.output, panes)
			}
		})
	}
}

func TestCreatedWindowID(t *testing.T) {
	for _, test := range []struct {
		name   string
		output string
		want   string
		valid  bool
	}{
		{name: "newline", output: "@12\n", want: "@12", valid: true},
		{name: "CRLF", output: "@0\r\n", want: "@0", valid: true},
		{name: "without newline", output: "@7", want: "@7", valid: true},
		{name: "empty", output: "", valid: false},
		{name: "only newline", output: "\n", valid: false},
		{name: "multiple lines", output: "@1\n@2\n", valid: false},
		{name: "multiple trailing newlines", output: "@1\n\n", valid: false},
		{name: "missing prefix", output: "1\n", valid: false},
		{name: "missing number", output: "@\n", valid: false},
		{name: "zero with leading zero", output: "@00\n", valid: false},
		{name: "positive with leading zero", output: "@01\n", valid: false},
		{name: "non-decimal", output: "@1x\n", valid: false},
		{name: "surrounding whitespace", output: " @1 \n", valid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := createdWindowID(test.output)
			if test.valid {
				if err != nil || got != test.want {
					t.Fatalf("createdWindowID(%q) = %q, %v; want %q", test.output, got, err, test.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("createdWindowID(%q) = %q, nil; want error", test.output, got)
			}
		})
	}
}
