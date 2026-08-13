package tui

import (
	"bytes"
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/moutansos/op/internal/domain"
)

func updateSelector(model selectorModel, msg tea.Msg) selectorModel {
	updated, _ := model.Update(msg)
	return updated.(selectorModel)
}

func TestProjectSelectorSearchesNamePathAndTags(t *testing.T) {
	projects := []domain.Project{
		{ID: "one", Name: "api", Path: "/repos/api", Tags: []string{"backend"}},
		{ID: "two", Name: "dotfiles", Path: "/home/me/.config/nvim", Tags: []string{"config"}},
	}
	items := make([]Action, len(projects))
	for index, project := range projects {
		items[index] = Action{Name: project.Name, ID: project.ID, Description: project.Path, Search: project.Name + " " + project.Path + " " + strings.Join(project.Tags, " ")}
	}
	model := newSelectorModelFor("open project", "projects", items)
	model = updateSelector(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("config")})
	if len(model.matches) != 1 || model.matches[0].action.ID != "two" {
		t.Fatalf("project matches = %#v", model.matches)
	}
	view := model.View()
	for _, expected := range []string{"2 projects", "dotfiles", "/home/me/.config/nvim"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("project selector view does not contain %q:\n%s", expected, view)
		}
	}
}

func TestSelectorFiltersImmediatelyWithBubblesFuzzyRanking(t *testing.T) {
	model := newSelectorModel("Actions", []Action{
		{Name: "Shell", ID: "shell"},
		{Name: "Open Code", ID: "open-code"},
		{Name: "Neovim", ID: "nvim"},
	})
	model = updateSelector(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("ocd")})

	if model.input.Value() != "ocd" || len(model.matches) != 1 || model.matches[0].action.ID != "open-code" {
		t.Fatalf("fuzzy matches = %#v for query %q", model.matches, model.input.Value())
	}
	if len(model.matches[0].indexes) != 3 {
		t.Fatalf("matched indexes = %#v, want three fuzzy positions", model.matches[0].indexes)
	}
}

func TestSelectorNavigationSelectionAndScrolling(t *testing.T) {
	actions := make([]Action, 8)
	for index := range actions {
		actions[index] = Action{Name: "Action " + string(rune('A'+index)), ID: string(rune('a' + index))}
	}
	model := newSelectorModel("Actions", actions)
	model = updateSelector(model, tea.WindowSizeMsg{Width: 30, Height: 7})
	for range 5 {
		model = updateSelector(model, tea.KeyMsg{Type: tea.KeyCtrlN})
	}
	if model.cursor != 5 || model.offset == 0 {
		t.Fatalf("cursor=%d offset=%d, want scrolled selection", model.cursor, model.offset)
	}
	model = updateSelector(model, tea.KeyMsg{Type: tea.KeyCtrlP})
	if model.cursor != 4 {
		t.Fatalf("ctrl-p cursor = %d, want 4", model.cursor)
	}
	model = updateSelector(model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.selected == nil || model.selected.ID != actions[4].ID {
		t.Fatalf("selected = %#v, want %#v", model.selected, actions[4])
	}
}

func TestSelectorNoMatchesEmptyAndSmallTerminal(t *testing.T) {
	model := newSelectorModel("Project actions", nil)
	if view := model.View(); !strings.Contains(view, "no actions available") {
		t.Fatalf("empty view = %q", view)
	}

	model = newSelectorModel("Project actions", []Action{{Name: "Neovim", ID: "nvim"}})
	model = updateSelector(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("zzz")})
	if len(model.matches) != 0 || !strings.Contains(model.View(), "no matching actions") {
		t.Fatalf("no-match state: matches=%#v view=%q", model.matches, model.View())
	}
	model = updateSelector(model, tea.KeyMsg{Type: tea.KeyEnter})
	if model.selected != nil {
		t.Fatalf("enter selected from no matches: %#v", model.selected)
	}

	model = updateSelector(model, tea.WindowSizeMsg{Width: 8, Height: 1})
	if lines := strings.Count(model.View(), "\n") + 1; lines > 1 {
		t.Fatalf("small terminal rendered %d lines: %q", lines, model.View())
	}
}

func TestSelectorViewUsesPanelSelectionAndContextualFooter(t *testing.T) {
	model := newSelectorModel("actions for alpha", []Action{
		{Name: "nvim", ID: "nvim"},
		{Name: "cd-here", ID: "shell"},
		{Name: "vs-code", ID: "code"},
	})
	model = updateSelector(model, tea.WindowSizeMsg{Width: 80, Height: 24})
	view := model.View()
	for _, expected := range []string{"actions for alpha", "3 actions", "filter", "nvim", "cd-here", "vs-code", "1/3", "enter select", "esc close"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("selector view does not contain %q:\n%s", expected, view)
		}
	}
	if !strings.Contains(view, "> nvim") {
		t.Fatalf("selected row was not marked:\n%s", view)
	}
}

func TestSelectorEscapeAndCtrlCCancel(t *testing.T) {
	for _, key := range []tea.KeyMsg{{Type: tea.KeyEsc}, {Type: tea.KeyCtrlC}} {
		model := newSelectorModel("Actions", []Action{{Name: "Shell", ID: "shell"}})
		updated, cmd := model.Update(key)
		model = updated.(selectorModel)
		if !model.canceled || model.selected != nil || cmd == nil {
			t.Fatalf("key %q cancellation state = %+v, cmd nil=%v", key.String(), model, cmd == nil)
		}
		if _, ok := cmd().(tea.QuitMsg); !ok {
			t.Fatalf("key %q did not return tea.Quit", key.String())
		}
	}
}

func TestSelectActionUsesProvidedStreamsAndRestoresAlternateScreen(t *testing.T) {
	var output bytes.Buffer
	selected, err := SelectAction(context.Background(), "Actions", []Action{{Name: "Shell", ID: "shell"}}, strings.NewReader("\x1b"), &output)
	if err != nil {
		t.Fatalf("SelectAction() error = %v", err)
	}
	if selected != nil {
		t.Fatalf("canceled selection = %#v", selected)
	}
	rendered := output.String()
	if !strings.Contains(rendered, "\x1b[?1049h") || !strings.Contains(rendered, "\x1b[?1049l") {
		t.Fatalf("alternate screen was not entered and restored: %q", rendered)
	}
}
