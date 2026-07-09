---
name: chat-media-attachments
description: How chat image and video attachments are accepted, validated, detected by magic bytes, converted to LLM content parts (image_url/video_url), and stored in ChatMessage. Video_url parts are now enabled for compatible endpoints.
triggers:
    - chat image
    - chat video
    - media attachment
    - ChatImageInput
    - ChatVideoInput
    - prepareChatImages
    - prepareChatVideos
    - video_url
    - image_url content part
    - detectChatVideoMediaType
---

## Chat Media Attachment System

The backend chat system supports both image and video file attachments sent as base64 data URLs in OpenAI-compatible content parts. Video `video_url` parts are now enabled for endpoints that support them.

### Key Files

- `internal/services/chat_images.go` — attachment constants, types, preparation, validation, detection, and content-parts building
- `internal/services/tool_images.go` — `toolResultMessages`, `toolResultImageMessage`, `toolResultVideoMessage` for tool-call results with media
- `internal/llm/types.go` — LLM message model with `MessageContentPart`, `ImageURL`, `VideoURL` fields
- `internal/services/chat.go` — `ChatMessageRequest`, `sendChatMessage` flow, `ChatMessage` struct

### Attachment Types

| Type | Input | Attachment | MIME types | Max per file | Max count |
|------|-------|------------|------------|--------------|-----------|
| Image | `ChatImageInput` | `ChatImageAttachment` | png, jpeg, webp, gif | 10 MB | 4 |
| Video | `ChatVideoInput` | `ChatVideoAttachment` | mp4, webm, quicktime(mov) | 50 MB | 4 |

Total media count limit: `maxChatMediaAttachments = 8`. Image message byte limit: `maxChatImageMessageBytes = 20 MB`.

### Request Flow

1. Frontend sends `ChatMessageRequest` with `Images []ChatImageInput` and/or `Videos []ChatVideoInput`
2. `sendChatMessage` calls `prepareChatImages` then `prepareChatVideos`
3. Each prepare function processes workspace @mentions (`referencedWorkspaceImages` / `referencedWorkspaceVideos`) and pasted inputs
4. Validation enforces per-type count/size limits via `validateChatImages` / `validateChatVideos`
5. Content text is built via `chatMediaTextContent` (labels all attachments as "Attached media:")
6. Content parts are built via `chatMediaContentParts` — produces:
   - One `text` part with attachment labels
   - `image_url` parts for each image (data URL)
   - `video_url` parts for each video (data URL)
7. `ChatMessage` stores results in `Images []ChatImageAttachment` and `Videos []ChatVideoAttachment`

### Tool Result Media Flow

When a tool returns media content, `toolResultMessages()` produces LLM history messages:
- **Images**: `toolResultImageMessage()` creates a user message with text + `image_url` part
- **Videos**: `toolResultVideoMessage()` creates a user message with text + `video_url` part (including detail hint if set)

Both produce multipart content with text label + media content part.

### LLM Content Parts Structure

```json
{
  "type": "video_url",
  "video_url": { "url": "data:video/mp4;base64,...", "detail": "low" }
}
```

`llm.VideoURLContentPart(url)` is the constructor. `cloneMessages` deep-copies both `ImageURL` and `VideoURL` pointers.

Same pattern for images:
```json
{
  "type": "image_url",
  "image_url": { "url": "data:image/png;base64,..." }
}
```

### Video Magic Byte Detection (`detectChatVideoMediaType`)

- **MP4/M4V**: bytes[4:8] == `"ftyp"` (ISO Base Media ftyp box)
- **WebM**: bytes[0:4] == `\x1A\x45\xDF\xA3` (EBML header)
- **MOV/QuickTime**: bytes[4:8] == `"moov"`

### Workspace @mention Resolution

- `chatImagePathKind` returns "supported" for image extensions (.png, .jpg, .jpeg, .webp, .gif)
- `chatVideoPathKind` returns "supported" for video extensions (.mp4, .webm, .mov)
- Both are checked independently via `referencedWorkspaceImages` / `referencedWorkspaceVideos`

### Endpoint Compatibility Note

`video_url` content parts are only supported by some OpenAI-compatible endpoints (not standard OpenAI). If an endpoint rejects them with a 400 error, remove the video loop from `chatMediaContentParts()` and revert `toolResultVideoMessage()` to text-only output. Your endpoint must be verified to support this before enabling.

### Change Hazards

- `ChatMessageRequest.Videos` is optional; omitting it preserves image-only behavior
- Video data URLs can be large (up to 50 MB raw → ~66 MB base64 encoded); ensure your endpoint accepts large payloads
- Video attachments are accepted, validated, stored in `ChatMessage.Videos`, displayed in UI, AND sent as `video_url` content parts to the LLM
