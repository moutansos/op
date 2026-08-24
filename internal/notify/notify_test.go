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
	}}, "https://desktop.example", 0, nil)
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
	}}, "", 0, nil)
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
