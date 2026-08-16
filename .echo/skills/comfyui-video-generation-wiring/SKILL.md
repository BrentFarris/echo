---
name: comfyui-video-generation-wiring
description: 'Video generation wiring: ComfyuiVideoWorkflow settings field, GeneratedVideos tracking through ExecutionContext, VideoIDProvider interface, explicit-only save_video system prompt guidance (no default saving), and the frames = duration × fps rule.'
triggers:
    - comfyui video
    - video generation
    - save_video
    - GeneratedVideos
    - VideoIDProvider
    - ComfyuiVideoWorkflow
    - AttachedVideo
    - video duration
    - frames fps
    - explicit video save
---

## ComfyUI Video Generation Wiring

Video generation in Echo follows the same pattern as image generation, with parallel structures for tracking and persistence.

### Settings Layer (`internal/llm/settings.go`)
- `Settings.ComfyuiVideoWorkflow` — optional workspace-relative path to a video workflow JSON (e.g., AnimateDiff, SVD)
- Trimmed in `Normalized()` alongside other ComfyUI fields
- Copied by value in `Clone()` (string field, no deep copy needed)

### ExecutionContext Layer (`internal/tools/types.go`)
- `ExecutionContext.ComfyuiVideoWorkflow` — passed from settings to comfyui_generate_video tool
- `GeneratedVideos map[string]AttachedVideo` — tracks video outputs during a turn, keyed by VideoID
- `AttachedVideo{Name, MediaType, DataURL}` — carries video metadata and data URL
- `VideoIDProvider` interface — `VideoID() string`, implemented by tools that produce video IDs

### Tool Call Wiring Pattern

All ExecutionContext construction sites pass `ComfyuiVideoWorkflow` and `GeneratedVideos`:

1. **`file_changes.go:executeTrackedToolCall`** — signature includes `generatedVideos map[string]tools.AttachedVideo`; passes settings.ComfyuiVideoWorkflow into context
2. **`chat.go:runChatTurnWithHistory`** — creates `generatedVideos` map, passes through executeToolCall chain, populates after LLMVideoContentProvider results
3. **`kanban_scheduler.go`** — same pattern in kanban agent loop
4. **`inline_code_prompt.go`** — passes nil for generatedImages/generatedVideos (inline tools don't produce videos)

### Video Tracking Logic

After a tool call, check if output implements `tools.LLMVideoContentProvider`:

```go
if provider, ok := result.Output.(tools.LLMVideoContentProvider); ok {
    if video, ok := provider.LLMVideoContent(); ok && video.DataURL != "" {
        // Check VideoIDProvider first, then fall back to map["videoId"]
        if idProvider, ok := result.Output.(tools.VideoIDProvider); ok && idProvider.VideoID() != "" {
            generatedVideos[idProvider.VideoID()] = tools.AttachedVideo{...}
        } else if outMap, jsonOk := result.Output.(map[string]any); jsonOk {
            if videoID, ok := outMap["videoId"].(string); ok && videoID != "" {
                generatedVideos[videoID] = tools.AttachedVideo{...}
            }
        }
    }
}
```

### Registry (`internal/tools/registry.go`)
- `"comfyui_generate_video"` and `"save_video"` are in `mutatingToolNames`
- `imagePath` and `workflowPath` already in `isPathArgKey` from image tool

### System Prompt Guidance (explicit-only video saving)
In `chat.go:chatSystemMessage`, the video guidance tells the model: use frames to control length (default 16) and fps (default 8); **when the user asks for a specific duration, compute frames = duration × fps and pass both** (e.g., 5s at 24fps → frames: 120), relying on the `duration` parameter only for duration-driven workflows; omit workflowPath/workflowJSON to use the configured default. **Saving is explicit-only**: the prompt says "Do not call save_video automatically after generating a video; only call save_video with the returned videoId when the user explicitly asks to save or download the video." The `save_video` tool description in `internal/tools/save_video.go` reinforces the same rule. Guarded by two tests in `chat_system_message_test.go`:
- `TestChatSystemMessageGuidesVideoWorkflowDefault` — asserts the default-workflow sentence and the duration/frames sentence.
- `TestChatSystemMessageRequiresExplicitVideoSave` — asserts the explicit-only save sentence is present AND that the legacy unconditional "After generating a video, use save_video ... to persist it to disk" wording is gone. Update both tests when changing this prompt text.

### Frontend Settings (`frontend/src/app/settings/index.ts`)
"Video Workflow" input field after txt2img/img2img workflow inputs, with `comfyuiVideoWorkflow` name.

### Pitfalls
- **Duration ≠ frames**: the configured default video workflow (MiniMax H3-style) is duration-driven for resolution ({{ASPECT_RATIO}}/{{MEGAPIXELS}}) but frame count still comes from {{FRAMES}}, which silently defaults to 16 when omitted — a "5s" request with no frames arg yields a ~2s clip. Always pass frames = duration × fps for length-critical requests.
- **No auto-save**: generation never writes to disk by itself; the video only lands in the workspace if the model explicitly calls `save_video` (which requires the user to have asked). Do not reintroduce default-save wording in the system prompt or tool descriptions — it regresses the explicit-only behavior and breaks `TestChatSystemMessageRequiresExplicitVideoSave`.
- Template substitution (`internal/comfyui/workflow.go:buildReplaceMap`) substitutes {{PROMPT}}, {{NEGATIVE_PROMPT}}, {{MODEL}}, {{IMAGE}}, {{WIDTH}}, {{HEIGHT}}, {{STEPS}}, {{CFG_SCALE}}, {{SEED}}, {{FRAMES}}, {{FPS}}, {{FORMAT}}, {{DURATION}}, {{ASPECT_RATIO}}, {{MEGAPIXELS}}; numeric results are coerced to float64.
- When adding a new ExecutionContext field, update ALL construction sites including tests (e.g., workspace_skills_test.go)
- `executeTrackedToolCall` signature changes require updating all callers: chat.go, kanban_scheduler.go, inline_code_prompt.go, and any test files
- TypeScript bindings need manual sync if `wails generate` doesn't regenerate automatically
