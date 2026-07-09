---
name: task-epic-grouping-and-filtering
description: Epic field wiring across backend structs, tool schemas, and frontend task UI with collapsible groups and filter pills.
triggers:
    - task epic
---

## Task Epic Grouping and Filtering

Tasks support an optional `epic: string` field that groups related work items under collapsible sub-sections within each priority lane.

### Backend wiring

| Layer | File | Field added |
|---|---|---|
| Service model | `internal/services/tasks.go` | `Epic` on `WorkspaceTask`, `TaskInput`, `storedWorkspaceTask` |
| Tool types | `internal/tools/types.go` | `Epic` on `tools.WorkspaceTask`, `WorkspaceTaskCreateRequest`, `WorkspaceTaskUpdateRequest` |
| Tool schemas | `internal/tools/workspace_tasks.go` | `epic` property in `workspace_task_create` and `workspace_task_update` JSON schemas |

Key wiring points:
- `normalizeTaskInput` trims `input.Epic`
- `createWorkspaceTask` writes `Epic` to stored task
- `UpdateWorkspaceTask` mutation sets `task.Epic = normalized.Epic`
- `toolWorkspaceTask` passes `Epic` through
- `taskBoardFromData` includes `Epic` in the returned `WorkspaceTask`

### Frontend wiring

| Layer | File | Change |
|---|---|---|
| State | `frontend/src/app/state.ts` | `taskEpicFilter: Map<string, string>` per workspace |
| Types | `frontend/src/app/types.ts` | `epic: string` on `TaskEditorDraft` |
| UI | `frontend/src/app/tasks/index.ts` | Epic input in editor, grouping in lanes, filter pills |
| Models | `frontend/wailsjs/go/models.ts` | `epic` on `WorkspaceTask`, `TaskInput` (regenerate with `wails generate`) |

### Rendering behavior

- `renderTaskLane` groups tasks by epic value; empty epic → `"__ungrouped__"` key
- Multiple groups → each renders as `<details class="task-epic-group">` with collapsible header showing label + count
- Single group → cards render directly without wrapper (inline label if not ungrouped)
- Epic filter bar appears when at least one task has an epic; pills filter visible tasks

### Data migration

No schema bump required. Existing tasks have `epic: ""` which renders as "Ungrouped".
