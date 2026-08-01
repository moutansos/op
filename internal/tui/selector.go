package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// Selector is the interactive action-selection boundary used by the CLI.
// A nil action means the user canceled the selector.
type Selector func(context.Context, string, []Action, io.Reader, io.Writer) (*Action, error)

// SelectAction runs an in-process fuzzy action selector in the alternate screen.
func SelectAction(ctx context.Context, title string, actions []Action, input io.Reader, output io.Writer) (*Action, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if input == nil {
		input = os.Stdin
	}
	if output == nil {
		output = os.Stdout
	}
	model := newSelectorModel(title, actions)
	result, err := tea.NewProgram(model,
		tea.WithContext(ctx),
		tea.WithInput(input),
		tea.WithOutput(output),
		tea.WithAltScreen(),
	).Run()
	if err != nil {
		return nil, err
	}
	final, ok := result.(selectorModel)
	if !ok || final.canceled || final.selected == nil {
		return nil, nil
	}
	selected := *final.selected
	return &selected, nil
}

type selectorMatch struct {
	action  Action
	indexes []int
}

type selectorModel struct {
	title    string
	actions  []Action
	input    textinput.Model
	matches  []selectorMatch
	cursor   int
	offset   int
	width    int
	height   int
	selected *Action
	canceled bool
}

func newSelectorModel(title string, actions []Action) selectorModel {
	filter := textinput.New()
	filter.Prompt = "> "
	filter.Placeholder = "type to filter"
	filter.CharLimit = 256
	filter.Focus()
	model := selectorModel{
		title:   title,
		actions: append([]Action(nil), actions...),
		input:   filter,
		width:   80,
		height:  24,
	}
	model.refilter()
	model.resizeInput()
	return model
}

func (m selectorModel) Init() tea.Cmd { return textinput.Blink }

func (m selectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(1, msg.Width)
		m.height = max(1, msg.Height)
		m.resizeInput()
		m.ensureVisible()
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "esc", "ctrl+c":
			m.canceled = true
			return m, tea.Quit
		case "enter":
			if len(m.matches) == 0 {
				return m, nil
			}
			selected := m.matches[m.cursor].action
			m.selected = &selected
			return m, tea.Quit
		case "up", "ctrl+p":
			m.move(-1)
			return m, nil
		case "down", "ctrl+n":
			m.move(1)
			return m, nil
		}
	}

	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	if m.input.Value() != before {
		m.refilter()
	}
	return m, cmd
}

func (m selectorModel) View() string {
	if m.height <= 2 || m.width < 16 {
		lines := []string{truncate(m.title, m.width), truncate(m.input.View(), m.width)}
		return strings.Join(lines[:min(len(lines), m.height)], "\n")
	}

	lines := []string{titleStyle.Render(truncate(m.title, m.width)), truncate(m.input.View(), m.width)}
	visible := m.visibleRows()
	switch {
	case len(m.actions) == 0:
		lines = append(lines, dimStyle.Render("No actions available."))
	case len(m.matches) == 0:
		lines = append(lines, dimStyle.Render("No matches."))
	default:
		end := min(len(m.matches), m.offset+visible)
		for index := m.offset; index < end; index++ {
			marker := "  "
			if index == m.cursor {
				marker = "> "
			}
			lines = append(lines, truncate(marker+m.matches[index].action.Name, m.width))
		}
		if len(m.matches) > visible {
			lines = append(lines, dimStyle.Render(fmt.Sprintf("%d-%d of %d", m.offset+1, end, len(m.matches))))
		}
	}
	lines = append(lines, dimStyle.Render("enter select   up/down navigate   esc cancel"))
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	return strings.Join(lines, "\n")
}

func (m *selectorModel) refilter() {
	term := m.input.Value()
	m.matches = m.matches[:0]
	if term == "" {
		for _, action := range m.actions {
			m.matches = append(m.matches, selectorMatch{action: action})
		}
	} else {
		targets := make([]string, len(m.actions))
		for index, action := range m.actions {
			targets[index] = action.Name + " " + action.ID
		}
		for _, rank := range list.DefaultFilter(term, targets) {
			m.matches = append(m.matches, selectorMatch{
				action:  m.actions[rank.Index],
				indexes: append([]int(nil), rank.MatchedIndexes...),
			})
		}
	}
	m.cursor = 0
	m.offset = 0
}

func (m *selectorModel) move(delta int) {
	if len(m.matches) == 0 {
		return
	}
	m.cursor = max(0, min(len(m.matches)-1, m.cursor+delta))
	m.ensureVisible()
}

func (m *selectorModel) ensureVisible() {
	visible := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	maximum := max(0, len(m.matches)-visible)
	m.offset = max(0, min(maximum, m.offset))
}

func (m selectorModel) visibleRows() int {
	return max(1, m.height-4)
}

func (m *selectorModel) resizeInput() {
	m.input.Width = max(1, m.width-2)
}
