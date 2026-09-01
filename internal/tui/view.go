package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/moutansos/op/internal/domain"
)

var (
	accentColor = lipgloss.AdaptiveColor{Light: "#18794E", Dark: "#86EFAC"}
	branchColor = lipgloss.AdaptiveColor{Light: "#2563EB", Dark: "#7DB7FF"}
	cleanColor  = lipgloss.AdaptiveColor{Light: "#1F8A3B", Dark: "#5EDC84"}
	dirtyColor  = lipgloss.AdaptiveColor{Light: "#C96A00", Dark: "#FFB24A"}
	dimColor    = lipgloss.AdaptiveColor{Light: "#666666", Dark: "#8A8A8A"}
	errorColor  = lipgloss.AdaptiveColor{Light: "#A51D2D", Dark: "#FF6B7A"}

	titleStyle         = lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	branchStyle        = lipgloss.NewStyle().Foreground(branchColor)
	projectBranchStyle = branchStyle.Copy()
	projectCleanStyle  = lipgloss.NewStyle().Foreground(cleanColor)
	projectDirtyStyle  = lipgloss.NewStyle().Foreground(dirtyColor)
	dimStyle           = lipgloss.NewStyle().Foreground(dimColor)
	errorStyle         = lipgloss.NewStyle().Foreground(errorColor)
)

// View renders the dashboard for wide, stacked, tabbed, and minimum-size layouts.
func (m Model) View() string {
	if m.width < minimumWidth || m.height < minimumHeight {
		return lipgloss.NewStyle().
			Width(max(1, m.width)).
			Height(max(1, m.height)).
			Align(lipgloss.Center, lipgloss.Center).
			Render(fmt.Sprintf("op dashboard needs at least %dx%d\ncurrent terminal: %dx%d", minimumWidth, minimumHeight, m.width, m.height))
	}

	var dashboard string
	switch {
	case m.height < 26:
		dashboard = m.tabbedView()
	case m.width >= wideWidth:
		dashboard = m.wideView()
	case m.width >= narrowWidth:
		dashboard = m.stackedView()
	default:
		dashboard = m.tabbedView()
	}
	if m.overlay != noOverlay {
		return m.overlayView(dashboard)
	}
	if m.operation == "open" {
		return m.openingView()
	}
	return dashboard
}

func (m Model) openingView() string {
	box := renderPanel("Opening Project", m.status, min(44, m.width-6), 0, true, nil)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceChars("·"),
		lipgloss.WithWhitespaceForeground(lipgloss.AdaptiveColor{Light: "#D4E3D8", Dark: "#303E34"}),
	)
}

func (m Model) wideView() string {
	leftWidth := (m.width - 1) / 2
	rightWidth := m.width - leftWidth - 1
	contentHeight := m.height - 1
	projectHeight := contentHeight - 5
	// The tmux tree carries agent state and grows with the number of panes, so
	// it takes the larger share and the fixed-size host metrics take the rest.
	statsHeight := max(8, contentHeight/3)
	tmuxHeight := contentHeight - statsHeight

	left := lipgloss.JoinVertical(lipgloss.Left,
		m.projectPanel(leftWidth, projectHeight),
		m.statusPanel(leftWidth, 5),
	)
	right := lipgloss.JoinVertical(lipgloss.Left,
		m.statsPanel(rightWidth, statsHeight),
		m.tmuxPanel(rightWidth, tmuxHeight),
	)
	return lipgloss.JoinHorizontal(lipgloss.Top, left, " ", right)
}

func (m Model) stackedView() string {
	projectHeight := max(7, m.height/2)
	remaining := m.height - projectHeight - 5
	tmuxHeight := max(6, (remaining*3)/5)
	statsHeight := remaining - tmuxHeight
	return lipgloss.JoinVertical(lipgloss.Left,
		m.projectPanel(m.width, projectHeight),
		m.statsPanel(m.width, statsHeight),
		m.tmuxPanel(m.width, tmuxHeight),
		m.statusPanel(m.width, 5),
	)
}

func (m Model) tabHeader() string {
	tabs := []string{"1 Projects", "2 System", "3 Tmux"}
	for index := range tabs {
		style := dimStyle.Padding(0, 1)
		if section(index) == m.section {
			style = titleStyle.Copy().Underline(true).Padding(0, 1)
		}
		tabs[index] = style.Render(tabs[index])
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}

func (m Model) tabbedView() string {
	header := m.tabHeader()
	contentHeight := m.height - 6
	var content string
	switch m.section {
	case statsSection:
		content = m.statsPanel(m.width, contentHeight)
	case tmuxSection:
		content = m.tmuxPanel(m.width, contentHeight)
	default:
		content = m.projectPanel(m.width, contentHeight)
	}
	return lipgloss.JoinVertical(lipgloss.Left, header, content, m.statusPanel(m.width, 5))
}

func (m Model) projectPanel(width, height int) string {
	title := "Projects"
	if m.projectsErr != nil {
		title += "  stale"
	}
	return renderPanel(title, m.projects.View(), width, height, m.section == projectsSection, m.projectsErr)
}

func (m Model) statsPanel(width, height int) string {
	title := "System + Processes"
	if m.statsErr != nil {
		title += "  stale"
	}
	body := "Statistics unavailable"
	if m.haveStats {
		body = m.renderStats(max(1, width-4), max(1, height-3))
	}
	return renderPanel(title, body, width, height, m.section == statsSection, m.statsErr)
}

func (m Model) tmuxPanel(width, height int) string {
	title := "Tmux"
	if waiting := m.agentsNeedingAttention(); waiting > 0 {
		title += fmt.Sprintf("  %d agent%s waiting", waiting, plural(waiting))
	}
	if m.tmuxErr != nil {
		title += "  stale"
	}
	body := "Tmux snapshot unavailable"
	if m.haveTmux {
		body = m.renderTmux(max(1, width-4), max(1, height-3))
	}
	return renderPanel(title, body, width, height, m.section == tmuxSection, m.tmuxErr)
}

func (m Model) statusPanel(width, height int) string {
	status := m.status
	if status == "" {
		status = "Ready"
	}
	if m.operation != "" {
		status = "[busy] " + status
	}
	if m.statusErr {
		status = errorStyle.Render(status)
	}
	// Agent attention is surfaced here as well as in the tree because the tmux
	// panel is hidden in the tabbed layout, and a blocked agent is exactly the
	// thing the operator should not have to go looking for.
	if waiting := m.waitingAgentSummary(); waiting != "" {
		status += "   " + waitingStyle.Render(waiting)
	}
	help := dimStyle.Render("enter default   a open with   w worktree   / filter   n new   c clone   r refresh   q quit")
	if m.section == tmuxSection {
		help = dimStyle.Render("enter select pane   j/k move   click pane   tab sections   r refresh   q quit")
	}
	return renderPanel("Actions / Status", status+"\n"+help, width, height, false, nil)
}

func (m Model) tmuxBodyOrigin() (x, y, width, height int, ok bool) {
	if m.width < minimumWidth || m.height < minimumHeight {
		return 0, 0, 0, 0, false
	}
	switch {
	case m.height < 26 || m.width < narrowWidth:
		if m.section != tmuxSection {
			return 0, 0, 0, 0, false
		}
		return 2, lipgloss.Height(m.tabHeader()) + 2, max(1, m.width-4), max(1, m.height-9), true
	case m.width >= wideWidth:
		leftWidth := (m.width - 1) / 2
		rightWidth := m.width - leftWidth - 1
		contentHeight := m.height - 1
		projectHeight := contentHeight - 5
		statsHeight := max(8, contentHeight/3)
		tmuxHeight := contentHeight - statsHeight
		left := lipgloss.JoinVertical(lipgloss.Left,
			m.projectPanel(leftWidth, projectHeight),
			m.statusPanel(leftWidth, 5),
		)
		stats := m.statsPanel(rightWidth, statsHeight)
		return lipgloss.Width(left) + 3, lipgloss.Height(stats) + 2, max(1, rightWidth-4), max(1, tmuxHeight-3), true
	default:
		projectHeight := max(7, m.height/2)
		remaining := m.height - projectHeight - 5
		tmuxHeight := max(6, (remaining*3)/5)
		statsHeight := remaining - tmuxHeight
		projects := m.projectPanel(m.width, projectHeight)
		stats := m.statsPanel(m.width, statsHeight)
		return 2, lipgloss.Height(projects) + lipgloss.Height(stats) + 2, max(1, m.width-4), max(1, tmuxHeight-3), true
	}
}

func (m Model) renderStats(width, height int) string {
	host := m.stats.Host
	lines := []string{
		truncate(fmt.Sprintf("CPU %5.1f%%   Memory %s / %s", host.CPUPercent, formatBytes(host.MemoryUsed), formatBytes(host.MemoryTotal)), width),
		truncate(fmt.Sprintf("Load %.2f %.2f %.2f   Uptime %s", host.LoadAverage[0], host.LoadAverage[1], host.LoadAverage[2], formatDuration(host.UptimeSeconds)), width),
	}
	if !m.stats.CapturedAt.IsZero() {
		lines = append(lines, dimStyle.Render(truncate("sample "+m.stats.CapturedAt.Format(time.Kitchen), width)))
	}
	if len(m.stats.Processes) == 0 {
		return strings.Join(append(lines, "", "No tmux-owned processes"), "\n")
	}

	lines = append(lines, "")
	if width >= 72 {
		lines = append(lines, dimStyle.Render(truncate(fmt.Sprintf("%-14s %-7s %6s %-16s %6s %8s %8s", "WINDOW", "PANE", "PID", "COMMAND", "CPU", "RSS", "UPTIME"), width)))
		for _, process := range m.stats.Processes {
			state := ""
			if process.Dead {
				state = " dead"
			}
			line := fmt.Sprintf("%-14s %-7s %6d %-16s %6s %8s %8s%s",
				truncate(process.WindowName, 14), truncate(process.PaneID, 7), process.RootPID,
				truncate(process.Command, 16), formatProcessCPU(process), formatBytes(process.ResidentBytes),
				formatDuration(process.UptimeSeconds), state)
			lines = append(lines, truncate(line, width))
		}
	} else {
		for _, process := range m.stats.Processes {
			state := "alive"
			if process.Dead {
				state = "dead"
			}
			lines = append(lines, truncate(fmt.Sprintf("%s %s  pid %d  %s", process.WindowName, process.PaneID, process.RootPID, state), width))
			detail := fmt.Sprintf("  %s  CPU %s  RSS %s  up %s", process.Command, formatProcessCPU(process), formatBytes(process.ResidentBytes), formatDuration(process.UptimeSeconds))
			lines = append(lines, dimStyle.Render(truncate(detail, width)))
		}
	}
	return limitLines(lines, height)
}

func formatProcessCPU(process domain.PaneProcessStats) string {
	if !process.CPUAvailable {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", process.CPUPercent)
}

func (m Model) overlayView(_ string) string {
	var title, body string
	switch m.overlay {
	case openersOverlay:
		title = "Open Project With"
		lines := make([]string, 0, len(m.openers)+1)
		for index, opener := range m.openers {
			marker := "  "
			if index == m.openerIndex {
				marker = "> "
			}
			lines = append(lines, marker+opener.Name+"  "+dimStyle.Render(string(opener.Mode)))
		}
		lines = append(lines, "", dimStyle.Render("enter run   esc cancel"))
		body = strings.Join(lines, "\n")
	case createOverlay:
		title = "Create Project"
		body = m.formView()
	case cloneOverlay:
		title = "Clone Repository"
		body = m.formView()
	case worktreeOverlay:
		title = "Create Worktree"
		body = m.formView()
	}

	overlayWidth := min(max(38, m.width/2), m.width-6)
	box := renderPanel(title, body, overlayWidth, 0, true, nil)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box,
		lipgloss.WithWhitespaceChars("·"),
		lipgloss.WithWhitespaceForeground(lipgloss.AdaptiveColor{Light: "#D4E3D8", Dark: "#303E34"}),
	)
}

func (m Model) formView() string {
	lines := make([]string, len(m.inputs))
	for index := range m.inputs {
		lines[index] = m.inputs[index].View()
	}
	return strings.Join(append(lines, "", dimStyle.Render("enter submit   tab next field   esc cancel")), "\n")
}

func renderPanel(title, body string, width, height int, focused bool, stale error) string {
	borderColor := dimColor
	if focused {
		borderColor = accentColor
	}
	header := titleStyle.Render(title)
	if stale != nil {
		header += "  " + errorStyle.Render(truncate(stale.Error(), max(10, width/2)))
	}
	content := header
	if body != "" {
		content += "\n" + body
	}
	style := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(borderColor).
		Padding(0, 1).
		Width(max(1, width-4))
	if height > 0 {
		style = style.Height(max(1, height-2))
	}
	return style.Render(content)
}

func formatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%dB", bytes)
	}
	divisor, exponent := uint64(unit), 0
	for value := bytes / unit; value >= unit && exponent < 4; value /= unit {
		divisor *= unit
		exponent++
	}
	return fmt.Sprintf("%.1f%ciB", float64(bytes)/float64(divisor), "KMGTPE"[exponent])
}

func formatDuration(seconds uint64) string {
	if seconds >= 86400 {
		return fmt.Sprintf("%dd%dh", seconds/86400, (seconds%86400)/3600)
	}
	if seconds >= 3600 {
		return fmt.Sprintf("%dh%dm", seconds/3600, (seconds%3600)/60)
	}
	if seconds >= 60 {
		return fmt.Sprintf("%dm%ds", seconds/60, seconds%60)
	}
	return fmt.Sprintf("%ds", seconds)
}

func truncate(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return "…"
	}
	return string(runes[:width-1]) + "…"
}

func limitLines(lines []string, height int) string {
	if height > 0 && len(lines) > height {
		if height == 1 {
			lines = lines[:1]
		} else {
			lines = append(lines[:height-1], dimStyle.Render("…"))
		}
	}
	return strings.Join(lines, "\n")
}
