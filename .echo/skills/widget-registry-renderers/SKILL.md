---
name: widget-registry-renderers
description: 'Widget registry architecture: how renderers read from state, the registry export shape, CSS class naming conventions, and empty-data placeholder patterns.'
triggers:
    - widget registry
    - dashboard widgets
    - widget renderer
    - live widget data
    - widget-grid integration
    - dashboard layout
---

## Widget Registry Architecture

**File:** `frontend/src/app/dashboard/widgets.ts`

### Core Pattern

Each widget renderer:
- Accepts `(workspace: services.Workspace | null)` parameter
- Falls back to `activeWorkspace()` when workspace is null
- Returns an HTML string (never throws, shows placeholder for empty data)
- Uses CSS custom properties — no hardcoded colors
- Reads only from `state.*` and helper maps (budget.ts, liveness.ts) — never imports Wails bindings

### Registry Export

```ts
export const widgetRegistry: Record<WidgetId, { renderer, defaultSize, title }>
export function availableWidgetsForView(view: AppMode): WidgetId[]
```

`grid.ts` derives a backwards-compatible `availableWidgets` map from the registry for use by `actions.ts`.

### Widget ID Convention

All IDs use kebab-case without `widget-` prefix: `chat-recent`, `kanban-summary`, `git-branch`, etc. The old `widget-*` prefix was removed to keep IDs concise.

### CSS Class Naming

Widget HTML uses `widget-*` prefixed classes for styling:
- Container: `widget-chat-recent`, `widget-kanban-summary`
- Items: `widget-chat-msg`, `widget-lane-badge`
- Shared: `widget-placeholder`, `status-dot`, `priority-badge`

### Empty Data Patterns

Every renderer handles three empty states:
1. No workspace: `"No workspace selected."`
2. No data for widget: widget-specific placeholder (e.g., `"No Kanban cards."`)
3. Partial data: render what's available, omit missing sections

### Progress Calculation

`cardProgressPercent()` mirrors the logic in `kanban/index.ts`: tool_call count vs acceptance criteria length, capped at 97% for in-progress cards. Do not diverge — keep both implementations in sync.

### Grid Integration

`grid.ts` delegates to the registry via `renderWidgetContent(widgetId, workspace)`. The add-widget panel uses `availableWidgetsForView()` and looks up title/size from the registry.

### Verification

- `cd frontend; npm run build` — must compile with zero errors
- All WidgetId values must appear as keys in `widgetRegistry`
- `availableWidgetsForView` must cover all AppMode values (including default return)
