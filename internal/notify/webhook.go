package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type webhookProvider struct {
	client  *http.Client
	url     string
	method  string
	headers map[string]string
}

func (p *webhookProvider) Type() string { return "webhook" }

func (p *webhookProvider) Send(ctx context.Context, notification Notification) error {
	event := "session.idle"
	switch notification.Type {
	case TypeQuestion:
		event = "session.question"
	case TypePermission:
		event = "session.permission"
	}
	source := notification.Source
	if source == "" {
		source = SourceOpenCode
	}
	body := map[string]any{
		"event":  event,
		"source": source,
		"session": map[string]string{
			"id":    notification.SessionID,
			"title": notification.SessionTitle,
		},
		"project": map[string]string{
			"id":        notification.ProjectID,
			"directory": notification.ProjectDirectory,
		},
		"desktopUrl": notification.DesktopURL,
		"timestamp":  notification.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
	}
	if notification.Hostname != "" {
		body["hostname"] = notification.Hostname
	}
	if notification.Question != "" {
		body["question"] = notification.Question
	}
	if notification.PermissionTitle != "" {
		body["permissionTitle"] = notification.PermissionTitle
	}
	if notification.PermissionType != "" {
		body["permissionType"] = notification.PermissionType
	}
	if len(notification.Choices) > 0 {
		body["choices"] = notification.Choices
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, p.method, p.url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range p.headers {
		request.Header.Set(key, value)
	}
	response, err := p.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	text, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
	return fmt.Errorf("webhook returned %s: %s", response.Status, strings.TrimSpace(string(text)))
}
