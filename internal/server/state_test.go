package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/moutansos/op/internal/state"
)

func TestStateSnapshotRequiresAuthentication(t *testing.T) {
	service := &fakeService{}
	tracker, err := state.New(service, state.Options{InstanceID: "child-one"})
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestHandler(t, service, func(o *Options) { o.State = tracker })
	for _, token := range []string{"", "wrong"} {
		if got := request(handler, http.MethodGet, "/v1/state", "", token, "").Code; got != http.StatusUnauthorized {
			t.Fatalf("status: %d", got)
		}
	}
	response := request(handler, http.MethodGet, "/v1/state", "", testToken, "")
	if response.Code != http.StatusOK {
		t.Fatalf("%d: %s", response.Code, response.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["instanceId"] != "child-one" {
		t.Fatalf("snapshot identity: %v", body)
	}
	if got := request(handler, http.MethodPost, "/v1/state", "", testToken, "").Code; got != http.StatusMethodNotAllowed {
		t.Fatalf("POST status: %d", got)
	}
}

func TestStateSnapshotUnavailableWithoutSampler(t *testing.T) {
	handler := newTestHandler(t, &fakeService{}, nil)
	response := request(handler, http.MethodGet, "/v1/state", "", testToken, "")
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("%d: %s", response.Code, response.Body.String())
	}
}
