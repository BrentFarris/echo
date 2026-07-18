---
name: chat-media-attachments
description: 'How chat image/video attachments work end-to-end: user uploads, tool result attachments to ChatMessage for UI rendering, stream events, LLM content parts, image compression before sending, media stripping from both in-memory messages and persistent history to prevent 413 errors, and user-friendly 413 error messaging.'
triggers:
    - chat image
    - chat video
    - media attachment
    - ChatImageInput
    - prepareChatImages
    - compressChatImage
    - image compression
    - 413 request entity too large
    - stripMediaContentParts
    - attachChatImage
    - LLMImageContentProvider
    - imaging.Resize
---

## Chat Media Attachment System

The backend chat system supports both image and video file attachments sent as base64 data URLs in OpenAI-compatible content parts. Video `video_url` parts are now enabled for endpoints that support them. Images are automatically compressed before sending to prevent 413 errors from large payloads.

### Key Files

- `internal/services/chat_images.go` — attachment constants, types, preparation, validation, detection, compression (`compressChatImage`), and content-parts building
- `internal/services/tool_images.go` — `toolResultMessages`, `toolResultImageMessage`, `toolResultVideoMessage`, `stripMediaContentParts` for tool-call results with media
- `internal/llm/types.go` — LLM message model with `MessageContentPart`, `ImageURL`, `VideoURL` fields
- `internal/llm/client.go` — `responseError()` includes 413-specific user-friendly messaging
- `internal/services/chat.go` — `ChatMessageRequest`, `sendChatMessage` flow, `ChatMessage` struct, `attachChatImage`, `attachChatVideo`, `ChatStreamEvent` attachment fields

### Attachment Types

| Type | Input | Attachment | MIME types | Max per file | Max count |
|------|-------|------------|------------|--------------|-----------|
| Image | `ChatImageInput` | `ChatImageAttachment` | png, jpeg, webp, gif | 10 MB | 4 |
| Video | `ChatVideoInput` | `ChatVideoAttachment` | mp4, webm, quicktime(mov) | 50 MB | 4 |

Total media count limit: `maxChatMediaAttachments = 8`. Image message byte limit: `maxChatImageMessageBytes = 20 MB`.

### Request Flow (User-Initiated Attachments)

1. Frontend sends `ChatMessageRequest` with `Images []ChatImageInput` and/or `Videos []ChatVideoInput`
2. `sendChatMessage` calls `prepareChatImages` then `prepareChatVideos`
3. Each prepare function processes workspace @mentions (`referencedWorkspaceImages` / `referencedWorkspaceVideos`) and pasted inputs
4. **Compression is applied** in both entry points: `normalizePastedChatImage` (after `parseChatImageDataURL`) and `readWorkspaceChatImage` (after `os.ReadFile`)
5. Validation enforces per-type count/size limits via `validateChatImages` / `validateChatVideos`
6. Content text is built via `chatMediaTextContent` (labels all attachments as "Attached media:")
7. Content parts are built via `chatMediaContentParts` — produces:
   - One `text` part with attachment labels
   - `image_url` parts for each image (data URL)
   - `video_url` parts for each video (data URL)
8. `ChatMessage` stores results in `Images []ChatImageAttachment` and `Videos []ChatVideoAttachment`

### Image Compression (`compressChatImage`)

Located in `internal/services/chat_images.go`. Applied to **all** non-GIF images before creating the data URL attachment, for both pasted and workspace-referenced images.

**Constants:**
- `maxImageDimension = 2048` — longest side limit
- `jpegQuality = 85` — JPEG encoding quality

**Behavior:**
- **GIF**: returned unchanged to preserve animation (lossless passthrough)
- **PNG**: decoded, resized if needed, re-encoded as JPEG at quality 85. This eliminates alpha channel overhead and typically reduces size significantly for screenshots/photos.
- **JPEG**: decoded, resized if needed, re-encoded at quality 85 for consistency
- **WebP**: decoded, resized if needed, converted to JPEG (Go stdlib cannot encode WebP)
- **Decode failure**: if `image.Decode` fails (e.g., corrupted file or minimal test PNG header), original bytes are returned unchanged. This prevents blocking chat sends on edge cases.
- **Resizing**: uses `imaging.Resize()` with Lanczos resampling when either dimension exceeds 2048px, maintaining aspect ratio

**Dependency:** `github.com/disintegration/imaging` v1.6.2 (added to `go.mod`)

### Better 413 Error Messaging

`responseError()` in `internal/llm/client.go` detects HTTP 413 status codes and returns a user-friendly message:
> "LLM endpoint rejected the request (413 Request Entity Too Large). This is usually caused by large image attachments or accumulated context. Try using smaller images"

Raw response body (e.g., nginx HTML error pages) are hidden from the user for 413 responses.

### Tool Result Media Flow (LLM Context + UI Rendering)

When a tool returns media content during chat execution, two things happen in `executeToolCall`:

**1. LLM context:** `toolResultMessages()` produces history messages:
- **Images**: `toolResultImageMessage()` creates a user message with text + `image_url` part
- **Videos**: `toolResultVideoMessage()` creates a user message with text + `video_url` part

**2. UI rendering:** The tool result is checked for provider interfaces and attached to the assistant `ChatMessage`:
- If `result.Output` implements `tools.LLMImageContentProvider`, `attachChatImage()` appends a `ChatImageAttachment` to `message.Images` and emits an `"image_attached"` stream event
- If `result.Output` implements `tools.LLMVideoContentProvider`, `attachChatVideo()` appends a `ChatVideoAttachment` to `message.Videos` and emits a `"video_attached"` stream event

The attachment uses `Source: "tool"`, auto-generated ID via `nextChatID("img")` / `nextChatID("vid")`, and name `<toolName>-generated`.

### Media Stripping (Prevents 413 Errors)

Tool result messages with image/video `ContentParts` contain full base64 data URLs (megabytes). If accumulated in the in-memory `messages` slice across tool-call iterations within a single turn, they cause **413 Request Entity Too Large** errors on subsequent LLM requests. Additionally, persistent history must never store raw data URLs.

**Mechanism:** `stripMediaContentParts()` in `internal/services/tool_images.go` removes all `ContentParts` from a message while preserving `Role`, `Content` (text description), `ToolCallID`, and `Name`.

**Applied at two points in `chat.go`:**
1. **`runChatTurnWithHistory` tool loop**: After each `executeToolCall`, every result message is stripped via `stripMediaContentParts()` before being appended to **both** the in-memory `messages` slice AND persistent history via `appendChatHistory`. This prevents base64 accumulation across iterations within a single turn.
2. **`replaceChatHistory`**: All messages are stripped before storage, providing defense-in-depth so compacted or recovered messages cannot reintroduce data URLs into persistent history.

**Result:** The LLM sees only the text description (e.g., "Image returned by tool filesystem_read_image: output.png (image/png, 2.3 MB).") in subsequent iterations — ~100 bytes instead of megabytes of base64. User-visible `ChatMessage.Images`/`Videos` are unaffected — images still display in the UI via attachments stored on the `ChatMessage` struct, not via LLM context messages.

**Trade-off:** The model no longer sees generated image/video pixels in subsequent iterations within a turn. It knows what prompt was sent to the generator and receives text descriptions. Visual verification benefit is outweighed by 413 prevention.

### Context Compaction and Images

`serializeContextMessage` in `context_compaction.go` already replaces image URLs with `[image omitted from checkpoint source]` when serializing stale messages for AI summarization. The `replaceChatHistory` stripping ensures compacted results also don't persist data URLs.

### Tool Result Attachment Stream Events

`ChatStreamEvent` has optional `ImageAttachment` and `VideoAttachment` pointer fields. The frontend listens for `"image_attached"` and `"video_attached"` event types to update the UI in real time without a full re-render.

```go
type ChatStreamEvent struct {
    // ... existing fields ...
    ImageAttachment *ChatImageAttachment  `json:"imageAttachment,omitempty"`
    VideoAttachment *ChatVideoAttachment  `json:"videoAttachment,omitempty"`
}
```

### Provider Interfaces

Tools that produce media implement these interfaces on their output type:

- `tools.LLMImageContentProvider` — `LLMImageContent() (LLMImageContent, bool)`
- `tools.LLMVideoContentProvider` — `LLMVideoContent() (LLMVideoContent, bool)`

Known implementers: `readImageFileOutput`, `readVideoFileOutput`, ComfyUI generate output.

### Video Magic Byte Detection (`detectChatVideoMediaType`)

- **MP4/M4V**: bytes[4:8] == `"ftyp"` (ISO Base Media ftyp box)
- **WebM**: bytes[0:4] == `\x1A\x45\xDF\xA3` (EBML header)
- **MOV/QuickTime**: bytes[4:8] == `"moov"`

### Testing

- `internal/services/chat_images_test.go` — unit tests for `compressChatImage`: small PNG passthrough, large PNG resize + JPEG conversion, GIF preservation, JPEG re-encoding, valid data URL output
- `internal/llm/client_test.go` — `TestResponseError413ReturnsUserFriendlyMessage` verifies 413 handling

### Change Hazards

- Tool result attachments bypass `validateChatImages`/`validateChatVideos` — there's no count limit guard for tool-produced media on a message. If this becomes an issue, add a cap in `attachChatImage`/`attachChatVideo`.
- Video data URLs can be large (up to 50 MB raw → ~66 MB base64 encoded)
- `video_url` content parts are only supported by some OpenAI-compatible endpoints
- **Always strip media `ContentParts` before storing in persistent history** — use `stripMediaContentParts()`. Both the `runChatTurnWithHistory` tool loop and `replaceChatHistory` must apply it. If a new code path writes to `session.History` or appends to the `messages` slice with tool results, add stripping there too.
- Tests `TestSystemServiceChatReadImageToolSendsImageContentPart` and `TestSystemServiceChatStripsImagesFromHistory` verify that images are stripped from the messages slice (not just history). Update them if stripping behavior changes.
- **Compression degrades gracefully**: `compressChatImage` returns original bytes on decode failure rather than erroring. This means test fixtures with minimal PNG headers still work, but production corrupted images won't be compressed. If strict compression is needed, change the decode-failure path to return an error.
