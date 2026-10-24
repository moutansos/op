package notify

import (
	"strings"
	"time"
)

func MapCopilotCLIHook(payload map[string]any) *Notification {
	if hookString(payload, "agentId", "agent_id") != "" {
		return nil
	}
	sessionID := hookString(payload, "sessionId", "session_id")
	if sessionID == "" {
		sessionID = "unknown"
	}
	directory := hookString(payload, "cwd")
	base := Notification{
		Source:           SourceCopilotCLI,
		SessionID:        sessionID,
		SessionTitle:     hookProjectName(directory, "Copilot CLI"),
		ProjectDirectory: directory,
		Timestamp:        time.Now(),
	}
	eventName := normalizeEventName(firstNonEmpty(hookString(payload, "hookEventName", "hook_event_name"), inferCopilotEvent(payload)))
	switch eventName {
	case "agentstop", "agent_stop", "stop":
		base.Type = TypeIdle
		return &base
	case "notification":
		return mapCopilotNotification(payload, base)
	case "permissionrequest", "permission_request":
		return mapCopilotPermission(payload, base)
	default:
		return nil
	}
}

func inferCopilotEvent(payload map[string]any) string {
	if hookString(payload, "notification_type", "notificationType") != "" {
		return "notification"
	}
	if hookString(payload, "toolName", "tool_name") != "" || payload["toolArgs"] != nil || payload["tool_input"] != nil || payload["toolInput"] != nil {
		return "permissionRequest"
	}
	if hookString(payload, "stopReason", "stop_reason", "transcriptPath", "transcript_path") != "" || payload["stop_hook_active"] != nil {
		return "agentStop"
	}
	return ""
}

func mapCopilotNotification(payload map[string]any, base Notification) *Notification {
	switch strings.ReplaceAll(strings.ToLower(hookString(payload, "notificationType", "notification_type")), "-", "_") {
	case "permission_prompt":
		base.Type = TypePermission
		base.PermissionTitle = firstNonEmpty(hookString(payload, "message"), hookString(payload, "title"), "Permission required")
		base.PermissionType = "permission"
		base.Choices = defaultPermissionChoices()
		return &base
	case "elicitation_dialog":
		base.Type = TypeQuestion
		base.Question = firstNonEmpty(hookString(payload, "message"), hookString(payload, "title"), "Copilot CLI is waiting for your response")
		return &base
	default:
		return nil
	}
}

func mapCopilotPermission(payload map[string]any, base Notification) *Notification {
	toolName := hookString(payload, "toolName", "tool_name")
	if toolName == "" {
		toolName = "tool"
	}
	toolInput := hookMap(payload, "toolArgs", "toolInput", "tool_input")
	if toolName == "ask_user" || toolName == "AskUserQuestion" || strings.Contains(strings.ToLower(toolName), "ask") && strings.Contains(strings.ToLower(toolName), "user") {
		fallback := firstNonEmpty(stringField(toolInput, "prompt"), stringField(toolInput, "question"), hookString(payload, "message"), hookString(payload, "title"), "Copilot CLI is waiting for your response")
		return mapAskUserQuestion(toolInput, base, fallback)
	}
	base.Type = TypePermission
	base.PermissionTitle = formatToolPermissionTitle(toolName, toolInput)
	base.PermissionType = toolName
	base.Choices = defaultPermissionChoices()
	return &base
}
