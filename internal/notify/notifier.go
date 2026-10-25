package notify

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"
)

type Sender interface {
	Send(ctx context.Context, notification Notification) error
}

type Notifier struct {
	providers         []Provider
	ignoreDirectories []string
	hostname          string
	logger            *slog.Logger
	dedupeWindow      time.Duration
	now               func() time.Time
	recentMu          sync.Mutex
	recentSends       map[string]time.Time
	observerMu        sync.Mutex
	observer          func(Observation)
	observations      []observationDelivery
	observing         bool
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
	return &Notifier{
		providers:         enabled,
		ignoreDirectories: ignored,
		hostname:          localHostname(),
		logger:            logger,
		dedupeWindow:      defaultDedupeWindow,
		now:               time.Now,
		recentSends:       map[string]time.Time{},
	}
}

func (n *Notifier) Send(ctx context.Context, notification Notification) error {
	if strings.TrimSpace(notification.Hostname) == "" {
		notification.Hostname = n.hostname
	}
	if ctx.Value(observedContextKey{}) != true && notification.Hops == 0 {
		n.Observe(notificationObservation(notification, CoverageNotificationOnly))
	}
	if ignored := n.matchIgnoredDirectory(notification.ProjectDirectory); ignored != "" {
		n.logger.Info("notification suppressed", "session", notification.SessionID, "directory", ignored)
		return nil
	}
	if n.isDuplicate(notification) {
		n.logger.Info("notification suppressed", "reason", "dedupe", "type", notification.Type, "session", notification.SessionID, "host", notification.Hostname)
		return nil
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

func (n *Notifier) isDuplicate(notification Notification) bool {
	if n == nil || n.dedupeWindow <= 0 {
		return false
	}
	now := time.Now()
	if n.now != nil {
		now = n.now()
	}
	key := dedupeKey(notification)
	n.recentMu.Lock()
	defer n.recentMu.Unlock()
	if n.recentSends == nil {
		n.recentSends = map[string]time.Time{}
	}
	for existing, seen := range n.recentSends {
		if now.Sub(seen) >= n.dedupeWindow {
			delete(n.recentSends, existing)
		}
	}
	if last, ok := n.recentSends[key]; ok && now.Sub(last) < n.dedupeWindow {
		return true
	}
	n.recentSends[key] = now
	return false
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
