---
name: kanban-card-progress-debounce
description: Debounce card_progress DOM rendering to prevent mobile flicker during kanban agent streaming, using per-workspace setTimeout batching at 150ms intervals.
triggers:
    - kanban
    - debounce
    - card_progress
    - mobile flicker
    - streaming
    - DOM performance
    - agent execution
---

## Problem
Backend emits `card_progress` events for every token chunk during agent streaming (10-50 events/sec). Each event triggered synchronous DOM patching, causing mobile flicker and touch unresponsiveness.

## Solution
Per-workspace debounce on `card_progress` DOM rendering at 150ms intervals. State mutation remains immediate; only visual updates are batched.

### Key files
- `frontend/src/app/state.ts`: `kanbanRenderDebounceTimers: Map<string, number>` stores per-workspace timer handles.
- `frontend/src/app/kanban/index.ts`: `scheduleKanbanProgressPatch()` batches patches; `applyKanbanEvent` routes card_progress through debounce and returns early; `finishKanbanRun`/`forgetKanbanRun` clear pending timers.

### Invariants
- Non-progress events (`card_started`, `scheduler_complete`) render immediately via `renderKanbanEventPreservingScroll()`.
- Debounced patch cascade follows the same priority: open card detail → in-progress cards → board fallback.
- Timers are cleared on run completion to prevent stale patches.

### Pitfalls
- Do not debounce state updates — only DOM rendering. State must reflect current event data immediately.
- Always clear timers in `finishKanbanRun` and `forgetKanbanRun` to avoid patches firing after a run ends.
- The 150ms constant (`KANBAN_PROGRESS_DEBOUNCE_MS`) is tuned for ~6fps smoothness; changing it affects perceived responsiveness.
