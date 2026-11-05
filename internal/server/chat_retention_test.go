package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/store"
	"github.com/brent/echo/internal/swarm"
)

func retentionAPI(t *testing.T, s *Server) func(string, string, string) *httptest.ResponseRecorder {
	t.Helper()
	handler := s.Handler()
	var cookies []*http.Cookie
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://localhost/api/v1"+path, strings.NewReader(body))
		r.RemoteAddr = "127.0.0.1:1234"
		for _, cookie := range cookies {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	login := call("POST", "/session", "{}")
	if login.Code != 200 {
		t.Fatal(login.Code, login.Body)
	}
	cookies = login.Result().Cookies()
	return call
}

func TestDeletedChatAPIStopsAndRestores(t *testing.T) {
	s := testServer(t)
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"Saved partial answer\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	})
	c := chat(t, s, g)
	d, err := s.send(c.ID, SendRequest{Content: "keep this", Generation: g})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("model did not start")
	}
	call := retentionAPI(t, s)
	path := "/chats/" + c.ID
	deleted := call("DELETE", path, "")
	if deleted.Code != 200 {
		t.Fatal(deleted.Code, deleted.Body)
	}
	waitDispatch(t, d)
	var retained domain.Chat
	if err = json.Unmarshal(deleted.Body.Bytes(), &retained); err != nil || retained.DeletedAt == nil {
		t.Fatal(retained, err)
	}
	if response := call("GET", path+"/messages", ""); response.Code != 200 || !strings.Contains(response.Body.String(), "keep this") {
		t.Fatal(response.Code, response.Body)
	}
	for _, request := range []struct{ method, suffix, body string }{
		{"POST", "/messages", `{"content":"blocked"}`}, {"PUT", "/generation", `{}`},
		{"POST", "/attachments", ""}, {"PATCH", "/messages/any", `{"content":"blocked"}`},
		{"DELETE", "/messages/any", ""}, {"POST", "/messages/any/fork", `{}`},
		{"POST", "/messages/any/rerun", `{}`}, {"POST", "/questions/any", `{}`},
	} {
		if response := call(request.method, path+request.suffix, request.body); response.Code != http.StatusConflict {
			t.Errorf("%s %s: %d %s", request.method, request.suffix, response.Code, response.Body)
		}
	}
	if response := call("POST", path+"/restore", "{}"); response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	if restored, err := s.Store.Chat(c.ID); err != nil || restored.ID != c.ID {
		t.Fatal(restored, err)
	}
	if response := call("DELETE", path+"/permanent", ""); response.Code != http.StatusConflict {
		t.Fatal("purged active chat", response.Code)
	}
	if response := call("DELETE", path, ""); response.Code != 200 {
		t.Fatal(response.Body)
	}
	if response := call("DELETE", path+"/permanent", ""); response.Code != 200 {
		t.Fatal(response.Body)
	}
	if response := call("GET", path+"/messages", ""); response.Code == 200 {
		t.Fatal("purged history remained readable")
	}
}

func TestDeletedRemoteChatCacheRestoreAndExpiry(t *testing.T) {
	a, _ := swarmFixture(t, "entry", remotePassword)
	b, bh := swarmFixture(t, "home", remotePassword)
	node := connectSwarm(t, a, bh, remotePassword)
	c := chatForSwarm(t, b)
	if err := b.Store.AddMessage(domain.Message{ID: "saved", ChatID: c.ID, Content: "retained remote text", Role: "user", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	call := retentionAPI(t, a)
	path := "/swarm/instances/" + node.ID + "/chats/" + c.ID
	if response := call("GET", path+"/messages", ""); response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	if response := call("DELETE", path, ""); response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	if b.Store.ChatDeleted(c.ID) {
		t.Fatal("soft deletion created a permanent tombstone")
	}
	if err := a.Swarm.SetDisconnected(node.ID, true); err != nil {
		t.Fatal(err)
	}
	if response := call("GET", path+"/messages", ""); response.Code != 200 || response.Header().Get("X-Echo-Cached") != "true" || !strings.Contains(response.Body.String(), "retained remote text") {
		t.Fatal(response.Code, response.Body)
	}
	if response := call("POST", path+"/restore", "{}"); response.Code == 200 {
		t.Fatal("offline restore succeeded")
	}
	if err := a.Swarm.SetDisconnected(node.ID, false); err != nil {
		t.Fatal(err)
	}
	eventuallySwarm(t, func() bool { return a.swarmAvailable(node.ID) })
	if response := call("POST", path+"/restore", "{}"); response.Code != 200 {
		t.Fatal(response.Code, response.Body)
	}
	if _, err := b.Store.Chat(c.ID); err != nil {
		t.Fatal(err)
	}
	if response := call("DELETE", path, ""); response.Code != 200 {
		t.Fatal(response.Body)
	}
	if response := call("DELETE", path+"/permanent", ""); response.Code != 200 {
		t.Fatal(response.Body)
	}
	if response := call("GET", path+"/messages", ""); response.Code == 200 {
		t.Fatal("purged remote cache served")
	}
	// Cached retention deadlines are enforced even without the home machine.
	old := domain.Now().Add(-store.ChatRetention)
	a.recordRemoteChatMutation(node.ID, domain.Chat{ID: "expired", Title: "private cached title", DeletedAt: &old}, false)
	_ = a.Store.Put("swarm-transcripts", swarm.Key(node.ID, "/api/v1/chats/expired/messages"), json.RawMessage(`[{"content":"expired"}]`))
	if err := a.expireRemoteChatCache(); err != nil {
		t.Fatal(err)
	}
	var cached json.RawMessage
	if err := a.Store.Get("swarm-transcripts", swarm.Key(node.ID, "/api/v1/chats/expired/messages"), &cached); err == nil {
		t.Fatal("expired cached bytes survived maintenance")
	}
	if strings.Contains(string(a.cachedSwarmState(node.ID)), "private cached title") {
		t.Fatal("expired metadata retained its title")
	}
	if response := call("GET", "/swarm/instances/"+node.ID+"/chats/expired/messages", ""); response.Code != http.StatusGone {
		t.Fatal(response.Code, response.Body)
	}
}

func TestRestoredSwarmChatAcceptsNewWorkWithoutOldResults(t *testing.T) {
	a, _ := swarmFixture(t, "entry", remotePassword)
	b, bh := swarmFixture(t, "worker", remotePassword)
	connectSwarm(t, a, bh, remotePassword)
	attachModel(t, b, func(w http.ResponseWriter, r *http.Request) { stream(w, "worker answer", nil) })
	c := chatForSwarm(t, a)
	d, err := a.send(c.ID, SendRequest{Content: "@worker.echo first"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	call := retentionAPI(t, a)
	path := "/chats/" + c.ID
	if response := call("DELETE", path, ""); response.Code != 200 {
		t.Fatal(response.Body)
	}
	a.reconcileSwarm()
	if _, err := b.Store.Chat(projectionID(a.Swarm.Node().ID, c.ID)); err != nil {
		t.Fatal("worker history removed during retention", err)
	}
	if response := call("POST", path+"/restore", "{}"); response.Code != 200 {
		t.Fatal(response.Body)
	}
	d, err = a.send(c.ID, SendRequest{Content: "@worker.echo second"})
	if err != nil {
		t.Fatal(err)
	}
	waitDispatch(t, d)
	runs, _ := a.Store.ChatRuns(c.ID)
	if len(runs) != 2 || runs[1].Status != "completed" {
		t.Fatal(runs)
	}
	for _, run := range runs {
		if run.Status != "completed" {
			t.Fatal(run)
		}
	}
	if response := call("DELETE", path, ""); response.Code != 200 {
		t.Fatal(response.Body)
	}
	if response := call("DELETE", path+"/permanent", ""); response.Code != 200 {
		t.Fatal(response.Body)
	}
	a.reconcileSwarm()
	var retired workerRequest
	if err := b.Store.Get("swarm-workers", workerKey(a.Swarm.Node().ID, d.id), &retired); err != nil {
		t.Fatal(err)
	}
	if retired.Status != "deleted" || retired.Message.Content != "" || retired.Chat.Title != "" || len(retired.Runs) != 0 {
		t.Fatal("purge retained worker execution content", retired)
	}
}

func TestRemoteInventoryAbsenceDoesNotCreatePermanentTombstone(t *testing.T) {
	s := testServer(t)
	// Inventory fetched before a concurrent creation can temporarily omit a
	// chat. Only an acknowledged permanent deletion can create a tombstone.
	s.pruneRemoteHistory("node", json.RawMessage(`{"chats":[{"id":"live"}]}`), json.RawMessage(`{"chats":[]}`))
	if s.remoteChatUnavailable("node", "live") {
		t.Fatal("inventory race permanently hid a live chat")
	}
}
