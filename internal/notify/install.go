package notify

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/moutansos/op/internal/notify/plugins"
)

const pluginName = "oc-notifier"

type InstallKind string

const (
	InstallClaude  InstallKind = "claude"
	InstallGrok    InstallKind = "grok"
	InstallCodex   InstallKind = "codex"
	InstallCopilot InstallKind = "copilot"
)

type InstallOptions struct {
	Kind          InstallKind
	SourceDir     string
	TargetDir     string
	HooksJSONPath string
	HomeDir       string
	CopilotHome   string
}

func DefaultPluginTarget(kind InstallKind, homeDir, copilotHome string) string {
	switch kind {
	case InstallClaude:
		return filepath.Join(homeDir, ".claude", "skills", pluginName)
	case InstallGrok:
		return filepath.Join(homeDir, ".grok", "plugins", pluginName)
	case InstallCodex:
		return filepath.Join(homeDir, ".codex", "hooks", pluginName)
	case InstallCopilot:
		if copilotHome != "" {
			return filepath.Join(copilotHome, "hooks", pluginName)
		}
		return filepath.Join(homeDir, ".copilot", "hooks", pluginName)
	default:
		return ""
	}
}

func InstallPlugin(options InstallOptions) (string, error) {
	source, err := pluginSource(options)
	if err != nil {
		return "", err
	}
	target := options.TargetDir
	if target == "" {
		target = DefaultPluginTarget(options.Kind, options.HomeDir, options.CopilotHome)
	}
	if target == "" {
		return "", fmt.Errorf("unknown plugin kind %q", options.Kind)
	}
	if err := extractPlugin(source, target); err != nil {
		return "", err
	}
	forwardPath := filepath.Join(target, "scripts", "forward.sh")
	_ = os.Chmod(forwardPath, 0o755)
	summary := fmt.Sprintf("Installed %s plugin at %s", options.Kind, target)
	switch options.Kind {
	case InstallCodex:
		hooksPath := options.HooksJSONPath
		if hooksPath == "" {
			hooksPath = filepath.Join(options.HomeDir, ".codex", "hooks.json")
		}
		extra, err := mergeCodexHooks(hooksPath, forwardPath)
		if err != nil {
			return "", err
		}
		return summary + "\n" + extra, nil
	case InstallCopilot:
		hooksPath := options.HooksJSONPath
		if hooksPath == "" {
			if options.TargetDir != "" {
				hooksPath = filepath.Join(filepath.Dir(target), pluginName+".json")
			} else if options.CopilotHome != "" {
				hooksPath = filepath.Join(options.CopilotHome, "hooks", pluginName+".json")
			} else {
				hooksPath = filepath.Join(options.HomeDir, ".copilot", "hooks", pluginName+".json")
			}
		}
		extra, err := writeCopilotHooks(hooksPath, forwardPath)
		if err != nil {
			return "", err
		}
		return summary + "\n" + extra, nil
	default:
		return summary, nil
	}
}

func pluginSource(options InstallOptions) (string, error) {
	if options.SourceDir != "" {
		return options.SourceDir, nil
	}
	switch options.Kind {
	case InstallClaude:
		return "claude-code", nil
	case InstallGrok:
		return "grok-code", nil
	case InstallCodex:
		return "codex", nil
	case InstallCopilot:
		return "copilot", nil
	default:
		return "", fmt.Errorf("unknown plugin kind %q", options.Kind)
	}
}

func extractPlugin(source, target string) error {
	if filepath.IsAbs(source) || strings.Contains(source, string(filepath.Separator)) {
		return copyTree(source, target)
	}
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return err
	}
	return fs.WalkDir(plugins.FS, source, func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(name, source)
		rel = strings.TrimPrefix(rel, "/")
		destination := target
		if rel != "" {
			destination = filepath.Join(target, filepath.FromSlash(rel))
		}
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		return copyEmbeddedFile(name, destination)
	})
}

func copyEmbeddedFile(name, destination string) error {
	source, err := plugins.FS.Open(name)
	if err != nil {
		return err
	}
	defer source.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(file, source)
	return err
}

func copyTree(source, target string) error {
	if err := os.RemoveAll(target); err != nil {
		return err
	}
	return filepath.WalkDir(source, func(current string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		data, err := os.ReadFile(current)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			return err
		}
		return os.WriteFile(destination, data, 0o644)
	})
}

func mergeCodexHooks(hooksPath, forwardPath string) (string, error) {
	existing := map[string]any{}
	if data, err := os.ReadFile(hooksPath); err == nil {
		if err := json.Unmarshal(data, &existing); err != nil {
			return "", fmt.Errorf("failed to parse existing Codex hooks file as JSON: %s", hooksPath)
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	hooks, _ := existing["hooks"].(map[string]any)
	if existing["hooks"] != nil && hooks == nil {
		return "", fmt.Errorf("invalid Codex hooks file shape: %s", hooksPath)
	}
	if hooks == nil {
		hooks = map[string]any{}
	}
	handler := map[string]any{
		"type":          "command",
		"command":       forwardPath,
		"timeout":       15,
		"statusMessage": pluginName,
	}
	var err error
	hooks["Stop"], err = upsertCodexGroup(hooks["Stop"], handler, forwardPath)
	if err != nil {
		return "", err
	}
	hooks["PermissionRequest"], err = upsertCodexGroup(hooks["PermissionRequest"], handler, forwardPath)
	if err != nil {
		return "", err
	}
	existing["hooks"] = hooks
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(hooksPath, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return "Registered Stop and PermissionRequest in " + hooksPath, nil
}

func upsertCodexGroup(raw any, handler map[string]any, forwardPath string) ([]any, error) {
	var groups []any
	if raw != nil {
		existing, ok := raw.([]any)
		if !ok {
			return nil, fmt.Errorf("expected matcher groups to be an array")
		}
		for _, item := range existing {
			group, ok := item.(map[string]any)
			if !ok {
				groups = append(groups, item)
				continue
			}
			hooks, _ := group["hooks"].([]any)
			filtered := make([]any, 0, len(hooks))
			for _, hook := range hooks {
				entry, ok := hook.(map[string]any)
				if !ok || !isOurCodexHandler(entry, forwardPath) {
					filtered = append(filtered, hook)
				}
			}
			if len(hooks) > 0 && len(filtered) == 0 {
				continue
			}
			if len(filtered) != len(hooks) {
				next := map[string]any{}
				for key, value := range group {
					next[key] = value
				}
				next["hooks"] = filtered
				groups = append(groups, next)
				continue
			}
			groups = append(groups, group)
		}
	}
	groups = append(groups, map[string]any{"hooks": []any{handler}})
	return groups, nil
}

func isOurCodexHandler(handler map[string]any, forwardPath string) bool {
	if status, _ := handler["statusMessage"].(string); status == pluginName {
		return true
	}
	command, _ := handler["command"].(string)
	if command == "" {
		return false
	}
	if filepath.Clean(command) == filepath.Clean(forwardPath) || command == forwardPath {
		return true
	}
	if strings.Contains(command, "hooks/oc-notifier/scripts/forward.sh") || strings.Contains(command, "oc-notifier/scripts/forward.sh") {
		return true
	}
	if strings.HasSuffix(command, "/scripts/forward.sh") || strings.HasSuffix(command, `\scripts\forward.sh`) {
		status, _ := handler["statusMessage"].(string)
		return status == "" || status == pluginName
	}
	return false
}

func writeCopilotHooks(hooksPath, forwardPath string) (string, error) {
	quoted := shellQuote(forwardPath)
	bashCommand := "bash " + quoted
	entry := func(matcher string) map[string]any {
		item := map[string]any{
			"type":       "command",
			"bash":       bashCommand,
			"powershell": bashCommand,
			"timeoutSec": 15,
		}
		if matcher != "" {
			item["matcher"] = matcher
		}
		return item
	}
	next := map[string]any{
		"version": 1,
		"hooks": map[string]any{
			"agentStop":    []any{entry("")},
			"notification": []any{entry("permission_prompt|elicitation_dialog")},
		},
	}
	if err := os.MkdirAll(filepath.Dir(hooksPath), 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(hooksPath, append(data, '\n'), 0o644); err != nil {
		return "", err
	}
	return "Registered agentStop and notification hooks in " + hooksPath, nil
}

func shellQuote(value string) string {
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || strings.ContainsRune("_./:-", r)) {
			return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
		}
	}
	return value
}
