---
name: chat-video-attachments
description: How chat image/video attachments flow through the composer AND how inline message media renders with a per-message show/hide toggle (hidden-by-default chip vs expanded bar+media), including state, actions, streaming patch behavior, and CSS locations.
triggers:
    - chat video
    - video attachment
    - addPastedChatVideos
    - renderChatVideoDrafts
    - remove-chat-video
    - hide media
    - show/hide chat media
    - collapsed media chip
    - toggle-chat-media
    - chatMediaExpanded
    - renderChatMessageMedia
    - bindActionEvents rebind
---

## Video Attachment Flow in Chat Composer

Video files (mp4, webm, mov) are handled in the chat composer parallel to image attachments.

### Supported Types
- `video/mp4`, `video/webm`, `video/quicktime`
- Constants: `maxChatVideoDrafts = 4`, `maxChatVideoBytes = 50 MB`
- Combined media limit: `maxChatMediaDrafts = 8`

### Entry Points

**Paste (`handleChatPaste` in `index.ts`):** Filters clipboard files by `file.type.startsWith("video/")`, routes to `addPastedChatVideos`. Images and videos can be pasted simultaneously.

**Drag-and-drop (`openDroppedFiles` in `bootstrap.ts`):** When in chat mode, dropped workspace files with extensions `mp4`, `webm`, `mov`, `m4v` are read via `ReadWorkspaceMediaFile(workspaceId, path)` which returns `{ mimeType, dataUrl, bytes }`. Drafts are added directly to `state.chatVideoDrafts` — NOT through File objects (data URLs from the backend are used directly).

**Web upload button (`handleChatVideoUpload` in `index.ts`):** Creates a hidden `<input type="file" accept="video/mp4,video/webm,video/quicktime">`, calls `addPastedChatVideos` on selection. Button is only rendered when `!isWailsRuntime()`.

### Draft State
- `state.chatVideoDrafts: Map<string, ChatVideoDraft[]>` in `state.ts`
- `ChatVideoDraft` type: `{ id, name, mediaType, dataUrl, bytes }`
- Helpers: `chatVideoDraftsFor(workspaceID)`, `chatVideoDraftTotalBytes(workspaceID)`

### Draft Rendering
- `renderChatVideoDrafts(workspaceID, disabled)` renders `.chat-video-drafts > .chat-video-chip` elements with video icon, name, size, and remove button (`data-action="remove-chat-video" data-video-id="..."`)
- CSS in `styles.css`: `.chat-video-drafts`, `.chat-video-chip`, `.chat-video-icon`

### Send Payload
In `handleChatSubmit`, videos are mapped to `services.ChatVideoInput.createFrom(...)` and included as `videos: [...]` on the `ChatMessageRequest`. Both `state.chatImageDrafts` and `state.chatVideoDrafts` are cleared on send. The send button enabled logic checks `imageDrafts.length > 0 || videoDrafts.length > 0`.

## Message Media Display (with show/hide toggle)

`renderChatMessageMedia(message)` in `chat/index.ts` is the single entry point for inline message media and wraps both images and videos:

- **Collapsed (DEFAULT):** `.chat-message-media.is-collapsed` renders only a compact `.chat-media-chip` button with image/video count icons, summary label (like `2 images + 1 video - 3.4 MB`), and eye icon. Clicking it expands. Collapsed = zero video decode/autoplay cost (media removed from DOM).
- **Expanded:** the wrapper contains a `.chat-message-media-bar` header row (summary label + "Hide media" icon button) followed by `renderChatMessageImages(message)` and `renderChatMessageVideos(message)`.
- Expand state: `state.chatMediaExpanded: Set<string>` of message IDs in `state.ts`. Media is hidden by default; a message only renders expanded while its ID is in the set. Runtime-only — intentionally not persisted (AGENTS.md: detail-view tracking stays runtime-only). Stale IDs for pruned messages are harmless.
- Toggle action: `data-action="toggle-chat-media" data-message-id="..."` handled in `actions.ts` — flips the Set entry, then swaps the `.chat-message-media` element in place using `renderChatMessageMedia(message)` (message looked up via `activeWorkspace()` + `chatSessionFor`).
- Streaming patches: `patchChatMessage` re-renders the whole `.chat-message-media` wrapper in-place when needed, so expand state survives streaming updates. It compares `data-media-signature` on the container against `chatMediaSignature(message)` (expand flag + per-item id/name/bytes/dataUrl-present) and SKIPS the rebuild when identical — this prevents expanded videos from restarting playback on every content delta. Do NOT reintroduce separate per-container patching for `.chat-message-images`/`.chat-message-videos` — the wrapper owns both.
- CSS: `.chat-message-media`, `.chat-message-media-bar`, `.chat-media-chip*` rules in `styles.css` (just before `.chat-message-images`); mobile scaling rule near line ~9150 includes `.chat-message-media`.

### Wails Generated Types
- `ChatVideoInput`: `{ id?, name?, mediaType?, dataUrl, bytes? }` — sent to backend
- `ChatVideoAttachment`: `{ id, source, name, path?, mediaType, bytes, dataUrl? }` — returned in messages
- `ChatMessageRequest.videos?: ChatVideoInput[]`
- `ChatMessage.videos?: ChatVideoAttachment[]`

### Key Pitfalls
1. Do NOT create a `Blob` from the backend's `dataUrl` string — it's already base64-encoded text, not binary data. Use the `dataUrl` directly for draft state.
2. `WorkspaceMediaFile.mimeType` (not `mediaType`) is the field name in generated types.
3. `patchChatControls` must check video drafts alongside image drafts for send button logic.
4. `bindChatEvents` must bind `[data-chat-video-upload]` clicks to `handleChatVideoUpload`.
5. Video removal action (`remove-chat-video`) is handled in `actions.ts`, not in `index.ts`.
6. `styles.css` exceeds the 256 KB `filesystem_edit_text` limit — edit it via shell (PowerShell string splice) or use targeted search+read.
7. Any new message-level media UI must go through `renderChatMessageMedia` so both the initial render and streaming patch paths stay in sync.
8. **CRITICAL: `bindActionEvents` binds click listeners per-element at render time — elements inserted into the DOM later have NO listener.** Whenever you insert a fresh `[data-action]` node via template/replaceChild (toggle action, `patchChatMessage` media swap), you MUST call `bindActionEvents(node)` on the new node. In `chat/index.ts` use `getAppCallbacks().bindActionEvents(...)` — a direct import from `../actions` would create a circular dependency (`actions.ts` imports `./chat`). Symptom of forgetting: button works once, then is dead until a full re-render.
