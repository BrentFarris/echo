---
name: browser-spa-framework
description: 'Architecture/conventions for the Echo browser SPA: Go stdlib server :3740, Vite-built TS+JS SPA (embedded web/dist), JSON envelope, WebSocket hub, chat tool loop, shared appdata echo.json, internal/tools registry, tool-generated media transport (Phase 0/1), and the file-browser image/video preview surface (/fs/media endpoint, preview kind allowlists, media tabs in codeView).'
triggers:
    - Echo web server
    - SPA frontend
    - add API endpoint
    - appdata echo.json
    - internal/tools
    - comfyui video
    - save_video
    - media transport
    - vision routing
    - file browser image preview
    - fs/media endpoint
    - video preview tab
---

# Echo Browser SPA Framework

Echo is now a browser-based app (not Wails). A Go server hosts a single-page application and a JSON/WebSocket API. Old Wails code lives in `echo/OLD` and should not be referenced for new work. Parity ports from the Wails branch (`origin/jjtw87`) are surgical: copy individual files/functions, never merge the branches (they diverge heavily — services-layer vs server-package architecture).

## Shared app data file (echo.json) — settings + workspaces
- `internal/appdata` owns the single Echo app data file at `os.UserConfigDir()/Echo/echo.json` (`DefaultStorePath()`). The file is one JSON document: `{"settings": <raw JSON>, "workspaces": [...]}`.
- `appdata.File` keeps `Settings` as `json.RawMessage` so the package stays decoupled from the settings schema. `appdata.Store.Load()` migrates a legacy bare-settings file (no `"settings"` key) by treating the whole document as settings. `Save()` writes to a `.tmp` then renames into place.
- **Both the settings store and the workspace manager share this one file** so they never clobber each other. When adding a new persisted top-level concern, extend `appdata.File` rather than creating a separate file.
- Tests must use `NewWithSettingsPath(addr, webDir, tempPath)` with an isolated temp path so they never touch the real `echo.json`.

## Workspace registration (internal/workspaces)
- `workspaces.Manager` keeps the workspace ID and absolute main-folder locator in shared appdata, while `.echo/workspace.json` is authoritative for the workspace name, folders, and settings. `Create(CreateRequest{Name, MainPath, Folders, Icon})`:
  1. Resolves the selected main folder to an absolute path and requires it to be available.
  2. Loads an existing `.echo/workspace.json` or creates a new configuration after validating requested folders.
  3. Resolves config-relative paths against `.echo`, keeps temporarily missing additional folders, and rejects a `mainPath` that does not identify `.echo`'s parent.
  4. Writes portable config paths (`../` for the main folder; relative extras when possible, absolute fallback across volumes).
  5. Copies an uploaded icon to `.echo/icon.<ext>` and registers or ID-preservingly rebinds the absolute locator in appdata.
- `Icon.Data` is `[]byte`, so the frontend must send image bytes as a **base64 string** (Go decodes `[]byte` from base64 JSON). `IconPath(id)` auto-detects the icon extension from `iconExt`, falling back to scanning `icon.*`.
- API endpoints in `internal/server/workspaces_api.go`: `GET /api/workspaces`, `POST /api/workspaces`, `GET /api/workspaces/{id}/icon` (serves the image). All use the standard JSON envelope.

## Backend (Go, stdlib net/http)
- Entry point: `echo/main.go`. Default port `3740` (`-port` flag), web root `-web web`. Production binaries embed `web/dist` via `//go:embed web/dist`; `main.go` serves embedded assets unless `-web` overrides. After frontend changes run `npm run build` in `web/` so the embedded bundle picks them up (or trigger the app's rebuild-relaunch flow which does both builds).
- `internal/server` package:
  - `server.go` — `Server` struct, `New(addr, webDir)`, `routes()`, `ListenAndServe()`, `Shutdown()`. Uses Go 1.22+ method-pattern `ServeMux` (e.g. `GET /api/health`). `Server` holds `store *settings.Store` and `workspaces *workspaces.Manager` (both backed by the same appdata path).
  - `api.go` — JSON helpers `writeJSON`, `writeData`, `writeError`, plus handlers.
  - `ws.go` — WebSocket `Hub` (gorilla/websocket).
- **JSON envelope**: every endpoint returns `{"ok":true,"data":...}` on success or `{"ok":false,"error":"..."}` on failure. New endpoints must follow this. Raw binary streams (e.g. `/fs/media`) bypass the envelope and set `Content-Type` directly.

## Settings persistence
- `internal/settings/store.go` — `Store` loads/saves `llm.Settings` into the shared appdata file, preserving the workspace list. `Load()` returns defaults if missing; `Save()` normalizes (endpoint profiles authoritative) and writes through `appdata.Store`. Note `s.store.Load()` returns `(llm.Settings, error)`.
- `internal/server/settings_api.go` — `GET /api/settings` returns `{settings, storagePath}`; `PUT /api/settings` decodes `{settings}`, calls `Validate()`, `store.Save()`, then `initLLM()`.
- `initLLM()` loads settings and calls `cfg.ForInteraction(llm.InteractionChat)`. `Server.llm` is a `chatStreamer` interface so tests inject a fake.
- Frontend: `js/views/settings.js` calls `get("/api/settings")` on mount and persists external fields live on blur via generic `[data-external-field]` inputs (state.external keys mirror llm.Settings camelCase JSON keys). ComfyUI card exposes comfyuiUrl + txt2img/img2img/**video** workflow paths.

## Tool-generated media transport (image/video) — docs/media_transport_contract.md
Contract doc is the source of truth; Phase 0 (transport) and Phase 1 (video generation) are implemented on this branch. Key wiring:
- Wire: structured `images[]`/`videos[]` ride the existing `tool_result` session event (omitempty); attachment shape = `sessions.MediaAttachment{id,name,mediaType,bytes,dataUrl}`; IDs are `gen-img-*`/`gen-vid-*` (NOT comfyui imageId/videoId).
- Extraction: `extractToolMedia()` in `internal/server/chat_tool_media.go` is provider-driven (`tools.LLMImageContentProvider`/`LLMVideoContentProvider`), capped at `maxAssistantTurnMedia = 8` per assistant sub-turn. Media persists on `AssistantTurn.Images/Videos`; snapshot restore falls out for free.
- Per-turn generated-ID registry: `chatSession.run()` owns `generatedImages`/`generatedVideos` maps; `trackGeneratedMediaLocked()` fills them right after extraction (keyed by `ImageIDProvider.GetImageID()`/`VideoIDProvider.VideoID()`); `toolContext(ctx, scopes, images, videos)` passes them into `tools.ExecutionContext` so `save_image`/`save_video` resolve payloads within the same turn. These maps are NOT reset between turns — they're per-run locals.
- LLM context hygiene: `toolResultVideoMessage` is **text-only** (never embeds base64 video in model context); `sanitizeMessages` uses `stripMediaContentParts` (keeps Role/Content/ToolCallID/Name/ToolCalls). Vision-endpoint re-routing requires actual image parts (`hasImageMedia`) — video-only turns must not flip to the vision endpoint.
- Video tools: `comfyui_generate_video` (hard-errors `missing_comfyui_url` when unset, unlike `comfyui_generate` which falls back to localhost:8188; default workflow setting `ComfyuiVideoWorkflow` is loaded directly off disk, not workspace-resolved) and `save_video` (writes decoded DataURL payload, records file change). Template vars include FRAMES/FPS/FORMAT/DURATION/ASPECT_RATIO/MEGAPIXELS.
- Frontend: `buildMediaFigure(attachment)` renders img vs `<video>` by MIME prefix; one lazily-created media zone per assistant message (`appendToolMedia` dedupes by id via `stream.mediaSeen`).
- Test patterns: unit fakes implement the provider interfaces (`fakeImageOutput`/`fakeVideoOutput` in `chat_tool_media_test.go`); WS end-to-end tests mirror `TestChatToolMediaTransportEmitsAndPersists` / `TestChatToolVideoTransportEmitsAndPersists` (assert tool_result attachments → persisted AssistantTurn → fresh-subscriber snapshot → text-only follow-up request). Fake streamers live in `server_test.go` (`imageToolStreamer`, `videoToolStreamer` for filesystem_read_video, `comfyuiVideoToolStreamer` in the video transport test).

## File-browser media preview (images/video in Echo Code)
Clicking an image/video file in the Explorer opens a read-only preview tab instead of the Monaco editor:
- Extension allowlist lives in two mirrored places that must stay in sync: `workspacefs.MediaTypeForName`/`Previewable` (Go, enforced when streaming) and `web/src/code/preview.ts` (`previewKindForPath`, used by the tree-click path). Images: png/jpg/jpeg/gif/webp/svg/bmp/ico/avif; videos: mp4/m4v/webm/ogv. Case-insensitive on extension only (filenames themselves stay case-sensitive).
- Endpoint: `GET /api/workspaces/{id}/fs/media?rootId=&path=` (`handleFSMedia` in `workspacefs_api.go`). It resolves through the same confined `resolve()` as every fs endpoint (symlink escape → 403), stats the file, caps at `MaxMediaBytes` (500 MiB): oversized images stream their first chunk via `io.CopyN` (browser fails decode gracefully), oversized videos are refused 413 since partial video doesn't play. Errors map through `writeWorkspaceFSError` with the added `unsupported_preview` → 415 code. Response sets `Content-Type` from the mapped media type and `Cache-Control: no-store`; the client also appends a `&v=<Date.now()>` cache-buster on activation.
- `workspacefs.Service.MediaMeta(workspaceID, ref)` returns resolved host path, size, media type, truncated flag — reuse it rather than re-statting in handlers.
- Frontend (`codeView.ts`): `OpenTab.kind` gained `"media"`; media tabs carry a placeholder Monaco model (`echo-media:` URI, retained/released like other models) so tab machinery (view state, persistence via `PersistedTab.kind === "media"`, disposal, dirty prompts) works unchanged. `openFile` routes previewable refs to `openMedia`; `activateTab`/`updateEditorSurface` toggle the `[data-media-preview-host]` div (rendered by `renderMediaPreview` as `<img>` or `<video controls autoplay loop playsinline>`). Both elements fire `error` on any fetch/stream failure, which surfaces an inline explanation panel. `saveTab` on a media tab is a no-op toast; `reloadCleanTab` skips media tabs (live refresh happens naturally on next activation). Tab/tree icons: `file-media` for images, `play-circle` for video.
- CSS: `.code-media-preview` / `.code-media-frame` / `.code-media-error` in `web/src/code/code.css` (absolute inset-0 siblings of the monaco hosts, hidden toggled together).

## WebSocket hub
- `Hub` owns a `clients map[*client]struct{}` guarded by `sync.RWMutex`, plus `register`/`unregister`/`shutdown` channels and a `run()` event loop.
- **Race avoidance**: on register, the hub queues the `welcome` event directly to the client *inside* the `register` case after inserting into the map. Do NOT broadcast from the HTTP handler immediately after `hub.register <- c`.
- `Hub.Broadcast(event)` marshals to JSON and fans out to all clients; safe from any goroutine.
- Each connection runs a `writePump` goroutine and a blocking `readPump`. `client.sendJSON(v)` queues JSON to one client safely.

## Chat streaming + tool calling (WebSocket, bidirectional)
- client -> `{type:"chat", message}`; server -> `chat_start`, `chat_event` (token/reasoning/tool_call/tool_result/...), `chat_done`/`chat_error`. Handled in `ws.go` via `client.handleChatMessage`.
- `handleChatMessage` builds the `ChatRequest` with `llm.WithTools(tools.LLMSchema())`, then calls `client.runChatLoop(...)`.
- **Tool-calling loop** (`chatSessions.run` in `chat_sessions.go`): appends the user message, streams an assistant turn via `collectAssistantTurn` (merges streamed `EventToolCall` deltas by index with `mergeToolDelta`, orders with `orderedToolCalls`), and if the turn produced tool calls, executes each via `tools.Execute(s.toolContext(...), name, args)`, marshals the `ExecutionResult` to JSON, appends `RoleTool` messages (+ optional image/video result messages), and repeats. The loop reuses the same growing `messages` slice every sub-turn, so anything appended there reaches the model again next iteration — keep large payloads OUT of it (see media hygiene above).
- **Testability**: `Server.llm` is a `chatStreamer` interface (`StreamChat(ctx, req) *llm.Stream`). Use this pattern for any new LLM-backed endpoint. Integration tests inject a fake streamer that emits a tool_call delta then a final answer, and assert the tool result was fed back into the follow-up request.

## internal/tools package (tool registry + execution)
- `internal/tools` mirrors the legacy OLD tool framework but trimmed to the minimal core. Each tool self-registers via `init()` calling `tools.Register(ToolFunc{Meta: Metadata{...}, Run: handler})`.
- Files: `registry.go` (`Register`, `defaultRegistry`, `Registered()`, `LLMSchema()`, `Execute()`), `types.go` (`Tool`, `ToolFunc`, `Metadata`, `Schema`, `ExecutionContext`, `WorkspaceRoot`, `AttachedImage`/`AttachedVideo`, `ImageIDProvider`/`VideoIDProvider`, `ExecutionResult`, `SafeError`), `arguments.go` (`DecodeToolArguments` with JSON-repair pass), `filesystem_helpers.go` (workspace path resolution), and one tool per file (e.g. `filesystem_list.go`).
- `tools.LLMSchema()` returns `[]llm.Tool` for the model; `tools.Execute(ctx, name, args)` returns an `ExecutionResult` (recovering panics, mapping `SafeError` to `ExecutionError{Code,Message}`).
- **Labeled workspace roots**: `ExecutionContext.WorkspaceRoots []WorkspaceRoot{ID,Label,Path}`. `server.workspaceToolRoots(workspace)` (in `chat_tools.go`) derives each label from the folder's base name via `normalizeWorkspaceFolderLabel` (lowercase slug, spaces->dashes), matching the legacy convention. A single folder still gets a label (not "."), so `filesystem_list` with `path:"."` lists the virtual roots; use the label path (e.g. `path:"echo"`) to list a folder's contents.
- **To port a tool from origin/jjtw87**: copy its `init()` registration verbatim (same name/description/params) plus its `Run` handler and any helpers it needs from OLD's `internal/tools` (types/arguments/filesystem_helpers). Only port the subset of infrastructure the tool actually uses. Port the `_test.go` too (rename colliding fixtures — e.g. `videoToolStreamer` existed for filesystem_read_video before the video transport test added `comfyuiVideoToolStreamer`). Watch for behavioral drift: source-branch tools may rely on defaults (e.g. comfyui URL fallback) that differ here. Add a `_test.go` porting the OLD tool's tests.

## LLM client (internal/llm)
- Raw OpenAI-compatible client. `types.go` (Message/ChatRequest/StreamEvent; `Message.Content` is a plain `string`; `ImageURLContentPart`/`VideoURLContentPart` builders), `settings.go` (Settings/LLMEndpoint/EndpointSelection, `ForInteraction()`), `client.go` (`NewClient`, `Complete`, `StreamChat`, `Cancel`), `stream.go` (SSE parsing, emits token/reasoning/tool_call/complete/usage/error/canceled).
- `chatCompletionsURL(endpoint)` appends `/chat/completions` unless already present. Streaming uses no total request timeout.

## Frontend (plain JavaScript, no build step)
- Served directly from `echo/web` (`index.html`, `css/app.css`, `js/...`). ES modules via `<script type="module">` — no bundler.
- NOTE: newer views are TypeScript under `web/src` built by Vite (`npm run check|test|build` in `web/`; output `web/dist` is embedded into the Go binary). `web/js/*` remains hand-written JS imported from the TS sources.
- `js/api.js` — `api(path, {query, body})` fetch wrapper returning the `data` field; throws on non-ok. Helpers `get/post/put/del`.
- `js/ws.js` — WebSocket client with auto-reconnect + backoff.
- `js/icons.js` — inline stroke-based SVG icon set (24x24 viewBox, `currentColor`). Reuse these rather than adding icon libraries. (The code view uses codicons from `@vscode/codicons`.)
- `js/app.js` — hash router mapping routes to lazy-loaded view modules; swaps them into `#app`.
- **View contract**: each view module exports `mount(root)` and `unmount()`. `mount` renders HTML, wires listeners, and stores a cleanup closure (module-level var); `unmount` runs it. Keep `unmount` idempotent.
- `js/chat.js` renders `chat_event` types: `token`/`reasoning` stream text; `tool_call`/`tool_result` render a lightweight `.chat-tool-line` activity line in the assistant message; tool media figures append to the per-message media zone.

## Workspace selector + add-workspace modal (js/workspaces.js)
- `loadWorkspaces()` fetches `GET /api/workspaces`; `openWorkspaceDropdown(trigger, {onSelect, onAdd})` renders a fixed-position dropdown anchored to the trigger (appended to `<body>` to avoid clipping) and returns a cleanup function. `openAddWorkspaceModal({onCreate})` renders and wires the modal, returns cleanup.
- The modal has a workspace name field, an icon upload (base64 via `arrayBufferToBase64`), and a folder list. The first folder is the **main** folder (shown with a "main" tag); "+ Add folder" button appends more string-input folder rows. Create POSTs to `/api/workspaces` and pushes the returned workspace into the module-level `workspaces` array.
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
- The code view defines its own `--code-*` tokens at the top of `web/src/code/code.css` (light + dark variants).

## Verification
- `go build ./...` and `go test ./...` (run with `-race -count=N` to catch the WS register race). Known pre-existing failures unrelated to recent work: `workspacefs.TestRevealCommandKeepsPathInOneArgument` (shell-quoting of semicolons in filenames on Linux) and `web/src/chatProtocol.test.ts` "sends attachment-only media..." (flaky/unstable around the chat media-zone assertions).
- `cd web && npm run check` (tsc --noEmit), `npx vitest run`, and `npm run build` (emits `web/dist` which `main.go` embeds — rebuild after any frontend change or use the in-app rebuild-relaunch).
- Manual smoke: `go run .` then `curl http://localhost:3740/api/health`, `/` (index), `/some/route` (SPA fallback), `/api/echo?message=x`, `GET/PUT /api/settings`, `GET/POST /api/workspaces`, `GET /api/workspaces/{id}/fs/media?rootId=&path=img.png` (expect raw bytes + Content-Type).
- go.mod is intentionally minimal (plus terminal/image deps). Do not re-add Wails/Labstack Echo.
