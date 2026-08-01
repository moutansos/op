package domain

import "context"

// Service is the use-case boundary implemented by internal/app and consumed by
// the CLI, dashboard, and HTTP server.
type Service interface {
	ListProjects(context.Context) ([]Project, error)
	CreateProject(context.Context, CreateProjectRequest) (CreateProjectResult, error)
	CloneProject(context.Context, CloneRequest) (CloneResult, error)
	CreateWorktree(context.Context, CreateWorktreeRequest) (CreateWorktreeResult, error)
	OpenProject(context.Context, OpenProjectRequest) (OpenProjectResult, error)
	RunProjectAction(context.Context, RunProjectActionRequest) (RunProjectActionResult, error)
	EnsureMainSession(context.Context) (EnsureMainSessionResult, error)
	GetTmuxSnapshot(context.Context) (TmuxSnapshot, error)
	GetStatsSnapshot(context.Context) (StatsSnapshot, error)
}
