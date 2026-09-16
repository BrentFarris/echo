package server

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/brent/echo/internal/sessions"
	"github.com/brent/echo/internal/workspacefs"
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

func TestCodeChatGoalReset(t *testing.T) {
	for _, scenario := range []struct {
		name           string
		status         sessions.GoalStatus
		busy           bool
		activeTurn     bool
		mainChat       bool
		persistFailure bool
		wantError      string
	}{
		{name: "provider error", status: sessions.GoalStatusPaused},
		{name: "blocked", status: sessions.GoalStatusBlocked},
		{name: "active goal", status: sessions.GoalStatusActive, wantError: "goal_transcript_locked"},
		{name: "background compression", status: sessions.GoalStatusPaused, busy: true, wantError: "session_busy"},
		{name: "paused response still stopping", status: sessions.GoalStatusPaused, activeTurn: true, wantError: "session_busy"},
		{name: "main paused goal remains protected", status: sessions.GoalStatusPaused, mainChat: true, wantError: "goal_transcript_locked"},
		{name: "main blocked goal remains protected", status: sessions.GoalStatusBlocked, mainChat: true, wantError: "goal_transcript_locked"},
		{name: "persistence failure", status: sessions.GoalStatusPaused, persistFailure: true, wantError: "session_clear_failed"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			s, _ := newTestServer(t)
			workspace := createChatWorkspace(t, s, "code-goal-reset")
			s.llm = errorStreamer{}
			url := startWebSocketTestServer(t, s)
			conn := dialSharedClient(t, url)
			surface := chatSurfaceCode
			if scenario.mainChat {
				surface = chatSurfaceMain
			}
			if err := conn.WriteJSON(map[string]any{"type": "session_subscribe", "workspaceId": workspace.ID, "surface": surface}); err != nil {
				t.Fatal(err)
			}
			snapshot := readChatSnapshot(t, conn)
			chatID := snapshot["activeChatId"].(string)
			if err := conn.WriteJSON(map[string]any{
				"type": "goal_start", "surface": surface, "workspaceId": workspace.ID, "chatId": chatID,
				"requestId": "failed-goal", "message": "Finish despite a missing model",
			}); err != nil {
				t.Fatal(err)
			}
			finished := readUntilSessionEvent(t, conn, "turn_finished")
			if finished["status"] != "error" {
				t.Fatalf("expected provider failure: %v", finished)
			}
			readUntilMessageType(t, conn, "goal_attention")
			parent, err := s.sessions.get(workspace.ID)
			if err != nil {
				t.Fatal(err)
			}
			session, _, err := parent.resolveSurfaceTab(chatID, surface)
			if err != nil {
				t.Fatal(err)
			}
			session.mu.Lock()
			goal, _ := session.currentGoalLocked()
			if goal == nil || goal.Status != sessions.GoalStatusPaused {
				session.mu.Unlock()
				t.Fatal("provider failure did not pause goal")
			}
			goal.Status = scenario.status
			goal.PendingSteering = []sessions.GoalSteering{{ID: "queued", Content: "Keep compatibility"}}
			before := cloneTabTranscript(session.transcript)
			if err := parent.persistTabLocked(before); err != nil {
				session.mu.Unlock()
				t.Fatal(err)
			}
			session.idleCompressionRunning = scenario.busy
			if scenario.activeTurn {
				session.active = &sessions.Turn{ID: "stopping", Status: "streaming"}
			}
			store := parent.store
			if scenario.persistFailure {
				// A store without this Code Chat cannot persist the replacement.
				parent.store = sessions.NewWorkspaceStore(t.TempDir())
			}
			session.mu.Unlock()
			storedBefore, err := store.Load(workspace.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := conn.WriteJSON(map[string]any{
				"type": "chat_clear", "surface": surface, "workspaceId": workspace.ID, "chatId": chatID,
			}); err != nil {
				t.Fatal(err)
			}
			if scenario.wantError != "" {
				failure := readUntilMessageType(t, conn, "command_error")
				if failure["code"] != scenario.wantError {
					t.Fatalf("unexpected rejection: %v", failure)
				}
				session.mu.Lock()
				unchanged := reflect.DeepEqual(before, session.transcript)
				session.idleCompressionRunning = false
				session.active = nil
				parent.store = store
				session.mu.Unlock()
				if !unchanged {
					t.Fatal("rejected reset changed live transcript")
				}
			} else {
				cleared := readChatSnapshot(t, conn)
				if cleared["goal"] != nil || len(cleared["turns"].([]any)) != 0 {
					t.Fatalf("reset left goal or history: %v", cleared)
				}
				// A new subscriber must see the reset, too.
				reconnected := subscribeCodeChat(t, dialSharedClient(t, url), workspace.ID)
				if reconnected["goal"] != nil || len(reconnected["turns"].([]any)) != 0 {
					t.Fatalf("reset lost on reconnect: %v", reconnected)
				}
			}
			storedAfter, err := sessions.NewWorkspaceStore(workspace.MainPath).Load(workspace.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(storedBefore.Tabs, storedAfter.Tabs) || storedBefore.ActiveChatID != storedAfter.ActiveChatID {
				t.Fatal("reset changed main chat")
			}
			if scenario.wantError != "" {
				if !reflect.DeepEqual(storedBefore.CodeChat, storedAfter.CodeChat) {
					t.Fatal("rejected reset changed persisted transcript")
				}
			} else {
				cleared := storedAfter.CodeChat
				if cleared == nil || cleared.ChatID != chatID || cleared.CurrentGoalID != "" || len(cleared.Goals) != 0 || len(cleared.Turns) != 0 || len(cleared.Messages) != 0 {
					t.Fatalf("reset was not durable: %#v", cleared)
				}
			}
		})
	}
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
		"references": []any{map[string]any{
			"kind": "directory", "label": "docs", "referencePath": "workspace/docs",
			"ref": map[string]any{"rootId": "root", "path": "docs"},
		}},
		"editorContext": map[string]any{"tabs": []any{
			map[string]any{
				"kind": "diff", "title": "main.go (Index)", "active": true,
				"ref": map[string]any{"rootId": "root", "path": "main.go"}, "reference": "workspace/main.go",
				"diff": map[string]any{"repositoryId": "repo-1", "repository": "workspace", "scope": "staged", "path": "main.go"},
				"selections": []any{map[string]any{
					"side": "original", "startLine": 3, "startColumn": 1, "endLine": 3, "endColumn": 13, "text": "focused code",
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
				if strings.Contains(message.Content, "Current Echo Code editor context") {
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
	turn := stored.CodeChat.Turns[0]
	if len(turn.References) != 1 || turn.References[0].ReferencePath != "workspace/docs" || turn.EditorContext == nil ||
		len(turn.EditorContext.Tabs) != 2 || turn.EditorContext.Tabs[0].Diff == nil ||
		turn.EditorContext.Tabs[0].Diff.RepositoryID != "repo-1" || len(turn.EditorContext.Tabs[0].Selections) != 1 {
		t.Fatalf("display-safe prompt resources were not persisted: %#v", turn)
	}
	encodedTurn, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encodedTurn), "focused code") || strings.Contains(string(encodedTurn), "package draft") {
		t.Fatalf("prompt resource summary retained inline editor contents: %s", encodedTurn)
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

func TestPromptReferencesAreBoundedDeduplicatedAndCodeOnly(t *testing.T) {
	input := []chatReferenceInput{
		{Kind: "file", Label: "main.go", ReferencePath: "workspace/main.go", Ref: workspacefs.FileRef{RootID: "root", Path: "main.go"}},
		{Kind: "file", Label: "duplicate", ReferencePath: "other/main.go", Ref: workspacefs.FileRef{RootID: "root", Path: "main.go"}},
	}
	references, err := promptReferences(chatSurfaceCode, input)
	if err != nil || len(references) != 1 || references[0].Label != "main.go" {
		t.Fatalf("prompt references were not deduplicated in first-seen order: %#v, %v", references, err)
	}
	if _, err := promptReferences(chatSurfaceMain, input[:1]); err == nil || !strings.Contains(err.Error(), "code chat") {
		t.Fatalf("expected main-chat references to be rejected, got %v", err)
	}
	tooMany := make([]chatReferenceInput, maxPromptReferences+1)
	if _, err := promptReferences(chatSurfaceCode, tooMany); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Fatalf("expected reference limit validation, got %v", err)
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
