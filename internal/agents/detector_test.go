package agents

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/moutansos/op/internal/domain"
)

// screenSequence returns a different screen on each capture, letting a test walk
// a pane through a scripted history.
type screenSequence struct {
	screens map[string][]string
	errs    map[string]error
	index   map[string]int
	calls   int
}

func newScreens(screens map[string][]string) *screenSequence {
	return &screenSequence{screens: screens, errs: map[string]error{}, index: map[string]int{}}
}

func (s *screenSequence) CapturePane(_ context.Context, paneID string) (string, error) {
	s.calls++
	if err, ok := s.errs[paneID]; ok {
		return "", err
	}
	frames := s.screens[paneID]
	if len(frames) == 0 {
		return "", errors.New("no screen configured for " + paneID)
	}
	position := s.index[paneID]
	if position >= len(frames) {
		position = len(frames) - 1
	}
	s.index[paneID] = position + 1
	return frames[position], nil
}

const claudeIdleScreen = `
  ⎿  Set model to Opus 5
────────────────────────────────
❯
────────────────────────────────
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← for agents
`

const opencodeBusyScreen = `
     ⠴ Thinking
     ▣  Build · Claude Opus 5
  ┃
   ⬝⬝⬝⬝⬝⬝⬝⬝  esc interrupt          32.4K (3%) · $0.43  ctrl+p commands
`

const opencodeIdleScreen = `
     ▣  Build · Claude Opus 5
  ┃
   ⬝⬝⬝⬝⬝⬝⬝⬝                         32.4K (3%) · $0.43  ctrl+p commands
`

func claudePane() Pane {
	return Pane{
		PaneID:     "%46",
		WindowName: "notifier",
		RootPID:    397633,
		Command:    "claude",
		Foreground: Foreground{PID: 399147, Command: "claude", Args: []string{"claude"}, Valid: true},
	}
}

func opencodePane() Pane {
	return Pane{
		PaneID:     "%6",
		WindowName: "op",
		RootPID:    425274,
		Command:    "opencode",
		Foreground: Foreground{PID: 539062, Command: "opencode", Args: []string{"opencode", "attach"}, Valid: true},
	}
}

func newTestDetector(t *testing.T) *Detector {
	t.Helper()
	detector, err := New(Options{QuietAfter: time.Second, IdleAfter: 30 * time.Second})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return detector
}

func TestFirstSampleCannotClassifyBeyondStarting(t *testing.T) {
	detector := newTestDetector(t)
	capturer := newScreens(map[string][]string{"%46": {claudeIdleScreen}})
	now := time.Unix(1_700_000_000, 0)

	states := detector.Classify(context.Background(), now, []Pane{claudePane()}, capturer)
	if len(states) != 1 {
		t.Fatalf("Classify() returned %d states, want 1", len(states))
	}
	if states[0].Activity != domain.AgentActivityStarting {
		t.Fatalf("first sample activity = %q, want %q", states[0].Activity, domain.AgentActivityStarting)
	}
	if states[0].AgentName != "claude" {
		t.Fatalf("agent name = %q, want claude", states[0].AgentName)
	}
}

func TestQuietPromptBecomesAwaitingInput(t *testing.T) {
	detector := newTestDetector(t)
	capturer := newScreens(map[string][]string{"%46": {claudeIdleScreen, claudeIdleScreen}})
	start := time.Unix(1_700_000_000, 0)
	pane := claudePane()

	detector.Classify(context.Background(), start, []Pane{pane}, capturer)
	states := detector.Classify(context.Background(), start.Add(5*time.Second), []Pane{pane}, capturer)

	if states[0].Activity != domain.AgentActivityAwaitingInput {
		t.Fatalf("activity = %q, want %q", states[0].Activity, domain.AgentActivityAwaitingInput)
	}
	if states[0].QuietSeconds != 5 {
		t.Fatalf("quiet seconds = %d, want 5", states[0].QuietSeconds)
	}
	if states[0].Detail == "" {
		t.Fatal("awaiting input should report the prompt line that matched")
	}
}

func TestChangingScreenStaysWorking(t *testing.T) {
	detector := newTestDetector(t)
	capturer := newScreens(map[string][]string{"%46": {claudeIdleScreen, claudeIdleScreen + "\nnew output"}})
	start := time.Unix(1_700_000_000, 0)
	pane := claudePane()

	detector.Classify(context.Background(), start, []Pane{pane}, capturer)
	states := detector.Classify(context.Background(), start.Add(30*time.Second), []Pane{pane}, capturer)

	if states[0].Activity != domain.AgentActivityWorking {
		t.Fatalf("activity = %q, want %q", states[0].Activity, domain.AgentActivityWorking)
	}
	if states[0].QuietSeconds != 0 {
		t.Fatalf("quiet seconds = %d, want 0 after new output", states[0].QuietSeconds)
	}
}

// A busy affordance must beat quiescence: sampling can land on two identical
// spinner frames, and an agent still offering "esc interrupt" is mid-task by its
// own account.
func TestBusyAffordanceOverridesQuiescence(t *testing.T) {
	detector := newTestDetector(t)
	capturer := newScreens(map[string][]string{"%6": {opencodeBusyScreen, opencodeBusyScreen}})
	start := time.Unix(1_700_000_000, 0)
	pane := opencodePane()

	detector.Classify(context.Background(), start, []Pane{pane}, capturer)
	states := detector.Classify(context.Background(), start.Add(time.Minute), []Pane{pane}, capturer)

	if states[0].Activity != domain.AgentActivityWorking {
		t.Fatalf("activity = %q, want %q", states[0].Activity, domain.AgentActivityWorking)
	}
}

func TestOpencodeQuietPromptIsAwaitingInput(t *testing.T) {
	detector := newTestDetector(t)
	capturer := newScreens(map[string][]string{"%6": {opencodeIdleScreen, opencodeIdleScreen}})
	start := time.Unix(1_700_000_000, 0)
	pane := opencodePane()

	detector.Classify(context.Background(), start, []Pane{pane}, capturer)
	states := detector.Classify(context.Background(), start.Add(4*time.Second), []Pane{pane}, capturer)

	if states[0].Activity != domain.AgentActivityAwaitingInput {
		t.Fatalf("activity = %q, want %q", states[0].Activity, domain.AgentActivityAwaitingInput)
	}
}

// An approval dialog blocks unconditionally, so it must be reported on the very
// frame it appears rather than waiting for the pane to settle.
func TestApprovalIsReportedWithoutWaitingForQuiescence(t *testing.T) {
	detector := newTestDetector(t)
	approval := "  Bash(rm -rf build)\n\n  Do you want to proceed?\n  ❯ 1. Yes\n    2. No\n"
	capturer := newScreens(map[string][]string{"%46": {claudeIdleScreen, approval}})
	start := time.Unix(1_700_000_000, 0)
	pane := claudePane()

	detector.Classify(context.Background(), start, []Pane{pane}, capturer)
	states := detector.Classify(context.Background(), start.Add(200*time.Millisecond), []Pane{pane}, capturer)

	if states[0].Activity != domain.AgentActivityAwaitingApproval {
		t.Fatalf("activity = %q, want %q", states[0].Activity, domain.AgentActivityAwaitingApproval)
	}
	if states[0].Detail != "Do you want to proceed?" {
		t.Fatalf("detail = %q, want the matched dialog line", states[0].Detail)
	}
}

// A silent tool call is quiet but not blocked. Reporting it as awaiting input
// would be the failure mode that makes the whole feature untrustworthy.
func TestQuietScreenWithoutPromptIsNotAwaitingInput(t *testing.T) {
	detector := newTestDetector(t)
	silent := "  Running tests...\n  compiling package 14 of 60\n"
	capturer := newScreens(map[string][]string{"%6": {silent, silent, silent}})
	start := time.Unix(1_700_000_000, 0)
	pane := opencodePane()

	detector.Classify(context.Background(), start, []Pane{pane}, capturer)
	working := detector.Classify(context.Background(), start.Add(10*time.Second), []Pane{pane}, capturer)
	if working[0].Activity != domain.AgentActivityWorking {
		t.Fatalf("activity after 10s = %q, want %q", working[0].Activity, domain.AgentActivityWorking)
	}

	idle := detector.Classify(context.Background(), start.Add(2*time.Minute), []Pane{pane}, capturer)
	if idle[0].Activity != domain.AgentActivityIdle {
		t.Fatalf("activity after 2m = %q, want %q", idle[0].Activity, domain.AgentActivityIdle)
	}
}

// A restarted agent inherits the pane ID but not its predecessor's history.
func TestNewForegroundProcessResetsQuiescence(t *testing.T) {
	detector := newTestDetector(t)
	capturer := newScreens(map[string][]string{"%46": {claudeIdleScreen, claudeIdleScreen}})
	start := time.Unix(1_700_000_000, 0)
	pane := claudePane()

	detector.Classify(context.Background(), start, []Pane{pane}, capturer)
	restarted := pane
	restarted.Foreground.PID = 500000
	states := detector.Classify(context.Background(), start.Add(time.Minute), []Pane{restarted}, capturer)

	if states[0].Activity != domain.AgentActivityStarting {
		t.Fatalf("activity = %q, want %q after the agent restarted", states[0].Activity, domain.AgentActivityStarting)
	}
}

func TestNonAgentPanesAreSkipped(t *testing.T) {
	detector := newTestDetector(t)
	capturer := newScreens(map[string][]string{"%5": {"~/source/repos/op  go-rewrite"}})
	shell := Pane{PaneID: "%5", Command: "zsh", Foreground: Foreground{PID: 424280, Command: "zsh", Valid: true}}

	states := detector.Classify(context.Background(), time.Unix(1_700_000_000, 0), []Pane{shell}, capturer)
	if len(states) != 0 {
		t.Fatalf("Classify() returned %d states for a shell pane, want 0", len(states))
	}
	if capturer.calls != 0 {
		t.Fatalf("capturer was called %d times for a non-agent pane, want 0", capturer.calls)
	}
}

func TestDeadPaneIsSkipped(t *testing.T) {
	detector := newTestDetector(t)
	pane := claudePane()
	pane.Dead = true
	states := detector.Classify(context.Background(), time.Unix(1_700_000_000, 0), []Pane{pane}, newScreens(nil))
	if len(states) != 0 {
		t.Fatalf("Classify() returned %d states for a dead pane, want 0", len(states))
	}
}

func TestCaptureFailureYieldsUnknownAndDropsBaseline(t *testing.T) {
	detector := newTestDetector(t)
	capturer := newScreens(map[string][]string{"%46": {claudeIdleScreen, claudeIdleScreen}})
	start := time.Unix(1_700_000_000, 0)
	pane := claudePane()

	detector.Classify(context.Background(), start, []Pane{pane}, capturer)
	capturer.errs["%46"] = errors.New("pane not found")
	states := detector.Classify(context.Background(), start.Add(time.Second), []Pane{pane}, capturer)
	if states[0].Activity != domain.AgentActivityUnknown {
		t.Fatalf("activity = %q, want %q", states[0].Activity, domain.AgentActivityUnknown)
	}

	delete(capturer.errs, "%46")
	recovered := detector.Classify(context.Background(), start.Add(time.Hour), []Pane{pane}, capturer)
	if recovered[0].Activity != domain.AgentActivityStarting {
		t.Fatalf("activity after recovery = %q, want %q", recovered[0].Activity, domain.AgentActivityStarting)
	}
}

func TestVanishedPanesAreForgotten(t *testing.T) {
	detector := newTestDetector(t)
	capturer := newScreens(map[string][]string{"%46": {claudeIdleScreen, claudeIdleScreen}})
	start := time.Unix(1_700_000_000, 0)
	pane := claudePane()

	detector.Classify(context.Background(), start, []Pane{pane}, capturer)
	detector.Classify(context.Background(), start.Add(time.Second), nil, capturer)
	if len(detector.observed) != 0 {
		t.Fatalf("detector retained %d pane observations after the pane disappeared", len(detector.observed))
	}
}

// Cursor movement and pane padding must not read as progress.
func TestTrailingWhitespaceDoesNotCountAsOutput(t *testing.T) {
	detector := newTestDetector(t)
	padded := claudeIdleScreen + "   \n\n  \n"
	capturer := newScreens(map[string][]string{"%46": {claudeIdleScreen, padded}})
	start := time.Unix(1_700_000_000, 0)
	pane := claudePane()

	detector.Classify(context.Background(), start, []Pane{pane}, capturer)
	states := detector.Classify(context.Background(), start.Add(5*time.Second), []Pane{pane}, capturer)
	if states[0].QuietSeconds != 5 {
		t.Fatalf("quiet seconds = %d, want 5 despite whitespace-only differences", states[0].QuietSeconds)
	}
}

func TestRuntimeWrappedAgentIsRecognized(t *testing.T) {
	detector := newTestDetector(t)
	pane := Pane{
		PaneID:  "%2",
		Command: "bun",
		Foreground: Foreground{
			PID:     416415,
			Command: "bun",
			Args:    []string{"/usr/bin/bun", "--smol", "/opt/opencode/index.js"},
			Valid:   true,
		},
	}
	name, ok := detector.Match(pane)
	if !ok || name != "opencode" {
		t.Fatalf("Match() = (%q, %v), want (opencode, true)", name, ok)
	}
}

func TestPaneCommandIdentifiesAgentWithoutProcfs(t *testing.T) {
	detector := newTestDetector(t)
	pane := Pane{PaneID: "%9", Command: "claude"}
	name, ok := detector.Match(pane)
	if !ok || name != "claude" {
		t.Fatalf("Match() = (%q, %v), want (claude, true)", name, ok)
	}
}

func TestInvalidPatternFailsConstruction(t *testing.T) {
	_, err := New(Options{Definitions: []Definition{{Name: "broken", PromptPatterns: []string{"("}}}})
	if err == nil {
		t.Fatal("New() with an invalid pattern should fail")
	}
}

func TestDuplicateDefinitionNameFailsConstruction(t *testing.T) {
	_, err := New(Options{Definitions: []Definition{{Name: "a"}, {Name: "A"}}})
	if err == nil {
		t.Fatal("New() with duplicate names should fail")
	}
}

func TestTrailingLinesKeepsBottomOfScreen(t *testing.T) {
	screen := "one\ntwo\nthree\nfour\nfive"
	got := trailingLines(screen, 2)
	if got != "four\nfive" {
		t.Fatalf("trailingLines() = %q, want %q", got, "four\nfive")
	}
}

// Scrollback must not trigger matches: a caret with a submitted command after it
// is history, not an offer of input.
func TestPromptPatternIgnoresSubmittedCommands(t *testing.T) {
	detector := newTestDetector(t)
	screen := "❯ run the tests\n  working on it\n  step 4 of 9\n"
	capturer := newScreens(map[string][]string{"%46": {screen, screen}})
	start := time.Unix(1_700_000_000, 0)
	pane := claudePane()

	detector.Classify(context.Background(), start, []Pane{pane}, capturer)
	states := detector.Classify(context.Background(), start.Add(5*time.Second), []Pane{pane}, capturer)
	if states[0].Activity == domain.AgentActivityAwaitingInput {
		t.Fatal("a caret followed by a submitted command must not read as an input prompt")
	}
}
