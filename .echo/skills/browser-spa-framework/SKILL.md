---
name: browser-spa-framework
description: 'Conventions and architecture for the browser-based Echo web app: Go stdlib server on port 3740 serving a plain-JS ES-module SPA, JSON API envelope, WebSocket hub, base UI shell with design tokens, internal/llm OpenAI-compatible client, WebSocket chat streaming, full-page settings view, shared appdata echo.json (settings + workspaces), and the workspace registration flow (.echo folder, workspace.json, icon).'
triggers:
    - Echo web server
    - SPA frontend
    - add API endpoint
    - new view module
    - JSON envelope
    - port 3740
    - base UI shell
    - LLM client
    - chat streaming
    - workspace
    - appdata echo.json
    - add workspace
---

# Echo Browser SPA Framework

Echo is now a browser-based app (not Wails). A Go server hosts a single-page application and a JSON/WebSocket API. Old Wails code lives in `echo/OLD` and should not be referenced for new work.

## Shared app data file (echo.json) — settings + workspaces
- `internal/appdata` owns the single Echo app data file at `os.UserConfigDir()/Echo/echo.json` (`DefaultStorePath()`). The file is one JSON document: `{"settings": <raw JSON>, "workspaces": [...]}`.
- `appdata.File` keeps `Settings` as `json.RawMessage` so the package stays decoupled from the settings schema. `appdata.Store.Load()` migrates a legacy bare-settings file (no `"settings"` key) by treating the whole document as settings. `Save()` writes to a `.tmp` then renames into place.
- **Both the settings store and the workspace manager share this one file** so they never clobber each other. When adding a new persisted top-level concern, extend `appdata.File` rather than creating a separate file.
- Tests must use `NewWithSettingsPath(addr, webDir, tempPath)` with an isolated temp path so they never touch the real `echo.json`.

## Workspace registration (internal/workspaces)
- `workspaces.Manager` reads/writes the workspace list inside the shared appdata file. `Create(CreateRequest{Name, MainPath, Folders, Icon})`:
  1. Verifies the name is unique (case-insensitive).
  2. Ensures every folder path exists and is a directory on the server machine.
  3. Creates `.echo/` in the main folder (`os.MkdirAll`).
  4. Copies an uploaded icon to `.echo/icon.<ext>` (ext whitelisted: png/gif/jpeg/jpg/webp/bmp/svg/ico).
  5. Writes `.echo/workspace.json` with `{name, mainPath, folders}` — the full folder list (main first, then extras) that Echo operates on.
  6. Appends an entry (id, name, mainPath, iconExt, folders) to the shared appdata file.
- `Icon.Data` is `[]byte`, so the frontend must send image bytes as a **base64 string** (Go decodes `[]byte` from base64 JSON). `IconPath(id)` auto-detects the icon extension from `iconExt`, falling back to scanning `icon.*`.
- API endpoints in `internal/server/workspaces_api.go`: `GET /api/workspaces`, `POST /api/workspaces`, `GET /api/workspaces/{id}/icon` (serves the image). All use the standard JSON envelope.

## Backend (Go, stdlib net/http)
- Entry point: `echo/main.go`. Default port `3740` (`-port` flag), web root `-web web`.
- `internal/server` package:
  - `server.go` — `Server` struct, `New(addr, webDir)`, `routes()`, `ListenAndServe()`, `Shutdown()`. Uses Go 1.22+ method-pattern `ServeMux` (e.g. `GET /api/health`). `Server` holds `store *settings.Store` and `workspaces *workspaces.Manager` (both backed by the same appdata path).
  - `api.go` — JSON helpers `writeJSON`, `writeData`, `writeError`, plus handlers.
  - `ws.go` — WebSocket `Hub` (gorilla/websocket).
- **JSON envelope**: every endpoint returns `{"ok":true,"data":...}` on success or `{"ok":false,"error":"..."}` on failure. New endpoints must follow this.

## Settings persistence
- `internal/settings/store.go` — `Store` loads/saves `llm.Settings` into the shared appdata file, preserving the workspace list. `Load()` returns defaults if missing; `Save()` normalizes (endpoint profiles authoritative) and writes through `appdata.Store`.
- `internal/server/settings_api.go` — `GET /api/settings` returns `{settings, storagePath}`; `PUT /api/settings` decodes `{settings}`, calls `Validate()`, `store.Save()`, then `initLLM()`.
- `initLLM()` loads settings and calls `cfg.ForInteraction(llm.InteractionChat)`. `Server.llm` is a `chatStreamer` interface so tests inject a fake.
- Frontend: `js/views/settings.js` calls `get("/api/settings")` on mount and `put("/api/settings", {settings: buildSettings()})` on Save.

## WebSocket hub
- `Hub` owns a `clients map[*client]struct{}` guarded by `sync.RWMutex`, plus `register`/`unregister`/`shutdown` channels and a `run()` event loop.
- **Race avoidance**: on register, the hub queues the `welcome` event directly to the client *inside* the `register` case after inserting into the map. Do NOT broadcast from the HTTP handler immediately after `hub.register <- c`.
- `Hub.Broadcast(event)` marshals to JSON and fans out to all clients; safe from any goroutine.
- Each connection runs a `writePump` goroutine and a blocking `readPump`. `client.sendJSON(v)` queues JSON to one client safely.

## Chat streaming (WebSocket, bidirectional)
- client -> `{type:"chat", message}`; server -> `chat_start`, `chat_event` (token/reasoning/...), `chat_done`/`chat_error`. Handled in `ws.go` via `client.handleChatMessage`.
- **Testability**: `Server.llm` is a `chatStreamer` interface (`StreamChat(ctx, req) *llm.Stream`). Use this pattern for any new LLM-backed endpoint.

## LLM client (`internal/llm`)
- Raw OpenAI-compatible client. `types.go` (Message/ChatRequest/StreamEvent), `settings.go` (Settings/LLMEndpoint/EndpointSelection, `ForInteraction()`), `client.go` (`NewClient`, `Complete`, `StreamChat`, `Cancel`), `stream.go` (SSE parsing, emits token/reasoning/tool_call/complete/usage/error/canceled).
- `chatCompletionsURL(endpoint)` appends `/chat/completions` unless already present. Streaming uses no total request timeout.

## Frontend (plain JavaScript, no build step)
- Served directly from `echo/web` (`index.html`, `css/app.css`, `js/...`). ES modules via `<script type="module">` — no bundler.
- `js/api.js` — `api(path, {query, body})` fetch wrapper returning the `data` field; throws on non-ok. Helpers `get/post/put/del`.
- `js/ws.js` — WebSocket client with auto-reconnect + backoff.
- `js/icons.js` — inline stroke-based SVG icon set (24x24 viewBox, `currentColor`). Reuse these rather than adding icon libraries.
- `js/app.js` — hash router mapping routes to lazy-loaded view modules; swaps them into `#app`.
- **View contract**: each view module exports `mount(root)` and `unmount()`. `mount` renders HTML, wires listeners, and stores a cleanup closure (module-level var); `unmount` runs it. Keep `unmount` idempotent.

## Workspace selector + add-workspace modal (js/workspaces.js)
- `loadWorkspaces()` fetches `GET /api/workspaces`; `openWorkspaceDropdown(trigger, {onSelect, onAdd})` renders a fixed-position dropdown anchored to the trigger (appended to `<body>` to avoid clipping) and returns a cleanup function. `openAddWorkspaceModal({onCreate})` renders and wires the modal, returns cleanup.
- The modal has a workspace name field, an icon upload (base64 via `arrayBufferToBase64`), and a folder list. The first folder is the **main** folder (shown with a "main" tag); a "+ Add folder" button appends more string-input folder rows. Create POSTs to `/api/workspaces` and pushes the returned workspace into the module-level `workspaces` array.
- Wired in `js/views/home.js`: the `.workspace-dropdown-trigger` (the "Select workspace" button, top-left workspace avatar) opens the dropdown; "+ Add a workspace" opens the modal. Cleanup closes the dropdown on unmount.
- Icon images render via `GET /api/workspaces/{id}/icon` when `iconExt` is set.

## Base UI shell (landing page)
- `js/views/home.js` renders the app shell: a `.app-shell` grid with three regions — `[data-region="left-nav"]`, `[data-region="main"]`, `[data-region="terminal"]`.
- **Left nav** (`.left-nav`, 72px rail): workspace avatar at top (the "Select workspace" trigger), view buttons (Chat/Kanban) below, and actions (Code/Tasks/Git/Dashboard/Settings) pushed to bottom via `margin-top:auto`.
- **Main**: `.work-panel.chat-panel` containing `.chat-log` + `.chat-composer`.
- **Terminal dock** (`.terminal-dock`): 34px collapsed bar; `.is-open` expands via `--terminal-dock-height` (default 280px).
- **Mobile** (≤720px): `.left-nav` hidden, `.mobile-bottom-nav` shown.

## Design tokens
- Colors/spacing/text/shadows are defined in `web/css/app.css` `:root` (light) and `@media (prefers-color-scheme: dark)` (dark). Use tokens like `--color-bg`, `--color-surface`, `--color-border`, `--color-accent`, `--space-*`, `--text-*` instead of hardcoding values.
- Dropdowns use `.model-dropdown`/`.workspace-dropdown` (fixed, z-index 1000); modals use `.modal-backdrop`/`.modal` (z-index 1100, `--shadow-modal`). Primary buttons use `.primary-button`; secondary use `.secondary-button`.

## Verification
- `go build ./...` and `go test ./...` (run with `-race -count=N` to catch the WS register race).
- `node --check web/js/*.js web/js/views/*.js` for JS syntax.
- Manual smoke: `go run .` then `curl http://localhost:3740/api/health`, `/` (index), `/some/route` (SPA fallback), `/api/echo?message=x`, `GET/PUT /api/settings`, `GET/POST /api/workspaces`.
- go.mod is intentionally minimal (only `github.com/gorilla/websocket`). Do not re-add Wails/Labstack Echo.
