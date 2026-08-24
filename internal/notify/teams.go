package notify

import (
	"context"
	"encoding/json"
	"net/http"
)

type teamsProvider struct {
	client     *http.Client
	webhookURL string
}

func (p *teamsProvider) Type() string { return "msteams" }

func (p *teamsProvider) Send(ctx context.Context, notification Notification) error {
	name := projectName(notification.ProjectDirectory)
	if name == "" {
		name = "project"
	}
	title, status, color := teamsEvent(notification.Type, name)
	body := []any{
		map[string]any{"type": "TextBlock", "size": "Large", "weight": "Bolder", "text": title, "style": "heading", "color": color},
		map[string]any{
			"type": "FactSet",
			"facts": []any{
				map[string]string{"title": "Source", "value": notification.Source.Label()},
				map[string]string{"title": "Host", "value": displayHostname(notification.Hostname)},
				map[string]string{"title": "Project", "value": name},
				map[string]string{"title": "Session", "value": firstNonEmpty(notification.SessionTitle, notification.SessionID)},
				map[string]string{"title": "Status", "value": status},
			},
		},
	}
	if notification.Question != "" {
		body = append(body, map[string]any{"type": "TextBlock", "text": "**Question:** " + truncate(notification.Question, 500), "wrap": true, "spacing": "Medium"})
	}
	if notification.PermissionTitle != "" {
		body = append(body, map[string]any{"type": "TextBlock", "text": "**Permission:** " + truncate(notification.PermissionTitle, 500), "wrap": true, "spacing": "Medium"})
	}
	if notification.ProjectDirectory != "" {
		body = append(body, map[string]any{"type": "TextBlock", "text": notification.ProjectDirectory, "size": "Small", "isSubtle": true, "wrap": true})
	}
	content := map[string]any{
		"$schema": "http://adaptivecards.io/schemas/adaptive-card.json",
		"type":    "AdaptiveCard",
		"version": "1.4",
		"body":    body,
	}
	if notification.DesktopURL != "" {
		label := "Open session"
		if notification.Source == SourceOpenCode || notification.Source == "" {
			label = "Open in OpenCode Desktop"
		}
		content["actions"] = []any{map[string]any{"type": "Action.OpenUrl", "title": label, "url": notification.DesktopURL}}
	}
	payload, err := json.Marshal(map[string]any{
		"type": "message",
		"attachments": []any{
			map[string]any{"contentType": "application/vnd.microsoft.card.adaptive", "content": content},
		},
	})
	if err != nil {
		return err
	}
	return postJSON(ctx, p.client, p.webhookURL, nil, payload)
}

func teamsEvent(kind Type, project string) (string, string, string) {
	switch kind {
	case TypePermission:
		return "Permission Required: " + project, "Waiting for permission", "attention"
	case TypeQuestion:
		return "Question Pending: " + project, "Waiting for your response", "warning"
	default:
		return "Session Idle: " + project, "Ready for input", "default"
	}
}
