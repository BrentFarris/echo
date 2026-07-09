---
name: agent-tool-filesystem-read-video
description: How the filesystem_read_video agent tool reads workspace video files, detects format via magic bytes, enforces size limits, and returns OpenAI-compatible video_url content parts through LLMVideoContentProvider.
triggers:
    - filesystem_read_video
    - video tool
    - LLMVideoContentProvider
    - readVideoFileOutput
    - detectVideoMediaType
    - video data URL agent
    - maxVideoFileBytes
---

## filesystem_read_video Agent Tool

### Location
`internal/tools/filesystem_read_video.go`

### Purpose
Registered agent tool that reads video files (MP4, WebM, MOV) from the workspace and returns them as data URLs in OpenAI-compatible `video_url` format for LLM context.

### Registration
Auto-registered via `init()` like other filesystem tools. Uses `labeledPathSchemaHint` for path descriptions.

### Supported Parameters
- `path` (required): Labeled workspace video file path
- `detail` (optional): OpenAI detail hint — "auto", "low", or "high"

### Size Limit
`maxVideoFileBytes = 50 * 1024 * 1024` (50 MB) — consistent with `maxChatVideoBytes` in `internal/services/chat_images.go`.

### Video Format Detection
`detectVideoMediaType` uses magic bytes:
- **MP4/M4V**: offset 4-8 == "ftyp" → `video/mp4`
- **WebM**: first 4 bytes == `0x1A 0x45 0xDF 0xA3` → `video/webm`
- **MOV/QuickTime**: offset 4-8 == "moov" → `video/quicktime`

This mirrors `detectChatVideoMediaType` in `internal/services/chat_images.go`.

### Output Structure
```go
type readVideoFileOutput struct {
    Path, Name, MediaType string
    Bytes                 int64
    ContentType           string // "video_url"
    Detail                string
    dataURL               string // unexported; base64 data URL
}
```

### LLMVideoContentProvider Interface
`readVideoFileOutput` implements `tools.LLMVideoContentProvider`:
```go
type LLMVideoContent struct { Path, Name, MediaType string; Bytes int64; DataURL, Detail string }
type LLMVideoContentProvider interface { LLMVideoContent() (LLMVideoContent, bool) }
```

### Integration with Chat Flow
`toolResultMessages` in `internal/services/tool_images.go` checks both `LLMImageContentProvider` and `LLMVideoContentProvider`. When a tool result implements the video provider, it creates an `llm.Message` with:
- Role: `user`
- ContentParts: text description + `video_url` content part via `llm.VideoURLContentPart()`
- Detail hint applied to `MessageVideoURL.Detail` if provided

### Path Validation
Uses shared `resolveWorkspacePath` from `filesystem_helpers.go` — prevents workspace escape, symlink traversal, and absolute paths.

### Verification
- Tool appears in `LLMSchema()` registry
- Tests: run `go test ./internal/tools/...`
- Full suite: `go test ./...`
