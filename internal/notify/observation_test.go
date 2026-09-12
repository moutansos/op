package notify

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moutansos/op/internal/domain"
)

func receiveObservation(t *testing.T, observations <-chan Observation) Observation {
	t.Helper()
	select {
	case observation := <-observations:
		return observation
	case <-time.After(time.Second):
		t.Fatal("observation not delivered")
		return Observation{}
	}
}

func TestNotifierObservesWithoutDelivery(t *testing.T) {
	for _, ignored := range []bool{false, true} {
		n := NewNotifier(nil, []string{"/ignored"}, nil)
		observations := make(chan Observation, 1)
		n.SetObserver(func(o Observation) { observations <- o })
		directory := "/project"
		if ignored {
			directory = "/ignored/project"
		}
		err := n.Send(context.Background(), Notification{Type: TypeQuestion, SessionID: "s", ProjectID: "native-id", ProjectDirectory: directory, Question: "Continue?"})
		if err != nil {
			t.Fatal(err)
		}
		o := receiveObservation(t, observations)
		if o.Activity != domain.AgentActivityAwaitingInput || o.Source != SourceOpenCode || o.ProjectID != "native-id" || o.ProjectDirectory != directory || o.Coverage != CoverageNotificationOnly || o.Timestamp.IsZero() || o.Notification == nil || o.Notification.Hostname == "" {
			t.Fatalf("observation = %+v", o)
		}
	}
}

func TestObserverDoesNotBlockDeliveryAndPreservesOrder(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	observations := make(chan Observation, 2)
	delivered := make(chan struct{}, 2)
	n := NewNotifier([]Provider{recordingProvider{send: func(Notification) { delivered <- struct{}{} }}}, nil, nil)
	n.SetObserver(func(o Observation) {
		if o.SessionID == "first" {
			close(entered)
			<-release
		}
		observations <- o
	})
	n.Observe(Observation{SessionID: "first"})
	<-entered
	done := make(chan struct{})
	go func() {
		_ = n.Send(context.Background(), Notification{Type: TypeIdle, SessionID: "second"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked observer delayed provider delivery")
	}
	select {
	case <-delivered:
	default:
		t.Fatal("provider was not called")
	}
	// The callback can be replaced while the old callback is still executing.
	n.SetObserver(nil)
	close(release)
	if o := receiveObservation(t, observations); o.SessionID != "first" {
		t.Fatalf("first observation = %+v", o)
	}
	if o := receiveObservation(t, observations); o.SessionID != "second" {
		t.Fatalf("second observation = %+v", o)
	}
}

func TestV2LifecycleBridge(t *testing.T) {
	for _, kind := range []string{"form.replied", "form.cancelled", "permission.replied", "session.deleted"} {
		t.Run(kind, func(t *testing.T) {
			directory, payload, ok := translateV2Event([]byte(`{"type":"` + kind + `","location":{"directory":"/repo"},"data":{"sessionID":"s","projectID":"native"}}`))
			if !ok {
				t.Fatal("event not translated")
			}
			sender := &observationSender{}
			m := newMonitor(nil, sender, "", time.Hour, SourceOpenCode2, nil)
			m.handlePayload(context.Background(), directory, payload)
			if len(sender.observations) != 1 || len(sender.notifications) != 0 {
				t.Fatalf("sender = %+v", sender)
			}
			o := sender.observations[0]
			if o.Source != SourceOpenCode2 || o.ProjectID != "native" || o.Terminated != (kind == "session.deleted") {
				t.Fatalf("observation = %+v", o)
			}
		})
	}
}

func TestConcurrentObserverReplacement(t *testing.T) {
	n := NewNotifier(nil, nil, nil)
	var wg sync.WaitGroup
	for range 10 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				n.SetObserver(func(Observation) {})
				n.Observe(Observation{SessionID: "s"})
				n.SetObserver(nil)
			}
		}()
	}
	wg.Wait()
}

type observationSender struct {
	observations  []Observation
	notifications []Notification
}

func (s *observationSender) Observe(o Observation) { s.observations = append(s.observations, o) }
func (s *observationSender) Send(_ context.Context, n Notification) error {
	s.notifications = append(s.notifications, n)
	return nil
}

func TestMonitorObservesImmediateLifecycle(t *testing.T) {
	sender := &observationSender{}
	m := newMonitor(nil, sender, "", time.Hour, SourceOpenCode, nil)
	defer func() {
		for _, timer := range m.pending {
			timer.Stop()
		}
	}()
	for _, status := range []string{"idle", "busy", "idle", "busy"} {
		m.handlePayload(context.Background(), "/repo", json.RawMessage(`{"type":"session.status","properties":{"sessionID":"s","status":{"type":"`+status+`"}}}`))
	}
	for _, kind := range []string{"question.replied", "question.rejected", "permission.replied", "permission.resolved"} {
		m.handlePayload(context.Background(), "/repo", json.RawMessage(`{"type":"`+kind+`","properties":{"sessionID":"s"}}`))
	}
	m.handlePayload(context.Background(), "/repo", json.RawMessage(`{"type":"session.deleted","properties":{"info":{"id":"s","projectID":"native-project","directory":"/native/repo"}}}`))
	if len(sender.notifications) != 0 || len(sender.observations) != 9 {
		t.Fatalf("sender = %+v", sender)
	}
	if sender.observations[0].Activity != domain.AgentActivityIdle || sender.observations[1].Activity != domain.AgentActivityWorking {
		t.Fatalf("statuses = %+v", sender.observations)
	}
	last := sender.observations[8]
	if !last.Terminated || last.ProjectID != "native-project" || last.ProjectDirectory != "/native/repo" || last.Coverage != CoverageNative {
		t.Fatalf("deleted = %+v", last)
	}
	if len(m.sessions) != 0 || len(m.pending) != 0 {
		t.Fatal("deleted session retained state")
	}
}

func TestHookLifecycleWithoutLegacyNotification(t *testing.T) {
	for _, event := range []string{"UserPromptSubmit", "PreToolUse", "SessionStart", "SessionEnd"} {
		t.Run(event, func(t *testing.T) {
			sender := &observationSender{}
			i := NewIngest(sender, nil)
			request := httptest.NewRequest("POST", "/", strings.NewReader(`{"hook_event_name":"`+event+`","session_id":"s","cwd":"/repo","projectId":"native"}`))
			response := httptest.NewRecorder()
			i.HandleClaudeCodeHook(response, request)
			if response.Code != 200 || len(sender.notifications) != 0 || len(sender.observations) != 1 {
				t.Fatalf("response %d, sender %+v", response.Code, sender)
			}
			o := sender.observations[0]
			if o.Terminated != (event == "SessionEnd") || o.Coverage != CoverageHooks || o.ProjectID != "native" {
				t.Fatalf("observation = %+v", o)
			}
		})
	}
	for _, payload := range []map[string]any{
		{"hook_event_name": "PreToolUse", "session_id": "s", "agent_id": "child"},
		{"hook_event_name": "PreToolUse"},
		{"hook_event_name": "Unsupported", "session_id": "s"},
	} {
		if o, ok := hookObservation(SourceClaudeCode, payload); ok {
			t.Fatalf("unexpected observation %+v", o)
		}
	}
	if _, ok := hookObservation(SourceCodex, map[string]any{"hook_event_name": "SessionEnd", "session_id": "s"}); ok {
		t.Fatal("guessed unsupported Codex hook")
	}
}

func TestNativeSendDoesNotDuplicateObservation(t *testing.T) {
	n := NewNotifier(nil, nil, nil)
	observations := make(chan Observation, 3)
	n.SetObserver(func(o Observation) { observations <- o })
	_ = sendNative(context.Background(), n, Notification{Type: TypePermission, Source: SourceClaudeCode, SessionID: "s"}, CoverageHooks)
	n.Observe(Observation{SessionID: "barrier"})
	if o := receiveObservation(t, observations); o.Coverage != CoverageHooks || o.Activity != domain.AgentActivityPermissionRequired {
		t.Fatalf("observation = %+v", o)
	}
	if o := receiveObservation(t, observations); o.SessionID != "barrier" {
		t.Fatalf("duplicate observation = %+v", o)
	}
}
