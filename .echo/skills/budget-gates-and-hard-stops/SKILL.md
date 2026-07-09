---
name: budget-gates-and-hard-stops
description: How budget gates prevent kanban execution when token limits are exceeded, including heartbeat pre-checks, watchdog verification gating, and runtime hard stops during usage recording.
triggers:
    - budget gate
    - hard stop
    - budget exceeded
    - heartbeat budget check
    - RecordTokenUsage cancellation
    - onBudgetExceeded
    - tick_no_budget
    - kanban cancellation
    - watchdog budget check
    - watchdog tick skip
---

## Budget Gates and Hard Stops

Three mechanisms prevent kanban execution from consuming tokens beyond configured limits:

### 1. Heartbeat Pre-Check (Gate)
In `heartbeatTick` (`heartbeat.go`), before calling `StartKanbanExecution`:
```go
if allowed, _, err := s.CheckTokenBudget(workspaceID); err != nil || !allowed {
    s.emitHeartbeatEvent(HeartbeatEvent{
        WorkspaceID: workspaceID,
        Type:        "tick_no_budget",
        Message:     "Heartbeat tick: workspace over token budget",
    })
    return
}
```
This runs on every heartbeat interval tick. If no budget is configured (`Limit == 0`), `CheckTokenBudget` returns `(true, 0, nil)` — execution proceeds freely.

### 2. Watchdog Pre-Check (Gate)
In `watchdogTick` (`watchdog.go`), before running verification on Done cards:
```go
allowed, _, err := s.CheckTokenBudget(workspaceID)
if err != nil || !allowed {
    s.emitHeartbeatEvent(HeartbeatEvent{
        WorkspaceID: workspaceID,
        Type:        "tick_no_budget",
        Message:     "Watchdog tick skipped — token budget exceeded",
    })
    return
}
```
This runs on every watchdog interval tick. If budget is exceeded, verification is skipped and cards remain `WatchdogChecked == false`, allowing them to be re-checked on future ticks once budget is reset via `ResetTokenBudget`.

### 3. Runtime Hard Stop (Breach)
In `RecordTokenUsage` (`budget.go`), when usage recording detects the workspace just crossed its limit:
```go
budget.Used += tokens
exceeded := false
if budget.Used >= budget.Limit && !budget.Paused {
    budget.Paused = true
    exceeded = true
}
// ... save, unlock s.mu ...
if exceeded {
    s.onBudgetExceeded(workspaceID)
}
```

`onBudgetExceeded`:
1. Acquires `chatMu`, retrieves the active kanban run's cancel func from `s.kanbanRuns[workspaceID]`
2. Releases `chatMu`, calls `cancelRun()` to cancel the scheduler context
3. Emits `budget_exceeded` heartbeat event via `emitHeartbeatEvent`

### Lock Ordering
`RecordTokenUsage` must release `s.mu` before calling `onBudgetExceeded`, which acquires `chatMu`. The established lock order is `chatMu → mu` (e.g., `appendKanbanAgentProgress`). Calling `onBudgetExceeded` while holding `s.mu` would deadlock.

The fix: removed `defer s.mu.Unlock()` from `RecordTokenUsage`; all exit paths have explicit `s.mu.Unlock()` before returning, with the success path unlocking before the `exceeded` check.

### Event Types
HeartbeatEvent types (documented in `events.go`):
- `"started"` — heartbeat started
- `"stopped"` — heartbeat stopped
- `"tick_no_eligible"` — no eligible cards on tick
- `"tick_no_budget"` — budget exceeded, skipped start (heartbeat) or verification (watchdog)
- `"budget_exceeded"` — runtime breach during RecordTokenUsage; active run canceled

### Idempotency
`onBudgetExceeded` fires only once per budget cycle because `RecordTokenUsage` sets `exceeded = true` only when transitioning from `!Paused` to `Paused`. Subsequent calls find `budget.Paused == true` and skip the handler. Reset via `ResetTokenBudget` unpauses.
