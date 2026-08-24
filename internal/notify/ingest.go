package notify

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const maxIngestBody = 1 << 20

type Ingest struct {
	notifier Sender
	logger   *slog.Logger
}

func NewIngest(notifier Sender, logger *slog.Logger) *Ingest {
	if logger == nil {
		logger = slog.Default()
	}
	return &Ingest{notifier: notifier, logger: logger}
}

func (i *Ingest) HandleNotify(w http.ResponseWriter, r *http.Request) {
	body, ok := readJSONObject(w, r)
	if !ok {
		return
	}
	notification, err := ParseNotifyBody(body)
	if err != nil {
		writeIngestError(w, http.StatusBadRequest, err.Error())
		return
	}
	i.logger.Info("ingest notify", "type", notification.Type, "source", notification.Source, "session", notification.SessionID)
	_ = i.notifier.Send(r.Context(), notification)
	writeIngestOK(w, false)
}

func (i *Ingest) HandleClaudeCodeHook(w http.ResponseWriter, r *http.Request) {
	i.handleHook(w, r, "claude-code", func(payload map[string]any) *Notification {
		return MapClaudeCodeHook(payload)
	})
}

func (i *Ingest) HandleGrokCodeHook(w http.ResponseWriter, r *http.Request) {
	i.handleHook(w, r, "grok-code", func(payload map[string]any) *Notification {
		return MapGrokCodeHook(payload)
	})
}

func (i *Ingest) HandleCodexHook(w http.ResponseWriter, r *http.Request) {
	i.handleHook(w, r, "codex", func(payload map[string]any) *Notification {
		return MapCodexHook(payload)
	})
}

func (i *Ingest) HandleCopilotCLIHook(w http.ResponseWriter, r *http.Request) {
	i.handleHook(w, r, "copilot-cli", func(payload map[string]any) *Notification {
		return MapCopilotCLIHook(payload)
	})
}

func (i *Ingest) handleHook(w http.ResponseWriter, r *http.Request, source string, mapHook func(map[string]any) *Notification) {
	payload, ok := readJSONObject(w, r)
	if !ok {
		return
	}
	notification := mapHook(payload)
	if notification == nil {
		i.logger.Info("ingest hook ignored", "source", source)
		writeIngestOK(w, true)
		return
	}
	i.logger.Info("ingest hook", "source", source, "type", notification.Type, "session", notification.SessionID)
	_ = i.notifier.Send(r.Context(), *notification)
	writeIngestOK(w, false)
}

func readJSONObject(w http.ResponseWriter, r *http.Request) (map[string]any, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxIngestBody)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeIngestError(w, http.StatusBadRequest, "could not read request body")
		return nil, false
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil || payload == nil {
		writeIngestError(w, http.StatusBadRequest, "Invalid JSON body")
		return nil, false
	}
	return payload, true
}

func writeIngestOK(w http.ResponseWriter, ignored bool) {
	body := map[string]any{"ok": true}
	if ignored {
		body["ignored"] = true
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(body)
}

func writeIngestError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": message})
}

func ParseNotifyBody(body map[string]any) (Notification, error) {
	kind, _ := body["type"].(string)
	if kind != string(TypeIdle) && kind != string(TypeQuestion) && kind != string(TypePermission) {
		return Notification{}, errString(`type must be "idle", "question", or "permission"`)
	}
	sessionID, _ := body["sessionId"].(string)
	if sessionID == "" {
		return Notification{}, errString("sessionId is required and must be a string")
	}
	notification := Notification{
		Type:             Type(kind),
		SessionID:        sessionID,
		SessionTitle:     stringField(body, "sessionTitle"),
		ProjectID:        stringField(body, "projectId"),
		ProjectDirectory: stringField(body, "projectDirectory"),
		DesktopURL:       stringField(body, "desktopUrl"),
		Timestamp:        time.Now(),
	}
	if notification.SessionTitle == "" {
		notification.SessionTitle = sessionID
	}
	switch source := stringField(body, "source"); source {
	case string(SourceClaudeCode), string(SourceOpenCode), string(SourceGrokCode), string(SourceCodex), string(SourceCopilotCLI):
		notification.Source = Source(source)
	}
	if raw, ok := body["timestamp"].(string); ok && raw != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			notification.Timestamp = parsed
		} else if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			notification.Timestamp = parsed
		}
	}
	if hostname := strings.TrimSpace(stringField(body, "hostname")); hostname != "" {
		notification.Hostname = hostname
	}
	if hops, ok := asInt(body["hops"]); ok && hops >= 0 {
		notification.Hops = hops
	}
	if question, ok := body["question"].(string); ok {
		notification.Question = question
	}
	if title, ok := body["permissionTitle"].(string); ok {
		notification.PermissionTitle = title
	}
	if kind, ok := body["permissionType"].(string); ok {
		notification.PermissionType = kind
	}
	if raw, ok := body["choices"].([]any); ok {
		notification.Choices = parseChoices(raw)
	}
	return notification, nil
}

type stringError string

func (e stringError) Error() string { return string(e) }

func errString(message string) error { return stringError(message) }

func stringField(body map[string]any, key string) string {
	value, _ := body[key].(string)
	return value
}

func asInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		if typed != float64(int(typed)) {
			return 0, false
		}
		return int(typed), true
	case int:
		return typed, true
	default:
		return 0, false
	}
}

func parseChoices(values []any) []Choice {
	choices := make([]Choice, 0, len(values))
	for _, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			continue
		}
		label, _ := item["label"].(string)
		if label == "" {
			continue
		}
		description, _ := item["description"].(string)
		choices = append(choices, Choice{Label: label, Description: description})
	}
	return choices
}

func hookString(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func hookMap(payload map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		switch typed := payload[key].(type) {
		case map[string]any:
			return typed
		case string:
			var parsed map[string]any
			if json.Unmarshal([]byte(typed), &parsed) == nil && parsed != nil {
				return parsed
			}
		}
	}
	return map[string]any{}
}

func normalizeEventName(name string) string {
	var builder strings.Builder
	for i, r := range name {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := rune(name[i-1])
			if prev >= 'a' && prev <= 'z' {
				builder.WriteByte('_')
			}
		}
		if r == '-' {
			builder.WriteByte('_')
			continue
		}
		if r >= 'A' && r <= 'Z' {
			builder.WriteRune(r + ('a' - 'A'))
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func defaultPermissionChoices() []Choice {
	return []Choice{
		{Label: "Once", Description: "Approve just this request"},
		{Label: "Always", Description: "Approve future matching requests"},
		{Label: "Reject", Description: "Deny the request"},
	}
}

func hookProjectName(directory, fallback string) string {
	if name := projectName(directory); name != "" {
		return name
	}
	if directory != "" {
		return directory
	}
	return fallback
}
