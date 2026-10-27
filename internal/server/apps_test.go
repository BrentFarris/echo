package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/brent/echo/internal/domain"
)

func installTestApp(t *testing.T, s *Server, id string) domain.PluginManifest {
	t.Helper()
	m, err := s.Plugins.Install(domain.PluginManifest{API: 1, ID: id, Name: id, Views: []domain.View{{ID: "renderer", Kind: "renderer"}, {ID: "main", Kind: "floating", Title: id, HTML: "<p>App</p>"}}, App: &domain.AppRegistration{ViewID: "main"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAppDockPersistenceAndConcurrentUpdates(t *testing.T) {
	guest, err := os.ReadFile("../plugins/guest/builtin.wasm")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	s, err := New(dir, nil, guest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if s != nil {
			s.Close()
		}
	}()
	first := installTestApp(t, s, "first")
	if ids, err := s.setAppDock(first.ID, true); err != nil || !slices.Equal(ids, []string{"first"}) {
		t.Fatal(ids, err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		id := fmt.Sprintf("app-%d", i)
		installTestApp(t, s, id)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.setAppDock(id, true); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	ids, err := s.setAppDock(first.ID, true)
	if err != nil || len(ids) != 9 || ids[0] != "first" {
		t.Fatal("lost/reordered/duplicate dock entries", ids, err)
	}
	s.Close()
	s = nil
	s, err = New(dir, nil, guest)
	if err != nil {
		t.Fatal(err)
	}
	reloaded, err := s.appDock()
	if err != nil || !slices.Equal(ids, reloaded) {
		t.Fatal("dock did not survive restart", reloaded, err)
	}
	first.Enabled = false
	first.App = nil
	if err := s.Plugins.Save(first); err != nil {
		t.Fatal(err)
	}
	retained, _ := s.appDock()
	if !slices.Equal(ids, retained) {
		t.Fatal("unavailable pin was removed")
	}
	if _, err := s.setAppDock(first.ID, true); err == nil {
		t.Fatal("unavailable app accepted a new dock request")
	}
	undocked, err := s.setAppDock(first.ID, false)
	if err != nil || slices.Contains(undocked, first.ID) {
		t.Fatal("unavailable pin could not be removed", err)
	}
}

func TestAppHTTPAvailabilityAndDockAuthentication(t *testing.T) {
	s := testServer(t)
	setRemotePassword(t, s)
	owner, device := remoteLogin(t, s), remoteLogin(t, s)
	m := installTestApp(t, s, "calculator")
	path := "/plugins/calculator/app/dock"
	if w := remoteRequest(s, "PUT", path, `{"docked":true}`, nil); w.Code != 401 {
		t.Fatal("unauthenticated dock mutation", w.Code)
	}
	r := httptest.NewRequest("PUT", "https://echo.example/api/v1"+path, strings.NewReader(`{"docked":true}`))
	r.RemoteAddr = "192.0.2.10:1234"
	r.Header.Set("Origin", "https://foreign.example")
	r.AddCookie(owner)
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatal("foreign origin accepted", w.Code)
	}
	if w := remoteRequest(s, "PUT", path, `{}`, owner); w.Code != 400 {
		t.Fatal("missing docked accepted")
	}
	if w := remoteRequest(s, "PUT", path, `{"docked":true}`, owner); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = remoteRequest(s, "GET", "/state", "", device)
	var state struct {
		AppDock []string `json:"appDock"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil || !slices.Equal(state.AppDock, []string{m.ID}) {
		t.Fatal("dock not shared with second session", w.Body.String(), err)
	}
	viewPath := "/plugins/calculator/views/main"
	w = remoteRequest(s, "GET", viewPath, "", owner)
	var view struct{ ID, Digest string }
	if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil || w.Code != 200 || view.ID != "main" || view.Digest != m.Digest {
		t.Fatal(w.Body.String(), err)
	}
	for _, path := range []string{"/plugins/calculator/views/renderer", viewPath + "?botId=missing"} {
		if w := remoteRequest(s, "GET", path, "", owner); w.Code != 400 {
			t.Fatal("invalid launch accepted", path)
		}
	}
	echo, _ := s.Bot("echo")
	echo.Plugins[m.ID] = false
	s.Store.Put("bots", echo.ID, echo)
	if w := remoteRequest(s, "GET", viewPath, "", owner); w.Code != 400 {
		t.Fatal("bot override ignored")
	}
	m.Scope = "app"
	s.Plugins.Save(m)
	if w := remoteRequest(s, "GET", viewPath, "", owner); w.Code != 200 {
		t.Fatal("app scope incorrectly used a bot override", w.Body.String())
	}
	for _, pending := range []bool{false, true} {
		m.Enabled = pending
		m.Pending = pending
		s.Plugins.Save(m)
		if w := remoteRequest(s, "GET", viewPath, "", owner); w.Code != 400 {
			t.Fatal("inactive view source returned")
		}
		if w := remoteRequest(s, "PUT", path, `{"docked":true}`, owner); w.Code != 400 {
			t.Fatal("inactive app docked")
		}
	}
	if w := remoteRequest(s, "PUT", path, `{"docked":false}`, owner); w.Code != 200 {
		t.Fatal("inactive app cannot be undocked")
	}
}

func TestAppAuthoringRegistrationAndLaunchEvents(t *testing.T) {
	s := testServer(t)
	echo, _ := s.Bot("echo")
	scope := domain.ExecutionScope{Bot: echo, PluginID: "plugin-authoring"}
	value, err := s.pluginCall(context.Background(), scope, "plugin.install", json.RawMessage(`{"id":"calculator","name":"Calculator","html":"<p>Calculator</p>"}`))
	if err != nil {
		t.Fatal(err)
	}
	m := value.(domain.PluginManifest)
	if m.App == nil || m.App.ViewID != "main" {
		t.Fatal("HTML-only install did not register its app", m)
	}
	value, err = s.pluginCall(context.Background(), scope, "plugin.install", json.RawMessage(`{"manifest":{"api":1,"id":"unlisted","name":"Unlisted","views":[{"id":"main","kind":"floating","html":"<p>Utility</p>"}]}}`))
	if err != nil || value.(domain.PluginManifest).App != nil {
		t.Fatal("full manifest implicitly registered an app", err)
	}
	m = installTestApp(t, s, "launch-target")
	events := make(chan domain.Event, 10)
	s.mu.Lock()
	s.subscribers[events] = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.subscribers, events); s.mu.Unlock() }()
	if _, err := s.pluginCall(context.Background(), scope, "plugin.open", json.RawMessage(`{"id":"launch-target"}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		var payload struct{ ID, ViewID string }
		json.Unmarshal(event.Payload, &payload)
		if event.Type != "plugin-open" || payload.ID != m.ID || payload.ViewID != "main" {
			t.Fatal(event)
		}
	default:
		t.Fatal("no launch event")
	}
	if _, err := s.pluginCall(context.Background(), scope, "plugin.open", json.RawMessage(`{"id":"launch-target","viewId":"renderer"}`)); err == nil {
		t.Fatal("renderer launched")
	}
}
