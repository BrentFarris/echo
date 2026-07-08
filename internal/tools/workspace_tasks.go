package tools

import (
	"encoding/json"
	"strings"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "workspace_task_list",
			Description: "List backlog tasks for the active Echo workspace so you can review, summarize, prioritize, or plan from them.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"properties": map[string]any{
					"priority": map[string]any{
						"type":        "string",
						"enum":        []any{"P0", "P1", "P2"},
						"description": "Optional priority filter.",
					},
					"includeCompleted": map[string]any{
						"type":        "boolean",
						"description": "Include completed tasks. Defaults to false.",
					},
				},
			},
		},
		Run: listWorkspaceTasks,
	})
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "workspace_task_create",
			Description: "Create one future-work card in the active Echo workspace backlog. This is allowed in Plan Mode because it records planning data rather than changing project source.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"title"},
				"properties": map[string]any{
					"title": map[string]any{
						"type":        "string",
						"description": "Concise task title.",
					},
					"details": map[string]any{
						"type":        "string",
						"description": "Optional Markdown details.",
					},
					"acceptanceCriteria": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional acceptance criteria.",
					},
					"priority": map[string]any{
						"type":        "string",
						"enum":        []any{"P0", "P1", "P2"},
						"description": "Task priority. Defaults to P1.",
					},
				},
			},
		},
		Run: createWorkspaceTask,
	})
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "workspace_task_convert_to_kanban",
			Description: "Convert a backlog task into a Ready Kanban card for agent execution. The task is marked completed and moved to done; a new card appears in the Ready lane.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"taskID", "expectedUpdatedAt"},
				"properties": map[string]any{
					"taskID": map[string]any{
						"type":        "string",
						"description": "The ID of the task to convert.",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "Override card title; defaults to task title.",
					},
					"description": map[string]any{
						"type":        "string",
						"description": "Override card description; defaults to task details.",
					},
					"acceptanceCriteria": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Override acceptance criteria; defaults to task criteria.",
					},
					"expectedUpdatedAt": map[string]any{
						"type":        "string",
						"description": "The updatedAt timestamp from the task, for optimistic concurrency.",
					},
				},
			},
		},
		Run: convertTaskToKanbanCard,
	})
}

func listWorkspaceTasks(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var request WorkspaceTaskListRequest
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	request.Priority = strings.ToUpper(strings.TrimSpace(request.Priority))
	if request.Priority != "" && request.Priority != "P0" && request.Priority != "P1" && request.Priority != "P2" {
		return nil, SafeError{Code: "invalid_arguments", Message: "priority must be P0, P1, or P2"}
	}
	if ctx.WorkspaceTasks == nil {
		return nil, SafeError{Code: "workspace_tasks_unavailable", Message: "workspace tasks are not available in this context"}
	}
	return ctx.WorkspaceTasks.ListWorkspaceTasks(ctx.context(), request)
}

func createWorkspaceTask(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var request WorkspaceTaskCreateRequest
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	request.Title = strings.TrimSpace(request.Title)
	request.Details = strings.TrimSpace(request.Details)
	request.Priority = strings.ToUpper(strings.TrimSpace(request.Priority))
	if request.Priority == "" {
		request.Priority = "P1"
	}
	if request.Title == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "title is required"}
	}
	if request.Priority != "P0" && request.Priority != "P1" && request.Priority != "P2" {
		return nil, SafeError{Code: "invalid_arguments", Message: "priority must be P0, P1, or P2"}
	}
	criteria := request.AcceptanceCriteria[:0]
	for _, criterion := range request.AcceptanceCriteria {
		if criterion = strings.TrimSpace(criterion); criterion != "" {
			criteria = append(criteria, criterion)
		}
	}
	request.AcceptanceCriteria = criteria
	if ctx.WorkspaceTasks == nil {
		return nil, SafeError{Code: "workspace_tasks_unavailable", Message: "workspace tasks are not available in this context"}
	}
	return ctx.WorkspaceTasks.CreateWorkspaceTask(ctx.context(), request)
}

func convertTaskToKanbanCard(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var request WorkspaceTaskConvertRequest
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	request.TaskID = strings.TrimSpace(request.TaskID)
	request.Title = strings.TrimSpace(request.Title)
	request.Description = strings.TrimSpace(request.Description)
	request.ExpectedUpdatedAt = strings.TrimSpace(request.ExpectedUpdatedAt)
	if request.TaskID == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "taskID is required"}
	}
	if request.ExpectedUpdatedAt == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "expectedUpdatedAt is required"}
	}
	criteria := request.AcceptanceCriteria[:0]
	for _, criterion := range request.AcceptanceCriteria {
		if criterion = strings.TrimSpace(criterion); criterion != "" {
			criteria = append(criteria, criterion)
		}
	}
	request.AcceptanceCriteria = criteria
	if ctx.WorkspaceTasks == nil {
		return nil, SafeError{Code: "workspace_tasks_unavailable", Message: "workspace tasks are not available in this context"}
	}
	return ctx.WorkspaceTasks.ConvertTaskToKanbanCard(ctx.context(), request)
}
