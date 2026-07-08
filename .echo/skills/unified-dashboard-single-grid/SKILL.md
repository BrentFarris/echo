---
name: unified-dashboard-single-grid
description: 'Unified dashboard architecture: single widget grid with all 15 widgets available, no per-view filtering, simplified CRUD actions always using "dashboard" as view key.'
triggers:
    - unified dashboard
---

## Unified Dashboard Architecture

Echo has ONE unified dashboard, not per-view silos. The dashboard shows a widget grid where users can add/remove/reorder any of the 15 available widgets regardless of which view they came from.

### Key Files
- `frontend/src/app/dashboard/grid.ts` — Main widget grid renderer; `renderDashboardWidgets()` always renders the unified grid (no view parameter)
- `frontend/src/app/dashboard/widgets.ts` — Widget registry (`widgetRegistry`) and `allWidgetIds` constant containing ALL 15 widget IDs
- `frontend/src/app/dashboard/index.ts` — Entry point; `renderDashboard()` calls `renderDashboardWidgets()` with no arguments
- `frontend/src/app/state.ts` — `getDashboardWidgets("dashboard")` and `setDashboardWidgets("dashboard", ...)` always use `"dashboard"` as the key
- `frontend/src/app/actions.ts` — Widget CRUD actions (`widget-remove`, `widget-add`, `widget-move-up`, `widget-move-down`, etc.) all hardcode `"dashboard"` as view

### Invariants
- `dashboardViewMode` state property does NOT exist — it was removed during unification
- `open-view-dashboard` action does NOT exist — use `open-dashboard` instead
- Per-view dashboard buttons in view headers (chat, git, kanban, tasks) were REMOVED entirely
- Side nav dashboard button toggles between `open-dashboard` and `close-dashboard`
- Default layout has 10 starter widgets: chat-busy-status, kanban-summary, system-workspaces, chat-token-budget, tasks-overview, kanban-progress, git-branch, chat-recent, git-recent-commits, system-heartbeat

### Widget CRUD Pattern
```ts
const view: AppMode = "dashboard";  // Always "dashboard"
const widgets = getDashboardWidgets(view);
// ... modify widgets
setDashboardWidgets(view, widgets);
```

### Pitfalls
- `allWidgetIds` is a static constant in `widgets.ts` — don't use `availableWidgetsForView()` (removed)
- `renderDashboardWidgets()` takes NO parameters — callers should not pass a view argument
- Dev Studio layout code (renderStatusBar, renderBacklogPanel, renderKanbanPanel, renderChatSection) was removed — don't re-add it
