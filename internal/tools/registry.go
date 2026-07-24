package tools

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/brent/echo/internal/llm"
)

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var defaultRegistry = NewRegistry()

var readOnlyToolNames = map[string]bool{
	"filesystem_list":             true,
	"filesystem_read_image":       true,
	"filesystem_read_video":       true,
	"filesystem_read_text":        true,
	"filesystem_search_text":      true,
	"filesystem_search_workspace": true,
	"filesystem_stat":             true,
	"git_inspect":                 true,
	"lsp_query":                   true,
	"web_read":                    true,
	"web_search":                  true,
	"workspace_context":           true,
	"workspace_skill_read":        true,
	"workspace_skill_search":      true,
	"workspace_task_list":         true,
}

var mutatingToolNames = map[string]bool{
	"comfyui_generate":                 true,
	"create_agent_mode":                true,
	"filesystem_create_text":           true,
	"filesystem_delete_file":           true,
	"filesystem_edit_text":             true,
	"kanban_delete_card":               true,
	"kanban_move_card":                 true,
	"kanban_reset_card":                true,
	"kanban_start_execution":           true,
	"kanban_stop_card":                 true,
	"kanban_update_card_description":   true,
	"restart":                          true,
	"save_image":                       true,
	"shell_command":                    true,
	"workspace_skill_record":           true,
	"workspace_task_create":            true,
	"workspace_task_convert_to_kanban": true,
	"workspace_task_delete":            true,
	"workspace_task_move":              true,
	"workspace_task_set_completed":     true,
	"workspace_task_update":            true,
}

var planModeDirectToolNames = func() map[string]bool {
	result := make(map[string]bool, len(readOnlyToolNames)+1)
	for name, allowed := range readOnlyToolNames {
		result[name] = allowed
	}
	result["workspace_task_create"] = true
	return result
}()

var planModeToolNames = func() map[string]bool {
	result := make(map[string]bool, len(planModeDirectToolNames)+len(researchAgentToolNames))
	for name, allowed := range planModeDirectToolNames {
		result[name] = allowed
	}
	for name, allowed := range researchAgentToolNames {
		result[name] = allowed
	}
	return result
}()

var researchAgentToolNames = map[string]bool{
	"research_agents_spawn":  true,
	"research_agent_send":    true,
	"research_agents_wait":   true,
	"research_agents_cancel": true,
}

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

func Register(tool Tool) {
	MustRegister(defaultRegistry, tool)
}

func MustRegister(registry *Registry, tool Tool) {
	if err := registry.Register(tool); err != nil {
		panic(err)
	}
}

func (r *Registry) Register(tool Tool) error {
	if tool == nil {
		return fmt.Errorf("tool is required")
	}
	metadata := tool.Metadata()
	if metadata.Name == "" {
		return fmt.Errorf("tool name is required")
	}
	if !toolNamePattern.MatchString(metadata.Name) {
		return fmt.Errorf("tool name %q must match %s", metadata.Name, toolNamePattern.String())
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tools[metadata.Name]; exists {
		return fmt.Errorf("duplicate tool name: %s", metadata.Name)
	}
	r.tools[metadata.Name] = tool
	return nil
}

func Registered() []Tool {
	return defaultRegistry.Registered()
}

func (r *Registry) Registered() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)

	registered := make([]Tool, 0, len(names))
	for _, name := range names {
		registered = append(registered, r.tools[name])
	}
	return registered
}

func LLMSchema() []llm.Tool {
	return schemaExcludingTools(defaultRegistry.Registered(), researchAgentToolNames)
}

// ChatLLMSchema includes chat-only orchestration tools in addition to the
// normal agent tool set. Other agent surfaces must use LLMSchema instead.
func ChatLLMSchema() []llm.Tool {
	return defaultRegistry.LLMSchema()
}

func ReadOnlyLLMSchema() []llm.Tool {
	return defaultRegistry.ReadOnlyLLMSchema()
}

func PlanModeLLMSchema() []llm.Tool {
	return schemaForTools(defaultRegistry.Registered(), planModeToolNames)
}

func PlanModeDirectLLMSchema() []llm.Tool {
	return schemaForTools(defaultRegistry.Registered(), planModeDirectToolNames)
}

func ResearchLLMSchema() []llm.Tool {
	return schemaForTools(defaultRegistry.Registered(), readOnlyToolNames)
}

func IsReadOnlyToolName(name string) bool {
	return readOnlyToolNames[name]
}

func IsPlanModeToolName(name string) bool {
	return planModeToolNames[name]
}

func IsMutatingToolName(name string) bool {
	return mutatingToolNames[name]
}

func IsResearchAgentToolName(name string) bool {
	return researchAgentToolNames[name]
}

func (r *Registry) LLMSchema() []llm.Tool {
	return schemaForTools(r.Registered(), nil)
}

func (r *Registry) ReadOnlyLLMSchema() []llm.Tool {
	return schemaForTools(r.Registered(), readOnlyToolNames)
}

func schemaForTools(registered []Tool, include map[string]bool) []llm.Tool {
	schema := make([]llm.Tool, 0, len(registered))
	for _, tool := range registered {
		metadata := tool.Metadata()
		if include != nil && !include[metadata.Name] {
			continue
		}
		schema = append(schema, llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        metadata.Name,
				Description: metadata.Description,
				Parameters:  cloneSchema(metadata.Parameters),
			},
		})
	}
	return schema
}

func schemaExcludingTools(registered []Tool, exclude map[string]bool) []llm.Tool {
	schema := make([]llm.Tool, 0, len(registered))
	for _, tool := range registered {
		metadata := tool.Metadata()
		if exclude[metadata.Name] {
			continue
		}
		schema = append(schema, llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name:        metadata.Name,
				Description: metadata.Description,
				Parameters:  cloneSchema(metadata.Parameters),
			},
		})
	}
	return schema
}

func Execute(ctx ExecutionContext, name string, arguments json.RawMessage) ExecutionResult {
	return defaultRegistry.Execute(ctx, name, arguments)
}

func (r *Registry) Execute(ctx ExecutionContext, name string, arguments json.RawMessage) (result ExecutionResult) {
	result = ExecutionResult{Tool: name}
	if ctx.FlowLog != nil {
		ctx.FlowLog.Log(slog.LevelInfo, "tool_request",
			slog.String("tool_call_id", ctx.ToolCallID),
			slog.String("tool", name),
			slog.String("arguments", string(arguments)),
		)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			result.Success = false
			result.Output = nil
			result.Error = &ExecutionError{Code: "tool_panic", Message: fmt.Sprintf("tool execution failed: %v", recovered)}
		}
		if ctx.FlowLog != nil {
			data, err := json.Marshal(result)
			if err != nil {
				data = []byte(fmt.Sprintf(`{"tool":%q,"success":false,"error":{"code":"marshal_error","message":%q}}`, name, err.Error()))
			}
			ctx.FlowLog.Log(slog.LevelInfo, "tool_execution_result",
				slog.String("tool_call_id", ctx.ToolCallID),
				slog.String("tool", name),
				slog.String("payload", string(data)),
			)
		}
	}()

	/* Permission checks: validate tool allowlist and path scope before execution. */
	if err := r.checkPermissions(ctx, name, arguments); err != nil {
		result.Error = err
		return result
	}

	tool, ok := r.lookup(name)
	if !ok {
		result.Error = &ExecutionError{Code: "tool_not_found", Message: fmt.Sprintf("tool %q is not registered", name)}
		return result
	}

	runContext := ctx.context()
	if err := runContext.Err(); err != nil {
		result.Error = &ExecutionError{Code: "canceled", Message: "tool execution was canceled"}
		return result
	}

	ctx.emit(Event{Type: "started", Tool: name})
	output, err := tool.Execute(ctx, arguments)
	if err != nil {
		if contextErr := runContext.Err(); contextErr != nil {
			result.Error = &ExecutionError{Code: "canceled", Message: "tool execution was canceled"}
			ctx.emit(Event{Type: "canceled", Tool: name})
			return result
		}
		result.Error = safeError("tool_error", err)
		ctx.emit(Event{Type: "error", Tool: name, Message: result.Error.Message})
		return result
	}
	if err := runContext.Err(); err != nil {
		result.Error = &ExecutionError{Code: "canceled", Message: "tool execution was canceled"}
		ctx.emit(Event{Type: "canceled", Tool: name})
		return result
	}
	result.Success = true
	result.Output = output
	ctx.emit(Event{Type: "completed", Tool: name})
	return result
}

// checkPermissions validates tool and path permissions from ExecutionContext.
// Returns nil if the call is permitted, or a structured ExecutionError otherwise.
func (r *Registry) checkPermissions(ctx ExecutionContext, name string, arguments json.RawMessage) *ExecutionError {
	// Prefer unified ToolScopes checker when available.
	if ctx.ToolScopes != nil {
		// Check tool allowlist via ToolScopes.
		if !ctx.ToolScopes.HasTool(name) {
			return &ExecutionError{Code: "tool_not_allowed", Message: fmt.Sprintf("tool %q is not allowed by the current agent mode", name)}
		}

		// Extract workspace-relative paths from arguments and check each against ToolScopes.
		for _, relPath := range extractWorkspacePaths(ctx, arguments) {
			if !ctx.ToolScopes.Allowed(name, relPath) {
				return &ExecutionError{Code: "path_not_allowed", Message: fmt.Sprintf("path %q is not allowed by the current agent mode", relPath)}
			}
		}
		return nil
	}

	// Legacy fallback: check tool allowlist.
	if ctx.ToolPermissions != nil && !ctx.ToolPermissions.Allowed(name) {
		return &ExecutionError{Code: "tool_not_allowed", Message: fmt.Sprintf("tool %q is not allowed by the current agent mode", name)}
	}

	// If no path restrictions, skip path extraction.
	if ctx.PathPermissions == nil {
		return nil
	}

	// Extract workspace-relative paths from arguments and check each against PathPermissions.
	for _, relPath := range extractWorkspacePaths(ctx, arguments) {
		if !ctx.PathPermissions.Matches(relPath) {
			return &ExecutionError{Code: "path_not_allowed", Message: fmt.Sprintf("path %q is not allowed by the current agent mode", relPath)}
		}
	}
	return nil
}

// extractWorkspacePaths extracts workspace-relative paths from tool arguments.
// It handles labeled paths (e.g., "echo/src/main.go") and returns the relative portion.
func extractWorkspacePaths(ctx ExecutionContext, arguments json.RawMessage) []string {
	var args map[string]any
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil
	}

	var paths []string
	for key, value := range args {
		str, ok := value.(string)
		if !ok || str == "" {
			continue
		}
		// Only check fields that look like path arguments.
		if !isPathArgKey(key) {
			continue
		}

		// Handle labeled workspace paths by extracting the relative portion.
		matched := false
		for _, root := range ctx.workspaceRoots() {
			label, rel := splitWorkspaceLabeledPath(str)
			if label != "" && strings.EqualFold(root.Label, label) {
				paths = append(paths, filepath.ToSlash(rel))
				matched = true
				break
			}
		}
		if matched {
			continue
		}

		// No labeled root matched; treat as plain workspace-relative path.
		str = strings.TrimPrefix(str, "./")
		if str != "." && str != "" {
			paths = append(paths, filepath.ToSlash(filepath.Clean(str)))
		}
	}
	return paths
}

// isPathArgKey reports whether a JSON key name represents a filesystem path argument.
func isPathArgKey(key string) bool {
	switch key {
	case "path", "workingDirectory", "repository", "base", "revision", "target", "workflowPath", "imagePath":
		return true
	default:
		return false
	}
}

func (r *Registry) lookup(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

func cloneSchema(schema Schema) map[string]any {
	if schema == nil {
		return nil
	}
	clone := make(map[string]any, len(schema))
	for key, value := range schema {
		clone[key] = cloneValue(value)
	}
	return clone
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		clone := make(map[string]any, len(typed))
		for key, value := range typed {
			clone[key] = cloneValue(value)
		}
		return clone
	case []any:
		clone := make([]any, len(typed))
		for i, value := range typed {
			clone[i] = cloneValue(value)
		}
		return clone
	default:
		return value
	}
}
