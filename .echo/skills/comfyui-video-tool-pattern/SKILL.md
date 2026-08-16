---
name: comfyui-video-tool-pattern
description: 'comfyui_generate_video tool: parameters, workflow resolution, handler flow, output type, format handling, model-facing default-workflow guidance requirement, and test patterns'
triggers:
    - comfyui video
    - video workflow default
    - comfyui_generate_video
---

## comfyui_generate_video Tool

Located in `echo/internal/tools/comfyui_generate_video.go`. Cloned from `comfyui_generate.go` pattern for video generation.

### Registration

Uses `ToolFunc` pattern registered via `init()`:
```go
func init() { Register(ToolFunc{Meta: Metadata{...}, Run: comfyuiGenerateVideo}) }
```

### Parameters

- `prompt` (required) — positive text prompt
- Video-specific: `frames` (default 16), `fps` (default 8.0), `format` ("mp4" or "gif", default "mp4")
- Duration-driven: `duration` (default 5s, for MiniMax H3-style workflows that compute frames from duration + aspect-ratio/megapixels resolution selector)
- Standard gen params: `negativePrompt`, `model`, `width`, `height`, `steps`, `cfgScale`, `seed`
- Input image: `imagePath` (workspace-relative) or `attachedImageIndex` (chat-attached images)
- Workflow override: `workflowPath` or `workflowJSON`

### Workflow Resolution Order

1. Explicit `workflowJSON` (highest priority)
2. Explicit `workflowPath` (workspace-relative file)
3. `ctx.ComfyuiVideoWorkflow` setting from ExecutionContext
4. Error with `missing_video_workflow` code

**Important:** Does NOT fall back to `ComfyuiTxt2imgWorkflow` or `ComfyuiImg2imgWorkflow`. Uses dedicated video workflow setting only.

### Model-facing guidance (critical pitfall)

The tool silently relies on the configured default (`ctx.ComfyuiVideoWorkflow`) when no workflow arg is passed — but **the model does not know a default exists unless it is told**. If the system prompt / tool schema don't advertise the default, the model will fabricate its own inline `workflowJSON`, which takes top priority and overrides the user's configured workflow.

Two places must keep advertising the default:
1. `internal/services/chat.go` → `chatSystemMessage`: the video guidance sentence instructs the model to call `comfyui_generate_video` **without** `workflowPath`/`workflowJSON` when the user doesn't specify a particular workflow, so it uses the settings default; only pass them for an explicitly requested workflow.
2. The tool's own `Metadata.Description` + `workflowPath`/`workflowJSON` param descriptions in `comfyui_generate_video.go` ("If no workflow is specified, uses the default video workflow configured in settings" / "Overrides the configured default video workflow; omit it to use the default").

Tests guarding this:
- `internal/services/chat_system_message_test.go` → `TestChatSystemMessageGuidesVideoWorkflowDefault` (asserts the guidance substring is present in the general-mode system message).
- `internal/tools/comfyui_generate_video_test.go` → `TestComfyuiGenerateVideoSchemaDocumentsDefaultWorkflow` (asserts the schema Description/param descriptions mention the default + override behavior via `LLMSchema()`).

If you change either the guidance wording or the schema descriptions, keep these two tests in sync.

### Handler Flow

1. Parse args, validate prompt required
2. Require ComfyUI URL configured (errors if empty) — unlike comfyui_generate which defaults to localhost:8188
3. Resolve model: explicit arg > ComfyuiDefaultCheckpoint
4. Build TemplateParams with Frames/FPS/Format fields
5. Upload input image if imagePath or attachedImageIndex provided (100MB limit vs 20MB for images)
6. Submit to ComfyUI via `client.Generate(params, workflow)` — template substitution handles {{FRAMES}}, {{FPS}}, {{FORMAT}}
7. Fetch first video from `result.OutputVideos` via `client.FetchVideoBytes`
8. Return comfyuiVideoOutput with VideoId for save_video tool

### Output Type

`comfyuiVideoOutput` implements:
- `LLMVideoContent()` — returns LLMVideoContent for inline chat rendering
- `VideoID()` — unique ID for save_video persistence

Media type detection via `detectComfyuiVideoMediaType(filename, data)` uses extension first (.mp4, .gif, .webm), then magic bytes fallback.

### Format Validation

`buildReplaceMap` in `workflow.go` only accepts "mp4" and "gif" as valid formats. Invalid values (including "webm") fall back to "mp4". Video outputs are fetched using ComfyUI's `videos` and `gifs` node output keys.

### Related gap (known, separate)

Research agents in `internal/services/chat_research.go` build their `ExecutionContext` WITHOUT any ComfyUI fields (no ComfyuiURL/ComfyuiVideoWorkflow), so a spawned research agent calling comfyui_generate_video always hits `missing_comfyui_url`. Only the main chat/kanban/inline paths (`file_changes.go` → `executeTrackedToolCall`) populate them.
