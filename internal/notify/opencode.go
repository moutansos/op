package notify

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type OpenCodeConfig struct {
	BaseURL        string
	DesktopBaseURL string
	Username       string
	Password       string
}

type sessionStatusEvent struct {
	SessionID string
	Status    string
}

type questionEvent struct {
	ID        string
	SessionID string
	Questions []questionInfo
}

type questionInfo struct {
	Question string
	Header   string
	Options  []Choice
	Custom   *bool
}

type permissionEvent struct {
	ID             string
	SessionID      string
	Title          string
	PermissionType string
	Patterns       []string
	AlwaysPatterns []string
}

type sessionInfo struct {
	ID              string
	ParentSessionID string
	Title           string
	ProjectID       string
}

type openCodeClient struct {
	baseURL string
	headers http.Header
	client  *http.Client
	logger  *slog.Logger
}

func newOpenCodeClient(config OpenCodeConfig, logger *slog.Logger) *openCodeClient {
	headers := make(http.Header)
	if config.Username != "" && config.Password != "" {
		encoded := base64.StdEncoding.EncodeToString([]byte(config.Username + ":" + config.Password))
		headers.Set("Authorization", "Basic "+encoded)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &openCodeClient{
		baseURL: strings.TrimRight(config.BaseURL, "/"),
		headers: headers,
		client:  &http.Client{},
		logger:  logger,
	}
}

func (c *openCodeClient) log() *slog.Logger {
	if c == nil || c.logger == nil {
		return slog.Default()
	}
	return c.logger
}

func (c *openCodeClient) fetchSessionInfo(ctx context.Context, sessionID, directory string) (sessionInfo, bool) {
	endpoint := c.baseURL + "/session/" + url.PathEscape(sessionID) + "?directory=" + url.QueryEscape(directory)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		c.log().Error("session info request", "err", err)
		return sessionInfo{}, false
	}
	for key, values := range c.headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	response, err := c.client.Do(request)
	if err != nil {
		c.log().Error("session info fetch", "err", err)
		return sessionInfo{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		c.log().Error("session info rejected", "status", response.Status, "body", strings.TrimSpace(string(body)))
		return sessionInfo{}, false
	}
	var payload struct {
		ID        string `json:"id"`
		ParentID  string `json:"parentID"`
		Title     string `json:"title"`
		ProjectID string `json:"projectID"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		c.log().Error("session info decode", "err", err)
		return sessionInfo{}, false
	}
	title := payload.Title
	if title == "" {
		title = sessionID
	}
	return sessionInfo{ID: payload.ID, ParentSessionID: payload.ParentID, Title: title, ProjectID: payload.ProjectID}, true
}

func (c *openCodeClient) watch(ctx context.Context, handle func(string, json.RawMessage)) error {
	delay := time.Second
	const maxDelay = 30 * time.Second
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.connect(ctx, handle)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.log().Error("opencode sse disconnected", "err", err, "reconnect", delay)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		if delay < maxDelay {
			delay *= 2
			if delay > maxDelay {
				delay = maxDelay
			}
		}
	}
}

func (c *openCodeClient) connect(ctx context.Context, handle func(string, json.RawMessage)) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/global/event", nil)
	if err != nil {
		return err
	}
	for key, values := range c.headers {
		for _, value := range values {
			request.Header.Add(key, value)
		}
	}
	request.Header.Set("Accept", "text/event-stream")
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		return fmt.Errorf("sse connection failed: %s %s", response.Status, strings.TrimSpace(string(body)))
	}
	c.log().Info("connected to opencode sse", "url", c.baseURL+"/global/event")
	return readSSE(response.Body, func(data string) {
		directory, payload, ok := parseGlobalEvent(data)
		if !ok {
			return
		}
		handle(directory, payload)
	})
}

func readSSE(body io.Reader, emit func(string)) error {
	reader := bufio.NewReader(body)
	var data strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
			switch {
			case strings.HasPrefix(line, "data:"):
				data.WriteString(strings.TrimSpace(line[5:]))
			case line == "" && data.Len() > 0:
				emit(data.String())
				data.Reset()
			}
		}
		if err != nil {
			if err == io.EOF {
				if data.Len() > 0 {
					emit(data.String())
				}
				return nil
			}
			return err
		}
	}
}

func parseGlobalEvent(data string) (string, json.RawMessage, bool) {
	var event struct {
		Directory string          `json:"directory"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(data), &event); err != nil || len(event.Payload) == 0 {
		return "", nil, false
	}
	return event.Directory, event.Payload, true
}

func payloadType(payload json.RawMessage) string {
	var envelope struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(payload, &envelope) != nil {
		return ""
	}
	return envelope.Type
}

func parseSessionStatus(payload json.RawMessage) (sessionStatusEvent, bool) {
	var event struct {
		Properties struct {
			SessionID string `json:"sessionID"`
			Status    struct {
				Type string `json:"type"`
			} `json:"status"`
		} `json:"properties"`
	}
	if json.Unmarshal(payload, &event) != nil || event.Properties.SessionID == "" || event.Properties.Status.Type == "" {
		return sessionStatusEvent{}, false
	}
	return sessionStatusEvent{SessionID: event.Properties.SessionID, Status: event.Properties.Status.Type}, true
}

func parseQuestionAsked(payload json.RawMessage) (questionEvent, bool) {
	var event struct {
		Properties struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
			Questions []struct {
				Question string `json:"question"`
				Header   string `json:"header"`
				Options  []struct {
					Label       string `json:"label"`
					Description string `json:"description"`
				} `json:"options"`
				Custom *bool `json:"custom"`
			} `json:"questions"`
		} `json:"properties"`
	}
	if json.Unmarshal(payload, &event) != nil || event.Properties.ID == "" || event.Properties.SessionID == "" {
		return questionEvent{}, false
	}
	questions := make([]questionInfo, 0, len(event.Properties.Questions))
	for _, item := range event.Properties.Questions {
		options := make([]Choice, 0, len(item.Options))
		for _, option := range item.Options {
			options = append(options, Choice{Label: option.Label, Description: option.Description})
		}
		questions = append(questions, questionInfo{Question: item.Question, Header: item.Header, Options: options, Custom: item.Custom})
	}
	return questionEvent{ID: event.Properties.ID, SessionID: event.Properties.SessionID, Questions: questions}, true
}

func parseLegacyQuestion(payload json.RawMessage) (questionEvent, bool) {
	var event struct {
		Properties struct {
			Part struct {
				Type      string `json:"type"`
				Tool      string `json:"tool"`
				SessionID string `json:"sessionID"`
				State     struct {
					Status string `json:"status"`
					Input  struct {
						Questions []struct {
							Question string `json:"question"`
							Header   string `json:"header"`
							Options  []struct {
								Label       string `json:"label"`
								Description string `json:"description"`
							} `json:"options"`
							Custom *bool `json:"custom"`
						} `json:"questions"`
					} `json:"input"`
				} `json:"state"`
			} `json:"part"`
		} `json:"properties"`
	}
	if json.Unmarshal(payload, &event) != nil {
		return questionEvent{}, false
	}
	part := event.Properties.Part
	if part.Type != "tool" || part.Tool != "question" || part.State.Status != "running" || part.SessionID == "" {
		return questionEvent{}, false
	}
	questions := make([]questionInfo, 0, len(part.State.Input.Questions))
	for _, item := range part.State.Input.Questions {
		options := make([]Choice, 0, len(item.Options))
		for _, option := range item.Options {
			label := option.Label
			if label == "" {
				label = "Option"
			}
			options = append(options, Choice{Label: label, Description: option.Description})
		}
		question := item.Question
		if question == "" {
			question = "OpenCode is waiting for your input"
		}
		questions = append(questions, questionInfo{Question: question, Header: item.Header, Options: options, Custom: item.Custom})
	}
	if len(questions) == 0 {
		questions = []questionInfo{{Question: "OpenCode is waiting for your input"}}
	}
	return questionEvent{ID: part.SessionID + ":" + truncate(questions[0].Question, 100), SessionID: part.SessionID, Questions: questions}, true
}

func parsePermissionAsked(payload json.RawMessage) (permissionEvent, bool) {
	var event struct {
		Properties struct {
			ID         string   `json:"id"`
			SessionID  string   `json:"sessionID"`
			Permission string   `json:"permission"`
			Patterns   []string `json:"patterns"`
			Always     []string `json:"always"`
		} `json:"properties"`
	}
	if json.Unmarshal(payload, &event) != nil || event.Properties.ID == "" || event.Properties.SessionID == "" {
		return permissionEvent{}, false
	}
	title := event.Properties.Permission
	if len(event.Properties.Patterns) > 0 {
		title = event.Properties.Permission + ": " + strings.Join(event.Properties.Patterns, ", ")
	}
	return permissionEvent{
		ID:             event.Properties.ID,
		SessionID:      event.Properties.SessionID,
		Title:          title,
		PermissionType: event.Properties.Permission,
		Patterns:       event.Properties.Patterns,
		AlwaysPatterns: event.Properties.Always,
	}, true
}

func parseLegacyPermission(payload json.RawMessage) (permissionEvent, bool) {
	var event struct {
		Properties struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			Pattern   any    `json:"pattern"`
			SessionID string `json:"sessionID"`
			Title     string `json:"title"`
		} `json:"properties"`
	}
	if json.Unmarshal(payload, &event) != nil || event.Properties.ID == "" || event.Properties.SessionID == "" {
		return permissionEvent{}, false
	}
	patterns := stringList(event.Properties.Pattern)
	return permissionEvent{
		ID:             event.Properties.ID,
		SessionID:      event.Properties.SessionID,
		Title:          event.Properties.Title,
		PermissionType: event.Properties.Type,
		Patterns:       patterns,
		AlwaysPatterns: patterns,
	}, true
}

func stringList(value any) []string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && text != "" {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}
