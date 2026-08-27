package tui

import (
	"github.com/charmbracelet/lipgloss"
	"strings"
	"testing"
	"time"

	"github.com/moutansos/op/internal/domain"
)

func treeModel(agents []domain.PaneAgentState, processes []domain.PaneProcessStats) Model {
	return Model{
		haveTmux: true,
		tmux: domain.TmuxSnapshot{
			Session: &domain.TmuxSession{
				Name:     "code",
				Attached: true,
				Windows: []domain.TmuxWindow{
					{
						Index: 1, Name: "notifier", Profile: "nvim", Active: true,
						Panes: []domain.TmuxPane{
							{ID: "%45", PID: 396585, CurrentCommand: "zsh"},
							{ID: "%46", PID: 397633, CurrentCommand: "claude"},
						},
					},
					{
						Index: 2, Name: "op", Profile: "nvim",
						Panes: []domain.TmuxPane{{ID: "%6", PID: 425274, CurrentCommand: "opencode"}},
					},
				},
			},
		},
		haveStats: true,
		stats: domain.StatsSnapshot{
			CapturedAt: time.Unix(1_700_000_000, 0),
			Processes:  processes,
			Agents:     agents,
		},
	}
}

func TestRenderTmuxDrawsSessionWindowPaneTree(t *testing.T) {
	model := treeModel(nil, nil)
	output := model.renderTmux(80, 0)

	for _, want := range []string{"code  attached  2 windows", "├─ ▸1:notifier", "└─  2:op", "%46", "%6"} {
		if !strings.Contains(output, want) {
			t.Fatalf("tree output missing %q:\n%s", want, output)
		}
	}
	if !strings.Contains(output, "└─ ") || !strings.Contains(output, "│  ") {
		t.Fatalf("tree output is not drawn with branch connectors:\n%s", output)
	}
}

func TestRenderTmuxShowsAwaitingInputBadge(t *testing.T) {
	model := treeModel(
		[]domain.PaneAgentState{{
			PaneID: "%46", WindowName: "notifier", AgentName: "claude",
			ForegroundPID: 399147, ForegroundCommand: "claude",
			Activity: domain.AgentActivityAwaitingInput, QuietSeconds: 134,
		}},
		[]domain.PaneProcessStats{{PaneID: "%46", ForegroundPID: 399147, ForegroundCommand: "claude", ProcessCount: 6}},
	)
	output := model.renderTmux(90, 0)

	if !strings.Contains(output, "input 2m14s") {
		t.Fatalf("tree output missing the waiting badge and duration:\n%s", output)
	}
	if !strings.Contains(output, "1 waiting") {
		t.Fatalf("tree header missing the waiting count:\n%s", output)
	}
	// The pane root and the process actually holding the terminal differ, and
	// both matter when deciding what to kill or attach to.
	if !strings.Contains(output, "397633→399147") {
		t.Fatalf("tree output should show the pane root and its foreground process:\n%s", output)
	}
}

func TestRenderTmuxShowsApprovalQuestion(t *testing.T) {
	model := treeModel(
		[]domain.PaneAgentState{{
			PaneID: "%46", AgentName: "claude",
			Activity: domain.AgentActivityAwaitingApproval,
			Detail:   "Do   you want   to proceed?",
		}},
		nil,
	)
	output := model.renderTmux(90, 0)

	if !strings.Contains(output, "approve") {
		t.Fatalf("tree output missing the approval badge:\n%s", output)
	}
	if !strings.Contains(output, "↳ Do you want to proceed?") {
		t.Fatalf("tree output should show the collapsed approval question:\n%s", output)
	}
}

// An ordinary input prompt matches the agent's idle footer, which adds nothing
// the badge has not already communicated.
func TestRenderTmuxOmitsDetailForPlainInputPrompts(t *testing.T) {
	model := treeModel(
		[]domain.PaneAgentState{{
			PaneID: "%46", AgentName: "claude",
			Activity: domain.AgentActivityAwaitingInput,
			Detail:   "⏵⏵ bypass permissions on (shift+tab to cycle)",
		}},
		nil,
	)
	if strings.Contains(model.renderTmux(90, 0), "bypass permissions") {
		t.Fatal("input prompts should not emit a detail line")
	}
}

func TestRenderTmuxHighlightsFocusedPane(t *testing.T) {
	model := treeModel(
		[]domain.PaneAgentState{{PaneID: "%46", Activity: domain.AgentActivityAwaitingInput, QuietSeconds: 3}},
		nil,
	)
	model.section = tmuxSection
	model.tmuxCursorPaneID = "%46"
	output := model.renderTmux(90, 0)
	if !strings.Contains(output, "▸%46") {
		t.Fatalf("focused pane missing cursor marker:\n%s", output)
	}
	if strings.Contains(output, "▸%45") {
		t.Fatalf("unfocused pane should not have a cursor marker:\n%s", output)
	}
}

func TestRenderTmuxWithoutSession(t *testing.T) {
	model := Model{haveTmux: true}
	if got := model.renderTmux(80, 0); got != "Managed session is not running" {
		t.Fatalf("renderTmux() = %q", got)
	}
}

func TestAgentsNeedingAttentionCountsOnlyBlockedAgents(t *testing.T) {
	model := treeModel([]domain.PaneAgentState{
		{PaneID: "%1", Activity: domain.AgentActivityWorking},
		{PaneID: "%2", Activity: domain.AgentActivityAwaitingInput},
		{PaneID: "%3", Activity: domain.AgentActivityAwaitingApproval},
		{PaneID: "%4", Activity: domain.AgentActivityIdle},
	}, nil)

	if got := model.agentsNeedingAttention(); got != 2 {
		t.Fatalf("agentsNeedingAttention() = %d, want 2", got)
	}
}

func TestWaitingAgentSummaryNamesBlockedAgents(t *testing.T) {
	model := treeModel([]domain.PaneAgentState{
		{PaneID: "%46", WindowName: "notifier", AgentName: "claude", Activity: domain.AgentActivityAwaitingInput},
		{PaneID: "%6", WindowName: "op", AgentName: "opencode", Activity: domain.AgentActivityWorking},
	}, nil)

	summary := model.waitingAgentSummary()
	if summary != "1 agent waiting: notifier (claude)" {
		t.Fatalf("waitingAgentSummary() = %q", summary)
	}
}

func TestWaitingAgentSummaryEmptyWhenNothingBlocked(t *testing.T) {
	model := treeModel([]domain.PaneAgentState{{PaneID: "%6", Activity: domain.AgentActivityWorking}}, nil)
	if summary := model.waitingAgentSummary(); summary != "" {
		t.Fatalf("waitingAgentSummary() = %q, want empty", summary)
	}
}

// Badges stay next to the pane description rather than touching the terminal
// edge, where tmux may wrap the trailing duration.
func TestBadgesStayOnPaneLine(t *testing.T) {
	const width = 96
	model := treeModel(
		[]domain.PaneAgentState{
			{PaneID: "%46", AgentName: "claude", Activity: domain.AgentActivityAwaitingInput, QuietSeconds: 3},
			{PaneID: "%6", AgentName: "opencode", Activity: domain.AgentActivityWorking},
		},
		[]domain.PaneProcessStats{
			{PaneID: "%46", ForegroundPID: 399147, ForegroundCommand: "claude", ProcessCount: 6},
			{PaneID: "%6", ForegroundPID: 539062, ForegroundCommand: "opencode", ProcessCount: 2},
		},
	)

	for _, line := range strings.Split(model.renderTmux(width, 0), "\n") {
		if !strings.Contains(line, "●") && !strings.Contains(line, "◐") {
			continue
		}
		if got := lipgloss.Width(line); got > width {
			t.Fatalf("badge line width = %d, exceeds %d: %q", got, width, line)
		}
		if strings.HasSuffix(line, "●") || strings.HasSuffix(line, "◐") {
			t.Fatalf("badge text was separated from its marker: %q", line)
		}
	}
}

// The badge is the reason to read the panel, so it must survive a terminal too
// narrow to show everything else.
func TestPaneBadgeSurvivesNarrowWidth(t *testing.T) {
	model := treeModel(
		[]domain.PaneAgentState{{PaneID: "%46", AgentName: "claude", Activity: domain.AgentActivityAwaitingInput, QuietSeconds: 30}},
		nil,
	)
	if !strings.Contains(model.renderTmux(34, 0), "input 30s") {
		t.Fatalf("badge was truncated away at narrow width:\n%s", model.renderTmux(34, 0))
	}
}
