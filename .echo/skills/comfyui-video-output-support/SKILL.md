---
name: comfyui-video-output-support
description: 'ComfyUI video output support: history collection, /view type parameter matching, FetchVideoBytes, inline chat rendering, and common pitfalls with SaveVideo type values.'
triggers:
    - comfyui video
    - FetchVideoBytes
    - OutputVideos
    - /view type parameter
    - SaveVideo output key
    - video fetch 400 error
    - type=output
    - inline video rendering
---

## ComfyUI Video Output Support

### Overview
Echo supports both image and video outputs from ComfyUI workflows. Video output handling involves collection from history, fetching via `/view`, and inline chat rendering.

### Collection (GetHistory in `client.go`)
Two-pass approach handles varying node output conventions:

**Pass 1 — "images" key with extension routing:** Many SaveVideo nodes (especially custom ones like Minimax H3) emit video files under the `"images"` output key. The code checks file extensions to route correctly:
- Video extensions (`.mp4`, `.webm`, `.avi`, `.mov`, `.mkv`, `.gif`) → `OutputVideos`
- Non-video files with `type == "output"` or empty type → `OutputImages`
- **Non-video files with `type == "custom"` are skipped** (avoids spurious outputs)

**Pass 2 — Dedicated video keys:** Iterates over `["videos", "video", "gifs", "animated_gifs"]`. Accepts entries where filename is present, or type is `"output"`/`"custom"`.

### Fetching via `/view` Endpoint
**Critical: The `type` query parameter must match exactly what was stored in history.**

| Node Type | History Key | Stored `type` | `/view` fetch `type` |
|---|---|---|---|
| SaveImage | `"images"` | `"output"` | `"output"` |
| SaveVideo (built-in) | `"images"` | `"output"` | **`"output"`** |
| VideoCombine | `"videos"` | `"custom"` | `"video"` |
| GIF nodes | `"gifs"` | `"custom"` | `"custom"` |

**Common pitfall:** `comfyui_generate_video.go` must use `type=output` for `.mp4`/`.webm` files from SaveVideo nodes. Using `type=video` returns HTTP 400 because the stored type is `"output"`, not `"video"`.

### Video Type Determination (in tool)
```go
// comfyui_generate_video.go — determine fetch type by extension
videoType := "output" // default for mp4/webm saved by SaveVideo
if ext == ".gif" {
    videoType = "custom"
}
videoData, err := client.FetchVideoBytes(ctx, filename, subfolder, videoType)
```

### Completion Check (`WaitForCompletionPoll` in `queue.go`)
Accepts either images OR videos as completion signal:
```go
if len(result.OutputImages) > 0 || len(result.OutputVideos) > 0 {
    return result, nil
}
```

### Video Template Variables (`workflow.go`)
- `{{FRAMES}}` → default 16
- `{{FPS}}` → default 8.0
- `{{FORMAT}}` → lowercased, default "mp4"

### Inline Chat Rendering Chain
1. Tool fetches video bytes → encodes as base64 dataURL
2. `LLMVideoContent()` provides inline content for the tool result
3. Chat execution loop tracks videos in `generatedVideos` map on `ExecutionContext`
4. `attachChatVideo(chatID, streamID, messageID, attachment)` mutates the message and emits SSE event
5. Frontend renders `<video controls autoplay loop muted playsinline>` tags

### Pitfalls
- **SaveVideo outputs under `"images"` key** — route by extension, not output key name
- **`/view?type=` must match stored type** — SaveVideo uses `type=output`, not `type=video`
- Video fetch failure with HTTP 400 almost always means wrong `type` parameter
- GIFs use `type=custom`, not `type=video`
