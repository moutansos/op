//go:build windows

package serveproc

import (
	"context"
	"os"
)

// CheckFlag makes op serve validate its startup without listening.
const CheckFlag = "--check"

// NotifyRestart never delivers a request on Windows, where op serve runs in WSL.
func NotifyRestart() (<-chan os.Signal, func()) {
	return make(chan os.Signal), func() {}
}

// CheckReplacement is unsupported on Windows.
func CheckReplacement(context.Context) error { return ErrUnsupported }

// Reexec is unsupported on Windows.
func Reexec() error { return ErrUnsupported }
