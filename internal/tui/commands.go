package tui

import (
	"context"
	"io"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/moutansos/op/internal/domain"
)

type projectsLoadedMsg struct {
	projects []domain.Project
	err      error
}

type projectFilterMatchesMsg struct {
	generation uint64
	matches    list.FilterMatchesMsg
}

type tmuxLoadedMsg struct {
	snapshot domain.TmuxSnapshot
	err      error
}

type statsLoadedMsg struct {
	snapshot domain.StatsSnapshot
	err      error
}

type openFinishedMsg struct {
	result domain.OpenProjectResult
	err    error
}

type selectPaneFinishedMsg struct {
	result domain.SelectPaneResult
	err    error
}

type actionFinishedMsg struct {
	result domain.RunProjectActionResult
	err    error
}

type createFinishedMsg struct {
	result domain.CreateProjectResult
	err    error
}

type cloneFinishedMsg struct {
	result domain.CloneResult
	err    error
}

type worktreeFinishedMsg struct {
	result domain.CreateWorktreeResult
	err    error
}

type projectTickMsg struct{}
type tmuxTickMsg struct{}
type statsTickMsg struct{}

func (m Model) loadProjectsCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, m.options.RefreshTimeout)
		defer cancel()
		projects, err := m.service.ListProjects(ctx)
		return projectsLoadedMsg{projects: projects, err: err}
	}
}

func (m Model) loadTmuxCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, m.options.RefreshTimeout)
		defer cancel()
		snapshot, err := m.service.GetTmuxSnapshot(ctx)
		return tmuxLoadedMsg{snapshot: snapshot, err: err}
	}
}

func (m Model) loadStatsCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, m.options.RefreshTimeout)
		defer cancel()
		snapshot, err := m.service.GetStatsSnapshot(ctx)
		return statsLoadedMsg{snapshot: snapshot, err: err}
	}
}

func (m Model) selectPaneCmd(paneID string) tea.Cmd {
	request := domain.SelectPaneRequest{PaneID: paneID}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, m.options.OperationTimeout)
		defer cancel()
		result, err := m.service.SelectPane(ctx, request)
		return selectPaneFinishedMsg{result: result, err: err}
	}
}

func (m Model) openProjectCmd(projectID, profile string) tea.Cmd {
	request := domain.OpenProjectRequest{ProjectID: projectID, Profile: profile}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, m.options.OperationTimeout)
		defer cancel()
		result, err := m.service.OpenProject(ctx, request)
		return openFinishedMsg{result: result, err: err}
	}
}

func (m Model) runActionCmd(projectID, action string) tea.Cmd {
	request := domain.RunProjectActionRequest{ProjectID: projectID, Action: action}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, m.options.OperationTimeout)
		defer cancel()
		result, err := m.service.RunProjectAction(ctx, request)
		return actionFinishedMsg{result: result, err: err}
	}
}

// serviceExecCommand lets Bubble Tea release and restore the terminal around
// service actions which own stdin and stdout, such as editors and shells.
type serviceExecCommand struct {
	ctx     context.Context
	service domain.Service
	request domain.RunProjectActionRequest
	result  domain.RunProjectActionResult
	stdin   io.Reader
	stdout  io.Writer
	stderr  io.Writer
}

var _ tea.ExecCommand = (*serviceExecCommand)(nil)

func (c *serviceExecCommand) Run() error {
	result, err := c.service.RunProjectAction(c.ctx, c.request)
	c.result = result
	return err
}

func (c *serviceExecCommand) SetStdin(stdin io.Reader)   { c.stdin = stdin }
func (c *serviceExecCommand) SetStdout(stdout io.Writer) { c.stdout = stdout }
func (c *serviceExecCommand) SetStderr(stderr io.Writer) { c.stderr = stderr }

func (c *serviceExecCommand) message(err error) tea.Msg {
	return actionFinishedMsg{result: c.result, err: err}
}

func (m Model) runTerminalActionCmd(projectID, action string) tea.Cmd {
	command := &serviceExecCommand{
		ctx:     m.ctx,
		service: m.service,
		request: domain.RunProjectActionRequest{ProjectID: projectID, Action: action},
	}
	return tea.Exec(command, command.message)
}

func (m Model) createProjectCmd(name string) tea.Cmd {
	request := domain.CreateProjectRequest{
		Name:         name,
		OpenOnFinish: true,
		Profile:      m.options.DefaultProfile,
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, m.options.OperationTimeout)
		defer cancel()
		result, err := m.service.CreateProject(ctx, request)
		return createFinishedMsg{result: result, err: err}
	}
}

func (m Model) cloneProjectCmd(url, directory string) tea.Cmd {
	request := domain.CloneRequest{
		URL:          url,
		Directory:    directory,
		OpenOnFinish: true,
		Profile:      m.options.DefaultProfile,
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, m.options.OperationTimeout)
		defer cancel()
		result, err := m.service.CloneProject(ctx, request)
		return cloneFinishedMsg{result: result, err: err}
	}
}

func (m Model) createWorktreeCmd(projectID, branch, directory string) tea.Cmd {
	request := domain.CreateWorktreeRequest{
		ProjectID:    projectID,
		Branch:       branch,
		Directory:    directory,
		OpenOnFinish: true,
		Profile:      m.options.DefaultProfile,
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, m.options.OperationTimeout)
		defer cancel()
		result, err := m.service.CreateWorktree(ctx, request)
		return worktreeFinishedMsg{result: result, err: err}
	}
}

func (m Model) projectTickCmd() tea.Cmd {
	return m.tickCmd(m.options.ProjectRefreshInterval, projectTickMsg{})
}

func (m Model) tmuxTickCmd() tea.Cmd {
	return m.tickCmd(m.options.TmuxRefreshInterval, tmuxTickMsg{})
}

func (m Model) statsTickCmd() tea.Cmd {
	return m.tickCmd(m.options.StatsRefreshInterval, statsTickMsg{})
}

func (m Model) tickCmd(after time.Duration, msg tea.Msg) tea.Cmd {
	return func() tea.Msg {
		timer := time.NewTimer(after)
		defer timer.Stop()
		select {
		case <-m.ctx.Done():
			return nil
		case <-timer.C:
			return msg
		}
	}
}
