package plugins

import "embed"

//go:embed all:claude-code all:grok-code all:codex all:copilot
var FS embed.FS
