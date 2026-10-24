package notify

import "time"

func MapCodexHook(payload map[string]any) *Notification {
	if hookString(payload, "agent_id") != "" {
		return nil
	}
	sessionID := hookString(payload, "session_id")
	if sessionID == "" {
		sessionID = "unknown"
	}
	directory := hookString(payload, "cwd")
	base := Notification{
		Source:           SourceCodex,
		SessionID:        sessionID,
		SessionTitle:     hookProjectName(directory, "Codex"),
		ProjectDirectory: directory,
		Timestamp:        time.Now(),
	}
	switch normalizeEventName(hookString(payload, "hook_event_name")) {
	case "stop":
		base.Type = TypeIdle
		return &base
	case "permissionrequest", "permission_request":
		toolName := hookString(payload, "tool_name")
		if toolName == "" {
			toolName = "tool"
		}
		toolInput := hookMap(payload, "tool_input")
		if toolName == "apply_patch" || toolName == "Edit" || toolName == "Write" {
			if description, _ := toolInput["description"].(string); description != "" {
				base.Type = TypePermission
				base.PermissionTitle = toolName + ": " + description
				base.PermissionType = toolName
				base.Choices = defaultPermissionChoices()
				return &base
			}
			if _, ok := toolInput["command"].(string); ok {
				base.Type = TypePermission
				base.PermissionTitle = toolName + " (file edit)"
				base.PermissionType = toolName
				base.Choices = defaultPermissionChoices()
				return &base
			}
		}
		base.Type = TypePermission
		base.PermissionTitle = formatToolPermissionTitle(toolName, toolInput)
		base.PermissionType = toolName
		base.Choices = defaultPermissionChoices()
		return &base
	default:
		return nil
	}
}
