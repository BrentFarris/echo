---
name: browser-spa-framework
description: 'Conventions and architecture for the browser-based Echo web app: Go stdlib server on port 3740 serving a plain-JS ES-module SPA, JSON API envelope, WebSocket hub, and the base UI shell (left nav + chat + terminal dock) with its design tokens.'
triggers:
    - Echo web server
    - SPA frontend
    - add API endpoint
    - WebSocket event
    - new view module
    - JSON envelope
    - port 3740
    - base UI shell
    - left nav
    - terminal dock
    - design tokens
---

# Echo Browser SPA Framework

Echo is now a browser-based app (not Wails). A Go server hosts a single-page application and a JSON/WebSocket API. Old Wails code lives in `echo/OLD` and should not be referenced for new work.

## Backend (Go, stdlib net/http)
- Entry point: `echo/main.go`. Default port `3740` (`-port` flag), web root `-web web`.
- `internal/server` package:
  - `server.go` — `Server` struct, `New(addr, webDir)`, `routes()`, `ListenAndServe()`, `Shutdown()`. Uses Go 1.22+ method-pattern `ServeMux` (e.g. `GET /api/health`).
  - `api.go` — JSON helpers `writeJSON`, `writeData`, `writeError`, plus handlers.
  - `ws.go` — WebSocket `Hub` (gorilla/websocket).
- **JSON envelope**: every endpoint returns `{"ok":true,"data":...}` on success or `{"ok":false,"error":"..."}` on failure. New endpoints must follow this.
- **SPA fallback**: unknown non-`/api` non-`/ws` paths that are not real files serve `index.html` so client-side routing works. Implemented in `routes()` via `isStaticAsset` + `serveIndex`.

## WebSocket hub
- `Hub` owns a `clients map[*client]struct{}` guarded by `sync.RWMutex`, plus `register`/`unregister`/`shutdown` channels and a `run()` event loop.
- **Race avoidance**: on register, the hub queues the `welcome` event directly to the client *inside* the `register` case after inserting into the map. Do NOT broadcast from the HTTP handler immediately after `hub.register <- c` — that races (broadcast can run before the client is in the map) and the client never gets its welcome.
- `Hub.Broadcast(event)` marshals to JSON and fans out to all clients; safe from any goroutine.
- Each connection runs a `writePump` goroutine (drains `c.send`, sends pings) and a blocking `readPump` (detects disconnect, unregisters).

## Frontend (plain JavaScript, no build step)
- Served directly from `echo/web` (`index.html`, `css/app.css`, `js/...`). ES modules via `<script type="module">` — no bundler.
- `js/api.js` — `api(path, {query, body})` fetch wrapper returning the `data` field; throws on non-ok. Helpers `get/post/put/del`.
- `js/ws.js` — WebSocket client with auto-reconnect + backoff. `on(type, handler)` dispatches messages by `type`; `onState(handler)` for connect state; `start()`/`stop()`.
- `js/icons.js` — inline stroke-based SVG icon set (24x24 viewBox, `currentColor`), matching OLD `icons.ts`. Reuse these rather than adding icon libraries.
- `js/app.js` — hash router mapping routes to lazy-loaded view modules (`import("./views/home.js")`); swaps them into `#app`.
- **View contract**: each view module exports `mount(root)` and `unmount()`. `mount` renders HTML, wires listeners, and stores a cleanup closure; `unmount` runs it. Keep `unmount` idempotent.

## Base UI shell (landing page)
- `js/views/home.js` renders the app shell: a `.app-shell` grid with three regions — `[data-region="left-nav"]`, `[data-region="main"]`, `[data-region="terminal"]`.
- **Left nav** (`.left-nav`, 72px rail): workspace avatar at top, view buttons (Chat/Kanban) below, and actions (Code/Tasks/Git/Dashboard/Settings) pushed to bottom via `margin-top:auto`. Active view gets `.is-active`.
- **Main**: `.work-panel.chat-panel` containing `.chat-log` (scrollable) + `.chat-composer` (contenteditable `[data-chat-input]` with placeholder, toolbar with attach/mic/model/mode/execute/more, and `.send-button`).
- **Terminal dock** (`.terminal-dock`): 34px collapsed bar with `.terminal-toolbar` (title + status + actions). `.is-open` expands via `--terminal-dock-height` (default 280px).
- **Mobile** (≤720px): `.left-nav` hidden, `.mobile-bottom-nav` shown, shell becomes single column.

## Design tokens
- Colors/spacing/text/shadows are defined in `web/css/app.css` `:root` (light) and `@media (prefers-color-scheme: dark)` (dark), copied from OLD. Use tokens like `--color-bg`, `--color-surface`, `--color-border`, `--color-accent`, `--space-*`, `--text-*` instead of hardcoding values.
- The base UI is intentionally non-functional (visual shell only). Sidebar buttons, terminal toggle, and composer are static.

## Verification
- `go build ./...` and `go test ./...` (run with `-race -count=N` to catch the WS register race).
- `node --check web/js/*.js web/js/views/*.js` for JS syntax.
- Manual smoke: `go run .` then `curl http://localhost:3740/api/health`, `/` (index), `/some/route` (SPA fallback), `/api/echo?message=x`.
- go.mod is intentionally minimal (only `github.com/gorilla/websocket`). Do not re-add Wails/Labstack Echo.
