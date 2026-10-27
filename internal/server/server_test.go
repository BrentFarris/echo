package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/domain"
	"github.com/brent/echo/internal/model"
)

func testServer(t *testing.T) *Server {
	t.Helper()
	guest, e := os.ReadFile("../plugins/guest/builtin.wasm")
	if e != nil {
		t.Fatal(e)
	}
	s, e := New(t.TempDir(), nil, guest)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(s.Close)
	return s
}
func stream(w http.ResponseWriter, content string, calls []domain.ToolCall) {
	w.Header().Set("Content-Type", "text/event-stream")
	delta := map[string]any{"content": content}
	if len(calls) > 0 {
		cs := []any{}
		for i, c := range calls {
			cs = append(cs, map[string]any{"index": i, "id": c.ID, "function": c.Function})
		}
		delta["tool_calls"] = cs
	}
	data, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta, "finish_reason": "stop"}}})
	fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", data)
}
func attachModel(t *testing.T, s *Server, handler http.HandlerFunc) domain.Generation {
	t.Helper()
	m := httptest.NewServer(handler)
	t.Cleanup(m.Close)
	ep := domain.Endpoint{ID: "model", Name: "Test", URL: m.URL, Model: "test-model", ContextTokens: 32768}
	s.Store.Put("endpoints", ep.ID, ep)
	return domain.Generation{EndpointID: ep.ID, Model: ep.Model, Temperature: .6}
}
func chat(t *testing.T, s *Server, g domain.Generation) domain.Chat {
	t.Helper()
	c := domain.Chat{ID: domain.ID(), Title: "New chat", Bots: map[string]string{}, Generation: g, CreatedAt: domain.Now(), UpdatedAt: domain.Now()}
	if e := s.Store.CreateChat(c); e != nil {
		t.Fatal(e)
	}
	return c
}
func waitDispatch(t *testing.T, d *dispatch) {
	t.Helper()
	select {
	case <-d.done:
	case <-time.After(20 * time.Second):
		d.cancel()
		t.Fatal("dispatch did not finish")
	}
}

func TestBotCreationThroughToolLoop(t *testing.T) {
	s := testServer(t)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages []model.Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&request)
		last := request.Messages[len(request.Messages)-1]
		if last.Role == "tool" {
			stream(w, "Created kaiju.", nil)
			return
		}
		args, _ := json.Marshal(map[string]any{"handle": "kaiju", "name": "Kaiju", "prompt": "Build the game"})
		stream(w, "", []domain.ToolCall{{ID: "create", Type: "function", Function: domain.FunctionCall{Name: "bot_save", Arguments: string(args)}}})
	})
	c := chat(t, s, g)
	d, e := s.send(c.ID, SendRequest{Content: "Create a bot named kaiju", Generation: g})
	if e != nil {
		t.Fatal(e)
	}
	waitDispatch(t, d)
	bots, _ := s.Bots()
	if len(bots) != 2 {
		ms, _ := s.messages(c.ID)
		t.Fatal("bot was not created", ms)
	}
	ms, e := s.messages(c.ID)
	if e != nil || len(ms) != 4 || ms[3].Content != "Created kaiju." {
		t.Fatal(ms, e)
	}
}
func TestRoutingFileUnionAndIndependentContexts(t *testing.T) {
	s := testServer(t)
	aRoot := t.TempDir()
	bRoot := t.TempDir()
	path := filepath.Join(aRoot, "map_editor.go")
	os.WriteFile(path, []byte("private workspace"), 0600)
	a, _ := s.saveBot(domain.Bot{Handle: "kaiju", Name: "Kaiju", Roots: []string{aRoot}})
	b, _ := s.saveBot(domain.Bot{Handle: "backend", Name: "Backend", Roots: []string{bRoot}})
	seen := make(chan []model.Message, 4)
	g := attachModel(t, s, func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Messages []model.Message `json:"messages"`
		}
		json.NewDecoder(r.Body).Decode(&input)
		seen <- input.Messages
		stream(w, "Done", nil)
	})
	c := chat(t, s, g)
	d, e := s.send(c.ID, SendRequest{Content: "Please @kaiju work with @backend on #map_editor", Segments: []domain.Segment{{Type: "file", Path: path}}, Generation: g})
	if e != nil {
		t.Fatal(e)
	}
	waitDispatch(t, d)
	for i := 0; i < 2; i++ {
		ms := <-seen
		serialized, _ := json.Marshal(ms)
		isA := strings.Contains(ms[0].Content, "@kaiju.")
		if strings.Contains(string(serialized), "Referenced path") != isA {
			t.Fatal("file reference leaked across bot scope", string(serialized))
		}
	}
	c, _ = s.Store.Chat(c.ID)
	if c.Bots[a.ID] != "Kaiju" || c.Bots[b.ID] != "Backend" {
		t.Fatal(c)
	}
	d, e = s.send(c.ID, SendRequest{Content: "List my bots", Generation: g})
	if e != nil {
		t.Fatal(e)
	}
	waitDispatch(t, d)
	ms := <-seen
	if !strings.Contains(ms[0].Content, "@echo.") || len(ms) != 2 {
		t.Fatal("unmentioned followup did not route privately to Echo", ms)
	}
	if _, e = s.send(c.ID, SendRequest{Content: "@missing do work", Generation: g}); e == nil {
		t.Fatal("unknown bot accepted")
	}
}
func TestPluginGenerationAndNamespace(t *testing.T) {
	s := testServer(t)
	echo, _ := s.Bot("echo")
	scope := domain.ExecutionScope{Bot: echo, PluginID: "plugin-authoring"}
	args := json.RawMessage(`{"id":"calculator","name":"Calculator","html":"<input id='number'><button onclick='document.querySelector(\"#number\").value=4'>2 + 2</button>"}`)
	value, e := s.pluginCall(context.Background(), scope, "plugin.install", args)
	if e != nil {
		t.Fatal(e)
	}
	data, _ := json.Marshal(value)
	if !strings.Contains(string(data), `"enabled":true`) {
		t.Fatal(value)
	}
	m, _ := s.Plugins.Get("calculator")
	if len(m.Views) != 1 || m.Pending {
		t.Fatal(m)
	}
	if _, e = s.pluginCall(context.Background(), domain.ExecutionScope{Bot: echo, PluginID: "calculator"}, "plugin.list", json.RawMessage(`{}`)); e == nil {
		t.Fatal("cross-plugin privileged capability accepted")
	}
}
func TestOriginAuthenticationAndRemoteGate(t *testing.T) {
	s := testServer(t)
	req := httptest.NewRequest("GET", "http://localhost/api/v1/state", nil)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatal(rr.Code)
	}
	req = httptest.NewRequest("POST", "http://localhost/api/v1/session", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Origin", "https://attacker.invalid")
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 403 {
		t.Fatal("cross-origin owner bootstrap accepted", rr.Code)
	}
	m, _ := s.Plugins.Get("hosted-server")
	m.Config["remote"] = true
	s.Store.Put("plugins", m.ID, m)
	if _, _, _, e := s.ListenAddress(3740); e == nil {
		t.Fatal("remote server enabled without password")
	}
}
