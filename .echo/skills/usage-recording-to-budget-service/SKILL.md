---
name: usage-recording-to-budget-service
description: How usage recording is wired from chat and kanban streaming paths to the budget service RecordTokenUsage method, with graceful error handling.
triggers:
    - usage recording
    - budget service
    - RecordTokenUsage
    - token usage
    - chat.go
    - kanban_scheduler.go
    - onUsage callback
    - totalUsage
---

## Usage Recording to Budget Service

Captured token usage from completed LLM streams is recorded to the budget service via `RecordTokenUsage(workspaceID string, tokens int64)`.

### Chat path (`chat.go`)

`runChatTurnWithHistory` accepts an `onUsage func(workspaceID string, usage llm.Usage)` callback. All 3 callers (SendChatMessage, EditChatMessage/retry, runChatTurn) pass a closure:

```go
func(wid string, u llm.Usage) {
    _, _ = s.RecordTokenUsage(wid, int64(u.TotalTokens))
}
```

The callback is invoked inside `runChatTurnWithHistory` at line ~497 when usage is present. Errors are ignored so chat execution continues gracefully.

### Kanban agent path (`kanban_scheduler.go`)

`runKanbanAgent` accumulates per-turn usage into `var totalUsage llm.Usage`. A deferred function records the total at exit:

```go
defer func() {
    if totalUsage.TotalTokens > 0 {
        _, _ = s.RecordTokenUsage(workspace.ID, int64(totalUsage.TotalTokens))
    }
}()
```

This records accumulated usage on all exit paths (success, cancellation, error). Errors are ignored so agent execution continues gracefully.

### Budget service (`budget.go`)

`RecordTokenUsage` acquires `s.mu`, adds tokens to the workspace budget's `Used` count, auto-pauses when exceeded, and persists. When no budget is configured for a workspace, it returns immediately without error.

### Key invariant

Budget recording errors must never halt chat or kanban execution — always discard the returned error with `_, _ = ...`.
