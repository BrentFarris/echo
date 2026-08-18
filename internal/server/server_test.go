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
	"github.com/brent/echo/internal/workspaces"
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

func TestPutSettingsExternalConnectionsRoundTrip(t *testing.T) {
	s, _ := newTestServer(t)

	// Persist external connection values exactly as the frontend sends them.
	cfg := llm.DefaultSettings()
	cfg.SearxngURL = "http://searxng.example:8080/"
	cfg.ComfyuiURL = "http://comfy.example:8188"
	cfg.ComfyuiTxt2imgWorkflow = "workflows/txt2img.json"
	cfg.ComfyuiImg2imgWorkflow = "workflows/img2img.json"

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

	// GET should reflect the saved external connection values.
	rr2 := doRequest(t, s, http.MethodGet, "/api/settings")
	var env struct {
		Data struct {
			Settings llm.Settings `json:"settings"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rr2.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := env.Data.Settings
	if got.SearxngURL != cfg.SearxngURL {
		t.Fatalf("expected searxngUrl %q, got %q", cfg.SearxngURL, got.SearxngURL)
	}
	if got.ComfyuiURL != cfg.ComfyuiURL {
		t.Fatalf("expected comfyuiUrl %q, got %q", cfg.ComfyuiURL, got.ComfyuiURL)
	}
	if got.ComfyuiTxt2imgWorkflow != cfg.ComfyuiTxt2imgWorkflow {
		t.Fatalf("expected comfyuiTxt2imgWorkflow %q, got %q", cfg.ComfyuiTxt2imgWorkflow, got.ComfyuiTxt2imgWorkflow)
	}
	if got.ComfyuiImg2imgWorkflow != cfg.ComfyuiImg2imgWorkflow {
		t.Fatalf("expected comfyuiImg2imgWorkflow %q, got %q", cfg.ComfyuiImg2imgWorkflow, got.ComfyuiImg2imgWorkflow)
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

// toolCallingStreamer emits a filesystem_list tool call on the first request,
// then a final answer on subsequent requests. It records each request so tests
// can assert the tool result was fed back into the conversation.
type toolCallingStreamer struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
	path     string
}

func (f *toolCallingStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	callCount := len(f.requests)
	f.mu.Unlock()

	events := make(chan llm.StreamEvent, 8)
	if callCount == 1 {
		events <- llm.StreamEvent{
			Type: llm.EventToolCall,
			ToolCall: &llm.ToolCallDelta{
				Index: 0,
				ID:    "call-1",
				Type:  "function",
				Function: llm.FunctionCallDelta{
					Name:      "filesystem_list",
					Arguments: `{"path":"` + f.path + `"}`,
				},
			},
		}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
	} else {
		events <- llm.StreamEvent{Type: llm.EventToken, Content: "Here are the files."}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	}
	close(events)
	return &llm.Stream{ID: "fake", Events: events}
}

func (f *toolCallingStreamer) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func (f *toolCallingStreamer) lastRequest() llm.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return llm.ChatRequest{}
	}
	return f.requests[len(f.requests)-1]
}

func TestChatToolCallingExecutesFilesystemList(t *testing.T) {
	s, _ := newTestServer(t)

	// Create a workspace whose main folder holds a file, and make it active so
	// the tool context resolves to a real directory.
	wsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(wsDir, "notes.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := s.workspaces.Create(workspaces.CreateRequest{
		Name:     "tool-ws",
		MainPath: wsDir,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := s.workspaces.SetActive(ws.ID); err != nil {
		t.Fatalf("set active: %v", err)
	}

	// Inject a streamer that requests filesystem_list then answers. The label
	// is the normalized base name of the workspace folder.
	fake := &toolCallingStreamer{path: normalizeWorkspaceFolderLabel(filepath.Base(wsDir))}
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

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read welcome: %v", err)
	}

	if err := conn.WriteJSON(map[string]any{"type": "chat", "message": "list the workspace"}); err != nil {
		t.Fatalf("write chat: %v", err)
	}

	var toolCallSeen, toolResultSeen, doneSeen bool
	for {
		var evt map[string]any
		if err := conn.ReadJSON(&evt); err != nil {
			t.Fatalf("read event: %v", err)
		}
		typ, _ := evt["type"].(string)
		if typ == "chat_event" {
			eventType, _ := evt["eventType"].(string)
			if eventType == "tool_call" {
				toolCallSeen = true
			}
			if eventType == "tool_result" {
				toolResultSeen = true
				if tool, _ := evt["tool"].(string); tool != "filesystem_list" {
					t.Fatalf("expected filesystem_list tool result, got %q", tool)
				}
			}
		}
		if typ == "chat_done" {
			doneSeen = true
			break
		}
		if typ == "chat_error" {
			t.Fatalf("unexpected chat_error: %v", evt)
		}
	}

	if !toolCallSeen {
		t.Fatal("expected a tool_call event")
	}
	if !toolResultSeen {
		t.Fatal("expected a tool_result event")
	}
	if !doneSeen {
		t.Fatal("expected chat_done")
	}

	// The loop must have made two requests: the initial turn and the follow-up
	// after the tool result was fed back.
	if fake.requestCount() != 2 {
		t.Fatalf("expected two stream requests, got %d", fake.requestCount())
	}
	// The final request must include the tool result message.
	last := fake.lastRequest()
	foundToolResult := false
	for _, m := range last.Messages {
		if m.Role == llm.RoleTool && m.ToolCallID == "call-1" {
			foundToolResult = true
			if !strings.Contains(m.Content, `"success":true`) {
				t.Fatalf("expected successful tool result, got %q", m.Content)
			}
			if !strings.Contains(m.Content, "notes.txt") {
				t.Fatalf("expected notes.txt in tool result, got %q", m.Content)
			}
		}
	}
	if !foundToolResult {
		t.Fatal("expected a tool result message in the follow-up request")
	}
}

// imageToolStreamer emits a filesystem_read_image tool call on the first
// request, then a final answer on subsequent requests.
type imageToolStreamer struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
	path     string
}

func (f *imageToolStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	callCount := len(f.requests)
	f.mu.Unlock()

	events := make(chan llm.StreamEvent, 8)
	if callCount == 1 {
		events <- llm.StreamEvent{
			Type: llm.EventToolCall,
			ToolCall: &llm.ToolCallDelta{
				Index: 0,
				ID:    "call-1",
				Type:  "function",
				Function: llm.FunctionCallDelta{
					Name:      "filesystem_read_image",
					Arguments: `{"path":"` + f.path + `"}`,
				},
			},
		}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
	} else {
		events <- llm.StreamEvent{Type: llm.EventToken, Content: "Here is the image."}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	}
	close(events)
	return &llm.Stream{ID: "fake", Events: events}
}

func (f *imageToolStreamer) lastRequest() llm.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return llm.ChatRequest{}
	}
	return f.requests[len(f.requests)-1]
}

func TestChatToolCallingFeedsImageToModel(t *testing.T) {
	s, _ := newTestServer(t)

	wsDir := t.TempDir()
	// A minimal valid PNG header so filesystem_read_image detects image/png.
	pngBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 0, 0, 0, 0}
	if err := os.WriteFile(filepath.Join(wsDir, "pic.png"), pngBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := s.workspaces.Create(workspaces.CreateRequest{
		Name:     "img-ws",
		MainPath: wsDir,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := s.workspaces.SetActive(ws.ID); err != nil {
		t.Fatalf("set active: %v", err)
	}

	fake := &imageToolStreamer{path: normalizeWorkspaceFolderLabel(filepath.Base(wsDir)) + "/pic.png"}
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

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read welcome: %v", err)
	}

	if err := conn.WriteJSON(map[string]any{"type": "chat", "message": "read the image"}); err != nil {
		t.Fatalf("write chat: %v", err)
	}

	var doneSeen bool
	for {
		var evt map[string]any
		if err := conn.ReadJSON(&evt); err != nil {
			t.Fatalf("read event: %v", err)
		}
		typ, _ := evt["type"].(string)
		if typ == "chat_done" {
			doneSeen = true
			break
		}
		if typ == "chat_error" {
			t.Fatalf("unexpected chat_error: %v", evt)
		}
	}
	if !doneSeen {
		t.Fatal("expected chat_done")
	}

	// The follow-up request must carry the image as a user content part so the
	// model can see it.
	last := fake.lastRequest()
	var foundImage bool
	for _, m := range last.Messages {
		if m.Role == llm.RoleUser && len(m.ContentParts) > 0 {
			for _, part := range m.ContentParts {
				if part.Type == "image_url" && part.ImageURL != nil && strings.HasPrefix(part.ImageURL.URL, "data:image/png;base64,") {
					foundImage = true
				}
			}
		}
	}
	if !foundImage {
		t.Fatal("expected the image to be fed back to the model as an image_url content part")
	}
}

// videoToolStreamer emits a filesystem_read_video tool call on the first
// request, then a final answer on subsequent requests.
type videoToolStreamer struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
	path     string
}

func (f *videoToolStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	callCount := len(f.requests)
	f.mu.Unlock()

	events := make(chan llm.StreamEvent, 8)
	if callCount == 1 {
		events <- llm.StreamEvent{
			Type: llm.EventToolCall,
			ToolCall: &llm.ToolCallDelta{
				Index: 0,
				ID:    "call-1",
				Type:  "function",
				Function: llm.FunctionCallDelta{
					Name:      "filesystem_read_video",
					Arguments: `{"path":"` + f.path + `"}`,
				},
			},
		}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
	} else {
		events <- llm.StreamEvent{Type: llm.EventToken, Content: "Here is the video."}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	}
	close(events)
	return &llm.Stream{ID: "fake", Events: events}
}

func (f *videoToolStreamer) lastRequest() llm.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return llm.ChatRequest{}
	}
	return f.requests[len(f.requests)-1]
}

func TestChatToolCallingFeedsVideoToModel(t *testing.T) {
	s, _ := newTestServer(t)

	wsDir := t.TempDir()
	// A minimal MP4 ftyp header so filesystem_read_video detects video/mp4.
	mp4Bytes := []byte{0x00, 0x00, 0x00, 0x20, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm'}
	if err := os.WriteFile(filepath.Join(wsDir, "clip.mp4"), mp4Bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := s.workspaces.Create(workspaces.CreateRequest{
		Name:     "vid-ws",
		MainPath: wsDir,
	})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := s.workspaces.SetActive(ws.ID); err != nil {
		t.Fatalf("set active: %v", err)
	}

	fake := &videoToolStreamer{path: normalizeWorkspaceFolderLabel(filepath.Base(wsDir)) + "/clip.mp4"}
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

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, _, err := conn.ReadMessage(); err != nil {
		t.Fatalf("read welcome: %v", err)
	}

	if err := conn.WriteJSON(map[string]any{"type": "chat", "message": "read the video"}); err != nil {
		t.Fatalf("write chat: %v", err)
	}

	var doneSeen bool
	for {
		var evt map[string]any
		if err := conn.ReadJSON(&evt); err != nil {
			t.Fatalf("read event: %v", err)
		}
		typ, _ := evt["type"].(string)
		if typ == "chat_done" {
			doneSeen = true
			break
		}
		if typ == "chat_error" {
			t.Fatalf("unexpected chat_error: %v", evt)
		}
	}
	if !doneSeen {
		t.Fatal("expected chat_done")
	}

	// The follow-up request must carry the video as a user content part so the
	// model can see it.
	last := fake.lastRequest()
	var foundVideo bool
	for _, m := range last.Messages {
		if m.Role == llm.RoleUser && len(m.ContentParts) > 0 {
			for _, part := range m.ContentParts {
				if part.Type == "video_url" && part.VideoURL != nil && strings.HasPrefix(part.VideoURL.URL, "data:video/mp4;base64,") {
					foundVideo = true
				}
			}
		}
	}
	if !foundVideo {
		t.Fatal("expected the video to be fed back to the model as a video_url content part")
	}
}

