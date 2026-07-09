---
name: per-tool-permission-ui
description: Per-tool permission UI in settings renders individual tool checkboxes with expandable path textareas, backed by CreateAgentModePerTool/UpdateAgentModePerTool service methods.
triggers:
    - per-tool permission
    - agent mode form
    - tool permissions UI
    - path per tool
    - CreateAgentModePerTool
    - UpdateAgentModePerTool
    - settings agent mode
---

## Per-Tool Permission UI

### Architecture

The agent mode form in `frontend/src/app/settings/index.ts` renders per-tool path permissions via:

1. **`availableToolNames`** — static array of all registered tool names (21 tools).
2. **`renderPerToolPermissionRows()`** — generates a checkbox + textarea pair for each tool. Unchecked tools have collapsed/disabled textareas.
3. **`state.agentModeDraftPermissions`** — `Record<string, string[]>` mapping tool name → glob paths[].

### State Fields

- `agentModeDraftPermissions: Record<string, string[]>` — the new per-tool permission map.
- Legacy `agentModeDraftToolPermissions` and `agentModeDraftPathPermissions` are kept for backward compat but deprecated.

### Go Service Methods

- `CreateAgentModePerTool(name, prompt string, permissions map[string][]string)` — creates mode with per-tool paths.
- `UpdateAgentModePerTool(id, name, prompt string, permissions map[string][]string)` — updates mode with per-tool paths.
- `buildPermissionsMapFromPerTool()` — converts the input map to internal `map[string]tools.ToolPermission`.

### TypeScript Bindings

- Manual additions to `frontend/wailsjs/go/services/SystemService.d.ts` and `.js` for `CreateAgentModePerTool` and `UpdateAgentModePerTool`.
- `frontend/src/backend/services.ts` exports wrapper functions.
- `services.AgentMode.permissions` is `Record<string, ToolPermission>` with `paths?: string[]`.

### Key Functions

- `extractPermissionsMap(mode)` — reads `mode.permissions` (new) or falls back to legacy flat fields.
- `collectPermissionsFromForm()` — reads checked checkboxes + textarea values from DOM at save time.
- `handlePerToolCheckbox()` / `handlePerToolPathsInput()` — update draft state and toggle textarea visibility.

### CSS Classes

- `.agent-mode-permissions-section` — container with background surface.
- `.per-tool-permission-row` — each tool's checkbox + paths row.
- `.per-tool-paths-container.is-collapsed` — hidden when tool is unchecked.

### Pitfalls

- `wails generate` in v2.11.0 does NOT regenerate bindings; manual updates to `.d.ts`, `.js`, and `services.ts` are required.
- `extractPermissionsMap` must handle both new `permissions` map and legacy flat fields for backward compat with persisted state.
- The `availableToolNames` array is static — it must be updated when tools are added or removed from the registry.
