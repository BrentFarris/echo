package server

import (
	"bytes"
	"context"
	"image/color"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/workspaces"
	"github.com/disintegration/imaging"
)

// TestRedactTurnForSnapshotStripsHeavyPayloads verifies the metadata-only
// projection: long tool results become flagged previews, reasoning is omitted,
// and media keeps its identity but loses its base64 payload. The input turn
// (and the slices it shares with the live transcript) must stay untouched.
func TestRedactTurnForSnapshotStripsHeavyPayloads(t *testing.T) {
	longResult := strings.Repeat("r", 5000)
	longReasoning := strings.Repeat("t", 5000)
	turn := sessions.Turn{
		ID:          "turn-1",
		UserContent: "hello",
		Images:      []sessions.MediaAttachment{{ID: "img-1", Name: "a.png", MediaType: "image/png", Bytes: 12, DataURL: "data:image/png;base64,AAAA"}},
		AssistantTurns: []sessions.AssistantTurn{{
			Number:    0,
			Reasoning: longReasoning,
			Tools: []sessions.ToolActivity{{
				CallID: "call-1", Name: "filesystem_read_text", Status: "complete", Success: true,
				Result: longResult,
			}},
			Images: []sessions.MediaAttachment{{ID: "gen-img-1", Name: "b.png", MediaType: "image/png", Bytes: 34, DataURL: "data:image/png;base64,BBBB"}},
		}},
		ResearchReasoning: []sessions.ResearchReasoning{{AgentID: "agent-1", AgentName: "Scout", Reasoning: longReasoning}},
		ResearchTools:     []sessions.ToolActivity{{CallID: "call-r", Name: "web_fetch", Status: "complete", Result: longResult}},
	}

	redacted := redactTurnForSnapshot(turn)

	if !redacted.AssistantTurns[0].ReasoningTruncated {
		t.Fatalf("expected reasoningTruncated flag: %#v", redacted.AssistantTurns[0])
	}
	if redacted.AssistantTurns[0].Reasoning != "" {
		t.Fatalf("expected reasoning to be omitted, got %d bytes", len(redacted.AssistantTurns[0].Reasoning))
	}
	tool := redacted.AssistantTurns[0].Tools[0]
	if !tool.ResultTruncated {
		t.Fatalf("expected resultTruncated flag: %#v", tool)
	}
	if tool.Result != longResult[:snapshotToolResultPreviewBytes] {
		t.Fatalf("expected %d byte preview, got %d bytes", snapshotToolResultPreviewBytes, len(tool.Result))
	}
	if got := redacted.AssistantTurns[0].Images[0]; got.DataURL != "" || got.ID != "gen-img-1" || got.Name != "b.png" {
		t.Fatalf("expected payload-free assistant media metadata, got %#v", got)
	}
	if got := redacted.Images[0]; got.DataURL != "" || got.ID != "img-1" {
		t.Fatalf("expected payload-free user media metadata, got %#v", got)
	}
	if redacted.ResearchReasoning[0].Reasoning != longReasoning[:snapshotResearchReasoningPreviewBytes] || !redacted.ResearchReasoning[0].Truncated {
		t.Fatalf("expected previewed research reasoning: %#v", redacted.ResearchReasoning[0])
	}
	if !redacted.ResearchTools[0].ResultTruncated {
		t.Fatalf("expected truncated research tool result: %#v", redacted.ResearchTools[0])
	}

	// The source turn must be untouched: it shares slice backing arrays with
	// the live transcript.
	if turn.AssistantTurns[0].Reasoning != longReasoning {
		t.Fatalf("redaction mutated the source turn reasoning")
	}
	if turn.AssistantTurns[0].Tools[0].Result != longResult {
		t.Fatalf("redaction mutated the source turn tool result")
	}
	if turn.AssistantTurns[0].Images[0].DataURL == "" || turn.Images[0].DataURL == "" {
		t.Fatalf("redaction mutated the source turn media payloads")
	}
}

// redactionToolStreamer emits reasoning plus a filesystem_read_text tool call
// on the first request, then a final answer on subsequent requests.
type redactionToolStreamer struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
	path     string
}

func (f *redactionToolStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
	f.mu.Lock()
	f.requests = append(f.requests, request)
	callCount := len(f.requests)
	f.mu.Unlock()

	events := make(chan llm.StreamEvent, 8)
	if callCount == 1 {
		events <- llm.StreamEvent{Type: llm.EventReasoning, Content: strings.Repeat("thinking ", 400)}
		events <- llm.StreamEvent{
			Type: llm.EventToolCall,
			ToolCall: &llm.ToolCallDelta{
				Index: 0,
				ID:    "call-redact",
				Type:  "function",
				Function: llm.FunctionCallDelta{
					Name:      "filesystem_read_text",
					Arguments: `{"path":"` + f.path + `"}`,
				},
			},
		}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
	} else {
		events <- llm.StreamEvent{Type: llm.EventToken, Content: "Done."}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	}
	close(events)
	return &llm.Stream{ID: "fake", Events: events}
}

// TestSnapshotRedactionAndFetchEndpoints runs the full wire contract: a turn
// with long reasoning and a long tool result produces a metadata-only
// snapshot, and the deferred content is fetchable per disclosure.
func TestSnapshotRedactionAndFetchEndpoints(t *testing.T) {
	s, _ := newTestServer(t)

	wsDir := t.TempDir()
	bigContent := strings.Repeat("x", 10000)
	if err := os.WriteFile(filepath.Join(wsDir, "big.txt"), []byte(bigContent), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := s.workspaces.Create(workspaces.CreateRequest{Name: "redact-ws", MainPath: wsDir})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := s.workspaces.SetActive(ws.ID); err != nil {
		t.Fatalf("set active: %v", err)
	}

	fake := &redactionToolStreamer{path: normalizeWorkspaceFolderLabel(filepath.Base(wsDir)) + "/big.txt"}
	s.llm = fake
	url := startWebSocketTestServer(t, s)

	conn := dialSharedClient(t, url)
	subscribeChat(t, conn, ws.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": ws.ID, "requestId": "request-redact", "message": "read the file",
	}); err != nil {
		t.Fatal(err)
	}
	readUntilSessionEvent(t, conn, "turn_finished")

	late := dialSharedClient(t, url)
	defer late.Close()
	if err := late.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": ws.ID}); err != nil {
		t.Fatal(err)
	}
	late.SetReadDeadline(time.Now().Add(3 * time.Second))
	var snapshot map[string]any
	for {
		var message map[string]any
		if err := late.ReadJSON(&message); err != nil {
			t.Fatalf("read late snapshot: %v", err)
		}
		if message["type"] == "session_snapshot" {
			snapshot = message
			break
		}
	}
	chatID, _ := snapshot["chatId"].(string)
	if chatID == "" {
		t.Fatalf("snapshot missing chatId: %#v", snapshot)
	}
	snapshotTurns, _ := snapshot["turns"].([]any)
	if len(snapshotTurns) != 1 {
		t.Fatalf("expected one snapshot turn, got %d", len(snapshotTurns))
	}
	turn, _ := snapshotTurns[0].(map[string]any)
	turnID, _ := turn["id"].(string)
	assistantArr, _ := turn["assistantTurns"].([]any)
	if len(assistantArr) == 0 {
		t.Fatalf("no assistant turns in snapshot: %#v", turn)
	}
	assistant, _ := assistantArr[0].(map[string]any)
	if assistant["reasoningTruncated"] != true {
		t.Fatalf("expected reasoningTruncated on snapshot assistant turn: %#v", assistant)
	}
	if reasoning, _ := assistant["reasoning"].(string); reasoning != "" {
		t.Fatalf("expected reasoning omitted from snapshot, got %d bytes", len(reasoning))
	}
	tools, _ := assistant["tools"].([]any)
	if len(tools) == 0 {
		t.Fatalf("no tools on snapshot assistant turn: %#v", assistant)
	}
	tool, _ := tools[0].(map[string]any)
	if tool["resultTruncated"] != true {
		t.Fatalf("expected resultTruncated on snapshot tool: %#v", tool)
	}
	if result, _ := tool["result"].(string); len(result) > snapshotToolResultPreviewBytes {
		t.Fatalf("expected tool result preview, got %d bytes", len(result))
	}

	// The full tool result is fetchable on demand.
	req := httptest.NewRequest(http.MethodGet, "/api/workspaces/"+ws.ID+"/chats/"+chatID+"/tool-results/call-redact", nil)
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("tool result fetch status %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), bigContent) {
		t.Fatalf("expected full tool result in response, got: %s", rr.Body.String())
	}

	// The full reasoning is fetchable per assistant turn.
	req = httptest.NewRequest(http.MethodGet, "/api/workspaces/"+ws.ID+"/chats/"+chatID+"/reasoning/"+turnID+"?assistant=0", nil)
	rr = httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("reasoning fetch status %d: %s", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "thinking") {
		t.Fatalf("expected full reasoning in response, got: %s", rr.Body.String())
	}

	// Unknown selectors are rejected cleanly.
	req = httptest.NewRequest(http.MethodGet, "/api/workspaces/"+ws.ID+"/chats/"+chatID+"/reasoning/"+turnID, nil)
	rr = httptest.NewRecorder()
	s.routes().ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without selector, got %d", rr.Code)
	}
}

// TestChatAttachmentPreviewResize verifies the ?width= preview variant: a
// large PNG is downscaled, and the unparameterized fetch returns the original
// bytes.
func TestChatAttachmentPreviewResize(t *testing.T) {
	s, _ := newTestServer(t)

	wsDir := t.TempDir()
	source := imaging.New(200, 100, color.NRGBA{R: 30, G: 144, B: 255, A: 255})
	var pngBytes bytes.Buffer
	if err := imaging.Encode(&pngBytes, source, imaging.PNG); err != nil {
		t.Fatalf("encode source png: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "photo.png"), pngBytes.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	ws, err := s.workspaces.Create(workspaces.CreateRequest{Name: "preview-ws", MainPath: wsDir})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := s.workspaces.SetActive(ws.ID); err != nil {
		t.Fatalf("set active: %v", err)
	}

	fake := &imageToolStreamer{path: normalizeWorkspaceFolderLabel(filepath.Base(wsDir)) + "/photo.png"}
	s.llm = fake
	url := startWebSocketTestServer(t, s)

	conn := dialSharedClient(t, url)
	subscribeChat(t, conn, ws.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": ws.ID, "requestId": "request-preview", "message": "read the image",
	}); err != nil {
		t.Fatal(err)
	}
	toolResult := readUntilSessionEvent(t, conn, "tool_result")
	readUntilSessionEvent(t, conn, "turn_finished")

	images, _ := toolResult["images"].([]any)
	if len(images) != 1 {
		t.Fatalf("expected one image attachment, got %#v", toolResult["images"])
	}
	image, _ := images[0].(map[string]any)
	attachmentID, _ := image["id"].(string)

	late := dialSharedClient(t, url)
	defer late.Close()
	if err := late.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": ws.ID}); err != nil {
		t.Fatal(err)
	}
	late.SetReadDeadline(time.Now().Add(3 * time.Second))
	var snapshot map[string]any
	for {
		var message map[string]any
		if err := late.ReadJSON(&message); err != nil {
			t.Fatalf("read late snapshot: %v", err)
		}
		if message["type"] == "session_snapshot" {
			snapshot = message
			break
		}
	}
	chatID, _ := snapshot["chatId"].(string)

	base := "/api/workspaces/" + ws.ID + "/chats/" + chatID + "/attachments/" + attachmentID

	// Full fetch returns the original bytes.
	rr := httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, base, nil))
	if rr.Code != http.StatusOK || !bytes.Equal(rr.Body.Bytes(), pngBytes.Bytes()) {
		t.Fatalf("full fetch status %d, %d bytes (expected %d)", rr.Code, rr.Body.Len(), pngBytes.Len())
	}

	// Preview fetch returns a downscaled PNG.
	rr = httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, base+"?width=32", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("preview fetch status %d: %s", rr.Code, rr.Body.String())
	}
	if contentType := rr.Header().Get("Content-Type"); contentType != "image/png" {
		t.Fatalf("expected image/png preview, got %q", contentType)
	}
	decoded, err := imaging.Decode(bytes.NewReader(rr.Body.Bytes()))
	if err != nil {
		t.Fatalf("preview is not a decodable image: %v", err)
	}
	if decoded.Bounds().Dx() > 32 || decoded.Bounds().Dy() > 32 {
		t.Fatalf("expected preview within 32px, got %v", decoded.Bounds())
	}
	if decoded.Bounds().Dx() > decoded.Bounds().Dy() && decoded.Bounds().Dx() != 32 {
		t.Fatalf("expected longest edge 32px, got %v", decoded.Bounds())
	}

	// Unknown attachments 404.
	rr = httptest.NewRecorder()
	s.routes().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, base+"-missing", nil))
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown attachment, got %d", rr.Code)
	}
}
