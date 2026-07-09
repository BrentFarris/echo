---
name: dashboard-layout-persistence
description: 'Dashboard layout persistence architecture: backend DashboardWidgetJSON struct, AppState field, Wails-bound methods, state.json serialization, and frontend debounced save/load flow.'
triggers:
    - dashboard layout persistence
---

## Dashboard Layout Persistence

### Backend structure

- `DashboardWidgetJSON` in `internal/services/system.go`:
  - Fields: `ID`, `View`, `Title`, `Size` (strings), `Order` (int)
  - JSON tags match the frontend widget shape

- `AppState.DashboardLayouts` is `map[string][]DashboardWidgetJSON` keyed by view name (e.g., "chat", "kanban", "dashboard")
- Persisted via `storedAppState.DashboardLayouts` in `internal/services/state_persistence.go` — serialized to `state.json` alongside settings, workspaces, configs

### Wails-bound methods

- `GetDashboardLayouts() map[string][]DashboardWidgetJSON` — returns deep clone under `mu`
- `SaveDashboardLayout(view string, widgets []DashboardWidgetJSON) error` — validates view, stores copy, calls `saveLocked()`

Both methods are whitelisted in `internal/webserver/server.go` `allowedRPCMethods`.

### Clone safety

- `cloneDashboardLayouts()` deep-copies the map and each widget slice
- Called from `cloneState()` when returning state to callers

### Frontend flow

- `loadDashboardLayoutsFromBackend()` in `frontend/src/app/state.ts`:
  - Called during `initialize()` in `bootstrap.ts` after `LoadState()`
  - Converts backend `Record<string, DashboardWidgetJSON[]>` to frontend `Record<AppMode, DashboardWidget[]>`
  - Falls back to defaults on error

- `setDashboardWidgets(view, widgets)` triggers `scheduleDashboardSave()`:
  - Debounced at 500ms via `window.setTimeout`
  - Iterates all views in `state.dashboardLayouts` and calls `SaveDashboardLayout` for each
  - Errors are non-fatal; layouts persist on next change or restart

- Service wrappers in `frontend/src/backend/services.ts`:
  - `GetDashboardLayouts()` and `SaveDashboardLayout(view, widgets)` use the standard `call()` pattern supporting both Wails runtime and web RPC

### Adding new persisted fields to AppState

1. Add field to `AppState` struct in `system.go`
2. Add matching field to `storedAppState` in `state_persistence.go`
3. Update `storedAppStateFrom()` and `appState()` conversion functions
4. Add clone helper and integrate into `cloneState()` if the field contains slices/maps
5. Run `wails generate module` to regenerate TypeScript bindings
