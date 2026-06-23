# AGENTS.md

Guidance for AI agents working in this repository.

## Project Snapshot

Echo is a Wails v2 desktop app. The backend is Go and the frontend is a modular, frameworkless TypeScript/Vite UI.

The app supports **web access** via a LAN HTTP server with RPC proxy and SSE event streaming, making the UI reachable from browsers on the network.

The product flow is:

1. A user selects one or more local workspaces.
2. Chat asks an OpenAI-compatible chat completions endpoint to inspect or plan work.
3. `ExecutePlan` decomposes visible chat into Kanban cards.
4. Kanban agents execute cards with registered local tools, streaming progress back to the UI.

Agents have additional capabilities:
- **LSP-powered code navigation** — go-to-definition, references, implementations, hover, document symbols, and completions.
- **Git integration** — branch management, change tracking, and commits for workspace files.
- **Web search** — queries a configurable SearXNG endpoint for current public information.

This app also reads an `AGENTS.md` file from each selected workspace and injects it into agent system prompts. The loader is in `internal/services/workspace_instructions.go` and truncates instructions after 64 KiB.

## Repository Map

- `main.go`: Wails entrypoint, embedded frontend assets, window options, service binding.
- `app.go`: app lifecycle wrapper; initializes `services.SystemService`.
- `internal/services`: Wails-bound application services, chat orchestration, Kanban state/scheduler, decomposition, prompts, workspace persistence.
- `internal/llm`: OpenAI-compatible chat completions client, streaming SSE parser, request/settings types.
- `internal/tools`: agent tool registry plus filesystem and shell tools scoped to the active workspace.
- `frontend/src/app/`: modular frontend — see Frontend Notes below.
- `frontend/src/styles.css`: all app styling.
- `frontend/wailsjs`: generated Wails bindings. Do not edit these by hand.
- `internal/searxng`: SearXNG web search client.
- `internal/webserver`: HTTP server for web access; RPC proxy to `SystemService`, SSE event endpoint, static asset serving.
- `build/windows`: Windows packaging assets.
- `build/appicon.png` and `build/bin`: Packaging assets.
- `docs/screenshots`: Product screenshots.

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
- `chat_images.go`: image attachment handling for chat messages.
- `errors.go`: shared error types and sentinel errors.
- `events.go`: runtime event subscription used by the web server SSE endpoint.
- `file_changes.go` / `git_changes.go` / `git_discard.go` / `git_repository.go`: Git repository operations, change tracking, branch management, and discard.
- `inline_code_prompt.go`: inline code editing prompts from the code view.
- `kanban_verification.go`: post-execution verification of Kanban card results.
- `lsp.go` / `lsp_go.go` / `lsp_query.go` / `lsp_warmup.go`: LSP server lifecycle, Go-specific handling, query execution, and warmup.
- `web_access.go`: web access settings, token rotation, and status management.
- `workspace_command_default.go` / `workspace_command_windows.go`: OS-specific workspace file/folder explorer launchers.
- `workspace_context.go`: programming context brief builder for agents.
- `workspace_files.go`: workspace file read/create/edit/delete/search operations.
- `workspace_ignore.go`: workspace ignore pattern handling.
- `workspace_text_search.go`: text search across workspace files.

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
- Filesystem tools are individually gated: `filesystem_list`, `filesystem_read_text`, `filesystem_read_image`, `filesystem_search_text`, `filesystem_search_workspace`, `filesystem_stat`, `filesystem_create_text`, `filesystem_edit_text`, `filesystem_delete_file`.
- `lsp_query` tool gives agents code navigation (definitions, references, implementations, hover, symbols, members).
- `web_search` tool provides web search via a configurable SearXNG endpoint.
- `workspace_context` tool builds compact programming context briefs for agents.

## Frontend Notes

The frontend is intentionally plain TypeScript, now organized into a modular structure:

- `frontend/src/app/`: core app logic — state, rendering, actions, events, theme, notifications, QR code, context menus, toasts, DOM utilities, and types. Subdirectories `chat/`, `kanban/`, `git/`, `changes/`, `settings/`, `workspace/` hold domain-specific UI code.
- `frontend/src/app/bootstrap.ts`: entry point that initializes and starts the app.
- `frontend/src/backend/`: backend abstraction layer. `services.ts` wraps Wails service calls; `runtime.ts` handles Wails runtime events; `web.ts` handles web-access mode (RPC calls + SSE).
- `frontend/src/codeView/`: code editor/viewer with file explorer, tabs, LSP integration, inline chat, search, references panel, and navigation.
- `frontend/src/markdown.ts`: Markdown rendering utilities.
- `render()` lives in `app/render.ts` and rewrites the app shell; targeted patch helpers update streaming chat/Kanban content without a full rerender where needed.
- Backend types and service functions come from `frontend/wailsjs/go/...`.
- Runtime events come from `EventsOn` in `frontend/wailsjs/runtime/runtime`.
- Keep new UI code consistent with the existing render-function and `data-action` event-delegation pattern.

After adding or renaming backend methods/types, do not hand-edit generated bindings. Run `wails generate` and then update imports/usages in `frontend/src/app/`.

## Testing Guidance

Use focused tests for the package you change, then broader checks when behavior crosses service/frontend boundaries.

- Backend changes: `go test ./...`
- LLM parsing/streaming changes: add or update tests in `internal/llm`.
- Chat, decomposition, Kanban, or persistence changes: add or update tests in `internal/services`.
- LSP changes: add or update tests in `internal/services` (lsp_test.go, lsp_query_test.go, lsp_go_test.go).
- Web access changes: add or update tests in `internal/webserver`.
- SearXNG changes: add or update tests in `internal/searxng`.
- Tool changes: add or update tests in `internal/tools`.
- Web search tool changes: add or update tests in `internal/tools` (web_search_test.go).
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
- Web access RPC methods are whitelisted in `internal/webserver/server.go`; new `SystemService` methods need explicit entry to be available over HTTP.
- LSP server lifecycle is managed per-workspace; changes must preserve shutdown and restart behavior.

