---
name: frontend-per-workspace-agent-modes
description: Frontend agent mode state is per-workspace Map; settings UI and chat dropdowns scope to active workspace; modes reload on workspace switch.
triggers:
    - agent mode state
    - per-workspace modes
    - frontend agent modes
    - workspace mode map
    - mode selector refresh
    - settings agent modes
---

## Frontend per-workspace agent mode state

Agent modes are stored in `state.agentModes` as `Map<string, services.AgentMode[]>` keyed by workspace ID. This replaced the flat `services.AgentMode[]` array.

### Key files
- `frontend/src/app/state.ts` — `agentModes: new Map()`, `agentModesForWorkspace(wsID)` helper
- `frontend/src/app/bootstrap.ts` — initial load calls `ListAgentModes(activeWS)`, event handler stores into Map
- `frontend/src/app/chat/index.ts` — `renderModeOptions()` uses `agentModesForWorkspace()`, CRUD ops use `state.agentModes.set(ws.id, modes)`
- `frontend/src/app/settings/index.ts` — `renderAgentModesSection()` scopes to active workspace, CRUD ops use `state.agentModes.set(ws.id, modes)`
- `frontend/src/app/actions.ts` — `activate-workspace` reloads modes via `ListAgentModes(workspaceID)`, `delete-workspace` calls `state.agentModes.delete(workspaceID)`

### Invariants
- Always access modes via `agentModesForWorkspace(workspaceID)` never `state.agentModes` directly except for `.set()`/`.delete()`.
- Backend `ListAgentModes(workspaceID)` scopes to the workspace; pass `""` only when no workspace is active.
- The `echo:agent-mode:event` payload contains modes for the active workspace; store with `state.agentModes.set(activeWS, modes)`.

### Pitfalls
- Do not iterate `state.agentModes` as an array — it is a Map.
- Settings UI must guard against missing active workspace before rendering modes.
- On workspace deletion, remove the entry from the Map to avoid stale data.
