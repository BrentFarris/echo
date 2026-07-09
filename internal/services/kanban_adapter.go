package services

import (
	"context"

	"github.com/brent/echo/internal/tools"
)

// kanbanManagerAdapter wraps SystemService to implement tools.KanbanManager.
type kanbanManagerAdapter struct {
	service *SystemService
}

func (a *kanbanManagerAdapter) MoveKanbanCard(ctx context.Context, workspaceID string, cardID string, lane string) (tools.KanbanBoard, error) {
	board, err := a.service.MoveKanbanCard(workspaceID, cardID, lane)
	return convertKanbanBoard(board), err
}

func (a *kanbanManagerAdapter) DeleteKanbanCard(ctx context.Context, workspaceID string, cardID string) (tools.KanbanBoard, error) {
	board, err := a.service.DeleteKanbanCard(workspaceID, cardID)
	return convertKanbanBoard(board), err
}

func (a *kanbanManagerAdapter) ResetKanbanCard(ctx context.Context, workspaceID string, cardID string) (tools.KanbanBoard, error) {
	board, err := a.service.ResetKanbanCard(workspaceID, cardID)
	return convertKanbanBoard(board), err
}

func (a *kanbanManagerAdapter) UpdateKanbanCardDescription(ctx context.Context, workspaceID string, cardID string, description string) (tools.KanbanBoard, error) {
	board, err := a.service.UpdateKanbanCardDescription(workspaceID, cardID, description)
	return convertKanbanBoard(board), err
}

func (a *kanbanManagerAdapter) StopKanbanCard(ctx context.Context, workspaceID string, cardID string) error {
	_, err := a.service.StopKanbanCard(workspaceID, cardID)
	return err
}

func convertKanbanBoard(board KanbanBoard) tools.KanbanBoard {
	result := tools.KanbanBoard{
		WorkspaceID: board.WorkspaceID,
		Ready:       make([]tools.KanbanCard, len(board.Ready)),
		InProgress:  make([]tools.KanbanCard, len(board.InProgress)),
		Blocked:     make([]tools.KanbanCard, len(board.Blocked)),
		Done:        make([]tools.KanbanCard, len(board.Done)),
	}
	for i, card := range board.Ready {
		result.Ready[i] = convertKanbanCard(card)
	}
	for i, card := range board.InProgress {
		result.InProgress[i] = convertKanbanCard(card)
	}
	for i, card := range board.Blocked {
		result.Blocked[i] = convertKanbanCard(card)
	}
	for i, card := range board.Done {
		result.Done[i] = convertKanbanCard(card)
	}
	return result
}

func convertKanbanCard(card KanbanCard) tools.KanbanCard {
	return tools.KanbanCard{
		ID:                 card.ID,
		WorkspaceID:        card.WorkspaceID,
		Title:              card.Title,
		Description:        card.Description,
		Lane:               card.Lane,
		Status:             card.Status,
		AcceptanceCriteria: append([]string(nil), card.AcceptanceCriteria...),
	}
}
