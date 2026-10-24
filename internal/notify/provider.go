package notify

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Provider interface {
	Type() string
	Send(ctx context.Context, notification Notification) error
}

type ProviderConfig struct {
	Type       string
	WebhookURL string
	URL        string
	Method     string
	Headers    map[string]string
	Token      string
	MaxHops    int
	Timeout    time.Duration
}

func NewProviders(configs []ProviderConfig, client *http.Client) ([]Provider, error) {
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	providers := make([]Provider, 0, len(configs))
	for i, config := range configs {
		provider, err := newProvider(config, client)
		if err != nil {
			return nil, fmt.Errorf("notifications.providers[%d]: %w", i, err)
		}
		if provider != nil {
			providers = append(providers, provider)
		}
	}
	return providers, nil
}

func newProvider(config ProviderConfig, client *http.Client) (Provider, error) {
	switch config.Type {
	case "discord":
		return &discordProvider{client: client, webhookURL: config.WebhookURL}, nil
	case "msteams":
		return &teamsProvider{client: client, webhookURL: config.WebhookURL}, nil
	case "webhook":
		method := strings.ToUpper(strings.TrimSpace(config.Method))
		if method == "" {
			method = http.MethodPost
		}
		return &webhookProvider{client: client, url: config.URL, method: method, headers: config.Headers}, nil
	case "parent":
		maxHops := config.MaxHops
		if maxHops == 0 {
			maxHops = defaultParentMaxHops
		}
		timeout := config.Timeout
		if timeout == 0 {
			timeout = defaultParentTimeout
		}
		return &parentProvider{
			client:    client,
			notifyURL: joinNotifyURL(config.URL),
			token:     config.Token,
			maxHops:   maxHops,
			timeout:   timeout,
		}, nil
	default:
		return nil, fmt.Errorf("unknown provider type %q", config.Type)
	}
}

func postJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return nil
	}
	limited := io.LimitReader(response.Body, 2048)
	text, _ := io.ReadAll(limited)
	return fmt.Errorf("webhook returned %s: %s", response.Status, strings.TrimSpace(string(text)))
}

func truncate(value string, max int) string {
	if len(value) <= max {
		return value
	}
	if max <= 3 {
		return value[:max]
	}
	return value[:max-3] + "..."
}
