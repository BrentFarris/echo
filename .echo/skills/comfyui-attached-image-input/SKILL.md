---
name: comfyui-attached-image-input
description: How comfyui_generate accepts chat-attached images via attachedImageIndex, wires AttachedImages through ExecutionContext, and routes to img2img workflows without disk I/O.
triggers:
    - comfyui attached image
    - attachedImageIndex
    - chat image img2img
    - comfyui memory upload
    - AttachedImages ExecutionContext
    - comfyui workflow routing
---

## ComfyUI Attached Image Input

### Overview
`comfyui_generate` accepts `attachedImageIndex` (0-based integer) to use chat-attached images directly as img2img input. Zero workspace disk I/O — base64 data is decoded from memory and uploaded via HTTP multipart to ComfyUI's `/upload/image` endpoint.

### Key Files
- `internal/tools/types.go`: `AttachedImage` struct (Name, MediaType, DataURL) and `AttachedImages []AttachedImage` field on `ExecutionContext`
- `internal/services/chat.go`: `latestUserMessageImages(workspaceID string)` walks the chat session in reverse to find the latest user message's images
- `internal/services/file_changes.go`: wires `AttachedImages` into `ExecutionContext` inside `executeTrackedToolCall`
- `internal/tools/comfyui_generate.go`: handles `attachedImageIndex` parameter, base64 decode, extension mapping, and workflow routing

### Resolution Priority
1. Explicit `imagePath` (workspace file path) — always takes precedence
2. `attachedImageIndex` with attached images available — routes to img2img workflow
3. No image input — routes to txt2img workflow

### Workflow Selection
`hasInputImage` is true when either `imagePath` is set OR `attachedImageIndex` is provided with attached images available. When `hasInputImage` is true and no custom workflow is specified, the img2img default workflow is selected.

### Attached Image Resolution
- `decodeAttachedImageData()` extracts raw bytes from a data URL (strips `data:mediatype;base64,` prefix)
- `attachedImageExtension()` maps MediaType to file extension (.png, .jpg, .webp, .gif)
- Server filename is generated as `echo_input_{uuid}{ext}`
- Index bounds checked: must be >= 0 and < len(ctx.AttachedImages)

### System Prompt Guidance
The chat system prompt includes guidance: "When images are attached to a chat message, use comfyui_generate with attachedImageIndex (0-based) to pass them directly as img2img input — no need to save to disk first."

### Error Codes
- `invalid_index`: attachedImageIndex out of range or negative
- `decode_image_failed`: data URL parsing or base64 decode failure
- `upload_image_failed`: ComfyUI upload endpoint error

### Testing
Tests in `internal/tools/comfyui_generate_test.go` cover:
- Memory upload via attachedImageIndex (PNG, JPEG, WebP, GIF)
- Index bounds handling (out of range, negative, no images present)
- Resolution priority (imagePath wins over attachedImageIndex)
- Workflow routing (img2img selected when attachedImageIndex used)
- Invalid data URL handling
