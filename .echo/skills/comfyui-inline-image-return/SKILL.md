---
name: comfyui-inline-image-return
description: How comfyui_generate fetches generated image bytes and returns them as inline-renderable content via LLMImageContentProvider.
triggers:
    - comfyui inline image
---

## ComfyUI Inline Image Return

`comfyui_generate` returns generated images inline in chat by implementing `LLMImageContentProvider`.

### Flow

1. `client.Generate()` posts workflow, polls `/history/{id}` until images appear
2. After generation, the tool fetches the first image via `client.FetchImageBytes()` from `/view?filename=...&subfolder=...&type=output`
3. Image bytes are base64-encoded into a `data:` URL stored in `comfyuiOutput.dataURL`
4. `comfyuiOutput.LLMImageContent()` returns the image metadata so chat renders it inline

### Key Files

- `internal/tools/comfyui_generate.go` — tool implementation with `LLMImageContentProvider`
- `internal/comfyui/client.go` — `Generate()`, `GetHistory()`, `FetchImageBytes()`
- `internal/comfyui/queue.go` — `WaitForCompletionPoll()` polling loop
- `internal/services/tool_images.go` — detects `LLMImageContentProvider` on tool results

### Tests

Mock servers must handle three endpoints:
- `POST /prompt` — returns `{"prompt_id": "..."}`
- `GET /history/{id}` — returns outputs with image list
- `GET /view?filename=...&subfolder=...&type=output` — returns raw image bytes
