package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestDesktopURLMatchesOpenCodeRoute(t *testing.T) {
	got := DesktopURL("https://opencode.example.com/", "/home/ben/source/repos/op", "ses_1")
	want := "https://opencode.example.com/L2hvbWUvYmVuL3NvdXJjZS9yZXBvcy9vcA/session/ses_1"
	if got != want {
		t.Fatalf("DesktopURL() = %q, want %q", got, want)
	}
	if DesktopURL("", "/tmp", "ses_1") != "" {
		t.Fatal("empty base URL should produce no desktop link")
	}
	server := DesktopURLForServer("https://beta.opencode.ai/", "https://oc2.example.com/", "ses_abc")
	encoded := "aHR0cHM6Ly9vYzIuZXhhbXBsZS5jb20"
	if server != "https://beta.opencode.ai/server/"+encoded+"/session/ses_abc" {
		t.Fatalf("DesktopURLForServer() = %q", server)
	}
	if got := normalizeServerURL("oc2.example.com/"); got != "http://oc2.example.com" {
		t.Fatalf("normalizeServerURL() = %q", got)
	}
}

func TestNotifierIgnoresConfiguredDirectories(t *testing.T) {
	var sent []Notification
	notifier := NewNotifier([]Provider{recordingProvider{send: func(n Notification) { sent = append(sent, n) }}}, []string{"/tmp"}, nil)
	if err := notifier.Send(context.Background(), Notification{Type: TypeIdle, SessionID: "s1", ProjectDirectory: "/tmp/probe"}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 0 {
		t.Fatalf("ignored directory still notified: %+v", sent)
	}
	if err := notifier.Send(context.Background(), Notification{Type: TypeIdle, SessionID: "s2", ProjectDirectory: "/home/ben/op"}); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 1 || sent[0].Hostname == "" {
		t.Fatalf("expected stamped notification, got %+v", sent)
	}
}

func TestParseNotifyBodyPreservesChildIdentity(t *testing.T) {
	notification, err := ParseNotifyBody(map[string]any{
		"type":             "question",
		"source":           "claude-code",
		"sessionId":        "abc",
		"sessionTitle":     "repo",
		"projectDirectory": "/repos/repo",
		"hostname":         "devbox",
		"hops":             float64(2),
		"timestamp":        "2026-02-04T12:00:00Z",
		"question":         "Ship it?",
		"choices":          []any{map[string]any{"label": "Yes"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if notification.Source != SourceClaudeCode || notification.Hostname != "devbox" || notification.Hops != 2 || notification.Question != "Ship it?" {
		t.Fatalf("parsed notification = %+v", notification)
	}
	if notification.Timestamp.UTC().Format(time.RFC3339) != "2026-02-04T12:00:00Z" {
		t.Fatalf("timestamp = %s", notification.Timestamp)
	}
}

func TestJoinNotifyURL(t *testing.T) {
	if got := joinNotifyURL("http://parent:4100"); got != "http://parent:4100/v1/notify" {
		t.Fatalf("joinNotifyURL(base) = %q", got)
	}
	if got := joinNotifyURL("http://parent:4100/v1/notify/"); got != "http://parent:4100/v1/notify" {
		t.Fatalf("joinNotifyURL(full) = %q", got)
	}
}

func TestParentProviderRespectsHopLimit(t *testing.T) {
	provider := &parentProvider{maxHops: 2, notifyURL: "http://parent/v1/notify"}
	err := provider.Send(context.Background(), Notification{Type: TypeIdle, SessionID: "s", Hops: 2})
	if err == nil || !strings.Contains(err.Error(), "hop limit") {
		t.Fatalf("error = %v, want hop limit", err)
	}
}

func TestWebhookProviderPostsNormalizedPayload(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Token") != "secret" {
			t.Errorf("missing custom header")
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	provider := &webhookProvider{client: server.Client(), url: server.URL, method: http.MethodPost, headers: map[string]string{"X-Token": "secret"}}
	err := provider.Send(context.Background(), Notification{
		Type: TypeIdle, Source: SourceOpenCode, SessionID: "s1", SessionTitle: "title",
		ProjectDirectory: "/repos/op", Timestamp: time.Unix(1_700_000_000, 0).UTC(), Hostname: "box",
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["event"] != "session.idle" || body["hostname"] != "box" {
		t.Fatalf("webhook body = %#v", body)
	}
}

func TestSSEParserEmitsDataEvents(t *testing.T) {
	var events []string
	err := readSSE(strings.NewReader("data: {\"a\":1}\n\ndata: {\"b\":2}\n\n"), func(data string) {
		events = append(events, data)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0] != `{"a":1}` || events[1] != `{"b":2}` {
		t.Fatalf("events = %#v", events)
	}
}

func TestParseSessionStatusAndQuestion(t *testing.T) {
	status, ok := parseSessionStatus([]byte(`{"type":"session.status","properties":{"sessionID":"s1","status":{"type":"idle"}}}`))
	if !ok || status.SessionID != "s1" || status.Status != "idle" {
		t.Fatalf("status = %+v ok=%v", status, ok)
	}
	question, ok := parseQuestionAsked([]byte(`{"type":"question.asked","properties":{"id":"q1","sessionID":"s1","questions":[{"question":"Ready?","options":[{"label":"Yes"}]}]}}`))
	if !ok || question.ID != "q1" || question.Questions[0].Question != "Ready?" {
		t.Fatalf("question = %+v ok=%v", question, ok)
	}
}

func TestMonitorNotifiesOnBusyToIdle(t *testing.T) {
	var mu sync.Mutex
	var sent []Notification
	monitor := newMonitor(&openCodeClient{client: &http.Client{}}, recordingSender{send: func(n Notification) {
		mu.Lock()
		sent = append(sent, n)
		mu.Unlock()
	}}, "https://desktop.example", 0, SourceOpenCode, nil)
	monitor.handleStatus(context.Background(), "/repos/op", sessionStatusEvent{SessionID: "s1", Status: "busy"})
	monitor.handleStatus(context.Background(), "/repos/op", sessionStatusEvent{SessionID: "s1", Status: "idle"})
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		count := len(sent)
		mu.Unlock()
		if count == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || sent[0].Type != TypeIdle || sent[0].DesktopURL == "" {
		t.Fatalf("sent = %+v", sent)
	}
}

func TestMonitorSkipsInitialIdle(t *testing.T) {
	var sent []Notification
	monitor := newMonitor(&openCodeClient{client: &http.Client{}}, recordingSender{send: func(n Notification) {
		sent = append(sent, n)
	}}, "", 0, SourceOpenCode, nil)
	monitor.handleStatus(context.Background(), "/repos/op", sessionStatusEvent{SessionID: "s1", Status: "idle"})
	time.Sleep(30 * time.Millisecond)
	if len(sent) != 0 {
		t.Fatalf("initial idle notified: %+v", sent)
	}
}

func TestClaudeAndCodexHookMapping(t *testing.T) {
	if got := MapClaudeCodeHook(map[string]any{"hook_event_name": "Stop", "session_id": "s", "cwd": "/repos/op"}); got == nil || got.Type != TypeIdle || got.Source != SourceClaudeCode {
		t.Fatalf("claude stop = %+v", got)
	}
	if got := MapClaudeCodeHook(map[string]any{"hook_event_name": "Stop", "session_id": "s", "agent_id": "sub"}); got != nil {
		t.Fatalf("subagent stop should be ignored: %+v", got)
	}
	if got := MapClaudeCodeHook(map[string]any{"hook_event_name": "Notification", "notification_type": "idle_prompt", "session_id": "s"}); got != nil {
		t.Fatalf("idle_prompt should be ignored to avoid double notify: %+v", got)
	}
	if got := MapCodexHook(map[string]any{"hook_event_name": "PermissionRequest", "session_id": "s", "tool_name": "Bash", "tool_input": map[string]any{"command": "ls"}}); got == nil || got.Type != TypePermission || !strings.Contains(got.PermissionTitle, "ls") {
		t.Fatalf("codex permission = %+v", got)
	}
	if got := MapGrokCodeHook(map[string]any{"hookEventName": "Stop", "reason": "channel_closed", "sessionId": "s"}); got != nil {
		t.Fatalf("grok shutdown stop should be ignored: %+v", got)
	}
	if got := MapCopilotCLIHook(map[string]any{"notificationType": "permission_prompt", "sessionId": "s", "message": "Allow?"}); got == nil || got.Type != TypePermission {
		t.Fatalf("copilot permission = %+v", got)
	}
}

func TestInstallClaudePluginExtractsForwarder(t *testing.T) {
	home := t.TempDir()
	summary, err := InstallPlugin(InstallOptions{Kind: InstallClaude, HomeDir: home})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(summary, home) {
		t.Fatalf("summary = %q", summary)
	}
	forward := filepath.Join(home, ".claude", "skills", "oc-notifier", "scripts", "forward.sh")
	data, err := os.ReadFile(forward)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "127.0.0.1:8787") {
		t.Fatalf("forwarder still points at the old default: %s", data)
	}
}

func TestInstallCodexPluginMergesHooks(t *testing.T) {
	home := t.TempDir()
	hooks := filepath.Join(home, ".codex", "hooks.json")
	if err := os.MkdirAll(filepath.Dir(hooks), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hooks, []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := InstallPlugin(InstallOptions{Kind: InstallCodex, HomeDir: home}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(hooks)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "echo keep") || !strings.Contains(string(data), "oc-notifier") {
		t.Fatalf("merged hooks = %s", data)
	}
}

type recordingProvider struct {
	send func(Notification)
}

func (r recordingProvider) Type() string { return "test" }

func (r recordingProvider) Send(_ context.Context, notification Notification) error {
	r.send(notification)
	return nil
}

type recordingSender struct {
	send func(Notification)
}

func (r recordingSender) Send(_ context.Context, notification Notification) error {
	r.send(notification)
	return nil
}

func TestTranslateV2Events(t *testing.T) {
	directory, payload, ok := translateV2Event([]byte(`{"type":"session.execution.started","data":{"sessionID":"ses_1"}}`))
	if !ok || directory != "" {
		t.Fatalf("started ok=%v directory=%q", ok, directory)
	}
	status, ok := parseSessionStatus(payload)
	if !ok || status.SessionID != "ses_1" || status.Status != "busy" {
		t.Fatalf("started status = %+v ok=%v", status, ok)
	}

	_, payload, ok = translateV2Event([]byte(`{"type":"session.execution.succeeded","data":{"sessionID":"ses_1"},"location":{"directory":"/repos/op"}}`))
	if !ok {
		t.Fatal("succeeded not translated")
	}
	status, ok = parseSessionStatus(payload)
	if !ok || status.Status != "idle" {
		t.Fatalf("succeeded status = %+v ok=%v", status, ok)
	}

	form := `{"type":"form.created","data":{"form":{"id":"frm_1","sessionID":"ses_1","title":"Questions","metadata":{"kind":"question","tool":{"id":"call_1"}},"fields":[{"key":"q0","type":"string","title":"Fix approach","description":"How should I fix it?","custom":true,"options":[{"value":"minimal","label":"","description":"Smallest diff"}]}]}},"location":{"directory":"/repos/op"}}`
	directory, payload, ok = translateV2Event([]byte(form))
	if !ok || directory != "/repos/op" {
		t.Fatalf("form ok=%v directory=%q", ok, directory)
	}
	question, ok := parseQuestionAsked(payload)
	if !ok || question.ID != "frm_1" || question.CallID != "call_1" {
		t.Fatalf("form question = %+v ok=%v", question, ok)
	}
	if question.Questions[0].Header != "Fix approach" || question.Questions[0].Question != "How should I fix it?" {
		t.Fatalf("question text = %+v", question.Questions[0])
	}
	if question.Questions[0].Options[0].Label != "minimal" {
		t.Fatalf("option label = %+v", question.Questions[0].Options)
	}

	other, _, ok := translateV2Event([]byte(`{"type":"form.created","data":{"form":{"id":"frm_2","sessionID":"ses_1","metadata":{"kind":"elicitation"},"fields":[{"title":"Name"}]}}}`))
	if ok || other != "" {
		t.Fatal("non-question forms should be ignored")
	}

	_, payload, ok = translateV2Event([]byte(`{"type":"permission.asked","data":{"id":"per_1","sessionID":"ses_1","action":"bash","resources":["rm -rf /tmp/x"],"source":{"id":"call_9"}}}`))
	if !ok {
		t.Fatal("permission not translated")
	}
	permission, ok := parsePermissionAsked(payload)
	if !ok || permission.ID != "per_1" || permission.PermissionType != "bash" || permission.CallID != "call_9" {
		t.Fatalf("permission = %+v ok=%v", permission, ok)
	}
	if len(permission.AlwaysPatterns) != 1 || permission.AlwaysPatterns[0] != "rm -rf /tmp/x" {
		t.Fatalf("always patterns = %#v", permission.AlwaysPatterns)
	}

	_, payload, ok = translateV2Event([]byte(`{"type":"session.compaction.started","data":{"sessionID":"ses_1"}}`))
	if !ok {
		t.Fatal("compaction should be busy")
	}
	status, ok = parseSessionStatus(payload)
	if !ok || status.Status != "busy" {
		t.Fatalf("compaction status = %+v", status)
	}
	if _, _, ok := translateV2Event([]byte(`{"type":"session.step.ended","data":{"sessionID":"ses_1"}}`)); ok {
		t.Fatal("step end is mid-turn and must not be idle")
	}
	encoded, _ := json.Marshal(`{"type":"session.retry.scheduled","data":{"sessionID":"ses_9"}}`)
	_, payload, ok = translateV2Event(encoded)
	if !ok {
		t.Fatal("double-encoded retry event should translate")
	}
	status, ok = parseSessionStatus(payload)
	if !ok || status.SessionID != "ses_9" || status.Status != "busy" {
		t.Fatalf("retry status = %+v ok=%v", status, ok)
	}

	if _, _, ok := translateV2Event([]byte(`{"type":"server.connected","data":{}}`)); ok {
		t.Fatal("unrelated events should be ignored")
	}
}

func TestCanonicalizeOpenCode2URLRewritesWildcardHost(t *testing.T) {
	if got := canonicalizeOpenCode2URL("http://0.0.0.0:49374/"); got != "http://127.0.0.1:49374" {
		t.Fatalf("canonicalizeOpenCode2URL() = %q", got)
	}
}

func TestEventDeduperCollapsesQuestionTwins(t *testing.T) {
	deduper := newEventDeduper(time.Minute)
	asked := questionEvent{ID: "que_1", SessionID: "ses_1", CallID: "call_1"}
	part := questionEvent{ID: "ses_1:call_1", SessionID: "ses_1", CallID: "call_1"}
	if !deduper.reserve(questionKeys(part)) {
		t.Fatal("first reservation should succeed")
	}
	if deduper.reserve(questionKeys(asked)) {
		t.Fatal("tool-part and question.asked sharing a call id must collapse")
	}
	other := questionEvent{ID: "que_2", SessionID: "ses_1"}
	if !deduper.reserve(questionKeys(other)) {
		t.Fatal("a different request with no call id should still notify")
	}
	permission := permissionEvent{ID: "per_1", SessionID: "ses_1", CallID: "call_1"}
	if !deduper.reserve(permissionKeys(permission)) {
		t.Fatal("question and permission namespaces must not collide")
	}
}

func TestEventDeduperDoesNotClaimOnReject(t *testing.T) {
	deduper := newEventDeduper(time.Minute)
	if !deduper.reserve([]string{"question:que_1"}) {
		t.Fatal("first reserve")
	}
	if deduper.reserve([]string{"question:que_1", "question:tool:ses:call"}) {
		t.Fatal("overlapping reserve should fail")
	}
	if !deduper.reserve([]string{"question:tool:ses:call"}) {
		t.Fatal("a rejected reservation must not claim its other keys")
	}
}

func TestNotifierDropsDuplicateWithinWindow(t *testing.T) {
	var sent []Notification
	notifier := NewNotifier([]Provider{recordingProvider{send: func(n Notification) { sent = append(sent, n) }}}, nil, nil)
	now := time.Now()
	notifier.now = func() time.Time { return now }
	first := Notification{Type: TypeIdle, Source: SourceOpenCode, SessionID: "s1", Hostname: "box"}
	if err := notifier.Send(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if err := notifier.Send(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	other := first
	other.SessionID = "s2"
	if err := notifier.Send(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 2 {
		t.Fatalf("sent = %+v, want the first idle and the other session", sent)
	}
	notifier.now = func() time.Time { return now.Add(defaultDedupeWindow) }
	if err := notifier.Send(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	if len(sent) != 3 {
		t.Fatalf("duplicate after the window should send, got %d", len(sent))
	}
}

func TestLegacyQuestionUsesCallID(t *testing.T) {
	payload := []byte(`{"type":"message.part.updated","properties":{"part":{"type":"tool","tool":"question","sessionID":"ses_1","callID":"call_1","state":{"status":"running","input":{"questions":[{"question":"Ship it?"}]}}}}}`)
	event, ok := parseLegacyQuestion(payload)
	if !ok || event.ID != "ses_1:call_1" || event.CallID != "call_1" {
		t.Fatalf("legacy question = %+v ok=%v", event, ok)
	}
}

func TestMonitorDropsLegacyTwinAfterQuestionAsked(t *testing.T) {
	var sent []Notification
	monitor := newMonitor(&openCodeClient{client: &http.Client{}}, recordingSender{send: func(n Notification) {
		sent = append(sent, n)
	}}, "", 0, SourceOpenCode, nil)
	asked := []byte(`{"type":"question.asked","properties":{"id":"que_1","sessionID":"ses_1","tool":{"callID":"call_1"},"questions":[{"question":"Ship it?"}]}}`)
	part := []byte(`{"type":"message.part.updated","properties":{"part":{"type":"tool","tool":"question","sessionID":"ses_1","callID":"call_1","state":{"status":"running","input":{"questions":[{"question":"Ship it?"}]}}}}}`)
	monitor.handlePayload(context.Background(), "/repos/op", part)
	monitor.handlePayload(context.Background(), "/repos/op", asked)
	if len(sent) != 1 || sent[0].Question != "Ship it?" {
		t.Fatalf("sent = %+v", sent)
	}
}

type scriptedIdleClient struct {
	mu     sync.Mutex
	active bool
	info   sessionInfo
}

func (c *scriptedIdleClient) watch(context.Context, func(string, json.RawMessage)) error {
	return nil
}

func (c *scriptedIdleClient) fetchSessionInfo(context.Context, string, string) (sessionInfo, bool) {
	return c.info, true
}

func (c *scriptedIdleClient) confirmIdle(context.Context, string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.active
}

func (c *scriptedIdleClient) setActive(active bool) {
	c.mu.Lock()
	c.active = active
	c.mu.Unlock()
}

func TestMonitorSkipsIdleWhileSessionStillActive(t *testing.T) {
	var mu sync.Mutex
	var sent []Notification
	client := &scriptedIdleClient{active: true, info: sessionInfo{Title: "title"}}
	monitor := newMonitor(client, recordingSender{send: func(n Notification) {
		mu.Lock()
		sent = append(sent, n)
		mu.Unlock()
	}}, "https://beta.example", 0, SourceOpenCode2, nil)
	monitor.serverURL = "https://oc2.example.com"
	monitor.handleStatus(context.Background(), "/repos/op", sessionStatusEvent{SessionID: "s1", Status: "busy"})
	monitor.handleStatus(context.Background(), "/repos/op", sessionStatusEvent{SessionID: "s1", Status: "idle"})
	time.Sleep(40 * time.Millisecond)
	mu.Lock()
	count := len(sent)
	mu.Unlock()
	if count != 0 {
		t.Fatalf("active session notified: %d", count)
	}
	client.setActive(false)
	monitor.handleStatus(context.Background(), "/repos/op", sessionStatusEvent{SessionID: "s1", Status: "busy"})
	monitor.handleStatus(context.Background(), "/repos/op", sessionStatusEvent{SessionID: "s1", Status: "idle"})
	deadline := time.Now().Add(time.Second)
	var got []Notification
	for time.Now().Before(deadline) {
		mu.Lock()
		got = append([]Notification(nil), sent...)
		mu.Unlock()
		if len(got) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(got) != 1 || !strings.Contains(got[0].DesktopURL, "/server/") {
		t.Fatalf("sent = %+v", got)
	}
}

func TestDiscordEmbedTitlesProjectAndSession(t *testing.T) {
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	provider := &discordProvider{client: server.Client(), webhookURL: server.URL}
	err := provider.Send(context.Background(), Notification{
		Type: TypeIdle, Source: SourceOpenCode, SessionID: "ses_1", SessionTitle: "Fix auth",
		ProjectDirectory: "/repos/op", Hostname: "devbox.example", Timestamp: time.Unix(1_700_000_000, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if body["username"] != "OpenCode · devbox" {
		t.Fatalf("username = %#v", body["username"])
	}
	embeds, _ := body["embeds"].([]any)
	embed, _ := embeds[0].(map[string]any)
	if embed["title"] != "op · Fix auth" {
		t.Fatalf("embed = %#v", embed)
	}
}

func TestGrokSessionTitleFromSummary(t *testing.T) {
	root := t.TempDir()
	sessionDir := filepath.Join(root, "sessions", urlEncodePath("/repos/op"), "ses_safe")
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	summary := []byte(`{"generated_title":"Rename the notifier","session_summary":"fallback"}`)
	if err := os.WriteFile(filepath.Join(sessionDir, "summary.json"), summary, 0o644); err != nil {
		t.Fatal(err)
	}
	notification := mapGrokCodeHook(map[string]any{
		"sessionId": "ses_safe", "cwd": "/repos/op", "hookEventName": "Stop", "reason": "end_turn",
	}, root)
	if notification == nil || notification.SessionTitle != "Rename the notifier" {
		t.Fatalf("notification = %+v", notification)
	}
	hashed := filepath.Join(root, "sessions", "slug-deadbeef", "ses_hash")
	if err := os.MkdirAll(hashed, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hashed, "summary.json"), []byte(`{"session_summary":"Hashed cwd"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readGrokSessionTitle("ses_hash", "/somewhere/else", root); got != "Hashed cwd" {
		t.Fatalf("hashed title = %q", got)
	}
	if readGrokSessionTitle("../etc", "/repos/op", root) != "" {
		t.Fatal("unsafe session id should be ignored")
	}
	ignored := mapGrokCodeHook(map[string]any{
		"sessionId": "ses_safe", "cwd": "/repos/op", "hookEventName": "Stop", "reason": "channel_closed",
	}, root)
	if ignored != nil {
		t.Fatalf("channel_closed should stay ignored, got %+v", ignored)
	}
}

func TestIngestNotifyHandler(t *testing.T) {
	var sent Notification
	ingest := NewIngest(recordingSender{send: func(n Notification) { sent = n }}, nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/notify", strings.NewReader(`{"type":"idle","sessionId":"s1","hostname":"child"}`))
	ingest.HandleNotify(recorder, request)
	if recorder.Code != http.StatusOK || sent.SessionID != "s1" || sent.Hostname != "child" {
		body, _ := io.ReadAll(recorder.Body)
		t.Fatalf("status=%d body=%s sent=%+v", recorder.Code, body, sent)
	}
}
