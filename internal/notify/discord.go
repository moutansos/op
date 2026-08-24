package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

var harnessAvatarURL = map[Source]string{
	SourceClaudeCode: "https://www.google.com/s2/favicons?domain=claude.ai&sz=128",
	SourceGrokCode:   "https://www.google.com/s2/favicons?domain=grok.com&sz=128",
	SourceCodex:      "https://www.google.com/s2/favicons?domain=openai.com&sz=128",
	SourceCopilotCLI: "https://www.google.com/s2/favicons?domain=github.com&sz=128",
	SourceOpenCode:   "https://www.google.com/s2/favicons?domain=opencode.ai&sz=128",
}

type discordProvider struct {
	client     *http.Client
	webhookURL string
}

func (p *discordProvider) Type() string { return "discord" }

func (p *discordProvider) Send(ctx context.Context, notification Notification) error {
	source := notification.Source
	if source == "" {
		source = SourceOpenCode
	}
	name := projectName(notification.ProjectDirectory)
	if name == "" {
		name = "project"
	}
	identity := source.Label() + " · " + displayHostname(notification.Hostname)
	eventLabel, status, color := discordEvent(notification.Type)
	fields := []map[string]any{
		{"name": "Event", "value": eventLabel, "inline": true},
		{"name": "Project", "value": name, "inline": true},
		{"name": "Session", "value": firstNonEmpty(notification.SessionTitle, notification.SessionID), "inline": true},
		{"name": "Status", "value": status, "inline": true},
	}
	if notification.Question != "" {
		fields = append(fields, map[string]any{"name": "Question", "value": truncate(notification.Question, 1024), "inline": false})
	}
	if notification.PermissionTitle != "" {
		fields = append(fields, map[string]any{"name": "Permission", "value": truncate(notification.PermissionTitle, 1024), "inline": false})
	}
	if len(notification.Choices) > 0 {
		label := "Options"
		if notification.Type == TypePermission {
			label = "Approval Options"
		}
		fields = append(fields, map[string]any{"name": label, "value": truncate(formatChoices(notification.Choices), 1024), "inline": false})
	}
	footer := notification.ProjectDirectory
	if footer == "" {
		footer = identity
	}
	embed := map[string]any{
		"title":     identity,
		"color":     color,
		"fields":    fields,
		"timestamp": notification.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		"footer":    map[string]any{"text": footer},
	}
	if notification.DesktopURL != "" {
		embed["url"] = notification.DesktopURL
	}
	body := map[string]any{
		"username":   identity,
		"avatar_url": harnessAvatarURL[source],
		"embeds":     []any{embed},
	}
	if notification.DesktopURL != "" {
		label := "Open session"
		if source == SourceOpenCode {
			label = "Open in OpenCode Desktop"
		}
		body["components"] = []any{
			map[string]any{
				"type": 1,
				"components": []any{
					map[string]any{"type": 2, "style": 5, "label": label, "url": notification.DesktopURL},
				},
			},
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return postJSON(ctx, p.client, p.webhookURL, nil, payload)
}

func discordEvent(kind Type) (string, string, int) {
	switch kind {
	case TypePermission:
		return "Permission required", "Waiting for permission", 0xed4245
	case TypeQuestion:
		return "Question pending", "Waiting for your response", 0xffa500
	default:
		return "Session idle", "Ready for input", 0x5865f2
	}
}

func formatChoices(choices []Choice) string {
	lines := make([]string, 0, len(choices))
	for _, choice := range choices {
		line := "• **" + choice.Label + "**"
		if choice.Description != "" {
			line += " — " + choice.Description
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
