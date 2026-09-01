package tui

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/moutansos/op/internal/domain"
)

type projectItem struct {
	project domain.Project
}

func (i projectItem) Title() string { return i.project.Name }

func (i projectItem) Description() string {
	return projectMetadata(i.project)
}

func (i projectItem) FilterValue() string {
	values := []string{i.project.Name, filepath.Base(i.project.Path), i.project.Path}
	values = append(values, i.project.Tags...)
	return strings.Join(values, " ")
}

type projectDelegate struct{}

func (projectDelegate) Height() int                         { return 1 }
func (projectDelegate) Spacing() int                        { return 0 }
func (projectDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

func (projectDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	project, ok := item.(projectItem)
	if !ok || m.Width() <= 0 {
		return
	}
	prefix := "  "
	nameStyle := lipgloss.NewStyle()
	if index == m.Index() {
		nameStyle = nameStyle.Foreground(selectorSelectedText).Background(accentColor).Bold(true)
		prefix = "> "
	}
	available := max(1, m.Width()-lipgloss.Width(prefix))
	name := ansi.Truncate(project.project.Name, available, "…")
	line := prefix + nameStyle.Render(name)
	if metadata := projectMetadata(project.project); metadata != "" {
		available -= lipgloss.Width(name) + 2
		if available > 0 {
			line += "  " + ansi.Truncate(metadata, available, "…")
		}
	}
	fmt.Fprint(w, line)
}

func projectMetadata(project domain.Project) string {
	parts := make([]string, 0, 2)
	if project.Branch != "" {
		parts = append(parts, projectBranchStyle.Render(project.Branch))
	}
	if project.GitState != "" && project.GitState != domain.GitStateUnknown {
		parts = append(parts, projectGitStateStyle(project.GitState).Render(string(project.GitState)))
	}
	return strings.Join(parts, "  ")
}

func projectGitStateStyle(state domain.GitState) lipgloss.Style {
	switch state {
	case domain.GitStateClean:
		return projectCleanStyle
	case domain.GitStateDirty:
		return projectDirtyStyle
	default:
		return dimStyle
	}
}
