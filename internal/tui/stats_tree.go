package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/moutansos/op/internal/domain"
)

// minWindowsRowsPerPane is how far a pane's Windows tree is collapsed when the
// panel cannot show every process.
const minWindowsRowsPerPane = 3

// hotCPUPercent marks a process using at least half of one CPU.
const hotCPUPercent = 50

var hotStyle = lipgloss.NewStyle().Bold(true).Foreground(warnColor)

type statsColumns struct{ cpu, memory, uptime bool }

// statsColumnsFor drops the uptime and then the memory column before it
// lets labels shrink below a useful width.
func statsColumnsFor(width int) statsColumns {
	switch {
	case width >= 56:
		return statsColumns{cpu: true, memory: true, uptime: true}
	case width >= 40:
		return statsColumns{cpu: true, memory: true}
	case width >= 24:
		return statsColumns{cpu: true}
	default:
		return statsColumns{}
	}
}

func (c statsColumns) width() int {
	width := 0
	if c.cpu {
		width += 7
	}
	if c.memory {
		width += 10
	}
	if c.uptime {
		width += 8
	}
	return width
}

type statsTreeRow struct {
	label  string
	cpu    string
	hot    bool
	memory string
	uptime string
	dim    bool
}

func (c statsColumns) render(row statsTreeRow, width int) string {
	labelWidth := max(1, width-c.width())
	label := ansi.Truncate(row.label, labelWidth, "…")
	label += strings.Repeat(" ", max(0, labelWidth-ansi.StringWidth(label)))
	var cells strings.Builder
	if c.cpu {
		cpu := fmt.Sprintf(" %6s", row.cpu)
		if row.hot {
			cpu = " " + hotStyle.Render(fmt.Sprintf("%6s", row.cpu))
		}
		cells.WriteString(cpu)
	}
	if c.memory {
		cells.WriteString(fmt.Sprintf(" %9s", row.memory))
	}
	if c.uptime {
		cells.WriteString(fmt.Sprintf(" %7s", row.uptime))
	}
	line := strings.TrimRight(label+cells.String(), " ")
	if row.dim {
		return dimStyle.Render(line)
	}
	return line
}

type statsTotal struct {
	cpu          float64
	cpuAvailable bool
	memory       uint64
}

func (t *statsTotal) add(process domain.PaneProcessStats) {
	t.memory += process.ResidentBytes
	if process.CPUAvailable {
		t.cpu += process.CPUPercent
		t.cpuAvailable = true
	}
}

func (t statsTotal) row(label string) statsTreeRow {
	return statsTreeRow{label: label, cpu: formatCPU(t.cpu, t.cpuAvailable), hot: t.cpuAvailable && t.cpu >= hotCPUPercent, memory: formatBytes(t.memory)}
}

func formatCPU(percent float64, available bool) string {
	if !available {
		return "-"
	}
	return fmt.Sprintf("%.1f%%", percent)
}

type statsWindow struct {
	label string
	panes []statsPane
}

type statsPane struct {
	pane    domain.TmuxPane
	process domain.PaneProcessStats
}

// statsWindows groups pane statistics the way the Tmux panel does. Without a
// tmux snapshot they are grouped by the window names they carry.
func (m Model) statsWindows() (string, []statsWindow) {
	processes := m.processesByPane()
	if session := m.tmux.Session; session != nil {
		windows := make([]statsWindow, 0, len(session.Windows))
		for _, window := range session.Windows {
			grouped := statsWindow{label: fmt.Sprintf("%d:%s", window.Index, window.Name)}
			for _, pane := range window.Panes {
				grouped.panes = append(grouped.panes, statsPane{pane: pane, process: processes[pane.ID]})
			}
			windows = append(windows, grouped)
		}
		return session.Name, windows
	}
	var windows []statsWindow
	index := make(map[string]int)
	for _, process := range m.stats.Processes {
		position, found := index[process.WindowName]
		if !found {
			position = len(windows)
			index[process.WindowName] = position
			windows = append(windows, statsWindow{label: process.WindowName})
		}
		pane := domain.TmuxPane{ID: process.PaneID, PID: process.RootPID, CurrentCommand: process.Command, Dead: process.Dead}
		windows[position].panes = append(windows[position].panes, statsPane{pane: pane, process: process})
	}
	return "", windows
}

// processTreeLines renders the session, its windows, their panes, and the
// Windows processes started from each pane, with CPU, memory, and uptime
// aligned in columns. Session, window, and pane rows show the totals below
// them. Every Windows process is shown when the tree fits in height rows;
// otherwise the largest Windows trees are collapsed first.
func (m Model) processTreeLines(width, height int) []string {
	columns := statsColumnsFor(width)
	sessionName, windows := m.statsWindows()
	if len(windows) == 0 {
		return []string{"No tmux-owned processes"}
	}
	limit := windowsRowLimit(windows, height)

	var session statsTotal
	for _, window := range windows {
		for _, pane := range window.panes {
			session.add(pane.process)
		}
	}
	header := statsTreeRow{label: "", cpu: "CPU", memory: "MEM", uptime: "UP", dim: true}
	if !m.stats.CapturedAt.IsZero() {
		header.label = "sample " + m.stats.CapturedAt.Format("3:04PM")
	}
	rootLabel := sessionName
	if rootLabel == "" {
		rootLabel = "processes"
	}
	lines := []string{columns.render(header, width), columns.render(session.row(rootLabel), width)}

	for windowIndex, window := range windows {
		windowBranch, windowTrunk := treeBranch(windowIndex == len(windows)-1)
		var total statsTotal
		for _, pane := range window.panes {
			total.add(pane.process)
		}
		lines = append(lines, columns.render(total.row(windowBranch+window.label), width))

		for paneIndex, pane := range window.panes {
			paneBranch, paneTrunk := treeBranch(paneIndex == len(window.panes)-1)
			lines = append(lines, columns.render(paneRow(windowTrunk+paneBranch, pane), width))
			for _, child := range windowsProcessRows(windowTrunk+paneTrunk, pane.process.WindowsProcesses, limit) {
				lines = append(lines, columns.render(child, width))
			}
		}
	}
	return lines
}

// windowsRowLimit returns the most Windows processes each pane can show
// while the tree fits in height rows, never fewer than minWindowsRowsPerPane.
// A pane whose tree is cut also gets a "more" row. Height zero means
// unlimited.
func windowsRowLimit(windows []statsWindow, height int) int {
	fixed, largest := 2, 0
	var sizes []int
	for _, window := range windows {
		fixed += 1 + len(window.panes)
		for _, pane := range window.panes {
			if size := len(pane.process.WindowsProcesses); size > 0 {
				sizes = append(sizes, size)
				largest = max(largest, size)
			}
		}
	}
	if height <= 0 {
		return largest
	}
	rows := func(limit int) int {
		total := fixed
		for _, size := range sizes {
			if size > limit {
				total += limit + 1
			} else {
				total += size
			}
		}
		return total
	}
	limit := largest
	for limit > minWindowsRowsPerPane && rows(limit) > height {
		limit--
	}
	return limit
}

func paneRow(prefix string, pane statsPane) statsTreeRow {
	process := pane.process
	// tmux names a Windows program's interop stub "init"; the foreground
	// process name is the program itself, as in the Tmux panel.
	command := process.ForegroundCommand
	for _, candidate := range []string{process.Command, pane.pane.CurrentCommand} {
		if command == "" {
			command = candidate
		}
	}
	label := fmt.Sprintf("%s%s %s %d", prefix, pane.pane.ID, command, pane.pane.PID)
	if pane.pane.Dead || process.Dead {
		return statsTreeRow{label: label + " [dead]", cpu: "-", memory: "-", uptime: "-", dim: true}
	}
	return statsTreeRow{
		label: label, cpu: formatCPU(process.CPUPercent, process.CPUAvailable),
		hot:    process.CPUAvailable && process.CPUPercent >= hotCPUPercent,
		memory: formatBytes(process.ResidentBytes), uptime: formatDuration(process.UptimeSeconds),
	}
}

// windowsProcessRows draws a pane's Windows processes, which arrive in
// depth-first order with depths, as a tree below the pane.
func windowsProcessRows(prefix string, processes []domain.WindowsProcessStats, limit int) []statsTreeRow {
	shown := processes
	if len(shown) > limit {
		shown = shown[:limit]
	}
	rows := make([]statsTreeRow, 0, len(shown)+1)
	// trunks[d] continues the branch at depth d for rows below it.
	var trunks []string
	for index, process := range shown {
		depth := min(process.Depth, len(trunks))
		trunks = trunks[:depth]
		branch, trunk := treeBranch(isLastSibling(processes, index))
		rows = append(rows, statsTreeRow{
			label:  prefix + strings.Join(trunks, "") + branch + fmt.Sprintf("%s %d", process.Name, process.PID),
			cpu:    formatCPU(process.CPUPercent, process.CPUAvailable),
			hot:    process.CPUAvailable && process.CPUPercent >= hotCPUPercent,
			memory: formatBytes(process.WorkingSetBytes), uptime: formatDuration(process.UptimeSeconds),
			dim: true,
		})
		trunks = append(trunks, trunk)
	}
	if hidden := len(processes) - len(shown); hidden > 0 {
		rows = append(rows, statsTreeRow{label: fmt.Sprintf("%s… %d more Windows processes", prefix, hidden), dim: true})
	}
	return rows
}

// isLastSibling reports whether no later process shares the parent of the
// process at index in a depth-first listing.
func isLastSibling(processes []domain.WindowsProcessStats, index int) bool {
	depth := processes[index].Depth
	for _, later := range processes[index+1:] {
		if later.Depth < depth {
			return true
		}
		if later.Depth == depth {
			return false
		}
	}
	return true
}
