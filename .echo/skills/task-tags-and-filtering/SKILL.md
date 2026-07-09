---
name: task-tags-and-filtering
description: Tag field wiring across backend structs, tool schemas, and frontend task UI with tag chips and multi-select OR-logic filter pills.
triggers:
    - task tags
    - tag chips
    - tag filter
    - backlog tags
    - free-form labels
---

## Task Tags Architecture

Tags are free-form `string[]` labels on backlog tasks (e.g. `frontend`, `bug`, `performance`). They follow the same structural pattern as epics but with different UI semantics.

### Backend structs — three layers, all need `Tags []string`

| Layer | File | Structs |
|-------|------|---------|
| Service model | `internal/services/tasks.go` | `WorkspaceTask`, `TaskInput`, `storedWorkspaceTask` |
| Tool types | `internal/tools/types.go` | `tools.WorkspaceTask`, `WorkspaceTaskCreateRequest`, `WorkspaceTaskUpdateRequest` |

Field definition: `Tags []string \`json:"tags,omitempty"\``

### Normalization

- `normalizeTaskInput()` calls `normalizeTaskTags()` which trims whitespace and filters empty strings (same pattern as `normalizeTaskCriteria`).
- Tool handlers in `internal/tools/workspace_tasks.go` also trim/filter tags before passing to the provider.
- Empty/nil tags are fine — `omitempty` handles backward compatibility with existing tasks.

### Conversion function

`toolWorkspaceTask()` in `services/tasks.go` must copy tags: `Tags: append([]string(nil), task.Tags...)`.

`taskBoardFromData()` must include tags when building `WorkspaceTask` from stored data.

### Frontend

- **State:** `taskTagFilters: Map<string, Set<string>>` per workspace in `state.ts`
- **Type:** `tags: string` (comma-separated) on `TaskEditorDraft` in `types.ts`
- **Input:** Comma-separated text input in task editor form (`data-task-tags`)
- **Parsing on submit:** `.split(",").map(v => v.trim()).filter(Boolean)`
- **Chips:** Rendered as `.task-tag-chip` pills on task cards (`.task-card-tags`) and detail view (`.task-detail-tags`)
- **Filter bar:** `.task-tag-bar` with `.task-tag-btn` pills; multi-select with OR logic (tasks matching *any* selected tag are shown)
- **Patch rendering:** `patchTaskPanel()` re-renders the tag bar and applies tag filtering alongside epic, search, and mode filters

### CSS classes

| Class | Purpose |
|-------|---------|
| `.task-tag-bar` | Scrollable pill row container |
| `.task-tag-bar-label` | "Tags:" label |
| `.task-tag-btn` | Individual filter pill button |
| `.task-tag-btn.is-active` | Active/inactive state styling |
| `.task-tag-chip` | Small rounded pill on task cards/detail |
| `.task-card-tags` | Chip container on task card |
| `.task-detail-tags` | Chip container in detail view |

### Pitfalls

- Adding a field to `WorkspaceTask` requires updating **all three** backend struct layers plus the conversion function, or data will be silently dropped.
- Tool handler tag normalization must match service-level normalization to avoid double-trimming issues (they both trim+filter, which is idempotent).
- Tag filter state is per-workspace; clearing it on workspace switch prevents stale filters.
