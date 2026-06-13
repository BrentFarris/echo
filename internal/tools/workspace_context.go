package tools

import (
	"encoding/json"
	"strings"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "workspace_context",
			Description: "Build a compact programming context brief for a task. Prefer this for broad implementation planning when target files are unknown; it returns likely files, manifests, commands, tests, verification commands, and optional language-server symbols.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"task"},
				"properties": map[string]any{
					"task": map[string]any{
						"type":        "string",
						"description": "The programming task, bug, feature, or investigation to gather context for.",
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Optional labeled workspace file or directory path to focus the brief. Defaults to . for all workspace folders. " + labeledPathSchemaHint,
					},
					"changedPaths": map[string]any{
						"type":        "array",
						"description": "Optional labeled workspace paths already changed or known to be relevant. " + labeledChangedPathsSchemaHint,
						"items":       map[string]any{"type": "string"},
					},
					"maxFiles": map[string]any{
						"type":        "integer",
						"description": "Maximum relevant files to return. Defaults to 12 and is capped at 30.",
						"minimum":     1,
						"maximum":     MaxWorkspaceContextMaxFiles,
					},
				},
			},
		},
		Run: queryWorkspaceContext,
	})
}

func queryWorkspaceContext(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var request WorkspaceContextRequest
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &request); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	request = NormalizeWorkspaceContextRequest(request)
	if strings.TrimSpace(request.Task) == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "task is required"}
	}
	if ctx.WorkspaceContext == nil {
		return nil, SafeError{Code: "workspace_context_unavailable", Message: "workspace context is not available in this context"}
	}
	return ctx.WorkspaceContext.QueryWorkspaceContext(ctx.context(), request)
}
