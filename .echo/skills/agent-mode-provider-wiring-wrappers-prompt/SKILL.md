---
name: agent-mode-provider-wiring-wrappers-prompt
description: SystemService implements tools.AgentModeProvider; wrapper methods convert service types to tool types; create_agent_mode guidance in workspace skill prompt.
triggers:
    - AgentModeProvider
---

## Agent Mode Provider Wiring

### Where ExecutionContext.AgentModes is set
- `internal/services/file_changes.go` in `executeTrackedToolCall`: `AgentModes: s` wires SystemService as the provider.

### Interface (tools/types.go)
```go
type AgentModeProvider interface {
    ListModes() []AgentModeSummary
    ResolveMode(id string) *AgentModeSummary
    CreateMode(ctx context.Context, request AgentModeCreationRequest) (AgentModeCreationResult, error)
    CreateAgentModeFromChat(workspaceID string) (AgentModeCreationResult, error)
}
```

### Wrapper methods on SystemService (internal/services/agent_mode_from_chat.go)
| Interface method | Service implementation | Notes |
|---|---|---|
| `ListModes()` | `ListAgentModesProvider()` → delegates to `ListAgentModes()`, converts to `[]tools.AgentModeSummary` | |
| `ResolveMode(id)` | `ResolveModeProvider(id)` → iterates `ListAgentModes()`, returns matching `*tools.AgentModeSummary` | |
| `CreateMode(ctx, req)` | Calls `CreateAgentMode(...)`, finds created mode in result list | Checks context cancellation |
| `CreateAgentModeFromChat(workspaceID)` | Direct delegation — already returns `tools.AgentModeCreationResult` | Analyzes chat transcript via LLM |

### Prompt guidance (internal/services/workspace_skill_prompt.go)
`workspaceSkillsPrompt` appends: *"If repeated tool usage in this chat suggests a reusable workflow, call create_agent_mode to synthesize a new agent mode from the transcript. Pass the workspace ID that owns the chat; the tool analyzes completed tool calls and creates a named mode with matching permissions. Use list_agent_modes to see available modes before creating a duplicate."*

### Type mapping
- `services.AgentMode` → `tools.AgentModeSummary`: 1:1 field copy (ID, Name, ToolPermissions, PathPermissions, BuiltIn)
- `AgentModeCreationResult` lives in `tools/types.go`; the services package removed its local duplicate and returns `tools.AgentModeCreationResult` directly.

### Pitfalls
- SystemService must implement ALL four AgentModeProvider methods; missing any one causes a build failure at the wiring site.
- `CreateAgentModeFromChat` needs the workspace ID to look up LLM settings — it cannot be called without a workspace context.
- The prompt guidance is appended unconditionally in `workspaceSkillsPrompt`; plan-mode filtering of mutating tools happens at the tool schema level, not in the prompt text.
