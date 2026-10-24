package wslproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
)

// Settings are the op.exe options that must be known before WSL is entered,
// so they cannot live in the Linux op configuration.
type Settings struct {
	Distro string
	Op     string
}

// resolveSettings reads the optional settings file and applies the
// OP_WSL_DISTRO and OP_WSL_OP environment variables over it.
func resolveSettings(host Host) (Settings, []string, error) {
	file, warnings, err := readSettingsFile(host)
	if err != nil {
		return Settings{}, nil, err
	}
	settings := file.settings
	opSource := file.path + ": op"
	if value, _ := host.LookupEnv("OP_WSL_DISTRO"); value != "" {
		settings.Distro = value
	}
	if value, _ := host.LookupEnv("OP_WSL_OP"); value != "" {
		settings.Op = value
		opSource = "OP_WSL_OP"
	}
	if settings.Op != "" && !path.IsAbs(settings.Op) {
		return Settings{}, nil, fmt.Errorf("%s must be an absolute Linux path, got %q", opSource, settings.Op)
	}
	return settings, warnings, nil
}

type settingsFile struct {
	path     string
	settings Settings
}

func readSettingsFile(host Host) (settingsFile, []string, error) {
	filename, err := host.SettingsPath()
	if err != nil || filename == "" {
		return settingsFile{}, nil, nil
	}
	result := settingsFile{path: filename}
	data, err := host.ReadFile(filename)
	if errors.Is(err, fs.ErrNotExist) {
		return result, nil, nil
	}
	if err != nil {
		return settingsFile{}, nil, fmt.Errorf("read %s: %w", filename, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil || fields == nil {
		if err == nil {
			err = errors.New("must be a JSON object")
		}
		return settingsFile{}, nil, fmt.Errorf("parse %s: %w", filename, err)
	}
	targets := map[string]*string{"distro": &result.settings.Distro, "op": &result.settings.Op}
	var warnings []string
	for name, raw := range fields {
		target, known := targets[name]
		if !known {
			warnings = append(warnings, fmt.Sprintf("%s: unknown field %q is ignored", filename, name))
			continue
		}
		if err := json.Unmarshal(raw, target); err != nil {
			return settingsFile{}, nil, fmt.Errorf("parse %s: %q must be a string", filename, name)
		}
	}
	sort.Strings(warnings)
	return result, warnings, nil
}
