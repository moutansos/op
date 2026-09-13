package cli

import (
	"context"
	"errors"
	"fmt"
	"syscall"
)

// The dashboard owns its server's lifetime. Binding happens before sampling or
// forwarding starts, so a standalone server can retain ownership of the port.
func (r *runner) runServingDashboard(ctx context.Context, service Service) error {
	ctx, stopSignals := r.options.Signals(ctx)
	defer stopSignals()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		err := r.serveRuntime(ctx, service, func() { close(ready) })
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
		return r.runDashboardUI(ctx, service)
	case <-ready:
	}
	uiErr := r.runDashboardUI(ctx, service)
	cancel()
	serverErr := <-done
	if serverErr != nil && errors.Is(uiErr, context.Canceled) {
		return serverErr
	}
	return errors.Join(uiErr, serverErr)
}
