---
name: settings-architecture
description: 'Echo Settings architecture across frontend rendering and drafts, Wails/web service wrappers, Go models and validation, persistence, and independently saved workspace/runtime sections. Use when adding, changing, debugging, or reviewing a Settings field or section.'
triggers:
    - settings architecture
    - settings panel
    - add setting
    - settings persistence
    - settings draft
    - SaveSettings
    - settings validation
    - settings event handling
    - settings backend
    - settings frontend
    - state.json
    - web access settings
---

# Echo Settings architecture

Treat the Settings overlay as an aggregator over several storage domains. Do not assume every control belongs to `llm.Settings` or waits for the footer Save button.

## Follow the main lifecycle

1. `internal/services/system.go` constructs `SystemService` with `defaultAppState()` and loads the user config file from `<UserConfigDir>/Echo/state.json`.
2. `internal/services/system.go:load()` overlays persisted JSON onto defaults, performs migrations, normalizes LLM endpoint profiles and web access, then stores the result in `s.state`.
3. `frontend/src/app/bootstrap.ts:initialize()` calls `LoadState()`, assigns the returned `services.AppState` to `state.appState`, and deep-clones `settings` and `webAccess` into editable drafts.
4. The `open-settings` action in `frontend/src/app/actions.ts` creates fresh drafts, loads live web-access and development-log status, hydrates workspace draft maps, applies the draft theme, and requests a render.
5. `frontend/src/app/render.ts:buildOverlays()` includes `renderSettingsOverlay()` while `state.settingsOpen` is true.
6. `frontend/src/app/events.ts` calls `bindSettingsEvents()` after rendering. Settings-specific form events are bound there; `data-action` buttons continue through the app-wide delegated action handler in `actions.ts`.
7. Generic form controls update `state.settingsDraft` in `handleSettingsInput()`. Specialized controls return early and use their own state and backend calls.
8. `handleSettingsSubmit()` saves the domains that participate in the footer Save flow, refreshes drafts from returned backend state, applies the committed theme, closes the overlay, and rerenders.

The app shell is rewritten by `render()`. Keep editable or asynchronous state in `frontend/src/app/state.ts` or a domain module rather than relying on transient DOM values.

## Know which component owns each setting

### Core application and LLM settings

- Model: `internal/llm/settings.go` (`Settings`, `LLMEndpoint`, `EndpointSelection`, and `Theme`).
- Frontend draft: `state.settingsDraft`.
- UI: most of `frontend/src/app/settings/index.ts`.
- Save API: `SystemService.SaveSettings`.
- Persistence: `storedAppState.Settings` in `state.json`.
- Save timing: text, number, select, endpoint, and theme edits normally wait for footer Save. Generic checkboxes call `saveSettingsImmediately()` as soon as they toggle.

`SaveSettings` locks `SystemService.mu`, normalizes the input, validates it, updates `s.state.Settings`, calls `saveLocked()`, and returns a cloned `AppState`.

### Web access

- Model and validation: `internal/services/web_access.go`.
- Frontend draft/status: `state.webAccessDraft` and `state.webAccessStatus`.
- APIs: `SaveWebAccessSettings`, `LoadWebAccessStatus`, and `RotateWebAccessToken`.
- Persistence: `storedAppState.WebAccess` in `state.json`.
- Runtime side effect: `SaveWebAccessSettings` asks the installed `WebAccessController` to apply the server configuration before committing it to state.
- Save timing: `enabled` and `enableTLS` save immediately; host, port, and token wait for footer Save.

Keep persisted configuration separate from `WebAccessStatus`, which reports whether the server is actually running and exposes its current URLs or error.

### Workspace settings

- Model: `Workspace` and `WorkspaceFolder` in `internal/services/system.go`.
- Persistence: the `Workspaces` slice in `state.json`.
- Immediate APIs: `SetWorkspaceFolderUseAgents`, `SetWorkspaceDefaultPlanMode`, `SetWorkspaceSearchParentGitRepositories`, icon actions, and workspace deletion.
- Footer Save APIs: `SetWorkspaceLetter` and `SetWorkspaceBuildCommand`, sourced from `state.workspaceLetterDrafts` and `state.workspaceBuildCommandDrafts`.

Every workspace mutation validates IDs, updates under `SystemService.mu`, calls `saveLocked()`, and returns a cloned `AppState`.

### Workspace debug settings

- UI renderer and submit adapter: `frontend/src/app/settings/index.ts`.
- Frontend cache: `frontend/src/codeView/debug.ts`.
- Backend: `internal/services/debug_settings.go`.
- Storage: the `debug` section of the first workspace folder's `.echo/workspace.json`.
- Save timing: footer Save, before core `SaveSettings`.

Preserve `ExpectedRevision` conflict checking and replace only the debug section. Do not move debug JSON into global `state.json`.

### Token budget and liveness

- UI/state modules: `frontend/src/app/budget.ts` and `frontend/src/app/liveness.ts`.
- Backend: `internal/services/budget.go` and `internal/services/liveness.go`.
- Scope: active workspace.
- Persistence: token budgets and liveness configs are serialized into `state.json`.
- Save timing: their inputs call their backend APIs immediately; footer Reset and Save do not roll these changes back.

Keep Go `time.Duration` values in nanoseconds across RPC. The liveness UI converts them to and from minutes only for display and editing.

### Agent modes

- UI and drafts: the Agent Modes portion of `frontend/src/app/settings/index.ts` plus `state.agentMode*` fields.
- Backend: `internal/services/agent_modes.go` and `internal/services/workspace_agent_modes.go`.
- Storage: custom modes are workspace-scoped files under `.echo/modes-<workspace-id>/<mode-id>/mode.json`; built-in modes are not editable.
- Save timing: create, edit, and delete actions save immediately and refresh the workspace mode list.

Read `.echo/skills/settings-agent-modes-section/SKILL.md` for the detailed CRUD and per-tool permission flow.

### Development logging and browser permissions

- AI flow logging is runtime-only and intentionally not remembered after restart. `SetDevelopmentLoggingEnabled` controls it immediately.
- Push notification permission is owned by the browser `Notification` API, not `state.json`.
- Rebuild/relaunch is an action surfaced in Settings, not a persisted setting.

## Work with the frontend form

Define the navigation entry in `settingsSections` and render a matching section whose `aria-labelledby` points to the same heading ID.

Use these event patterns:

- Add `name="<jsonField>"` for a simple `llm.Settings` field. `handleSettingsInput()` copies the value into a new `llm.Settings` instance.
- Add numeric field names to `numericFields`; otherwise the draft receives a string.
- Use `data-settings-inverted-boolean` when the persisted field is negative but the label is positive, such as `disableNotificationSounds`.
- Add a distinct `data-*` marker and an early branch in `handleSettingsInput()` for settings with custom state, conversion, validation, save timing, or rerender behavior.
- Use `data-action` for buttons and implement the action in `frontend/src/app/actions.ts`.
- Escape text and attribute values with the utilities in `frontend/src/app/utils.ts`.

Remember that every generic checkbox immediately calls `saveSettingsImmediately()`. Route a checkbox through a specialized branch before the generic path if that behavior is wrong for the new setting.

Use `state.formError` for blocking form errors and `pushToast()` for operation feedback. Rerender after state changes that alter the visible section structure.

## Preserve LLM endpoint behavior

Treat `Settings.Endpoints` and `Settings.EndpointSelection` as the modern source of truth. The top-level endpoint, model, generation, prompt-appendage, and header fields remain legacy mirrors of the selected Chat endpoint.

- Synchronize mirrors with `settingsWithEndpointSync()` before saving from the frontend.
- Keep endpoint names unique and require every routed topic to reference a saved endpoint.
- Preserve per-endpoint headers and generation values when normalizing.
- Keep `endpointProfilesChanged()` in `SaveSettings`: profile changes use `NormalizedEndpointProfiles()`, while legacy top-level callers use `Normalized()`.
- Resolve runtime settings with `Settings.ForInteraction()` rather than reading the Chat endpoint for every interaction.

Test endpoint changes in both `internal/llm/settings_test.go` and `internal/services/settings_test.go`; regressions can silently make one endpoint overwrite another.

## Extend backend persistence safely

For a new field inside `llm.Settings`:

1. Add the Go field and JSON tag in `internal/llm/settings.go`.
2. Define its default, normalization, migration, and validation behavior. Detect key presence during load when zero or false has a different meaning from a missing legacy field.
3. Update clone logic when the field contains a map or slice.
4. Add the frontend control and draft conversion.
5. Regenerate Wails bindings; do not hand-edit `frontend/wailsjs`.

For a new settings domain or `AppState` field:

1. Add the field to `AppState` in `internal/services/system.go`.
2. Add it to `storedAppState`, `storedAppStateFrom()`, and `storedAppState.appState()` in `internal/services/state_persistence.go`.
3. Initialize defaults and deep-clone maps or slices in `cloneState()`.
4. Add a locked, validating `SystemService` method that persists with `saveLocked()`.
5. Add the wrapper in `frontend/src/backend/services.ts`.
6. Regenerate Wails bindings when exported Go methods or types change.
7. Add the method to `allowedRPCMethods` in `internal/webserver/server.go` so browser mode matches desktop mode.

Service wrappers must continue to use the standard `call()` path, which selects generated Wails bindings in the desktop runtime and `webRpc()` in LAN browser mode.

## Understand save and reset semantics

- `state.appState` is the last backend snapshot; drafts are editable copies.
- Close and Reset discard unsaved core, web-access, theme, workspace-letter, and build-command drafts. Close also restores the committed theme preview.
- Immediate settings have already changed backend state and are not undone by Reset.
- Footer Save is sequential, not transactional: debug settings, core settings, web access, workspace labels, and build commands can persist before a later step fails.
- Theme colors preview immediately through `applyTheme()`, but are compacted with `settingsWithCompactTheme()` before persistence.
- `saveLocked()` writes indented JSON with owner-only file permissions and skips the write when bytes are unchanged.
- Backend methods return cloned state. Preserve deep-copy behavior so callers cannot mutate service-owned maps or slices.

## Keep layout and specialized guidance aligned

Style the overlay in `frontend/src/styles.css` using the existing `.settings-*`, `.field`, and `.settings-toggle` patterns.

Read `.echo/skills/settings-responsive-breakpoints/SKILL.md` before changing the settings grid, sidebar, or mobile layout. Keep desktop, tablet, and mobile behavior aligned with its documented breakpoints.

## Verify changes

Run focused checks first, then broaden when the change crosses layers:

```powershell
go test ./internal/llm ./internal/services
cd frontend
npm run build
cd ..
go test ./...
```

Run `wails generate` before the frontend build whenever a Wails-bound method or exported Go model changes. Add domain-specific tests for web access, debug settings, budgets, liveness, agent modes, or workspace mutations when those owners change.
