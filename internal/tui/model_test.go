package tui

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/moutansos/op/internal/domain"
)

type fakeService struct {
	listProjectsCalls int
	openCalls         int
	createCalls       int
	cloneCalls        int
	worktreeCalls     int
	actionCalls       int
	tmuxCalls         int
	statsCalls        int

	projects    []domain.Project
	openReq     domain.OpenProjectRequest
	createReq   domain.CreateProjectRequest
	cloneReq    domain.CloneRequest
	actionReq   domain.RunProjectActionRequest
	worktreeReq domain.CreateWorktreeRequest

	listErr     error
	openErr     error
	actionErr   error
	worktreeErr error

	listDeadline time.Time
}

func (f *fakeService) ListProjects(ctx context.Context) ([]domain.Project, error) {
	f.listProjectsCalls++
	f.listDeadline, _ = ctx.Deadline()
	return f.projects, f.listErr
}

func (f *fakeService) CreateProject(_ context.Context, request domain.CreateProjectRequest) (domain.CreateProjectResult, error) {
	f.createCalls++
	f.createReq = request
	return domain.CreateProjectResult{Project: domain.Project{ID: request.Name, Name: request.Name}}, nil
}

func (f *fakeService) CloneProject(_ context.Context, request domain.CloneRequest) (domain.CloneResult, error) {
	f.cloneCalls++
	f.cloneReq = request
	return domain.CloneResult{Project: domain.Project{ID: "clone", Name: "clone"}}, nil
}

func (f *fakeService) CreateWorktree(_ context.Context, request domain.CreateWorktreeRequest) (domain.CreateWorktreeResult, error) {
	f.worktreeCalls++
	f.worktreeReq = request
	return domain.CreateWorktreeResult{Project: domain.Project{ID: "worktree", Name: request.Branch}}, f.worktreeErr
}

func (f *fakeService) OpenProject(_ context.Context, request domain.OpenProjectRequest) (domain.OpenProjectResult, error) {
	f.openCalls++
	f.openReq = request
	if f.openErr != nil {
		return domain.OpenProjectResult{}, f.openErr
	}
	return domain.OpenProjectResult{
		Project: domain.Project{ID: request.ProjectID, Name: "alpha"},
		Window:  domain.TmuxWindow{Name: "alpha"},
	}, nil
}

func (f *fakeService) RunProjectAction(_ context.Context, request domain.RunProjectActionRequest) (domain.RunProjectActionResult, error) {
	f.actionCalls++
	f.actionReq = request
	return domain.RunProjectActionResult{
		Project: domain.Project{ID: request.ProjectID, Name: "alpha"},
		Action:  request.Action,
		Started: true,
	}, f.actionErr
}

func (f *fakeService) EnsureMainSession(context.Context) (domain.EnsureMainSessionResult, error) {
	return domain.EnsureMainSessionResult{}, nil
}

func (f *fakeService) GetTmuxSnapshot(context.Context) (domain.TmuxSnapshot, error) {
	f.tmuxCalls++
	return domain.TmuxSnapshot{}, nil
}

func (f *fakeService) GetStatsSnapshot(context.Context) (domain.StatsSnapshot, error) {
	f.statsCalls++
	return domain.StatsSnapshot{}, nil
}

func testModel(service *fakeService) Model {
	model := NewModel(context.Background(), service, Options{
		DefaultProfile:         "default-profile",
		ProjectRefreshInterval: time.Hour,
		TmuxRefreshInterval:    time.Hour,
		StatsRefreshInterval:   time.Hour,
		RefreshTimeout:         time.Second,
		OperationTimeout:       time.Second,
	})
	model = updateTestModel(model, tea.WindowSizeMsg{Width: 120, Height: 35})
	return model
}

func updateTestModel(model Model, msg tea.Msg) Model {
	updated, _ := model.Update(msg)
	return updated.(Model)
}

func loadProjectsForTest(model Model, projects ...domain.Project) Model {
	return updateTestModel(model, projectsLoadedMsg{projects: projects})
}

func updateTestModelWithCmd(model Model, msg tea.Msg) (Model, tea.Cmd) {
	updated, cmd := model.Update(msg)
	return updated.(Model), cmd
}

func deliverTestCmd(model Model, cmd tea.Cmd) Model {
	if cmd == nil {
		return model
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, command := range batch {
			model = deliverTestCmd(model, command)
		}
		return model
	}
	if msg == nil {
		return model
	}
	updated, _ := model.Update(msg)
	return updated.(Model)
}

func selectProjectForTest(t *testing.T, model *Model, projectID string) {
	t.Helper()
	for index, item := range model.projects.VisibleItems() {
		if item.(projectItem).project.ID == projectID {
			model.projects.Select(index)
			return
		}
	}
	t.Fatalf("project %q is not visible", projectID)
}

func visibleProjectIDs(model Model) []string {
	ids := make([]string, len(model.projects.VisibleItems()))
	for index, item := range model.projects.VisibleItems() {
		ids[index] = item.(projectItem).project.ID
	}
	return ids
}

func TestFilteringFocusAndResize(t *testing.T) {
	service := &fakeService{}
	model := testModel(service)
	model = loadProjectsForTest(model,
		domain.Project{ID: "one", Name: "api-server", Path: "/repos/api-server"},
		domain.Project{ID: "two", Name: "dotfiles", Path: "/home/me/.config/nvim", Tags: []string{"cfg"}},
	)

	updated, _ := model.Update(tea.FocusMsg{})
	model = updated.(Model)
	if !model.projects.SettingFilter() {
		t.Fatal("window focus did not focus the embedded project filter")
	}
	if got := len(model.projects.VisibleItems()); got != 2 {
		t.Fatalf("visible projects after focus = %d, want 2", got)
	}
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("cfg")})
	model = updated.(Model)
	if model.projects.FilterValue() != "cfg" {
		t.Fatalf("filter value = %q, want cfg", model.projects.FilterValue())
	}

	model.projects.SetFilterText("cfg")
	if got := len(model.projects.VisibleItems()); got != 1 {
		t.Fatalf("visible filtered projects = %d, want 1", got)
	}
	item := model.projects.VisibleItems()[0].(projectItem)
	if item.project.ID != "two" {
		t.Fatalf("filtered project = %q, want two", item.project.ID)
	}

	model.projects.ResetFilter()
	model = updateTestModel(model, tea.KeyMsg{Type: tea.KeyTab})
	if model.section != statsSection {
		t.Fatalf("focused section = %d, want stats", model.section)
	}
	model = updateTestModel(model, tea.WindowSizeMsg{Width: 60, Height: 24})
	if model.projects.Width() != 56 || model.projects.Height() != 15 {
		t.Fatalf("narrow list size = %dx%d, want 56x15", model.projects.Width(), model.projects.Height())
	}
	if view := model.View(); !strings.Contains(view, "1 Projects") || !strings.Contains(view, "System + Processes") {
		t.Fatalf("narrow view did not render tabs and focused system panel:\n%s", view)
	}
}

func TestWindowFocusPreservesAppliedProjectFilter(t *testing.T) {
	model := loadProjectsForTest(testModel(&fakeService{}),
		domain.Project{ID: "one", Name: "api-server", Path: "/repos/api-server"},
		domain.Project{ID: "two", Name: "dotfiles", Path: "/home/me/.config/nvim"},
	)
	model.projects.SetFilterText("api")
	model.section = statsSection

	model = updateTestModel(model, tea.FocusMsg{})
	if model.section != projectsSection {
		t.Fatalf("section after focus = %d, want projects", model.section)
	}
	if !model.projects.SettingFilter() {
		t.Fatal("window focus did not re-enter filter mode")
	}
	if got := model.projects.FilterValue(); got != "api" {
		t.Fatalf("filter value after focus = %q, want api", got)
	}
	if got := visibleProjectIDs(model); !slices.Equal(got, []string{"one"}) {
		t.Fatalf("visible project IDs after focus = %v, want [one]", got)
	}
	model = updateTestModel(model, tea.KeyMsg{Type: tea.KeyEsc})
	if model.projects.SettingFilter() {
		t.Fatal("escape did not leave filter mode")
	}
	model = updateTestModel(model, tea.KeyMsg{Type: tea.KeyTab})
	if model.section != statsSection {
		t.Fatalf("section after escape and tab = %d, want stats", model.section)
	}
}

func TestFilteredRefreshPreservesSelectionThroughReorderAddAndRemove(t *testing.T) {
	service := &fakeService{}
	model := loadProjectsForTest(testModel(service),
		domain.Project{ID: "one", Name: "api-one", Path: "/repos/one"},
		domain.Project{ID: "two", Name: "api-two", Path: "/repos/two"},
		domain.Project{ID: "docs", Name: "docs", Path: "/repos/docs"},
	)
	model.projects.SetFilterText("api")
	selectProjectForTest(t, &model, "two")

	model, filterCmd := updateTestModelWithCmd(model, projectsLoadedMsg{projects: []domain.Project{
		{ID: "three", Name: "api-three", Path: "/repos/three"},
		{ID: "docs", Name: "docs", Path: "/repos/docs"},
		{ID: "two", Name: "api-two", Path: "/repos/two"},
	}})
	if model.selectedProject() != nil {
		t.Fatal("selection remained actionable before refreshed filtering completed")
	}
	model, openCmd := updateTestModelWithCmd(model, tea.KeyMsg{Type: tea.KeyEnter})
	if openCmd != nil || service.openCalls != 0 {
		t.Fatal("enter during refilter scheduled an open")
	}
	model, actionCmd := updateTestModelWithCmd(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if actionCmd != nil || model.overlay == actionsOverlay || service.actionCalls != 0 {
		t.Fatal("action key during refilter targeted the temporary cursor item")
	}

	model = deliverTestCmd(model, filterCmd)
	if got := model.selectedProjectID(); got != "two" {
		t.Fatalf("selected project after refresh = %q, want two", got)
	}
	if got := visibleProjectIDs(model); !sameProjectIDs(got, []string{"three", "two"}) {
		t.Fatalf("visible project IDs = %v, want [three two]", got)
	}

	model, openCmd = updateTestModelWithCmd(model, tea.KeyMsg{Type: tea.KeyEnter})
	if openCmd == nil {
		t.Fatal("enter after refilter did not schedule an open")
	}
	openCmd()
	if service.openReq.ProjectID != "two" {
		t.Fatalf("opened project ID = %q, want two", service.openReq.ProjectID)
	}

	model.operation = ""
	model.actions = []Action{{Name: "GUI", ID: "code"}}
	model.overlay = actionsOverlay
	model, actionCmd = updateTestModelWithCmd(model, tea.KeyMsg{Type: tea.KeyEnter})
	if actionCmd == nil {
		t.Fatal("action after refilter did not schedule a command")
	}
	actionCmd()
	if service.actionReq.ProjectID != "two" {
		t.Fatalf("action project ID = %q, want two", service.actionReq.ProjectID)
	}
}

func TestFilteredRefreshRemovedSelectionStaysUnselected(t *testing.T) {
	service := &fakeService{}
	model := loadProjectsForTest(testModel(service),
		domain.Project{ID: "one", Name: "api-one", Path: "/repos/one"},
		domain.Project{ID: "two", Name: "api-two", Path: "/repos/two"},
	)
	model.projects.SetFilterText("api")
	selectProjectForTest(t, &model, "two")

	model, filterCmd := updateTestModelWithCmd(model, projectsLoadedMsg{projects: []domain.Project{
		{ID: "one", Name: "api-one", Path: "/repos/one"},
		{ID: "three", Name: "api-three", Path: "/repos/three"},
	}})
	model = deliverTestCmd(model, filterCmd)
	if model.selectedProject() != nil || model.selectedProjectID() != "" {
		t.Fatalf("removed selection silently changed to %q", model.selectedProjectID())
	}

	model, cmd := updateTestModelWithCmd(model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil || service.openCalls != 0 {
		t.Fatal("enter targeted another project after the selected project was removed")
	}
	model, cmd = updateTestModelWithCmd(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	if cmd != nil || model.overlay == actionsOverlay || service.actionCalls != 0 {
		t.Fatal("action picker opened for another project after the selected project was removed")
	}
}

func TestFilteredRefreshCompletesWhileProjectsAreHidden(t *testing.T) {
	tests := []struct {
		name    string
		section section
		overlay overlay
	}{
		{name: "unfocused section", section: statsSection, overlay: noOverlay},
		{name: "actions overlay", section: projectsSection, overlay: actionsOverlay},
		{name: "create overlay", section: projectsSection, overlay: createOverlay},
		{name: "clone overlay", section: projectsSection, overlay: cloneOverlay},
		{name: "worktree overlay", section: projectsSection, overlay: worktreeOverlay},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			model := loadProjectsForTest(testModel(&fakeService{}),
				domain.Project{ID: "one", Name: "api-one", Path: "/repos/one"},
				domain.Project{ID: "two", Name: "api-two", Path: "/repos/two"},
			)
			model.projects.SetFilterText("api")
			selectProjectForTest(t, &model, "two")
			model.section = test.section
			model.overlay = test.overlay
			if test.overlay == createOverlay {
				model.inputs = []textinput.Model{textinput.New()}
			}
			if test.overlay == cloneOverlay || test.overlay == worktreeOverlay {
				model.inputs = []textinput.Model{textinput.New(), textinput.New()}
			}

			model, filterCmd := updateTestModelWithCmd(model, projectsLoadedMsg{projects: []domain.Project{
				{ID: "three", Name: "api-three", Path: "/repos/three"},
				{ID: "two", Name: "api-two", Path: "/repos/two"},
			}})
			model, _ = updateTestModelWithCmd(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
			if got := model.projects.FilterValue(); got != "api" {
				t.Fatalf("hidden key input changed project filter to %q", got)
			}
			model = deliverTestCmd(model, filterCmd)
			if got := model.selectedProjectID(); got != "two" {
				t.Fatalf("selected project after hidden refilter = %q, want two", got)
			}
		})
	}
}

func TestRapidFilteredRefreshAndFilterResultsKeepLatestProjects(t *testing.T) {
	model := loadProjectsForTest(testModel(&fakeService{}),
		domain.Project{ID: "one", Name: "api-one", Path: "/repos/one"},
		domain.Project{ID: "two", Name: "api2-two", Path: "/repos/two"},
	)
	model.projects.SetFilterText("api")
	selectProjectForTest(t, &model, "two")

	model, firstRefreshCmd := updateTestModelWithCmd(model, projectsLoadedMsg{projects: []domain.Project{
		{ID: "first", Name: "api-first", Path: "/repos/first"},
		{ID: "two", Name: "api2-two", Path: "/repos/two"},
	}})
	model, secondRefreshCmd := updateTestModelWithCmd(model, projectsLoadedMsg{projects: []domain.Project{
		{ID: "two", Name: "api2-two", Path: "/repos/two"},
		{ID: "second", Name: "api2-second", Path: "/repos/second"},
	}})
	model = deliverTestCmd(model, secondRefreshCmd)
	model = deliverTestCmd(model, firstRefreshCmd)
	if got := visibleProjectIDs(model); !sameProjectIDs(got, []string{"two", "second"}) {
		t.Fatalf("stale refresh replaced latest filtered projects: %v", got)
	}
	if got := model.selectedProjectID(); got != "two" {
		t.Fatalf("selection after successive refreshes = %q, want two", got)
	}

	model.projects.SetFilterState(list.Filtering)
	model, typingCmd := updateTestModelWithCmd(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'2'}})
	model, latestRefreshCmd := updateTestModelWithCmd(model, projectsLoadedMsg{projects: []domain.Project{
		{ID: "latest", Name: "api2-latest", Path: "/repos/latest"},
		{ID: "two", Name: "api2-two", Path: "/repos/two"},
	}})
	model = deliverTestCmd(model, typingCmd)
	if !model.projectRefiltering {
		t.Fatal("stale typing result completed the newer project refilter")
	}
	model = deliverTestCmd(model, latestRefreshCmd)
	if got := visibleProjectIDs(model); !sameProjectIDs(got, []string{"latest", "two"}) {
		t.Fatalf("latest filtered projects = %v, want [latest two]", got)
	}
	if got := model.selectedProjectID(); got != "two" {
		t.Fatalf("selection after rapid filter results = %q, want two", got)
	}
}

func sameProjectIDs(got, want []string) bool {
	got = slices.Clone(got)
	want = slices.Clone(want)
	slices.Sort(got)
	slices.Sort(want)
	return slices.Equal(got, want)
}

func TestEnterSchedulesOpenWithoutIODuringUpdate(t *testing.T) {
	service := &fakeService{}
	model := loadProjectsForTest(testModel(service), domain.Project{ID: "p1", Name: "alpha", Path: "/repos/alpha"})

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if service.openCalls != 0 {
		t.Fatalf("OpenProject called %d times directly in Update", service.openCalls)
	}
	if cmd == nil || model.operation != "open" {
		t.Fatal("enter did not schedule an open operation")
	}
	if model.projects.FilterValue() != "" || model.projects.FilterState() != list.Unfiltered {
		t.Fatalf("project filter was not cleared: value=%q state=%d", model.projects.FilterValue(), model.projects.FilterState())
	}
	view := model.View()
	if !strings.Contains(view, "Opening Project") || !strings.Contains(view, "Opening alpha...") {
		t.Fatalf("opening view did not show centered loading message:\n%s", view)
	}
	message := cmd()
	if service.openCalls != 1 {
		t.Fatalf("OpenProject calls after command = %d, want 1", service.openCalls)
	}
	if service.openReq.ProjectID != "p1" || service.openReq.Profile != "default-profile" {
		t.Fatalf("open request = %#v", service.openReq)
	}
	result, ok := message.(openFinishedMsg)
	if !ok || result.err != nil {
		t.Fatalf("open command message = %#v", message)
	}
}

func TestEnterSchedulesOpenWhileProjectFilterIsFocused(t *testing.T) {
	service := &fakeService{}
	model := loadProjectsForTest(testModel(service),
		domain.Project{ID: "p1", Name: "alpha", Path: "/repos/alpha"},
		domain.Project{ID: "p2", Name: "beta", Path: "/repos/beta"},
	)
	model = updateTestModel(model, tea.FocusMsg{})
	if !model.projects.SettingFilter() {
		t.Fatal("project filter is not focused")
	}
	model, _ = updateTestModelWithCmd(model, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("beta")})

	model, cmd := updateTestModelWithCmd(model, tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || model.operation != "open" {
		t.Fatal("enter did not schedule an open while the project filter was focused")
	}
	if model.projects.FilterValue() != "" || model.projects.FilterState() != list.Unfiltered {
		t.Fatalf("project filter was not cleared: value=%q state=%d", model.projects.FilterValue(), model.projects.FilterState())
	}
	if view := model.View(); !strings.Contains(view, "Opening beta...") {
		t.Fatalf("opening view did not identify the filtered project:\n%s", view)
	}
	cmd()
	if service.openCalls != 1 || service.openReq.ProjectID != "p2" {
		t.Fatalf("open calls = %d, request = %#v", service.openCalls, service.openReq)
	}
}

func TestSuccessErrorAndImmediateMutationRefresh(t *testing.T) {
	service := &fakeService{}
	model := testModel(service)
	model.tmuxRefreshing = false
	model.projectsRefreshing = false
	model.operation = "create"

	updated, cmd := model.Update(createFinishedMsg{result: domain.CreateProjectResult{
		Project: domain.Project{ID: "new", Name: "new"},
	}})
	model = updated.(Model)
	if model.operation != "" || model.statusErr || !strings.Contains(model.status, "Created new") {
		t.Fatalf("unexpected success state: operation=%q status=%q error=%v", model.operation, model.status, model.statusErr)
	}
	if cmd == nil || !model.projectsRefreshing || !model.tmuxRefreshing {
		t.Fatal("successful mutation did not immediately schedule project and tmux refreshes")
	}
	if service.listProjectsCalls != 0 || service.tmuxCalls != 0 {
		t.Fatal("refresh I/O occurred directly in Update")
	}

	model.operation = "open"
	updated, cmd = model.Update(openFinishedMsg{err: errors.New("tmux unavailable")})
	model = updated.(Model)
	if cmd != nil || !model.statusErr || !strings.Contains(model.status, "tmux unavailable") {
		t.Fatalf("unexpected error state: status=%q error=%v cmd=%v", model.status, model.statusErr, cmd != nil)
	}
}

func TestRefreshErrorRetainsStaleSnapshots(t *testing.T) {
	service := &fakeService{}
	model := testModel(service)
	projects := []domain.Project{{ID: "p1", Name: "alpha", Path: "/repos/alpha"}}
	stats := domain.StatsSnapshot{
		CapturedAt: time.Now(),
		Host: domain.HostStats{
			CPUPercent:    27.5,
			MemoryUsed:    4 << 30,
			MemoryTotal:   16 << 30,
			LoadAverage:   [3]float64{1.2, 1.1, 0.9},
			UptimeSeconds: 90061,
		},
		Processes: []domain.PaneProcessStats{{
			WindowName: "alpha", PaneID: "%1", RootPID: 42, Command: "nvim",
			CPUPercent: 3.2, CPUAvailable: true, ResidentBytes: 128 << 20, UptimeSeconds: 61,
		}},
	}
	model = updateTestModel(model, projectsLoadedMsg{projects: projects})
	model = updateTestModel(model, statsLoadedMsg{snapshot: stats})
	model = updateTestModel(model, projectsLoadedMsg{err: errors.New("catalog timeout")})
	model = updateTestModel(model, statsLoadedMsg{err: errors.New("sample timeout")})

	if model.selectedProjectID() != "p1" || model.stats.Host.CPUPercent != 27.5 {
		t.Fatal("a refresh error replaced the last successful snapshot")
	}
	view := model.View()
	for _, expected := range []string{"stale", "catalog timeout", "sample timeout", "27.5%", "nvim", "3.2%", "128.0MiB"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("stale statistics view does not contain %q:\n%s", expected, view)
		}
	}
}

func TestOnlyOneRefreshPerSourceIsInFlight(t *testing.T) {
	service := &fakeService{}
	model := testModel(service)
	if !model.projectsRefreshing {
		t.Fatal("initial project refresh should be in flight")
	}
	updated, cmd := model.Update(projectTickMsg{})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("tick should schedule the next timer")
	}
	if service.listProjectsCalls != 0 {
		t.Fatal("tick performed project I/O directly")
	}

	model.projectsRefreshing = false
	updated, cmd = model.Update(projectTickMsg{})
	model = updated.(Model)
	if cmd == nil || !model.projectsRefreshing {
		t.Fatal("idle source did not schedule a refresh")
	}
}

func TestRefreshCommandsHaveTimeoutAndQuitDoesNotCallService(t *testing.T) {
	service := &fakeService{}
	model := testModel(service)
	before := time.Now()
	message := model.loadProjectsCmd()()
	if result, ok := message.(projectsLoadedMsg); !ok || result.err != nil {
		t.Fatalf("project refresh message = %#v", message)
	}
	if service.listDeadline.Before(before.Add(500*time.Millisecond)) || service.listDeadline.After(before.Add(2*time.Second)) {
		t.Fatalf("refresh deadline = %s, want about one second after %s", service.listDeadline, before)
	}

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("q did not schedule Bubble Tea quit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("q command was not tea.Quit")
	}
	if service.openCalls != 0 || service.createCalls != 0 || service.cloneCalls != 0 || service.actionCalls != 0 || service.tmuxCalls != 0 {
		t.Fatal("q invoked a project or tmux mutation")
	}
}

func TestCreateCloneAndActionFormsScheduleCommands(t *testing.T) {
	service := &fakeService{}
	model := loadProjectsForTest(testModel(service), domain.Project{ID: "p1", Name: "alpha", Path: "/repos/alpha"})

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	model = updated.(Model)
	model.inputs[0].SetValue("created")
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if service.createCalls != 0 || cmd == nil {
		t.Fatal("create form did not defer service I/O to a command")
	}
	if _, ok := cmd().(createFinishedMsg); !ok {
		t.Fatal("create command returned the wrong message type")
	}
	if service.createReq.Name != "created" || !service.createReq.OpenOnFinish {
		t.Fatalf("create request = %#v", service.createReq)
	}

	model.operation = ""
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'c'}})
	model = updated.(Model)
	model.inputs[0].SetValue("https://example.test/repo.git")
	model.inputs[1].SetValue("repo-dir")
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if service.cloneCalls != 0 || cmd == nil {
		t.Fatal("clone form did not defer service I/O to a command")
	}
	cmd()
	if service.cloneReq.Directory != "repo-dir" || service.cloneReq.Profile != "default-profile" {
		t.Fatalf("clone request = %#v", service.cloneReq)
	}

	model.operation = ""
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	model = updated.(Model)
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if service.actionCalls != 0 || cmd == nil {
		t.Fatal("action picker did not defer service I/O to an Exec command")
	}
	cmd()
	if service.actionCalls != 0 {
		t.Fatal("constructing the Exec message ran the interactive service action")
	}
}

func TestActionCommandSelectionAndTerminalHandoffAdapter(t *testing.T) {
	service := &fakeService{}
	model := loadProjectsForTest(testModel(service), domain.Project{ID: "p1", Name: "alpha", Path: "/repos/alpha"})
	model.options.Actions = []Action{{Name: "Custom", ID: "custom"}}
	model.actions = []Action{
		{Name: "Neovim", ID: "nvim"},
		{Name: "Shell", ID: "shell"},
		{Name: "Custom", ID: "custom"},
		{Name: "VS Code", ID: "code"},
	}

	for index, action := range []string{"nvim", "shell", "custom"} {
		if !model.actionUsesTerminal(action) {
			t.Fatalf("actionUsesTerminal(%q) = false", action)
		}
		model.overlay = actionsOverlay
		model.actionIndex = index
		updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
		model = updated.(Model)
		if cmd == nil {
			t.Fatalf("action %q returned no command", action)
		}
		cmd()
		if service.actionCalls != 0 {
			t.Fatalf("action %q used a normal tea.Cmd instead of tea.Exec", action)
		}
		model.operation = ""
	}
	if model.actionUsesTerminal("code") || model.actionUsesTerminal("worktree") {
		t.Fatal("non-terminal action selected Exec")
	}
	model.overlay = actionsOverlay
	model.actionIndex = 3
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if _, ok := cmd().(actionFinishedMsg); !ok || service.actionCalls != 1 || service.actionReq.Action != "code" {
		t.Fatalf("GUI action did not use asynchronous command: calls=%d request=%#v", service.actionCalls, service.actionReq)
	}
	model.operation = ""
	service.actionCalls = 0

	command := &serviceExecCommand{
		ctx: context.Background(), service: service,
		request: domain.RunProjectActionRequest{ProjectID: "p1", Action: "shell"},
	}
	stdin := bytes.NewBufferString("input")
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	command.SetStdin(stdin)
	command.SetStdout(stdout)
	command.SetStderr(stderr)
	if command.stdin != stdin || command.stdout != stdout || command.stderr != stderr {
		t.Fatal("Exec adapter did not accept Bubble Tea terminal streams")
	}
	if err := command.Run(); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	message, ok := command.message(nil).(actionFinishedMsg)
	if !ok || message.result.Action != "shell" || service.actionCalls != 1 {
		t.Fatalf("Exec completion = %#v, calls = %d", message, service.actionCalls)
	}
	model.operation = "action"
	model = updateTestModel(model, message)
	if model.operation != "" || model.statusErr || !strings.Contains(model.status, "Started shell") {
		t.Fatalf("terminal action success state: operation=%q status=%q", model.operation, model.status)
	}
}

func TestConfiguredActionsExcludeReservedIDsAndDispatchAcceptedCustomID(t *testing.T) {
	service := &fakeService{}
	model := NewModel(context.Background(), service, Options{
		GUIEditors: true,
		Actions: []Action{
			{Name: "Open shell logs", ID: "Open shell logs"},
			{Name: "Case-distinct Nvim", ID: "Nvim"},
			{Name: "Override Neovim", ID: "nvim"},
			{Name: "Override Code", ID: "code"},
			{Name: "Override Shell", ID: "shell"},
			{Name: "Override Worktree", ID: "worktree"},
			{Name: "Worktree branch syntax", ID: "worktree:feature"},
		},
	})

	counts := make(map[string]int)
	accepted := make(map[string]bool)
	for _, configured := range model.actions {
		counts[configured.ID]++
		accepted[configured.Name] = true
	}
	for _, id := range []string{"nvim", "code", "shell", "worktree"} {
		if counts[id] != 1 {
			t.Fatalf("built-in action %q appears %d times, want once: %#v", id, counts[id], model.actions)
		}
	}
	for _, rejected := range []string{"Override Neovim", "Override Code", "Override Shell", "Override Worktree", "Worktree branch syntax"} {
		if accepted[rejected] {
			t.Fatalf("reserved configured action %q appeared in picker: %#v", rejected, model.actions)
		}
	}
	for _, name := range []string{"Open shell logs", "Case-distinct Nvim"} {
		if !accepted[name] {
			t.Fatalf("accepted configured action %q missing from picker: %#v", name, model.actions)
		}
	}

	command := &serviceExecCommand{
		ctx: context.Background(), service: service,
		request: domain.RunProjectActionRequest{ProjectID: "p1", Action: "Open shell logs"},
	}
	if err := command.Run(); err != nil {
		t.Fatalf("custom action Run() error = %v", err)
	}
	if service.actionCalls != 1 || service.actionReq.Action != "Open shell logs" {
		t.Fatalf("custom action dispatch calls=%d request=%#v", service.actionCalls, service.actionReq)
	}
}

func TestBypassReservedActionMetadataDoesNotChangeBuiltInHandling(t *testing.T) {
	service := &fakeService{}
	model := NewModel(context.Background(), service, Options{
		GUIEditors: true,
		Actions: []Action{
			{Name: "Code as terminal", ID: "code"},
			{Name: "Worktree as terminal", ID: "worktree"},
		},
	})
	model = loadProjectsForTest(model, domain.Project{ID: "p1", Name: "alpha", Path: "/repos/alpha"})

	if !model.actionUsesTerminal("nvim") || !model.actionUsesTerminal("shell") {
		t.Fatal("terminal built-ins were not classified by the authoritative action policy")
	}
	if model.actionUsesTerminal("code") || model.actionUsesTerminal("worktree") {
		t.Fatal("reserved Options.Action metadata changed non-terminal built-in classification")
	}

	indexOf := func(id string) int {
		for index, candidate := range model.actions {
			if candidate.ID == id {
				return index
			}
		}
		return -1
	}
	codeIndex := indexOf("code")
	worktreeIndex := indexOf("worktree")
	if codeIndex < 0 || worktreeIndex < 0 {
		t.Fatalf("built-in actions missing from picker: %#v", model.actions)
	}

	model.overlay = actionsOverlay
	model.actionIndex = codeIndex
	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil {
		t.Fatal("code action returned no command")
	}
	if _, ok := cmd().(actionFinishedMsg); !ok || service.actionCalls != 1 || service.actionReq.Action != "code" {
		t.Fatalf("code action did not retain asynchronous built-in handling: calls=%d request=%#v", service.actionCalls, service.actionReq)
	}

	model.operation = ""
	model.overlay = actionsOverlay
	model.actionIndex = worktreeIndex
	updated, _ = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if model.overlay != worktreeOverlay || service.actionCalls != 1 {
		t.Fatalf("worktree action did not retain form handling: overlay=%d calls=%d", model.overlay, service.actionCalls)
	}
}

func TestTerminalActionErrorUpdatesModel(t *testing.T) {
	service := &fakeService{actionErr: errors.New("editor exited")}
	model := testModel(service)
	command := &serviceExecCommand{
		ctx: context.Background(), service: service,
		request: domain.RunProjectActionRequest{ProjectID: "p1", Action: "nvim"},
	}
	err := command.Run()
	model.operation = "action"
	model = updateTestModel(model, command.message(err))
	if model.operation != "" || !model.statusErr || !strings.Contains(model.status, "editor exited") {
		t.Fatalf("terminal action error state: operation=%q status=%q", model.operation, model.status)
	}
}

func TestWorktreeFormSchedulesCreateWorktree(t *testing.T) {
	service := &fakeService{}
	model := loadProjectsForTest(testModel(service), domain.Project{ID: "p1", Name: "alpha", Path: "/repos/alpha"})
	model.actionIndex = 2
	model.overlay = actionsOverlay

	updated, cmd := model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil || model.overlay != worktreeOverlay || len(model.inputs) != 2 {
		t.Fatalf("worktree action did not open form: overlay=%d inputs=%d", model.overlay, len(model.inputs))
	}
	if service.actionCalls != 0 || service.worktreeCalls != 0 {
		t.Fatal("opening worktree form performed service I/O")
	}
	model.inputs[0].SetValue("feature/tui")
	model.inputs[1].SetValue("alpha-tui")
	updated, cmd = model.Update(tea.KeyMsg{Type: tea.KeyEnter})
	model = updated.(Model)
	if cmd == nil || model.operation != "worktree" || service.worktreeCalls != 0 {
		t.Fatal("worktree form did not defer CreateWorktree")
	}
	message, ok := cmd().(worktreeFinishedMsg)
	if !ok || message.err != nil {
		t.Fatalf("worktree command message = %#v", message)
	}
	want := domain.CreateWorktreeRequest{
		ProjectID: "p1", Branch: "feature/tui", Directory: "alpha-tui",
		OpenOnFinish: true, Profile: "default-profile",
	}
	if service.worktreeReq != want || service.actionCalls != 0 {
		t.Fatalf("worktree request = %#v, want %#v; action calls = %d", service.worktreeReq, want, service.actionCalls)
	}
}

func TestWorktreeErrorUpdatesModel(t *testing.T) {
	model := testModel(&fakeService{})
	model.operation = "worktree"
	model = updateTestModel(model, worktreeFinishedMsg{err: errors.New("branch exists")})
	if model.operation != "" || !model.statusErr || !strings.Contains(model.status, "branch exists") {
		t.Fatalf("worktree error state: operation=%q status=%q", model.operation, model.status)
	}
}

func TestMinimumSizeAndTabbedStatisticsRendering(t *testing.T) {
	service := &fakeService{}
	model := testModel(service)
	model = updateTestModel(model, tea.WindowSizeMsg{Width: 30, Height: 8})
	if view := model.View(); !strings.Contains(view, "needs at least") {
		t.Fatalf("small terminal view = %q", view)
	}

	model = updateTestModel(model, tea.WindowSizeMsg{Width: 60, Height: 24})
	model.section = statsSection
	model = updateTestModel(model, statsLoadedMsg{snapshot: domain.StatsSnapshot{
		Host: domain.HostStats{CPUPercent: 12.3, MemoryUsed: 2 << 30, MemoryTotal: 8 << 30},
		Processes: []domain.PaneProcessStats{{
			WindowName: "api", PaneID: "%2", RootPID: 1234, Command: "op",
			CPUPercent: 1.5, ResidentBytes: 28 << 20, UptimeSeconds: 120,
		}},
	}})
	view := model.View()
	for _, expected := range []string{"CPU  12.3%", "Memory 2.0GiB / 8.0GiB", "api", "pid 1234", "CPU -", "RSS 28.0MiB"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("statistics view does not contain %q:\n%s", expected, view)
		}
	}
}
