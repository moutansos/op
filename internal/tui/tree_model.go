package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/moutansos/op/internal/domain"
)

type processTreeModel struct {
	Model
	err              error
	usingCachedStats bool
}

func newTreeModel(ctx context.Context, service domain.Service, options Options) processTreeModel {
	m := NewModel(ctx, service, options)
	m.section = tmuxSection
	m.status = "Loading process tree..."
	if snapshot, ok := readSnapshotCache(m.options.SnapshotCachePath, m.options.SnapshotMaxAge, time.Now()); ok {
		m.tmux, m.haveTmux = snapshot.Tmux, true
		m.stats, m.haveStats = snapshot.Stats, true
		m.focusTmuxCursor()
		m.status = "Refreshing process tree..."
		return processTreeModel{Model: m, usingCachedStats: true}
	}
	return processTreeModel{Model: m}
}

func (m processTreeModel) Init() tea.Cmd {
	return tea.Batch(
		m.loadTmuxCmd(), m.loadStatsCmd(),
		m.tmuxTickCmd(), m.statsTickCmd(),
	)
}

func (m processTreeModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tmuxLoadedMsg:
		m.tmuxRefreshing = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.tmux, m.haveTmux = msg.snapshot, true
			m.focusTmuxCursor()
		}
	case statsLoadedMsg:
		m.statsRefreshing = false
		if msg.err != nil {
			m.err = msg.err
		} else {
			if m.usingCachedStats {
				msg.snapshot.Agents = preserveStartingAgents(msg.snapshot.Agents, m.stats.Agents)
				m.usingCachedStats = false
			}
			m.stats, m.haveStats = msg.snapshot, true
			m.focusTmuxCursor()
		}
	case tmuxTickMsg:
		cmds := []tea.Cmd{m.tmuxTickCmd()}
		if !m.tmuxRefreshing {
			m.tmuxRefreshing = true
			cmds = append(cmds, m.loadTmuxCmd())
		}
		return m, tea.Batch(cmds...)
	case statsTickMsg:
		cmds := []tea.Cmd{m.statsTickCmd()}
		if !m.statsRefreshing {
			m.statsRefreshing = true
			cmds = append(cmds, m.loadStatsCmd())
		}
		return m, tea.Batch(cmds...)
	case selectPaneFinishedMsg:
		m.operation = ""
		if msg.err != nil {
			m.err = msg.err
			return m, nil
		}
		m.cancel()
		return m, tea.Quit
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			m.cancel()
			return m, tea.Quit
		case "up", "k":
			m.moveTmuxCursor(-1)
		case "down", "j":
			m.moveTmuxCursor(1)
		case "enter":
			if m.operation == "" && m.tmuxCursorPaneID != "" {
				m.operation = "select-pane"
				return m, m.selectPaneCmd(m.tmuxCursorPaneID)
			}
		case "r":
			m.tmuxRefreshing = true
			m.statsRefreshing = true
			return m, tea.Batch(m.loadTmuxCmd(), m.loadStatsCmd())
		}
	}
	return m, nil
}

func preserveStartingAgents(current, cached []domain.PaneAgentState) []domain.PaneAgentState {
	byPane := make(map[string]domain.PaneAgentState, len(cached))
	for _, agent := range cached {
		byPane[agent.PaneID] = agent
	}
	for index, agent := range current {
		if agent.Activity != domain.AgentActivityStarting {
			continue
		}
		if previous, ok := byPane[agent.PaneID]; ok {
			current[index] = previous
		}
	}
	return current
}

func (m processTreeModel) View() string {
	width, height := max(1, m.width), max(1, m.height)
	header := titleStyle.Render("Process Tree")
	footer := dimStyle.Render("j/k move  enter select  r refresh  q close")
	if m.err != nil {
		footer = errorStyle.Render(m.err.Error())
	} else if m.operation != "" {
		footer = dimStyle.Render("Selecting pane...")
	}
	bodyHeight := max(1, height-2)
	body := m.renderTmux(width, bodyHeight)
	view := strings.Join([]string{header, body, footer}, "\n")
	return lipgloss.NewStyle().Width(width).Height(height).Render(fmt.Sprintf("%s", view))
}

// RunTree starts the focused process-tree selector used by the tmux popup.
func RunTree(ctx context.Context, service domain.Service, options Options) error {
	if service == nil {
		return fmt.Errorf("tui: service is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	model := newTreeModel(ctx, service, options)
	defer model.cancel()
	_, err := tea.NewProgram(model, tea.WithContext(ctx), tea.WithAltScreen()).Run()
	return err
}
