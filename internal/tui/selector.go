package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/moutansos/op/internal/domain"
)

var (
	selectorBorderColor  = lipgloss.AdaptiveColor{Light: "#AECFB8", Dark: "#476551"}
	selectorSelectedText = lipgloss.AdaptiveColor{Light: "#FFFFFF", Dark: "#0F2418"}
	selectorPanelStyle   = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(selectorBorderColor).
				Padding(1)
	selectorSelectedStyle = lipgloss.NewStyle().
				Bold(true).
				Foreground(selectorSelectedText).
				Background(accentColor)
)

// Selector is the interactive action-selection boundary used by the CLI.
// A nil action means the user canceled the selector.
type Selector func(context.Context, string, []Action, io.Reader, io.Writer) (*Action, error)

// ProjectSelector is the interactive project-selection boundary used by the CLI.
// A nil project means the user canceled the selector.
type ProjectSelector func(context.Context, string, []domain.Project, io.Reader, io.Writer) (*domain.Project, error)

// SelectAction runs an in-process fuzzy action selector in the alternate screen.
func SelectAction(ctx context.Context, title string, actions []Action, input io.Reader, output io.Writer) (*Action, error) {
	return selectItem(ctx, newSelectorModel(title, actions), input, output)
}

// SelectProject runs an in-process fuzzy project selector in the alternate screen.
func SelectProject(ctx context.Context, title string, projects []domain.Project, input io.Reader, output io.Writer) (*domain.Project, error) {
	items := make([]Action, len(projects))
	byID := make(map[string]domain.Project, len(projects))
	for index, project := range projects {
		search := []string{project.Name, filepath.Base(project.Path), project.Path}
		search = append(search, project.Tags...)
		items[index] = Action{Name: project.Name, ID: project.ID, Description: projectMetadata(project), Search: strings.Join(search, " ")}
		byID[project.ID] = project
	}
	selected, err := selectItem(ctx, newSelectorModelFor(title, "projects", items), input, output)
	if err != nil || selected == nil {
		return nil, err
	}
	project, found := byID[selected.ID]
	if !found {
		return nil, nil
	}
	return &project, nil
}

func selectItem(ctx context.Context, model selectorModel, input io.Reader, output io.Writer) (*Action, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if input == nil {
		input = os.Stdin
	}
	if output == nil {
		output = os.Stdout
	}
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
	noun     string
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
	return newSelectorModelFor(title, "actions", actions)
}

func newSelectorModelFor(title, noun string, actions []Action) selectorModel {
	filter := textinput.New()
	filter.Prompt = "filter  "
	filter.PromptStyle = titleStyle.Copy()
	filter.Placeholder = "type to narrow " + noun
	filter.PlaceholderStyle = dimStyle.Copy()
	filter.Cursor.Style = titleStyle.Copy()
	filter.CharLimit = 256
	filter.Focus()
	model := selectorModel{
		title:   title,
		noun:    noun,
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
	if m.compact() {
		return m.compactView()
	}

	contentWidth := m.contentWidth()
	count := dimStyle.Render(fmt.Sprintf("%d %s", len(m.actions), m.noun))
	title := titleStyle.Render(truncate(m.title, max(1, contentWidth-lipgloss.Width(count)-2)))
	headerGap := max(1, contentWidth-lipgloss.Width(title)-lipgloss.Width(count))
	lines := []string{
		title + strings.Repeat(" ", headerGap) + count,
		m.input.View(),
		"",
	}
	lines = append(lines, m.actionRows(contentWidth)...)
	lines = append(lines, m.footer(contentWidth))
	body := strings.Join(lines, "\n")
	panel := selectorPanelStyle.Copy().Width(contentWidth + 2).Render(body)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, panel)
}

func (m selectorModel) compactView() string {
	lines := []string{titleStyle.Render(truncate(m.title, m.width)), truncate(m.input.View(), m.width)}
	lines = append(lines, m.actionRows(m.width)...)
	lines = append(lines, m.footer(m.width))
	if len(lines) > m.height {
		lines = lines[:m.height]
	}
	return strings.Join(lines, "\n")
}

func (m selectorModel) actionRows(width int) []string {
	lines := make([]string, 0, m.visibleRows())
	visible := m.visibleRows()
	switch {
	case len(m.actions) == 0:
		lines = append(lines, dimStyle.Render("no "+m.noun+" available"))
	case len(m.matches) == 0:
		lines = append(lines, dimStyle.Render("no matching "+m.noun))
	default:
		end := min(len(m.matches), m.offset+visible)
		for index := m.offset; index < end; index++ {
			name := ansi.Truncate(m.matches[index].action.Name, max(1, width-2), "…")
			if description := m.matches[index].action.Description; description != "" {
				available := max(1, width-lipgloss.Width(name)-4)
				name += "  " + ansi.Truncate(description, available, "…")
			}
			label := "  " + name
			if index == m.cursor {
				label = "> " + name
				lines = append(lines, selectorSelectedStyle.Copy().Width(width).Render(label))
				continue
			}
			lines = append(lines, lipgloss.NewStyle().Width(width).Render(label))
		}
	}
	return lines
}

func (m selectorModel) footer(width int) string {
	position := "0/0"
	if len(m.matches) > 0 {
		position = fmt.Sprintf("%d/%d", m.cursor+1, len(m.matches))
	}
	help := "up/down move  enter select  esc close"
	if width < 44 {
		help = "enter select  esc close"
	}
	return dimStyle.Render(truncate(position+"  "+help, width))
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
			targets[index] = action.Search
			if targets[index] == "" {
				targets[index] = action.Name + " " + action.ID
			}
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
	if m.compact() {
		return max(1, m.height-3)
	}
	return max(1, m.height-8)
}

func (m *selectorModel) resizeInput() {
	m.input.Width = max(1, m.contentWidth()-lipgloss.Width(m.input.Prompt))
}

func (m selectorModel) compact() bool { return m.height < 12 || m.width < 28 }

func (m selectorModel) contentWidth() int {
	if m.compact() {
		return max(1, m.width)
	}
	return max(1, min(60, m.width-8))
}
