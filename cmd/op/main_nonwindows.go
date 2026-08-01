//go:build !windows

package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/moutansos/op/internal/cli"
	"github.com/moutansos/op/internal/wslproxy"
)

// Set with: go build -ldflags "-X main.version=... -X main.commit=... -X main.date=..."
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	wslproxy.KeepLinuxBinaryMarker()
	ctx, stop := rootContext(context.Background())
	code := cli.Run(ctx, os.Args[1:], cli.Options{
		Version: cli.Version{Version: version, Commit: commit, Date: date},
	})
	stop()
	os.Exit(code)
}

func rootContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, shutdownSignals()...)
}
