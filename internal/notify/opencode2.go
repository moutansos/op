package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const openCode2BasicUser = "opencode"

type OpenCode2Config struct {
	Enabled        bool
	BaseURL        string
	DesktopBaseURL string
	Password       string
	ServiceFile    string
}

type openCode2ServiceFile struct {
	URL      string `json:"url"`
	Password string `json:"password"`
}

type openCode2Client struct {
	baseURL     string
	password    string
	serviceFile string
	client      *http.Client
	logger      *slog.Logger
}

type v2WireEvent struct {
	Type     string          `json:"type"`
	Data     json.RawMessage `json:"data"`
	Location *v2Location     `json:"location"`
}

type v2Location struct {
	Directory string `json:"directory"`
}

func newOpenCode2Client(config OpenCode2Config, logger *slog.Logger) *openCode2Client {
	if logger == nil {
		logger = slog.Default()
	}
	serviceFile := strings.TrimSpace(config.ServiceFile)
	if serviceFile == "" {
		serviceFile = defaultOpenCode2ServiceFile()
	}
	return &openCode2Client{
		baseURL:     strings.TrimRight(strings.TrimSpace(config.BaseURL), "/"),
		password:    config.Password,
		serviceFile: serviceFile,
		client:      &http.Client{},
		logger:      logger,
	}
}

func defaultOpenCode2ServiceFile() string {
	if dir := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); dir != "" {
		return filepath.Join(dir, "opencode", "service.json")
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(".local", "state", "opencode", "service.json")
	}
	return filepath.Join(home, ".local", "state", "opencode", "service.json")
}

func (c *openCode2Client) log() *slog.Logger {
	if c == nil || c.logger == nil {
		return slog.Default()
	}
	return c.logger
}

func (c *openCode2Client) resolve() (string, string, error) {
	baseURL := c.baseURL
	password := c.password
	if baseURL == "" || password == "" {
		discovered, err := readOpenCode2ServiceFile(c.serviceFile)
		if err != nil {
			if baseURL == "" {
				return "", "", err
			}
		} else {
			if baseURL == "" {
				baseURL = discovered.URL
			}
			if password == "" {
				password = discovered.Password
			}
		}
	}
	baseURL = canonicalizeOpenCode2URL(baseURL)
	if baseURL == "" {
		return "", "", fmt.Errorf("opencode2 service url is empty")
	}
	return baseURL, password, nil
}

func readOpenCode2ServiceFile(path string) (openCode2ServiceFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return openCode2ServiceFile{}, fmt.Errorf("read opencode2 service file: %w", err)
	}
	var file openCode2ServiceFile
	if err := json.Unmarshal(data, &file); err != nil {
		return openCode2ServiceFile{}, fmt.Errorf("decode opencode2 service file: %w", err)
	}
	return file, nil
}

func canonicalizeOpenCode2URL(raw string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return raw
	}
	host := parsed.Hostname()
	if host == "0.0.0.0" || host == "::" || host == "[::]" {
		port := parsed.Port()
		if port != "" {
			parsed.Host = "127.0.0.1:" + port
		} else {
			parsed.Host = "127.0.0.1"
		}
	}
	return strings.TrimRight(parsed.String(), "/")
}

func (c *openCode2Client) authorize(request *http.Request, password string) {
	if password == "" {
		return
	}
	encoded := base64.StdEncoding.EncodeToString([]byte(openCode2BasicUser + ":" + password))
	request.Header.Set("Authorization", "Basic "+encoded)
}

func (c *openCode2Client) fetchSessionInfo(ctx context.Context, sessionID, directory string) (sessionInfo, bool) {
	baseURL, password, err := c.resolve()
	if err != nil {
		c.log().Error("opencode2 endpoint", "err", err)
		return sessionInfo{}, false
	}
	endpoint := baseURL + "/api/session/" + url.PathEscape(sessionID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		c.log().Error("opencode2 session info request", "err", err)
		return sessionInfo{}, false
	}
	c.authorize(request, password)
	response, err := c.client.Do(request)
	if err != nil {
		c.log().Error("opencode2 session info fetch", "err", err)
		return sessionInfo{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 512))
		c.log().Error("opencode2 session info rejected", "status", response.Status, "body", strings.TrimSpace(string(body)))
		return sessionInfo{}, false
	}
	var payload struct {
		Data struct {
			ID        string `json:"id"`
			ParentID  string `json:"parentID"`
			Title     string `json:"title"`
			ProjectID string `json:"projectID"`
			Location  struct {
				Directory string `json:"directory"`
			} `json:"location"`
		} `json:"data"`
	}
	if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
		c.log().Error("opencode2 session info decode", "err", err)
		return sessionInfo{}, false
	}
	title := payload.Data.Title
	if title == "" {
		title = sessionID
	}
	infoDirectory := payload.Data.Location.Directory
	if infoDirectory == "" {
		infoDirectory = directory
	}
	return sessionInfo{
		ID:              payload.Data.ID,
		ParentSessionID: payload.Data.ParentID,
		Title:           title,
		ProjectID:       payload.Data.ProjectID,
		Directory:       infoDirectory,
	}, true
}

func (c *openCode2Client) watch(ctx context.Context, handle func(string, json.RawMessage)) error {
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
		c.log().Error("opencode2 sse disconnected", "err", err, "reconnect", delay)
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

func (c *openCode2Client) connect(ctx context.Context, handle func(string, json.RawMessage)) error {
	baseURL, password, err := c.resolve()
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/event", nil)
	if err != nil {
		return err
	}
	c.authorize(request, password)
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
	c.log().Info("connected to opencode2 sse", "url", baseURL+"/api/event")
	return readSSE(response.Body, func(data string) {
		directory, payload, ok := translateV2Event([]byte(data))
		if !ok {
			return
		}
		handle(directory, payload)
	})
}

func translateV2Event(data []byte) (string, json.RawMessage, bool) {
	var event v2WireEvent
	if json.Unmarshal(data, &event) != nil || event.Type == "" {
		return "", nil, false
	}
	directory := ""
	if event.Location != nil {
		directory = event.Location.Directory
	}
	switch event.Type {
	case "form.replied", "form.cancelled":
		kind := "question.replied"
		if event.Type == "form.cancelled" {
			kind = "question.rejected"
		}
		payload, err := json.Marshal(map[string]any{"type": kind, "properties": event.Data})
		return directory, payload, err == nil
	case "permission.replied", "permission.rejected", "session.deleted":
		payload, err := json.Marshal(map[string]any{"type": event.Type, "properties": event.Data})
		return directory, payload, err == nil
	case "session.execution.started", "session.step.started":
		sessionID := v2SessionID(event.Data)
		if sessionID == "" {
			return "", nil, false
		}
		return directory, v2StatusPayload(sessionID, "busy"), true
	case "session.execution.succeeded", "session.execution.failed", "session.execution.interrupted":
		sessionID := v2SessionID(event.Data)
		if sessionID == "" {
			return "", nil, false
		}
		return directory, v2StatusPayload(sessionID, "idle"), true
	case "form.created":
		payload, ok := v2QuestionPayload(event.Data)
		if !ok {
			return "", nil, false
		}
		return directory, payload, true
	case "permission.asked":
		payload, ok := v2PermissionPayload(event.Data)
		if !ok {
			return "", nil, false
		}
		return directory, payload, true
	default:
		return "", nil, false
	}
}

func v2SessionID(data json.RawMessage) string {
	var payload struct {
		SessionID string `json:"sessionID"`
	}
	if json.Unmarshal(data, &payload) != nil {
		return ""
	}
	return payload.SessionID
}

func v2StatusPayload(sessionID, status string) json.RawMessage {
	payload, _ := json.Marshal(map[string]any{
		"type": "session.status",
		"properties": map[string]any{
			"sessionID": sessionID,
			"status":    map[string]any{"type": status},
		},
	})
	return payload
}

func v2QuestionPayload(data json.RawMessage) (json.RawMessage, bool) {
	var event struct {
		Form struct {
			ID        string `json:"id"`
			SessionID string `json:"sessionID"`
			Title     string `json:"title"`
			Fields    []struct {
				Key         string `json:"key"`
				Title       string `json:"title"`
				Description string `json:"description"`
				Type        string `json:"type"`
				Custom      *bool  `json:"custom"`
				Options     []struct {
					Label       string `json:"label"`
					Description string `json:"description"`
				} `json:"options"`
			} `json:"fields"`
		} `json:"form"`
	}
	if json.Unmarshal(data, &event) != nil || event.Form.ID == "" || event.Form.SessionID == "" {
		return nil, false
	}
	questions := make([]map[string]any, 0, len(event.Form.Fields)+1)
	if event.Form.Title != "" && len(event.Form.Fields) == 0 {
		questions = append(questions, map[string]any{"question": event.Form.Title})
	}
	for _, field := range event.Form.Fields {
		question := strings.TrimSpace(field.Title)
		if question == "" {
			question = strings.TrimSpace(field.Description)
		}
		if question == "" {
			question = event.Form.Title
		}
		if question == "" {
			question = "OpenCode is waiting for your input"
		}
		options := make([]map[string]any, 0, len(field.Options))
		for _, option := range field.Options {
			label := option.Label
			if label == "" {
				label = "Option"
			}
			options = append(options, map[string]any{"label": label, "description": option.Description})
		}
		item := map[string]any{"question": question, "options": options}
		if event.Form.Title != "" && event.Form.Title != question {
			item["header"] = event.Form.Title
		}
		if field.Custom != nil {
			item["custom"] = *field.Custom
		}
		questions = append(questions, item)
	}
	if len(questions) == 0 {
		questions = []map[string]any{{"question": firstNonEmpty(event.Form.Title, "OpenCode is waiting for your input")}}
	}
	payload, err := json.Marshal(map[string]any{
		"type": "question.asked",
		"properties": map[string]any{
			"id":        event.Form.ID,
			"sessionID": event.Form.SessionID,
			"questions": questions,
		},
	})
	if err != nil {
		return nil, false
	}
	return payload, true
}

func v2PermissionPayload(data json.RawMessage) (json.RawMessage, bool) {
	var event struct {
		ID        string   `json:"id"`
		SessionID string   `json:"sessionID"`
		Action    string   `json:"action"`
		Resources []string `json:"resources"`
		Save      []string `json:"save"`
		Message   string   `json:"message"`
	}
	if json.Unmarshal(data, &event) != nil || event.ID == "" || event.SessionID == "" {
		return nil, false
	}
	permission := event.Action
	if event.Message != "" {
		permission = event.Action
		if permission == "" {
			permission = event.Message
		}
	}
	if permission == "" {
		permission = "permission"
	}
	payload, err := json.Marshal(map[string]any{
		"type": "permission.asked",
		"properties": map[string]any{
			"id":         event.ID,
			"sessionID":  event.SessionID,
			"permission": permission,
			"patterns":   event.Resources,
			"always":     event.Save,
		},
	})
	if err != nil {
		return nil, false
	}
	return payload, true
}
