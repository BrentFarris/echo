# AGENTS.md

Guidance for AI agents working in this repository.

## Project Snapshot

Echo is a Wails v2 desktop app. The backend is Go and the frontend is a small, frameworkless TypeScript/Vite UI.

The product flow is:

1. A user selects one or more local workspaces.
2. Chat asks an OpenAI-compatible chat completions endpoint to inspect or plan work.
3. `ExecutePlan` decomposes visible chat into Kanban cards.
4. Kanban agents execute cards with registered local tools, streaming progress back to the UI.

This app also reads an `AGENTS.md` file from each selected workspace and injects it into agent system prompts. The loader is in `internal/services/workspace_instructions.go` and truncates instructions after 64 KiB.

## Repository Map

- `main.go`: Wails entrypoint, embedded frontend assets, window options, service binding.
- `app.go`: app lifecycle wrapper; initializes `services.SystemService`.
- `internal/services`: Wails-bound application services, chat orchestration, Kanban state/scheduler, decomposition, prompts, workspace persistence.
- `internal/llm`: OpenAI-compatible chat completions client, streaming SSE parser, request/settings types.
- `internal/tools`: agent tool registry plus filesystem and shell tools scoped to the active workspace.
- `frontend/src/main.ts`: single-page UI, render functions, event handlers, Wails service calls, runtime event listeners.
- `frontend/src/styles.css`: all app styling.
- `frontend/wailsjs`: generated Wails bindings. Do not edit these by hand.
- `build/windows`: Windows packaging assets.

## Common Commands

Run from the repo root unless noted.

```powershell
wails dev
go test ./...
cd frontend
npm run build
cd ..
wails build
```

When backend service method signatures or exported Go models change, regenerate Wails bindings:

```powershell
wails generate
```

The Wails config runs `npm install`, `npm run dev`, and `npm run build` inside `frontend` as needed.

## Backend Notes

`SystemService` is the main boundary exposed to the frontend. Adding a UI-callable backend feature usually means adding an exported method on `*SystemService`, then regenerating Wails bindings.

Important service files:

- `system.go`: persisted app settings/workspaces and workspace status checks.
- `chat.go`: chat sessions, streaming assistant messages, tool execution loop, `echo:chat:event`.
- `decomposition.go`: visible-plan-to-Kanban-card conversion and JSON parsing/validation.
- `kanban.go`: board/card model and lane operations.
- `kanban_scheduler.go`: concurrent card agents, progress events, cancellation, `echo:kanban:event`.
- `state_persistence.go`: persisted chat/Kanban snapshot format and startup restoration.
- `workspace_instructions.go`: reads workspace `AGENTS.md` into prompts.
- `stream_loop.go` and `inline_tool_call.go`: safeguards for looping streams and models that emit inline tool-call text.

State details matter:

- Settings, workspace list, current chat sessions, and Kanban cards persist to the current user's config dir at `Echo/state.json`.
- Persistence stores only the latest snapshot, not historical revisions. Live stream/agent process state and detail-view tracking remain runtime-only.
- Interrupted chat streams restore as canceled; interrupted in-progress Kanban cards restore as blocked.
- `SystemService.mu` protects persisted app state and cards.
- `SystemService.chatMu` protects chat sessions, stream cancels, active Kanban runs/agents, and detail-view tracking.
- Prefer clone helpers already in the package when returning state to callers.

LLM behavior:

- `internal/llm` expects an OpenAI-compatible `/chat/completions` endpoint.
- Streaming uses server-sent events and emits content, reasoning, tool-call deltas, completion, errors, and cancellation.
- Tests generally use `httptest` servers rather than live model endpoints.

Tool behavior:

- Tools register themselves with `tools.Register` in `init`.
- The default registry is exposed to models through `tools.LLMSchema()`.
- Filesystem and shell tools require workspace-relative paths and reject traversal outside the workspace.
- Text file reads/edits are capped; see `maxTextFileBytes` in `internal/tools/filesystem_helpers.go`.
- Shell commands default to PowerShell on Windows and `/bin/sh` elsewhere, with bounded timeout/output.

## Frontend Notes

The frontend is intentionally plain TypeScript:

- `frontend/src/main.ts` owns state maps, rendering, event delegation, service calls, and Wails event listeners.
- `render()` rewrites the app shell; targeted patch helpers update streaming chat/Kanban content without a full rerender where needed.
- Backend types and service functions come from `frontend/wailsjs/go/...`.
- Runtime events come from `EventsOn` in `frontend/wailsjs/runtime/runtime`.
- Keep new UI code consistent with the existing render-function and `data-action` event-delegation pattern.

After adding or renaming backend methods/types, do not hand-edit generated bindings. Run `wails generate` and then update imports/usages in `frontend/src/main.ts`.

## Testing Guidance

Use focused tests for the package you change, then broader checks when behavior crosses service/frontend boundaries.

- Backend changes: `go test ./...`
- LLM parsing/streaming changes: add or update tests in `internal/llm`.
- Chat, decomposition, Kanban, or persistence changes: add or update tests in `internal/services`.
- Tool changes: add or update tests in `internal/tools`.
- Frontend TypeScript changes: `cd frontend; npm run build`
- Release/build changes: `wails build`

Prefer deterministic tests with temp dirs and local HTTP handlers. Avoid live network/model calls in tests.

## Change Hazards

- Do not persist chat history or Kanban cards unless the product behavior explicitly changes.
- Do not block Wails service methods on long-running model/tool work; current chat and Kanban execution use goroutines and stream progress through events.
- Keep workspace paths normalized and do not bypass the existing workspace path guards.
- Preserve cancellation behavior when changing streams or agents.
- Keep frontend generated files out of manual edits.
- If a change affects a Wails-bound Go type or method, remember that TypeScript bindings can become stale until regenerated.

