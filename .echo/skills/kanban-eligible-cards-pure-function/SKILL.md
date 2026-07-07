---
name: kanban-eligible-cards-pure-function
description: Pure FindEligibleCards function and kanbanBoardCardsByID helper for lock-free Kanban card eligibility checks based on dependency resolution.
triggers:
    - kanban eligible
    - FindEligibleCards
    - dependency resolution
    - pure function kanban
    - kanban board filtering
    - lock-free kanban
---

## Kanban eligibility as pure functions

`FindEligibleCards(board KanbanBoard, limit int) []KanbanCard` in `internal/services/kanban.go` is a lock-free pure function that returns Ready cards whose dependencies are all Done.

### Signature and behavior

```go
func FindEligibleCards(board KanbanBoard, limit int) []KanbanCard
```

- Iterates only `board.Ready` cards.
- A card is eligible when every entry in its `Dependencies` slice has a matching card in `board.Done`.
- **Ghost dependencies** (IDs not found anywhere on the board) are treated as unblocked — this matches the existing `enrichKanbanCard` behavior.
- Returns at most `limit` cards; returns `nil` for `limit <= 0`.
- No locks are held; callers pass a snapshot `KanbanBoard`.

### Helper

`kanbanBoardCardsByID(board KanbanBoard) map[string]KanbanCard` builds a full-board lookup from all four lanes. Used internally by `FindEligibleCards`.

### Relationship to existing code

The scheduler's `eligibleReadyCards` method in `kanban_scheduler.go` holds `s.mu` and iterates `s.state.KanbanCards`. `FindEligibleCards` can replace that logic once callers load the board snapshot first, reducing lock scope.

### Test coverage (`kanban_test.go`)

11 tests cover independent cards, single/multi-dependency blocking, chain resolution (base → middle → final), limit enforcement, zero-limit edge case, ghost dependencies, In Progress dependency blocking, empty boards, and partial-done multi-dep scenarios.
