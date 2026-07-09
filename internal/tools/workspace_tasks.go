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
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional arbitrary backlog tags.",
					},
					"priority": map[string]any{
						"type":        "string",
						"enum":        []any{"P0", "P1", "P2"},
						"description": "Task priority. Defaults to P1.",
					},
				"epic": map[string]any{
					"type":        "string",
					"description": "Optional epic/group name for grouping related tasks.",
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
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "workspace_task_update",
			Description: "Edit an existing task's title, description, acceptance criteria.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"taskID", "expectedUpdatedAt"},
				"properties": map[string]any{
					"taskID": map[string]any{
						"type":        "string",
						"description": "The ID of the task to update.",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "New concise task title.",
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
						"description": "Task priority.",
					},
					"epic": map[string]any{
						"type":        "string",
						"description": "Optional epic/group name for grouping related tasks.",
					},
					"tags": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional free-form tags (e.g. frontend, bug, performance).",
					},
					"expectedUpdatedAt": map[string]any{
						"type":        "string",
						"description": "The updatedAt timestamp from the task, for optimistic concurrency.",
					},
				},
			},
		},
		Run: updateWorkspaceTask,
	})
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "workspace_task_delete",
			Description: "Remove a backlog task.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"taskID", "expectedUpdatedAt"},
				"properties": map[string]any{
					"taskID": map[string]any{
						"type":        "string",
						"description": "The ID of the task to delete.",
					},
					"expectedUpdatedAt": map[string]any{
						"type":        "string",
						"description": "The updatedAt timestamp from the task, for optimistic concurrency.",
					},
				},
			},
		},
		Run: deleteWorkspaceTask,
	})
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "workspace_task_set_completed",
			Description: "Toggle a task between done/undone.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"taskID", "completed", "expectedUpdatedAt"},
				"properties": map[string]any{
					"taskID": map[string]any{
						"type":        "string",
						"description": "The ID of the task.",
					},
					"completed": map[string]any{
						"type":        "boolean",
						"description": "Whether the task is completed.",
					},
					"expectedUpdatedAt": map[string]any{
						"type":        "string",
						"description": "The updatedAt timestamp from the task, for optimistic concurrency.",
					},
				},
			},
		},
		Run: setWorkspaceTaskCompleted,
	})
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "workspace_task_move",
			Description: "Change task priority (P0/P1/P2).",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"taskID", "priority", "expectedUpdatedAt"},
				"properties": map[string]any{
					"taskID": map[string]any{
						"type":        "string",
						"description": "The ID of the task.",
					},
					"priority": map[string]any{
						"type":        "string",
						"enum":        []any{"P0", "P1", "P2"},
						"description": "New task priority.",
					},
					"expectedUpdatedAt": map[string]any{
						"type":        "string",
						"description": "The updatedAt timestamp from the task, for optimistic concurrency.",
					},
				},
			},
		},
		Run: moveWorkspaceTask,
	})
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "workspace_task_reorder",
			Description: "Reorder backlog tasks within a priority lane. Accepts an ordered list of task IDs and assigns sequential sort orders.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"taskIDs", "priority"},
				"properties": map[string]any{
					"taskIDs": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Ordered list of task IDs for the target priority lane.",
					},
					"priority": map[string]any{
						"type":        "string",
						"enum":        []any{"P0", "P1", "P2"},
						"description": "Target priority lane (P0, P1, or P2).",
					},
				},
			},
		},
		Run: reorderWorkspaceTasks,
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

	tags := request.Tags[:0]
	seenTags := map[string]bool{}
	for _, tag := range request.Tags {
		tag = strings.TrimSpace(tag)
		key := strings.ToLower(tag)
		if tag != "" && !seenTags[key] {
			seenTags[key] = true
			tags = append(tags, tag)
		}
	}
	request.Tags = tags

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

func updateWorkspaceTask(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var request WorkspaceTaskUpdateRequest
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	request.TaskID = strings.TrimSpace(request.TaskID)
	request.Title = strings.TrimSpace(request.Title)
	request.Details = strings.TrimSpace(request.Details)
	request.Priority = strings.ToUpper(strings.TrimSpace(request.Priority))
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

	tags := request.Tags[:0]
	for _, tag := range request.Tags {
		if tag = strings.TrimSpace(tag); tag != "" {
			tags = append(tags, tag)
		}
	}
	request.Tags = tags

	if ctx.WorkspaceTasks == nil {
		return nil, SafeError{Code: "workspace_tasks_unavailable", Message: "workspace tasks are not available in this context"}
	}
	return ctx.WorkspaceTasks.UpdateWorkspaceTask(ctx.context(), request)
}

func deleteWorkspaceTask(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var request WorkspaceTaskDeleteRequest
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	request.TaskID = strings.TrimSpace(request.TaskID)
	request.ExpectedUpdatedAt = strings.TrimSpace(request.ExpectedUpdatedAt)
	if request.TaskID == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "taskID is required"}
	}
	if request.ExpectedUpdatedAt == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "expectedUpdatedAt is required"}
	}
	if ctx.WorkspaceTasks == nil {
		return nil, SafeError{Code: "workspace_tasks_unavailable", Message: "workspace tasks are not available in this context"}
	}
	if err := ctx.WorkspaceTasks.DeleteWorkspaceTask(ctx.context(), request); err != nil {
		return nil, SafeError{Code: "task_delete_failed", Message: err.Error()}
	}
	return map[string]any{"success": true, "taskID": request.TaskID}, nil
}

func setWorkspaceTaskCompleted(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var request WorkspaceTaskCompleteRequest
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	request.TaskID = strings.TrimSpace(request.TaskID)
	request.ExpectedUpdatedAt = strings.TrimSpace(request.ExpectedUpdatedAt)
	if request.TaskID == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "taskID is required"}
	}
	if request.ExpectedUpdatedAt == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "expectedUpdatedAt is required"}
	}
	if ctx.WorkspaceTasks == nil {
		return nil, SafeError{Code: "workspace_tasks_unavailable", Message: "workspace tasks are not available in this context"}
	}
	return ctx.WorkspaceTasks.SetWorkspaceTaskCompleted(ctx.context(), request)
}

func moveWorkspaceTask(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var request WorkspaceTaskMoveRequest
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	request.TaskID = strings.TrimSpace(request.TaskID)
	request.Priority = strings.ToUpper(strings.TrimSpace(request.Priority))
	request.ExpectedUpdatedAt = strings.TrimSpace(request.ExpectedUpdatedAt)
	if request.TaskID == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "taskID is required"}
	}
	if request.Priority != "P0" && request.Priority != "P1" && request.Priority != "P2" {
		return nil, SafeError{Code: "invalid_arguments", Message: "priority must be P0, P1, or P2"}
	}
	if request.ExpectedUpdatedAt == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "expectedUpdatedAt is required"}
	}
	if ctx.WorkspaceTasks == nil {
		return nil, SafeError{Code: "workspace_tasks_unavailable", Message: "workspace tasks are not available in this context"}
	}
	return ctx.WorkspaceTasks.MoveWorkspaceTask(ctx.context(), request)
}

func reorderWorkspaceTasks(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var request WorkspaceTaskReorderRequest
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	request.Priority = strings.ToUpper(strings.TrimSpace(request.Priority))
	if request.Priority != "P0" && request.Priority != "P1" && request.Priority != "P2" {
		return nil, SafeError{Code: "invalid_arguments", Message: "priority must be P0, P1, or P2"}
	}
	if len(request.TaskIDs) == 0 {
		return nil, SafeError{Code: "invalid_arguments", Message: "taskIDs are required"}
	}
	if ctx.WorkspaceTasks == nil {
		return nil, SafeError{Code: "workspace_tasks_unavailable", Message: "workspace tasks are not available in this context"}
	}
	return ctx.WorkspaceTasks.ReorderWorkspaceTasks(ctx.context(), request)
}
