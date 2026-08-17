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
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/gorilla/websocket"
)

// newTestServer builds a Server with a temp web dir containing an index.html.
func newTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html><body>index</body></html>"), 0o644); err != nil {
		t.Fatalf("write index: %v", err)
	}
	s := New("127.0.0.1:0", dir)
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

// fakeStreamer is a minimal chatStreamer for tests.
type fakeStreamer struct{}

func (f *fakeStreamer) StreamChat(_ context.Context, _ llm.ChatRequest) *llm.Stream {
	events := make(chan llm.StreamEvent, 4)
	events <- llm.StreamEvent{Type: llm.EventToken, Content: "hello"}
	events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	close(events)
	return &llm.Stream{ID: "fake", Events: events}
}
