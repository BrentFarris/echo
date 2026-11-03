package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/store"
	"github.com/brent/echo/internal/swarm"
)

func gatedSwarmPeer(t *testing.T, entered chan<- struct{}, release <-chan struct{}) *httptest.Server {
	t.Helper()
	dir := t.TempDir()
	db, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	m, err := swarm.New(context.Background(), swarm.Config{
		Store: db, Secrets: &swarmTestSecrets{values: map[string]string{}}, IdentityFile: filepath.Join(dir, "identity"), Name: "sleepy", Epoch: "one",
		Enabled: func() bool { return true }, CheckPassword: func(context.Context, string, string) error { return nil },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			respond(w, map[string]any{"bots": []any{}, "chats": []any{}}, nil)
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.Close)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/swarm/link", m.LinkHTTP)
	mux.HandleFunc("/api/v1/swarm/pair", func(w http.ResponseWriter, r *http.Request) {
		var req swarm.PairRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		value, err := m.Pair(r.Context(), req, r.RemoteAddr)
		respond(w, value, err)
	})
	host := httptest.NewServer(mux)
	t.Cleanup(host.Close)
	return host
}

func TestSwarmCacheDoesNotWaitForStalledPeer(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	_, online := swarmFixture(t, "online", remotePassword)
	connectSwarm(t, a, online, remotePassword)
	entered, release := make(chan struct{}, 8), make(chan struct{})
	b := gatedSwarmPeer(t, entered, release)
	defer close(release)
	node := connectSwarm(t, a, b, remotePassword)
	a.requestSwarmRefresh()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("background request did not start")
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		w := httptest.NewRecorder()
		a.swarmState(w, httptest.NewRequest("GET", "/api/v1/swarm/state", nil))
		if w.Code != 200 {
			t.Errorf("state: %s", w.Body)
		}
		// Local-only resolution must not fetch the unrelated peer roster.
		if _, _, err := a.resolveSwarm(context.Background(), SendRequest{Content: "@echo hello"}); err != nil {
			t.Error(err)
		}
		if _, _, err := a.resolveSwarm(context.Background(), SendRequest{Content: "@online.echo hello"}); err != nil {
			t.Error(err)
		}
		c := chatForSwarm(t, a)
		finish := a.stopChat(c.ID)
		finish()
		if err := a.Store.DeleteChat(c.ID); err != nil {
			t.Error(err)
		}
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("local work waited for the gated peer")
	}
	// A failed background probe marks an otherwise connected route unavailable.
	eventuallySwarm(t, func() bool { return !a.swarmAvailable(node.ID) })
	_, _, err := a.resolveSwarm(context.Background(), SendRequest{Content: "@echo @sleepy.echo hello"})
	if err == nil || !strings.Contains(err.Error(), "sleepy") || !strings.Contains(err.Error(), "offline") {
		t.Fatal(err)
	}
}

func TestSwarmOfflineCacheAndRecovery(t *testing.T) {
	a, _ := swarmFixture(t, "desktop", remotePassword)
	_, bh := swarmFixture(t, "linux", remotePassword)
	node := connectSwarm(t, a, bh, remotePassword)
	eventuallySwarm(t, func() bool { return len(a.cachedSwarmState(node.ID)) > 0 })
	if err := a.Swarm.SetDisconnected(node.ID, true); err != nil {
		t.Fatal(err)
	}
	if a.swarmAvailable(node.ID) {
		t.Fatal("disconnected peer is online")
	}
	// On restart the in-memory inventory is empty; persisted snapshots remain
	// immediately readable without a network request.
	a.swarmCacheMu.Lock()
	delete(a.swarmCache, node.ID)
	a.swarmCacheMu.Unlock()
	if len(a.cachedSwarmState(node.ID)) == 0 {
		t.Fatal("lost persisted offline inventory")
	}
	_, _, err := a.resolveSwarm(context.Background(), SendRequest{Content: "@echo @linux.echo hello"})
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatal(err)
	}
	path := "/api/v1/chats/history/messages"
	if err := a.Store.Put("swarm-transcripts", swarm.Key(node.ID, path), json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/swarm/instances/"+node.ID+"/chats/history/messages", nil)
	r.SetPathValue("nodeId", node.ID)
	r.SetPathValue("path", "chats/history/messages")
	w := httptest.NewRecorder()
	a.swarmProxy(w, r)
	if w.Code != 200 || w.Header().Get("X-Echo-Cached") != "true" {
		t.Fatalf("cached transcript: %d %s", w.Code, w.Body)
	}
	if err := a.Swarm.SetDisconnected(node.ID, false); err != nil {
		t.Fatal(err)
	}
	eventuallySwarm(t, func() bool { return a.swarmAvailable(node.ID) })
	if err := a.Swarm.Revoke(node.ID); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	a.swarmProxy(w, r)
	if w.Code != 403 {
		t.Fatalf("revoked cache exposed: %d", w.Code)
	}
}
