package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
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
	s.authDisabled = true
	return s, dir
}

func createChatWorkspace(t *testing.T, s *Server, name string) workspaces.Workspace {
	t.Helper()
	workspace, err := s.workspaces.Create(workspaces.CreateRequest{Name: name, MainPath: t.TempDir()})
	if err != nil {
		t.Fatalf("create chat workspace: %v", err)
	}
	return workspace
}

func subscribeChat(t *testing.T, conn *websocket.Conn, workspaceID string) {
	t.Helper()
	if err := conn.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": workspaceID}); err != nil {
		t.Fatalf("subscribe chat: %v", err)
	}
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		var message map[string]any
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatalf("read session snapshot: %v", err)
		}
		if message["type"] == "session_snapshot" {
			return
		}
	}
}

func sendSharedChat(t *testing.T, conn *websocket.Conn, workspaceID, message, model string) {
	t.Helper()
	subscribeChat(t, conn, workspaceID)
	payload := map[string]any{
		"type": "chat_send", "workspaceId": workspaceID,
		"requestId": "request-" + strings.ReplaceAll(message, " ", "-"), "message": message,
	}
	if model != "" {
		payload["model"] = model
	}
	if err := conn.WriteJSON(payload); err != nil {
		t.Fatalf("write shared chat: %v", err)
	}
}

// readCompatChatEvent maps the session protocol to the old assertion shape so
// the detailed tool-loop expectations remain concise.
func readCompatChatEvent(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	for {
		var message map[string]any
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatalf("read event: %v", err)
		}
		if message["type"] != "session_event" {
			if message["type"] == "command_error" {
				return map[string]any{"type": "chat_error", "error": message["error"]}
			}
			continue
		}
		event, _ := message["event"].(map[string]any)
		eventType, _ := event["type"].(string)
		switch eventType {
		case "turn_started":
			event["type"] = "chat_start"
		case "turn_finished":
			status, _ := event["status"].(string)
			switch status {
			case "done":
				event["type"] = "chat_done"
			case "stopped":
				event["type"] = "chat_stopped"
			default:
				event["type"] = "chat_error"
			}
		default:
			event["type"] = "chat_event"
			event["eventType"] = eventType
		}
		return event
	}
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
	workspace := createChatWorkspace(t, s, "streaming")

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
	sendSharedChat(t, conn, workspace.ID, "hello", "")

	// Expect an explicitly bounded assistant turn around the token stream.
	var got []string
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		evt := readCompatChatEvent(t, conn)
		typ, _ := evt["type"].(string)
		label := typ
		if typ == "chat_event" {
			label, _ = evt["eventType"].(string)
			if turn, ok := evt["turn"].(float64); !ok || turn != 0 {
				t.Fatalf("expected turn 0 on %q, got %v", label, evt["turn"])
			}
			if label == "assistant_turn_end" && evt["hasToolCalls"] != false {
				t.Fatalf("expected direct response to end without tool calls, got %v", evt["hasToolCalls"])
			}
		}
		got = append(got, label)
		if typ == "chat_done" {
			break
		}
	}
	want := []string{"chat_start", "assistant_turn_start", "token", "assistant_turn_end", "chat_done"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected chat event order: got %v, want %v", got, want)
	}
}

// cancellableStreamer emits a token then blocks until its context is canceled,
// at which point it emits an EventCanceled and closes. It lets tests verify
// that a "stop" message cancels the in-progress stream.
type cancellableStreamer struct {
	started chan struct{}
}

func (f *cancellableStreamer) StreamChat(ctx context.Context, _ llm.ChatRequest) *llm.Stream {
	events := make(chan llm.StreamEvent, 4)
	events <- llm.StreamEvent{Type: llm.EventToken, Content: "partial"}
	if f.started != nil {
		close(f.started)
	}
	go func() {
		<-ctx.Done()
		events <- llm.StreamEvent{Type: llm.EventCanceled}
		close(events)
	}()
	return &llm.Stream{ID: "fake", Events: events}
}

func TestChatStopOverWebSocket(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "stopping")

	// Inject a streamer that blocks until canceled so we can exercise the
	// stop path deterministically.
	started := make(chan struct{})
	fake := &cancellableStreamer{started: started}
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

	// Send a chat message that will block streaming.
	sendSharedChat(t, conn, workspace.ID, "hello", "")

	// Wait for the stream to start (chat_start + first token), then send stop.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		evt := readCompatChatEvent(t, conn)
		if evt["type"] == "chat_start" {
			break
		}
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("stream never started")
	}

	// Send the stop message and expect chat_stopped (not chat_done/chat_error).
	if err := conn.WriteJSON(map[string]any{"type": "chat_stop", "workspaceId": workspace.ID}); err != nil {
		t.Fatalf("write stop: %v", err)
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var stoppedSeen bool
	for {
		evt := readCompatChatEvent(t, conn)
		typ, _ := evt["type"].(string)
		if typ == "chat_stopped" {
			stoppedSeen = true
			break
		}
		if typ == "chat_done" || typ == "chat_error" {
			t.Fatalf("expected chat_stopped, got %q", typ)
		}
	}
	if !stoppedSeen {
		t.Fatal("expected chat_stopped")
	}
}

func TestChatRoutesToSelectedModel(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "model-routing")

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
	sendSharedChat(t, conn, workspace.ID, "hello", "model-b")

	// Drain until chat_done.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		evt := readCompatChatEvent(t, conn)
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
	workspace := createChatWorkspace(t, s, "model-fallback")

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
	sendSharedChat(t, conn, workspace.ID, "hello", "does-not-exist")

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for {
		evt := readCompatChatEvent(t, conn)
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

	events := make(chan llm.StreamEvent, 10)
	if callCount == 1 {
		events <- llm.StreamEvent{Type: llm.EventReasoning, Content: "I should inspect the workspace."}
		events <- llm.StreamEvent{Type: llm.EventToken, Content: "I'll list the workspace first."}
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

	sendSharedChat(t, conn, ws.ID, "list the workspace", "")

	var toolCallSeen, toolResultSeen, doneSeen bool
	var eventOrder []string
	var emittedCallID string
	var turnEndToolFlags []bool
	for {
		evt := readCompatChatEvent(t, conn)
		typ, _ := evt["type"].(string)
		if typ == "chat_event" {
			eventType, _ := evt["eventType"].(string)
			eventOrder = append(eventOrder, eventType)
			if eventType == "assistant_turn_end" {
				turnEndToolFlags = append(turnEndToolFlags, evt["hasToolCalls"] == true)
			}
			if eventType == "tool_call" {
				toolCallSeen = true
				emittedCallID, _ = evt["callId"].(string)
				if emittedCallID != "call-1" {
					t.Fatalf("expected tool call ID call-1, got %q", emittedCallID)
				}
				if args, _ := evt["arguments"].(string); !strings.Contains(args, `"path"`) {
					t.Fatalf("expected complete tool arguments, got %q", args)
				}
				if order, ok := evt["callOrder"].(float64); !ok || order != 0 {
					t.Fatalf("expected call order 0, got %v", evt["callOrder"])
				}
			}
			if eventType == "tool_result" {
				toolResultSeen = true
				if tool, _ := evt["tool"].(string); tool != "filesystem_list" {
					t.Fatalf("expected filesystem_list tool result, got %q", tool)
				}
				if callID, _ := evt["callId"].(string); callID != emittedCallID {
					t.Fatalf("tool result call ID %q does not match %q", callID, emittedCallID)
				}
				if success, ok := evt["success"].(bool); !ok || !success {
					t.Fatalf("expected successful tool result, got %v", evt["success"])
				}
				if content, _ := evt["content"].(string); !strings.Contains(content, "notes.txt") {
					t.Fatalf("expected complete tool result content, got %q", content)
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
	wantOrder := []string{
		"assistant_turn_start", "reasoning", "token", "assistant_turn_end",
		"tool_call", "tool_result", "assistant_turn_start", "token",
		"assistant_turn_end",
	}
	if !reflect.DeepEqual(eventOrder, wantOrder) {
		t.Fatalf("unexpected tool-loop event order: got %v, want %v", eventOrder, wantOrder)
	}
	if !reflect.DeepEqual(turnEndToolFlags, []bool{true, false}) {
		t.Fatalf("unexpected turn completion flags: %v", turnEndToolFlags)
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

// multipleToolStreamer requests one valid and one unknown tool in the same
// turn, then emits a final answer. It exercises ordered call metadata, failure
// payloads, and the fallback ID used when a provider omits a call ID.
type multipleToolStreamer struct {
	mu       sync.Mutex
	requests int
	path     string
}

func (f *multipleToolStreamer) StreamChat(_ context.Context, _ llm.ChatRequest) *llm.Stream {
	f.mu.Lock()
	f.requests++
	requestNumber := f.requests
	f.mu.Unlock()

	events := make(chan llm.StreamEvent, 8)
	if requestNumber == 1 {
		events <- llm.StreamEvent{Type: llm.EventToolCall, ToolCall: &llm.ToolCallDelta{
			Index: 0,
			ID:    "call-good",
			Type:  "function",
			Function: llm.FunctionCallDelta{
				Name:      "filesystem_list",
				Arguments: `{"path":"` + f.path + `"}`,
			},
		}}
		events <- llm.StreamEvent{Type: llm.EventToolCall, ToolCall: &llm.ToolCallDelta{
			Index: 1,
			Type:  "function",
			Function: llm.FunctionCallDelta{
				Name:      "missing_tool",
				Arguments: `{"value":42}`,
			},
		}}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
	} else {
		events <- llm.StreamEvent{Type: llm.EventToken, Content: "Finished both tool calls."}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	}
	close(events)
	return &llm.Stream{ID: "fake", Events: events}
}

func TestChatMultipleToolEventsPreserveOrderAndFailures(t *testing.T) {
	s, _ := newTestServer(t)
	wsDir := t.TempDir()
	ws, err := s.workspaces.Create(workspaces.CreateRequest{Name: "multi-tools", MainPath: wsDir})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := s.workspaces.SetActive(ws.ID); err != nil {
		t.Fatalf("set active: %v", err)
	}
	s.llm = &multipleToolStreamer{path: normalizeWorkspaceFolderLabel(filepath.Base(wsDir))}

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
	sendSharedChat(t, conn, ws.ID, "run two tools", "")

	var calls, results []map[string]any
	for {
		evt := readCompatChatEvent(t, conn)
		if evt["type"] == "chat_event" {
			switch evt["eventType"] {
			case "tool_call":
				calls = append(calls, evt)
			case "tool_result":
				results = append(results, evt)
			}
		}
		if evt["type"] == "chat_done" {
			break
		}
		if evt["type"] == "chat_error" {
			t.Fatalf("unexpected chat error: %v", evt)
		}
	}

	if len(calls) != 2 || len(results) != 2 {
		t.Fatalf("expected two calls and results, got calls=%d results=%d", len(calls), len(results))
	}
	if calls[0]["callId"] != "call-good" || calls[1]["callId"] != "turn-0-call-1" {
		t.Fatalf("unexpected call IDs: %v, %v", calls[0]["callId"], calls[1]["callId"])
	}
	if calls[0]["callOrder"] != float64(0) || calls[1]["callOrder"] != float64(1) {
		t.Fatalf("unexpected call order: %v, %v", calls[0]["callOrder"], calls[1]["callOrder"])
	}
	if results[0]["success"] != true || results[1]["success"] != false {
		t.Fatalf("unexpected result statuses: %v, %v", results[0]["success"], results[1]["success"])
	}
	if results[0]["callId"] != calls[0]["callId"] || results[1]["callId"] != calls[1]["callId"] {
		t.Fatalf("result IDs do not match calls: calls=%v results=%v", calls, results)
	}
	if content, _ := results[1]["content"].(string); !strings.Contains(content, "missing_tool") {
		t.Fatalf("expected complete unknown-tool error payload, got %q", content)
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

	sendSharedChat(t, conn, ws.ID, "read the image", "")

	var doneSeen bool
	for {
		evt := readCompatChatEvent(t, conn)
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
	if transcript := loadActiveTabTranscript(t, ws); !transcript.Vision {
		t.Fatal("reading an image did not persist Vision routing for the chat")
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

	sendSharedChat(t, conn, ws.ID, "read the video", "")

	var doneSeen bool
	for {
		evt := readCompatChatEvent(t, conn)
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
	if transcript := loadActiveTabTranscript(t, ws); !transcript.Vision {
		t.Fatal("reading a video did not persist Vision routing for the chat")
	}
}
