package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/moutansos/op/internal/domain"
)

const testToken = "correct-horse-battery-staple"

type fakeService struct {
	listProjects   func(context.Context) ([]domain.Project, error)
	createProject  func(context.Context, domain.CreateProjectRequest) (domain.CreateProjectResult, error)
	cloneProject   func(context.Context, domain.CloneRequest) (domain.CloneResult, error)
	createWorktree func(context.Context, domain.CreateWorktreeRequest) (domain.CreateWorktreeResult, error)
	openProject    func(context.Context, domain.OpenProjectRequest) (domain.OpenProjectResult, error)
	getTmux        func(context.Context) (domain.TmuxSnapshot, error)
}

func (f *fakeService) ListProjects(ctx context.Context) ([]domain.Project, error) {
	if f.listProjects != nil {
		return f.listProjects(ctx)
	}
	return nil, nil
}

func (f *fakeService) CreateProject(ctx context.Context, request domain.CreateProjectRequest) (domain.CreateProjectResult, error) {
	if f.createProject != nil {
		return f.createProject(ctx, request)
	}
	return domain.CreateProjectResult{}, nil
}

func (f *fakeService) CloneProject(ctx context.Context, request domain.CloneRequest) (domain.CloneResult, error) {
	if f.cloneProject != nil {
		return f.cloneProject(ctx, request)
	}
	return domain.CloneResult{}, nil
}

func (f *fakeService) CreateWorktree(ctx context.Context, request domain.CreateWorktreeRequest) (domain.CreateWorktreeResult, error) {
	if f.createWorktree != nil {
		return f.createWorktree(ctx, request)
	}
	return domain.CreateWorktreeResult{}, nil
}

func (f *fakeService) OpenProject(ctx context.Context, request domain.OpenProjectRequest) (domain.OpenProjectResult, error) {
	if f.openProject != nil {
		return f.openProject(ctx, request)
	}
	return domain.OpenProjectResult{}, nil
}

func (f *fakeService) RunProjectAction(context.Context, domain.RunProjectActionRequest) (domain.RunProjectActionResult, error) {
	return domain.RunProjectActionResult{}, nil
}

func (f *fakeService) EnsureMainSession(context.Context) (domain.EnsureMainSessionResult, error) {
	return domain.EnsureMainSessionResult{}, nil
}

func (f *fakeService) GetTmuxSnapshot(ctx context.Context) (domain.TmuxSnapshot, error) {
	if f.getTmux != nil {
		return f.getTmux(ctx)
	}
	return domain.TmuxSnapshot{}, nil
}

func (f *fakeService) GetStatsSnapshot(context.Context) (domain.StatsSnapshot, error) {
	return domain.StatsSnapshot{}, nil
}

func newTestHandler(t *testing.T, service domain.Service, edit func(*Options)) *Handler {
	t.Helper()
	options := DefaultOptions()
	options.Token = testToken
	if edit != nil {
		edit(&options)
	}
	handler, err := NewHandler(service, options)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	t.Cleanup(func() { _ = handler.Close() })
	return handler
}

func request(handler http.Handler, method, path, body, token, contentType string) *httptest.ResponseRecorder {
	return requestWithIdempotency(handler, method, path, body, token, contentType, "")
}

func requestWithIdempotency(handler http.Handler, method, path, body, token, contentType, idempotencyKey string) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	handler.ServeHTTP(recorder, req)
	return recorder
}

func decode[T any](t *testing.T, reader io.Reader) T {
	t.Helper()
	var value T
	if err := json.NewDecoder(reader).Decode(&value); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return value
}

func TestAuthenticationIncludesDependencyHealth(t *testing.T) {
	handler := newTestHandler(t, &fakeService{}, nil)

	health := request(handler, http.MethodGet, "/v1/health", "", "", "")
	if health.Code != http.StatusUnauthorized {
		t.Fatalf("health status = %d, body = %s", health.Code, health.Body.String())
	}
	authorizedHealth := request(handler, http.MethodGet, "/v1/health", "", testToken, "")
	if authorizedHealth.Code != http.StatusOK {
		t.Fatalf("authorized health status = %d, body = %s", authorizedHealth.Code, authorizedHealth.Body.String())
	}

	for name, token := range map[string]string{"missing": "", "wrong": "wrong"} {
		t.Run(name, func(t *testing.T) {
			response := request(handler, http.MethodGet, "/v1/projects", "", token, "")
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			if response.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("missing WWW-Authenticate header")
			}
		})
	}

	authorized := request(handler, http.MethodGet, "/v1/projects", "", testToken, "")
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d, body = %s", authorized.Code, authorized.Body.String())
	}
}

func TestStrictJSONDecoding(t *testing.T) {
	called := atomic.Bool{}
	service := &fakeService{createProject: func(_ context.Context, request domain.CreateProjectRequest) (domain.CreateProjectResult, error) {
		called.Store(true)
		return domain.CreateProjectResult{Project: domain.Project{ID: request.Name}}, nil
	}}
	handler := newTestHandler(t, service, func(options *Options) { options.MaxBodyBytes = 32 })

	tests := []struct {
		name        string
		body        string
		contentType string
		want        int
	}{
		{name: "missing content type", body: `{"name":"repo"}`, want: http.StatusUnsupportedMediaType},
		{name: "wrong content type", body: `{"name":"repo"}`, contentType: "text/plain", want: http.StatusUnsupportedMediaType},
		{name: "unknown field", body: `{"name":"repo","command":"rm"}`, contentType: "application/json", want: http.StatusBadRequest},
		{name: "trailing object", body: `{"name":"repo"}{}`, contentType: "application/json", want: http.StatusBadRequest},
		{name: "null", body: `null`, contentType: "application/json", want: http.StatusBadRequest},
		{name: "array", body: `[]`, contentType: "application/json", want: http.StatusBadRequest},
		{name: "empty", body: ``, contentType: "application/json", want: http.StatusBadRequest},
		{name: "too large", body: `{"name":"this-name-is-far-too-long-for-the-limit"}`, contentType: "application/json", want: http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called.Store(false)
			response := request(handler, http.MethodPost, "/v1/projects", test.body, testToken, test.contentType)
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d, body = %s", response.Code, test.want, response.Body.String())
			}
			if called.Load() {
				t.Fatal("service called for invalid request")
			}
		})
	}

	valid := request(handler, http.MethodPost, "/v1/projects", `{"name":"repo"}`, testToken, "application/json; charset=utf-8")
	if valid.Code != http.StatusCreated || !called.Load() {
		t.Fatalf("valid status = %d, called = %v, body = %s", valid.Code, called.Load(), valid.Body.String())
	}
}

func TestRoutesDelegateDomainRequests(t *testing.T) {
	project := domain.Project{ID: "project-1", Name: "repo", Kind: domain.ProjectKindRepository}
	service := &fakeService{
		listProjects: func(context.Context) ([]domain.Project, error) { return []domain.Project{project}, nil },
		createProject: func(_ context.Context, request domain.CreateProjectRequest) (domain.CreateProjectResult, error) {
			if request.Name != "new-repo" || !request.OpenOnFinish {
				t.Fatalf("unexpected create request: %#v", request)
			}
			return domain.CreateProjectResult{Project: project}, nil
		},
		openProject: func(_ context.Context, request domain.OpenProjectRequest) (domain.OpenProjectResult, error) {
			if request.ProjectID != project.ID || request.Profile != "shell" || !request.NewInstance {
				t.Fatalf("unexpected open request: %#v", request)
			}
			return domain.OpenProjectResult{Project: project, Window: domain.TmuxWindow{ID: "@2"}}, nil
		},
		getTmux: func(context.Context) (domain.TmuxSnapshot, error) {
			return domain.TmuxSnapshot{Session: &domain.TmuxSession{ID: "$1", Name: "code"}}, nil
		},
	}
	handler := newTestHandler(t, service, nil)

	projects := request(handler, http.MethodGet, "/v1/projects", "", testToken, "")
	if projects.Code != http.StatusOK || !strings.Contains(projects.Body.String(), project.ID) {
		t.Fatalf("projects: %d %s", projects.Code, projects.Body.String())
	}
	created := request(handler, http.MethodPost, "/v1/projects", `{"name":"new-repo","openOnFinish":true}`, testToken, "application/json")
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", created.Code, created.Body.String())
	}
	opened := request(handler, http.MethodPost, "/v1/projects/project-1/open", `{"profile":"shell","newInstance":true}`, testToken, "application/json")
	if opened.Code != http.StatusOK || !strings.Contains(opened.Body.String(), `"id":"@2"`) {
		t.Fatalf("open: %d %s", opened.Code, opened.Body.String())
	}
	tmux := request(handler, http.MethodGet, "/v1/tmux", "", testToken, "")
	if tmux.Code != http.StatusOK || !strings.Contains(tmux.Body.String(), `"name":"code"`) {
		t.Fatalf("tmux: %d %s", tmux.Code, tmux.Body.String())
	}
	missing := request(handler, http.MethodGet, "/v1/jobs/not-there", "", testToken, "")
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing job: %d %s", missing.Code, missing.Body.String())
	}
	for _, path := range []string{"/openapi.json", "/v1/openapi.json", "/docs/", "/swagger/"} {
		response := request(handler, http.MethodGet, path, "", "", "")
		if response.Code != http.StatusOK {
			t.Fatalf("%s: %d", path, response.Code)
		}
		if strings.Contains(path, "openapi") && !json.Valid(response.Body.Bytes()) {
			t.Fatalf("%s returned invalid JSON", path)
		}
	}
	wrongMethod := request(handler, http.MethodDelete, "/v1/projects", "", "", "")
	if wrongMethod.Code != http.StatusMethodNotAllowed || !strings.Contains(wrongMethod.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("wrong method: %d %s", wrongMethod.Code, wrongMethod.Body.String())
	}
	unknown := request(handler, http.MethodGet, "/v1/unknown", "", "", "")
	if unknown.Code != http.StatusNotFound || !strings.Contains(unknown.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("unknown route: %d %s", unknown.Code, unknown.Body.String())
	}
}

func TestHealthReportsDependencyState(t *testing.T) {
	service := &fakeService{
		listProjects: func(context.Context) ([]domain.Project, error) {
			return nil, domain.NewError(domain.ErrorCodeDependency, "catalog", "not ready", nil)
		},
		getTmux: func(context.Context) (domain.TmuxSnapshot, error) { return domain.TmuxSnapshot{}, nil },
	}
	handler := newTestHandler(t, service, func(options *Options) { options.Version = "test-version" })
	response := request(handler, http.MethodGet, "/v1/health", "", testToken, "")
	health := decode[HealthResponse](t, response.Body)
	if response.Code != http.StatusOK || health.Status != "degraded" || health.Version != "test-version" || health.Dependencies["projects"].Status != "unavailable" || health.Dependencies["tmux"].Status != "available" {
		t.Fatalf("health = %#v", health)
	}
}

func TestAsyncJobTransitionsAndSingleFlight(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	service := &fakeService{cloneProject: func(ctx context.Context, request domain.CloneRequest) (domain.CloneResult, error) {
		once.Do(func() { close(started) })
		select {
		case <-release:
			return domain.CloneResult{Project: domain.Project{ID: "cloned", Name: request.Directory}}, nil
		case <-ctx.Done():
			return domain.CloneResult{}, ctx.Err()
		}
	}}
	handler := newTestHandler(t, service, func(options *Options) { options.MaxConcurrentJobs = 1 })
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	accepted := request(handler, http.MethodPost, "/v1/projects/clone", `{"url":"https://user:secret@example.test/org/repo.git","directory":"repo"}`, testToken, "application/json")
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("clone status = %d, body = %s", accepted.Code, accepted.Body.String())
	}
	job := decode[domain.Job](t, accepted.Body)
	if job.Status != domain.JobStatusQueued || accepted.Header().Get("Location") != "/v1/jobs/"+job.ID {
		t.Fatalf("initial job = %#v, location = %q", job, accepted.Header().Get("Location"))
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("clone did not start")
	}
	waitForJobStatus(t, handler, job.ID, domain.JobStatusRunning)

	duplicate := request(handler, http.MethodPost, "/v1/projects/clone", `{"url":"https://other:credentials@example.test/other.git","directory":"repo"}`, testToken, "application/json")
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d, body = %s", duplicate.Code, duplicate.Body.String())
	}

	close(release)
	finished := waitForJobStatus(t, handler, job.ID, domain.JobStatusSucceeded)
	if finished.StartedAt == nil || finished.FinishedAt == nil || finished.ProjectID != "cloned" {
		t.Fatalf("finished job = %#v", finished)
	}
}

func TestAsyncFailureIsRedacted(t *testing.T) {
	service := &fakeService{createWorktree: func(context.Context, domain.CreateWorktreeRequest) (domain.CreateWorktreeResult, error) {
		return domain.CreateWorktreeResult{}, domain.NewError(domain.ErrorCodeDependency, "git.worktree", "failed https://user:secret@example.test/repo and Bearer top-secret", errors.New("private"))
	}}
	handler := newTestHandler(t, service, nil)
	response := request(handler, http.MethodPost, "/v1/projects/p1/worktrees", `{"branch":"feature"}`, testToken, "application/json")
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	job := decode[domain.Job](t, response.Body)
	failed := waitForJobStatus(t, handler, job.ID, domain.JobStatusFailed)
	encoded, _ := json.Marshal(failed)
	if bytes.Contains(encoded, []byte("secret")) || bytes.Contains(encoded, []byte("top-secret")) || !bytes.Contains(encoded, []byte("[REDACTED]")) {
		t.Fatalf("credentials not redacted: %s", encoded)
	}
}

func TestRedactionPreservesOrdinaryBearerWords(t *testing.T) {
	message := "valid bearer token required; bearer authentication is enabled"
	if got := redact(message); got != message {
		t.Fatalf("redact(%q) = %q", message, got)
	}
	redacted := redact("failed with Bearer top-secret, Bearer secret, and Authorization: Bearer alphabetic")
	if strings.Contains(redacted, "top-secret") || strings.Contains(redacted, "Bearer secret") || strings.Contains(redacted, "alphabetic") || !strings.Contains(redacted, "[REDACTED]") {
		t.Fatalf("credential redaction = %q", redacted)
	}
}

func TestAsyncIdempotencyReplayAndPayloadConflict(t *testing.T) {
	var cloneCalls atomic.Int32
	var worktreeCalls atomic.Int32
	service := &fakeService{
		cloneProject: func(_ context.Context, request domain.CloneRequest) (domain.CloneResult, error) {
			cloneCalls.Add(1)
			return domain.CloneResult{Project: domain.Project{ID: request.Directory}}, nil
		},
		createWorktree: func(_ context.Context, request domain.CreateWorktreeRequest) (domain.CreateWorktreeResult, error) {
			worktreeCalls.Add(1)
			return domain.CreateWorktreeResult{Project: domain.Project{ID: request.Directory}}, nil
		},
	}
	handler := newTestHandler(t, service, nil)

	cloneBody := `{"url":"https://example.test/repo.git","directory":"repo"}`
	first := requestWithIdempotency(handler, http.MethodPost, "/v1/projects/clone", cloneBody, testToken, "application/json", "clone-request")
	replay := requestWithIdempotency(handler, http.MethodPost, "/v1/projects/clone", cloneBody, testToken, "application/json", "clone-request")
	firstJob := decode[domain.Job](t, first.Body)
	replayJob := decode[domain.Job](t, replay.Body)
	if first.Code != http.StatusAccepted || replay.Code != http.StatusAccepted || replayJob.ID != firstJob.ID || replay.Header().Get("Idempotency-Replayed") != "true" {
		t.Fatalf("first=%d/%#v replay=%d/%#v headers=%v", first.Code, firstJob, replay.Code, replayJob, replay.Header())
	}
	waitForJobStatus(t, handler, firstJob.ID, domain.JobStatusSucceeded)
	if cloneCalls.Load() != 1 {
		t.Fatalf("clone calls = %d", cloneCalls.Load())
	}
	mismatch := requestWithIdempotency(handler, http.MethodPost, "/v1/projects/clone", `{"url":"https://example.test/other.git","directory":"other"}`, testToken, "application/json", "clone-request")
	if mismatch.Code != http.StatusConflict {
		t.Fatalf("mismatch = %d %s", mismatch.Code, mismatch.Body.String())
	}

	worktreeBody := `{"branch":"feature","directory":"repo-feature"}`
	worktree := requestWithIdempotency(handler, http.MethodPost, "/v1/projects/p1/worktrees", worktreeBody, testToken, "application/json", "worktree-request")
	worktreeReplay := requestWithIdempotency(handler, http.MethodPost, "/v1/projects/p1/worktrees", worktreeBody, testToken, "application/json", "worktree-request")
	worktreeJob := decode[domain.Job](t, worktree.Body)
	worktreeReplayJob := decode[domain.Job](t, worktreeReplay.Body)
	if worktree.Code != http.StatusAccepted || worktreeReplay.Code != http.StatusAccepted || worktreeReplayJob.ID != worktreeJob.ID {
		t.Fatalf("worktree=%d/%#v replay=%d/%#v", worktree.Code, worktreeJob, worktreeReplay.Code, worktreeReplayJob)
	}
	waitForJobStatus(t, handler, worktreeJob.ID, domain.JobStatusSucceeded)
	if worktreeCalls.Load() != 1 {
		t.Fatalf("worktree calls = %d", worktreeCalls.Load())
	}
}

func TestCompletedJobAndIdempotencyRetentionIsBounded(t *testing.T) {
	service := &fakeService{cloneProject: func(_ context.Context, request domain.CloneRequest) (domain.CloneResult, error) {
		return domain.CloneResult{Project: domain.Project{ID: request.Directory}}, nil
	}}
	handler := newTestHandler(t, service, func(options *Options) {
		options.MaxConcurrentJobs = 1
		options.MaxRetainedJobs = 2
	})
	ids := make([]string, 0, 5)
	for i := range 5 {
		body := fmt.Sprintf(`{"url":"https://example.test/repo-%d.git","directory":"repo-%d"}`, i, i)
		response := requestWithIdempotency(handler, http.MethodPost, "/v1/projects/clone", body, testToken, "application/json", fmt.Sprintf("request-%d", i))
		if response.Code != http.StatusAccepted {
			t.Fatalf("submit %d = %d %s", i, response.Code, response.Body.String())
		}
		job := decode[domain.Job](t, response.Body)
		ids = append(ids, job.ID)
		waitForJobStatus(t, handler, job.ID, domain.JobStatusSucceeded)
	}
	for index, id := range ids {
		response := request(handler, http.MethodGet, "/v1/jobs/"+id, "", testToken, "")
		want := http.StatusNotFound
		if index >= len(ids)-2 {
			want = http.StatusOK
		}
		if response.Code != want {
			t.Fatalf("job %d status = %d, want %d", index, response.Code, want)
		}
	}
	handler.jobs.mu.RLock()
	defer handler.jobs.mu.RUnlock()
	if len(handler.jobs.jobs) != 2 || len(handler.jobs.idempotency) != 2 || len(handler.jobs.jobIdempotency) != 2 {
		t.Fatalf("retained jobs=%d idempotency=%d reverse=%d", len(handler.jobs.jobs), len(handler.jobs.idempotency), len(handler.jobs.jobIdempotency))
	}
}

func TestJobTimeoutTransitionsToFailure(t *testing.T) {
	service := &fakeService{cloneProject: func(ctx context.Context, _ domain.CloneRequest) (domain.CloneResult, error) {
		<-ctx.Done()
		return domain.CloneResult{}, ctx.Err()
	}}
	handler := newTestHandler(t, service, func(options *Options) { options.JobTimeout = 5 * time.Millisecond })
	response := request(handler, http.MethodPost, "/v1/projects/clone", `{"url":"https://example.test/slow.git","directory":"slow"}`, testToken, "application/json")
	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	job := decode[domain.Job](t, response.Body)
	failed := waitForJobStatus(t, handler, job.ID, domain.JobStatusFailed)
	if failed.Error == nil || failed.Error.Code != domain.ErrorCodeTimeout {
		t.Fatalf("failed job = %#v", failed)
	}
}

func TestRejectsUnsafeRemoteInputs(t *testing.T) {
	handler := newTestHandler(t, &fakeService{}, nil)
	tests := []struct {
		path string
		body string
	}{
		{path: "/v1/projects", body: `{"name":"../outside"}`},
		{path: "/v1/projects/clone", body: `{"url":"file:///etc/passwd"}`},
		{path: "/v1/projects/clone", body: `{"url":"https://example.test/repo","directory":"/tmp/repo"}`},
		{path: "/v1/projects/p1/worktrees", body: `{"branch":"-dangerous"}`},
		{path: "/v1/projects/p1/open", body: `{"command":"rm -rf /"}`},
		{path: "/v1/projects", body: `{"name":"repo","profile":"shell"}`},
		{path: "/v1/projects/clone", body: `{"url":"https://example.test/repo","profile":"shell"}`},
		{path: "/v1/projects/p1/worktrees", body: `{"branch":"feature","profile":"shell"}`},
	}
	for _, test := range tests {
		response := request(handler, http.MethodPost, test.path, test.body, testToken, "application/json")
		if response.Code != http.StatusBadRequest {
			t.Errorf("%s with %s: status = %d, body = %s", test.path, test.body, response.Code, response.Body.String())
		}
	}
}

func TestUnusedProfileReturnsStructuredServiceError(t *testing.T) {
	handler := newTestHandler(t, &fakeService{}, nil)
	response := request(handler, http.MethodPost, "/v1/projects/clone", `{"url":"https://example.test/repo","profile":"shell"}`, testToken, "application/json")
	envelope := decode[ErrorResponse](t, response.Body)
	if response.Code != http.StatusBadRequest || envelope.Error == nil || envelope.Error.Code != domain.ErrorCodeInvalidArgument || envelope.Error.Field != "profile" {
		t.Fatalf("status=%d error=%#v", response.Code, envelope.Error)
	}
}

func TestCloneURLValidationMatchesGitSchemes(t *testing.T) {
	accepted := []string{
		"https://github.com/example/project.git",
		"ssh://git@github.com/example/project.git",
		"git@github.com:example/project.git",
		"github.com:example/project.git",
	}
	for _, value := range accepted {
		if err := validateCloneURL(value); err != nil {
			t.Errorf("validateCloneURL(%q) = %v", value, err)
		}
	}
	rejected := []string{
		"http://github.com/example/project.git",
		"git://github.com/example/project.git",
		"file:///tmp/project.git",
		"https://github.com",
		"https://github.com/example/project.git?upload-pack=evil",
		"https:example/project.git",
		"../project.git",
		"C:\\project.git",
	}
	for _, value := range rejected {
		if err := validateCloneURL(value); !domain.IsCode(err, domain.ErrorCodeInvalidArgument) {
			t.Errorf("validateCloneURL(%q) = %v, want invalid argument", value, err)
		}
	}
}

func TestJobConcurrencyLimit(t *testing.T) {
	const count = 6
	var active atomic.Int32
	var maximum atomic.Int32
	release := make(chan struct{})
	service := &fakeService{cloneProject: func(ctx context.Context, request domain.CloneRequest) (domain.CloneResult, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for {
			old := maximum.Load()
			if current <= old || maximum.CompareAndSwap(old, current) {
				break
			}
		}
		select {
		case <-release:
			return domain.CloneResult{Project: domain.Project{ID: request.Directory}}, nil
		case <-ctx.Done():
			return domain.CloneResult{}, ctx.Err()
		}
	}}
	handler := newTestHandler(t, service, func(options *Options) {
		options.MaxConcurrentJobs = 2
		options.MaxQueuedJobs = count
	})
	defer close(release)

	for i := range count {
		body := fmt.Sprintf(`{"url":"https://example.test/repo-%d.git","directory":"repo-%d"}`, i, i)
		response := request(handler, http.MethodPost, "/v1/projects/clone", body, testToken, "application/json")
		if response.Code != http.StatusAccepted {
			t.Fatalf("job %d status = %d, body = %s", i, response.Code, response.Body.String())
		}
	}
	deadline := time.Now().Add(time.Second)
	for active.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := maximum.Load(); got != 2 {
		t.Fatalf("maximum concurrency = %d, want 2", got)
	}
}

func TestStatusMappingAndContext(t *testing.T) {
	tests := []struct {
		code domain.ErrorCode
		want int
	}{
		{domain.ErrorCodeInvalidArgument, http.StatusBadRequest},
		{domain.ErrorCodeNotFound, http.StatusNotFound},
		{domain.ErrorCodeAlreadyExists, http.StatusConflict},
		{domain.ErrorCodeConflict, http.StatusConflict},
		{domain.ErrorCodeUnauthorized, http.StatusUnauthorized},
		{domain.ErrorCodeForbidden, http.StatusForbidden},
		{domain.ErrorCodeDependency, http.StatusServiceUnavailable},
		{domain.ErrorCodeCanceled, http.StatusRequestTimeout},
		{domain.ErrorCodeTimeout, http.StatusGatewayTimeout},
		{domain.ErrorCodeInternal, http.StatusInternalServerError},
	}
	for _, test := range tests {
		t.Run(string(test.code), func(t *testing.T) {
			service := &fakeService{listProjects: func(context.Context) ([]domain.Project, error) {
				return nil, domain.NewError(test.code, "test", "failure", nil)
			}}
			handler := newTestHandler(t, service, nil)
			response := request(handler, http.MethodGet, "/v1/projects", "", testToken, "")
			if response.Code != test.want {
				t.Fatalf("status = %d, want %d", response.Code, test.want)
			}
		})
	}

	contextKey := struct{}{}
	service := &fakeService{listProjects: func(ctx context.Context) ([]domain.Project, error) {
		if ctx.Value(contextKey) != "present" {
			t.Fatal("request context was not propagated")
		}
		return nil, nil
	}}
	handler := newTestHandler(t, service, nil)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/projects", nil).WithContext(context.WithValue(context.Background(), contextKey, "present"))
	req.Header.Set("Authorization", "Bearer "+testToken)
	handler.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
}

func TestServerOptionsAndTimeouts(t *testing.T) {
	if _, err := NewHandler(&fakeService{}, Options{ListenAddress: "0.0.0.0:8787"}); err == nil {
		t.Fatal("non-loopback listener without authentication accepted")
	}
	if _, err := NewHandler(&fakeService{}, Options{Token: testToken, TLSCertFile: "cert.pem"}); err == nil {
		t.Fatal("incomplete TLS pair accepted")
	}
	if _, err := NewHandler(&fakeService{}, Options{ListenAddress: "0.0.0.0:8787", Token: testToken}); err == nil {
		t.Fatal("non-loopback listener without TLS accepted")
	}
	server, err := NewServer(&fakeService{}, Options{Token: testToken})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	t.Cleanup(func() { _ = server.handler.Close() })
	if server.HTTPServer().Addr != defaultListenAddress || server.HTTPServer().ReadHeaderTimeout <= 0 || server.HTTPServer().ReadTimeout <= 0 || server.HTTPServer().WriteTimeout <= 0 || server.HTTPServer().IdleTimeout <= 0 {
		t.Fatalf("server defaults not applied: %#v", server.HTTPServer())
	}
}

func waitForJobStatus(t *testing.T, handler http.Handler, id string, wanted domain.JobStatus) domain.Job {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		response := request(handler, http.MethodGet, "/v1/jobs/"+id, "", testToken, "")
		if response.Code != http.StatusOK {
			t.Fatalf("get job status = %d, body = %s", response.Code, response.Body.String())
		}
		job := decode[domain.Job](t, response.Body)
		if job.Status == wanted {
			return job
		}
		if job.Status == domain.JobStatusFailed || job.Status == domain.JobStatusCanceled || job.Status == domain.JobStatusSucceeded {
			t.Fatalf("job reached %q while waiting for %q: %#v", job.Status, wanted, job)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for job %s to reach %s", id, wanted)
	return domain.Job{}
}
