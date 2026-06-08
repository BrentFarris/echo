package services

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestKanbanBoardGroupsCardsByLane(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Ready work", Description: "Ready", AcceptanceCriteria: []string{"Ready"}, Lane: KanbanLaneReady},
		{ID: "card-2", WorkspaceID: workspaceID, Title: "Active work", Description: "Active", AcceptanceCriteria: []string{"Active"}, Lane: KanbanLaneInProgress},
		{ID: "card-3", WorkspaceID: workspaceID, Title: "Blocked work", Description: "Blocked", AcceptanceCriteria: []string{"Blocked"}, Lane: KanbanLaneBlocked},
		{ID: "card-4", WorkspaceID: workspaceID, Title: "Done work", Description: "Done", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneDone},
	})

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatalf("load board: %v", err)
	}
	if len(board.Ready) != 1 || len(board.InProgress) != 1 || len(board.Blocked) != 1 || len(board.Done) != 1 {
		t.Fatalf("expected one card in each lane, got %#v", board)
	}
	if board.Ready[0].ID != "card-1" || board.InProgress[0].ID != "card-2" || board.Blocked[0].ID != "card-3" || board.Done[0].ID != "card-4" {
		t.Fatalf("cards were grouped into the wrong lanes: %#v", board)
	}
}

func TestKanbanDependencyBlockedReadyCardIsNotEligible(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Prerequisite", Description: "First", AcceptanceCriteria: []string{"First"}, Lane: KanbanLaneReady},
		{ID: "card-2", WorkspaceID: workspaceID, Title: "Dependent", Description: "Second", AcceptanceCriteria: []string{"Second"}, Dependencies: []string{"card-1"}, Lane: KanbanLaneReady},
	})

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatalf("load board: %v", err)
	}
	if len(board.Ready) != 2 {
		t.Fatalf("expected two ready cards, got %#v", board.Ready)
	}
	dependent := board.Ready[1]
	if dependent.Eligible {
		t.Fatalf("expected dependent card to be ineligible, got %#v", dependent)
	}
	if len(dependent.BlockedBy) != 1 || dependent.BlockedBy[0] != "card-1" {
		t.Fatalf("expected card-1 to block dependent card, got %#v", dependent.BlockedBy)
	}

	if _, err := service.MoveKanbanCard(workspaceID, "card-2", KanbanLaneInProgress); err == nil {
		t.Fatal("expected dependency-blocked card to be rejected for execution")
	}
}

func TestKanbanCardCanStartAfterDependenciesAreDone(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Prerequisite", Description: "First", AcceptanceCriteria: []string{"First"}, Lane: KanbanLaneReady},
		{ID: "card-2", WorkspaceID: workspaceID, Title: "Dependent", Description: "Second", AcceptanceCriteria: []string{"Second"}, Dependencies: []string{"card-1"}, Lane: KanbanLaneReady},
	})

	if _, err := service.MoveKanbanCard(workspaceID, "card-1", KanbanLaneDone); err != nil {
		t.Fatalf("move prerequisite done: %v", err)
	}
	board, err := service.MoveKanbanCard(workspaceID, "card-2", KanbanLaneInProgress)
	if err != nil {
		t.Fatalf("move dependent in progress: %v", err)
	}
	if len(board.InProgress) != 1 || board.InProgress[0].ID != "card-2" {
		t.Fatalf("expected dependent card in progress, got %#v", board.InProgress)
	}
}

func TestUpdateKanbanCardDescriptionBeforeExecution(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Ready work", Description: "Original", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneReady},
	})

	board, err := service.UpdateKanbanCardDescription(workspaceID, "card-1", "  Updated detail  ")
	if err != nil {
		t.Fatalf("update description: %v", err)
	}
	if len(board.Ready) != 1 {
		t.Fatalf("expected ready card, got %#v", board)
	}
	card := board.Ready[0]
	if card.Description != "Updated detail" {
		t.Fatalf("expected trimmed description, got %q", card.Description)
	}
	if len(card.ProgressTranscript) != 1 || card.ProgressTranscript[0].Title != "Description updated" {
		t.Fatalf("expected description update in transcript, got %#v", card.ProgressTranscript)
	}
}

func TestUpdateKanbanCardDescriptionRejectsStartedCard(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Active work", Description: "Original", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneInProgress},
	})

	if _, err := service.UpdateKanbanCardDescription(workspaceID, "card-1", "Updated"); err == nil {
		t.Fatal("expected started card description edit to be rejected")
	}

	board, err := service.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatalf("load board: %v", err)
	}
	if len(board.InProgress) != 1 || board.InProgress[0].Description != "Original" {
		t.Fatalf("expected original description to be preserved, got %#v", board)
	}
}

func TestUpdateKanbanCardDescriptionRejectsRunningWorkspace(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Ready work", Description: "Original", AcceptanceCriteria: []string{"Done"}, Lane: KanbanLaneReady},
	})
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	service.chatMu.Lock()
	service.kanbanRuns[workspaceID] = cancel
	service.chatMu.Unlock()

	if _, err := service.UpdateKanbanCardDescription(workspaceID, "card-1", "Updated"); err == nil {
		t.Fatal("expected running workspace description edit to be rejected")
	}
}

func TestCreateKanbanCardFromChatMessageUsesAssistantContentOnly(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	content := "# Build search panel\n\nImplement the visible assistant plan."
	service.chatMu.Lock()
	service.chatSessions[workspaceID] = &chatSessionState{
		WorkspaceID: workspaceID,
		Messages: []ChatMessage{
			{ID: "msg-user", Role: "user", Content: "Plan search UI", Status: "complete"},
			{
				ID:        "msg-assistant",
				Role:      "assistant",
				Content:   content,
				Reasoning: "hidden thinking should not be copied",
				ToolCalls: []ChatToolActivity{{
					ID:     "call-1",
					Name:   "filesystem_read_text",
					Status: "complete",
					Result: "tool result should not be copied",
				}},
				Status: "complete",
			},
		},
	}
	service.chatMu.Unlock()

	board, err := service.CreateKanbanCardFromChatMessage(workspaceID, "msg-assistant")
	if err != nil {
		t.Fatalf("create card from chat message: %v", err)
	}
	if len(board.Ready) != 1 {
		t.Fatalf("expected one ready card, got %#v", board)
	}
	card := board.Ready[0]
	if card.Title != "Build search panel" {
		t.Fatalf("expected title from visible message, got %q", card.Title)
	}
	if card.Description != content {
		t.Fatalf("expected description to be visible content, got %q", card.Description)
	}
	if strings.Contains(card.Description, "hidden thinking") || strings.Contains(card.Description, "tool result") {
		t.Fatalf("description included hidden debug state: %q", card.Description)
	}
	if len(card.AcceptanceCriteria) != 1 || card.AcceptanceCriteria[0] == "" {
		t.Fatalf("expected default acceptance criteria, got %#v", card.AcceptanceCriteria)
	}
	if len(card.ProgressTranscript) != 1 || card.ProgressTranscript[0].Content != "Created directly from an Echo chat message." {
		t.Fatalf("expected direct creation transcript, got %#v", card.ProgressTranscript)
	}
}

func TestResetKanbanCardStartsFresh(t *testing.T) {
	service, workspaceID := newKanbanTestService(t)
	seedKanbanCards(t, service, []KanbanCard{
		{
			ID:                 "card-1",
			WorkspaceID:        workspaceID,
			Title:              "Retryable",
			Description:        "Start again",
			AcceptanceCriteria: []string{"Done"},
			Dependencies:       []string{"card-0"},
			Lane:               KanbanLaneDone,
			Status:             KanbanLaneDone,
			ProgressTranscript: []KanbanProgressEntry{{
				Type:    "result",
				Title:   "Final result",
				Content: "Old attempt.",
				Status:  KanbanLaneDone,
			}},
		},
	})

	board, err := service.ResetKanbanCard(workspaceID, "card-1")
	if err != nil {
		t.Fatalf("reset card: %v", err)
	}
	if len(board.Ready) != 1 || board.Ready[0].ID != "card-1" {
		t.Fatalf("expected reset card in ready lane, got %#v", board)
	}
	card := board.Ready[0]
	if card.Status != KanbanLaneReady || card.Lane != KanbanLaneReady {
		t.Fatalf("expected ready status after reset, got %#v", card)
	}
	if len(card.ProgressTranscript) != 0 {
		t.Fatalf("expected reset to clear transcript, got %#v", card.ProgressTranscript)
	}
	if card.Title != "Retryable" || card.Description != "Start again" || len(card.AcceptanceCriteria) != 1 || card.AcceptanceCriteria[0] != "Done" {
		t.Fatalf("expected reset to preserve card definition, got %#v", card)
	}
	if len(card.Dependencies) != 1 || card.Dependencies[0] != "card-0" {
		t.Fatalf("expected reset to preserve dependencies, got %#v", card.Dependencies)
	}
}

func TestKanbanCardStateIsRuntimeOnlyAcrossRestart(t *testing.T) {
	root := t.TempDir()
	storePath := filepath.Join(root, "state.json")
	service, workspaceID := newKanbanTestServiceWithStore(t, root, storePath)
	seedKanbanCards(t, service, []KanbanCard{
		{ID: "card-1", WorkspaceID: workspaceID, Title: "Runtime", Description: "Keep status in memory", AcceptanceCriteria: []string{"Status changes"}, Lane: KanbanLaneReady},
	})

	if _, err := service.MoveKanbanCard(workspaceID, "card-1", KanbanLaneDone); err != nil {
		t.Fatalf("move card done: %v", err)
	}

	reloaded := NewSystemServiceWithStorePath(storePath)
	board, err := reloaded.LoadKanbanBoard(workspaceID)
	if err != nil {
		t.Fatalf("load reloaded board: %v", err)
	}
	if len(board.Ready) != 0 || len(board.InProgress) != 0 || len(board.Blocked) != 0 || len(board.Done) != 0 {
		t.Fatalf("expected cards to be runtime-only after reload, got %#v", board)
	}
}

func newKanbanTestService(t *testing.T) (*SystemService, string) {
	t.Helper()
	root := t.TempDir()
	return newKanbanTestServiceWithStore(t, root, filepath.Join(root, "state.json"))
}

func newKanbanTestServiceWithStore(t *testing.T, workspacePath string, storePath string) (*SystemService, string) {
	t.Helper()
	service := NewSystemServiceWithStorePath(storePath)
	state, err := service.AddWorkspace(workspacePath)
	if err != nil {
		t.Fatalf("add workspace: %v", err)
	}
	return service, state.ActiveWorkspaceID
}

func seedKanbanCards(t *testing.T, service *SystemService, cards []KanbanCard) {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	service.state.KanbanCards = cloneKanbanCards(cards)
	if err := service.saveLocked(); err != nil {
		t.Fatalf("save seeded cards: %v", err)
	}
}
