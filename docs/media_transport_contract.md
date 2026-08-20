# Phase 0 — Transport Contract for Tool-Generated Media (image/video) in Chat

Status: **implemented** (Phase 0 steps 1–6 complete; manual checklist pending).
Branch: `jjtw87_web` (browser SPA). Source of truth for the parity goal: `origin/jjtw87` (Wails app).

## Phase 1 — Video generation (ported from `origin/jjtw87`)

Status: **implemented** (2026-08-20); live-server verification pending. Scope per the
contract's "Non-goals / deferred" list: video generation tooling,
`ComfyuiVideoWorkflow`, workflow video template vars, text-only LLM video messages,
and `stripMediaContentParts` history hygiene.

## Phase 3 — Collapsible media chips/headers (ported from `origin/jjtw87`)

Status: **implemented** (2026-08-20), frontend-only; no transport changes. Ported from
the Wails app (`frontend/src/app/chat/index.ts` + `actions.ts` + `styles.css`).

Adaptations for this SPA (vs. the Wails implementation):
- Expand state stays runtime-only per message key (`collapseMediaState` Set in
  `web/js/chat.js`): turn IDs for user zones, `<turnId>-assistant` for the
  assistant zone (independent expand state per direction). Not persisted, so
  reloads restore the default-collapsed state — same as the Wails app.
- Zones are DOM-built (not HTML-string patched): `createMediaZone(kind, ownerKey, ...)`
  renders either the collapsed chip or the expanded bar + `.chat-message-media-gallery`;
  toggling re-renders the zone in place from its own figures (figures carry
  `data-attachment-id/-media-type/-bytes` so no external lookup is needed).
- User uploads render through the same collapsible zone as tool-generated media
  (`appendUserMedia` now delegates to `createMediaZone("user", ...)`) — previously they
  were an always-expanded flat grid. One zone per user message, one per assistant
  message (kept at the existing mount point after `.chat-final-content`).
- No signature-based patch skipping: this SPA has no streaming media-patch path
  (media only arrives on `tool_result`/restore, each a single rebuild), so the
  Wails `data-media-signature` optimization was dropped.
- CSS: `.chat-media-chip*` / `.chat-message-media-bar` / `.chat-message-media-gallery`
  rules added in `app.css` using existing design tokens; the old flat-grid
  `.chat-message-media` layout moved onto the gallery element. Chip uses the new
  `icons.eye` plus the existing `icons.image`/`icons.video`/`icons.collapse`.

What landed on this branch:
- `internal/tools/comfyui_generate_video.go` — `comfyui_generate_video` tool (prompt +
  frames/fps/format/duration/aspectRatio/megapixels, workflowJSON/workflowPath or the
  configured default video workflow, imagePath/attachedImageIndex img2vid input,
  `{{FRAMES}}/{{FPS}}/{{FORMAT}}/{{DURATION}}/{{ASPECT_RATIO}}/{{MEGAPIXELS}}` template
  substitution, first-video fetch via `/view` with ComfyUI-reported storage type,
  `LLMVideoContentProvider` + `VideoIDProvider` output). Unlike `comfyui_generate`,
  an unset ComfyUI URL is a hard error (`missing_comfyui_url`).
- `internal/tools/save_video.go` — `save_video` resolves `videoId` from the turn-local
  `GeneratedVideos` registry and writes the decoded payload into the workspace
  (recorded as a file change like `save_image`).
- Server wiring (`internal/server/chat_sessions.go`): per-turn `generatedImages` /
  `generatedVideos` maps are populated by `trackGeneratedMediaLocked` right after media
  extraction and passed through `toolContext(...)`, so `save_image`/`save_video` can
  resolve IDs within the same turn. Vision-endpoint re-routing now requires actual image
  content parts (`hasImageMedia`) because video tool results are text-only in context.
- Text-only LLM video messages: `toolResultVideoMessage` no longer embeds the base64
  data URL (videos would blow the context window); the UI renders them from the
  structured `videos[]` attachments instead. `sanitizeMessages` uses the explicit
  `stripMediaContentParts` helper (keeps Role/Content/ToolCallID/Name/ToolCalls).
- System prompt gains `comfyui_generate_video` guidance when the mode allows the tool.
- Settings UI exposes the default video workflow path (`comfyuiVideoWorkflow`).
- Tests: ported unit suites (`comfyui_generate_video_test.go`, `save_video_test.go`),
  `TestChatToolVideoTransportEmitsAndPersists` WS integration test (event → transcript →
  snapshot restore → text-only follow-up request), plus tracking/hygiene unit tests in
  `chat_tool_media_test.go`.

Caps note (from D3): the per-sub-turn cap stays at 8; revisit once real video sizes land.

Implementation notes vs. proposal:
- Cap landed as **per assistant sub-turn** (`maxAssistantTurnMedia = 8`, applied against
  `step.Images/Videos` counts) rather than per tool result — each provider yields at most
  one artifact today, so a per-result cap could never bind; the turn-level budget bounds
  repeated generation calls within one batch and is unit-testable.
- Names go through `safeChatMediaName` (basename + control-char strip), same rules as
  user uploads, falling back to `generated-image`/`generated-video`.

## Goal

Define the wire + storage contract that lets media produced by agent tools
(`comfyui_generate` today; `comfyui_generate_video`, `save_*`, others tomorrow) reach the
browser chat UI, stream incrementally during a turn, and survive reload/reconnect —
without parsing private tool-result JSON in the frontend.

This phase is **transport only**. It does not add video generation (Phase 1) and does not
add the collapsible media headers (Phase 3). Media arrives rendered as the existing
expanded `<figure>` gallery markup.

## Decisions (with rationale)

### D1. Ride the existing `tool_result` session event — no new event type

The `tool_result` event already correlates 1:1 with a tool call
(`turnId`, `turn`, `callId`, `callOrder`) and the SPA already dispatches it in
`applyEvent()` (`web/js/chat.js`). A separate `media_attachment` event would introduce
ordering concerns relative to `tool_result` and a second correlation path for zero benefit.

New optional fields on the event payload (camelCase, matching the rest of the protocol):

```jsonc
{
  "type": "tool_result",
  "turnId": "...", "turn": 2, "callId": "...", "callOrder": 3,
  "tool": "comfyui_generate",
  "success": true,
  "content": "{...raw tool result json...}",
  // NEW — omitted entirely when the tool produced no media (omitempty semantics):
  "images": [ /* MediaAttachment */ ],
  "videos": [ /* MediaAttachment */ ]
}
```

Same key names and same object shape as the user-attachment fields on `turn_started`
(`internal/server/chat_sessions.go` ~line 409), so the frontend has exactly one
attachment shape for both directions.

### D2. Attachment object = `sessions.MediaAttachment`, verbatim

```go
// internal/sessions/store.go
type MediaAttachment struct {
    ID        string `json:"id"`
    Name      string `json:"name"`
    MediaType string `json:"mediaType"`
    Bytes     int64  `json:"bytes"`
    DataURL   string `json:"dataUrl"`
}
```

- `id`: server-generated via `newSessionID("gen-img")` / `newSessionID("gen-vid")`.
  Deliberately **not** the ComfyUI `imageId`/`videoId` (those are `save_image`/`save_video`
  registry keys and are meaningless outside the comfyui tools).
- `name`: filename reported by the tool output (falls back to the provider label, then
  `"generated-image"` / `"generated-video"`).
- `mediaType`: MIME from the provider (`image/png`, `video/mp4`, …).
- `bytes`: decoded byte length (must match the data URL payload; enforced by construction).
- `dataUrl`: full `data:<mime>;base64,...` string.

### D3. Extraction is provider-driven, not tool-name-driven

In the tool-execution path (`chat_sessions.go` ~line 1718, right after
`toolResultImageMessage`/`toolResultVideoMessage` are consulted), extract media generically:

```go
func extractToolMedia(result tools.ExecutionResult) (images, videos []sessions.MediaAttachment)
```

Rules:
- Only when `result.Success && result.Output != nil`.
- `tools.LLMImageContentProvider` → image attachment(s); `tools.LLMVideoContentProvider` →
  video attachment(s). Both interfaces may be implemented by one output; handle independently.
- Skip empty `DataURL` (provider says nothing usable).
- Caps (defense, revisit in Phase 1 once real video sizes land):
  - max **8** attachments per tool result total; drop extras and log a warning.
  - no per-file size cap in Phase 0 (uploads already allow 20 MB raw per message);
    flag oversized payloads (>20 MB) in logs only.
- Never mutates `result`; pure read.

The existing LLM-context synthesis (`toolResultImageMessage` / `toolResultVideoMessage`,
~lines 2051/2078) is left untouched in this phase — its refactor (including making the
video message text-only and stripping media parts from persisted history) belongs to
Phase 1. Today `sanitizeMessages` already drops `ContentParts` before persisting, so no
regression risk.

### D4. Persistence: media lives on `AssistantTurn`

Mirror how user attachments are stored on `Turn` (`Turn.Images`/`Turn.Videos` hold the
single copy of each potentially large payload; LLM messages are rehydrated separately):

```go
// internal/sessions/store.go — additive, backward compatible
type AssistantTurn struct {
    Number       int            `json:"number"`
    Content      string         `json:"content,omitempty"`
    Reasoning    string         `json:"reasoning,omitempty"`
    HasToolCalls bool           `json:"hasToolCalls,omitempty"`
    Tools        []ToolActivity `json:"tools,omitempty"`
    Images       []MediaAttachment `json:"images,omitempty"` // NEW
    Videos       []MediaAttachment `json:"videos,omitempty"` // NEW
}
```

Write site: the same locked section that records the tool result into
`s.active.AssistantTurns[last]` (~line 1732) appends the extracted attachments to
`step.Images` / `step.Videos`. Because `finish()` copies `*s.active` into
`s.transcript.Turns` and persists, and `session_snapshot` serializes `sessions.Turn`
directly, **reload/reconnect restoration falls out for free** — the snapshot carries
assistant media with no extra plumbing.

Trade-off acknowledged: persisted transcript files grow by the media payload size.
This is identical to the existing behavior for *user*-uploaded media on `Turn`, so it is
consistent product behavior, not a new class of bloat. Lazy/metadata-only persistence is a
deliberate non-goal for Phase 0.

### D5. Trajectory

Record media alongside the existing `tool/result` trajectory step (add a `"media"` entry
with counts + names, **not** the data URLs — trajectories are diagnostic, not archives).

### D6. Compatibility matrix

| Scenario | Behavior |
|---|---|
| New server → old client | Extra fields ignored. ✅ |
| Old server → new client | Absent fields ⇒ no media shown (today's behavior). ✅ |
| Pre-existing persisted transcripts | Missing `images`/`videos` on `AssistantTurn` ⇒ `nil` ⇒ `omitempty` ⇒ absent in snapshot ⇒ client treats as `[]`. ✅ |
| Edit/rerun/prune flows | Rewind destroys views; rebuild reads persisted `AssistantTurn` incl. media. Automatic. ✅ |
| Code-surface chats | Surface-agnostic; same pipeline. ✅ |

## Frontend contract (`web/js/chat.js`)

1. **Shared figure builder.** Extract the `<figure class="chat-message-media-item">`
   construction out of `appendUserMedia()` into `buildMediaFigure(attachment, kind)` so
   user uploads and tool-generated media render identically (img vs video element,
   caption with name + `mediaType · size`).

2. **One media zone per turn view.** Lazily create a single
   `<div class="chat-message-media" data-media-zone>` **inside the assistant message
   element, after `.chat-final-content`**, on first media arrival. One zone per turn
   regardless of how many assistant sub-turns/tools contribute — this keeps the
   `promoteFinalText()` cleanup (which strips progress blocks from the sub-turn element)
   from ever touching media, and matches `jjtw87`'s one-container-per-message layout.

3. **Streaming ingestion.** In the `tool_result` case of `applyEvent()`, before/alongside
   `completeToolCall()`: collect `event.images`/`event.videos`, de-dupe by `id`, append
   figures to the zone (zone hidden while empty). Arrival order = tool completion order.

4. **Restoration.** `renderStoredTurn()` forwards `assistant.images`/`assistant.videos`
   (from `turn.assistantTurns[*]`) through the same zone-builder path, accumulating
   across sub-turns in `number` order.

5. **State bookkeeping.** Track per-stream `mediaSeen: Set<id>` so a duplicate replay
   (resubscribe races are already prevented by the sequence check, but belt-and-braces)
   cannot double-append.

6. **Collapse UX is OUT OF SCOPE here** — Phase 3 replaces the zone's internals with the
   chip/bar toggle while keeping the same zone element and data attributes, so Phase 3
   touches only rendering, not transport.

## Implementation steps (order matters; each step compiles/tests green)

1. `internal/sessions/store.go` — add `AssistantTurn.Images/Videos`; extend
   `store_test.go` round-trip coverage.
2. New `internal/server/chat_tool_media.go` — `extractToolMedia()` + caps/constants +
   table-driven unit tests (fake providers: image-only, video-only, both, failing
   result, empty dataURL, over-cap).
3. `internal/server/chat_sessions.go` — wire extraction into the tool-result block:
   append to `step.Images/Videos` (locked), attach to `toolResultEvent`, add media
   summary to the `tool/result` trajectory step.
4. Server integration test (pattern: `server_test.go` ~line 781): drive a turn whose tool
   emits image content; assert (a) `tool_result` event carries `images[0].{id,name,mediaType,bytes,dataUrl}`,
   (b) finished transcript's `AssistantTurn.Images` is populated, (c) a fresh subscribe
   snapshot surfaces the same attachments.
5. `web/js/chat.js` — items 1–5 above; keep `appendUserMedia` working via the shared
   builder.
6. CSS audit: verify `.chat-message-media*` rules (app.css ~1087) cover the assistant-side
   placement; add minimal overrides only if the zone sits differently than the user bubble.
7. Manual verification checklist (below).

## Non-goals / deferred

- Video generation tooling, `ComfyuiVideoWorkflow`, workflow video template vars → **Phase 1**.
- Text-only LLM video messages + `stripMediaContentParts` history hygiene → **Phase 1**
  (needs the `services`-layer refactor; independent of this contract).
- ~~Collapsible chip/header UI, per-message expand state~~ → **Phase 3** (implemented 2026-08-20; signature-based patch skipping dropped — this SPA has no streaming media-patch path).
- Metadata-only persistence / on-demand `/view`-style fetch endpoint for big clips → revisit
  after Phase 1 lands real video sizes.
- Resizing/compressing generated images before emit (uploads are normalized; tool outputs
  are passed through as-is in Phase 0).

## Manual verification checklist

1. txt2img via `comfyui_generate` → image figure appears in the assistant message while
   the turn is still streaming (before `turn_finished`).
2. `filesystem_read_image` on a workspace PNG → same treatment (proves provider-driven,
   not comfyui-specific).
3. Reload page mid-conversation (and kill+restart Echo) → media restored from snapshot.
4. Rerun an earlier turn → older media intact; rebuilt turn shows its own media.
5. Failed tool call (bad prompt / missing ComfyUI) → no media zone, error path unchanged.
6. Two consecutive image-producing calls in one turn → two figures, correct order.
7. Old-transcript chat (created pre-change) opens cleanly with no console errors.
8. Mobile viewport: media zone wraps without breaking the composer.

## Open questions (answer before step 3)

1. **Emit-on-`tool_result` confirmed?** (D1) — recommended; alternative is a sibling
   `tool_media` event fired immediately after `tool_result` (cleaner separation, costs an
   extra event + handler).
2. **Persist full base64 on `AssistantTurn`** (D4) — recommended for parity with user
   attachments; alternative is metadata-only + new fetch RPC (larger surface, defers nicely
   but breaks offline/transcript-portability).
3. **Caps** (D3): 8 attachments / no size cap acceptable for Phase 0?
