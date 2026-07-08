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
				},
			},
		},
		Run: createWorkspaceTask,
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
