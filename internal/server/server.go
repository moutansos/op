package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode"

	"github.com/moutansos/op/internal/domain"
	"github.com/moutansos/op/internal/notify"
)

const (
	defaultListenAddress     = "127.0.0.1:8787"
	defaultMaxBodyBytes      = 64 << 10
	defaultConcurrentJobs    = 2
	defaultQueuedJobs        = 32
	defaultRetainedJobs      = 256
	defaultReadHeaderTimeout = 5 * time.Second
	defaultReadTimeout       = 15 * time.Second
	defaultWriteTimeout      = 30 * time.Second
	defaultIdleTimeout       = 60 * time.Second
	defaultHealthTimeout     = 2 * time.Second
)

// Options configures both the HTTP handler and the listening server.
type Options struct {
	ListenAddress      string
	Token              string
	TLSCertFile        string
	TLSKeyFile         string
	Version            string
	MaxBodyBytes       int64
	MaxConcurrentJobs  int
	MaxQueuedJobs      int
	MaxRetainedJobs    int
	JobTimeout         time.Duration
	HealthCheckTimeout time.Duration
	ReadHeaderTimeout  time.Duration
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
	IdleTimeout        time.Duration
	Logger             *slog.Logger
	NotifyIngest       *notify.Ingest
}

// DefaultOptions returns secure defaults suitable for local CLI composition.
func DefaultOptions() Options {
	return Options{
		ListenAddress:      defaultListenAddress,
		MaxBodyBytes:       defaultMaxBodyBytes,
		MaxConcurrentJobs:  defaultConcurrentJobs,
		MaxQueuedJobs:      defaultQueuedJobs,
		MaxRetainedJobs:    defaultRetainedJobs,
		ReadHeaderTimeout:  defaultReadHeaderTimeout,
		ReadTimeout:        defaultReadTimeout,
		WriteTimeout:       defaultWriteTimeout,
		IdleTimeout:        defaultIdleTimeout,
		HealthCheckTimeout: defaultHealthTimeout,
	}
}

// Handler is the versioned API handler. Close cancels queued and running jobs.
type Handler struct {
	service domain.Service
	options Options
	mux     *http.ServeMux
	jobs    *jobManager
	locks   *keyLocks
}

// NewHandler constructs an HTTP handler without opening a listener.
func NewHandler(service domain.Service, options Options) (*Handler, error) {
	if service == nil {
		return nil, errors.New("server: service is required")
	}
	options = applyDefaults(options)
	if err := validateOptions(options); err != nil {
		return nil, err
	}

	locks := newKeyLocks()
	h := &Handler{
		service: service,
		options: options,
		mux:     http.NewServeMux(),
		locks:   locks,
	}
	h.jobs = newJobManager(options.MaxConcurrentJobs, options.MaxQueuedJobs, options.MaxRetainedJobs, options.JobTimeout, options.Logger, locks)
	h.routes()
	return h, nil
}

func applyDefaults(options Options) Options {
	defaults := DefaultOptions()
	if options.ListenAddress == "" {
		options.ListenAddress = defaults.ListenAddress
	}
	if options.MaxBodyBytes == 0 {
		options.MaxBodyBytes = defaults.MaxBodyBytes
	}
	if options.MaxConcurrentJobs == 0 {
		options.MaxConcurrentJobs = defaults.MaxConcurrentJobs
	}
	if options.MaxQueuedJobs == 0 {
		options.MaxQueuedJobs = defaults.MaxQueuedJobs
	}
	if options.MaxRetainedJobs == 0 {
		options.MaxRetainedJobs = defaults.MaxRetainedJobs
	}
	if options.ReadHeaderTimeout == 0 {
		options.ReadHeaderTimeout = defaults.ReadHeaderTimeout
	}
	if options.ReadTimeout == 0 {
		options.ReadTimeout = defaults.ReadTimeout
	}
	if options.WriteTimeout == 0 {
		options.WriteTimeout = defaults.WriteTimeout
	}
	if options.IdleTimeout == 0 {
		options.IdleTimeout = defaults.IdleTimeout
	}
	if options.HealthCheckTimeout == 0 {
		options.HealthCheckTimeout = defaults.HealthCheckTimeout
	}
	options.Token = strings.TrimSpace(options.Token)
	return options
}

func validateOptions(options Options) error {
	host, _, err := net.SplitHostPort(options.ListenAddress)
	if err != nil {
		return fmt.Errorf("server: invalid listen address %q: %w", options.ListenAddress, err)
	}
	if (options.TLSCertFile == "") != (options.TLSKeyFile == "") {
		return errors.New("server: TLS certificate and key must be configured together")
	}
	if !isLoopbackHost(host) {
		if options.Token == "" {
			return errors.New("server: bearer authentication is required for a non-loopback listener")
		}
		if options.TLSCertFile == "" {
			return errors.New("server: TLS is required for a non-loopback listener")
		}
	}
	if options.MaxBodyBytes <= 0 {
		return errors.New("server: maximum body size must be greater than zero")
	}
	if options.MaxConcurrentJobs <= 0 {
		return errors.New("server: maximum concurrent jobs must be greater than zero")
	}
	if options.MaxQueuedJobs <= 0 {
		return errors.New("server: maximum queued jobs must be greater than zero")
	}
	if options.MaxRetainedJobs <= 0 {
		return errors.New("server: maximum retained jobs must be greater than zero")
	}
	if options.JobTimeout < 0 || options.HealthCheckTimeout < 0 || options.ReadHeaderTimeout < 0 || options.ReadTimeout < 0 || options.WriteTimeout < 0 || options.IdleTimeout < 0 {
		return errors.New("server: timeouts must not be negative")
	}
	return nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address, err := netip.ParseAddr(host)
	return err == nil && address.IsLoopback()
}

func (h *Handler) routes() {
	h.mux.Handle("GET /v1/health", h.authenticate(http.HandlerFunc(h.health)))

	h.mux.Handle("GET /v1/projects", h.authenticate(http.HandlerFunc(h.listProjects)))
	h.mux.Handle("GET /v1/tmux", h.authenticate(http.HandlerFunc(h.getTmux)))
	h.mux.Handle("GET /v1/jobs/{id}", h.authenticate(http.HandlerFunc(h.getJob)))
	h.mux.Handle("POST /v1/projects", h.authenticate(http.HandlerFunc(h.createProject)))
	h.mux.Handle("POST /v1/projects/clone", h.authenticate(http.HandlerFunc(h.cloneProject)))
	h.mux.Handle("POST /v1/projects/{id}/open", h.authenticate(http.HandlerFunc(h.openProject)))
	h.mux.Handle("POST /v1/projects/{id}/worktrees", h.authenticate(http.HandlerFunc(h.createWorktree)))
	if h.options.NotifyIngest != nil {
		h.mux.Handle("POST /v1/notify", h.authenticate(http.HandlerFunc(h.options.NotifyIngest.HandleNotify)))
		h.mux.Handle("POST /v1/claude-code/hook", h.authenticate(http.HandlerFunc(h.options.NotifyIngest.HandleClaudeCodeHook)))
		h.mux.Handle("POST /v1/grok-code/hook", h.authenticate(http.HandlerFunc(h.options.NotifyIngest.HandleGrokCodeHook)))
		h.mux.Handle("POST /v1/codex/hook", h.authenticate(http.HandlerFunc(h.options.NotifyIngest.HandleCodexHook)))
		h.mux.Handle("POST /v1/copilot-cli/hook", h.authenticate(http.HandlerFunc(h.options.NotifyIngest.HandleCopilotCLIHook)))
	}

	h.mux.HandleFunc("GET /openapi.json", h.openAPI)
	h.mux.HandleFunc("GET /v1/openapi.json", h.openAPI)
	h.mux.HandleFunc("GET /docs", h.swaggerUI)
	h.mux.HandleFunc("GET /docs/", h.swaggerUI)
	h.mux.HandleFunc("GET /swagger", h.swaggerUI)
	h.mux.HandleFunc("GET /swagger/", h.swaggerUI)

	h.mux.HandleFunc("/v1/projects", methodNotAllowed(http.MethodGet, http.MethodPost))
	h.mux.HandleFunc("/v1/projects/clone", methodNotAllowed(http.MethodPost))
	h.mux.HandleFunc("/v1/projects/{id}/open", methodNotAllowed(http.MethodPost))
	h.mux.HandleFunc("/v1/projects/{id}/worktrees", methodNotAllowed(http.MethodPost))
	h.mux.HandleFunc("/v1/jobs/{id}", methodNotAllowed(http.MethodGet))
	h.mux.HandleFunc("/v1/health", methodNotAllowed(http.MethodGet))
	h.mux.HandleFunc("/v1/tmux", methodNotAllowed(http.MethodGet))
	if h.options.NotifyIngest != nil {
		h.mux.HandleFunc("/v1/notify", methodNotAllowed(http.MethodPost))
		h.mux.HandleFunc("/v1/claude-code/hook", methodNotAllowed(http.MethodPost))
		h.mux.HandleFunc("/v1/grok-code/hook", methodNotAllowed(http.MethodPost))
		h.mux.HandleFunc("/v1/codex/hook", methodNotAllowed(http.MethodPost))
		h.mux.HandleFunc("/v1/copilot-cli/hook", methodNotAllowed(http.MethodPost))
	}
	h.mux.HandleFunc("/v1/", func(w http.ResponseWriter, _ *http.Request) {
		writeError(w, domain.ResourceError(domain.ErrorCodeNotFound, "server.route", "route", "route not found", nil))
	})
}

func methodNotAllowed(methods ...string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Allow", strings.Join(methods, ", "))
		writeStatusError(w, http.StatusMethodNotAllowed, domain.NewError(domain.ErrorCodeInvalidArgument, "server.route", "method not allowed", nil))
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	defer func() {
		if recovered := recover(); recovered != nil {
			if h.options.Logger != nil {
				h.options.Logger.Error("HTTP handler panic", "method", r.Method, "path", r.URL.Path)
			}
			writeError(w, domain.NewError(domain.ErrorCodeInternal, "server.request", "internal server error", nil))
		}
	}()
	h.mux.ServeHTTP(w, r)
}

// Close cancels all asynchronous work and waits for workers to exit.
func (h *Handler) Close() error {
	h.jobs.close()
	return nil
}

func (h *Handler) authenticate(next http.HandlerFunc) http.HandlerFunc {
	expected := sha256.Sum256([]byte(h.options.Token))
	return func(w http.ResponseWriter, r *http.Request) {
		values := r.Header.Values("Authorization")
		if h.options.Token == "" || len(values) != 1 {
			unauthorized(w)
			return
		}
		parts := strings.Fields(values[0])
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			unauthorized(w)
			return
		}
		actual := sha256.Sum256([]byte(parts[1]))
		if subtle.ConstantTimeCompare(actual[:], expected[:]) != 1 {
			unauthorized(w)
			return
		}
		next.ServeHTTP(w, r)
	}
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", `Bearer realm="op"`)
	writeError(w, domain.NewError(domain.ErrorCodeUnauthorized, "server.authenticate", "valid bearer token required", nil))
}

// HealthResponse is returned by the liveness endpoint.
type HealthResponse struct {
	Status       string                      `json:"status"`
	Version      string                      `json:"version"`
	Time         time.Time                   `json:"time"`
	Dependencies map[string]DependencyStatus `json:"dependencies"`
}

type DependencyStatus struct {
	Status string `json:"status"`
}

func (h *Handler) health(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cancel := func() {}
	if h.options.HealthCheckTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, h.options.HealthCheckTimeout)
	}
	defer cancel()
	dependencies := map[string]DependencyStatus{
		"projects": {Status: "available"},
		"tmux":     {Status: "available"},
	}
	status := "ok"
	if _, err := h.service.ListProjects(ctx); err != nil {
		dependencies["projects"] = DependencyStatus{Status: "unavailable"}
		status = "degraded"
	}
	if _, err := h.service.GetTmuxSnapshot(ctx); err != nil {
		dependencies["tmux"] = DependencyStatus{Status: "unavailable"}
		status = "degraded"
	}
	writeJSON(w, http.StatusOK, HealthResponse{
		Status:       status,
		Version:      h.options.Version,
		Time:         time.Now().UTC(),
		Dependencies: dependencies,
	})
}

// ProjectsResponse is returned by the project collection endpoint.
type ProjectsResponse struct {
	Projects []domain.Project `json:"projects"`
}

func (h *Handler) listProjects(w http.ResponseWriter, r *http.Request) {
	projects, err := h.service.ListProjects(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	if projects == nil {
		projects = []domain.Project{}
	}
	writeJSON(w, http.StatusOK, ProjectsResponse{Projects: projects})
}

func (h *Handler) getTmux(w http.ResponseWriter, r *http.Request) {
	snapshot, err := h.service.GetTmuxSnapshot(r.Context())
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, snapshot)
}

func (h *Handler) getJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	job, ok := h.jobs.get(id)
	if !ok {
		writeError(w, domain.ResourceError(domain.ErrorCodeNotFound, "server.get_job", "job", "job not found", nil))
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func (h *Handler) createProject(w http.ResponseWriter, r *http.Request) {
	var request domain.CreateProjectRequest
	if !h.decodeJSON(w, r, &request) {
		return
	}
	if err := validateProjectName(request.Name); err != nil {
		writeError(w, err)
		return
	}
	if err := validateOpenProfile(request.Profile, request.OpenOnFinish); err != nil {
		writeError(w, err)
		return
	}
	key := "project:" + strings.ToLower(request.Name)
	if !h.locks.acquire(key) {
		writeError(w, domain.ResourceError(domain.ErrorCodeConflict, "server.create_project", "project", "another operation is already running for this project", nil))
		return
	}
	defer h.locks.release(key)
	result, err := h.service.CreateProject(r.Context(), request)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, result)
}

func (h *Handler) cloneProject(w http.ResponseWriter, r *http.Request) {
	var request domain.CloneRequest
	if !h.decodeJSON(w, r, &request) {
		return
	}
	if err := validateCloneRequest(request); err != nil {
		writeError(w, err)
		return
	}
	idempotencyKey, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	key := cloneKey(request)
	job, replayed, err := h.jobs.submit("clone", key, "", idempotencyKey, requestFingerprint("clone", "", request), func(ctx context.Context) jobResult {
		result, err := h.service.CloneProject(ctx, request)
		if err != nil {
			return jobResult{err: err}
		}
		return jobResult{projectID: result.Project.ID, value: map[string]any{"project": result.Project, "open": result.Open}}
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	w.Header().Set("Location", "/v1/jobs/"+url.PathEscape(job.ID))
	writeJSON(w, http.StatusAccepted, job)
}

type openProjectRequest struct {
	Profile     string `json:"profile,omitempty"`
	NewInstance bool   `json:"newInstance,omitempty"`
}

func (h *Handler) openProject(w http.ResponseWriter, r *http.Request) {
	projectID, ok := pathProjectID(w, r)
	if !ok {
		return
	}
	var input openProjectRequest
	if !h.decodeJSON(w, r, &input) {
		return
	}
	if err := validateProfile(input.Profile); err != nil {
		writeError(w, err)
		return
	}
	key := "project:" + projectID
	if !h.locks.acquire(key) {
		writeError(w, domain.ResourceError(domain.ErrorCodeConflict, "server.open_project", "project", "another operation is already running for this project", nil))
		return
	}
	defer h.locks.release(key)
	result, err := h.service.OpenProject(r.Context(), domain.OpenProjectRequest{
		ProjectID:   projectID,
		Profile:     input.Profile,
		NewInstance: input.NewInstance,
	})
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type worktreeRequest struct {
	Branch       string `json:"branch"`
	Directory    string `json:"directory,omitempty"`
	OpenOnFinish bool   `json:"openOnFinish,omitempty"`
	Profile      string `json:"profile,omitempty"`
}

func (h *Handler) createWorktree(w http.ResponseWriter, r *http.Request) {
	projectID, ok := pathProjectID(w, r)
	if !ok {
		return
	}
	var input worktreeRequest
	if !h.decodeJSON(w, r, &input) {
		return
	}
	if err := validateWorktreeRequest(input); err != nil {
		writeError(w, err)
		return
	}
	idempotencyKey, ok := idempotencyKey(w, r)
	if !ok {
		return
	}
	request := domain.CreateWorktreeRequest{
		ProjectID:    projectID,
		Branch:       input.Branch,
		Directory:    input.Directory,
		OpenOnFinish: input.OpenOnFinish,
		Profile:      input.Profile,
	}
	job, replayed, err := h.jobs.submit("worktree", "project:"+projectID, projectID, idempotencyKey, requestFingerprint("worktree", projectID, input), func(ctx context.Context) jobResult {
		result, err := h.service.CreateWorktree(ctx, request)
		if err != nil {
			return jobResult{err: err}
		}
		return jobResult{projectID: result.Project.ID, value: map[string]any{"project": result.Project, "open": result.Open}}
	})
	if err != nil {
		writeError(w, err)
		return
	}
	if replayed {
		w.Header().Set("Idempotency-Replayed", "true")
	}
	w.Header().Set("Location", "/v1/jobs/"+url.PathEscape(job.ID))
	writeJSON(w, http.StatusAccepted, job)
}

func pathProjectID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := strings.TrimSpace(r.PathValue("id"))
	if id == "" || len(id) > 512 || strings.IndexFunc(id, unicode.IsControl) >= 0 {
		writeError(w, domain.FieldError(domain.ErrorCodeInvalidArgument, "server.request", "projectId", "must be a valid project ID"))
		return "", false
	}
	return id, true
}

func (h *Handler) decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		writeStatusError(w, http.StatusUnsupportedMediaType, domain.FieldError(domain.ErrorCodeInvalidArgument, "server.decode", "contentType", "Content-Type must be application/json"))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, h.options.MaxBodyBytes)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeStatusError(w, http.StatusRequestEntityTooLarge, domain.FieldError(domain.ErrorCodeInvalidArgument, "server.decode", "body", "request body is too large"))
		} else {
			writeError(w, domain.FieldError(domain.ErrorCodeInvalidArgument, "server.decode", "body", "could not read request body"))
		}
		return false
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		writeError(w, domain.FieldError(domain.ErrorCodeInvalidArgument, "server.decode", "body", "request body must contain one valid JSON object"))
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(w, domain.FieldError(domain.ErrorCodeInvalidArgument, "server.decode", "body", "request body must contain one valid JSON object"))
		return false
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, domain.FieldError(domain.ErrorCodeInvalidArgument, "server.decode", "body", "request body must contain one JSON object"))
		return false
	}
	return true
}

func validateProjectName(name string) error {
	if name == "" || name != strings.TrimSpace(name) || len(name) > 255 || name == "." || name == ".." || strings.ContainsAny(name, `/\\`) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, "server.validate", "name", "must be a simple project name")
	}
	return nil
}

func validateCloneRequest(request domain.CloneRequest) error {
	if err := validateCloneURL(request.URL); err != nil {
		return err
	}
	if request.Directory != "" {
		if err := validateProjectName(request.Directory); err != nil {
			typed := err.(*domain.Error)
			typed.Field = "directory"
			return typed
		}
	}
	return validateOpenProfile(request.Profile, request.OpenOnFinish)
}

func validateCloneURL(value string) error {
	if value == "" || value != strings.TrimSpace(value) || strings.IndexFunc(value, unicode.IsControl) >= 0 {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, "server.validate", "url", "must be a valid repository URL")
	}
	if isSCPLikeCloneURL(value) {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, "server.validate", "url", "must be a valid repository URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "https" && scheme != "ssh" {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, "server.validate", "url", "scheme must be https or ssh")
	}
	if parsed.Host == "" || strings.Trim(parsed.Path, "/") == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, "server.validate", "url", "must include a host and repository path without a query or fragment")
	}
	return nil
}

func isSCPLikeCloneURL(value string) bool {
	if strings.Contains(value, "://") || strings.ContainsAny(value, "\\ ") {
		return false
	}
	colon := strings.IndexByte(value, ':')
	if colon <= 0 || colon == len(value)-1 {
		return false
	}
	host := value[:colon]
	path := value[colon+1:]
	at := strings.LastIndexByte(host, '@')
	if at < 0 && isReservedCloneScheme(host) {
		return false
	}
	if at >= 0 {
		if at == 0 || at == len(host)-1 {
			return false
		}
		host = host[at+1:]
	}
	return host != "" && !strings.ContainsAny(host, "/:") && strings.Trim(path, "/") != ""
}

func isReservedCloneScheme(value string) bool {
	switch strings.ToLower(value) {
	case "file", "ftp", "git", "http", "https", "ssh":
		return true
	default:
		return false
	}
}

func validateWorktreeRequest(request worktreeRequest) error {
	if request.Branch == "" || request.Branch != strings.TrimSpace(request.Branch) || len(request.Branch) > 255 || strings.HasPrefix(request.Branch, "-") || strings.IndexFunc(request.Branch, unicode.IsControl) >= 0 {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, "server.validate", "branch", "must be a valid branch name")
	}
	if request.Directory != "" {
		if err := validateProjectName(request.Directory); err != nil {
			typed := err.(*domain.Error)
			typed.Field = "directory"
			return typed
		}
	}
	return validateOpenProfile(request.Profile, request.OpenOnFinish)
}

func validateProfile(profile string) error {
	if len(profile) > 255 || strings.IndexFunc(profile, unicode.IsControl) >= 0 {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, "server.validate", "profile", "must be a valid profile name")
	}
	return nil
}

func validateOpenProfile(profile string, open bool) error {
	if err := validateProfile(profile); err != nil {
		return err
	}
	if profile != "" && !open {
		return domain.FieldError(domain.ErrorCodeInvalidArgument, "server.validate", "profile", "requires openOnFinish")
	}
	return nil
}

func idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	values := r.Header.Values("Idempotency-Key")
	if len(values) == 0 {
		return "", true
	}
	key := values[0]
	if len(values) != 1 || key == "" || key != strings.TrimSpace(key) || len(key) > 255 || strings.IndexFunc(key, unicode.IsControl) >= 0 {
		writeError(w, domain.FieldError(domain.ErrorCodeInvalidArgument, "server.request", "Idempotency-Key", "must be one non-empty value of at most 255 characters"))
		return "", false
	}
	return key, true
}

func requestFingerprint(kind, projectID string, payload any) string {
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(append([]byte(kind+"\x00"+projectID+"\x00"), encoded...))
	return fmt.Sprintf("%x", digest)
}

func cloneKey(request domain.CloneRequest) string {
	if request.Directory != "" {
		return "project:" + strings.ToLower(request.Directory)
	}
	parsed, err := url.Parse(request.URL)
	if err == nil && parsed.User != nil {
		parsed.User = nil
		return "clone:" + strings.ToLower(parsed.String())
	}
	return "clone:" + strings.ToLower(request.URL)
}

// Server owns an http.Server and its asynchronous handler lifecycle.
type Server struct {
	handler *Handler
	http    *http.Server
	options Options
}

// NewServer constructs a configured server without starting it.
func NewServer(service domain.Service, options Options) (*Server, error) {
	handler, err := NewHandler(service, options)
	if err != nil {
		return nil, err
	}
	options = handler.options
	return &Server{
		handler: handler,
		options: options,
		http: &http.Server{
			Addr:              options.ListenAddress,
			Handler:           handler,
			ReadHeaderTimeout: options.ReadHeaderTimeout,
			ReadTimeout:       options.ReadTimeout,
			WriteTimeout:      options.WriteTimeout,
			IdleTimeout:       options.IdleTimeout,
		},
	}, nil
}

// Handler returns the server's composable HTTP handler.
func (s *Server) Handler() http.Handler { return s.handler }

// HTTPServer exposes the configured standard-library server.
func (s *Server) HTTPServer() *http.Server { return s.http }

// ListenAndServe starts HTTP or HTTPS according to Options.
func (s *Server) ListenAndServe() error {
	if s.options.TLSCertFile != "" {
		return s.http.ListenAndServeTLS(s.options.TLSCertFile, s.options.TLSKeyFile)
	}
	return s.http.ListenAndServe()
}

// Serve serves an existing listener, applying TLS when configured.
func (s *Server) Serve(listener net.Listener) error {
	if s.options.TLSCertFile == "" {
		return s.http.Serve(listener)
	}
	certificate, err := tls.LoadX509KeyPair(s.options.TLSCertFile, s.options.TLSKeyFile)
	if err != nil {
		return err
	}
	configuration := &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	return s.http.Serve(tls.NewListener(listener, configuration))
}

// Shutdown stops HTTP traffic and cancels asynchronous jobs.
func (s *Server) Shutdown(ctx context.Context) error {
	s.handler.jobs.stop()
	return errors.Join(s.http.Shutdown(ctx), s.handler.jobs.shutdown(ctx))
}

// Close immediately stops HTTP traffic and waits for asynchronous jobs to exit.
func (s *Server) Close() error {
	return errors.Join(s.http.Close(), s.handler.Close())
}
