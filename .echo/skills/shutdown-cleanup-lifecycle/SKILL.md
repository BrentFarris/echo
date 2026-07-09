---
name: shutdown-cleanup-lifecycle
description: SystemService Shutdown method must cancel contexts and stop tickers for heartbeats, watchdogs, kanban runs/agents, and chat streams to prevent resource leaks.
triggers:
    - shutdown cleanup
    - ticker.Stop
    - resource leak
    - heartbeat ticker
    - watchdog ticker
    - SystemService Shutdown
    - cancel and stop
---

## Shutdown cleanup invariance

`SystemService.Shutdown()` (in `internal/services/kanban_scheduler.go`) is responsible for cleaning up all long-running goroutines and timers. The pattern is:

1. **Lock phase** (`chatMu.Lock`): Collect cancel functions and ticker pointers from all running handles — kanban runs, kanban agents, heartbeats, watchdogs, and chat streams. Also mark busy sessions as canceled.
2. **Unlock** (`chatMu.Unlock`).
3. **Cancel phase**: Call each collected `cancel()` to drain goroutine select loops on `ctx.Done()`.
4. **Stop tickers**: Call `ticker.Stop()` on all heartbeat and watchdog tickers after canceling their contexts. This releases the underlying timer resources.

### Pitfall: ticker leak

Heartbeat and watchdog handles (`heartbeatHandle`, `watchdogHandle`) each store a `*time.Ticker` and a `context.CancelFunc`. The goroutine backing each has `defer ticker.Stop()`, but if Shutdown only calls `cancel()` without also collecting and calling `ticker.Stop()`, the ticker channel is never consumed and the timer resource leaks until GC. Shutdown must stop tickers explicitly after canceling contexts.

### Verification

- `TestHeartbeatShutdownCancelsHeartbeats` confirms heartbeats are canceled on shutdown.
- `TestKanbanSchedulerShutdownCancelsActiveAgents` confirms agent cancellation.
- Run `go test ./internal/services/... -run "Shutdown|Heartbeat|Watchdog"` to verify the full cleanup path.
