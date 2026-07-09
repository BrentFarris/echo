package tools

import (
	"encoding/json"
	"fmt"
	"strings"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "kanban_move_card",
			Description: "Move a card to another lane (ready/inProgress/blocked/done).",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"cardID", "lane"},
				"properties": map[string]any{
					"cardID": map[string]any{
						"type":        "string",
						"description": "The ID of the kanban card.",
					},
					"lane": map[string]any{
						"type":        "string",
						"enum":        []any{"ready", "inProgress", "blocked", "done"},
						"description": "Target lane.",
					},
				},
			},
		},
		Run: kanbanMoveCard,
	})
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "kanban_delete_card",
			Description: "Remove a kanban card.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"cardID"},
				"properties": map[string]any{
					"cardID": map[string]any{
						"type":        "string",
						"description": "The ID of the kanban card to delete.",
					},
				},
			},
		},
		Run: kanbanDeleteCard,
	})
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "kanban_reset_card",
			Description: "Reset a card back to ready lane.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"cardID"},
				"properties": map[string]any{
					"cardID": map[string]any{
						"type":        "string",
						"description": "The ID of the kanban card to reset.",
					},
				},
			},
		},
		Run: kanbanResetCard,
	})
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "kanban_update_card_description",
			Description: "Edit a card's description.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"cardID", "description"},
				"properties": map[string]any{
					"cardID": map[string]any{
						"type":        "string",
						"description": "The ID of the kanban card.",
					},
					"description": map[string]any{
						"type":        "string",
						"description": "New card description.",
					},
				},
			},
		},
		Run: kanbanUpdateCardDescription,
	})
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "kanban_stop_card",
			Description: "Stop execution of a running card.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"cardID"},
				"properties": map[string]any{
					"cardID": map[string]any{
						"type":        "string",
						"description": "The ID of the kanban card to stop.",
					},
				},
			},
		},
		Run: kanbanStopCard,
	})
}

func kanbanMoveCard(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args struct {
		CardID string `json:"cardID"`
		Lane   string `json:"lane"`
	}
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	args.CardID = strings.TrimSpace(args.CardID)
	args.Lane = strings.TrimSpace(args.Lane)
	if args.CardID == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "cardID is required"}
	}
	if args.Lane != "ready" && args.Lane != "inProgress" && args.Lane != "blocked" && args.Lane != "done" {
		return nil, SafeError{Code: "invalid_arguments", Message: "lane must be ready, inProgress, blocked, or done"}
	}
	if ctx.KanbanManager == nil {
		return nil, SafeError{Code: "kanban_unavailable", Message: "kanban management is not available in this context"}
	}
	workspaceID := resolveWorkspaceID(ctx)
	board, err := ctx.KanbanManager.MoveKanbanCard(ctx.context(), workspaceID, args.CardID, args.Lane)
	if err != nil {
		return nil, SafeError{Code: "kanban_move_failed", Message: err.Error()}
	}
	return kanbanBoardToMap(board), nil
}

func kanbanDeleteCard(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args struct {
		CardID string `json:"cardID"`
	}
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	args.CardID = strings.TrimSpace(args.CardID)
	if args.CardID == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "cardID is required"}
	}
	if ctx.KanbanManager == nil {
		return nil, SafeError{Code: "kanban_unavailable", Message: "kanban management is not available in this context"}
	}
	workspaceID := resolveWorkspaceID(ctx)
	board, err := ctx.KanbanManager.DeleteKanbanCard(ctx.context(), workspaceID, args.CardID)
	if err != nil {
		return nil, SafeError{Code: "kanban_delete_failed", Message: err.Error()}
	}
	return kanbanBoardToMap(board), nil
}

func kanbanResetCard(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args struct {
		CardID string `json:"cardID"`
	}
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	args.CardID = strings.TrimSpace(args.CardID)
	if args.CardID == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "cardID is required"}
	}
	if ctx.KanbanManager == nil {
		return nil, SafeError{Code: "kanban_unavailable", Message: "kanban management is not available in this context"}
	}
	workspaceID := resolveWorkspaceID(ctx)
	board, err := ctx.KanbanManager.ResetKanbanCard(ctx.context(), workspaceID, args.CardID)
	if err != nil {
		return nil, SafeError{Code: "kanban_reset_failed", Message: err.Error()}
	}
	return kanbanBoardToMap(board), nil
}

func kanbanUpdateCardDescription(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args struct {
		CardID      string `json:"cardID"`
		Description string `json:"description"`
	}
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	args.CardID = strings.TrimSpace(args.CardID)
	args.Description = strings.TrimSpace(args.Description)
	if args.CardID == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "cardID is required"}
	}
	if args.Description == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "description is required"}
	}
	if ctx.KanbanManager == nil {
		return nil, SafeError{Code: "kanban_unavailable", Message: "kanban management is not available in this context"}
	}
	workspaceID := resolveWorkspaceID(ctx)
	board, err := ctx.KanbanManager.UpdateKanbanCardDescription(ctx.context(), workspaceID, args.CardID, args.Description)
	if err != nil {
		return nil, SafeError{Code: "kanban_update_failed", Message: err.Error()}
	}
	return kanbanBoardToMap(board), nil
}

func kanbanStopCard(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args struct {
		CardID string `json:"cardID"`
	}
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	args.CardID = strings.TrimSpace(args.CardID)
	if args.CardID == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "cardID is required"}
	}
	if ctx.KanbanManager == nil {
		return nil, SafeError{Code: "kanban_unavailable", Message: "kanban management is not available in this context"}
	}
	workspaceID := resolveWorkspaceID(ctx)
	if err := ctx.KanbanManager.StopKanbanCard(ctx.context(), workspaceID, args.CardID); err != nil {
		return nil, SafeError{Code: "kanban_stop_failed", Message: err.Error()}
	}
	return map[string]any{
		"success":   true,
		"cardID":    args.CardID,
		"message":   fmt.Sprintf("Card %q stopped.", args.CardID),
	}, nil
}

// resolveWorkspaceID returns the first workspace ID from context.
func resolveWorkspaceID(ctx ExecutionContext) string {
	if len(ctx.WorkspaceRoots) == 0 {
		return ""
	}
	return ctx.WorkspaceRoots[0].ID
}

// kanbanBoardToMap converts a KanbanBoard to a map for JSON response.
func kanbanBoardToMap(board KanbanBoard) map[string]any {
	countReady := len(board.Ready)
	countInProgress := len(board.InProgress)
	countBlocked := len(blockedCards(board.Blocked))
	countDone := len(board.Done)
	return map[string]any{
		"workspaceId": board.WorkspaceID,
		"ready":       countReady,
		"inProgress":  countInProgress,
		"blocked":     countBlocked,
		"done":        countDone,
	}
}

func blockedCards(cards []KanbanCard) []KanbanCard {
	return cards
}
