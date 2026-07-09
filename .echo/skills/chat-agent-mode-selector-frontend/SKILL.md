---
name: chat-agent-mode-selector-frontend
description: 'Frontend agent mode selector: dynamic dropdown from backend modes with permission badges, delete actions, AI-driven mode creation from chat transcripts, and event-driven synchronization via echo:agent-mode:event.'
triggers:
    - agent mode selector
    - mode dropdown
    - permission badges
    - delete agent mode
    - create mode from chat
    - CreateAgentModeFromChat
    - ListAgentModes
    - mode selection state
    - echo:agent-mode:event
    - agent mode event
---

## Agent Mode Frontend Architecture

### State Layer (`frontend/src/app/state.ts`)
- `state.agentModes: services.AgentMode[]` — cached list from `ListAgentModes()` RPC
- `state.selectedAgentModeIds: Map<string, string>` — per-workspace selected agent mode ID
- `state.creatingAgentModes: Set<string>` — workspaces with in-progress AI mode synthesis

### Key Functions
- `chatAgentModeIDFor(workspaceID)` — returns selected mode ID; falls back to "plan"/"general" built-in IDs if no selection exists
- `chatAgentModeNameFor(workspaceID)` — resolves mode name from `state.agentModes` for display
- `setChatAgentMode(workspaceID, modeID)` — sets/clears per-workspace mode selection

### Backend Service Wrappers (`frontend/src/backend/services.ts`)
- `ListAgentModes()` → `Promise<services.AgentMode[]>`
- `CreateAgentMode(name, prompt, toolPermissions, pathPermissions)` → `Promise<services.AgentMode[]>`
- `DeleteAgentMode(id)` → `Promise<services.AgentMode[]>`
- `CreateAgentModeFromChat(workspaceID)` → `Promise<services.AgentModeCreationResult>`

### Mode Selector Rendering (`frontend/src/app/chat/index.ts`)
- `renderModeOptions(workspaceID)` — renders dynamic `<li>` items from `state.agentModes`:
  - Each mode has a `[data-mode-id]` attribute for selection
  - Non-built-in modes have delete buttons with `[data-mode-delete-id]`
  - Permission badges show tool/path permission counts
  - "+ Create Mode" option uses `[data-mode-create]` attribute
- `bindModeDropdownEvents(root)` — delegates clicks to:
  - `[data-mode-id]` → `selectAgentMode()` → sets selection, patches panel
  - `[data-mode-delete-id]` → `deleteAgentMode()` → RPC delete with confirmation
  - `[data-mode-create]` → `createAgentModeFromChat()` → AI synthesis via RPC

### Bootstrap Loading & Event Synchronization (`frontend/src/app/bootstrap.ts`)
- Agent modes load via `ListAgentModes()` during `initialize()` after `LoadState()`
- Failure is non-fatal — modes will be empty until first chat render triggers reload
- **Event subscription**: In `startApp()`, `EventsOn("echo:agent-mode:event", ...)` listens for backend-emitted events and updates `state.agentModes` then calls `render()`. This keeps the dropdown in sync when modes are created, updated, or deleted from any source (settings panel, agent tool, chat synthesis).

### Backend Event Emission (`internal/services/agent_modes.go`)
- Constant `agentModeEventName = "echo:agent-mode:event"`
- `emitAgentModeEvent(event any)` broadcasts via both `emitRuntimeEvent` and `runtime.EventsEmit`
- Called after every mode mutation: create, update, delete (both flat and per-tool variants)
- Payload is the full updated `[]AgentMode` list from `cloneAgentModes(s.state.AgentModes)`

### CSS Classes (`.mode-option`, `.mode-badge`, `.mode-delete-btn`, `.mode-create-option`)
- Mode badges use accent color for tools, muted color for paths
- Delete button fades in on hover, turns red on hover
- Create option is italic with accent color

### Critical Invariants
- Built-in modes (general, plan) cannot be deleted — `builtIn: true` flag
- Agent mode ID sent to backend via `agentModeId` field in chat message requests
- Mode selection persists per-workspace in `selectedAgentModeIds`; cleared when workspace changes
- Event subscription must guard the payload with `Array.isArray(modes)` since Wails event payloads are untyped
