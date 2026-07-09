---
name: settings-agent-modes-section
description: 'Agent Modes CRUD section in Settings: create, edit, delete custom modes with inline form for name, prompt, tool permissions, and path permissions.'
triggers:
    - agent mode settings
    - mode management UI
    - create agent mode
    - edit agent mode
    - delete agent mode
    - settings panel
    - permission form
    - system prompt editor
---

## Settings panel structure

Settings overlay (`frontend/src/app/settings/index.ts`) renders as a full `<form>` with:
- Left nav sidebar with section buttons (`data-settings-nav-target`)
- Right content area with `<section class="settings-section">` per topic
- Footer with Reset and Save buttons

Sections defined in `settingsSections` array. Each section has an `aria-labelledby` matching its `<h3 id>`.

## Agent Modes section

Agent Modes section provides CRUD for custom agent modes via Settings:

### State fields (state.ts)
- `agentModeEditingId`: mode ID currently being edited
- `agentModeCreating`: boolean, true when creating new mode
- `agentModeDraftName`, `agentModeDraftPrompt`: string drafts
- `agentModeDraftToolPermissions`, `agentModeDraftPathPermissions`: string[] drafts

### Exported functions (settings/index.ts)
- `startCreateAgentMode()` — enter create form state
- `startEditAgentMode(modeID)` — populate draft from existing mode
- `cancelAgentMode()` — reset all agent mode draft/edit state
- `saveNewAgentMode()` — calls `CreateAgentMode()`, updates `state.agentModes`
- `saveAgentMode(modeID)` — calls `UpdateAgentMode()`, updates `state.agentModes`
- `deleteAgentModeSettings(modeID)` — confirmation dialog, calls `DeleteAgentMode()`

### Actions (actions.ts)
- `create-agent-mode` → `startCreateAgentMode()`
- `edit-agent-mode` → `startEditAgentMode(modeID from data-agent-mode-id)`
- `cancel-agent-mode` → `cancelAgentMode()`
- `save-new-agent-mode` → `saveNewAgentMode()`
- `save-agent-mode` → `saveAgentMode(modeID)`
- `delete-agent-mode-settings` → `deleteAgentModeSettings(modeID)`

### Form input handling
- Uses `data-agent-mode-field` + `data-agent-mode-field-name` attributes on inputs/textareas
- `handleAgentModeFieldInput()` maps field names to state draft fields
- Textarea values parsed via `parsePermissionLines()` (newline-separated)
- Added textarea binding in `bindSettingsEvents()`

### CSS classes
- `.agent-mode-list`, `.agent-mode-row`, `.agent-mode-main` — list/row layout mirroring LLM endpoint pattern
- `.agent-mode-form` — inline form with grid layout for textareas
- `.agent-mode-form-actions` — Save/Cancel button row
- `.agent-mode-actions` — edit/delete icon buttons

### Backend sync
- `CreateAgentMode`, `UpdateAgentMode`, `DeleteAgentMode` in `frontend/src/backend/services.ts`
- Results update `state.agentModes` (same array used by chat mode selector)
- Built-in modes display without edit/delete buttons

### Settings close/reset
- `close-settings` and `reset-settings` actions call `cancelAgentMode()` to clear editing state
