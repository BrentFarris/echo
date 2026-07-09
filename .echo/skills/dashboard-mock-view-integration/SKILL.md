---
name: dashboard-mock-view-integration
description: 'Dashboard widget grid renderer: CSS Grid layout engine, edit mode UI (add/remove/reorder), per-view navigation flow, and state-driven widget rendering replacing the static mock-up.'
triggers:
    - dashboard widget grid
    - per-view dashboard
    - open-view-dashboard
    - dashboard navigation
    - widget actions
    - reset dashboard layout
---

## Dashboard Widget Grid Architecture

### Overview
The dashboard view renders a configurable widget grid from `state.dashboardLayouts[view]`. It replaced the static mock-up with a live state-driven grid engine and edit mode UI.

### Key Files
- `frontend/src/app/dashboard/grid.ts` — Widget grid renderer: layout engine, card shell, edit controls, add-widget panel
- `frontend/src/app/dashboard/index.ts` — Thin wrapper calling `renderDashboardWidgets(view)` using `state.dashboardViewMode ?? "dashboard"`
- `frontend/src/app/state.ts` — `dashboardLayouts`, `dashboardEditMode`, `dashboardViewMode`, `dashboardPreviousMode` state fields; `getDashboardWidgets()`, `setDashboardWidgets()` helpers
- `frontend/src/app/actions.ts` — All dashboard action handlers (see below)
- `frontend/src/app/render.ts` — `buildMain()` passes `state.dashboardViewMode ?? "chat"` to `renderDashboard()`; left nav dashboard button uses per-view mode
- `frontend/src/styles.css` — Widget grid CSS appended at end (grid, cards, edit controls, picker, responsive)

### Grid Layout Engine
- 12-column CSS Grid (`grid-template-columns: repeat(12, minmax(0, 1fr))`)
- Widget sizes map to column/row spans:
  - `small`: span 3 columns × 1 row
  - `medium`: span 6 columns × 1 row
  - `large`: span 12 columns × 2 rows
  - `wide`: span 12 columns × 1 row
- Responsive overrides at ≤1440px (6-column grid) and ≤720px (single column, all widgets full-width via `!important`)

### Widget Card Shell
Each widget card has:
- `<article class="widget-card widget-size-{size}">` with inline `grid-column`/`grid-row` styles
- Header: title + edit controls (visible only in edit mode)
- Body: placeholder content per widget ID (to be replaced by actual widget renderers)

### Edit Mode UI
Controlled by `state.dashboardEditMode`:
- **On**: Each card shows move up/down/remove buttons; "Add Widget" panel lists available widgets filtered by what's already added; toolbar shows "Done" button
- **Off**: Clean cards with no controls; toolbar shows "Customize" button; empty state prompts to customize

### Available Widgets
Defined in `availableWidgets` map in `grid.ts`, keyed by `AppMode`. Each entry has `id`, `title`, `size`. The widget picker filters out already-added widgets.

### Per-View Dashboard Navigation

Each view (chat, tasks, kanban) has a dashboard entry button in its panel header using `data-action="open-view-dashboard" data-view="<view>"`. The left nav dashboard button also uses this action, passing the current `mode` as the view.

**State flow:**
- `open-view-dashboard`: sets `state.dashboardViewMode = view`, saves `state.dashboardPreviousMode = state.appMode`, switches to `"dashboard"` mode
- `close-dashboard`: restores `state.appMode = state.dashboardPreviousMode ?? "chat"`, clears dashboard mode
- `buildMain()` in render.ts passes `state.dashboardViewMode ?? "chat"` to `renderDashboard()`

This ensures that clicking the dashboard button from any view opens that view's dashboard, and closing returns to the originating view.

### Action Handlers (actions.ts)
All handlers are in `handleAction()` and return early without falling through:
- `dashboard-edit-toggle`: flips `state.dashboardEditMode`
- `widget-remove` / `remove-widget`: filters widget from layout by ID
- `widget-add` / `add-widget`: pushes new widget with lookup from `availableWidgets` map
- `widget-move-up` / `move-widget-up`: swaps widget with previous in array
- `widget-move-down` / `move-widget-down`: swaps widget with next in array
- `reset-dashboard-layout`: replaces current view's widgets with defaults from `defaultDashboardLayouts()`
- `open-view-dashboard`: sets per-view dashboard mode and preserves previous mode for return navigation

Both prefixed (`widget-*`) and unprefixed (`add-widget`, etc.) action names are handled as aliases.

### State Pattern
- `getDashboardWidgets(view)` returns widgets for a view, initializing from defaults if not set
- `setDashboardWidgets(view, widgets)` stores the updated array
- Widget arrays are mutated in-place (push/swap) then stored — this is intentional since the grid re-renders on every state change

### CSS Pattern
- All widget grid CSS appended to end of `styles.css`
- Uses existing CSS custom properties for theme compatibility
- Responsive breakpoints at 1440px and 720px match existing dashboard breakpoints

### Pitfalls
- `grid.ts` imports from `../state` and `../types` (parent directory), not `./state`
- Widget picker must filter by current view's available widgets, not all widgets
- Inline `grid-column`/`grid-row` styles are overridden with `!important` in responsive CSS
- Actions import `availableWidgets` from `./dashboard/grid` to look up widget definitions when adding
- Per-view dashboard uses `data-view` attribute on the button, not the current app mode — always read it from `target.dataset.view`
- `close-dashboard` restores from `dashboardPreviousMode`, which is set by both `open-dashboard` and `open-view-dashboard`
