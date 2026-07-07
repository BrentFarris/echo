---
name: chat-video-attachments
description: 'How video file attachments are handled in the chat composer: paste, drag-and-drop, web upload button, draft rendering, send payload, and message display.'
triggers:
    - chat video
    - video attachment
    - video draft
    - addPastedChatVideos
    - renderChatVideoDrafts
    - chat-video-upload
    - remove-chat-video
    - ChatVideoInput
    - ChatVideoAttachment
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

### Message Display
- `renderChatMessageVideos(message)` renders `.chat-message-videos > .chat-message-video` with `<video>` tags for data URLs or fallback icon
- Referenced in `renderChatMessage` template after `renderChatMessageImages`
- CSS: `.chat-message-videos`, `.chat-message-video`, `.chat-message-video video`

### Wails Generated Types
- `ChatVideoInput`: `{ id?, name?, mediaType?, dataUrl, bytes? }` — sent to backend
- `ChatVideoAttachment`: `{ id, source, name, path?, mediaType, bytes, dataUrl? }` — returned in messages
- `ChatMessageRequest.videos?: ChatVideoInput[]`
- `ChatMessage.videos?: ChatVideoAttachment[]`

### Key Pitfalls
1. Do NOT create `Blob` from the backend's `dataUrl` string — it's already base64-encoded text, not binary data. Use the `dataUrl` directly for draft state.
2. `WorkspaceMediaFile.mimeType` (not `mediaType`) is the field name in generated types.
3. `patchChatControls` must check video drafts alongside image drafts for send button logic.
4. `bindChatEvents` must bind `[data-chat-video-upload]` clicks to `handleChatVideoUpload`.
5. Video removal action (`remove-chat-video`) is handled in `actions.ts`, not in `index.ts`.
