package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/moutansos/op/internal/domain"
)

var (
	waitingColor    = lipgloss.AdaptiveColor{Light: "#B45309", Dark: "#FBBF24"}
	permissionColor = lipgloss.AdaptiveColor{Light: "#7C3AED", Dark: "#C4B5FD"}
	approvalColor   = lipgloss.AdaptiveColor{Light: "#A51D2D", Dark: "#FF8A8A"}
	workingColor    = lipgloss.AdaptiveColor{Light: "#166534", Dark: "#6EE7A0"}

	waitingStyle    = lipgloss.NewStyle().Bold(true).Foreground(waitingColor)
	permissionStyle = lipgloss.NewStyle().Bold(true).Foreground(permissionColor)
	approvalStyle   = lipgloss.NewStyle().Bold(true).Foreground(approvalColor)
	workingStyle    = lipgloss.NewStyle().Foreground(workingColor)
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

type tmuxTreeLine struct {
	text   string
	paneID string
}

func (m Model) tmuxPanes() []domain.TmuxPane {
	if m.tmux.Session == nil {
		return nil
	}
	var panes []domain.TmuxPane
	for _, window := range m.tmux.Session.Windows {
		panes = append(panes, window.Panes...)
	}
	return panes
}

func (m *Model) ensureTmuxCursor() {
	m.restoreTmuxCursor(false)
}

func (m *Model) focusTmuxCursor() {
	m.restoreTmuxCursor(true)
}

func (m *Model) restoreTmuxCursor(preferWaiting bool) {
	panes := m.tmuxPanes()
	if len(panes) == 0 {
		m.tmuxCursorPaneID = ""
		return
	}
	agents := m.agentsByPane()
	valid, waiting := false, false
	for _, pane := range panes {
		if pane.ID != m.tmuxCursorPaneID {
			continue
		}
		valid = true
		waiting = agents[pane.ID].Activity.NeedsAttention()
		break
	}
	if valid && (!preferWaiting || waiting || m.agentsNeedingAttention() == 0) {
		return
	}
	for _, pane := range panes {
		if agents[pane.ID].Activity.NeedsAttention() {
			m.tmuxCursorPaneID = pane.ID
			return
		}
	}
	if valid {
		return
	}
	m.tmuxCursorPaneID = panes[0].ID
}

func (m *Model) moveTmuxCursor(delta int) {
	panes := m.tmuxPanes()
	if len(panes) == 0 {
		m.tmuxCursorPaneID = ""
		return
	}
	index := 0
	for i, pane := range panes {
		if pane.ID == m.tmuxCursorPaneID {
			index = i
			break
		}
	}
	m.tmuxCursorPaneID = panes[(index+delta+len(panes))%len(panes)].ID
}

func (m Model) paneAt(x, y int) (string, bool) {
	originX, originY, width, height, ok := m.tmuxBodyOrigin()
	if !ok || x < originX || x >= originX+width || y < originY || y >= originY+height {
		return "", false
	}
	line := y - originY
	lines := m.tmuxTreeLines(width, height)
	if line < 0 || line >= len(lines) || lines[line].paneID == "" {
		return "", false
	}
	return lines[line].paneID, true
}

func (m Model) renderTmux(width, height int) string {
	lines := m.tmuxTreeLines(width, height)
	texts := make([]string, len(lines))
	for index, line := range lines {
		texts[index] = line.text
	}
	return strings.Join(texts, "\n")
}

func (m Model) tmuxTreeLines(width, height int) []tmuxTreeLine {
	if m.tmux.Session == nil {
		return []tmuxTreeLine{{text: "Managed session is not running"}}
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
	lines := []tmuxTreeLine{{text: summary}}

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
		lines = append(lines, tmuxTreeLine{text: label})

		for paneIndex, pane := range window.Panes {
			paneBranch, paneTrunk := treeBranch(paneIndex == len(window.Panes)-1)
			prefix := windowTrunk + paneBranch
			selected := m.section == tmuxSection && pane.ID == m.tmuxCursorPaneID
			if selected {
				prefix = strings.TrimSuffix(prefix, " ") + "▸"
			}
			lines = append(lines, tmuxTreeLine{
				text:   m.renderPaneLine(prefix, width, pane, processes[pane.ID], agentStates[pane.ID], selected),
				paneID: pane.ID,
			})
			if detail := paneDetailLine(agentStates[pane.ID]); detail != "" {
				lines = append(lines, tmuxTreeLine{
					text:   dimStyle.Render(truncate(windowTrunk+paneTrunk+"  "+detail, width)),
					paneID: pane.ID,
				})
			}
		}
	}
	return limitTreeLines(lines, height)
}

func limitTreeLines(lines []tmuxTreeLine, height int) []tmuxTreeLine {
	if height > 0 && len(lines) > height {
		if height == 1 {
			return lines[:1]
		}
		return append(lines[:height-1], tmuxTreeLine{text: dimStyle.Render("…")})
	}
	return lines
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
	selected bool,
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
	bodyStyle := dimStyle
	if selected {
		bodyStyle = titleStyle
	}
	if badgeText == "" {
		return bodyStyle.Render(line)
	}
	return bodyStyle.Render(line) + "  " + badgeStyle.Render(badgeText)
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
// can decide without switching to the pane. Input prompts do not earn a line:
// the text matched there is the agent's idle footer, which says nothing the
// badge has not already said.
func paneDetailLine(agent domain.PaneAgentState) string {
	switch agent.Activity {
	case domain.AgentActivityPermissionRequired, domain.AgentActivityAwaitingApproval:
	default:
		return ""
	}
	if agent.Detail == "" {
		return ""
	}
	return "↳ " + collapseSpaces(strings.TrimLeft(agent.Detail, " \t│┃"))
}

func agentBadge(agent domain.PaneAgentState) (string, lipgloss.Style) {
	switch agent.Activity {
	case domain.AgentActivityPermissionRequired:
		return "△ permission " + formatDuration(agent.QuietSeconds), permissionStyle
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
