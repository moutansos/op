package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/moutansos/op/internal/domain"
)

const (
	minimumWidth  = 44
	minimumHeight = 14
	narrowWidth   = 72
	wideWidth     = 110
)

type section int

const (
	projectsSection section = iota
	statsSection
	tmuxSection
	sectionCount
)

type overlay int

const (
	noOverlay overlay = iota
	openersOverlay
	createOverlay
	cloneOverlay
	worktreeOverlay
)

// Model is the root Bubble Tea dashboard model.
type Model struct {
	ctx     context.Context
	cancel  context.CancelFunc
	service domain.Service
	options Options

	projects list.Model
	openers  []ProjectOpener
	inputs   []textinput.Model

	width             int
	height            int
	section           section
	overlay           overlay
	openerIndex       int
	inputIndex        int
	worktreeProjectID string

	operation string
	status    string
	statusErr bool

	projectsRefreshing bool
	tmuxRefreshing     bool
	statsRefreshing    bool
	projectsPending    bool
	tmuxPending        bool
	projectsErr        error
	tmuxErr            error
	statsErr           error

	projectFilterGeneration     uint64
	projectRefiltering          bool
	projectSelectionID          string
	projectSelectionRequired    bool
	projectSelectionUnavailable bool

	tmux      domain.TmuxSnapshot
	haveTmux  bool
	stats     domain.StatsSnapshot
	haveStats bool
}

// NewModel builds a dashboard model without performing I/O.
func NewModel(ctx context.Context, service domain.Service, options Options) Model {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(ctx)
	options = options.withDefaults()
	delegate := list.NewDefaultDelegate()
	delegate.SetSpacing(0)
	projects := list.New(nil, delegate, 40, 12)
	projects.Title = "Projects"
	projects.SetShowTitle(false)
	projects.SetShowHelp(false)
	projects.SetShowPagination(false)
	projects.SetStatusBarItemName("project", "projects")
	projects.DisableQuitKeybindings()

	return Model{
		ctx:                ctx,
		cancel:             cancel,
		service:            service,
		options:            options,
		projects:           projects,
		openers:            options.projectOpeners(),
		projectsRefreshing: true,
		tmuxRefreshing:     true,
		statsRefreshing:    true,
		status:             "Loading dashboard snapshots...",
	}
}

// Init focuses project filtering and starts the independent refresh loops.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		func() tea.Msg { return tea.FocusMsg{} },
		m.loadProjectsCmd(), m.loadTmuxCmd(), m.loadStatsCmd(),
		m.projectTickCmd(), m.tmuxTickCmd(), m.statsTickCmd(),
	)
}

// Update transforms dashboard state and schedules all I/O as tea.Cmd values.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeChildren()
		return m, nil

	case tea.FocusMsg:
		if m.overlay != noOverlay {
			return m, nil
		}
		m.section = projectsSection
		if m.projects.FilterState() == list.Unfiltered {
			m.projects.SetFilterText("")
		}
		m.projects.SetFilterState(list.Filtering)
		return m, textinput.Blink

	case projectsLoadedMsg:
		m.projectsRefreshing = false
		if msg.err != nil {
			m.projectsErr = msg.err
			m.setError("Project refresh failed", msg.err)
			return m, m.startPendingProjectsRefresh()
		}
		m.projectsErr = nil
		selectedID, selectionRequired := m.projectSelection()
		if m.projectRefiltering {
			selectedID = m.projectSelectionID
			selectionRequired = m.projectSelectionRequired
		}
		items := make([]list.Item, len(msg.projects))
		for index, project := range msg.projects {
			items[index] = projectItem{project: project}
		}
		cmd := m.projects.SetItems(items)
		if m.projects.FilterState() == list.Unfiltered {
			m.finishProjectSelection(selectedID, selectionRequired)
			m.projectRefiltering = false
		} else {
			m.projectFilterGeneration++
			m.projectRefiltering = true
			m.projectSelectionID = selectedID
			m.projectSelectionRequired = selectionRequired
			cmd = wrapProjectListCmd(cmd, m.projectFilterGeneration)
		}
		if m.status == "Loading dashboard snapshots..." {
			m.setStatus(fmt.Sprintf("Loaded %d projects", len(items)))
		}
		return m, tea.Batch(cmd, m.startPendingProjectsRefresh())

	case projectFilterMatchesMsg:
		if msg.generation != m.projectFilterGeneration {
			return m, nil
		}
		m.projects, _ = m.projects.Update(msg.matches)
		if m.projectRefiltering {
			m.finishProjectSelection(m.projectSelectionID, m.projectSelectionRequired)
			m.projectRefiltering = false
		}
		return m, nil

	case list.FilterMatchesMsg:
		m.projects, _ = m.projects.Update(msg)
		return m, nil

	case tmuxLoadedMsg:
		m.tmuxRefreshing = false
		if msg.err != nil {
			m.tmuxErr = msg.err
			m.setError("Tmux refresh failed", msg.err)
			return m, m.startPendingTmuxRefresh()
		}
		m.tmux, m.haveTmux, m.tmuxErr = msg.snapshot, true, nil
		return m, m.startPendingTmuxRefresh()

	case statsLoadedMsg:
		m.statsRefreshing = false
		if msg.err != nil {
			m.statsErr = msg.err
			m.setError("Statistics refresh failed", msg.err)
			return m, nil
		}
		m.stats, m.haveStats, m.statsErr = msg.snapshot, true, nil
		return m, nil

	case projectTickMsg:
		cmds := []tea.Cmd{m.projectTickCmd()}
		if !m.projectsRefreshing {
			m.projectsRefreshing = true
			cmds = append(cmds, m.loadProjectsCmd())
		}
		return m, tea.Batch(cmds...)

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

	case openFinishedMsg:
		m.operation = ""
		if msg.err != nil {
			m.setError("Open failed", msg.err)
			return m, nil
		}
		if msg.result.Mode == domain.ProjectOpenModeGUI {
			m.setStatus(fmt.Sprintf("Opened %s with %s", msg.result.Project.Name, msg.result.Profile))
		} else {
			verb := "Opened"
			if msg.result.Reused {
				verb = "Selected"
			}
			m.setStatus(fmt.Sprintf("%s %s in window %s", verb, msg.result.Project.Name, msg.result.Window.Name))
		}
		return m, m.refreshAfterMutation(false)

	case actionFinishedMsg:
		m.operation = ""
		if msg.err != nil {
			m.setError("Action failed", msg.err)
			return m, nil
		}
		m.setStatus(fmt.Sprintf("Started %s for %s", msg.result.Action, msg.result.Project.Name))
		return m, m.refreshAfterMutation(true)

	case createFinishedMsg:
		m.operation = ""
		if msg.err != nil {
			m.setError("Create failed", msg.err)
			return m, nil
		}
		m.setStatus("Created " + msg.result.Project.Name)
		return m, m.refreshAfterMutation(true)

	case cloneFinishedMsg:
		m.operation = ""
		if msg.err != nil {
			m.setError("Clone failed", msg.err)
			return m, nil
		}
		m.setStatus("Cloned " + msg.result.Project.Name)
		return m, m.refreshAfterMutation(true)

	case worktreeFinishedMsg:
		m.operation = ""
		if msg.err != nil {
			m.setError("Worktree failed", msg.err)
			return m, nil
		}
		m.setStatus("Created worktree " + msg.result.Project.Name)
		return m, m.refreshAfterMutation(true)
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m.updateProjects(msg)
	}

	if key.String() == "ctrl+c" {
		return m.quit()
	}
	if m.overlay == createOverlay || m.overlay == cloneOverlay || m.overlay == worktreeOverlay {
		return m.updateForm(key)
	}
	if m.overlay == openersOverlay {
		return m.updateOpeners(key)
	}
	if m.overlay == noOverlay && m.section == projectsSection && m.projects.SettingFilter() {
		if key.String() == "enter" {
			m.projectFilterGeneration++
			m.projects.SetFilterText(m.projects.FilterValue())
			return m.startOpen()
		}
		return m.updateProjects(key)
	}

	switch key.String() {
	case "q":
		return m.quit()
	case "tab":
		m.section = (m.section + 1) % sectionCount
		return m, nil
	case "shift+tab":
		m.section = (m.section + sectionCount - 1) % sectionCount
		return m, nil
	case "1":
		m.section = projectsSection
		return m, nil
	case "2":
		m.section = statsSection
		return m, nil
	case "3":
		m.section = tmuxSection
		return m, nil
	case "n":
		return m.openForm(createOverlay)
	case "c":
		return m.openForm(cloneOverlay)
	case "a":
		if m.selectedProject() == nil {
			m.setError("Open unavailable", errors.New("select a project first"))
			return m, nil
		}
		m.overlay, m.openerIndex = openersOverlay, 0
		return m, nil
	case "w":
		project := m.selectedProject()
		if project == nil {
			m.setError("Worktree unavailable", errors.New("select a project first"))
			return m, nil
		}
		m.worktreeProjectID = project.ID
		return m.openForm(worktreeOverlay)
	case "enter":
		if m.section != projectsSection {
			return m, nil
		}
		return m.startOpen()
	case "r":
		return m, m.refreshAll()
	}

	if m.section == projectsSection {
		return m.updateProjects(key)
	}
	return m, nil
}

func (m Model) quit() (tea.Model, tea.Cmd) {
	m.cancel()
	return m, tea.Quit
}

func (m Model) startOpen() (tea.Model, tea.Cmd) {
	project := m.selectedProject()
	if project == nil {
		m.setError("Open unavailable", errors.New("select a project first"))
		return m, nil
	}
	if m.operation != "" {
		m.setError("Operation busy", errors.New(m.operation+" is still running"))
		return m, nil
	}
	projectID := project.ID
	m.projectFilterGeneration++
	m.projects.ResetFilter()
	m.finishProjectSelection(projectID, true)
	m.operation = "open"
	m.setStatus("Opening " + project.Name + "...")
	return m, m.openProjectCmd(projectID, m.options.DefaultProfile)
}

func (m Model) updateOpeners(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.overlay = noOverlay
	case "q":
		return m.quit()
	case "up", "k":
		m.openerIndex = (m.openerIndex + len(m.openers) - 1) % len(m.openers)
	case "down", "j":
		m.openerIndex = (m.openerIndex + 1) % len(m.openers)
	case "enter":
		project := m.selectedProject()
		if project == nil || len(m.openers) == 0 {
			m.overlay = noOverlay
			m.setError("Open unavailable", errors.New("select a project first"))
			return m, nil
		}
		if m.operation != "" {
			m.setError("Operation busy", errors.New(m.operation+" is still running"))
			return m, nil
		}
		opener := m.openers[m.openerIndex]
		m.overlay = noOverlay
		m.operation = "open"
		m.setStatus("Opening " + project.Name + " with " + opener.Name + "...")
		return m, m.openProjectCmd(project.ID, opener.ID)
	}
	return m, nil
}

func (m Model) openForm(kind overlay) (tea.Model, tea.Cmd) {
	if m.operation != "" {
		m.setError("Operation busy", errors.New(m.operation+" is still running"))
		return m, nil
	}
	m.overlay, m.inputIndex = kind, 0
	first := textinput.New()
	first.CharLimit = 512
	first.Width = max(20, m.width-16)
	if kind == createOverlay {
		first.Prompt = "Name: "
		first.Placeholder = "new-project"
		m.inputs = []textinput.Model{first}
	} else if kind == cloneOverlay {
		first.Prompt = "URL: "
		first.Placeholder = "https://host/owner/repository.git"
		directory := textinput.New()
		directory.Prompt = "Directory: "
		directory.Placeholder = "optional destination name"
		directory.CharLimit = 255
		directory.Width = max(20, m.width-16)
		m.inputs = []textinput.Model{first, directory}
	} else {
		first.Prompt = "Branch: "
		first.Placeholder = "feature/my-change"
		directory := textinput.New()
		directory.Prompt = "Directory: "
		directory.Placeholder = "optional sibling directory"
		directory.CharLimit = 255
		directory.Width = max(20, m.width-16)
		m.inputs = []textinput.Model{first, directory}
	}
	cmd := m.inputs[0].Focus()
	return m, cmd
}

func (m Model) updateForm(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch key.String() {
	case "esc":
		m.closeForm()
		return m, nil
	case "tab", "shift+tab", "up", "down":
		m.inputs[m.inputIndex].Blur()
		if key.String() == "shift+tab" || key.String() == "up" {
			m.inputIndex = (m.inputIndex + len(m.inputs) - 1) % len(m.inputs)
		} else {
			m.inputIndex = (m.inputIndex + 1) % len(m.inputs)
		}
		return m, m.inputs[m.inputIndex].Focus()
	case "enter":
		value := strings.TrimSpace(m.inputs[0].Value())
		if value == "" {
			m.setError("Invalid form", errors.New("the first field is required"))
			return m, nil
		}
		kind := m.overlay
		projectID := m.worktreeProjectID
		directory := ""
		if len(m.inputs) > 1 {
			directory = strings.TrimSpace(m.inputs[1].Value())
		}
		m.closeForm()
		if kind == createOverlay {
			m.operation = "create"
			m.setStatus("Creating " + value + "...")
			return m, m.createProjectCmd(value)
		}
		if kind == worktreeOverlay {
			m.operation = "worktree"
			m.setStatus("Creating worktree for " + value + "...")
			return m, m.createWorktreeCmd(projectID, value, directory)
		}
		m.operation = "clone"
		m.setStatus("Cloning repository...")
		return m, m.cloneProjectCmd(value, directory)
	}

	var cmd tea.Cmd
	m.inputs[m.inputIndex], cmd = m.inputs[m.inputIndex].Update(key)
	return m, cmd
}

func (m *Model) closeForm() {
	for index := range m.inputs {
		m.inputs[index].Blur()
	}
	m.inputs = nil
	m.inputIndex = 0
	m.overlay = noOverlay
	m.worktreeProjectID = ""
}

func (m *Model) resizeChildren() {
	width := max(20, m.width-4)
	height := max(4, m.height-9)
	if m.width >= wideWidth {
		width = max(20, (m.width-1)/2-4)
		height = max(4, m.height-9)
	} else if m.width >= narrowWidth {
		height = max(4, m.height/2-5)
	}
	m.projects.SetSize(width, height)
	for index := range m.inputs {
		m.inputs[index].Width = max(20, m.width-16)
	}
}

func (m *Model) setStatus(status string) {
	m.status, m.statusErr = status, false
}

func (m *Model) setError(prefix string, err error) {
	m.status, m.statusErr = prefix+": "+err.Error(), true
}

func (m Model) selectedProject() *domain.Project {
	if m.projectRefiltering || m.projectSelectionUnavailable {
		return nil
	}
	item, ok := m.projects.SelectedItem().(projectItem)
	if !ok {
		return nil
	}
	project := item.project
	return &project
}

func (m Model) selectedProjectID() string {
	project := m.selectedProject()
	if project == nil {
		return ""
	}
	return project.ID
}

func (m Model) projectSelection() (string, bool) {
	if m.projectSelectionUnavailable {
		return "", true
	}
	project := m.selectedProject()
	if project == nil {
		return "", false
	}
	return project.ID, true
}

func (m *Model) finishProjectSelection(projectID string, required bool) {
	m.projectSelectionID = ""
	m.projectSelectionRequired = false
	if !required {
		m.projectSelectionUnavailable = false
		m.projects.ResetSelected()
		return
	}
	for index, item := range m.projects.VisibleItems() {
		project, ok := item.(projectItem)
		if ok && project.project.ID == projectID {
			m.projects.Select(index)
			m.projectSelectionUnavailable = false
			return
		}
	}
	m.projects.ResetSelected()
	m.projectSelectionUnavailable = true
}

func (m Model) updateProjects(msg tea.Msg) (tea.Model, tea.Cmd) {
	filterValue := m.projects.FilterValue()
	filterState := m.projects.FilterState()
	var cmd tea.Cmd
	m.projects, cmd = m.projects.Update(msg)
	if m.projects.FilterValue() != filterValue {
		m.projectFilterGeneration++
	}
	if m.projectRefiltering && filterState != list.Unfiltered && m.projects.FilterState() == list.Unfiltered {
		m.projectFilterGeneration++
		m.finishProjectSelection(m.projectSelectionID, m.projectSelectionRequired)
		m.projectRefiltering = false
	}
	if key, ok := msg.(tea.KeyMsg); ok && filterState != list.Filtering && m.projectNavigationKey(key) && len(m.projects.VisibleItems()) > 0 {
		m.projectSelectionUnavailable = false
	}
	return m, wrapProjectListCmd(cmd, m.projectFilterGeneration)
}

func (m Model) projectNavigationKey(msg tea.KeyMsg) bool {
	value := msg.String()
	for _, binding := range [][]string{
		m.projects.KeyMap.CursorUp.Keys(),
		m.projects.KeyMap.CursorDown.Keys(),
		m.projects.KeyMap.NextPage.Keys(),
		m.projects.KeyMap.PrevPage.Keys(),
		m.projects.KeyMap.GoToStart.Keys(),
		m.projects.KeyMap.GoToEnd.Keys(),
	} {
		for _, candidate := range binding {
			if candidate == value {
				return true
			}
		}
	}
	return false
}

func wrapProjectListCmd(cmd tea.Cmd, generation uint64) tea.Cmd {
	if cmd == nil {
		return nil
	}
	return func() tea.Msg {
		msg := cmd()
		switch msg := msg.(type) {
		case list.FilterMatchesMsg:
			return projectFilterMatchesMsg{generation: generation, matches: msg}
		case tea.BatchMsg:
			commands := make(tea.BatchMsg, len(msg))
			for index, command := range msg {
				commands[index] = wrapProjectListCmd(command, generation)
			}
			return commands
		default:
			return msg
		}
	}
}

func (m *Model) refreshAll() tea.Cmd {
	var commands []tea.Cmd
	if !m.projectsRefreshing {
		m.projectsRefreshing = true
		commands = append(commands, m.loadProjectsCmd())
	}
	if !m.tmuxRefreshing {
		m.tmuxRefreshing = true
		commands = append(commands, m.loadTmuxCmd())
	}
	if !m.statsRefreshing {
		m.statsRefreshing = true
		commands = append(commands, m.loadStatsCmd())
	}
	return tea.Batch(commands...)
}

func (m *Model) refreshAfterMutation(projects bool) tea.Cmd {
	var commands []tea.Cmd
	if projects {
		if m.projectsRefreshing {
			m.projectsPending = true
		} else {
			m.projectsRefreshing = true
			commands = append(commands, m.loadProjectsCmd())
		}
	}
	if m.tmuxRefreshing {
		m.tmuxPending = true
	} else {
		m.tmuxRefreshing = true
		commands = append(commands, m.loadTmuxCmd())
	}
	return tea.Batch(commands...)
}

func (m *Model) startPendingProjectsRefresh() tea.Cmd {
	if !m.projectsPending {
		return nil
	}
	m.projectsPending = false
	m.projectsRefreshing = true
	return m.loadProjectsCmd()
}

func (m *Model) startPendingTmuxRefresh() tea.Cmd {
	if !m.tmuxPending {
		return nil
	}
	m.tmuxPending = false
	m.tmuxRefreshing = true
	return m.loadTmuxCmd()
}
