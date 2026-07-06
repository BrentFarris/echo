package tools

import (
	"encoding/json"
	"strings"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "create_agent_mode",
			Description: "Create a new user-defined agent mode with explicit parameters for name, system prompt, tool permissions, and path permissions.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"name"},
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Mode name in lowercase kebab-case, at most 64 characters.",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "Custom system prompt guidance for this mode, or empty string.",
					},
					"toolPermissions": map[string]any{
						"type":        "array",
						"description": "List of allowed tool names; omit for all-tool access.",
						"items":       map[string]any{"type": "string"},
					},
				"pathPermissions": map[string]any{
					"type":        "array",
					"description": "Glob patterns for allowed workspace paths; omit for unrestricted.",
					"items":       map[string]any{"type": "string"},
				},
				"permissions": map[string]any{
					"type":        "object",
					"description": "Per-tool path permissions mapping tool names to glob patterns; omit to use flat toolPermissions and pathPermissions instead.",
					"additionalProperties": map[string]any{
						"type":  "array",
						"items": map[string]any{"type": "string"},
					},
				},
				},
			},
		},
		Run: createAgentMode,
	})
}

func createAgentMode(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var request AgentModeCreationRequest
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &request); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "name is required"}
	}
	if ctx.AgentModes == nil {
		return nil, SafeError{Code: "agent_modes_unavailable", Message: "agent mode management is not available in this context"}
	}
	if request.Permissions != nil {
		return ctx.AgentModes.CreateModePerTool(ctx.context(), request.Name, request.Prompt, request.Permissions)
	}
	return ctx.AgentModes.CreateMode(ctx.context(), request)
}
