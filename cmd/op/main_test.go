//go:build !windows

package main

import (
	"context"
	"testing"
	"time"
)

func TestRootContextInheritsParentCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	ctx, stop := rootContext(parent)
	defer stop()

	cancelParent()
	select {
	case <-ctx.Done():
		if err := ctx.Err(); err != context.Canceled {
			t.Fatalf("root context error = %v, want canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("root context did not inherit parent cancellation")
	}
}
