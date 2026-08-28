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

var researchAgentToolNames = map[string]bool{
	ResearchAgentsSpawnToolName:  true,
	ResearchAgentSendToolName:    true,
	ResearchAgentsWaitToolName:   true,
	ResearchAgentsCancelToolName: true,
}

var researchWorkerToolNames = map[string]bool{
	"filesystem_list":             true,
	"filesystem_read_image":       true,
	"filesystem_read_text":        true,
	"filesystem_read_video":       true,
	"filesystem_search_text":      true,
	"filesystem_search_workspace": true,
	"filesystem_stat":             true,
	"git_inspect":                 true,
	"web_fetch":                   true,
	"web_search":                  true,
	"workspace_skill_read":        true,
	"workspace_skill_search":      true,
}

type ChatSchemaOptions struct {
	PlanMode        bool
	ResearchEnabled bool
	SandboxGUI      bool
	WorkspaceID     string
}

type Registry struct {
	mu           sync.RWMutex
	tools        map[string]toolRegistration
	nextID       uint64
	pluginPolicy func(pluginID, toolName, workspaceID string) bool
}

type toolRegistration struct {
	id    uint64
	owner string
	tool  Tool
}

const CoreToolOwner = "core"

func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]toolRegistration)}
}

// CloneDefaultRegistry creates a server-owned registry containing immutable
// snapshots of every statically registered core tool.
func CloneDefaultRegistry() *Registry { return defaultRegistry.Clone() }

func (r *Registry) Clone() *Registry {
	clone := NewRegistry()
	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, registration := range r.tools {
		clone.nextID++
		registration.id = clone.nextID
		clone.tools[name] = registration
	}
	clone.pluginPolicy = r.pluginPolicy
	return clone
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
	_, err := r.registerOwned(CoreToolOwner, tool)
	return err
}

// RegisterOwned installs a dynamically disposable registration. The disposer
// can remove only the exact registration it owns, so stale plugin lifecycle
// callbacks cannot remove a replacement registration.
func (r *Registry) RegisterOwned(owner string, tool Tool) (func(), error) {
	owner = strings.TrimSpace(owner)
	if owner == "" || owner == CoreToolOwner {
		return nil, fmt.Errorf("plugin tool owner is required")
	}
	return r.registerOwned(owner, tool)
}

func (r *Registry) registerOwned(owner string, tool Tool) (func(), error) {
	if tool == nil {
		return nil, fmt.Errorf("tool is required")
	}
	metadata := tool.Metadata()
	if metadata.Name == "" {
		return nil, fmt.Errorf("tool name is required")
	}
	if !toolNamePattern.MatchString(metadata.Name) {
		return nil, fmt.Errorf("tool name %q must match %s", metadata.Name, toolNamePattern.String())
	}

	r.mu.Lock()
	if _, exists := r.tools[metadata.Name]; exists {
		r.mu.Unlock()
		return nil, fmt.Errorf("duplicate tool name: %s", metadata.Name)
	}
	r.nextID++
	registration := toolRegistration{id: r.nextID, owner: owner, tool: tool}
	r.tools[metadata.Name] = registration
	r.mu.Unlock()

	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			current, exists := r.tools[metadata.Name]
			if exists && current.id == registration.id && current.owner == owner {
				delete(r.tools, metadata.Name)
			}
		})
	}, nil
}

// SetPluginPolicy supplies the execution-time activation and digest approval
// check for non-core tools. It is consulted both when schemas are assembled
// and immediately before a tool executes.
func (r *Registry) SetPluginPolicy(policy func(pluginID, toolName, workspaceID string) bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pluginPolicy = policy
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
		registered = append(registered, r.tools[name].tool)
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

// ChatLLMSchemaForScopes includes chat-only orchestration tools according to
// the active turn options while still enforcing the selected mode's scopes.
func ChatLLMSchemaForScopes(scopes *ToolScopeChecker, options ChatSchemaOptions) []llm.Tool {
	return defaultRegistry.chatLLMSchemaForScopes(scopes, options)
}

// ResearchLLMSchemaForScopes returns the non-mutating tools available to a
// child research agent. Research orchestration itself is always excluded.
func ResearchLLMSchemaForScopes(scopes *ToolScopeChecker) []llm.Tool {
	return defaultRegistry.researchLLMSchemaForScopes(scopes)
}

func IsResearchAgentToolName(name string) bool {
	return researchAgentToolNames[name]
}

func IsResearchWorkerToolName(name string) bool {
	return researchWorkerToolNames[name]
}

func (r *Registry) LLMSchema() []llm.Tool {
	return r.LLMSchemaForScopes(nil)
}

func (r *Registry) LLMSchemaForScopes(scopes *ToolScopeChecker) []llm.Tool {
	return r.chatLLMSchemaForScopes(scopes, ChatSchemaOptions{})
}

func (r *Registry) ChatLLMSchemaForScopes(scopes *ToolScopeChecker, options ChatSchemaOptions) []llm.Tool {
	return r.chatLLMSchemaForScopes(scopes, options)
}

func (r *Registry) ResearchLLMSchemaForScopes(scopes *ToolScopeChecker) []llm.Tool {
	return r.researchLLMSchemaForScopes(scopes)
}

func (r *Registry) chatLLMSchemaForScopes(scopes *ToolScopeChecker, options ChatSchemaOptions) []llm.Tool {
	registered, policy := r.registrations()
	schema := make([]llm.Tool, 0, len(registered))
	for _, registration := range registered {
		metadata := registration.tool.Metadata()
		if registration.owner != CoreToolOwner && (options.PlanMode || policy == nil || !policy(registration.owner, metadata.Name, options.WorkspaceID)) {
			continue
		}
		if planModeOnlyToolNames[metadata.Name] && !options.PlanMode {
			continue
		}
		if researchAgentToolNames[metadata.Name] && !options.ResearchEnabled {
			continue
		}
		if sandboxGUIToolNames[metadata.Name] && (options.PlanMode || !options.SandboxGUI) {
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

func (r *Registry) researchLLMSchemaForScopes(scopes *ToolScopeChecker) []llm.Tool {
	registered, _ := r.registrations()
	schema := make([]llm.Tool, 0, len(researchWorkerToolNames))
	for _, registration := range registered {
		if registration.owner != CoreToolOwner {
			continue
		}
		metadata := registration.tool.Metadata()
		if !researchWorkerToolNames[metadata.Name] || (scopes != nil && !scopes.HasTool(metadata.Name)) {
			continue
		}
		schema = append(schema, llm.Tool{
			Type: "function",
			Function: llm.ToolFunction{
				Name: metadata.Name, Description: metadata.Description, Parameters: cloneSchema(metadata.Parameters),
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

	registration, policy, ok := r.lookup(name)
	if !ok {
		result.Error = &ExecutionError{Code: "tool_not_found", Message: fmt.Sprintf("tool %q is not registered", name)}
		return result
	}
	if registration.owner != CoreToolOwner && (policy == nil || !policy(registration.owner, name, ctx.WorkspaceID)) {
		result.Error = &ExecutionError{Code: "tool_not_active", Message: fmt.Sprintf("plugin tool %q is not active or approved for this workspace", name)}
		return result
	}

	output, err := registration.tool.Execute(ctx, arguments)
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

func (r *Registry) lookup(name string) (toolRegistration, func(string, string, string) bool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, r.pluginPolicy, ok
}

func (r *Registry) registrations() ([]toolRegistration, func(string, string, string) bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	sort.Strings(names)
	registered := make([]toolRegistration, 0, len(names))
	for _, name := range names {
		registered = append(registered, r.tools[name])
	}
	return registered, r.pluginPolicy
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
