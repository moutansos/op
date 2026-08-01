//go:build !windows

package main

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestRootContextCanceledBySIGTERM(t *testing.T) {
	ctx, stop := rootContext(context.Background())
	defer stop()

	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("send SIGTERM: %v", err)
	}
	select {
	case <-ctx.Done():
		if err := ctx.Err(); err != context.Canceled {
			t.Fatalf("root context error = %v, want canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SIGTERM did not cancel root context")
	}
}
