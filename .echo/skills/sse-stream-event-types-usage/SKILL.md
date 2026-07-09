---
name: sse-stream-event-types-usage
description: Stream.Usage field exposes final token usage synchronously after stream parsing completes, populated via **Usage output parameter in parseStreamLogged.
triggers:
    - SSE stream
    - event types
    - usage parsing
    - stream chunk
    - EventUsage
    - StreamEvent
    - token usage
    - llm streaming
    - Stream.Usage
    - parseStreamLogged
    - usageOut
---

## Stream struct with synchronous Usage access

The `Stream` struct (`internal/llm/client.go`) carries the final token usage synchronously after the events channel closes:

```go
type Stream struct {
    ID     string
    Events <-chan StreamEvent
    Usage  *Usage          // populated before channel closes; nil if no usage in stream

    cancel context.CancelFunc
}
```

### How Usage is populated

`Client.StreamChat` creates the `*Stream`, then starts a goroutine that calls `parseStreamLogged` with `&stream.Usage`. Inside `flushStreamData` (`internal/llm/stream.go`), when a chunk contains a top-level `usage` field, the usage is:

1. Emitted as an `EventUsage` event on the channel (as always).
2. Written to the `usageOut **Usage` output pointer so `stream.Usage` is set before `close(events)`.

### Signature chain

```go
func parseStream(ctx, reader, events)                         // usageOut = nil
func parseStreamLogged(ctx, reader, events, logger, usageOut **Usage)
func flushStreamData(..., usageOut **Usage)                   // captures into *usageOut when chunk.Usage != nil
```

The `**Usage` (pointer-to-pointer) pattern keeps backward compatibility: callers that don't need the output pass `nil`, and the write is guarded with `if usageOut != nil`.

### Consumer guidance

After draining `stream.Events`, read `stream.Usage` directly — no need to track usage from events. It will be non-nil if the endpoint sent a usage chunk, nil otherwise.

```go
stream := client.StreamChat(ctx, request)
for event := range stream.Events { /* ... */ }
if stream.Usage != nil {
    fmt.Printf("tokens: %d\n", stream.Usage.TotalTokens)
}
```

### Verification

Tests in `internal/llm/stream_test.go` cover usage emission and absence. The existing `TestParseStreamEmitsUsage` and `TestParseStreamIgnoresEmptyUsage` remain valid since `parseStream` still works with `usageOut = nil`. Run with `go test ./internal/llm/...`.
