package tools

import (
	"encoding/json"
	"strings"
)

func init() {
	Register(ToolFunc{
		Meta: Metadata{
			Name:        "lsp_query",
			Description: "Use the workspace language server for code navigation. Supports definitions, references, implementations, type definitions, hover info, document symbols, and completion/member candidates once you know the file and symbol position.",
			Parameters: Schema{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []any{"operation", "path"},
				"properties": map[string]any{
					"operation": map[string]any{
						"type":        "string",
						"description": "Navigation operation to run. Use members or completion at the position after a dot to inspect available fields or methods.",
						"enum": []any{
							"definition",
							"references",
							"implementation",
							"type_definition",
							"hover",
							"document_symbols",
							"completion",
							"members",
						},
					},
					"path": map[string]any{
						"type":        "string",
						"description": "Labeled workspace source file path. " + labeledPathSchemaHint,
					},
					"line": map[string]any{
						"type":        "integer",
						"description": "1-based source line for operations that need a cursor position.",
						"minimum":     1,
					},
					"column": map[string]any{
						"type":        "integer",
						"description": "1-based character column for operations that need a cursor position.",
						"minimum":     1,
					},
					"position": map[string]any{
						"type":        "integer",
						"description": "UTF-16 offset in the file. Prefer line and column unless the UTF-16 offset is already known.",
						"minimum":     0,
					},
					"includeDeclaration": map[string]any{
						"type":        "boolean",
						"description": "For references, include the declaration location. Defaults to true.",
					},
					"maxResults": map[string]any{
						"type":        "integer",
						"description": "Maximum locations, symbols, or completion items to return. Defaults to 100 and is capped at 200.",
						"minimum":     1,
						"maximum":     200,
					},
					"triggerKind": map[string]any{
						"type":        "integer",
						"description": "Optional LSP completion trigger kind for completion or members operations.",
					},
					"triggerCharacter": map[string]any{
						"type":        "string",
						"description": "Optional LSP completion trigger character, such as '.'.",
					},
				},
			},
		},
		Run: queryLSP,
	})
}

func queryLSP(ctx ExecutionContext, arguments json.RawMessage) (any, error) {
	if err := ctx.context().Err(); err != nil {
		return nil, err
	}
	var request CodeNavigationRequest
	if len(arguments) > 0 {
		if err := DecodeToolArguments(arguments, &request); err != nil {
			return nil, SafeError{Code: "invalid_arguments", Message: "arguments must be valid JSON"}
		}
	}
	request.Operation = normalizeLSPQueryOperation(request.Operation)
	if request.Operation == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "operation must be one of definition, references, implementation, type_definition, hover, document_symbols, completion, or members"}
	}
	request.Path = strings.TrimSpace(request.Path)
	if request.Path == "" {
		return nil, SafeError{Code: "invalid_arguments", Message: "path is required"}
	}
	if ctx.CodeNavigator == nil {
		return nil, SafeError{Code: "lsp_unavailable", Message: "language server navigation is not available in this context"}
	}
	return ctx.CodeNavigator.QueryCode(ctx.context(), request)
}

func normalizeLSPQueryOperation(operation string) string {
	operation = strings.ToLower(strings.TrimSpace(operation))
	operation = strings.ReplaceAll(operation, "-", "_")
	switch operation {
	case "definition", "references", "implementation", "hover", "completion", "members":
		return operation
	case "type_definition", "typedef", "type":
		return "type_definition"
	case "document_symbols", "document_symbol", "symbols", "outline":
		return "document_symbols"
	default:
		return ""
	}
}
