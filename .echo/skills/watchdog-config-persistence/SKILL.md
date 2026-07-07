---
name: watchdog-config-persistence
description: WatchdogConfig persistence in AppState via state.json — fields, conversions, clone, and default initialization.
triggers:
    - watchdog config persistence
    - WatchdogConfig
    - watchdog start stop
    - cloneWatchdogConfigs
---

## WatchdogConfig Persistence

`WatchdogConfig` (in `internal/services/watchdog.go`) has fields `Enabled bool` and `Interval time.Duration`. It persists in `AppState.WatchdogConfigs` as `map[string]WatchdogConfig`, keyed by workspace ID.

### Key files
- `internal/services/watchdog.go` — struct, `StartWatchdog`, `StopWatchdog`, `GetWatchdogConfig`
- `internal/services/system.go` — `AppState` field, `defaultAppState`, `cloneState`, `cloneWatchdogConfigs`
- `internal/services/state_persistence.go` — `storedAppState.WatchdogConfigs` for state.json

### Persistence flow
- `StartWatchdog` writes config to `state.WatchdogConfigs[workspaceID]` and calls `saveLocked()`.
- `StopWatchdog` deletes the entry and saves.
- On startup, `load()` unmarshals `storedAppState` which includes `WatchdogConfigs`; `stored.appState()` restores them into `AppState`.

### Clone / default
- `defaultAppState()` initializes `WatchdogConfigs: make(map[string]WatchdogConfig)`.
- `cloneState()` calls `cloneWatchdogConfigs()` to deep-copy the map before returning state to callers.

### Wails bindings
`StartWatchdog(workspaceID, cfg)`, `StopWatchdog(workspaceID)`, and `GetWatchdogConfig(workspaceID)` are exposed in generated bindings at `frontend/wailsjs/go/services/SystemService.*`.

### Pitfall: wails generate module drops some methods
Running `wails generate module` can drop methods that use types not recognized by the generator (e.g., `time.Time`, `http.Handler`). After running it, always diff `frontend/wailsjs/go/services/SystemService.d.ts` and `.js` against expected methods — manually restore any dropped bindings (such as `ClearKanbanCardRecovery`) before building.
