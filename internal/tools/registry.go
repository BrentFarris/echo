package tools

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/brent/echo/internal/llm"
)

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var defaultRegistry = NewRegistry()

var planModeOnlyToolNames = map[string]bool{
	AskUserQuestionsToolName: true,
}

type Registry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]Tool)}
}

// Register self-registers a tool into the default registry. Tools call this
// from their package init function.
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

// LLMSchema returns the standard OpenAI-compatible tool schema, sorted by
// name. Plan-mode-only orchestration tools are intentionally excluded.
func LLMSchema() []llm.Tool {
	return defaultRegistry.LLMSchema()
}

// LLMSchemaForScopes returns only tools available to the selected agent mode.
func LLMSchemaForScopes(scopes *ToolScopeChecker) []llm.Tool {
	return defaultRegistry.LLMSchemaForScopes(scopes)
}

// ChatLLMSchemaForScopes optionally includes plan-mode-only orchestration
// tools. Callers must pass includePlanModeTools only for the built-in Plan
// mode; custom modes cannot opt into interactive orchestration implicitly.
func ChatLLMSchemaForScopes(scopes *ToolScopeChecker, includePlanModeTools bool) []llm.Tool {
	return defaultRegistry.chatLLMSchemaForScopes(scopes, includePlanModeTools)
}

func (r *Registry) LLMSchema() []llm.Tool {
	return r.LLMSchemaForScopes(nil)
}

func (r *Registry) LLMSchemaForScopes(scopes *ToolScopeChecker) []llm.Tool {
	return r.chatLLMSchemaForScopes(scopes, false)
}

func (r *Registry) chatLLMSchemaForScopes(scopes *ToolScopeChecker, includePlanModeTools bool) []llm.Tool {
	registered := r.Registered()
	schema := make([]llm.Tool, 0, len(registered))
	for _, tool := range registered {
		metadata := tool.Metadata()
		if planModeOnlyToolNames[metadata.Name] && !includePlanModeTools {
			continue
		}
		if scopes != nil && !scopes.HasTool(metadata.Name) {
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

// Execute runs the named tool against the default registry and returns a
// structured result. Panics are recovered and reported as errors.
func Execute(ctx ExecutionContext, name string, arguments json.RawMessage) ExecutionResult {
	return defaultRegistry.Execute(ctx, name, arguments)
}

func (r *Registry) Execute(ctx ExecutionContext, name string, arguments json.RawMessage) (result ExecutionResult) {
	result = ExecutionResult{Tool: name}
	defer func() {
		if recovered := recover(); recovered != nil {
			result.Success = false
			result.Output = nil
			result.Error = &ExecutionError{Code: "tool_panic", Message: fmt.Sprintf("tool execution failed: %v", recovered)}
		}
	}()

	if err := ctx.context().Err(); err != nil {
		result.Error = &ExecutionError{Code: "canceled", Message: "tool execution was canceled"}
		return result
	}
	if ctx.ToolScopes != nil {
		if !ctx.ToolScopes.HasTool(name) {
			result.Error = &ExecutionError{Code: "tool_not_allowed", Message: fmt.Sprintf("tool %q is not allowed by the current agent mode", name)}
			return result
		}
		for _, path := range extractWorkspacePaths(ctx, arguments) {
			if !ctx.ToolScopes.Allowed(name, path) {
				result.Error = &ExecutionError{Code: "path_not_allowed", Message: fmt.Sprintf("path %q is not allowed by the current agent mode", path)}
				return result
			}
		}
	}

	tool, ok := r.lookup(name)
	if !ok {
		result.Error = &ExecutionError{Code: "tool_not_found", Message: fmt.Sprintf("tool %q is not registered", name)}
		return result
	}

	output, err := tool.Execute(ctx, arguments)
	if err != nil {
		if ctxErr := ctx.context().Err(); ctxErr != nil {
			result.Error = &ExecutionError{Code: "canceled", Message: "tool execution was canceled"}
			return result
		}
		result.Error = safeError("tool_error", err)
		return result
	}
	if err := ctx.context().Err(); err != nil {
		result.Error = &ExecutionError{Code: "canceled", Message: "tool execution was canceled"}
		return result
	}
	result.Success = true
	result.Output = output
	return result
}

func extractWorkspacePaths(ctx ExecutionContext, arguments json.RawMessage) []string {
	var args map[string]any
	if err := DecodeToolArguments(arguments, &args); err != nil {
		return nil
	}
	paths := []string{}
	for key, value := range args {
		path, ok := value.(string)
		if !ok || path == "" || !isPathArgKey(key) {
			continue
		}
		normalized := filepath.ToSlash(filepath.Clean(path))
		for _, root := range ctx.WorkspaceRoots {
			prefix := root.Label + "/"
			if strings.EqualFold(normalized, root.Label) {
				normalized = "."
				break
			}
			if len(normalized) > len(prefix) && strings.EqualFold(normalized[:len(prefix)], prefix) {
				normalized = normalized[len(prefix):]
				break
			}
		}
		paths = append(paths, normalized)
	}
	return paths
}

func isPathArgKey(key string) bool {
	switch key {
	case "path", "workingDirectory", "repository", "base", "target", "workflowPath", "imagePath":
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
