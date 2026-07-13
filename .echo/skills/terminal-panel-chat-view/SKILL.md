---
name: terminal-panel-chat-view
description: 'Terminal panel in chat view: types, state, backend wrappers, event handling, rendering with proper HTML escaping, actions, and CSS for live streaming shell commands. Covers the critical invariant that patch rendering (renderTerminalRunHtml) and full rendering (renderTerminalRun) must use identical escapeHtml/escapeAttribute escaping.'
triggers:
    - terminal panel
    - shell command
    - live streaming output
    - echo:shell:event
    - RunShellCommand
    - StopShellCommand
    - terminal runs
    - stdout stderr
    - exit code badge
    - CSS.escape
    - escapeHtml
    - renderTerminalRunHtml
---

# Terminal Panel in Chat View

## Overview
The terminal panel is a collapsible `<details>` section rendered below the chat panel in chat mode. It provides command history with live-scrolling, color-coded stdout/stderr lines, exit code badges, duration display, spinner for running commands, stop button, and an input row at the bottom.

## Files Modified

### `frontend/src/app/types.ts`
- Added `ShellCommandRunStatus`: `"running" | "completed" | "timed-out"`
- Added `ShellCommandLine`: `{ type: "stdout" | "stderr"; text: string }`
- Added `ShellCommandRun`: tracks id, command, lines[], status, exitCode?, durationMs?, startedAt
- Added `ShellCommandEvent`: matches backend event structure with workspaceId, id, type, and data

### `frontend/src/app/state.ts`
- Added per-workspace terminal state:
  - `terminalRuns: Map<string, ShellCommandRun[]>` — command history per workspace
  - `terminalDrafts: Map<string, string>` — input draft per workspace
  - `terminalOpen: Set<string>` — which workspaces have the panel expanded

### `frontend/src/backend/services.ts`
- Added `RunShellCommand(workspaceID, command, workingDirectory, timeoutSeconds, maxOutputBytes)` — manual wrapper using `(window as any)["go"]...` for Wails and `webRpc` for web mode (Wails bindings don't include these methods)
- Added `StopShellCommand(workspaceID, runID)` — same pattern

### `frontend/src/app/bootstrap.ts`
- Registered `"echo:shell:event"` listener via `EventsOn`
- `applyShellCommandEvent(event)` handles all 4 event types:
  - `started`: creates new run entry with status "running"
  - `stdout`/`stderr`: appends line, auto-opens terminal
  - `completed`: sets final status, exitCode, durationMs
- `patchTerminalPanel()` does targeted DOM update of the runs container (avoids full re-render during streaming)
- Global capture-phase keydown handler for Enter on `[data-terminal-input]` to trigger run

### `renderTerminalRunHtml` vs `renderTerminalRun` — critical escaping invariant
- **`renderTerminalRunHtml`** in `bootstrap.ts`: used by `patchTerminalPanel()` during live streaming patches. Must use `escapeHtml()` for text content and `escapeAttribute()` for HTML attribute values. **Never use `CSS.escape()` for HTML rendering** — it escapes spaces as `\20 `, leaves `<`/`>`/`&` unescaped, and produces garbled output.
- **`renderTerminalRun`** in `render.ts`: used by full `render()`. Uses the same `escapeHtml()`/`escapeAttribute()` pattern. Both functions must produce identical HTML to avoid visual differences between streaming patches and full re-renders.
- Import `escapeHtml` and `escapeAttribute` from `./utils` in any file that generates HTML strings with user/workspace data.

### `frontend/src/app/actions.ts`
- `run-shell-command`: reads input value, clears draft, calls backend, updates command text on existing run, opens panel, renders
- `stop-shell-command`: extracts runID from data attribute, calls StopShellCommand
- `toggle-terminal`: toggles workspace in terminalOpen set

### `frontend/src/app/render.ts`
- `renderTerminalPanel(workspace)`: returns `<details>` with summary (icon + label + spinner), content area with runs list and input row
- `renderTerminalRun(run)`: renders command header, status badges/spinner/stop button, scrollable output div
- Integrated into `renderWorkspacePanels` for mode `"chat"`

### `frontend/src/app/icons.ts`
- Added `terminal` icon SVG (lucide Terminal: polyline + line)

### `frontend/src/styles.css`
- `.terminal-panel`: grid layout with `max-height: 60vh` and `overflow: hidden` to bound panel height; collapsible details/summary styling with rotation chevron
- `.terminal-content`: inner grid for runs area and input row, uses `align-content: start` (not stretch) to avoid unbounded growth
- `.terminal-runs`: scrollable command history container with `max-height: 400px` so overflow scrolls internally
- `.terminal-output`: max-height 320px, monospace font, scrollable per-run output
- Monospace font: `"Cascadia Mono", "SFMono-Regular", Consolas, monospace`

## Backend Integration
Backend emits events on `"echo:shell:event"` (constant `ShellRuntimeEventName` in events.go):
- Event types: `"started"`, `"stdout"`, `"stderr"`, `"completed"`
- Run IDs are formatted as `"workspaceID:seq"` 
- `RunShellCommand` returns the runID immediately; output streams via events
- Web server RPC whitelist includes both `RunShellCommand` and `StopShellCommand`

## Key Design Decisions
1. **Manual backend wrappers** — Wails bindings don't auto-generate for these methods, so we use the same pattern as `PrepareRebuildAndRelaunch` (direct `(window as any)["go"]...` call)
2. **Patch-based updates** — `patchTerminalPanel()` updates only the runs container during streaming to avoid full re-render jank
3. **Auto-open on output** — terminal opens automatically when stdout/stderr arrives so users don't miss output
4. **Proper HTML escaping** — `escapeHtml()` for text content, `escapeAttribute()` for attribute values. Never use `CSS.escape()` in HTML generation (it's for CSS selectors only, used correctly in `querySelector` calls).

## Pitfalls
- **CSS.escape() in HTML**: Using `CSS.escape()` to escape HTML content produces garbled output (`\20 ` for spaces) and leaves dangerous characters unescaped. Always use `escapeHtml()`/`escapeAttribute()` instead.
- **Panel height**: Without `max-height` on `.terminal-panel` and `.terminal-runs`, the terminal panel grows to fill remaining viewport space (`minmax(0, 1fr)`), pushing/overlapping chat content. The current bounded heights (60vh panel, 400px runs) prevent this.
