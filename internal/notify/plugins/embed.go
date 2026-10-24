package plugins

import "embed"

//go:embed claude-code grok-code codex copilot
var FS embed.FS
