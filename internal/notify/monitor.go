package notify

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/moutansos/op/internal/domain"
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

type attentionKey struct {
	sessionID string
	kind      Type
	requestID string
}

type eventClient interface {
	watch(ctx context.Context, handle func(string, json.RawMessage)) error
	fetchSessionInfo(ctx context.Context, sessionID, directory string) (sessionInfo, bool)
}

// idleConfirmer reports whether a session is still running after debounce.
// OpenCode 2 implements it against /api/session/active; v1 does not.
type idleConfirmer interface {
	confirmIdle(ctx context.Context, sessionID string) bool
}

type Monitor struct {
	client     eventClient
	notifier   Sender
	desktopURL string
	serverURL  string
	debounce   time.Duration
	source     Source
	logger     *slog.Logger

	mu                 sync.Mutex
	sessions           map[string]sessionState
	pending            map[string]*time.Timer
	subagents          map[string]struct{}
	questions          *eventDeduper
	permissions        *eventDeduper
	attention          map[attentionKey]Observation
	sawQuestionAsked   bool
	sawPermissionAsked bool
}

func newMonitor(client eventClient, notifier Sender, desktopURL string, debounce time.Duration, source Source, logger *slog.Logger) *Monitor {
	if logger == nil {
		logger = slog.Default()
	}
	if source == "" {
		source = SourceOpenCode
	}
	return &Monitor{
		client:      client,
		notifier:    notifier,
		desktopURL:  desktopURL,
		debounce:    debounce,
		source:      source,
		logger:      logger,
		sessions:    map[string]sessionState{},
		pending:     map[string]*time.Timer{},
		subagents:   map[string]struct{}{},
		questions:   newEventDeduper(questionTTL),
		permissions: newEventDeduper(questionTTL),
		attention:   map[attentionKey]Observation{},
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
	case "question.replied", "question.rejected", "question.resolved", "permission.replied", "permission.rejected", "permission.resolved", "session.deleted":
		m.handleLifecycle(directory, payload)
	case "session.status":
		event, ok := parseSessionStatus(payload)
		if ok {
			m.handleStatus(ctx, directory, event)
		}
	case "question.asked":
		event, ok := parseQuestionAsked(payload)
		if ok {
			m.mu.Lock()
			m.sawQuestionAsked = true
			m.mu.Unlock()
			m.handleQuestion(ctx, directory, event)
		}
	case "message.part.updated":
		// The tool part usually arrives before question.asked, so the latch
		// rarely suppresses that twin — call-id correlation does. It does drop
		// a later part with no ask behind it, which is what a denied question
		// permission looks like.
		m.mu.Lock()
		seen := m.sawQuestionAsked
		m.mu.Unlock()
		if seen {
			return
		}
		event, ok := parseLegacyQuestion(payload)
		if ok {
			m.handleQuestion(ctx, directory, event)
		}
	case "permission.asked":
		event, ok := parsePermissionAsked(payload)
		if ok {
			m.mu.Lock()
			m.sawPermissionAsked = true
			m.mu.Unlock()
			m.handlePermission(ctx, directory, event)
		}
	case "permission.updated":
		m.mu.Lock()
		seen := m.sawPermissionAsked
		m.mu.Unlock()
		if seen {
			return
		}
		event, ok := parseLegacyPermission(payload)
		if ok {
			m.handlePermission(ctx, directory, event)
		}
	}
}

func (m *Monitor) handleStatus(ctx context.Context, directory string, event sessionStatusEvent) {
	activity := domain.AgentActivityUnknown
	switch event.Status {
	case "busy", "retry":
		activity = domain.AgentActivityWorking
	case "idle":
		activity = domain.AgentActivityIdle
	}
	if activity != domain.AgentActivityUnknown {
		m.mu.Lock()
		m.clearAttentionLocked(event.SessionID)
		m.mu.Unlock()
		observe(m.notifier, Observation{Source: m.source, SessionID: event.SessionID,
			ProjectDirectory: directory, Activity: activity, Detail: event.Status,
			Timestamp: time.Now(), Coverage: CoverageNative})
	}
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
	if confirmer, ok := m.client.(idleConfirmer); ok {
		if !confirmer.confirmIdle(ctx, sessionID) {
			m.mu.Lock()
			m.sessions[sessionID] = sessionState{status: "busy", lastSeen: time.Now()}
			m.mu.Unlock()
			m.logger.Info("session still active on server, skipping idle notification", "session", sessionID)
			return
		}
	}
	title := info.Title
	if title == "" {
		title = sessionID
	}
	directory = firstNonEmpty(info.Directory, directory)
	_ = m.notifier.Send(alreadyObserved(ctx), Notification{
		Type:             TypeIdle,
		Source:           m.source,
		SessionID:        sessionID,
		SessionTitle:     title,
		ProjectID:        info.ProjectID,
		ProjectDirectory: directory,
		DesktopURL:       m.desktopLink(directory, sessionID),
		Timestamp:        time.Now(),
	})
}

func (m *Monitor) handleQuestion(ctx context.Context, directory string, event questionEvent) {
	m.mu.Lock()
	if _, known := m.subagents[event.SessionID]; known {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	// Claim the request id and tool call before the session-info await so a
	// twin event that overlaps in flight cannot also notify.
	if !m.questions.reserve(questionKeys(event)) {
		return
	}

	info, _ := m.client.fetchSessionInfo(ctx, event.SessionID, directory)
	if info.ParentSessionID != "" {
		m.mu.Lock()
		m.subagents[event.SessionID] = struct{}{}
		m.mu.Unlock()
		return
	}
	title := info.Title
	if title == "" {
		title = event.SessionID
	}
	directory = firstNonEmpty(info.Directory, directory)
	_ = m.sendAttention(ctx, event.ID, Notification{
		Type:             TypeQuestion,
		Source:           m.source,
		SessionID:        event.SessionID,
		SessionTitle:     title,
		ProjectID:        info.ProjectID,
		ProjectDirectory: directory,
		DesktopURL:       m.desktopLink(directory, event.SessionID),
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
	m.mu.Unlock()
	if !m.permissions.reserve(permissionKeys(event)) {
		return
	}

	info, _ := m.client.fetchSessionInfo(ctx, event.SessionID, directory)
	if info.ParentSessionID != "" {
		m.mu.Lock()
		m.subagents[event.SessionID] = struct{}{}
		m.mu.Unlock()
		return
	}
	title := info.Title
	if title == "" {
		title = event.SessionID
	}
	directory = firstNonEmpty(info.Directory, directory)
	_ = m.sendAttention(ctx, event.ID, Notification{
		Type:             TypePermission,
		Source:           m.source,
		SessionID:        event.SessionID,
		SessionTitle:     title,
		ProjectID:        info.ProjectID,
		ProjectDirectory: directory,
		DesktopURL:       m.desktopLink(directory, event.SessionID),
		Timestamp:        time.Now(),
		PermissionTitle:  event.Title,
		PermissionType:   event.PermissionType,
		Choices:          buildPermissionChoices(event),
	})
}

func (m *Monitor) handleLifecycle(directory string, payload json.RawMessage) {
	var event struct {
		Type       string `json:"type"`
		Properties struct {
			RequestID string `json:"requestID"`
			ID        string `json:"id"`
			FormID    string `json:"formID"`
			SessionID string `json:"sessionID"`
			ProjectID string `json:"projectID"`
			Info      struct {
				ID        string `json:"id"`
				ProjectID string `json:"projectID"`
				Directory string `json:"directory"`
			} `json:"info"`
		} `json:"properties"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return
	}
	terminated := event.Type == "session.deleted"
	id := event.Properties.SessionID
	if terminated {
		id = firstNonEmpty(id, event.Properties.Info.ID)
	}
	if id == "" {
		return
	}
	m.mu.Lock()
	if timer := m.pending[id]; timer != nil {
		timer.Stop()
		delete(m.pending, id)
	}
	if terminated {
		delete(m.sessions, id)
		delete(m.subagents, id)
		m.clearAttentionLocked(id)
	} else {
		m.sessions[id] = sessionState{status: "busy", lastSeen: time.Now()}
		kind := TypeQuestion
		if strings.HasPrefix(event.Type, "permission.") {
			kind = TypePermission
		}
		requestID := firstNonEmpty(event.Properties.RequestID, event.Properties.FormID, event.Properties.ID)
		delete(m.attention, attentionKey{id, kind, requestID})
		var remaining *Observation
		for key, observation := range m.attention {
			if key.sessionID == id && (remaining == nil || observation.Timestamp.After(remaining.Timestamp)) {
				copy := observation
				remaining = &copy
			}
		}
		if remaining != nil {
			remaining.Timestamp = time.Now()
			observe(m.notifier, *remaining)
			m.mu.Unlock()
			return
		}
	}
	m.mu.Unlock()
	// Resolution clears attention but does not itself prove resumed execution.
	activity := domain.AgentActivityUnknown
	observe(m.notifier, Observation{Source: m.source, SessionID: id,
		ProjectID:        firstNonEmpty(event.Properties.ProjectID, event.Properties.Info.ProjectID),
		ProjectDirectory: firstNonEmpty(event.Properties.Info.Directory, directory),
		Activity:         activity, Detail: event.Type, Terminated: terminated,
		Timestamp: time.Now(), Coverage: CoverageNative})
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
	m.questions.mu.Lock()
	m.questions.forgetLocked(now)
	m.questions.mu.Unlock()
	m.permissions.mu.Lock()
	m.permissions.forgetLocked(now)
	m.permissions.mu.Unlock()
	for id, state := range m.sessions {
		if now.Sub(state.lastSeen) > sessionTTL {
			delete(m.sessions, id)
			delete(m.subagents, id)
		}
	}
	for key, observation := range m.attention {
		if now.Sub(observation.Timestamp) > questionTTL {
			delete(m.attention, key)
		}
	}
}

func (m *Monitor) clearAttentionLocked(sessionID string) {
	for key := range m.attention {
		if key.sessionID == sessionID {
			delete(m.attention, key)
		}
	}
}

func (m *Monitor) sendAttention(ctx context.Context, requestID string, notification Notification) error {
	observation := notificationObservation(notification, CoverageNative)
	m.mu.Lock()
	const maxPendingAttention = 1024
	if len(m.attention) >= maxPendingAttention {
		var oldest attentionKey
		var at time.Time
		for key, value := range m.attention {
			if at.IsZero() || value.Timestamp.Before(at) {
				oldest, at = key, value.Timestamp
			}
		}
		delete(m.attention, oldest)
	}
	m.attention[attentionKey{notification.SessionID, notification.Type, requestID}] = observation
	observe(m.notifier, observation)
	m.mu.Unlock()
	return m.notifier.Send(alreadyObserved(ctx), notification)
}

func (m *Monitor) desktopLink(directory, sessionID string) string {
	if m.source == SourceOpenCode2 {
		return DesktopURLForServer(m.desktopURL, m.serverURL, sessionID)
	}
	return DesktopURL(m.desktopURL, directory, sessionID)
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
