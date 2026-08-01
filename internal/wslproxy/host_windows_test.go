//go:build windows && (amd64 || arm64)

package wslproxy

import (
	"path/filepath"
	"testing"
)

func TestWindowsHostAbsolutePathMatchesFilepathAbs(t *testing.T) {
	tests := []string{
		`C:\config\op.json`,
		`C:\`,
		`C:config.json`,
		`\config\op.json`,
		`\\server\share\op.json`,
		`//server/share/op.json`,
		`config.json`,
		`config files\op.json`,
	}
	host := windowsHost{}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			want, wantErr := filepath.Abs(input)
			got, err := host.AbsolutePath(input)
			if (err != nil) != (wantErr != nil) || got != want {
				t.Fatalf("AbsolutePath(%q) = %q, %v; filepath.Abs = %q, %v", input, got, err, want, wantErr)
			}
		})
	}
}
