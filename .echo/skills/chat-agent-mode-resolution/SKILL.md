---
name: chat-agent-mode-resolution
description: Chat flow resolves agent modes by ID, injects unified ToolScopeChecker into execution context, and composes system prompts with per-tool permission summaries. CreateAgentModeFromChat synthesizes modes from transcript tool usage via LLM using the new permissions map format.
triggers:
    - agent mode
    - mode resolution
    - chat mode ID
    - permission injection
    - system prompt composition
    - CreateAgentModeFromChat
    - tool permission checker
    - ToolScopeChecker
    - buildToolScopes
    - formatAgentModePermissionSummary
    - per-tool permissions
    - agentModeID
---

## Chat Agent Mode Resolution

### Overview
Chat turns resolve agent modes by ID string (`agentModeID`). The resolved mode determines permissions, system prompt composition, and tool schema selection. A unified `ToolScopeChecker` replaces the legacy separate `ToolPermissionChecker` and `PathMatcher`.

### Key Files
- `internal/services/chat.go`: `runChatTurnWithHistory` resolves mode, builds `ToolScopeChecker`, composes system prompt with per-tool permission summary.
- `internal/services/agent_modes.go`: `resolveAgentMode(id)`, `buildToolScopes(permissions)`, `createAgentModeFromGenerated`.
- `internal/services/chat_images.go`: `ChatMessageRequest` carries `PlanMode bool` (backward compat) and `AgentModeID string`.
- `internal/services/agent_mode_from_chat.go`: LLM-based mode synthesis from transcript tool usage.

### Mode Resolution Flow
1. `sendChatMessage` normalizes `AgentModeID`: empty + `PlanMode=true` → `"plan"`, else empty → `"general"`
2. `runChatTurnWithHistory` calls `s.resolveAgentMode(agentModeID)` → `(AgentMode, string)`
3. `isPlanMode := resolvedModeID == AgentModeIDPlan` determines tool schema (full vs read-only)
4. `buildToolScopes(mode.Permissions)` converts Permissions map → `*tools.ToolScopeChecker`
5. Single `toolScopes` passes through to `executeToolCall(..., isPlanMode, toolScopes)`
6. `chatSystemMessage(workspace, mode, candidates)` composes system prompt with per-tool permission summary

### System Prompt Composition
`chatSystemMessage` calls `formatAgentModePermissionSummary(mode)`:
- Uses sorted tool names from Permissions map (deterministic output)
- Shows per-tool path restrictions when any tool has constraints
- Lists tools without path constraints as "all paths"
- Falls back to legacy flat format behavior when Permissions map is empty

### CreateAgentModeFromChat
`internal/services/agent_mode_from_chat.go`:
- System prompt (`agentModeFromChatSystemPrompt`) describes the new `permissions` map JSON format with per-tool path arrays
- `generatedAgentMode` struct has `Permissions map[string]tools.ToolPermission` plus backward-compatible flat fields
- `parseGeneratedAgentMode` handles both new and old formats (new takes priority)
- `createAgentModeFromGenerated` routes to `CreateAgentModePerTool` for new format or `CreateAgentMode` for flat fallback

### Frontend Mapping
- `chatAgentModeIDFor(workspaceID)` returns `"plan"` or `"general"` based on composer mode

### Backward Compatibility
- `ChatMessageRequest.PlanMode` retained; maps to `AgentModeIDPlan` when true
- Empty mode ID defaults to `AgentModeIDGeneral` (full permissions)
- LLM can still output old flat `toolPermissions`/`pathPermissions`; parsing falls back gracefully
