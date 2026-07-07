---
name: kanban-card-persistence
description: KanbanCard struct fields, JSON persistence via state.json and workspace autosave, cloneKanbanCard deep-copy requirements, and the WatchdogChecked flag for post-execution verification tracking.
triggers:
    - kanban card fields
    - KanbanCard struct
    - kanban persistence
    - cloneKanbanCard
    - state.json kanban
    - workspace autosave kanban
    - add kanban field
    - WatchdogChecked
    - watchdog
---

## KanbanCard persistence

`KanbanCard` (defined in `internal/services/kanban.go`) is persisted in `AppState.KanbanCards []KanbanCard` and serialized to `state.json`. Workspace-scoped autosave also includes cards in `workspaceAutosaveData`.

### Key files
- `internal/services/kanban.go` — `KanbanCard` struct definition, `cloneKanbanCard`, `cloneKanbanCards`.
- `internal/services/state_persistence.go` — `storedAppState.KanbanCards`, `workspaceAutosaveData.KanbanCards`, load/save conversions.
- `internal/services/watchdog.go` — watchdog service that reads/writes `WatchdogChecked`.

### Current struct fields (as of 2026-07-06)
```go
type KanbanCard struct {
    ID                 string                   `json:"id"`
    WorkspaceID        string                   `json:"workspaceId"`
    Title              string                   `json:"title"`
    Description        string                   `json:"description"`
    Direction          string                   `json:"direction,omitempty"`
    AcceptanceCriteria []string                 `json:"acceptanceCriteria"`
    Dependencies       []string                 `json:"dependencies,omitempty"`
    DependencyStatuses []KanbanDependencyStatus `json:"dependencyStatuses,omitempty"`
    BlockedBy          []string                 `json:"blockedBy,omitempty"`
    Eligible           bool                     `json:"eligible"`
    Lane               string                   `json:"lane"`
    Status             string                   `json:"status"`
    ProgressTranscript []KanbanProgressEntry    `json:"progressTranscript,omitempty"`
    AutoRetriesUsed    int                      `json:"autoRetriesUsed,omitempty"`
    RecoveryType       string                   `json:"recoveryType,omitempty"`
    StalledAt          *time.Time               `json:"stalledAt,omitempty"`
    WatchdogChecked    bool                     `json:"watchdogChecked,omitempty"`
}
```

### WatchdogChecked flag
- Added in card-23 to track whether a Done card has been verified by the watchdog service.
- Default value is `false` (zero value for bool).
- Set to `true` by `watchdogTick` after verification runs against workspace file changes.
- Cards with `WatchdogChecked == true` are skipped on subsequent watchdog ticks.
- See `internal/services/watchdog.go` for the full watchdog service.

### Adding new fields
- Use `omitempty` for optional fields so existing persisted cards deserialize with zero values.
- Always update `cloneKanbanCard` for pointer and slice fields:
  - Slices: `append([]T(nil), card.Field...)`
  - Pointers (e.g., `*time.Time`): copy the value behind the pointer if non-nil
  - Value types (`int`, `string`, `bool`) are safe by value — no action needed

### Persistence flow
1. `saveLocked()` serializes `AppState.KanbanCards` into `storedAppState`.
2. Workspace autosave (`workspaceAutosaveData`) includes a filtered, cloned subset of cards for the workspace.
3. On load, JSON unmarshal populates fields; missing optional fields get Go zero values.

### Cloning
`cloneKanbanCard` is called by `cloneKanbanCards` and by workspace autosave to produce independent copies. Any new pointer or slice field must be deep-copied here to prevent shared-mutable-state bugs across persisted snapshots and runtime cards.
