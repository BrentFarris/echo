package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/brent/echo/internal/llm"
)

type trajectoryStreamer struct{}

func (trajectoryStreamer) StreamChat(_ context.Context, _ llm.ChatRequest) *llm.Stream {
	usage := &llm.Usage{PromptTokens: 11, CompletionTokens: 3, TotalTokens: 14}
	events := make(chan llm.StreamEvent, 40)
	for range 35 {
		events <- llm.StreamEvent{Type: llm.EventReasoning, Content: "inspect", Raw: json.RawMessage(`{"choices":[{"delta":{"reasoning_content":"inspect"}}]}`)}
	}
	events <- llm.StreamEvent{Type: llm.EventToken, Content: "answer", Raw: json.RawMessage(`{"choices":[{"delta":{"content":"answer"}}]}`)}
	events <- llm.StreamEvent{Type: llm.EventUsage, Usage: usage, Raw: json.RawMessage(`{"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`)}
	events <- llm.StreamEvent{Type: llm.EventComplete, FinishReason: "stop", Usage: usage, Raw: json.RawMessage(`{"choices":[{"finish_reason":"stop"}]}`)}
	close(events)
	return &llm.Stream{ID: "trajectory", Events: events, Usage: usage}
}

func TestTrajectoryCaptureAPIExportAndClear(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "trajectory")
	s.llm = trajectoryStreamer{}
	parent, err := s.sessions.get(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	parent.mu.Lock()
	chatID := parent.activeChatID
	session := parent.tabs[chatID]
	parent.mu.Unlock()

	url := startWebSocketTestServer(t, s)
	conn := dialSharedClient(t, url)
	subscribeChat(t, conn, workspace.ID)
	if err := conn.WriteJSON(map[string]any{
		"type": "chat_send", "workspaceId": workspace.ID, "chatId": chatID,
		"requestId": "trajectory-request", "message": "record this prompt",
	}); err != nil {
		t.Fatal(err)
	}
	liveTypes := make(map[string]int)
	var finished map[string]any
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	for finished == nil {
		var message map[string]any
		if err := conn.ReadJSON(&message); err != nil {
			t.Fatalf("read trajectory chat completion: %v", err)
		}
		if message["type"] == "trajectory_event" && message["chatId"] == chatID {
			event, _ := message["event"].(map[string]any)
			if eventType, ok := event["type"].(string); ok {
				liveTypes[eventType]++
			}
			continue
		}
		if message["type"] == "session_event" && message["chatId"] == chatID {
			event, _ := message["event"].(map[string]any)
			if event["type"] == "turn_finished" {
				finished = event
			}
		}
	}
	if finished["status"] != "done" {
		t.Fatalf("chat did not complete: %v", finished)
	}

	page, err := session.trajectory.Page(0, 20)
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{
		"turn/start": false, "user/message": false, "request/start": false,
		"assistant/chunk": false, "assistant/message": false, "turn/end": false,
	}
	chunkCount := 0
	for _, event := range page.Events {
		if _, ok := wanted[event.Type]; ok {
			wanted[event.Type] = true
		}
		if event.Type == "request/start" && !strings.Contains(string(event.Data), "record this prompt") {
			t.Fatalf("request record did not contain the exact prompt: %s", event.Data)
		}
		if event.Type == "assistant/chunk" && strings.Contains(string(event.Data), "reasoning_content") {
			if !strings.Contains(string(event.Data), `"raw"`) {
				t.Fatalf("raw provider frame was not retained: %s", event.Data)
			}
		}
		if event.Type == "assistant/chunk" {
			chunkCount++
		}
	}
	for eventType, found := range wanted {
		if !found {
			t.Fatalf("trajectory did not capture %s: %#v", eventType, page.Events)
		}
	}
	if chunkCount != 4 {
		t.Fatalf("expected three reasoning chunks and one content/completion chunk, got %d", chunkCount)
	}
	if liveTypes["assistant/chunk"] != chunkCount || liveTypes["assistant/message"] != 1 || liveTypes["turn/end"] != 1 {
		t.Fatalf("live trajectory events did not mirror persisted records: live=%v chunks=%d", liveTypes, chunkCount)
	}

	base := "/api/workspaces/" + workspace.ID + "/chats/" + chatID + "/trajectory"
	rr := doRequest(t, s, http.MethodGet, base+"?turnLimit=20")
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "record this prompt") {
		t.Fatalf("unexpected trajectory response %d: %s", rr.Code, rr.Body.String())
	}
	search := doRequest(t, s, http.MethodGet, base+"/search?q=record+this+prompt")
	if search.Code != http.StatusOK || !strings.Contains(search.Body.String(), "request/start") {
		t.Fatalf("unexpected search response %d: %s", search.Code, search.Body.String())
	}
	export := doRequest(t, s, http.MethodGet, base+"/export")
	if export.Code != http.StatusOK || export.Header().Get("Content-Type") != "application/x-ndjson; charset=utf-8" || !strings.Contains(export.Body.String(), `"record":"header"`) {
		t.Fatalf("unexpected export response %d %q: %s", export.Code, export.Header().Get("Content-Type"), export.Body.String())
	}

	trajectoryPath := session.trajectory.Path()
	if err := conn.WriteJSON(map[string]any{"type": "chat_clear", "workspaceId": workspace.ID, "chatId": chatID}); err != nil {
		t.Fatal(err)
	}
	readChatSnapshot(t, conn)
	if _, err := os.Stat(trajectoryPath); !os.IsNotExist(err) {
		t.Fatalf("clear did not delete trajectory %q: %v", trajectoryPath, err)
	}
}
