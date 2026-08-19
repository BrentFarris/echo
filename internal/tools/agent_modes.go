package tools

import (
	"encoding/json"
	"strings"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "create_agent_mode",
			Description: "Create a new user-defined agent mode with explicit parameters for its name, system prompt, tool permissions, and path permissions.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"name"},
				"properties": map[string]any{
					"name": map[string]any{
						"type":        "string",
						"description": "Human-readable mode name, at most 80 characters.",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "Custom system prompt guidance, or an empty string for a permissions-only mode.",
					},
					"toolPermissions": map[string]any{
						"type":        "array",
						"description": "Legacy list of allowed tool names. Omit for unrestricted tool access.",
						"items":       map[string]any{"type": "string"},
					},
					"pathPermissions": map[string]any{
						"type":        "array",
						"description": "Legacy glob patterns applied to every tool in toolPermissions.",
						"items":       map[string]any{"type": "string"},
					},
					"permissions": map[string]any{
						"type":        "object",
						"description": "Preferred mapping of allowed tool names to workspace-relative path globs. An empty path list allows that tool on every path.",
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
	if err := DecodeToolArguments(arguments, &request); err != nil {
		return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
	}
	request.Name = strings.TrimSpace(request.Name)
	request.Prompt = strings.TrimSpace(request.Prompt)
	if request.Name == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "name is required"}
	}
	if ctx.AgentModes == nil {
		return nil, SafeError{Code: "agent_modes_unavailable", Message: "agent mode management is not available in this context"}
	}
	return ctx.AgentModes.CreateAgentMode(ctx.context(), request)
}
