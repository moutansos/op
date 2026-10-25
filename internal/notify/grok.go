package notify

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var grokPermissionTypes = map[string]bool{
	"permission_prompt": true,
	"approval_required": true,
	"needs_input":       true,
	"agent_needs_input": true,
}

var grokIdleTypes = map[string]bool{
	"idle_prompt":     true,
	"idle":            true,
	"agent_completed": true,
	"turn_complete":   true,
}

var grokQuestionTypes = map[string]bool{
	"elicitation_dialog": true,
	"question":           true,
}

func MapGrokCodeHook(payload map[string]any) *Notification {
	return mapGrokCodeHook(payload, "")
}

func mapGrokCodeHook(payload map[string]any, grokHome string) *Notification {
	sessionID := hookString(payload, "sessionId", "session_id")
	if sessionID == "" {
		sessionID = "unknown"
	}
	directory := hookString(payload, "cwd", "workspaceRoot")
	base := Notification{
		Source:           SourceGrokCode,
		SessionID:        sessionID,
		SessionTitle:     hookProjectName(directory, "Grok"),
		ProjectDirectory: directory,
		Timestamp:        time.Now(),
	}
	eventName := normalizeEventName(hookString(payload, "hookEventName", "hook_event_name"))
	var notification *Notification
	switch eventName {
	case "notification":
		notification = mapGrokNotification(payload, base)
	case "stop":
		if reason := hookString(payload, "reason"); reason != "" && reason != "end_turn" {
			return nil
		}
		base.Type = TypeIdle
		notification = &base
	case "permissiondenied", "permission_denied":
		notification = buildGrokPermission(payload, base, "Permission denied", true)
	default:
		return nil
	}
	if notification == nil {
		return nil
	}
	if title := readGrokSessionTitle(notification.SessionID, notification.ProjectDirectory, grokHome); title != "" {
		notification.SessionTitle = title
	}
	return notification
}

func mapGrokNotification(payload map[string]any, base Notification) *Notification {
	msg := firstNonEmpty(hookString(payload, "message"), hookString(payload, "notificationMessage"), hookString(payload, "title"))
	notificationType := strings.ReplaceAll(strings.ToLower(hookString(payload, "notificationType", "notification_type")), "-", "_")
	if grokIdleTypes[notificationType] || strings.Contains(strings.ToLower(msg), "waiting for your input") {
		return nil
	}
	if grokPermissionTypes[notificationType] {
		fallback := msg
		if fallback == "" {
			fallback = "Permission required"
		}
		return buildGrokPermission(payload, base, fallback, false)
	}
	if grokQuestionTypes[notificationType] {
		base.Type = TypeQuestion
		base.Question = firstNonEmpty(msg, "Grok is waiting for your response")
		return &base
	}
	return nil
}

func buildGrokPermission(payload map[string]any, base Notification, fallback string, denied bool) *Notification {
	toolName := hookString(payload, "toolName", "tool_name")
	toolInput := hookMap(payload, "toolInput", "tool_input")
	msg := firstNonEmpty(hookString(payload, "message"), hookString(payload, "notificationMessage"), hookString(payload, "title"))
	title := fallback
	if toolName != "" {
		fromTool := formatToolPermissionTitle(toolName, toolInput)
		if msg != "" {
			title = fromTool + "\n" + msg
		} else {
			title = fromTool
		}
		if denied {
			title = "Denied: " + title
		}
	} else if denied && !strings.HasPrefix(strings.ToLower(title), "denied") {
		title = "Denied: " + title
	}
	base.Type = TypePermission
	base.PermissionTitle = title
	base.PermissionType = toolName
	if base.PermissionType == "" {
		if denied {
			base.PermissionType = "denied"
		} else {
			base.PermissionType = "permission"
		}
	}
	if !denied {
		base.Choices = defaultPermissionChoices()
	}
	return &base
}

var grokSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// readGrokSessionTitle reads generated_title (then session_summary) from
// $GROK_HOME/sessions/<urlencoded-cwd>/<sessionId>/summary.json. Missing or
// unreadable files leave the project-folder fallback in place.
func readGrokSessionTitle(sessionID, cwd, grokHome string) string {
	if sessionID == "" || sessionID == "unknown" || !grokSessionIDPattern.MatchString(sessionID) {
		return ""
	}
	home := resolveGrokHome(grokHome)
	if home == "" {
		return ""
	}
	sessionsRoot := filepath.Join(home, "sessions")
	trimmed := strings.TrimRight(cwd, `/\`)
	var candidates []string
	if trimmed != "" {
		candidates = append(candidates, filepath.Join(sessionsRoot, urlEncodePath(trimmed), sessionID, "summary.json"))
		if !strings.HasSuffix(cwd, "/") && !strings.HasSuffix(cwd, `\`) {
			candidates = append(candidates, filepath.Join(sessionsRoot, urlEncodePath(trimmed+"/"), sessionID, "summary.json"))
		}
	}
	for _, path := range candidates {
		if title := titleFromGrokSummary(path); title != "" {
			return title
		}
	}
	return grokTitleBySessionID(sessionsRoot, sessionID)
}

func resolveGrokHome(override string) string {
	if trimmed := strings.TrimSpace(override); trimmed != "" {
		return trimmed
	}
	if env := strings.TrimSpace(os.Getenv("GROK_HOME")); env != "" {
		return env
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".grok")
}

func urlEncodePath(value string) string {
	// Match JS encodeURIComponent for path bytes Grok uses as directory names.
	return strings.ReplaceAll(url.QueryEscape(value), "+", "%20")
}

func titleFromGrokSummary(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var payload map[string]any
	if json.Unmarshal(data, &payload) != nil {
		return ""
	}
	if generated, _ := payload["generated_title"].(string); strings.TrimSpace(generated) != "" {
		return strings.TrimSpace(generated)
	}
	if summary, _ := payload["session_summary"].(string); strings.TrimSpace(summary) != "" {
		return strings.TrimSpace(summary)
	}
	return ""
}

// grokTitleBySessionID finds summary.json when the cwd group is a hash slug
// rather than the encoded path. The session id is constrained to a safe
// pattern, so joining it cannot escape sessionsRoot.
func grokTitleBySessionID(sessionsRoot, sessionID string) string {
	entries, err := os.ReadDir(sessionsRoot)
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if title := titleFromGrokSummary(filepath.Join(sessionsRoot, entry.Name(), sessionID, "summary.json")); title != "" {
			return title
		}
	}
	return ""
}
