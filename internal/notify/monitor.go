package notify

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

const (
	sessionTTL   = time.Hour
	questionTTL  = 30 * time.Minute
	cleanupEvery = 5 * time.Minute
)

type sessionState struct {
	status   string
	lastSeen time.Time
}

type Monitor struct {
	client     *openCodeClient
	notifier   Sender
	desktopURL string
	debounce   time.Duration
	logger     *slog.Logger

	mu          sync.Mutex
	sessions    map[string]sessionState
	pending     map[string]*time.Timer
	subagents   map[string]struct{}
	questions   map[string]time.Time
	permissions map[string]time.Time
}

func newMonitor(client *openCodeClient, notifier Sender, desktopURL string, debounce time.Duration, logger *slog.Logger) *Monitor {
	if logger == nil {
		logger = slog.Default()
	}
	return &Monitor{
		client:      client,
		notifier:    notifier,
		desktopURL:  desktopURL,
		debounce:    debounce,
		logger:      logger,
		sessions:    map[string]sessionState{},
		pending:     map[string]*time.Timer{},
		subagents:   map[string]struct{}{},
		questions:   map[string]time.Time{},
		permissions: map[string]time.Time{},
	}
}

func (m *Monitor) Run(ctx context.Context) error {
	go m.cleanupLoop(ctx)
	return m.client.watch(ctx, func(directory string, payload json.RawMessage) {
		m.handlePayload(ctx, directory, payload)
	})
}

func (m *Monitor) handlePayload(ctx context.Context, directory string, payload json.RawMessage) {
	switch payloadType(payload) {
	case "session.status":
		event, ok := parseSessionStatus(payload)
		if ok {
			m.handleStatus(ctx, directory, event)
		}
	case "question.asked":
		event, ok := parseQuestionAsked(payload)
		if ok {
			m.handleQuestion(ctx, directory, event)
		}
	case "message.part.updated":
		event, ok := parseLegacyQuestion(payload)
		if ok {
			m.handleQuestion(ctx, directory, event)
		}
	case "permission.asked":
		event, ok := parsePermissionAsked(payload)
		if ok {
			m.handlePermission(ctx, directory, event)
		}
	case "permission.updated":
		event, ok := parseLegacyPermission(payload)
		if ok {
			m.handlePermission(ctx, directory, event)
		}
	}
}

func (m *Monitor) handleStatus(ctx context.Context, directory string, event sessionStatusEvent) {
	m.mu.Lock()
	previous := m.sessions[event.SessionID]
	m.sessions[event.SessionID] = sessionState{status: event.Status, lastSeen: time.Now()}
	_, knownSubagent := m.subagents[event.SessionID]
	if event.Status != "idle" {
		if timer := m.pending[event.SessionID]; timer != nil {
			timer.Stop()
			delete(m.pending, event.SessionID)
			m.logger.Info("cancelled pending idle notification", "session", event.SessionID, "status", event.Status)
		}
		m.mu.Unlock()
		return
	}
	if knownSubagent || previous.status == "" || previous.status == "idle" || m.pending[event.SessionID] != nil {
		m.mu.Unlock()
		return
	}
	delay := m.debounce
	timer := time.AfterFunc(delay, func() { m.flushIdle(ctx, directory, event.SessionID) })
	m.pending[event.SessionID] = timer
	m.mu.Unlock()
	m.logger.Info("scheduled idle notification", "session", event.SessionID, "debounce", delay)
}

func (m *Monitor) flushIdle(ctx context.Context, directory, sessionID string) {
	m.mu.Lock()
	delete(m.pending, sessionID)
	state := m.sessions[sessionID]
	_, knownSubagent := m.subagents[sessionID]
	m.mu.Unlock()
	if knownSubagent || state.status != "idle" {
		return
	}
	info, _ := m.client.fetchSessionInfo(ctx, sessionID, directory)
	if info.ParentSessionID != "" {
		m.mu.Lock()
		m.subagents[sessionID] = struct{}{}
		m.mu.Unlock()
		return
	}
	title := info.Title
	if title == "" {
		title = sessionID
	}
	_ = m.notifier.Send(ctx, Notification{
		Type:             TypeIdle,
		Source:           SourceOpenCode,
		SessionID:        sessionID,
		SessionTitle:     title,
		ProjectID:        info.ProjectID,
		ProjectDirectory: directory,
		DesktopURL:       DesktopURL(m.desktopURL, directory, sessionID),
		Timestamp:        time.Now(),
	})
}

func (m *Monitor) handleQuestion(ctx context.Context, directory string, event questionEvent) {
	m.mu.Lock()
	if _, known := m.subagents[event.SessionID]; known {
		m.mu.Unlock()
		return
	}
	if _, seen := m.questions[event.ID]; seen {
		m.mu.Unlock()
		return
	}
	m.questions[event.ID] = time.Now()
	m.mu.Unlock()

	info, _ := m.client.fetchSessionInfo(ctx, event.SessionID, directory)
	if info.ParentSessionID != "" {
		m.mu.Lock()
		m.subagents[event.SessionID] = struct{}{}
		delete(m.questions, event.ID)
		m.mu.Unlock()
		return
	}
	title := info.Title
	if title == "" {
		title = event.SessionID
	}
	_ = m.notifier.Send(ctx, Notification{
		Type:             TypeQuestion,
		Source:           SourceOpenCode,
		SessionID:        event.SessionID,
		SessionTitle:     title,
		ProjectID:        info.ProjectID,
		ProjectDirectory: directory,
		DesktopURL:       DesktopURL(m.desktopURL, directory, event.SessionID),
		Timestamp:        time.Now(),
		Question:         formatQuestionText(event),
		Choices:          buildQuestionChoices(event),
	})
}

func (m *Monitor) handlePermission(ctx context.Context, directory string, event permissionEvent) {
	m.mu.Lock()
	if _, known := m.subagents[event.SessionID]; known {
		m.mu.Unlock()
		return
	}
	if _, seen := m.permissions[event.ID]; seen {
		m.mu.Unlock()
		return
	}
	m.permissions[event.ID] = time.Now()
	m.mu.Unlock()

	info, _ := m.client.fetchSessionInfo(ctx, event.SessionID, directory)
	if info.ParentSessionID != "" {
		m.mu.Lock()
		m.subagents[event.SessionID] = struct{}{}
		delete(m.permissions, event.ID)
		m.mu.Unlock()
		return
	}
	title := info.Title
	if title == "" {
		title = event.SessionID
	}
	_ = m.notifier.Send(ctx, Notification{
		Type:             TypePermission,
		Source:           SourceOpenCode,
		SessionID:        event.SessionID,
		SessionTitle:     title,
		ProjectID:        info.ProjectID,
		ProjectDirectory: directory,
		DesktopURL:       DesktopURL(m.desktopURL, directory, event.SessionID),
		Timestamp:        time.Now(),
		PermissionTitle:  event.Title,
		PermissionType:   event.PermissionType,
		Choices:          buildPermissionChoices(event),
	})
}

func (m *Monitor) cleanupLoop(ctx context.Context) {
	ticker := time.NewTicker(cleanupEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for _, timer := range m.pending {
				timer.Stop()
			}
			m.pending = map[string]*time.Timer{}
			m.mu.Unlock()
			return
		case <-ticker.C:
			m.cleanup(time.Now())
		}
	}
}

func (m *Monitor) cleanup(now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, state := range m.sessions {
		if now.Sub(state.lastSeen) > sessionTTL {
			delete(m.sessions, id)
			delete(m.subagents, id)
		}
	}
	for id, seen := range m.questions {
		if now.Sub(seen) > questionTTL {
			delete(m.questions, id)
		}
	}
	for id, seen := range m.permissions {
		if now.Sub(seen) > questionTTL {
			delete(m.permissions, id)
		}
	}
}

func formatQuestionText(event questionEvent) string {
	parts := make([]string, 0, len(event.Questions))
	for _, item := range event.Questions {
		if item.Header != "" {
			parts = append(parts, item.Header+": "+item.Question)
			continue
		}
		parts = append(parts, item.Question)
	}
	return joinParagraphs(parts)
}

func buildQuestionChoices(event questionEvent) []Choice {
	var choices []Choice
	for _, item := range event.Questions {
		choices = append(choices, item.Options...)
		if item.Custom == nil || *item.Custom {
			choices = append(choices, Choice{Label: "Custom answer", Description: "Type your own response in OpenCode"})
		}
	}
	return choices
}

func buildPermissionChoices(event permissionEvent) []Choice {
	always := "Approve future matching requests for this session"
	if len(event.AlwaysPatterns) > 0 {
		always = "Approve future requests matching: " + joinComma(event.AlwaysPatterns)
	}
	return []Choice{
		{Label: "Once", Description: "Approve just this request"},
		{Label: "Always", Description: always},
		{Label: "Reject", Description: "Deny the request"},
	}
}

func joinParagraphs(parts []string) string {
	result := ""
	for i, part := range parts {
		if i > 0 {
			result += "\n\n"
		}
		result += part
	}
	return result
}

func joinComma(values []string) string {
	result := ""
	for i, value := range values {
		if i > 0 {
			result += ", "
		}
		result += value
	}
	return result
}
