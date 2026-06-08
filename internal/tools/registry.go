package tools

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"sync"

	"github.com/brent/echo/internal/llm"
)

var toolNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var defaultRegistry = NewRegistry()

var readOnlyToolNames = map[string]bool{
	"filesystem_list":        true,
	"filesystem_read_image":  true,
	"filesystem_read_text":   true,
	"filesystem_search_text": true,
	"filesystem_stat":        true,
	"web_search":             true,
}

var mutatingToolNames = map[string]bool{
	"filesystem_create_text": true,
	"filesystem_delete_file": true,
	"filesystem_edit_text":   true,
	"shell_command":          true,
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
	return defaultRegistry.LLMSchema()
}

func ReadOnlyLLMSchema() []llm.Tool {
	return defaultRegistry.ReadOnlyLLMSchema()
}

func IsReadOnlyToolName(name string) bool {
	return readOnlyToolNames[name]
}

func IsMutatingToolName(name string) bool {
	return mutatingToolNames[name]
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

func Execute(ctx ExecutionContext, name string, arguments json.RawMessage) ExecutionResult {
	return defaultRegistry.Execute(ctx, name, arguments)
}

func (r *Registry) Execute(ctx ExecutionContext, name string, arguments json.RawMessage) (result ExecutionResult) {
	result = ExecutionResult{Tool: name}
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

	defer func() {
		if recovered := recover(); recovered != nil {
			result.Success = false
			result.Output = nil
			result.Error = &ExecutionError{Code: "tool_panic", Message: fmt.Sprintf("tool execution failed: %v", recovered)}
		}
	}()

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
