---
name: integrated-terminal-xterm-architecture
description: Explain and safely modify Echo's integrated xterm.js terminal, including the persistent frontend controller, Go PTY lifecycle, sequenced output replay, Wails and web transport, dock rendering, resizing, saved commands, cleanup, and verification. Use for xterm.js, TerminalController, terminal session RPCs, go-pty, echo:terminal:event, terminal reconnects, or terminal UI work.
triggers:
    - xterm.js
    - "@xterm/xterm"
    - integrated terminal
    - TerminalController
    - terminal session
    - StartTerminalSession
    - SyncTerminalSession
    - echo:terminal:event
    - PTY
    - go-pty
    - terminal replay
    - terminal resize
---

# Integrated Terminal (xterm.js) Architecture

## Start with the correct terminal

Treat the integrated terminal as a byte-oriented interactive PTY:

```text
xterm.js input
  -> terminal session RPC
  -> Go PTY and interactive shell
  -> sequenced base64 output event
  -> xterm.js VT parser and renderer
```

Do not confuse it with the retired line-oriented shell panel. The current implementation uses:

- `StartTerminalSession`, `SyncTerminalSession`, `WriteTerminalSession`, `ResizeTerminalSession`, `StopTerminalSession`, and `RestartTerminalSession`
- `echo:terminal:event`
- `.terminal-dock` and `.terminal-xterm-instance`

Do not restore or extend the retired `RunShellCommand`, `StopShellCommand`, `echo:shell:event`, `ShellCommandRun`, or `.terminal-panel` flow. The web server test explicitly requires the retired shell RPC methods to remain absent from the allowlist. The agent `shell_command` tool is also a separate, non-interactive facility.

## Use this file map

| File | Responsibility |
|---|---|
| `frontend/src/app/terminal/index.ts` | Own xterm.js instances, session metadata, RPC calls, event ordering, replay, rendering, resizing, preferences, theming, mobile sizing, and saved-command execution. |
| `frontend/src/app/render.ts` | Render the terminal as its own app-shell region and call `mountTerminalDock` after region updates. |
| `frontend/src/app/actions.ts` | Route delegated toolbar, restart, stop, maximize, saved-command, and workspace-deletion actions. |
| `frontend/src/app/bootstrap.ts` | Load preferences, subscribe to `echo:terminal:event`, and resync the active terminal after web reconnect/visibility changes. |
| `frontend/src/app/state.ts` | Store client-only open, maximized, height, and saved-menu state per workspace. |
| `frontend/src/app/types.ts` | Define the frontend `TerminalEvent` transport shape. |
| `frontend/src/backend/services.ts` | Route terminal RPCs through generated Wails bindings on desktop or `webRpc` in a browser. |
| `frontend/src/backend/runtime.ts` and `web.ts` | Route events through Wails runtime events or the web SSE connection. |
| `frontend/src/styles.css` | Style the dock, toolbar, xterm host, saved-command popover, resize handle, status messages, and mobile full-screen layout. |
| `internal/services/terminal_session.go` | Own PTY creation, shell selection, input, resize, output sequencing/replay, stop/restart, and cleanup. |
| `internal/services/terminal_session_test.go` | Test session lifecycle, ANSI byte preservation, replay, truncation reset, bounds, stale IDs, restart, and concurrent cleanup. |
| `internal/services/events.go` | Name `TerminalRuntimeEventName` as `echo:terminal:event` and feed web event subscribers. |
| `internal/services/system.go` | Store `terminalSessions`, `terminalMu`, and `terminalSeq` on `SystemService`. |
| `internal/webserver/server.go` | Allow the six terminal RPC methods for authenticated web access. |
| `internal/webserver/server_test.go` | Verify the terminal RPC allowlist and retired shell RPC removal. |
| `frontend/wailsjs/go/...` | Generated terminal methods and `TerminalSessionSnapshot`/`TerminalOutputChunk` models. Never edit these by hand. |

Dependencies live in `frontend/package.json` (`@xterm/xterm`, `@xterm/addon-fit`, and `@xterm/addon-web-links`) and `go.mod` (`github.com/aymanbagabas/go-pty`).

## Preserve the frontend ownership model

Keep one `TerminalController` per workspace per frontend client in `terminalControllers`. Keep lightweight toolbar/session information separately in `terminalMeta`.

Create xterm.js lazily when `terminalController(workspaceID)` is first requested. Configure the terminal with:

- a blinking bar cursor
- a monospace font stack and 13px font size
- 5,000 lines of client-side scrollback
- colors derived from Echo's code-editor CSS variables
- `FitAddon` for viewport-to-row/column calculation
- `WebLinksAddon` for terminal links

Keep the `TerminalController.host` DOM element and xterm.js instance alive across normal application renders. `renderTerminalDock` returns the dock shell and an empty `[data-terminal-viewport]`; `mountTerminalDock` then reattaches the controller's existing host to that viewport. This separation preserves xterm's parser, buffer, selection, and scrollback instead of rebuilding the terminal from HTML.

When changing render behavior:

1. Continue rendering the terminal in the dedicated `data-region="terminal"` app-shell region.
2. Continue calling `mountTerminalDock` after `updateRegion` and event binding.
3. Append the existing controller host; do not serialize terminal output into `innerHTML`.
4. Call `terminal.open(host)` only when `terminal.element` does not exist.
5. Fit only after the host has non-zero dimensions.
6. Focus only when the interaction warrants it. The current mount path focuses when the host has just been attached, while start, restart, and saved-command execution also restore focus.

Write live output directly with `terminal.write`. Do not trigger a full app render for each `data` event. Full renders are appropriate for toolbar/status transitions such as start, stop, exit, errors, opening, maximizing, and saved-command UI changes.

## Follow the session start and shell lifecycle

Keep exactly one backend `terminalSession` per workspace in `SystemService.terminalSessions`.

`StartTerminalSession` must:

1. Validate the workspace and choose its first available folder as the working directory.
2. Resolve PowerShell Core and then Windows PowerShell on Windows; otherwise use an absolute valid `$SHELL` or fall back to `/bin/sh`.
3. Clamp the requested terminal size.
4. Return the existing session snapshot when the workspace already has a session. Start is intentionally idempotent.
5. Allocate a new session ID as `workspaceID:sequence`.
6. Create and size a `go-pty` backend before launching the shell.
7. Set `TERM=xterm-256color`, `COLORTERM=truecolor`, and `TERM_PROGRAM=Echo`.
8. Store the running session, emit a `started` event, and launch `runTerminalSession` in a goroutine.

Closing the dock only hides it. It must not kill the shell or dispose the controller. Sessions and xterm buffers are runtime-only, not persisted in `state.json`.

`StopTerminalSession` kills and closes the current PTY and waits for its reader/process goroutine to finish. Keep stop idempotent internally through `stopOnce`.

`RestartTerminalSession` must remove the old session from the workspace map before waiting for it to stop, then create a new session and session ID. Reject writes and resizes that carry a stale session ID.

On natural process exit:

- retain the exited session and replay buffer
- publish an `exited` event with the last output sequence, exit code, and optional error message
- show the frontend exit message
- restart when the user explicitly clicks Restart or presses Enter in the exited xterm instance

Close the PTY during workspace deletion through `closeWorkspaceTerminalSession`. Close all PTYs during `SystemService.Shutdown` through `closeAllTerminalSessions`. Remove the frontend controller and local preferences with `disposeWorkspaceTerminal` after workspace deletion.

## Preserve raw terminal bytes

Do not convert PTY output into lines, JSON text, or HTML. ANSI control sequences, cursor movement, alternate-screen programs, partial UTF-8 sequences, carriage returns, and binary-safe byte boundaries must reach xterm.js unchanged.

The backend output path is:

1. Read up to 32 KiB from the PTY.
2. Copy the returned bytes before reusing the read buffer.
3. Increment the session output sequence.
4. append the byte chunk to the replay buffer.
5. Trim oldest chunks while retained output exceeds 2 MiB.
6. Base64-encode the chunk into a `TerminalEvent{type:"data"}`.
7. Emit it to both the internal runtime event bus (used by web SSE) and Wails runtime events.

The frontend must decode base64 into a `Uint8Array` and pass those bytes to `terminal.write`. Do not decode terminal output with `TextDecoder`; xterm.js must own stream decoding and VT parsing.

## Keep sequence and replay recovery intact

Treat event delivery as lossy. Web SSE subscribers can disconnect or drop buffered events, and a frontend can be suspended.

Track `lastSequence` in each controller:

- Ignore duplicate or old events where `sequence <= lastSequence`.
- Accept a live event only when `sequence === lastSequence + 1`.
- Call `SyncTerminalSession(workspaceID, sessionID, lastSequence)` when a gap appears.
- Also resync after failed writes/resizes and after browser reconnect, visibility restoration, `pageshow`, or `online`.

`SyncTerminalSession` returns a `TerminalSessionSnapshot` containing output newer than `afterSequence`. When the requested sequence predates the oldest retained 2 MiB chunk, return `reset: true` and all retained chunks.

When applying a snapshot:

1. Reset xterm and set `lastSequence` to zero for a forced reset, changed session ID, or `snapshot.reset`.
2. Sort chunks by sequence.
3. Skip already-applied chunks.
4. Write each decoded byte chunk to xterm.
5. Advance to at least `snapshot.lastSequence`.
6. Update `terminalMeta`, flush input waiting for a session ID, and render status UI.

If sync says the session no longer exists, call `StartTerminalSession`. Because start is idempotent, this safely attaches to a current shared session or creates a replacement.

Keep the session ID and sequence checks on both sides. They prevent output from a replaced PTY and input intended for an old PTY from corrupting the current terminal.

## Keep input ordered and size-bounded

Route xterm's `onData` strings through `queueInput`:

1. Buffer input until the next animation frame to avoid an RPC per keystroke burst.
2. Split by UTF-8 byte length, not JavaScript string length.
3. Keep frontend chunks at or below 48 KiB, leaving headroom under the backend 64 KiB limit.
4. Serialize calls through `writeChain` so paste and typing order cannot race.
5. On a write failure, show a toast and resync.

Keep the backend 64 KiB check and `writeMu`. Loop until every byte is written and report zero-byte writes as `io.ErrShortWrite`.

Saved commands use the same path: open the dock, ensure a session exists, append `\r`, queue the command as terminal input, and focus xterm. Keep saved-command persistence in the existing `SavedCommand` backend APIs; do not add it to terminal session snapshots.

## Keep resizing two-stage and debounced

Use `ResizeObserver` to notice viewport changes and `FitAddon.fit()` to calculate xterm rows and columns. Let xterm's `onResize` event feed a 100 ms debounce before calling `ResizeTerminalSession`.

Keep backend size clamping at 2-500 columns and 2-200 rows. Do not trust browser dimensions.

Desktop dock height is a separate UI concern:

- default to 280px
- clamp to at least 160px and at most 70% of the viewport
- update `--terminal-dock-height` while pointer-dragging
- support ArrowUp, ArrowDown, Home, and End on the resize separator
- fit xterm during height changes
- persist height after the interaction

On mobile, use a fixed full-screen terminal and the `visualViewport` height/offset CSS variables so the on-screen keyboard and browser chrome do not obscure the terminal. Do not expose the desktop resize or maximize controls at the mobile breakpoint.

## Separate client preferences from backend state

Persist only `open`, `maximized`, and `height` per workspace in local storage under `echo.terminalDock.v1`. Validate stored workspace IDs and clamp stored heights when loading.

Do not persist:

- session IDs
- terminal status
- replay sequences
- PTY output
- xterm scrollback
- shell processes
- the saved-command popover's open state

Each desktop or browser client has its own xterm instance and local preferences. The backend PTY is shared per workspace, so multiple authenticated web clients can type, resize, stop, or restart the same shell. Preserve the web-access warning in settings and treat every terminal RPC as privileged command execution.

## Maintain Wails and web parity

Keep all six terminal methods in `frontend/src/backend/services.ts` using the common `call()` wrapper:

- Wails desktop: generated `SystemService` bindings
- Web access: authenticated `webRpc`

Keep `echo:terminal:event` flowing through:

- Wails `EventsEmit` -> `frontend/src/backend/runtime.ts`
- `emitRuntimeEvent` -> web server SSE -> `webEventsOn`

Keep the six RPC names in `internal/webserver/server.go` and its focused allowlist test.

After changing an exported Go terminal model or `SystemService` method signature, run `wails generate`. Never hand-edit `frontend/wailsjs`. Then update the handwritten frontend wrapper and `TerminalEvent` type if the transport changed.

## Preserve the primary concurrency rules

- Use `terminalMu` only for the workspace-to-session map and global session sequence.
- Use each session's `mu` for status, exit metadata, output sequence, and replay data.
- Use `writeMu` to serialize PTY writes.
- Use `stopOnce` to make concurrent stop/shutdown safe.
- Remove sessions from the global map before waiting during restart or cleanup.
- Never hold `terminalMu` while waiting for a process to exit.
- Copy PTY read buffers before storing or emitting them.
- Keep event emission outside the session lock.

When adding fields to snapshots or events, clone pointer values while holding the session lock and return immutable copies of replay chunks.

## Avoid these common regressions

- Do not recreate `Terminal` during `render()`; it loses scrollback, selection, focus state, and parser state.
- Do not render terminal bytes as HTML or use `escapeHtml` on PTY output; pass bytes to xterm.js.
- Do not use line scanners for PTY output; they break prompts, carriage-return updates, and full-screen programs.
- Do not assume events arrive exactly once or in order; preserve sequence checks and snapshot repair.
- Do not send a JavaScript string larger than the Go byte limit; keep UTF-8-aware chunking.
- Do not run multiple PTYs for one workspace unless the product model, backend map, UI state, and RPC contracts are redesigned together.
- Do not kill the shell when the dock is collapsed or the app rerenders.
- Do not forget web RPC allowlisting or SSE recovery when adding terminal operations.
- Do not manually modify generated Wails files.
- Do not treat saved commands as a second execution engine; inject them into the interactive session.

## Verify changes

Run focused checks first:

```powershell
go test ./internal/services -run TerminalSession
go test ./internal/webserver -run TerminalRPC
cd frontend
npm run build
```

Run broader checks when a change crosses service, lifecycle, web, or shared frontend boundaries:

```powershell
go test ./...
cd frontend
npm run build
```

Regenerate bindings before the frontend build when exported backend types or signatures changed:

```powershell
wails generate
```

Manually verify the behavior affected by the change:

1. Open, collapse, reopen, resize, maximize, stop, and restart the terminal.
2. Run ANSI-colored output and an interactive/full-screen command.
3. Paste Unicode and a large input block.
4. Confirm output survives unrelated app renders without duplication.
5. Confirm a process exit shows status and Enter starts a new shell.
6. Confirm saved commands enter the same PTY.
7. Confirm desktop Wails and authenticated web access both receive output.
8. Confirm reconnect or visibility restoration catches up without duplicate output.
9. Confirm deleting a workspace and shutting down Echo close the PTY.
