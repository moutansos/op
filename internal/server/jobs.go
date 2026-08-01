package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"sync"
	"time"

	"github.com/moutansos/op/internal/domain"
)

type keyLocks struct {
	mu   sync.Mutex
	keys map[string]struct{}
}

func newKeyLocks() *keyLocks { return &keyLocks{keys: make(map[string]struct{})} }

func (l *keyLocks) acquire(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, exists := l.keys[key]; exists {
		return false
	}
	l.keys[key] = struct{}{}
	return true
}

func (l *keyLocks) release(key string) {
	l.mu.Lock()
	delete(l.keys, key)
	l.mu.Unlock()
}

type jobResult struct {
	projectID string
	value     map[string]any
	err       error
}

type queuedJob struct {
	id  string
	key string
	run func(context.Context) jobResult
}

type idempotencyRecord struct {
	fingerprint string
	jobID       string
}

type jobManager struct {
	ctx            context.Context
	cancel         context.CancelFunc
	timeout        time.Duration
	logger         *slog.Logger
	locks          *keyLocks
	queue          chan queuedJob
	mu             sync.RWMutex
	jobs           map[string]domain.Job
	retainedJobs   int
	completed      []string
	idempotency    map[string]idempotencyRecord
	jobIdempotency map[string]string
	closed         bool
	workers        sync.WaitGroup
	done           chan struct{}
}

func newJobManager(concurrency, queueSize, retainedJobs int, timeout time.Duration, logger *slog.Logger, locks *keyLocks) *jobManager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &jobManager{
		ctx:            ctx,
		cancel:         cancel,
		timeout:        timeout,
		logger:         logger,
		locks:          locks,
		queue:          make(chan queuedJob, queueSize),
		jobs:           make(map[string]domain.Job),
		retainedJobs:   retainedJobs,
		idempotency:    make(map[string]idempotencyRecord),
		jobIdempotency: make(map[string]string),
		done:           make(chan struct{}),
	}
	for range concurrency {
		m.workers.Add(1)
		go m.worker()
	}
	go func() {
		m.workers.Wait()
		close(m.done)
	}()
	return m
}

func (m *jobManager) submit(kind, key, projectID, idempotencyKey, fingerprint string, run func(context.Context) jobResult) (domain.Job, bool, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return domain.Job{}, false, domain.NewError(domain.ErrorCodeDependency, "server.queue_job", "job manager is closed", nil)
	}
	if idempotencyKey != "" {
		if record, ok := m.idempotency[idempotencyKey]; ok {
			if record.fingerprint != fingerprint {
				m.mu.Unlock()
				return domain.Job{}, false, domain.FieldError(domain.ErrorCodeConflict, "server.queue_job", "Idempotency-Key", "was already used with a different request payload")
			}
			job := cloneJob(m.jobs[record.jobID])
			m.mu.Unlock()
			return job, true, nil
		}
	}
	if !m.locks.acquire(key) {
		m.mu.Unlock()
		return domain.Job{}, false, domain.ResourceError(domain.ErrorCodeConflict, "server.queue_job", "project", "another operation is already running for this project", nil)
	}
	id, err := newJobID()
	if err != nil {
		m.mu.Unlock()
		m.locks.release(key)
		return domain.Job{}, false, domain.NewError(domain.ErrorCodeInternal, "server.queue_job", "could not create job", nil)
	}
	job := domain.Job{
		ID:        id,
		Kind:      kind,
		Status:    domain.JobStatusQueued,
		CreatedAt: time.Now().UTC(),
		ProjectID: projectID,
	}
	m.jobs[id] = job
	if idempotencyKey != "" {
		m.idempotency[idempotencyKey] = idempotencyRecord{fingerprint: fingerprint, jobID: id}
		m.jobIdempotency[id] = idempotencyKey
	}
	select {
	case m.queue <- queuedJob{id: id, key: key, run: run}:
		m.mu.Unlock()
		return job, false, nil
	default:
		delete(m.jobs, id)
		delete(m.idempotency, idempotencyKey)
		delete(m.jobIdempotency, id)
		m.mu.Unlock()
		m.locks.release(key)
		return domain.Job{}, false, domain.NewError(domain.ErrorCodeDependency, "server.queue_job", "job queue is full", nil)
	}
}

func (m *jobManager) worker() {
	defer m.workers.Done()
	for {
		select {
		case <-m.ctx.Done():
			return
		case queued := <-m.queue:
			m.run(queued)
		}
	}
}

func (m *jobManager) run(queued queuedJob) {
	defer m.locks.release(queued.key)
	started := time.Now().UTC()
	m.mu.Lock()
	job, exists := m.jobs[queued.id]
	if !exists || job.Status != domain.JobStatusQueued {
		m.mu.Unlock()
		return
	}
	job.Status = domain.JobStatusRunning
	job.StartedAt = &started
	m.jobs[queued.id] = job
	m.mu.Unlock()

	ctx := m.ctx
	cancel := func() {}
	if m.timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, m.timeout)
	}
	result := runJob(ctx, queued.run)
	if result.err == nil && ctx.Err() != nil {
		result.err = ctx.Err()
	}
	cancel()
	finished := time.Now().UTC()

	m.mu.Lock()
	job = m.jobs[queued.id]
	job.FinishedAt = &finished
	if result.err == nil {
		job.Status = domain.JobStatusSucceeded
		job.ProjectID = result.projectID
		job.Result = result.value
	} else {
		job.Error = publicError(result.err)
		if domain.CodeOf(result.err) == domain.ErrorCodeCanceled || m.ctx.Err() != nil {
			job.Status = domain.JobStatusCanceled
		} else {
			job.Status = domain.JobStatusFailed
		}
	}
	m.jobs[queued.id] = job
	m.completed = append(m.completed, queued.id)
	m.pruneLocked()
	m.mu.Unlock()

	if result.err != nil && m.logger != nil {
		m.logger.Error("asynchronous job failed", "job_id", job.ID, "kind", job.Kind, "error", job.Error.Error())
	}
}

func runJob(ctx context.Context, run func(context.Context) jobResult) (result jobResult) {
	defer func() {
		if recover() != nil {
			result = jobResult{err: domain.NewError(domain.ErrorCodeInternal, "server.job", "job failed unexpectedly", nil)}
		}
	}()
	return run(ctx)
}

func (m *jobManager) get(id string) (domain.Job, bool) {
	m.mu.RLock()
	job, ok := m.jobs[id]
	m.mu.RUnlock()
	return cloneJob(job), ok
}

func cloneJob(job domain.Job) domain.Job {
	if job.Result != nil {
		result := make(map[string]any, len(job.Result))
		for key, value := range job.Result {
			result[key] = value
		}
		job.Result = result
	}
	if job.Error != nil {
		copy := *job.Error
		job.Error = &copy
	}
	return job
}

func (m *jobManager) pruneLocked() {
	for len(m.completed) > m.retainedJobs {
		id := m.completed[0]
		m.completed = m.completed[1:]
		delete(m.jobs, id)
		if key := m.jobIdempotency[id]; key != "" {
			if record, ok := m.idempotency[key]; ok && record.jobID == id {
				delete(m.idempotency, key)
			}
			delete(m.jobIdempotency, id)
		}
	}
}

func (m *jobManager) close() {
	m.stop()
	<-m.done
}

func (m *jobManager) shutdown(ctx context.Context) error {
	m.stop()
	select {
	case <-m.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *jobManager) stop() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.cancel()
	now := time.Now().UTC()
	queuedKeys := make([]string, 0)
	for id, job := range m.jobs {
		if job.Status == domain.JobStatusQueued {
			job.Status = domain.JobStatusCanceled
			job.FinishedAt = &now
			job.Error = domain.NewError(domain.ErrorCodeCanceled, "server.job", "server stopped before job started", nil)
			m.jobs[id] = job
			m.completed = append(m.completed, id)
		}
	}
	m.pruneLocked()
	for {
		select {
		case queued := <-m.queue:
			queuedKeys = append(queuedKeys, queued.key)
		default:
			m.mu.Unlock()
			for _, key := range queuedKeys {
				m.locks.release(key)
			}
			return
		}
	}
}

func newJobID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
