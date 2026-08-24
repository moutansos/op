package notify

import (
	"context"
	"log/slog"
	"net/http"
	"time"
)

type Options struct {
	Debounce          time.Duration
	IgnoreDirectories []string
	OpenCode          OpenCodeConfig
	Providers         []ProviderConfig
	HTTPClient        *http.Client
	Logger            *slog.Logger
}

type Service struct {
	Notifier *Notifier
	Ingest   *Ingest
	monitor  *Monitor
	logger   *slog.Logger
}

func New(options Options) (*Service, error) {
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	providers, err := NewProviders(options.Providers, options.HTTPClient)
	if err != nil {
		return nil, err
	}
	notifier := NewNotifier(providers, options.IgnoreDirectories, logger)
	service := &Service{
		Notifier: notifier,
		Ingest:   NewIngest(notifier, logger),
		logger:   logger,
	}
	if options.OpenCode.BaseURL != "" {
		client := newOpenCodeClient(options.OpenCode, logger)
		service.monitor = newMonitor(client, notifier, options.OpenCode.DesktopBaseURL, options.Debounce, logger)
	}
	return service, nil
}

func (s *Service) WatchOpenCode(ctx context.Context) error {
	if s == nil || s.monitor == nil {
		return nil
	}
	return s.monitor.Run(ctx)
}
