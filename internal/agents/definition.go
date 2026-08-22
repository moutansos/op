// Package agents classifies what interactive coding agents running inside tmux
// panes are currently doing, with particular attention to whether an agent has
// stopped and is blocked on the operator.
//
// Kernel-level signals cannot answer that question. Agents such as opencode and
// Claude Code multiplex terminal input and network sockets through a single
// event loop, so a process blocked on a human keystroke and a process blocked on
// an HTTP response present identically: sleeping, parked in ep_poll, consuming
// no CPU. The only reliable discriminator available from outside the process is
// what the agent has painted on its terminal.
//
// This package therefore classifies from two observations:
//
//   - Quiescence. A working agent repaints continuously, because every agent
//     worth the name animates a spinner or streams tokens. A pane whose visible
//     contents are byte-identical across consecutive samples is not producing
//     output.
//   - Recognition. Quiescence alone cannot separate "waiting at a prompt" from
//     "running a slow tool that prints nothing", so a quiet pane is only
//     reported as blocked when a known prompt or confirmation pattern is visible.
package agents

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// Definition describes how to recognize one agent and how to read its terminal.
//
// Pattern lists are unioned with the generic defaults in this package rather
// than replacing them, so a definition only needs to carry what is specific to
// that agent.
type Definition struct {
	// Name labels the agent in snapshots and the dashboard.
	Name string
	// Match holds command names, compared case-insensitively against the
	// foreground process comm, the basename of its argv[0], and the pane's
	// reported current command.
	Match []string
	// BusyPatterns force a Working classification when visible. They express
	// affordances an agent only renders mid-task, such as "esc to interrupt".
	BusyPatterns []string
	// PromptPatterns mark a terminal that is offering the operator an input
	// line. They are only consulted once the pane has gone quiet.
	PromptPatterns []string
	// ApprovalPatterns mark an explicit confirmation dialog. They take
	// precedence over quiescence because such a dialog is a hard block whether
	// or not it just finished rendering.
	ApprovalPatterns []string
}

type compiledDefinition struct {
	name     string
	match    map[string]struct{}
	busy     []*regexp.Regexp
	prompt   []*regexp.Regexp
	approval []*regexp.Regexp
}

// genericBusyPatterns are rendered by essentially every agent while it works.
// The braille run is the standard spinner alphabet; because a spinner advances
// between samples it also defeats quiescence on its own, so this list mainly
// guards against sampling landing on identical spinner frames.
var genericBusyPatterns = []string{
	`(?i)\besc(ape)?\s+to\s+interrupt\b`,
	`(?i)\besc\s+interrupt\b`,
	`(?i)\bctrl\+c\s+to\s+(stop|cancel|interrupt)\b`,
	`[\x{280B}\x{2819}\x{2839}\x{2838}\x{283C}\x{2834}\x{2826}\x{2827}\x{2807}\x{280F}]`,
}

// genericPromptPatterns recognize an idle input affordance. They deliberately
// require the caret to be alone on its line: a caret with text after it is
// scrollback from a command the operator already submitted.
var genericPromptPatterns = []string{
	`(?m)^\s*[\x{276f}>\x{00bb}\x{25b6}]\s*$`,
	`(?m)^\s*\x{2502}\s*[\x{276f}>]\s*\x{2502}?\s*$`,
	`(?i)\?\s+for\s+shortcuts`,
	`(?i)\btype\s+(a\s+)?(message|your\s+message)\b`,
}

// genericApprovalPatterns recognize confirmation dialogs. Agents phrase these
// inconsistently, so the list favors the shapes rather than exact wording.
var genericApprovalPatterns = []string{
	`(?i)\bdo you want to\b`,
	`(?i)\bwould you like to\b`,
	`(?i)\(\s*y(es)?\s*/\s*n(o)?\s*\)`,
	`(?i)\[\s*y\s*/\s*n\s*\]`,
	`(?i)\ballow\s+(this|the)\s+(tool|command|edit|action)\b`,
	`(?i)\bpress\s+(enter|y)\s+to\s+(confirm|approve|continue)\b`,
	`(?i)\bwaiting\s+for\s+(your\s+)?(approval|confirmation|permission)\b`,
	`(?i)\brequires?\s+(your\s+)?(approval|permission)\b`,
	`(?m)^\s*[\x{276f}>]?\s*1\.\s*yes\b`,
}

// Builtins returns the agent profiles op recognizes without configuration.
func Builtins() []Definition {
	return []Definition{
		{
			Name:           "opencode",
			Match:          []string{"opencode", "oc", "oca"},
			PromptPatterns: []string{`(?i)ctrl\+p\s+commands`},
		},
		{
			Name:  "claude",
			Match: []string{"claude", "claude-code"},
			PromptPatterns: []string{
				`(?i)shift\+tab\s+to\s+cycle`,
				`(?i)\bfor\s+shortcuts\b`,
			},
			ApprovalPatterns: []string{
				`(?i)\bdo you want to (proceed|make this edit|create)\b`,
			},
		},
		{
			Name:  "codex",
			Match: []string{"codex"},
			PromptPatterns: []string{
				`(?i)\bsend\s+message\b`,
			},
		},
		{
			Name:  "aider",
			Match: []string{"aider"},
			PromptPatterns: []string{
				`(?m)^\s*(architect|code|ask)?\s*>\s*$`,
			},
			ApprovalPatterns: []string{
				`(?i)\(Y\)es\s*/\s*\(N\)o`,
			},
		},
		{
			Name:  "gemini",
			Match: []string{"gemini"},
		},
		{
			Name:  "grok",
			Match: []string{"grok"},
		},
	}
}

func compile(definitions []Definition) ([]compiledDefinition, error) {
	compiled := make([]compiledDefinition, 0, len(definitions))
	seen := make(map[string]struct{}, len(definitions))
	for i, definition := range definitions {
		name := strings.TrimSpace(definition.Name)
		if name == "" {
			return nil, fmt.Errorf("agent definition %d: name must not be empty", i)
		}
		if _, duplicate := seen[strings.ToLower(name)]; duplicate {
			return nil, fmt.Errorf("agent definition %q: name must be unique", name)
		}
		seen[strings.ToLower(name)] = struct{}{}

		match := make(map[string]struct{}, len(definition.Match)+1)
		for _, value := range definition.Match {
			value = strings.ToLower(strings.TrimSpace(value))
			if value != "" {
				match[value] = struct{}{}
			}
		}
		if len(match) == 0 {
			match[strings.ToLower(name)] = struct{}{}
		}

		busy, err := compilePatterns(name, "busyPatterns", definition.BusyPatterns, genericBusyPatterns)
		if err != nil {
			return nil, err
		}
		prompt, err := compilePatterns(name, "promptPatterns", definition.PromptPatterns, genericPromptPatterns)
		if err != nil {
			return nil, err
		}
		approval, err := compilePatterns(name, "approvalPatterns", definition.ApprovalPatterns, genericApprovalPatterns)
		if err != nil {
			return nil, err
		}

		compiled = append(compiled, compiledDefinition{
			name:     name,
			match:    match,
			busy:     busy,
			prompt:   prompt,
			approval: approval,
		})
	}
	return compiled, nil
}

func compilePatterns(agent, field string, configured, generic []string) ([]*regexp.Regexp, error) {
	patterns := make([]*regexp.Regexp, 0, len(configured)+len(generic))
	for i, pattern := range configured {
		expression, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("agent %q %s[%d]: %w", agent, field, i, err)
		}
		patterns = append(patterns, expression)
	}
	for _, pattern := range generic {
		expression, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("agents: builtin %s %q: %w", field, pattern, err)
		}
		patterns = append(patterns, expression)
	}
	return patterns, nil
}

// matches reports whether any identity token names this agent.
func (d compiledDefinition) matches(identities []string) bool {
	for _, identity := range identities {
		if identity == "" {
			continue
		}
		if _, ok := d.match[identity]; ok {
			return true
		}
	}
	return false
}

// commandIdentities reduces the observed names for a pane's foreground process
// to the lowercase tokens a definition may match. Runtimes are unwrapped one
// level so an agent launched as "bun /usr/lib/opencode/index.js" is still
// recognized when it has not renamed its own process.
func commandIdentities(comm string, argv []string, paneCommand string) []string {
	identities := make([]string, 0, 4)
	add := func(value string) {
		value = strings.ToLower(strings.TrimSpace(value))
		value = strings.TrimSuffix(value, ".js")
		value = strings.TrimSuffix(value, ".ts")
		if value == "" {
			return
		}
		for _, existing := range identities {
			if existing == value {
				return
			}
		}
		identities = append(identities, value)
	}

	add(comm)
	if len(argv) > 0 {
		add(path.Base(strings.TrimPrefix(argv[0], "-")))
		if isRuntime(path.Base(argv[0])) {
			for _, argument := range argv[1:] {
				if strings.HasPrefix(argument, "-") {
					continue
				}
				add(path.Base(argument))
				// A generic entry-point filename names the module system, not
				// the program, so the package directory is what identifies the
				// agent in that case.
				if isGenericEntrypoint(path.Base(argument)) {
					add(path.Base(path.Dir(argument)))
				}
				break
			}
		}
	}
	add(paneCommand)
	return identities
}

var runtimes = map[string]struct{}{
	"bun": {}, "node": {}, "nodejs": {}, "deno": {}, "python": {}, "python3": {},
	"ruby": {}, "uv": {}, "uvx": {}, "npx": {}, "bunx": {},
}

func isRuntime(name string) bool {
	_, ok := runtimes[strings.ToLower(name)]
	return ok
}

var genericEntrypoints = map[string]struct{}{
	"index": {}, "main": {}, "cli": {}, "app": {}, "run": {}, "start": {}, "__main__": {},
}

func isGenericEntrypoint(name string) bool {
	name = strings.ToLower(name)
	for _, extension := range []string{".js", ".mjs", ".cjs", ".ts", ".py"} {
		name = strings.TrimSuffix(name, extension)
	}
	_, ok := genericEntrypoints[name]
	return ok
}
