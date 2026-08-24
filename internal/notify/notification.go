package notify

import (
	"encoding/base64"
	"os"
	"strings"
	"time"
)

type Type string

const (
	TypeIdle       Type = "idle"
	TypeQuestion   Type = "question"
	TypePermission Type = "permission"
)

type Source string

const (
	SourceOpenCode   Source = "opencode"
	SourceClaudeCode Source = "claude-code"
	SourceGrokCode   Source = "grok-code"
	SourceCodex      Source = "codex"
	SourceCopilotCLI Source = "copilot-cli"
)

type Choice struct {
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
}

type Notification struct {
	Type             Type      `json:"type"`
	Source           Source    `json:"source,omitempty"`
	SessionID        string    `json:"sessionId"`
	SessionTitle     string    `json:"sessionTitle"`
	ProjectID        string    `json:"projectId"`
	ProjectDirectory string    `json:"projectDirectory"`
	DesktopURL       string    `json:"desktopUrl"`
	Timestamp        time.Time `json:"timestamp"`
	Hostname         string    `json:"hostname,omitempty"`
	Hops             int       `json:"hops,omitempty"`
	Question         string    `json:"question,omitempty"`
	PermissionTitle  string    `json:"permissionTitle,omitempty"`
	PermissionType   string    `json:"permissionType,omitempty"`
	Choices          []Choice  `json:"choices,omitempty"`
}

func (s Source) Label() string {
	switch s {
	case SourceClaudeCode:
		return "Claude Code"
	case SourceGrokCode:
		return "Grok"
	case SourceCodex:
		return "Codex"
	case SourceCopilotCLI:
		return "Copilot CLI"
	default:
		return "OpenCode"
	}
}

func displayHostname(hostname string) string {
	raw := strings.TrimSpace(hostname)
	if raw == "" {
		raw, _ = os.Hostname()
	}
	if raw == "" {
		return "unknown"
	}
	if short, _, ok := strings.Cut(raw, "."); ok && short != "" {
		return short
	}
	return raw
}

func localHostname() string {
	return displayHostname("")
}

func projectName(directory string) string {
	trimmed := strings.Trim(directory, "/")
	if trimmed == "" {
		return ""
	}
	parts := strings.Split(trimmed, "/")
	return parts[len(parts)-1]
}

func DesktopURL(baseURL, directory, sessionID string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || sessionID == "" {
		return ""
	}
	encoded := base64.RawURLEncoding.EncodeToString([]byte(directory))
	return baseURL + "/" + encoded + "/session/" + sessionID
}

func normalizeDirectory(path string) string {
	return strings.TrimRight(path, "/")
}
