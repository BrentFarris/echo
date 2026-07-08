---
name: kanban-auto-schedule-on-creation
description: 'Auto-scheduling on Ready card creation: removing run guards and triggering immediate scheduler startup when no run is active.'
triggers:
    - kanban auto-schedule
    - Ready card creation
    - CreateReadyKanbanCard
    - CreateKanbanCardFromChatMessage
    - StartKanbanExecution
    - auto-start scheduler
    - kanban run guard removal
---

## Auto-scheduling on Ready card creation

`CreateReadyKanbanCard` and `CreateKanbanCardFromChatMessage` no longer reject cards when a kanban run is active. After creating a card, both functions check if a run exists under `chatMu`; if not, they call `StartKanbanExecution(workspaceID, defaultAgentLimit)` to immediately process eligible cards. Errors from this call are ignored — the scheduler will skip gracefully on failure.

### Key files
- `internal/services/kanban.go`: `CreateReadyKanbanCard`, `CreateKanbanCardFromChatMessage` — auto-scheduling logic after card creation.
- `internal/services/kanban_scheduler.go`: `StartKanbanExecution` — entry point; returns early if run already active.
- `internal/services/workspace_autosave_test.go`: updated to stop any active run before manipulating cards, since auto-scheduling may move the card to inProgress.

### Lock ordering
1. Card creation holds `s.mu` to append the card and build the board.
2. Auto-schedule check acquires `s.chatMu` briefly to inspect `kanbanRuns`.
3. `StartKanbanExecution` acquires its own locks internally (`s.mu` then `s.chatMu`).

No deadlock because the auto-schedule check releases `chatMu` before calling `StartKanbanExecution`, and card creation releases `mu` before the check.

### Testing pitfalls
- Tests that create cards must account for the scheduler goroutine potentially moving the card to inProgress before assertions run.
- Use `StopKanbanExecution` before manipulating card state in tests, or assert lane-agnostic properties (card exists) rather than specific lanes.
