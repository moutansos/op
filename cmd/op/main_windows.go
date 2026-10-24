//go:build windows && (amd64 || arm64)

package main

import (
	"os"

	"github.com/moutansos/op/internal/wslproxy"
)

func main() {
	os.Exit(wslproxy.Run(os.Args[1:], wslproxy.NewWindowsHost(), os.Stderr))
}
