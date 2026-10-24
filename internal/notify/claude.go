package notify

import (
	"strings"
	"time"
)

func MapClaudeCodeHook(payload map[string]any) *Notification {
	if hookString(payload, "agent_id") != "" {
		return nil
	}
	sessionID := hookString(payload, "session_id")
	if sessionID == "" {
		sessionID = "unknown"
	}
	directory := hookString(payload, "cwd")
	base := Notification{
		Source:           SourceClaudeCode,
		SessionID:        sessionID,
		SessionTitle:     hookProjectName(directory, "Claude Code"),
		ProjectDirectory: directory,
		Timestamp:        time.Now(),
	}
	switch hookString(payload, "hook_event_name") {
	case "Notification":
		return mapClaudeNotification(payload, base)
	case "PermissionRequest":
		return mapClaudePermission(payload, base)
	case "Stop":
		base.Type = TypeIdle
		return &base
	default:
		return nil
	}
}

func mapClaudeNotification(payload map[string]any, base Notification) *Notification {
	switch hookString(payload, "notification_type") {
	case "agent_completed":
		base.Type = TypeIdle
		return &base
	case "agent_needs_input":
		base.Type = TypePermission
		base.PermissionTitle = firstNonEmpty(hookString(payload, "message"), hookString(payload, "title"), "Permission required")
		base.PermissionType = "permission"
		base.Choices = defaultPermissionChoices()
		return &base
	case "elicitation_dialog":
		base.Type = TypeQuestion
		base.Question = firstNonEmpty(hookString(payload, "message"), hookString(payload, "title"), "Claude Code is waiting for input")
		return &base
	default:
		return nil
	}
}

func mapClaudePermission(payload map[string]any, base Notification) *Notification {
	toolName := hookString(payload, "tool_name")
	if toolName == "" {
		toolName = "tool"
	}
	toolInput := hookMap(payload, "tool_input")
	if toolName == "AskUserQuestion" {
		return mapAskUserQuestion(toolInput, base, "Claude Code is waiting for your response")
	}
	base.Type = TypePermission
	base.PermissionTitle = formatClaudePermissionTitle(toolName, toolInput)
	base.PermissionType = toolName
	base.Choices = defaultPermissionChoices()
	if _, ok := payload["permission_suggestions"]; ok {
		base.Choices = defaultPermissionChoices()
	}
	return &base
}

func mapAskUserQuestion(toolInput map[string]any, base Notification, fallback string) *Notification {
	parts, choices := collectQuestionParts(toolInput)
	base.Type = TypeQuestion
	base.Question = fallback
	if len(parts) > 0 {
		base.Question = joinParagraphs(parts)
	}
	if len(choices) > 0 {
		base.Choices = choices
	}
	return &base
}

func collectQuestionParts(toolInput map[string]any) ([]string, []Choice) {
	raw, _ := toolInput["questions"].([]any)
	var parts []string
	var choices []Choice
	for _, item := range raw {
		question, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text, _ := question["question"].(string)
		if text == "" {
			text = "Question"
		}
		if header, _ := question["header"].(string); header != "" {
			parts = append(parts, header+": "+text)
		} else {
			parts = append(parts, text)
		}
		options, _ := question["options"].([]any)
		for _, option := range options {
			entry, ok := option.(map[string]any)
			if !ok {
				continue
			}
			label, _ := entry["label"].(string)
			if label == "" {
				label = "Option"
			}
			description, _ := entry["description"].(string)
			choices = append(choices, Choice{Label: label, Description: description})
		}
	}
	return parts, choices
}

func formatClaudePermissionTitle(toolName string, toolInput map[string]any) string {
	if toolName == "Bash" {
		if command, _ := toolInput["command"].(string); command != "" {
			return "Bash: " + truncate(command, 120)
		}
	}
	if toolName == "Edit" || toolName == "Write" || toolName == "Read" {
		if path, _ := toolInput["file_path"].(string); path != "" {
			return toolName + ": " + path
		}
	}
	if description, _ := toolInput["description"].(string); description != "" {
		return toolName + ": " + description
	}
	return toolName
}

func formatToolPermissionTitle(toolName string, toolInput map[string]any) string {
	command, _ := toolInput["command"].(string)
	if command != "" && (toolName == "Bash" || toolName == "bash" || toolName == "run_terminal_command" || toolName == "powershell" || strings.Contains(strings.ToLower(toolName), "shell") || strings.Contains(strings.ToLower(toolName), "terminal") || strings.Contains(strings.ToLower(toolName), "exec")) {
		return "Bash: " + truncate(command, 120)
	}
	for _, key := range []string{"file_path", "path", "target_file", "filePath"} {
		if path, _ := toolInput[key].(string); path != "" {
			return toolName + ": " + path
		}
	}
	if description, _ := toolInput["description"].(string); description != "" {
		return toolName + ": " + description
	}
	return toolName
}
