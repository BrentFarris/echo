---
name: save-image-tool-workflow
description: save_image agent tool for persisting generated images to workspace disk, with ImageID tracking in ExecutionContext.GeneratedImages across tool calls within a turn. Wired through both chat.go and kanban_scheduler.go execution loops with diagnostic logging.
triggers:
    - save_image
    - save image
    - image persistence
    - generated images
    - ImageID tracking
    - comfyui_generate output
    - binary file write
    - ExecutionContext.GeneratedImages
    - kanban image chaining
    - toolResultImageMessage
    - image not found error
---

# save_image Tool: Persisting Generated Images to Workspace Disk

## Problem Solved
When `comfyui_generate` returns an image, the agent only sees metadata (name, mediaType, bytes) — not the base64 data. The `dataURL` field on `comfyuiOutput` is unexported and never serialized. There was no way for agents to save generated images because:
1. Agent can't see image binary data in JSON response
2. No tool exists to write binary files (`filesystem_create_text` only handles text)
3. No way to reference previously-generated images across tool calls within a turn

## Architecture

### ImageID Tracking Flow
1. `comfyui_generate` generates a UUID `imageID` when image data is fetched (line ~340 in comfyui_generate.go). Note: the struct field is unexported (`imageID string`) but the exported method `ImageID() string` implements `tools.ImageIDProvider`.
2. The `ImageID` is included in the JSON output sent to the model via exported `ImageID string json:"imageId,omitempty"` field on `comfyuiOutput`
3. In both chat and kanban tool execution loops, after a successful tool execution, if the result implements `LLMImageContentProvider`, the image data is:
   - Attached to the chat message for UI rendering (chat path only)
   - Stored in `generatedImages map[string]tools.AttachedImage` keyed by ImageID
4. The `generatedImages` map is passed through `executeTrackedToolCall` into `ExecutionContext.GeneratedImages`
5. Subsequent tool calls within the same turn can reference the image by ID

### Where generatedImages Is Wired
The `generatedImages` map is created per-agent-turn and threaded through every tool call:

| Path | File | Map Declaration | Passed to executeTrackedToolCall |
|---|---|---|---|
| Chat | `chat.go` | `runChatTurnWithHistory` ~line 400 | Yes, in `executeToolCall` |
| Kanban | `kanban_scheduler.go` | `runKanbanAgent` ~line 391 | Yes, in `executeKanbanToolCall` |
| Inline code prompt | `inline_code_prompt.go` | — | Passes `nil` (no cross-call image chaining needed) |

### Image Tracking Logic Pattern
Both chat.go and kanban_scheduler.go use the same pattern after tool execution:

```go
if result.Success && result.Output != nil {
    if provider, ok := result.Output.(tools.LLMImageContentProvider); ok {
        if image, ok := provider.LLMImageContent(); ok && image.DataURL != "" {
            if idProvider, ok := result.Output.(tools.ImageIDProvider); ok && idProvider.GetImageID() != "" {
                generatedImages[idProvider.GetImageID()] = tools.AttachedImage{...}
                fmt.Fprintln(os.Stderr, "[chat|kanban] tracked generated image", ...)
            } else if outMap, jsonOk := result.Output.(map[string]any); jsonOk {
                if imageID, ok := outMap["imageId"].(string); ok && imageID != "" {
                    generatedImages[imageID] = tools.AttachedImage{...}
                    fmt.Fprintln(os.Stderr, "[chat|kanban] tracked generated image", ..., "(via map)")
                } else {
                    fmt.Fprintln(os.Stderr, "[chat|kanban] tool", ..., "returned image but no imageId in output map")
                }
            } else {
                fmt.Fprintln(os.Stderr, "[chat|kanban] tool", ..., "returned image but output is not ImageIDProvider or map[string]any")
            }
        } else {
            fmt.Fprintln(os.Stderr, "[chat|kanban] tool", ..., "LLMImageContent returned empty DataURL")
        }
    } else {
        fmt.Fprintln(os.Stderr, "[chat|kanban] tool", ..., "output does not implement LLMImageContentProvider")
    }
}
```

The two extraction paths handle: (a) `ImageIDProvider` interface (comfyuiOutput), and (b) raw JSON map fallback.
Diagnostic logging at each branch point helps identify why tracking failed.

### save_image Tool Implementation
- **File**: `internal/tools/save_image.go`
- **Parameters**: `imageId` (required), `path` (required), `overwrite` (optional, defaults false)
- **Logic**: Looks up imageId in `ctx.GeneratedImages`, decodes base64 from DataURL, resolves workspace path via `resolveWorkspaceChildPath`, writes binary file using `os.OpenFile` with same pattern as `filesystem_create_text`
- **Error messages**: When image not found, error includes list of available image IDs for debugging
- **Registry**: Added to `mutatingToolNames` in registry.go; included in `availableToolNames` in frontend settings

### ExecutionContext Wiring
- `ExecutionContext.GeneratedImages map[string]tools.AttachedImage` in types.go
- `executeTrackedToolCall` signature accepts `generatedImages map[string]tools.AttachedImage` parameter (file_changes.go)
- All callers must include the parameter; pass `nil` only for paths that don't need cross-call image tracking

### System Prompt Guidance
Added to `chatSystemMessage` in chat.go: "After generating an image with comfyui_generate, use the returned imageId with save_image to persist it to workspace disk if needed."

## Key Files
- `internal/tools/save_image.go` — Tool implementation and error messages
- `internal/tools/save_image_test.go` — Unit tests (happy path, not-found, empty dataURL, missing args, file-exists, overwrite)
- `internal/tools/comfyui_generate.go` — ImageID field on output; implements `ImageIDProvider` via exported method
- `internal/tools/types.go` — GeneratedImages field on ExecutionContext
- `internal/tools/registry.go` — save_image in mutatingToolNames
- `internal/services/chat.go` — Image tracking in executeToolCall with diagnostic logging, system prompt guidance
- `internal/services/kanban_scheduler.go` — generatedImages wired through executeKanbanToolCall (same pattern as chat) with diagnostic logging
- `internal/services/file_changes.go` — executeTrackedToolCall accepts generatedImages parameter
- `internal/services/inline_code_prompt.go` — Passes nil for generatedImages
- `frontend/src/app/settings/index.ts` — save_image in availableToolNames array

## Pitfalls
- The `chatToolCallExecution` and `kanbanToolCallExecution` structs do NOT have a `Result` field — image tracking must happen inside the executeToolCall method where `result` is available, not in the caller loop
- Indentation in execution loops is critical — the for-loop body must be properly indented (inside the outer for-loop)
- All callers of `executeTrackedToolCall` must include the `generatedImages` parameter even if passing nil
- The image tracking uses type assertion to `map[string]any` as fallback to extract `imageId` — this works because comfyuiOutput fields are serialized as map keys when returned through tool execution
- `comfyuiGenerate` assigns to `output.imageID` (lowercase unexported field); the exported method `GetImageID()` provides access via the `tools.ImageIDProvider` interface
- Diagnostic logging uses `fmt.Fprintln(os.Stderr, ...)` — requires `"os"` import in both chat.go and kanban_scheduler.go
