---
name: dashboard-default-layouts-and-empty-states
description: Dashboard default layouts per view, empty state handling when no workspace selected, widget registry conventions, and unified grid-rendering architecture.
triggers:
    - dashboard layout
    - default dashboard
    - widget registry
    - empty state dashboard
    - add dashboard widget
    - dashboard per-view
    - dashboard index migration
    - remove mock data
---

## Dashboard Default Layouts

Default layouts are defined in `frontend/src/app/state.ts` → `defaultDashboardLayouts()`. Each view maps to an array of `DashboardWidget` objects with id, view, title, size, and order.

**Current per-view defaults:**
- **Chat**: chat-busy-status (small), chat-token-budget (wide), chat-recent (large), system-heartbeat (small)
- **Kanban**: kanban-summary (wide), kanban-progress (medium), kanban-done-count (small), system-workspaces (small)
- **Tasks**: tasks-overview (large), tasks-priority-strip (wide), system-heartbeat (small)
- **Git**: git-branch (small), git-change-count (small), git-recent-commits (large)
- **Code**: code-open-tabs (medium), code-workspace-status (small), system-heartbeat (small)
- **Dashboard**: chat-busy-status (small), kanban-summary (wide), system-workspaces (medium)

## Adding New Widgets

1. Add the widget ID to the `WidgetId` union in `frontend/src/app/types.ts`.
2. Write a renderer function in `frontend/src/app/dashboard/widgets.ts` — it receives `services.Workspace | null` and returns an HTML string. Use `activeWorkspace()` fallback pattern: `const workspace = ws ?? activeWorkspace();`.
3. Register in `widgetRegistry` with `renderer`, `defaultSize`, and `title`.
4. Add the widget ID to the relevant view(s) in `availableWidgetsForView()`.
5. Add a default entry in `defaultDashboardLayouts()` for the target view.

## Empty State Handling

When no workspace is selected, `renderDashboardWidgets()` in `grid.ts` returns early with a `.dashboard-no-workspace` empty state div instead of rendering broken widgets. Individual widget renderers also check `if (!workspace)` and return `<p class="widget-placeholder">No workspace selected.</p>`.

## Architecture

**`frontend/src/app/dashboard/index.ts`**: Minimal entry point. `renderDashboard()` delegates all view modes (including "dashboard") to `renderDashboardWidgets()`. Legacy command center functions were removed — the widget grid is now the sole rendering path.

**`frontend/src/app/dashboard/grid.ts`**: CSS Grid layout engine with edit mode controls, widget card rendering, add-widget panel, and empty-state fallbacks. Entry point: `renderDashboardWidgets(view)`.

**`frontend/src/app/dashboard/widgets.ts`**: Widget registry (`widgetRegistry`) mapping each `WidgetId` to a `{ renderer, defaultSize, title }` entry. Contains all renderer functions reading from live `state.*`.

## Important Facts

- Widget renderers read from live `state.*` only — no Wails bindings or backend service imports.
- `Workspace` model uses `displayName`, not `label` (generated from Go struct).
- Code view state is in `frontend/src/codeView/state.ts` → `codeStates` Map and `ensureCodeState()`.
- Dashboard layouts persist to backend via debounced `SaveDashboardLayout`.
