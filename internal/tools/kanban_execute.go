package tools

import (
	"encoding/json"
	"fmt"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "kanban_start_execution",
			Description: "Start kanban execution for the active workspace, picking up Ready cards. Returns immediately; execution runs asynchronously.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"workspaceID": map[string]any{
						"type":        "string",
						"description": "Workspace ID to execute kanban for. Defaults to the first workspace if omitted.",
					},
					"concurrency": map[string]any{
						"type":        "integer",
						"minimum":     1,
						"maximum":     maxAgentLimit,
						"description": "Optional concurrency limit (1–8). Defaults to 2.",
					},
				},
			},
		},
		Run: startKanbanExecution,
	})
}

const maxAgentLimit = 8

func startKanbanExecution(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var args struct {
		WorkspaceID string `json:"workspaceID,omitempty"`
		Concurrency *int   `json:"concurrency,omitempty"`
	}
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	if ctx.KanbanExecutor == nil {
		return nil, SafeError{Code: "kanban_unavailable", Message: "kanban execution is not available in this context"}
	}
	if args.WorkspaceID == "" {
		// Default to the first workspace.
		if len(ctx.WorkspaceRoots) == 0 {
			return nil, SafeError{Code: "no_workspace", Message: "no workspace is configured"}
		}
		args.WorkspaceID = ctx.WorkspaceRoots[0].ID
	}
	concurrency := 2
	if args.Concurrency != nil {
		concurrency = *args.Concurrency
		if concurrency < 1 {
			concurrency = 1
		}
		if concurrency > maxAgentLimit {
			concurrency = maxAgentLimit
		}
	}
	if err := ctx.KanbanExecutor.StartKanbanExecutionWithContext(ctx.context(), args.WorkspaceID, concurrency); err != nil {
		return nil, SafeError{Code: "kanban_start_failed", Message: err.Error()}
	}
	return map[string]any{
		"success":       true,
		"workspaceID":   args.WorkspaceID,
		"concurrency":   concurrency,
		"message":       fmt.Sprintf("Kanban execution started for workspace %q with concurrency %d.", args.WorkspaceID, concurrency),
	}, nil
}
