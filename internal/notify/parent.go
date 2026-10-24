package notify

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	defaultParentMaxHops = 8
	defaultParentTimeout = 10 * time.Second
)

type parentProvider struct {
	client    *http.Client
	notifyURL string
	token     string
	maxHops   int
	timeout   time.Duration
}

func (p *parentProvider) Type() string { return "parent" }

func (p *parentProvider) Send(ctx context.Context, notification Notification) error {
	if notification.Hops >= p.maxHops {
		return fmt.Errorf("parent forward hop limit exceeded (%d >= %d)", notification.Hops, p.maxHops)
	}
	source := notification.Source
	if source == "" {
		source = SourceOpenCode
	}
	body := map[string]any{
		"type":             notification.Type,
		"source":           source,
		"sessionId":        notification.SessionID,
		"sessionTitle":     notification.SessionTitle,
		"projectId":        notification.ProjectID,
		"projectDirectory": notification.ProjectDirectory,
		"desktopUrl":       notification.DesktopURL,
		"timestamp":        notification.Timestamp.UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		"hops":             notification.Hops + 1,
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
	headers := map[string]string{}
	if p.token != "" {
		headers["Authorization"] = "Bearer " + p.token
	}
	sendCtx := ctx
	cancel := func() {}
	if p.timeout > 0 {
		sendCtx, cancel = context.WithTimeout(ctx, p.timeout)
	}
	defer cancel()
	return postJSON(sendCtx, p.client, p.notifyURL, headers, payload)
}

func joinNotifyURL(raw string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(raw), "/")
	if strings.HasSuffix(trimmed, "/v1/notify") {
		return trimmed
	}
	return trimmed + "/v1/notify"
}
