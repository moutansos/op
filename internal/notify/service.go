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
	OpenCode2         OpenCode2Config
	Providers         []ProviderConfig
	HTTPClient        *http.Client
	Logger            *slog.Logger
}

type Service struct {
	Notifier *Notifier
	Ingest   *Ingest
	monitor  *Monitor
	monitor2 *Monitor
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
		service.monitor = newMonitor(client, notifier, options.OpenCode.DesktopBaseURL, options.Debounce, SourceOpenCode, logger)
	}
	if options.OpenCode2.Enabled {
		client := newOpenCode2Client(options.OpenCode2, logger)
		service.monitor2 = newMonitor(client, notifier, options.OpenCode2.DesktopBaseURL, options.Debounce, SourceOpenCode2, logger)
	}
	return service, nil
}

func (s *Service) WatchOpenCode(ctx context.Context) error {
	if s == nil || s.monitor == nil {
		return nil
	}
	return s.monitor.Run(ctx)
}

func (s *Service) WatchOpenCode2(ctx context.Context) error {
	if s == nil || s.monitor2 == nil {
		return nil
	}
	return s.monitor2.Run(ctx)
}
