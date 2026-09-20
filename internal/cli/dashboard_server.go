package cli

import (
	"context"
	"errors"
	"fmt"
	"syscall"

	"github.com/moutansos/op/internal/state"
)

// The dashboard owns its server's lifetime. Binding happens before sampling or
// forwarding starts, so a standalone server can retain ownership of the port.
func (r *runner) runServingDashboard(ctx context.Context, service Service) error {
	ctx, stopSignals := r.options.Signals(ctx)
	defer stopSignals()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := make(chan *state.Tracker, 1)
	done := make(chan error, 1)
	go func() {
		err := r.serveRuntime(ctx, service, func(tracker *state.Tracker) { ready <- tracker })
		done <- err
		if !errors.Is(err, syscall.EADDRINUSE) {
			cancel()
		}
	}()
	select {
	case err := <-done:
		if !errors.Is(err, syscall.EADDRINUSE) {
			return err
		}
		fmt.Fprintf(r.options.Stderr, "API address %s is already in use; dashboard running with local API hosting disabled.\n", r.config.Server.Listen)
		return r.runDashboardUI(ctx, service, nil)
	case tracker := <-ready:
		uiErr := r.runDashboardUI(ctx, service, tracker)
		cancel()
		serverErr := <-done
		if serverErr != nil && errors.Is(uiErr, context.Canceled) {
			return serverErr
		}
		return errors.Join(uiErr, serverErr)
	}
}

func (r *runner) runParentDashboard(ctx context.Context, service Service) error {
	ctx, stopSignals := r.options.Signals(ctx)
	defer stopSignals()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	tracker, notifyService, err := r.newStateTracker(service)
	if err != nil {
		return err
	}
	done := make(chan struct{})
	go func() { defer close(done); r.runParentWork(ctx, tracker, notifyService) }()
	defer func() { cancel(); <-done }()
	return r.runDashboardUI(ctx, service, tracker)
}
