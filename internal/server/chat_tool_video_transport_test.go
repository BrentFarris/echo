package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
	"github.com/brent/echo/internal/workspaces"
)

// comfyuiVideoToolStreamer emits a comfyui_generate_video tool call on the first
// request, then a final answer on subsequent requests.
type comfyuiVideoToolStreamer struct {
	mu       sync.Mutex
	requests []llm.ChatRequest
}

func (f *comfyuiVideoToolStreamer) StreamChat(_ context.Context, request llm.ChatRequest) *llm.Stream {
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
				ID:    "call-vid",
				Type:  "function",
				Function: llm.FunctionCallDelta{
					Name:      "comfyui_generate_video",
					Arguments: `{"prompt":"a walking cat"}`,
				},
			},
		}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "tool_calls"}
	} else {
		events <- llm.StreamEvent{Type: llm.EventToken, Content: "Here is your video."}
		events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop"}
	}
	close(events)
	return &llm.Stream{ID: "fake", Events: events}
}

func (f *comfyuiVideoToolStreamer) lastRequest() llm.ChatRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		return llm.ChatRequest{}
	}
	return f.requests[len(f.requests)-1]
}

// TestChatToolVideoTransportEmitsAndPersists verifies the Phase 1 end-to-end
// path for a tool that produces video media:
//  1. the tool_result session event carries structured videos[] attachments,
//  2. the finished transcript persists them on the AssistantTurn,
//  3. a fresh subscriber's snapshot restores the same attachments,
//  4. the follow-up LLM request keeps the video tool result text-only (no
//     base64 VideoURL content part leaks into the model context).
func TestChatToolVideoTransportEmitsAndPersists(t *testing.T) {
	s, _ := newTestServer(t)

	wsDir := t.TempDir()
	ws, err := s.workspaces.Create(workspaces.CreateRequest{Name: "video-ws", MainPath: wsDir})
	if err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	if err := s.workspaces.SetActive(ws.ID); err != nil {
		t.Fatalf("set active: %v", err)
	}

	// Point the configured default video workflow at a template file; the
	// setting is loaded directly off disk (absolute path).
	videoWorkflow := `{"10": {"class_type": "VHS_VideoCombine", "inputs": {"format": "{{FORMAT}}"}}}`
	wfPath := wsDir + "/video_workflow.json"
	if err := os.WriteFile(wfPath, []byte(videoWorkflow), 0o644); err != nil {
		t.Fatal(err)
	}

	mp4Data := []byte{0x00, 0x00, 0x00, 0x1C, 'f', 't', 'y', 'p'}
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/prompt" || r.URL.Path == "/prompt/":
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{"prompt_id": "mock-vid-1"})
		case strings.HasPrefix(r.URL.Path, "/history/mock-vid-1"):
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]any{
				"mock-vid-1": map[string]any{
					"status": map[string]any{},
					"outputs": map[string]any{
						"10": map[string]any{
							"videos": []any{
								map[string]any{
									"filename":  "cat.mp4",
									"subfolder": "",
									"type":      "output",
								},
							},
						},
					},
				},
			})
		case r.URL.Path == "/view":
			w.Header().Set("Content-Type", "video/mp4")
			w.Write(mp4Data)
		default:
			http.NotFound(w, r)
		}
	}))
	defer mock.Close()

	cfg, err := s.store.Load()
	if err != nil {
		t.Fatalf("load settings: %v", err)
	}
	cfg.ComfyuiURL = mock.URL
	cfg.ComfyuiVideoWorkflow = wfPath
	if err := s.store.Save(cfg); err != nil {
		t.Fatalf("save settings: %v", err)
	}
	s.initLLM()

	fake := &comfyuiVideoToolStreamer{}
	s.llm = fake
	url := startWebSocketTestServer(t, s)

	conn := dialSharedClient(t, url)
	subscribeChat(t, conn, ws.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": ws.ID, "requestId": "request-video", "message": "make a video",
	}); err != nil {
		t.Fatal(err)
	}

	var toolResult = readUntilSessionEvent(t, conn, "tool_result")
	readUntilSessionEvent(t, conn, "turn_finished")

	// 1. Structured video rides the tool_result event.
	videos, ok := toolResult["videos"].([]any)
	if !ok || len(videos) != 1 {
		t.Fatalf("expected exactly one video attachment on tool_result, got %#v", toolResult["videos"])
	}
	video, ok := videos[0].(map[string]any)
	if !ok {
		t.Fatalf("malformed video attachment: %#v", videos[0])
	}
	if id, _ := video["id"].(string); !strings.HasPrefix(id, "gen-vid-") {
		t.Fatalf("expected gen-vid-* ID, got %q", id)
	}
	if name, _ := video["name"].(string); name != "cat.mp4" {
		t.Fatalf("expected name cat.mp4, got %q", name)
	}
	if mediaType, _ := video["mediaType"].(string); mediaType != "video/mp4" {
		t.Fatalf("expected mediaType video/mp4, got %q", mediaType)
	}
	if bytes, _ := video["bytes"].(float64); bytes != float64(len(mp4Data)) {
		t.Fatalf("expected bytes %d, got %v", len(mp4Data), video["bytes"])
	}
	dataURL, _ := video["dataUrl"].(string)
	if !strings.HasPrefix(dataURL, "data:video/mp4;base64,") {
		t.Fatalf("expected video data URL, got %q", dataURL)
	}
	if _, hasImages := toolResult["images"]; hasImages {
		t.Fatalf("unexpected images key on tool_result: %#v", toolResult)
	}

	// 2. The finished transcript persists media on the assistant turn.
	transcript := loadActiveTabTranscript(t, ws)
	if len(transcript.Turns) != 1 {
		t.Fatalf("expected one persisted turn, got %d", len(transcript.Turns))
	}
	foundVideo := false
	for _, assistant := range transcript.Turns[0].AssistantTurns {
		for _, attachment := range assistant.Videos {
			if attachment.Name == "cat.mp4" && strings.HasPrefix(attachment.DataURL, "data:video/mp4;base64,") {
				foundVideo = true
			}
		}
	}
	if !foundVideo {
		t.Fatalf("assistant turn video not persisted: %#v", transcript.Turns[0].AssistantTurns)
	}

	// 3. A fresh subscriber's snapshot restores the same attachments.
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
	snapshotTurns, _ := snapshot["turns"].([]any)
	if len(snapshotTurns) != 1 {
		t.Fatalf("expected one snapshot turn, got %d", len(snapshotTurns))
	}
	turn, _ := snapshotTurns[0].(map[string]any)
	assistantArr, _ := turn["assistantTurns"].([]any)
	restored := false
	for _, rawAssistant := range assistantArr {
		assistant, _ := rawAssistant.(map[string]any)
		rawVideos, _ := assistant["videos"].([]any)
		for _, rawVideo := range rawVideos {
			vid, _ := rawVideo.(map[string]any)
			if vid["name"] == "cat.mp4" && strings.HasPrefix(asString(vid["dataUrl"]), "data:video/mp4;base64,") {
				restored = true
			}
		}
	}
	if !restored {
		t.Fatalf("fresh subscriber did not receive restored video: %#v", snapshotTurns[0])
	}

	// 4. Context hygiene: the follow-up request must carry the video tool
	// result as plain text only — no base64 VideoURL content part.
	last := fake.lastRequest()
	if len(last.Messages) < 2 {
		t.Fatalf("expected follow-up request with messages, got %d", len(last.Messages))
	}
	foundTextOnly := false
	for _, message := range last.Messages {
		if !strings.Contains(message.Content, "Video returned by tool comfyui_generate_video") {
			continue
		}
		for _, part := range message.ContentParts {
			if part.VideoURL != nil {
				t.Fatalf("video data URL leaked into LLM context: %#v", part)
			}
		}
		foundTextOnly = true
	}
	if !foundTextOnly {
		t.Fatalf("text-only video tool result missing from follow-up request: %#v", last.Messages)
	}
}
