---
name: task-within-lane-reorder
description: Task within-lane reordering via drag-and-drop with sortOrder persistence, including backend API, tool registration, frontend drop indicators, and Wails binding patterns.
triggers:
    - task reorder
    - within-lane drag
    - sortOrder
    - backlog ordering
    - drag-and-drop tasks
    - drop indicator
    - ReorderWorkspaceTasks
---

## Task Within-Lane Reordering Architecture

### Overview
Backlog tasks within a priority lane (P0/P1/P2) can be manually reordered via drag-and-drop. The `sortOrder` integer field persists ordering; existing tasks default to 0 and sort by `CreatedAt` descending as tiebreaker.

### Backend (`internal/services/tasks.go`)
- `WorkspaceTask.SortOrder int` — persisted in tasks.json via `storedWorkspaceTask`
- `ReorderWorkspaceTasks(workspaceID, taskIDs, targetPriority)` — assigns sequential sort orders (0, 1, 2...) to all tasks in a priority lane. Validates IDs exist, deduplicates, skips completed tasks.
- `taskBoardFromData` sorting: primary ascending by `SortOrder`, secondary descending by `CreatedAt`, tertiary ascending by `ID`.
- Emits `"reordered"` event type via `emitTaskEvent`.

### Tool (`internal/tools/workspace_tasks.go`)
- `workspace_task_reorder` tool registered with `taskIDs[]` + `priority` params.
- `WorkspaceTaskReorderRequest/Response` in `types.go`.
- `tools.WorkspaceTasksProvider.ReorderWorkspaceTasks` interface method.
- `tools.WorkspaceTask.SortOrder` field included in tool schema.

### Frontend (`frontend/src/app/tasks/index.ts`)
- Each task card wrapped in `<div class="task-card-drop-zone">` with a `.task-drop-indicator` child.
- Drop zone handlers: `handleTaskDropZoneDragOver`, `handleTaskDropZoneDragLeave`, `handleTaskDropZoneDrop`.
- Within-lane drop logic: removes dragged task from lane order, inserts before target, computes new ID list.
- Cross-lane drops still handled at lane level via original `handleTaskDrop` — only triggers when priorities differ.
- Optimistic UI with rollback on failure; `arraysEqual()` guards against no-op reorders.
- `clearAllDropIndicators()` resets visual state on drop/drag-end.

### CSS (`frontend/src/styles.css`)
- `.task-card-drop-zone`: flex column wrapper for card + indicator.
- `.task-drop-indicator`: 3px height line, transparent by default, transitions to `var(--color-accent-strong)` when `.is-visible`.
- Lane-level `.is-drop-target` styling only applies for cross-lane drops (priority mismatch check in `handleTaskDragOver`).

### Wails Bindings
- `ReorderWorkspaceTasks` added to `SystemService.d.ts`, `SystemService.js`, and `services.ts` wrapper.
- `sortOrder: number` field added to `WorkspaceTask` class in `models.ts`.

### Data Migration
- Existing tasks have `sortOrder: 0` (Go zero value). Sort falls back to `CreatedAt` descending — preserves current behavior.
- First within-lane reorder assigns explicit sequential orders to all tasks in that lane.

### Verification
- `go test ./...` passes
- `cd frontend; npm run build` passes
