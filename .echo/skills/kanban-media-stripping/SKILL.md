---
name: kanban-media-stripping
description: stripMediaContentParts must be applied to Kanban tool result messages to prevent 413 errors from accumulated base64 image data in LLM context.
triggers:
    - 413 request entity too large
    - stripMediaContentParts
    - kanban tool result
    - base64 accumulation
    - comfyui_generate kanban
    - filesystem_read_image kanban
    - LLM context bloat
---

## Kanban Tool Result Media Stripping

Both regular chat (`chat.go`) and Kanban agent loops (`kanban_scheduler.go`) must strip media content parts from tool result messages before appending to the in-memory `messages` slice sent to the LLM endpoint. This prevents base64 image/video data from accumulating across turns and causing **413 Request Entity Too Large** errors.

### Pattern (chat.go, line 632-635)
```go
for _, resultMessage := range execution.Messages {
    stripped := stripMediaContentParts(resultMessage)
    messages = append(messages, stripped)
    s.appendChatHistory(workspace.ID, stripped)
}
```

### Pattern (kanban_scheduler.go, line 607-610)
```go
for _, resultMessage := range execution.Messages {
    stripped := stripMediaContentParts(resultMessage)
    messages = append(messages, stripped)
}
```

### Why it matters
Tools like `comfyui_generate`, `filesystem_read_image`, and `save_image` return `LLMImageContentProvider` results. The `toolResultMessages()` function creates messages with `ContentParts` containing full base64-encoded image data (1-5+ MB). Without stripping, each tool call adds megabytes to the context, eventually exceeding the LLM endpoint's request size limit.

### Function location
`stripMediaContentParts` is defined in `internal/services/tool_images.go` and is package-private (lowercase), so it's accessible within `internal/services`.

### Verification
Test `TestKanbanSchedulerReadImageToolStripsMediaFromContext` in `kanban_scheduler_test.go` confirms that media content parts are absent from the second LLM request after an image-reading tool call.
