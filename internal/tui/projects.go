package tui

import (
	"path/filepath"
	"strings"

	"github.com/moutansos/op/internal/domain"
)

type projectItem struct {
	project domain.Project
}

func (i projectItem) Title() string { return i.project.Name }

func (i projectItem) Description() string {
	description := i.project.Path
	if i.project.GitState != "" && i.project.GitState != domain.GitStateUnknown {
		description += "  [" + string(i.project.GitState) + "]"
	}
	return description
}

func (i projectItem) FilterValue() string {
	values := []string{i.project.Name, filepath.Base(i.project.Path), i.project.Path}
	values = append(values, i.project.Tags...)
	return strings.Join(values, " ")
}
