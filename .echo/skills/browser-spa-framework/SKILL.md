---
name: browser-spa-framework
description: 'Conventions and architecture for the browser-based Echo web app: Go stdlib server on port 3740 serving a plain-JS ES-module SPA, JSON API envelope, WebSocket hub, the base UI shell (left nav + chat + terminal dock) with design tokens, the internal/llm OpenAI-compatible client, and the bidirectional WebSocket chat streaming path (including reasoning-model handling).'
triggers:
    - Echo web server
    - SPA frontend
    - add API endpoint
    - WebSocket event
    - new view module
    - JSON envelope
    - port 3740
    - base UI shell
    - LLM client
    - chat completions
    - OpenAI compatible
    - streaming
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
- **Inbound messages**: `readPump` decodes `{type, ...}` and dispatches. `client.sendJSON(v)` queues JSON to one client safely from any goroutine.

## Chat streaming (WebSocket, bidirectional)
- Chat flows entirely over the WebSocket channel (no HTTP polling, no SSE endpoint):
  - client -> `{type:"chat", message:"..."}`
  - server -> `{type:"chat_start", message}`
  - server -> `{type:"chat_event", eventType:"token"|"reasoning"|..., content, finishReason, error}`
  - server -> `{type:"chat_done"}` or `{type:"chat_error", error}`
- Server side: `client.handleChatMessage` builds an `llm.NewChatRequest(..., llm.WithStream(true))`, calls `StreamChat`, and relays each `StreamEvent` back to the originating client. Handled in `ws.go`.
- **Testability**: `Server.llm` is a `chatStreamer` interface (`StreamChat(ctx, req) *llm.Stream`) so tests inject a fake streamer instead of hitting a real LLM. Use this pattern for any new LLM-backed endpoint.
- **Hard-coded endpoint**: `server.go` has `llmEndpoint`/`llmModel` consts (currently `http://192.168.50.178:8023/v1` / `deepseek-ai/DeepSeek-V4-Flash-0731`) wired into `initLLM()`. These are TEMPORARY and must be replaced by user-configurable settings in a later step — do not treat them as permanent.

## LLM client (`internal/llm`)
- Ported from OLD Wails code. Raw OpenAI-compatible chat completions client.
- `types.go` — `Message` (string or content-parts content), `ChatRequest`, `ChatResponse`, `Tool`/`ToolCall`, `StreamEvent`/`EventType`. Custom `Message.MarshalJSON` emits `content` as a string or an array of parts; assistant messages always carry explicit (possibly empty) content.
- `settings.go` — `Settings`/`LLMEndpoint`/`EndpointSelection`, `DefaultSettings()`, `Normalized()`, `Validate()`, `ForInteraction()`. Default endpoint `http://localhost:11434/v1`, model `Qwen3.6-35B-A3B`. The searxng default URL is inlined as a constant (no searxng dependency).
- `client.go` — `NewClient(settings, opts...)`, `Complete(ctx, req)` (non-streaming), `StreamChat(ctx, req) *Stream`, `Cancel(streamID)`, plus in-memory conversation storage. Options: `WithAPIKey`, `WithHTTPClient`, `WithLogger`. Uses stdlib `slog` (flowlog was dropped).
- `stream.go` — SSE parsing. Emits `token`, `reasoning` (from `reasoning_content`/`reasoning`/`thinking_content`/`thinking`), `tool_call`, `complete`, `usage`, `error`, `canceled` events. `[DONE]` and `finish_reason` both emit `EventComplete`.
- `chatCompletionsURL(endpoint)` appends `/chat/completions` unless already present.
- Streaming does NOT use a total request timeout (only `Complete` does); cancellation is via context.

## Frontend (plain JavaScript, no build step)
- Served directly from `echo/web` (`index.html`, `css/app.css`, `js/...`). ES modules via `<script type="module">` — no bundler.
- `js/api.js` — `api(path, {query, body})` fetch wrapper returning the `data` field; throws on non-ok. Helpers `get/post/put/del`.
- `js/ws.js` — WebSocket client with auto-reconnect + backoff. `on(type, handler)` dispatches messages by `type`; `onState(handler)` for connect state; `send(data)` sends JSON (drops if not open); `start()`/`stop()`.
- `js/icons.js` — inline stroke-based SVG icon set (24x24 viewBox, `currentColor`), matching OLD `icons.ts`. Reuse these rather than adding icon libraries.
- `js/app.js` — hash router mapping routes to lazy-loaded view modules (`import("./views/home.js")`); swaps them into `#app`.
- **View contract**: each view module exports `mount(root)` and `unmount()`. `mount` renders HTML, wires listeners, and stores a cleanup closure (module-level var); `unmount` runs it. Keep `unmount` idempotent.

## Chat view (streaming, fluid)
- `js/chat.js` — chat logic. `sendMessage(log, text)` renders the user message, creates an assistant message element once, then mutates its `.chat-message-content` text on each `chat_event` token — **no DOM recreation during streaming**. `finishStream` unsubscribes and removes the `.is-streaming` class.
- **Reasoning models**: the configured endpoint is a reasoning model that streams `reasoning` events for several seconds BEFORE any `token` events. The chat view MUST handle `reasoning` events or the answer area appears empty with only a blinking caret. Render reasoning into a separate `.chat-message-reasoning` block (hidden until it has content, distinct styling via `.chat-message-reasoning`), and stream `token` events into the main `.chat-message-content`. Never concatenate reasoning into the answer.
- `js/views/home.js` — wires the composer (Enter to send, Shift+Enter newline) to `sendMessage`. The `.send-button` submits; the input is a contenteditable `[data-chat-input]`.
- CSS: `.chat-message`, `.chat-message-user` (accent bubble right-aligned), `.chat-message-assistant`, `.chat-message-reasoning`, `.is-streaming` caret animation, `.chat-empty` placeholder removed on first message.

## Base UI shell (landing page)
- `js/views/home.js` renders the app shell: a `.app-shell` grid with three regions — `[data-region="left-nav"]`, `[data-region="main"]`, `[data-region="terminal"]`.
- **Left nav** (`.left-nav`, 72px rail): workspace avatar at top, view buttons (Chat/Kanban) below, and actions (Code/Tasks/Git/Dashboard/Settings) pushed to bottom via `margin-top:auto`. Active view gets `.is-active`.
- **Main**: `.work-panel.chat-panel` containing `.chat-log` (scrollable) + `.chat-composer` (contenteditable `[data-chat-input]` with placeholder, toolbar with attach/mic/model/mode/execute/more, and `.send-button`).
- **Terminal dock** (`.terminal-dock`): 34px collapsed bar with `.terminal-toolbar` (title + status + actions). `.is-open` expands via `--terminal-dock-height` (default 280px).
- **Mobile** (≤720px): `.left-nav` hidden, `.mobile-bottom-nav` shown, shell becomes single column.

## Design tokens
- Colors/spacing/text/shadows are defined in `web/css/app.css` `:root` (light) and `@media (prefers-color-scheme: dark)` (dark), copied from OLD. Use tokens like `--color-bg`, `--color-surface`, `--color-border`, `--color-accent`, `--space-*`, `--text-*` instead of hardcoding values.

## Verification
- `go build ./...` and `go test ./...` (run with `-race -count=N` to catch the WS register race).
- `node --check web/js/*.js web/js/views/*.js` for JS syntax.
- Manual smoke: `go run .` then `curl http://localhost:3740/api/health`, `/` (index), `/some/route` (SPA fallback), `/api/echo?message=x`.
- go.mod is intentionally minimal (only `github.com/gorilla/websocket`). Do not re-add Wails/Labstack Echo.
