package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/gorilla/websocket"
)

// newTestServer builds a Server with a temp web dir containing an index.html
// and an isolated temp settings path so tests never touch the real config.
func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html><body>index</body></html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	settingsPath := filepath.Join(dir, "echo.json")
	s := NewWithSettingsPath("127.0.0.1:0", dir, settingsPath)
	return s, dir
}

func doRequest(t *testing.T, s *Server, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	return rr
}

func TestIndexServed(t *testing.T) {
	s, _ := newTestServer(t)
	rr := doRequest(t, s, http.MethodGet, "/")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "index") {
		t.Fatalf("expected index body, got %q", rr.Body.String())
	}
}

func TestSPAFallback(t *testing.T) {
	s, _ := newTestServer(t)
	// Unknown non-API path should fall back to index.html for client-side routing.
	rr := doRequest(t, s, http.MethodGet, "/some/client/route")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 for SPA fallback, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "index") {
		t.Fatalf("expected index body on fallback, got %q", rr.Body.String())
	}
}

func TestHealthEndpoint(t *testing.T) {
	s, _ := newTestServer(t)
	rr := doRequest(t, s, http.MethodGet, "/api/health")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.OK {
		t.Fatalf("expected ok=true")
	}
	if env.Data.Status != "ok" {
		t.Fatalf("expected status ok, got %q", env.Data.Status)
	}
}

func TestEchoEndpoint(t *testing.T) {
	s, _ := newTestServer(t)
	rr := doRequest(t, s, http.MethodGet, "/api/echo?message=hi")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.Data.Message != "hi" {
		t.Fatalf("expected message hi, got %q", env.Data.Message)
	}
}

func TestGetSettings(t *testing.T) {
	s, _ := newTestServer(t)
	rr := doRequest(t, s, http.MethodGet, "/api/settings")
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Settings llm.Settings `json:"settings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !env.OK {
		t.Fatalf("expected ok=true")
	}
	if len(env.Data.Settings.Endpoints) == 0 {
		t.Fatalf("expected default endpoints")
	}
}

func TestPutSettingsRoundTrip(t *testing.T) {
	s, _ := newTestServer(t)

	// Build a settings payload with a custom endpoint.
	cfg := llm.DefaultSettings()
	cfg.Endpoints = append(cfg.Endpoints, llm.LLMEndpoint{
		ID:       "second",
		Name:     "Second",
		Endpoint: "http://example.com/v1",
		Model:    "model-b",
	})
	cfg.EndpointSelection.Chat = "second"

	body, err := json.Marshal(map[string]any{"settings": cfg})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	// Now GET should reflect the saved settings.
	rr2 := doRequest(t, s, http.MethodGet, "/api/settings")
	var env struct {
		Data struct {
			Settings llm.Settings `json:"settings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Data.Settings.Endpoints) != 2 {
		t.Fatalf("expected 2 endpoints, got %d", len(env.Data.Settings.Endpoints))
	}
	if env.Data.Settings.EndpointSelection.Chat != "second" {
		t.Fatalf("expected chat routing to second, got %q", env.Data.Settings.EndpointSelection.Chat)
	}
}

func TestPutSettingsRejectsInvalid(t *testing.T) {
	s, _ := newTestServer(t)
	// An invalid endpoint URL should fail validation. Build a minimal payload
	// matching what the frontend sends (endpoints + endpointSelection only).
	cfg := llm.Settings{
		Endpoints: []llm.LLMEndpoint{{
			ID:       "x",
			Name:     "Bad",
			Endpoint: "not-a-url",
			Model:    "m",
		}},
		EndpointSelection: llm.EndpointSelection{Chat: "x", Research: "x", Vision: "x", InlineCode: "x"},
	}
	body, err := json.Marshal(map[string]any{"settings": cfg})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestWebSocketUpgradeAndWelcome(t *testing.T) {
	s, _ := newTestServer(t)

	// Start a real listener so the WebSocket client can connect.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go s.httpServer.Serve(ln)

	wsURL := "ws://" + ln.Addr().String() + "/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Expect a welcome event shortly after connecting.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, msg, err := conn.ReadMessage()
	if err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	var evt map[string]any
	if err := json.Unmarshal(msg, &evt); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if evt["type"] != "welcome" {
		t.Fatalf("expected welcome event, got %v", evt["type"])
	}
}

func TestChatStreamingOverWebSocket(t *testing.T) {
	s, _ := newTestServer(t)

	// Inject a fake streamer that emits a token, a complete, then closes.
	fake := &fakeStreamer{}
	s.llm = fake

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go s.httpServer.Serve(ln)

	conn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Consume the welcome event.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read welcome: %v", err)
	}

	// Send a chat message.
	if err := conn.WriteJSON(map[string]any{"type": "chat", "message": "hello"}); err != nil {
		t.Fatalf("write chat: %v", err)
	}

	// Expect chat_start, a token event, and chat_done.
	var got []string
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var evt map[string]any
		if err := conn.ReadJSON(&evt); err != nil {
			t.Fatalf("read event: %v", err)
		}
		typ, _ := evt["type"].(string)
		got = append(got, typ)
		if typ == "chat_done" {
			break
		}
	}
	joined := strings.Join(got, ",")
	if !strings.Contains(joined, "chat_start") {
		t.Fatalf("expected chat_start, got %v", got)
	}
	if !strings.Contains(joined, "chat_event") {
		t.Fatalf("expected chat_event, got %v", got)
	}
	if !strings.Contains(joined, "chat_done") {
		t.Fatalf("expected chat_done, got %v", got)
	}
}

func TestChatRoutesToSelectedModel(t *testing.T) {
	s, _ := newTestServer(t)

	// Configure a second endpoint with a distinct model and route chat to it.
	cfg := llm.DefaultSettings()
	cfg.Endpoints = append(cfg.Endpoints, llm.LLMEndpoint{
		ID:       "second",
		Name:     "Second",
		Endpoint: "http://example.com/v1",
		Model:    "model-b",
	})
	if err := s.store.Save(cfg); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	s.initLLM()

	// Inject a capturing streamer so we can inspect the routed model.
	capturing := &capturingStreamer{}
	s.llm = capturing

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go s.httpServer.Serve(ln)

	conn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// Consume the welcome event.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read welcome: %v", err)
	}

	// Send a chat message with the second endpoint's model selected.
	if err := conn.WriteJSON(map[string]any{"type": "chat", "message": "hello", "model": "model-b"}); err != nil {
		t.Fatalf("write chat: %v", err)
	}

	// Drain until chat_done.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var evt map[string]any
		if err := conn.ReadJSON(&evt); err != nil {
			t.Fatalf("read event: %v", err)
		}
		if evt["type"] == "chat_done" {
			break
		}
	}

	if got := capturing.lastRequest().Model; got != "model-b" {
		t.Fatalf("expected routed model model-b, got %q", got)
	}
}

func TestChatFallsBackToDefaultModelWhenUnknown(t *testing.T) {
	s, _ := newTestServer(t)

	cfg := llm.DefaultSettings()
	cfg.Endpoints = append(cfg.Endpoints, llm.LLMEndpoint{
		ID:       "second",
		Name:     "Second",
		Endpoint: "http://example.com/v1",
		Model:    "model-b",
	})
	if err := s.store.Save(cfg); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	s.initLLM()

	capturing := &capturingStreamer{}
	s.llm = capturing

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go s.httpServer.Serve(ln)

	conn, _, err := websocket.DefaultDialer.Dial("ws://"+ln.Addr().String()+"/ws", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read welcome: %v", err)
	}

	// Send a chat message with a model that does not exist on any endpoint.
	if err := conn.WriteJSON(map[string]any{"type": "chat", "message": "hello", "model": "does-not-exist"}); err != nil {
		t.Fatalf("write chat: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var evt map[string]any
		if err := conn.ReadJSON(&evt); err != nil {
			t.Fatalf("read event: %v", err)
		}
		if evt["type"] == "chat_done" {
			break
		}
	}

	// The default chat endpoint's model should be used.
	defaultModel := llm.DefaultModel
	if got := capturing.lastRequest().Model; got != defaultModel {
		t.Fatalf("expected fallback model %q, got %q", defaultModel, got)
	}
}

// fakeStreamer is a minimal chatStreamer for tests.
type fakeStreamer struct{}

func (f *fakeStreamer) StreamChat(_ context.Context, _ llm.ChatRequest) *llm.Stream {
	events := make(chan llm.StreamEvent, 4)
	events <- llm.StreamEvent{Type: llm.EventToken, Content: "hello"}
	events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	close(events)
	return &llm.Stream{ID: "fake", Events: events}
}

// capturingStreamer records the last ChatRequest it received so tests can
// assert which model the chat handler routed the prompt to.
type capturingStreamer struct {
	mu      sync.Mutex
	request llm.ChatRequest
}

func (c *capturingStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
	c.mu.Lock()
	c.request = request
	c.mu.Unlock()
	events := make(chan llm.StreamEvent, 4)
	events <- llm.StreamEvent{Type: llm.EventToken, Content: "hello"}
	events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	close(events)
	return &llm.Stream{ID: "fake", Events: events}
}

func (c *capturingStreamer) lastRequest() llm.ChatRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.request
}
