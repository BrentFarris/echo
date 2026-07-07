---
name: chat-streaming-usage-callback
description: How chat.go captures Stream.Usage after stream completion and exposes it via the onUsage callback on runChatTurnWithHistory.
triggers:
    - chat streaming
    - usage callback
    - token usage
    - runChatTurnWithHistory
    - streamAssistantResponseAttempt
    - chatStreamAttemptResult
    - onUsage
    - Stream.Usage
    - card-5
---

## Usage capture in chat streaming path

After card-4 exposed `Stream.Usage` synchronously on the LLM layer, card-5 wired it through `chat.go`'s streaming chain so callers of `runChatTurnWithHistory` can receive usage via callback.

### Flow

```
streamAssistantResponseAttempt
  → reads stream.Usage after events channel drains
  → returns in chatStreamAttemptResult.usage (*llm.Usage)

streamAssistantResponse
  → passes usage through from attempt result
  → returns (string, []ToolCall, bool, string, *Usage, error)

runChatTurnWithHistory(..., onUsage func(workspaceID string, usage llm.Usage))
  → calls onUsage(workspace.ID, *usage) when usage != nil && onUsage != nil
```

### chatStreamAttemptResult

```go
type chatStreamAttemptResult struct {
    content      string
    toolCalls    []llm.ToolCall
    finished     bool
    finishReason string
    loop         *streamLoopDetection
    usage        *llm.Usage       // added in card-5
    err          error
}
```

### Callback semantics

- `onUsage` is nil for internal callers (`SendChatMessage`, `EditChatMessage`, `runChatTurn`) — they pass `nil`.
- Callers that care about token accounting (e.g., Kanban agents, future billing) provide the callback.
- Callback fires after each stream attempt completes successfully; not called on errors or loop retries.
- Usage is a value copy (`*usage`), safe to use after the call returns.

### Key files

- `internal/services/chat.go`: `streamAssistantResponseAttempt`, `streamAssistantResponse`, `runChatTurnWithHistory`
- `internal/llm/client.go`: `Stream.Usage *Usage` field (card-4)

### Verification

`go build ./...` and `go test ./...` cover compilation and existing chat tests. No new test was added since the callback is nil for all existing callers and the usage plumbing follows the same pattern as other attempt result fields.
