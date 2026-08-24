package notify

import (
	"context"
	"log/slog"
	"strings"
	"sync"
)

type Sender interface {
	Send(ctx context.Context, notification Notification) error
}

type Notifier struct {
	providers         []Provider
	ignoreDirectories []string
	hostname          string
	logger            *slog.Logger
}

func NewNotifier(providers []Provider, ignoreDirectories []string, logger *slog.Logger) *Notifier {
	enabled := make([]Provider, 0, len(providers))
	for _, provider := range providers {
		if provider != nil {
			enabled = append(enabled, provider)
		}
	}
	ignored := make([]string, 0, len(ignoreDirectories))
	for _, directory := range ignoreDirectories {
		if normalized := normalizeDirectory(directory); normalized != "" {
			ignored = append(ignored, normalized)
		}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Notifier{providers: enabled, ignoreDirectories: ignored, hostname: localHostname(), logger: logger}
}

func (n *Notifier) Send(ctx context.Context, notification Notification) error {
	if ignored := n.matchIgnoredDirectory(notification.ProjectDirectory); ignored != "" {
		n.logger.Info("notification suppressed", "session", notification.SessionID, "directory", ignored)
		return nil
	}
	if strings.TrimSpace(notification.Hostname) == "" {
		notification.Hostname = n.hostname
	}
	if len(n.providers) == 0 {
		n.logger.Info("notification dropped; no providers", "type", notification.Type, "session", notification.SessionID)
		return nil
	}
	var wait sync.WaitGroup
	for _, provider := range n.providers {
		wait.Add(1)
		go func(provider Provider) {
			defer wait.Done()
			if err := provider.Send(ctx, notification); err != nil {
				n.logger.Error("notification provider failed", "provider", provider.Type(), "err", err)
			}
		}(provider)
	}
	wait.Wait()
	return nil
}

func (n *Notifier) matchIgnoredDirectory(projectDirectory string) string {
	if projectDirectory == "" || len(n.ignoreDirectories) == 0 {
		return ""
	}
	directory := normalizeDirectory(projectDirectory)
	for _, ignored := range n.ignoreDirectories {
		prefix := ignored + "/"
		if ignored == "/" {
			prefix = "/"
		}
		if directory == ignored || strings.HasPrefix(directory, prefix) {
			return ignored
		}
	}
	return ""
}
