package agents

import (
	"context"
	"errors"
	"hash/fnv"
	"regexp"
	"strings"
	"time"

	"github.com/moutansos/op/internal/domain"
)

const (
	// DefaultQuietAfter is how long a pane must paint nothing before its
	// contents are trusted as a settled screen rather than a gap between frames.
	DefaultQuietAfter = 1200 * time.Millisecond
	// DefaultIdleAfter is how long an unrecognized quiet pane must stay quiet
	// before it is reported as idle instead of assumed to be mid-task.
	DefaultIdleAfter = 90 * time.Second
	// DefaultScanLines is how many trailing non-empty lines are pattern matched.
	DefaultScanLines = 24
)

// Capturer reads the visible contents of a pane. Implementations must return
// the rendered text without escape sequences.
type Capturer interface {
	CapturePane(ctx context.Context, paneID string) (string, error)
}

// Foreground identifies the leader of a pane terminal's foreground process
// group, which is the only process able to read the operator's keystrokes.
type Foreground struct {
	PID     int32
	Command string
	Args    []string
	Valid   bool
}

// Pane is one unit of observation.
type Pane struct {
	PaneID     string
	WindowName string
	RootPID    int32
	Command    string
	Dead       bool
	Foreground Foreground
}

// Options configures a Detector. Zero values fall back to the package defaults.
type Options struct {
	Definitions []Definition
	QuietAfter  time.Duration
	IdleAfter   time.Duration
	ScanLines   int
}

type paneObservation struct {
	foregroundPID int32
	digest        uint64
	changedAt     time.Time
	newScreen     bool
	// leftInitial is true once this agent run has painted something other than
	// its first screen. The first settled screen is the new-session chrome, and
	// that must not be reported as waiting.
	leftInitial bool
}

type lineMatch struct {
	line  string
	start int
	ok    bool
}

// Detector classifies agent panes across successive samples. It retains the
// previous screen digest for every pane, so a single call cannot classify
// anything beyond "starting"; callers are expected to poll.
//
// Detector is not safe for concurrent use.
type Detector struct {
	definitions []compiledDefinition
	quietAfter  time.Duration
	idleAfter   time.Duration
	scanLines   int
	observed    map[string]paneObservation
}

// New builds a detector. It fails only when a configured pattern does not
// compile, which is a configuration error worth surfacing at startup.
func New(options Options) (*Detector, error) {
	definitions := options.Definitions
	if len(definitions) == 0 {
		definitions = Builtins()
	}
	compiled, err := compile(definitions)
	if err != nil {
		return nil, err
	}
	detector := &Detector{
		definitions: compiled,
		quietAfter:  options.QuietAfter,
		idleAfter:   options.IdleAfter,
		scanLines:   options.ScanLines,
		observed:    make(map[string]paneObservation),
	}
	if detector.quietAfter <= 0 {
		detector.quietAfter = DefaultQuietAfter
	}
	if detector.idleAfter <= 0 {
		detector.idleAfter = DefaultIdleAfter
	}
	if detector.scanLines <= 0 {
		detector.scanLines = DefaultScanLines
	}
	if detector.idleAfter < detector.quietAfter {
		detector.idleAfter = detector.quietAfter
	}
	return detector, nil
}

// Match reports the agent definition that claims a pane's foreground process.
// Callers use it to avoid capturing panes that hold no agent.
func (d *Detector) Match(pane Pane) (string, bool) {
	definition, ok := d.definitionFor(pane, commandIdentities(pane.Foreground.Command, pane.Foreground.Args, pane.Command))
	return definition.name, ok
}

// Classify samples every agent-bearing pane and returns its current activity.
// Panes without a recognized agent are skipped, and panes that have disappeared
// are forgotten so a recycled pane ID cannot inherit a stale baseline.
//
// A capture failure yields an unknown classification rather than an error: a
// pane that dies mid-sample must not prevent the remaining panes from being
// reported.
func (d *Detector) Classify(ctx context.Context, now time.Time, panes []Pane, capturer Capturer) []domain.PaneAgentState {
	states := make([]domain.PaneAgentState, 0, len(panes))
	live := make(map[string]struct{}, len(panes))

	for _, pane := range panes {
		if err := ctx.Err(); err != nil {
			break
		}
		if pane.PaneID == "" {
			continue
		}
		identities := commandIdentities(pane.Foreground.Command, pane.Foreground.Args, pane.Command)
		definition, matched := d.definitionFor(pane, identities)
		if !matched {
			continue
		}
		live[pane.PaneID] = struct{}{}
		states = append(states, d.classifyPane(ctx, now, pane, definition, capturer))
	}

	for paneID := range d.observed {
		if _, ok := live[paneID]; !ok {
			delete(d.observed, paneID)
		}
	}
	return states
}

func (d *Detector) definitionFor(pane Pane, identities []string) (compiledDefinition, bool) {
	if pane.Dead {
		return compiledDefinition{}, false
	}
	for _, definition := range d.definitions {
		if definition.matches(identities) {
			return definition, true
		}
	}
	return compiledDefinition{}, false
}

func (d *Detector) classifyPane(
	ctx context.Context,
	now time.Time,
	pane Pane,
	definition compiledDefinition,
	capturer Capturer,
) domain.PaneAgentState {
	state := domain.PaneAgentState{
		PaneID:            pane.PaneID,
		WindowName:        pane.WindowName,
		AgentName:         definition.name,
		ForegroundPID:     pane.Foreground.PID,
		ForegroundCommand: pane.Foreground.Command,
		Activity:          domain.AgentActivityUnknown,
	}
	if state.ForegroundCommand == "" {
		state.ForegroundCommand = pane.Command
	}

	if capturer == nil {
		return state
	}
	screen, err := capturer.CapturePane(ctx, pane.PaneID)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return state
		}
		// The pane vanished or tmux refused the read. Drop the baseline so the
		// next successful sample starts clean instead of comparing against a
		// screen that may be arbitrarily old.
		delete(d.observed, pane.PaneID)
		return state
	}

	digest := screenDigest(screen)
	tail := trailingLines(screen, d.scanLines)
	newScreen := matchVisibleNewScreen(definition.newScreen, tail, screen)
	previous, hadPrevious := d.observed[pane.PaneID]
	// A different foreground process is a different agent run, even in the same
	// pane, so its predecessor's quiescence says nothing about it.
	if hadPrevious && previous.foregroundPID != pane.Foreground.PID {
		hadPrevious = false
	}

	changedAt := now
	leftInitial := false
	switch {
	case !hadPrevious:
		changedAt = now
	case previous.newScreen && newScreen.ok && !previous.leftInitial && previous.digest != digest:
		// Fresh-session chrome repaints during startup without meaning that the
		// operator has asked a question yet.
		changedAt = previous.changedAt
	case previous.digest != digest:
		changedAt = now
		leftInitial = previous.leftInitial || now.Sub(previous.changedAt) >= d.quietAfter
	default:
		changedAt = previous.changedAt
		leftInitial = previous.leftInitial
	}
	d.observed[pane.PaneID] = paneObservation{
		foregroundPID: pane.Foreground.PID,
		digest:        digest,
		changedAt:     changedAt,
		newScreen:     newScreen.ok,
		leftInitial:   leftInitial,
	}

	quiet := now.Sub(changedAt)
	if quiet < 0 {
		quiet = 0
	}
	state.ChangedAt = changedAt
	state.QuietSeconds = uint64(quiet / time.Second)

	state.Activity, state.Detail = classify(definition, tail, newScreen, quiet, d.quietAfter, d.idleAfter, hadPrevious, leftInitial)
	return state
}

// classify applies the decision order that keeps false "waiting" reports rare.
//
// A permission prompt or confirmation dialog is checked first because it blocks
// unconditionally, even on the frame it appears. A busy affordance is checked
// next because an agent that still offers "esc to interrupt" is by its own
// account mid-task, which overrides a screen that merely happens to be still.
// Only then does quiescence matter, and a quiet pane is reported as blocked
// solely when a prompt is recognized: a slow tool call that prints nothing is
// quiet too, and calling that "awaiting input" would be the one failure mode
// worth avoiding.
//
// The agent's first settled screen is the new-session chrome. That screen
// always has a prompt, but the operator has not been asked anything yet, so it
// is idle rather than waiting.
func classify(
	definition compiledDefinition,
	tail string,
	newScreen lineMatch,
	quiet, quietAfter, idleAfter time.Duration,
	hadBaseline bool,
	leftInitial bool,
) (domain.AgentActivity, string) {
	permission := matchLine(definition.permission, tail)
	permissionDetail := matchPermissionDetail(definition.permission, tail)
	if !permissionDetail.ok {
		permissionDetail = permission
	}
	approval := matchLine(definition.approval, tail)
	busy := matchLine(definition.busy, tail)
	prompt := matchLine(definition.prompt, tail)

	if permission.ok && permission.start > maxLineStart(prompt, newScreen) {
		return domain.AgentActivityPermissionRequired, permissionDetail.line
	}
	if approval.ok && approval.start > maxLineStart(permission, prompt, newScreen) {
		return domain.AgentActivityAwaitingApproval, approval.line
	}
	if busy.ok {
		return domain.AgentActivityWorking, busy.line
	}
	if !hadBaseline {
		return domain.AgentActivityStarting, ""
	}
	if quiet < quietAfter {
		return domain.AgentActivityWorking, ""
	}
	if newScreen.ok {
		return domain.AgentActivityIdle, newScreen.line
	}
	if prompt.ok {
		if !leftInitial {
			return domain.AgentActivityIdle, prompt.line
		}
		return domain.AgentActivityAwaitingInput, prompt.line
	}
	if quiet >= idleAfter {
		return domain.AgentActivityIdle, ""
	}
	return domain.AgentActivityWorking, ""
}

// matchVisibleNewScreen recognizes unused welcome chrome anywhere in the
// visible pane, not just near the footer. When the marker is above the trailing
// scan window, keep its start before all tail matches so current prompts,
// approvals, and permission dialogs still win precedence.
func matchVisibleNewScreen(patterns []*regexp.Regexp, tail, screen string) lineMatch {
	match := matchLine(patterns, tail)
	if match.ok {
		return match
	}
	match = matchLine(patterns, screen)
	if match.ok {
		match.start = -1
	}
	return match
}

func matchLine(patterns []*regexp.Regexp, text string) lineMatch {
	if text == "" {
		return lineMatch{}
	}
	best := lineMatch{start: -1}
	for _, pattern := range patterns {
		locations := pattern.FindAllStringIndex(text, -1)
		if len(locations) == 0 {
			continue
		}
		location := locations[len(locations)-1]
		if location[0] <= best.start {
			continue
		}
		start := strings.LastIndexByte(text[:location[0]], '\n') + 1
		end := strings.IndexByte(text[location[0]:], '\n')
		if end < 0 {
			end = len(text)
		} else {
			end += location[0]
		}
		best = lineMatch{line: strings.TrimSpace(text[start:end]), start: location[0], ok: true}
	}
	return best
}

func maxLineStart(matches ...lineMatch) int {
	best := -1
	for _, match := range matches {
		if match.ok && match.start > best {
			best = match.start
		}
	}
	return best
}

func matchPermissionDetail(patterns []*regexp.Regexp, text string) lineMatch {
	best := lineMatch{start: -1}
	for _, pattern := range patterns {
		if pattern == nil {
			continue
		}
		for _, location := range pattern.FindAllStringIndex(text, -1) {
			start := strings.LastIndexByte(text[:location[0]], '\n') + 1
			end := strings.IndexByte(text[location[0]:], '\n')
			if end < 0 {
				end = len(text)
			} else {
				end += location[0]
			}
			line := strings.TrimSpace(text[start:end])
			if strings.Contains(line, "←") {
				return lineMatch{line: line, start: location[0], ok: true}
			}
			if location[0] > best.start {
				best = lineMatch{line: line, start: location[0], ok: true}
			}
		}
	}
	return best
}

// screenDigest hashes a pane's rendered contents. Trailing whitespace and
// trailing blank lines are removed first so that cursor movement and pane
// padding, which carry no information about progress, cannot register as output.
func screenDigest(screen string) uint64 {
	hash := fnv.New64a()
	lines := strings.Split(screen, "\n")
	last := -1
	for i, line := range lines {
		if strings.TrimSpace(line) != "" {
			last = i
		}
	}
	for i := 0; i <= last; i++ {
		_, _ = hash.Write([]byte(strings.TrimRight(lines[i], " \t\r")))
		_, _ = hash.Write([]byte{'\n'})
	}
	return hash.Sum64()
}

// trailingLines returns the last count non-empty lines, preserving their order
// and the blank lines between them. Prompts and dialogs live at the bottom of a
// terminal, and scanning the whole scrollback would match stale text.
func trailingLines(screen string, count int) string {
	if count <= 0 {
		return ""
	}
	lines := strings.Split(screen, "\n")
	nonEmpty := 0
	start := 0
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			nonEmpty++
			if nonEmpty >= count {
				start = i
				break
			}
		}
	}
	return strings.Join(lines[start:], "\n")
}
