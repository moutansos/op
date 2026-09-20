package state

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/moutansos/op/internal/domain"
)

func TestResolveEventsURL(t *testing.T) {
	got, err := ResolveEventsURL("https://muxplane.example/v1/op/state", "")
	if err != nil || got != "https://muxplane.example/v1/op/events" {
		t.Fatalf("derived = %q, %v", got, err)
	}
	got, err = ResolveEventsURL("https://muxplane.example/ingest", "https://muxplane.example/bus")
	if err != nil || got != "https://muxplane.example/bus" {
		t.Fatalf("explicit = %q, %v", got, err)
	}
	if _, err := ResolveEventsURL("", "file:///tmp/events"); err == nil {
		t.Fatal("accepted file events URL")
	}
}

func TestParentSSEExecutesOpenAndIgnoresDuplicates(t *testing.T) {
	var mu sync.Mutex
	var opened []domain.OpenProjectRequest
	events := make(chan string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/op/state" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if r.URL.Path != "/v1/op/events" || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("X-Op-Instance-Id") != "child-one" {
			t.Errorf("bad subscribe: %s %v", r.URL, r.Header)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		flusher.Flush()
		for {
			select {
			case event, ok := <-events:
				if !ok {
					return
				}
				_, _ = io.WriteString(w, event)
				flusher.Flush()
			case <-r.Context().Done():
				return
			}
		}
	}))
	defer server.Close()
	service := &commandService{}
	service.projects = []domain.Project{{ID: "catalog", Path: t.TempDir()}}
	service.open = func(_ context.Context, request domain.OpenProjectRequest) (domain.OpenProjectResult, error) {
		mu.Lock()
		opened = append(opened, request)
		mu.Unlock()
		return domain.OpenProjectResult{Project: domain.Project{ID: request.ProjectID}}, nil
	}
	x := tracker(t, service, Options{InstanceID: "child-one", ParentURL: server.URL + "/v1/op/state", ParentToken: "secret", HeartbeatInterval: time.Hour, RefreshInterval: time.Hour})
	x.sample(context.Background())
	snapshot := x.Snapshot()
	if len(snapshot.Projects.Data) != 1 {
		t.Fatal("expected catalog project in snapshot")
	}
	namespacedID := snapshot.Projects.Data[0].ID
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- x.Run(ctx) }()
	deadline := time.After(2 * time.Second)
	for x.Connection().State != LinkConnected {
		select {
		case <-deadline:
			t.Fatalf("not connected: %+v", x.Connection())
		case <-time.After(10 * time.Millisecond):
		}
	}
	payload := `event: command
id: evt-1
data: {"version":1,"instanceId":"child-one","eventId":"evt-1","type":"command.open_project","payload":{"projectId":"` + namespacedID + `","profile":"nvim"}}

`
	events <- payload
	events <- payload
	events <- "event: command\ndata: {\"instanceId\":\"other\",\"eventId\":\"evt-2\",\"type\":\"command.open_project\",\"payload\":{\"projectId\":\"catalog\"}}\n\n"
	deadline = time.After(2 * time.Second)
	for {
		mu.Lock()
		n := len(opened)
		mu.Unlock()
		if n >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("command not executed")
		case <-time.After(10 * time.Millisecond):
		}
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(opened) != 1 || opened[0].ProjectID != "catalog" || opened[0].Profile != "nvim" {
		t.Fatalf("opened = %+v", opened)
	}
	cancel()
	<-done
}

func TestParentSSEReconnectsAfterDrop(t *testing.T) {
	var mu sync.Mutex
	connections := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/op/state" {
			w.WriteHeader(204)
			return
		}
		mu.Lock()
		connections++
		n := connections
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if n == 1 {
			return
		}
		flusher, _ := w.(http.Flusher)
		flusher.Flush()
		<-r.Context().Done()
	}))
	defer server.Close()
	x := tracker(t, &fakeService{}, Options{ParentURL: server.URL + "/v1/op/state", HeartbeatInterval: time.Hour, RefreshInterval: time.Hour})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go x.Run(ctx)
	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := connections
		mu.Unlock()
		if n >= 2 && x.Connection().State == LinkConnected {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("connections=%d status=%+v", n, x.Connection())
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestParseParentSSEKeepaliveAndMultiline(t *testing.T) {
	var events []sseEvent
	err := readParentSSE(context.Background(), strings.NewReader(": keepalive\n\nevent: command\nid: 1\ndata: {\"a\":1}\ndata: {\"b\":2}\n\n"), func(event sseEvent) {
		events = append(events, event)
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "1" || !strings.Contains(events[0].Data, `"a":1`) {
		t.Fatalf("events = %+v", events)
	}
}

type commandService struct {
	fakeService
	open func(context.Context, domain.OpenProjectRequest) (domain.OpenProjectResult, error)
}

func (s *commandService) OpenProject(ctx context.Context, request domain.OpenProjectRequest) (domain.OpenProjectResult, error) {
	if s.open != nil {
		return s.open(ctx, request)
	}
	return domain.OpenProjectResult{}, nil
}

func TestCommandEnvelopeJSONRoundTrip(t *testing.T) {
	raw := []byte(`{"version":1,"type":"command.open_project","payload":{"projectId":"p"}}`)
	var body struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Type != CommandOpenProject {
		t.Fatal(err)
	}
}
