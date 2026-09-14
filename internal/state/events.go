package state

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/moutansos/op/internal/domain"
)

const maxSeenCommands = 256

func validateParentEndpoint(name, raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("state: %s must be an absolute HTTP(S) endpoint without credentials or fragment", name)
	}
	return nil
}

// ResolveEventsURL returns the SSE endpoint. An explicit events URL wins;
// otherwise a parent snapshot URL ending in /state becomes /events, or /events
// is appended.
func ResolveEventsURL(parentURL, eventsURL string) (string, error) {
	if eventsURL != "" {
		if err := validateParentEndpoint("parent events URL", eventsURL); err != nil {
			return "", err
		}
		return eventsURL, nil
	}
	if parentURL == "" {
		return "", nil
	}
	u, err := url.Parse(parentURL)
	if err != nil {
		return "", err
	}
	path := strings.TrimRight(u.Path, "/")
	if strings.HasSuffix(path, "/state") {
		u.Path = strings.TrimSuffix(path, "/state") + "/events"
	} else {
		u.Path = path + "/events"
	}
	return u.String(), nil
}

func (t *Tracker) recordPush(err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastPushAt = time.Now()
	if err != nil {
		t.lastPushErr = err.Error()
		return
	}
	t.lastPushErr = ""
}

func (t *Tracker) setLink(state LinkState, detail string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.link.State = state
	t.link.Detail = detail
	t.link.EventsURL = t.options.ParentEventsURL
}

func (t *Tracker) subscribe(ctx context.Context) {
	backoff := 100 * time.Millisecond
	for {
		if ctx.Err() != nil {
			return
		}
		t.mu.Lock()
		if t.link.State != LinkConnected {
			if t.link.State == LinkError {
				t.link.State = LinkReconnecting
			} else if t.link.State == "" || t.link.State == LinkConnecting {
				t.link.State = LinkConnecting
			} else {
				t.link.State = LinkReconnecting
			}
			t.link.Detail = "connecting to parent"
		}
		t.mu.Unlock()
		err := t.consumeEvents(ctx)
		if ctx.Err() != nil {
			return
		}
		detail := "parent event stream closed"
		state := LinkReconnecting
		if err != nil {
			detail = err.Error()
			if isAuthError(err) {
				state = LinkError
			}
		}
		t.setLink(state, detail)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > 30*time.Second {
			backoff = 30 * time.Second
		}
	}
}

func isAuthError(err error) bool {
	var status *httpStatusError
	return errors.As(err, &status) && (status.code == http.StatusUnauthorized || status.code == http.StatusForbidden)
}

type httpStatusError struct {
	code   int
	status string
}

func (e *httpStatusError) Error() string {
	return "parent events: " + e.status
}

func (t *Tracker) consumeEvents(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.options.ParentEventsURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	req.Header.Set("X-Op-Instance-Id", t.options.InstanceID)
	if t.options.ParentToken != "" {
		req.Header.Set("Authorization", "Bearer "+t.options.ParentToken)
	}
	t.mu.Lock()
	lastID := t.lastEvent
	t.mu.Unlock()
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	resp, err := t.sseClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		err := &httpStatusError{code: resp.StatusCode, status: strings.TrimSpace(resp.Status + " " + string(body))}
		return err
	}
	t.setLink(LinkConnected, "connected")
	return readParentSSE(ctx, resp.Body, func(event sseEvent) {
		t.handleSSE(ctx, event)
	})
}

type sseEvent struct {
	Event string
	ID    string
	Data  string
}

func readParentSSE(ctx context.Context, body io.Reader, emit func(sseEvent)) error {
	reader := bufio.NewReader(body)
	var current sseEvent
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case line == "":
				if current.Data != "" || current.Event != "" || current.ID != "" {
					emit(current)
					current = sseEvent{}
				}
			case strings.HasPrefix(line, ":"):
				// comment / keepalive
			case strings.HasPrefix(line, "event:"):
				current.Event = strings.TrimSpace(line[6:])
			case strings.HasPrefix(line, "id:"):
				current.ID = strings.TrimSpace(line[3:])
			case strings.HasPrefix(line, "data:"):
				chunk := strings.TrimSpace(line[5:])
				if current.Data != "" {
					current.Data += "\n"
				}
				current.Data += chunk
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				if current.Data != "" || current.Event != "" || current.ID != "" {
					emit(current)
				}
				return nil
			}
			return err
		}
	}
}

func (t *Tracker) handleSSE(ctx context.Context, event sseEvent) {
	if event.ID != "" {
		t.mu.Lock()
		t.lastEvent = event.ID
		t.link.LastEvent = time.Now()
		t.mu.Unlock()
	}
	kind := event.Event
	if kind == "ping" || kind == "hello" || event.Data == "" {
		return
	}
	var command struct {
		Version    int             `json:"version"`
		InstanceID string          `json:"instanceId"`
		EventID    string          `json:"eventId"`
		Type       string          `json:"type"`
		Payload    json.RawMessage `json:"payload"`
	}
	if json.Unmarshal([]byte(event.Data), &command) != nil || command.Type == "" {
		return
	}
	if command.InstanceID != "" && command.InstanceID != t.options.InstanceID {
		return
	}
	id := command.EventID
	if id == "" {
		id = event.ID
	}
	if id != "" && t.rememberCommand(id) {
		return
	}
	if command.Type == CommandPing {
		return
	}
	go t.executeCommand(ctx, command.Type, command.Payload)
}

func (t *Tracker) rememberCommand(id string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if _, seen := t.seenIDs[id]; seen {
		return true
	}
	if len(t.seenOrder) >= maxSeenCommands {
		oldest := t.seenOrder[0]
		t.seenOrder = t.seenOrder[1:]
		delete(t.seenIDs, oldest)
	}
	t.seenIDs[id] = struct{}{}
	t.seenOrder = append(t.seenOrder, id)
	return false
}

func (t *Tracker) executeCommand(ctx context.Context, kind string, payload json.RawMessage) {
	cmdCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	switch kind {
	case CommandOpenProject:
		var body struct {
			ProjectID   string `json:"projectId"`
			NativeID    string `json:"nativeId"`
			Profile     string `json:"profile"`
			NewInstance bool   `json:"newInstance"`
		}
		if json.Unmarshal(payload, &body) != nil {
			return
		}
		projectID := t.catalogID(firstNonEmpty(body.NativeID, body.ProjectID))
		if projectID == "" {
			return
		}
		_, _ = t.service.OpenProject(cmdCtx, domain.OpenProjectRequest{ProjectID: projectID, Profile: body.Profile, NewInstance: body.NewInstance})
	case CommandSelectPane:
		var body struct {
			PaneID string `json:"paneId"`
		}
		if json.Unmarshal(payload, &body) != nil || body.PaneID == "" {
			return
		}
		_, _ = t.service.SelectPane(cmdCtx, domain.SelectPaneRequest{PaneID: t.nativePaneID(body.PaneID)})
	}
}

func (t *Tracker) catalogID(id string) string {
	if id == "" {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, project := range t.snapshot.Projects.Data {
		if project.ID == id || project.NativeID == id {
			return project.NativeID
		}
	}
	return id
}

func (t *Tracker) nativePaneID(id string) string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, window := range t.snapshot.Tmux.Data {
		for _, pane := range window.Panes {
			if pane.ID == id || pane.NativeID == id {
				return pane.NativeID
			}
		}
	}
	return id
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
