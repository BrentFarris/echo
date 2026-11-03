package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
)

func TestSwarmRequestsDuringInitialization(t *testing.T) {
	// No services exist yet: even constructing the ordinary handler here would
	// risk dereferencing partial state. Every tunneled endpoint must retry.
	s := &Server{}
	for _, path := range []string{"/api/v1/state", "/api/v1/plugins/monaco-editor/bots/echo/fs/roots", "/api/v1/swarm/pair"} {
		w := httptest.NewRecorder()
		s.serveSwarmOwner(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "1" {
			t.Fatalf("startup request %s: %d %s", path, w.Code, w.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body["error"] == "" {
			t.Fatalf("startup diagnostic: %s, %v", w.Body.String(), err)
		}
	}
}

func TestSwarmStartupRetryOnExistingConnection(t *testing.T) {
	a, _ := swarmFixture(t, "entry", remotePassword)
	b, host := swarmFixture(t, "starting", remotePassword)
	if !b.ready.Load() {
		t.Fatal("New returned before publishing readiness")
	}
	node := connectSwarm(t, a, host, remotePassword)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Reproduce an already-connected peer reaching the startup gate. Keep the
	// real services intact so their background tasks remain race-free.
	b.ready.Store(false)
	response, err := a.Swarm.RoundTrip(ctx, node.ID, http.MethodGet, "/api/v1/state", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusServiceUnavailable || response.Header.Get("Retry-After") != "1" {
		t.Fatalf("early peer request: %d %v", response.StatusCode, response.Header)
	}
	b.ready.Store(true)
	var state struct {
		Bots []domain.Bot `json:"bots"`
	}
	if err := a.Swarm.JSON(ctx, node.ID, http.MethodGet, "/api/v1/state", nil, &state); err != nil {
		t.Fatal("retry on established link", err)
	}
	if len(state.Bots) == 0 {
		t.Fatal("ready state did not include bots")
	}
	b.Close()
	if b.ready.Load() {
		t.Fatal("shutdown did not revoke readiness")
	}
}
