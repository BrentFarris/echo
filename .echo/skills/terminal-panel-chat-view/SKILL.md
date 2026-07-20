---
name: terminal-panel-chat-view
description: 'Terminal panel in chat view: types, state, backend wrappers, event handling, rendering with proper HTML escaping, actions, and CSS for live streaming shell commands plus Saved Commands overlay dialog UI. Covers the critical invariant that patch rendering (renderTerminalRunHtml) and full rendering (renderTerminalRun) must use identical escapeHtml/escapeAttribute escaping.'
triggers:
    - terminal panel
    - shell command
    - live streaming output
    - echo:shell:event
    - saved commands
    - overlay dialog
    - escapeHtml
    - renderTerminalRunHtml
    - tap-to-run
---

# Terminal Panel in Chat View

## Overview
The terminal panel is a collapsible `<details>` section rendered below the chat panel in chat mode. It provides command history with live-scrolling, color-coded stdout/stderr lines, exit code badges, duration display, spinner for running commands, stop button, a **Saved Commands** collapsible subsection, and an input row at the bottom.

## Files Modified

### `frontend/src/app/types.ts`
- Added `ShellCommandRunStatus`: `"running" | "completed" | "timed-out"`
- Added `ShellCommandLine`: `{ type: "stdout" | "stderr"; text: string }`
- Added `ShellCommandRun`: tracks id, command, lines[], status, exitCode?, durationMs?, startedAt

### `frontend/src/app/state.ts`
- Added per-workspace terminal state:
  - `terminalRuns: Map<string, ShellCommandRun[]>` — command history per workspace
  - `terminalDrafts: Map<string, string>` — input draft per workspace
  - `terminalOpen: Set<string>` — which workspaces have the panel expanded
- Added saved commands state:
  - `savedCommands: Map<string, services.SavedCommand[]>` — persisted commands per workspace
  - `savedCommandEditingId: string` — ID of command being edited (empty = none; `"new-*"` prefix for new commands)
  - `savedCommandDraftName: string` — name input draft for editing
  - `savedCommandDraftCommand: string` — command input draft for editing
  - `savedCommandOpenSections: Set<string>` — which workspaces have the saved-commands `<details>` expanded

### `frontend/src/backend/services.ts`
- Added `RunShellCommand(workspaceID, command, workingDirectory, timeoutSeconds, maxOutputBytes)` — manual wrapper using `(window as any)["go"]...` for Wails and `webRpc` for web mode (Wails bindings don't include these methods)
- Added `StopShellCommand(workspaceID, runID)` — same pattern
- Added `GetSavedCommands(workspaceID)`, `UpsertSavedCommand(workspaceID, id, name, command, order)`, `DeleteSavedCommand(workspaceID, id)` — standard `call()` wrappers since Wails bindings are generated for these

### `frontend/src/app/bootstrap.ts`
- Registered `"echo:shell:event"` listener via `EventsOn`
- `applyShellCommandEvent(event)` handles all 4 event types:
  - `started`: creates new run entry with status "running"
  - `stdout`/`stderr`: appends line, auto-opens terminal
  - `completed`: sets final status, exitCode, durationMs
- `patchTerminalPanel()` does targeted DOM update of the runs container (avoids full re-render during streaming)
- Global capture-phase keydown handler for Enter on `[data-terminal-input]` to trigger run
- **Escape key handler** closes saved command dialog (capture-phase keydown on Escape when `savedCommandEditingId` is set)
- **Backdrop click handler** closes saved command dialog when clicking `.saved-command-dialog-overlay` outside `.saved-command-dialog`
- **Bootstrap loads saved commands** for each workspace after `LoadState()` via `GetSavedCommands(ws.id)`

### Saved Commands in render.ts
- `renderSavedCommandsSection(workspace)`: renders a `<details>` collapsible section between `.terminal-runs` and `.terminal-input-row`. Shows "+ Save current" button even when no commands exist (non-collapsible bar). When commands exist, wraps list in `<details>` with toggle. **No inline edit form** — editing uses an overlay dialog.
- `renderSavedCommandItem(sc, workspaceId)`: renders each saved command as a compact card: `.terminal-saved-info` (name bold + truncated mono command text stacked vertically) on the left, small edit/delete icon-buttons in `.terminal-saved-actions` on the right. The entire `.terminal-saved-item` div has `data-action="run-saved-command"` so tapping anywhere runs it.
- `renderSavedCommandDialog()`: renders a floating overlay dialog (in the overlays region via `buildOverlays()`) with title ("New Command" or "Edit Command"), full-width name input, full-width monospace command input, and Save/Cancel buttons in a `.dialog-actions` row. Shows when `savedCommandEditingId` is set.

### Saved Commands in actions.ts
Action handlers for saved commands:
- `toggle-saved-commands`: toggles workspace in `savedCommandOpenSections`, re-renders
- `run-saved-command`: looks up command from saved list, sets `terminalDrafts` to the command text, opens terminal, then programmatically clicks the run button after 50ms delay (needed for DOM to update before click fires)
- `add-saved-command`: reads `[data-terminal-input]` value; extracts first word as name draft; sets editing ID with `"new-*"` prefix; re-renders to show overlay dialog
- `edit-saved-command`: sets editing state from existing command, re-renders (shows overlay dialog)
- `delete-saved-command`: calls backend `DeleteSavedCommand`, updates local map, clears edit state if matching, re-renders
- `save-edited-command`: reads values from `[data-saved-edit-name]` and `[data-saved-edit-command]` inputs in the overlay dialog; validates non-empty; generates UUID for new commands or uses existing ID; calculates order; calls `UpsertSavedCommand`; updates local map; clears editing state
- `cancel-edit-command`: clears all editing state, re-renders

**Event propagation**: `bindActionEvents` stops propagation on clicks inside `.terminal-saved-actions` so edit/delete button clicks don't bubble to the parent `.terminal-saved-item` run handler.

### Saved Commands backend (already existed)
- `SavedCommand` struct: `{ID, Name, Command, Order}` in `internal/services/system.go`
- `AppState.SavedCommands map[string][]SavedCommand` persisted to state.json
- RPC whitelist in `internal/webserver/server.go` includes `GetSavedCommands`, `UpsertSavedCommand`, `DeleteSavedCommand`

### `renderTerminalRunHtml` vs `renderTerminalRun` — critical escaping invariant
- **`renderTerminalRunHtml`** in `bootstrap.ts`: used by `patchTerminalPanel()` during live streaming patches. Must use `escapeHtml()` for text content and `escapeAttribute()` for HTML attribute values. **Never use `CSS.escape()` for HTML rendering** — it escapes spaces as `\20 `, leaves `<`/`>`/`&` unescaped, and produces garbled output.
- **`renderTerminalRun`** in `render.ts`: used by full `render()`. Uses the same `escapeHtml()`/`escapeAttribute()` pattern. Both functions must produce identical HTML to avoid visual differences between streaming patches and full re-renders.
- Import `escapeHtml` and `escapeAttribute` from `./utils` in any file that generates HTML strings with user/workspace data.

### `frontend/src/app/actions.ts`
- `run-shell-command`: reads input value, clears draft, calls backend, updates command text on existing run, opens panel, renders
- `stop-shell-command`: extracts runID from data attribute, calls StopShellCommand
- `toggle-terminal`: toggles workspace in terminalOpen set

### `frontend/src/app/render.ts`
- `renderTerminalPanel(workspace)`: returns `<details>` with summary (icon + label + spinner), content area with runs list, saved commands section, and input row
- `renderTerminalRun(run)`: renders command header, status badges/spinner/stop button, scrollable output div
- `buildOverlays()`: includes `renderSavedCommandDialog()` when `savedCommandEditingId` is set (renders in overlays region alongside settings, toasts, context menu)
- Integrated into `renderWorkspacePanels` for mode `"chat"`

### `frontend/src/app/icons.ts`
- Added `terminal` icon SVG (lucide Terminal: polyline + line)

### `frontend/src/styles.css`
- `.terminal-panel`: grid layout with `max-height: calc(80vh - 200px)` and `overflow: hidden` to bound panel height; collapsible details/summary styling with rotation chevron. The `calc(80vh - 200px)` accounts for ~200px of UI chrome (header, chat composer, etc.) above the terminal so the input row stays within the viewport on desktop.
- `.terminal-content`: inner grid for runs area and input row, uses `align-content: start` (not stretch) to avoid unbounded growth
- `.terminal-runs`: scrollable command history container with `max-height: 400px` so overflow scrolls internally via `overflow-y: auto`
- `.terminal-output`: max-height 320px, monospace font, scrollable per-run output
- Monospace font: `"Cascadia Mono", "SFMono-Regular", Consolas, monospace`
- `.terminal-saved-commands`: collapsible section with border-top separator; summary toggle with chevron rotation
- `.terminal-saved-item`: compact card layout — flex row with `.terminal-saved-info` (vertical stack: bold name + muted mono command) and `.terminal-saved-actions` (small 28px icon buttons, opacity 0.6→1 on hover). Entire item is clickable (`cursor: pointer`) with accent-tinted hover background.
- `.saved-command-dialog-overlay`: fixed overlay at z-index 50 with backdrop blur, matching existing modal patterns (`.code-close-dirty-overlay`, `.overlay`)
- `.saved-command-dialog`: centered dialog panel with surface background, border, rounded corners; full-width inputs with monospace command field; Save (primary-button) and Cancel (secondary-button) actions row
- All interactive elements maintain `min-height: 36px` for mobile tap targets
- Mobile media query at ≤720px: dialog overlay adjusts padding, dialog fills width up to 400px max

## Backend Integration
Backend emits events on `"echo:shell:event"` (constant `ShellRuntimeEventName` in events.go):
- Event types: `"started"`, `"stdout"`, `"stderr"`, `"completed"`
- Run IDs are formatted as `"workspaceID:seq"` 
- `RunShellCommand` returns the runID immediately; output streams via events
- Web server RPC whitelist includes `RunShellCommand`, `StopShellCommand`, `GetSavedCommands`, `UpsertSavedCommand`, `DeleteSavedCommand`

## Key Design Decisions
1. **Manual backend wrappers** — Wails bindings don't auto-generate for `RunShellCommand`/`StopShellCommand`, so we use the same pattern as `PrepareRebuildAndRelaunch` (direct `(window as any)["go"]...` call). Saved command methods have generated bindings and use standard `call()` wrappers.
2. **Patch-based updates** — `patchTerminalPanel()` updates only the runs container during streaming to avoid full re-render jank. Saved commands section is not patched during streaming (it doesn't change during command execution).
3. **Auto-open on output** — terminal opens automatically when stdout/stderr arrives so users don't miss output
4. **Proper HTML escaping** — `escapeHtml()` for text content, `escapeAttribute()` for attribute values. Never use `CSS.escape()` in HTML generation (it's for CSS selectors only, used correctly in `querySelector` calls).
5. **"new-*" prefix for edit IDs** — New saved commands use `"new-${Date.now()}"` as temporary editing ID to distinguish from existing command IDs; `save-edited-command` checks this prefix to decide whether to generate a new UUID or update an existing record.
6. **Overlay dialog for saved commands** — Edit form renders in the overlays region (not inline in terminal panel) via `buildOverlays()`, so it floats above all content. Dialog closes on Escape key (capture-phase), backdrop click, Cancel button, or successful save.

## Pitfalls
- **CSS.escape() in HTML**: Using `CSS.escape()` to escape HTML content produces garbled output (`\20 ` for spaces) and leaves dangerous characters unescaped. Always use `escapeHtml()`/`escapeAttribute()` instead.
- **Panel height**: Without `max-height` on `.terminal-panel` and `.terminal-runs`, the terminal panel grows to fill remaining viewport space (`minmax(0, 1fr)`), pushing/overlapping chat content. The `calc(80vh - 200px)` on `.terminal-panel` accounts for UI chrome above it; a plain `60vh` caused the input row to be cut off at the viewport bottom.
- **Run-saved-command timing**: After setting `terminalDrafts`, the run button is clicked via `setTimeout(..., 50)` to allow the DOM to update with the new draft value before the click handler reads it. Without this delay, the run would execute an empty command.
- **Saved commands dialog reads from DOM**: `save-edited-command` reads current values from `[data-saved-edit-name]` and `[data-saved-edit-command]` inputs rather than state drafts, since the user may have edited them after the dialog rendered. State drafts are only used as initial values.
- **Event propagation on saved command items**: The `.terminal-saved-item` div has `data-action="run-saved-command"` for tap-to-run. Edit/delete buttons inside `.terminal-saved-actions` must stop propagation (handled in `bindActionEvents`) or clicking them would also trigger run on the parent item.
- **Dialog z-index**: Use z-index 50 for `.saved-command-dialog-overlay` to match `.code-close-dirty-overlay` and stay above other UI elements but below nothing that matters (it's a transient modal).
