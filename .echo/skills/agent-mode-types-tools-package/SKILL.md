---
name: agent-mode-types-tools-package
description: Agent mode types (AgentModeSummary, AgentModeProvider, AgentModeCreationResult) defined in internal/tools/types.go; ExecutionContext carries AgentModes field; create_agent_mode tool registration with permissions routing to CreateModePerTool.
triggers:
    - agent mode
---

## Agent Mode Types in tools Package

### Location
`internal/tools/types.go`

### Types Defined
- **`AgentModeSummary`** — lightweight mode descriptor (ID, Name, ToolPermissions, PathPermissions, BuiltIn). Excludes the full prompt text. Used for listing and resolution without transferring large prompt strings.
- **`AgentModeCreationResult`** — result of creating a mode (ID, Name, Prompt, ToolPermissions, PathPermissions). Mirrors the services package's creation result.
- **`AgentModeCreationRequest`** — request parameters for creating a mode: Name (required), Prompt, ToolPermissions, PathPermissions, Permissions (per-tool map).

### AgentModeProvider Interface
```go
type AgentModeProvider interface {
    ListModes() []AgentModeSummary
    ResolveMode(id string) *AgentModeSummary
    CreateMode(ctx context.Context, request AgentModeCreationRequest) (AgentModeCreationResult, error)
    CreateAgentModeFromChat(workspaceID string) (AgentModeCreationResult, error)
    CreateModePerTool(ctx context.Context, name string, prompt string, permissions map[string][]string) (AgentModeCreationResult, error)
}
```

### ExecutionContext Field
`ExecutionContext.AgentModes AgentModeProvider` — wired in `file_changes.go` `executeTrackedToolCall` as `AgentModes: s,` where `s *SystemService` implements the interface directly. When nil, tools should return `agent_modes_unavailable`.

### create_agent_mode Tool
- **File:** `internal/tools/agent_modes.go`
- **Registered via** `init()` → `Register(ToolFunc{...})`
- **Parameters:** name (required), prompt, toolPermissions, pathPermissions, permissions (per-tool map)
- **Execution routing:** When `request.Permissions` is non-nil, calls `ctx.AgentModes.CreateModePerTool(ctx.context(), request.Name, request.Prompt, request.Permissions)`. Otherwise falls back to `ctx.AgentModes.CreateMode(ctx.context(), request)` with flat toolPermissions/pathPermissions.
- **Schema:** The `permissions` property is an object mapping tool names (string keys) to glob-pattern arrays (`additionalProperties` of type array of strings). It is optional — omitting it uses the legacy flat permission fields.
- **In registry:** listed in `mutatingToolNames` so it appears in full `LLMSchema()` but is excluded from `ReadOnlyLLMSchema()`

### Services-Side Implementation
`internal/services/agent_mode_from_chat.go`:
- `(s *SystemService) CreateMode(...)` — delegates to `CreateAgentMode(name, prompt, toolPermissions, pathPermissions)`, converts result to `tools.AgentModeCreationResult`
- `(s *SystemService) CreateModePerTool(...)` — delegates to create mode with per-tool permissions map
- `(s *SystemService) ListModes()` / `ResolveMode(id)` — thin wrappers around `ListAgentModesProvider()` / `ResolveModeProvider()`
- `(s *SystemService) CreateAgentModeFromChatProvider(...)` — delegates to `CreateAgentModeFromChat(workspaceID)`

### Relationship to services Package
`internal/services/agent_modes.go` defines the full `AgentMode` struct (with prompt) and CRUD operations (`CreateAgentMode`, `UpdateAgentMode`, `DeleteAgentMode`). The tools package types are lighter-weight summaries for execution-time access. Services implements `AgentModeProvider` directly to bridge the two layers.

### Pitfalls
- `AgentModeSummary` intentionally omits `Prompt` — tools don't need the full system prompt text during listing/resolution.
- Interface methods return slices and pointers; nil `AgentModes` on `ExecutionContext` means no provider is set (backward compatible).
- `CreateMode` uses case-insensitive name matching (`strings.EqualFold`) when finding the created mode in the result list.
- Tool requires `name`; empty name returns `invalid_arguments` error before calling the provider.
- **Routing invariant:** `permissions` and flat `toolPermissions`/`pathPermissions` are mutually exclusive from the caller's perspective — if `permissions` is provided, the flat fields are ignored by the routing logic.
