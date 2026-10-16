package wslproxy

import (
	"os"
	"strings"
)

var tmuxVariables = []string{"TMUX", "TMUX_PANE"}

// ExportTmuxToWindows lets Windows programs started inside tmux under WSL,
// such as a pwsh.exe preferred shell, see TMUX and TMUX_PANE. op.exe forwards
// them back into WSL, so op behaves the same from Linux and Windows shells.
// It must run before op starts a tmux server, which inherits the environment.
func ExportTmuxToWindows() {
	if os.Getenv("WSL_DISTRO_NAME") == "" && os.Getenv("WSL_INTEROP") == "" {
		return
	}
	_ = os.Setenv("WSLENV", bidirectionalWSLEnv(os.Getenv("WSLENV"), tmuxVariables))
}

// bidirectionalWSLEnv replaces any existing entries for names with entries
// that carry no direction flag and preserves every other entry.
func bidirectionalWSLEnv(value string, names []string) string {
	entries := make([]string, 0)
	for _, entry := range strings.Split(value, ":") {
		if entry == "" {
			continue
		}
		name := strings.SplitN(entry, "/", 2)[0]
		owned := false
		for _, candidate := range names {
			if strings.EqualFold(name, candidate) {
				owned = true
				break
			}
		}
		if !owned {
			entries = append(entries, entry)
		}
	}
	return strings.Join(append(entries, names...), ":")
}
