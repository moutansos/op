package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/moutansos/op/internal/domain"
)

var (
	waitingColor  = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	approvalColor = lipgloss.AdaptiveColor{Light: "#A51D2D", Dark: "#FF8A8A"}
	workingColor  = lipgloss.AdaptiveColor{Light: "#166534", Dark: "#6EE7A0"}

	waitingStyle  = lipgloss.NewStyle().Bold(true).Foreground(waitingColor)
	approvalStyle = lipgloss.NewStyle().Bold(true).Foreground(approvalColor)
	workingStyle  = lipgloss.NewStyle().Foreground(workingColor)
)

// agentsByPane indexes the latest agent classifications by pane.
func (m Model) agentsByPane() map[string]domain.PaneAgentState {
	states := make(map[string]domain.PaneAgentState, len(m.stats.Agents))
	for _, state := range m.stats.Agents {
		states[state.PaneID] = state
	}
	return states
}

// processesByPane indexes the latest per-pane process aggregates.
func (m Model) processesByPane() map[string]domain.PaneProcessStats {
	processes := make(map[string]domain.PaneProcessStats, len(m.stats.Processes))
	for _, process := range m.stats.Processes {
		processes[process.PaneID] = process
	}
	return processes
}

// agentsNeedingAttention counts agents that have stopped and are blocked on the
// operator. It drives the one number worth glancing at.
func (m Model) agentsNeedingAttention() int {
	count := 0
	for _, state := range m.stats.Agents {
		if state.Activity.NeedsAttention() {
			count++
		}
	}
	return count
}

// waitingAgentSummary names the blocked agents compactly, or returns empty when
// nothing is waiting.
func (m Model) waitingAgentSummary() string {
	names := make([]string, 0, len(m.stats.Agents))
	for _, state := range m.stats.Agents {
		if !state.Activity.NeedsAttention() {
			continue
		}
		label := state.WindowName
		if label == "" {
			label = state.PaneID
		}
		names = append(names, fmt.Sprintf("%s (%s)", label, state.AgentName))
	}
	if len(names) == 0 {
		return ""
	}
	summary := fmt.Sprintf("%d agent%s waiting: %s", len(names), plural(len(names)), strings.Join(names, ", "))
	return summary
}

func plural(count int) string {
	if count == 1 {
		return ""
	}
	return "s"
}

// renderTmux draws the managed session as a tree of windows, panes, and the
// process actually holding each pane's terminal, annotated with what any agent
// found there is doing.
func (m Model) renderTmux(width, height int) string {
	if m.tmux.Session == nil {
		return "Managed session is not running"
	}
	if width < 1 {
		width = 1
	}
	session := m.tmux.Session
	agentStates := m.agentsByPane()
	processes := m.processesByPane()

	attached := "detached"
	if session.Attached {
		attached = "attached"
	}
	summary := fmt.Sprintf("%s  %s  %d windows", session.Name, attached, len(session.Windows))
	if waiting := m.agentsNeedingAttention(); waiting > 0 {
		summary += "  " + waitingStyle.Render(fmt.Sprintf("%d waiting", waiting))
	}
	lines := []string{summary}

	for windowIndex, window := range session.Windows {
		windowBranch, windowTrunk := treeBranch(windowIndex == len(session.Windows)-1)
		marker := " "
		if window.Active {
			marker = "▸"
		}
		label := fmt.Sprintf("%s%s%d:%s", windowBranch, marker, window.Index, window.Name)
		if window.Profile != "" {
			label += dimStyle.Render("  " + window.Profile)
		}
		lines = append(lines, label)

		for paneIndex, pane := range window.Panes {
			paneBranch, paneTrunk := treeBranch(paneIndex == len(window.Panes)-1)
			prefix := windowTrunk + paneBranch
			lines = append(lines, m.renderPaneLine(prefix, width, pane, processes[pane.ID], agentStates[pane.ID]))
			if detail := paneDetailLine(agentStates[pane.ID]); detail != "" {
				lines = append(lines, dimStyle.Render(truncate(windowTrunk+paneTrunk+"  "+detail, width)))
			}
		}
	}
	return limitLines(lines, height)
}

// renderPaneLine composes one pane row. The badge is laid out first and the
// descriptive text is truncated around it, because an agent's state is the
// reason to read this panel at all and must survive a narrow terminal.
func (m Model) renderPaneLine(
	prefix string,
	width int,
	pane domain.TmuxPane,
	process domain.PaneProcessStats,
	agent domain.PaneAgentState,
) string {
	badgeText, badgeStyle := agentBadge(agent)
	body := pane.ID + "  " + paneCommandLabel(pane, process)
	if pane.Dead {
		body += "  [dead]"
	} else if process.ProcessCount > 1 {
		body += fmt.Sprintf("  %dp", process.ProcessCount)
	}

	available := width - runeLen(prefix)
	if badgeText != "" {
		available -= runeLen(badgeText) + 2
	}
	line := prefix + truncate(body, max(0, available))
	if badgeText == "" {
		return dimStyle.Render(line)
	}
	pad := width - runeLen(line) - runeLen(badgeText)
	if pad < 1 {
		pad = 1
	}
	return dimStyle.Render(line) + strings.Repeat(" ", pad) + badgeStyle.Render(badgeText)
}

// paneCommandLabel names the process that owns the pane's input. tmux reports
// the foreground command too, but the resolved PID is what distinguishes one
// agent run from the next, so it is shown when it is known and non-trivial.
func paneCommandLabel(pane domain.TmuxPane, process domain.PaneProcessStats) string {
	command := process.ForegroundCommand
	if command == "" {
		command = pane.CurrentCommand
	}
	if process.ForegroundPID > 0 && process.ForegroundPID != pane.PID {
		return fmt.Sprintf("%s %d\u2192%d", command, pane.PID, process.ForegroundPID)
	}
	return fmt.Sprintf("%s %d", command, pane.PID)
}

// paneDetailLine surfaces the question an agent is blocked on, so the operator
// can decide without switching to the pane. Only approvals earn a line: the text
// matched for an ordinary input prompt is the agent's idle footer, which says
// nothing the badge has not already said.
func paneDetailLine(agent domain.PaneAgentState) string {
	if agent.Activity != domain.AgentActivityAwaitingApproval || agent.Detail == "" {
		return ""
	}
	return "↳ " + collapseSpaces(agent.Detail)
}

func agentBadge(agent domain.PaneAgentState) (string, lipgloss.Style) {
	switch agent.Activity {
	case domain.AgentActivityAwaitingApproval:
		return "▲ approve " + formatDuration(agent.QuietSeconds), approvalStyle
	case domain.AgentActivityAwaitingInput:
		return "● input " + formatDuration(agent.QuietSeconds), waitingStyle
	case domain.AgentActivityWorking:
		return "◐ working", workingStyle
	case domain.AgentActivityIdle:
		return "○ idle " + formatDuration(agent.QuietSeconds), dimStyle
	case domain.AgentActivityStarting:
		return "· starting", dimStyle
	case domain.AgentActivityUnknown:
		if agent.AgentName != "" {
			return "· unknown", dimStyle
		}
	}
	return "", dimStyle
}

// treeBranch returns the connector for an entry and the continuation prefix its
// children must carry.
func treeBranch(last bool) (branch, trunk string) {
	if last {
		return "└─ ", "   "
	}
	return "├─ ", "│  "
}

func collapseSpaces(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func runeLen(value string) int {
	return lipgloss.Width(value)
}
