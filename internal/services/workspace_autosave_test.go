package services

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/brent/echo/internal/llm"
)

func TestWorkspaceAutosaveWritesOnlyAtCompletionAndShutdown(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	state, err := service.AddWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID
	autosavePath := filepath.Join(workspacePath, workspaceCacheDirName, workspaceAutosaveFileName)

	if _, err := service.CreateReadyKanbanCard(workspaceID, "Card", "Initial description", []string{"Done"}); err != nil {
		t.Fatalf("create card: %v", err)
	}
	if _, err := os.Stat(autosavePath); !os.IsNotExist(err) {
		t.Fatalf("expected no autosave before a completion boundary, got %v", err)
	}
	globalData, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read global state: %v", err)
	}
	if strings.Contains(string(globalData), "kanbanCards") || strings.Contains(string(globalData), "chatSessions") {
		t.Fatalf("expected global state to omit workspace state, got %s", globalData)
	}

	service.chatMu.Lock()
	service.chatSessions[workspaceID] = &chatSessionState{
		WorkspaceID: workspaceID,
		Messages: []ChatMessage{
			{ID: "msg-1", Role: llm.RoleUser, Content: "Question", Status: "complete"},
			{ID: "msg-2", Role: llm.RoleAssistant, Content: "Answer", Status: "streaming"},
		},
		History: []llm.Message{
			{Role: llm.RoleUser, Content: "Question"},
			{Role: llm.RoleAssistant, Content: "Answer"},
		},
	}
	service.chatMu.Unlock()
	service.completeChatMessage(workspaceID, "stream-1", "msg-2", "stop")

	completedChatData, err := os.ReadFile(autosavePath)
	if err != nil {
		t.Fatalf("read chat completion autosave: %v", err)
	}
	var completedChat workspaceAutosave
	if err := json.Unmarshal(completedChatData, &completedChat); err != nil {
		t.Fatalf("decode chat completion autosave: %v", err)
	}
	if completedChat.ChatSession == nil || len(completedChat.ChatSession.Messages) != 2 ||
		completedChat.ChatSession.Messages[1].Status != "complete" {
		t.Fatalf("expected completed chat in autosave, got %#v", completedChat.ChatSession)
	}
	if len(completedChat.KanbanCards) != 1 || completedChat.KanbanCards[0].Lane != KanbanLaneReady {
		t.Fatalf("expected current kanban state in autosave, got %#v", completedChat.KanbanCards)
	}

	if _, err := service.UpdateKanbanCardDescription(workspaceID, completedChat.KanbanCards[0].ID, "Updated description"); err != nil {
		t.Fatalf("update card: %v", err)
	}
	afterEditData, err := os.ReadFile(autosavePath)
	if err != nil {
		t.Fatalf("read autosave after edit: %v", err)
	}
	if !bytes.Equal(completedChatData, afterEditData) {
		t.Fatal("expected ordinary card edits not to rewrite the autosave")
	}

	if _, err := service.MoveKanbanCard(workspaceID, completedChat.KanbanCards[0].ID, KanbanLaneDone); err != nil {
		t.Fatalf("complete kanban board: %v", err)
	}
	kanbanCompleteData, err := os.ReadFile(autosavePath)
	if err != nil {
		t.Fatalf("read kanban completion autosave: %v", err)
	}
	if bytes.Equal(afterEditData, kanbanCompleteData) {
		t.Fatal("expected kanban completion to rewrite the autosave")
	}

	if _, err := service.CreateReadyKanbanCard(workspaceID, "Unsaved card", "Save on close", []string{"Saved"}); err != nil {
		t.Fatalf("create unsaved card: %v", err)
	}
	service.Shutdown()
	shutdownData, err := os.ReadFile(autosavePath)
	if err != nil {
		t.Fatalf("read shutdown autosave: %v", err)
	}
	var shutdownAutosave workspaceAutosave
	if err := json.Unmarshal(shutdownData, &shutdownAutosave); err != nil {
		t.Fatalf("decode shutdown autosave: %v", err)
	}
	if len(shutdownAutosave.KanbanCards) != 2 {
		t.Fatalf("expected shutdown to save the latest board, got %#v", shutdownAutosave.KanbanCards)
	}
}

func TestSystemServiceMigratesLegacyWorkspaceStateToAutosave(t *testing.T) {
	root := t.TempDir()
	workspacePath := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspacePath, 0o755); err != nil {
		t.Fatal(err)
	}
	storePath := filepath.Join(root, "state.json")
	service := NewSystemServiceWithStorePath(storePath)
	state, err := service.AddWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	workspaceID := state.ActiveWorkspaceID

	legacy := storedAppState{
		Settings:          state.Settings,
		WebAccess:         state.WebAccess,
		Workspaces:        state.Workspaces,
		ActiveWorkspaceID: workspaceID,
		KanbanCards: []KanbanCard{{
			ID:                 "card-1",
			WorkspaceID:        workspaceID,
			Title:              "Legacy card",
			Description:        "Migrate me",
			AcceptanceCriteria: []string{"Migrated"},
			Lane:               KanbanLaneReady,
			Status:             KanbanLaneReady,
		}},
		ChatSessions: map[string]persistedChatSession{
			workspaceID: {
				WorkspaceID: workspaceID,
				Messages:    []ChatMessage{{ID: "msg-1", Role: llm.RoleUser, Content: "Legacy chat", Status: "complete"}},
				History:     []llm.Message{{Role: llm.RoleUser, Content: "Legacy chat"}},
			},
		},
	}
	data, err := json.MarshalIndent(legacy, "", "  ")
	if err != nil {
		t.Fatalf("encode legacy state: %v", err)
	}
	if err := os.WriteFile(storePath, data, 0o600); err != nil {
		t.Fatalf("write legacy state: %v", err)
	}

	reloaded := NewSystemServiceWithStorePath(storePath)
	board, err := reloaded.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatalf("load migrated board: %v", err)
	}
	if len(board.Ready) != 1 || board.Ready[0].Title != "Legacy card" {
		t.Fatalf("expected legacy card to migrate, got %#v", board)
	}
	chat, err := reloaded.LoadChatSession(workspaceID)
	if err != nil {
		t.Fatalf("load migrated chat: %v", err)
	}
	if len(chat.Messages) != 1 || chat.Messages[0].Content != "Legacy chat" {
		t.Fatalf("expected legacy chat to migrate, got %#v", chat.Messages)
	}
	if _, err := os.Stat(filepath.Join(workspacePath, workspaceCacheDirName, workspaceAutosaveFileName)); err != nil {
		t.Fatalf("expected migrated workspace autosave: %v", err)
	}
	globalData, err := os.ReadFile(storePath)
	if err != nil {
		t.Fatalf("read migrated global state: %v", err)
	}
	if strings.Contains(string(globalData), "kanbanCards") || strings.Contains(string(globalData), "chatSessions") {
		t.Fatalf("expected migrated global state to omit workspace data, got %s", globalData)
	}
}
