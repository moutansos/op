package notify

import (
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
	switch eventName {
	case "notification":
		return mapGrokNotification(payload, base)
	case "stop":
		if reason := hookString(payload, "reason"); reason != "" && reason != "end_turn" {
			return nil
		}
		base.Type = TypeIdle
		return &base
	case "permissiondenied", "permission_denied":
		return buildGrokPermission(payload, base, "Permission denied", true)
	default:
		return nil
	}
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
