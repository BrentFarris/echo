package server

import (
	"strings"
	"testing"

	"github.com/brent/echo/internal/sessions"
	"github.com/gorilla/websocket"
)

func subscribeCodeChat(t *testing.T, conn *websocket.Conn, workspaceID string) map[string]any {
	t.Helper()
	if err := conn.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": workspaceID, "surface": "code"}); err != nil {
		t.Fatal(err)
	}
	snapshot := readChatSnapshot(t, conn)
	if snapshot["surface"] != "code" {
		t.Fatalf("unexpected code-chat snapshot: %v", snapshot)
	}
	return snapshot
}

func TestCodeChatIsPersistentAndIndependentFromMainTabs(t *testing.T) {
	s, _ := newTestServer(t)
	workspace := createChatWorkspace(t, s, "code-chat")
	fake := &historyStreamer{}
	s.llm = fake
	url := startWebSocketTestServer(t, s)
	mainClient := dialSharedClient(t, url)
	if err := mainClient.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": workspace.ID}); err != nil {
		t.Fatal(err)
	}
	mainSnapshot := readChatSnapshot(t, mainClient)
	beforeCode, err := sessions.NewWorkspaceStore(workspace.MainPath).Load(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if beforeCode.CodeChat != nil {
		t.Fatalf("code chat was created before its first subscription: %#v", beforeCode.CodeChat)
	}

	codeClient := dialSharedClient(t, url)
	codeSnapshot := subscribeCodeChat(t, codeClient, workspace.ID)
	codeID, _ := codeSnapshot["activeChatId"].(string)
	if !strings.HasPrefix(codeID, "code-chat-") || len(snapshotChatIDs(t, codeSnapshot)) != 1 {
		t.Fatalf("dedicated code chat was not created: %v", codeSnapshot)
	}

	for _, id := range snapshotChatIDs(t, mainSnapshot) {
		if id == codeID {
			t.Fatalf("code chat leaked into main tabs: %v", mainSnapshot)
		}
	}

	if err := codeClient.WriteJSON(map[string]any{
		"type": "chat_send", "surface": "code", "workspaceId": workspace.ID, "chatId": codeID,
		"requestId": "code-context-request", "message": "review the open code",
		"editorContext": map[string]any{"tabs": []any{
			map[string]any{
				"kind": "file", "title": "main.go", "active": true,
				"ref": map[string]any{"rootId": "root", "path": "main.go"}, "reference": "workspace/main.go",
				"selections": []any{map[string]any{
					"startLine": 3, "startColumn": 1, "endLine": 3, "endColumn": 13, "text": "focused code",
				}},
			},
			map[string]any{"kind": "untitled", "title": "Untitled-1", "dirty": true, "content": "package draft"},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	readSessionEventForChat(t, codeClient, codeID, "turn_finished")

	fake.mu.Lock()
	requests := append([]string(nil), func() []string {
		values := make([]string, 0, len(fake.requests))
		for _, request := range fake.requests {
			for _, message := range request.Messages {
				if message.Name == "echo-code-context" {
					values = append(values, message.Content)
				}
			}
		}
		return values
	}()...)
	fake.mu.Unlock()
	if len(requests) != 1 || !strings.Contains(requests[0], "workspace/main.go") || !strings.Contains(requests[0], "package draft") ||
		!strings.Contains(requests[0], "focused code") || !strings.Contains(requests[0], "user's focused context") {
		t.Fatalf("model did not receive editor context: %#v", requests)
	}

	stored, err := sessions.NewWorkspaceStore(workspace.MainPath).Load(workspace.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.CodeChat == nil || len(stored.CodeChat.Turns) != 1 || len(stored.Tabs) != 1 || stored.ActiveChatID != mainSnapshot["activeChatId"] {
		t.Fatalf("code chat was not persisted independently: %#v", stored)
	}
	for _, message := range stored.CodeChat.Messages {
		if message.Name == "echo-code-context" || message.Name == "echo-agent-mode" {
			t.Fatalf("ephemeral system context was persisted: %#v", stored.CodeChat.Messages)
		}
	}

	if err := codeClient.WriteJSON(map[string]any{"type": "chat_clear", "surface": "code", "workspaceId": workspace.ID, "chatId": codeID}); err != nil {
		t.Fatal(err)
	}
	cleared := readChatSnapshot(t, codeClient)
	if turns, ok := cleared["turns"].([]any); !ok || len(turns) != 0 {
		t.Fatalf("code chat was not cleared: %v", cleared)
	}
}

func TestEditorContextLimitsAreValidated(t *testing.T) {
	context := &editorContext{Tabs: make([]editorContextTab, maxEditorContextTabs+1)}
	for index := range context.Tabs {
		context.Tabs[index] = editorContextTab{Kind: "file", Title: "main.go"}
	}
	if _, err := editorContextMessage(chatSurfaceCode, context); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("expected tab limit error, got %v", err)
	}
	context = &editorContext{Tabs: []editorContextTab{{Kind: "untitled", Title: "draft", Content: strings.Repeat("x", maxEditorContextBytes+1)}}}
	if _, err := editorContextMessage(chatSurfaceCode, context); err == nil || !strings.Contains(err.Error(), "inline content") {
		t.Fatalf("expected content limit error, got %v", err)
	}
	context = &editorContext{Tabs: []editorContextTab{{Kind: "file", Title: "main.go", Content: "untrusted inline file"}}}
	if _, err := editorContextMessage(chatSurfaceCode, context); err == nil || !strings.Contains(err.Error(), "non-untitled") {
		t.Fatalf("expected file-content validation error, got %v", err)
	}
	context = &editorContext{Tabs: []editorContextTab{{
		Kind: "file", Title: "main.go", Active: true,
		Selections: []editorContextSelection{{StartLine: 4, StartColumn: 8, EndLine: 4, EndColumn: 2, Text: "backwards"}},
	}}}
	if _, err := editorContextMessage(chatSurfaceCode, context); err == nil || !strings.Contains(err.Error(), "invalid range") {
		t.Fatalf("expected selection range error, got %v", err)
	}
	context = &editorContext{Tabs: []editorContextTab{{
		Kind: "diff", Title: "main.go", Active: true,
		Selections: []editorContextSelection{{Side: "working", StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 2, Text: "x"}},
	}}}
	if _, err := editorContextMessage(chatSurfaceCode, context); err == nil || !strings.Contains(err.Error(), "invalid diff side") {
		t.Fatalf("expected diff-side error, got %v", err)
	}
	selections := make([]editorContextSelection, maxEditorContextSelections+1)
	for index := range selections {
		selections[index] = editorContextSelection{StartLine: index + 1, StartColumn: 1, EndLine: index + 1, EndColumn: 2, Text: "x"}
	}
	context = &editorContext{Tabs: []editorContextTab{{Kind: "file", Title: "main.go", Active: true, Selections: selections}}}
	if _, err := editorContextMessage(chatSurfaceCode, context); err == nil || !strings.Contains(err.Error(), "more than") {
		t.Fatalf("expected selection limit error, got %v", err)
	}
	context = &editorContext{Tabs: []editorContextTab{{
		Kind: "untitled", Title: "draft", Active: true, Content: "buffer",
		Selections: []editorContextSelection{{StartLine: 1, StartColumn: 1, EndLine: 1, EndColumn: 2, Text: strings.Repeat("x", maxEditorContextBytes)}},
	}}}
	if _, err := editorContextMessage(chatSurfaceCode, context); err == nil || !strings.Contains(err.Error(), "inline content") {
		t.Fatalf("expected combined inline-content error, got %v", err)
	}
}
