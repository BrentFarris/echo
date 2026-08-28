---
name: browser-spa-framework
description: 'Architecture/conventions for the Echo browser SPA: Go stdlib server :3740, Vite-built TS+JS SPA (embedded web/dist), JSON envelope, WebSocket hub, chat tool loop, shared appdata echo.json, internal/tools registry, tool-generated media transport (Phase 0/1), and the file-browser image/video/audio preview surface (/fs/media endpoint, preview kind allowlists, media tabs in codeView).'
triggers:
    - Echo web server
    - SPA frontend
    - media preview
    - fs/media endpoint
    - audio preview
    - code editor media tab
    - image video audio preview
---

# Echo Browser SPA Framework

Echo is now a browser-based app (not Wails). A Go server hosts a single-page application and a JSON/WebSocket API. Old Wails code lives in `echo/OLD` and should not be referenced for new work. Parity ports from the Wails branch (`origin/jjtw87`) are surgical: copy individual files/functions, never merge the branches (they diverge heavily — services-layer vs server-package architecture).

## Shared app data file (echo.json) — settings + workspaces
- `internal/appdata` owns the single Echo app data file at `os.UserConfigDir()/Echo/echo.json` (`DefaultStorePath()`). The file is one JSON document: `{"settings": <raw JSON>, "workspaces": [...]}`.
- `appdata.File` keeps `Settings` as `json.RawMessage` so the package stays decoupled from the settings schema. `appdata.Store.Load()` migrates a legacy bare-settings file (no `"settings"` key) by treating the whole document as settings. `Save()` writes to a `.tmp` then renames into place.
- **Both the settings store and the workspace manager share this one file** so they never clobber each other. When adding a new persisted top-level concern, extend `appdata.File` rather than creating a separate file.
- Tests must use `NewWithSettingsPath(addr, webDir, tempPath)` with an isolated temp path so they never touch the real `echo.json`.

## Workspace registration (internal/workspaces)
- `workspaces.Manager` keeps the workspace ID and absolute main-folder locator in shared appdata, while `.echo/workspace.json` is authoritative for the workspace name, folders, and settings. `Create(...)` writes portable config paths and registers an ID-preserving locator in appdata. `Icon.Data` is `[]byte`, so the frontend must send image bytes as a **base64 string**. API endpoints in `internal/server/workspaces_api.go` all use the standard JSON envelope.

## Backend (Go, stdlib net/http)
- Entry point: `echo/main.go`. Default port `3740` (`-port` flag). Production binaries embed `web/dist` via `//go:embed web/dist`; after frontend changes run `npm run build` in `web/` so the embedded bundle picks them up.
- `internal/server` package: `server.go` (Server, New, routes, ListenAndServe/Shutdown, Go 1.22 method-pattern ServeMux), `api.go` (writeJSON/writeData/writeError), `ws.go` (WebSocket Hub).
- **JSON envelope**: every endpoint returns `{"ok":true,"data":...}` or `{"ok":false,"error":"..."}`. New endpoints must follow this. Raw binary streams (e.g. `/fs/media`) bypass the envelope and set `Content-Type` directly.

## Settings persistence
- `internal/settings/store.go` — `Store` loads/saves `llm.Settings` into the shared appdata file, preserving the workspace list. `internal/server/settings_api.go` exposes `GET/PUT /api/settings`. `initLLM()` configures `Server.llm` (a `chatStreamer` interface so tests inject a fake).

## Tool-generated media transport (image/video) — docs/media_transport_contract.md
- Wire: structured `images[]`/`videos[]` ride the existing `tool_result` session event (omitempty); attachment shape = `sessions.MediaAttachment{id,name,mediaType,bytes,dataUrl}`; IDs are `gen-img-*`/`gen-vid-*`.
- Extraction: `extractToolMedia()` in `internal/server/chat_tool_media.go` is provider-driven (`tools.LLMImageContentProvider`/`LLMVideoContentProvider`), capped at `maxAssistantTurnMedia = 8` per assistant sub-turn.
- LLM context hygiene: `toolResultVideoMessage` is **text-only**; `sanitizeMessages` uses `stripMediaContentParts`. Vision-endpoint re-routing requires actual image parts (`hasImageMedia`).
- Video tools: `comfyui_generate_video` (hard-errors `missing_comfyui_url` when unset) and `save_video`. Template vars include FRAMES/FPS/FORMAT/DURATION/ASPECT_RATIO/MEGAPIXELS.
- Frontend: `buildMediaFigure(attachment)` renders img vs `<video>` by MIME prefix; one lazily-created media zone per assistant message.

## File-browser media preview (images/video/audio in Echo Code)
Clicking an image, video, or audio file in the Explorer opens a read-only preview tab instead of the Monaco editor:
- Extension allowlist lives in two mirrored places that must stay in sync: `workspacefs.MediaTypeForName`/`Previewable` (Go, enforced when streaming) and `web/src/code/preview.ts` (`previewKindForPath`, used by the tree-click path). Images: png/jpg/jpeg/gif/webp/svg/bmp/ico/avif; videos: mp4/m4v/webm/ogv; audio: mp3/wav/ogg/oga/opus/flac/m4a/aac/weba. Case-insensitive on extension only (filenames themselves stay case-sensitive).
- `PreviewKind` on the frontend is `"image" | "video" | "audio"`. The audio file icon / error icon and tab icon use the `music` codicon; video uses `play-circle`; images use `file-media` (also mirrored in `codeView.fileIcon()` for tree icons).
- Endpoint: `GET /api/workspaces/{id}/fs/media?rootId=&path=` (`handleFSMedia` in `workspacefs_api.go`). It resolves through the same confined `resolve()` as every fs endpoint, stats the file, caps at `MaxMediaBytes` (500 MiB): oversized images stream their first chunk via `io.CopyN` (browser fails decode gracefully), oversized videos AND audio are refused 413 since partial media doesn't play. Errors map through `writeWorkspaceFSError` with `unsupported_preview` → 415. Response sets `Content-Type` from the mapped media type and `Cache-Control: no-store`; the client also appends a `&v=<Date.now()>` cache-buster on activation.
- `workspacefs.Service.MediaMeta(workspaceID, ref)` returns resolved host path, size, media type, truncated flag — reuse it rather than re-statting in handlers.
- Frontend (`codeView.ts`): `OpenTab.kind` gained `"media"`; media tabs carry a placeholder Monaco model (`echo-media:` URI) so tab machinery works unchanged. `openFile` routes previewable refs to `openMedia`; `renderMediaPreview` renders `<img>`, `<video controls>`, or `<audio controls preload="metadata">`. All three elements fire `error` on any fetch/stream failure, surfacing an inline explanation panel. `saveTab` on a media tab is a no-op toast; `reloadCleanTab` skips media tabs.
- CSS: `.code-media-preview` / `.code-media-frame` / `.code-media-error` in `web/src/code/code.css` (absolute inset-0 siblings of the monaco hosts). `.code-media-frame-audio audio` gives the player a compact centered strip width.

## WebSocket hub
- `Hub` owns a `clients map[*client]struct{}` guarded by `sync.RWMutex`, plus `register`/`unregister`/`shutdown` channels. **Race avoidance**: on register, the hub queues the `welcome` event to the client *inside* the `register` case after inserting into the map — do NOT broadcast from the HTTP handler immediately after `hub.register <- c`.
- `Hub.Broadcast(event)` marshals and fans out to all clients. Each connection runs a `writePump` goroutine and a blocking `readPump`.

## Chat streaming + tool calling (WebSocket, bidirectional)
- client -> `{type:"chat", message}`; server -> `chat_start`, `chat_event` (token/reasoning/tool_call/tool_result/...), `chat_done`/`chat_error`. Handled in `ws.go` via `client.handleChatMessage`.
- **Tool-calling loop** (`chatSessions.run` in `chat_sessions.go`): appends the user message, streams an assistant turn via `collectAssistantTurn`, and if the turn produced tool calls, executes each via `tools.Execute(s.toolContext(...), name, args)`, appends `RoleTool` messages, and repeats. Keep large payloads OUT of the growing `messages` slice (see media hygiene above).
- **Testability**: `Server.llm` is a `chatStreamer` interface (`StreamChat(ctx, req) *llm.Stream`). Use this pattern for any new LLM-backed endpoint.

## internal/tools package (tool registry + execution)
- `internal/tools` mirrors the legacy OLD tool framework trimmed to the minimal core. Each tool self-registers via `init()` calling `tools.Register(ToolFunc{Meta: Metadata{...}, Run: handler})`.
- Files: `registry.go`, `types.go` (Tool/ToolFunc/Metadata/Schema/ExecutionContext/WorkspaceRoot/AttachedImage/AttachedVideo/ImageIDProvider/VideoIDProvider/ExecutionResult/SafeError), `arguments.go` (`DecodeToolArguments` with JSON-repair pass), `filesystem_helpers.go`, and one tool per file.
- **Labeled workspace roots**: `ExecutionContext.WorkspaceRoots []WorkspaceRoot{ID,Label,Path}`. `server.workspaceToolRoots(workspace)` derives each label from the folder's base name via `normalizeWorkspaceFolderLabel`. A single folder still gets a label (not "."); use the label path (e.g. `path:"echo"`) to list a folder's contents.
- **To port a tool from origin/jjtw87**: copy its `init()` registration verbatim plus its `Run` handler and any helpers. Port the `_test.go` too. Watch for behavioral drift (e.g. comfyui URL fallback) that differs here.

## LLM client (internal/llm)
- Raw OpenAI-compatible client. `types.go` (Message/ChatRequest/StreamEvent; `Message.Content` is a plain `string`; `ImageURLContentPart`/`VideoURLContentPart` builders), `settings.go` (Settings/LLMEndpoint/EndpointSelection, `ForInteraction()`), `client.go` (`NewClient`, `Complete`, `StreamChat`, `Cancel`), `stream.go` (SSE parsing).
- `chatCompletionsURL(endpoint)` appends `/chat/completions` unless already present. Streaming uses no total request timeout.

## Frontend
- Newer views are TypeScript under `web/src` built by Vite (`npm run check|test|build` in `web/`; output `web/dist` is embedded into the Go binary). `web/js/*` remains hand-written JS imported from the TS sources.
- `js/api.js` — `api(path, {query, body})` fetch wrapper returning the `data` field; helpers `get/post/put/del`. `js/ws.js` — WebSocket client with auto-reconnect + backoff. `js/icons.js` — inline SVG icon set; the code view uses codicons from `@vscode/codicons`.
- `js/app.js` — hash router mapping routes to lazy-loaded view modules. Each view module exports `mount(root)` and `unmount()`; keep `unmount` idempotent. `js/chat.js` renders `chat_event` types.

## Workspace selector + add-workspace modal (js/workspaces.js, js/views/home.js)
- `loadWorkspaces()` fetches `GET /api/workspaces`; `openWorkspaceDropdown(trigger, {onSelect, onAdd})` renders a fixed-position dropdown. `openAddWorkspaceModal({onCreate})` renders and wires the modal.
- The modal has a workspace name field, an icon upload (base64 via `arrayBufferToBase64`), and a folder list (first folder is the **main** folder). Create POSTs to `/api/workspaces` and pushes the returned workspace into the module-level `workspaces` array.
- Icon images render via `GET /api/workspaces/{id}/icon` when `iconExt` is set.

## Base UI shell (landing page)
- `js/views/home.js` renders the app shell: a `.app-shell` grid with three regions — `[data-region="left-nav"]`, `[data-region="main"]`, `[data-region="terminal"]`.
- **Left nav** (`.left-nav`, 72px rail): workspace avatar, view buttons (Chat/Kanban), actions (Code/Tasks/Git/Dashboard/Settings). **Main**: `.work-panel.chat-panel` with `.chat-log` + `.chat-composer`. **Terminal dock** (`.terminal-dock`): 34px collapsed bar; `.is-open` expands via `--terminal-dock-height`. **Mobile** (≤720px): `.left-nav` hidden, `.mobile-bottom-nav` shown.

## Design tokens
- Defined in `web/css/app.css` `:root` (light) and `@media (prefers-color-scheme: dark)` (dark). Use tokens like `--color-bg`, `--color-surface`, `--color-border`, `--color-accent`, `--space-*`, `--text-*`.
- Dropdowns use `.model-dropdown`/`.workspace-dropdown` (fixed, z-index 1000); modals use `.modal-backdrop`/`.modal` (z-index 1100). The code view defines its own `--code-*` tokens at the top of `web/src/code/code.css`.

## Verification
- `go build ./...` and `go test ./...`. Known pre-existing failures unrelated to recent work: `workspacefs.TestRevealCommandKeepsPathInOneArgument` (shell-quoting of semicolons in filenames on Linux) and `web/src/chatProtocol.test.ts` "sends attachment-only media..." (flaky/unstable around the chat media-zone assertions).
- `cd web && npm run check` (tsc --noEmit), `npx vitest run`, and `npm run build` (emits `web/dist` which `main.go` embeds — rebuild after any frontend change or use the in-app rebuild-relaunch).
- Manual smoke: `go run .` then `curl http://localhost:3740/api/health`, `/` (index), `/some/route` (SPA fallback), `GET/PUT /api/settings`, `GET/POST /api/workspaces`, `GET /api/workspaces/{id}/fs/media?rootId=&path=img.png` (expect raw bytes + Content-Type).
- go.mod is intentionally minimal (plus terminal/image deps). Do not re-add Wails/Labstack Echo.
