---
name: heartbeat-config-persistence
description: HeartbeatConfig persistence in AppState via state.json — fields, conversions, clone, and default initialization.
triggers:
    - heartbeat config persistence
---

## Heartbeat config persistence

`HeartbeatConfig` (defined in `internal/services/heartbeat.go`) is persisted per-workspace in `AppState.HeartbeatConfigs map[string]HeartbeatConfig`.

### Key files
- `internal/services/heartbeat.go` — `HeartbeatConfig` struct definition (`Enabled bool`, `Interval time.Duration`).
- `internal/services/system.go` — `AppState` field, `defaultAppState()` initialization, `cloneState()` cloning, `cloneHeartbeatConfigs()` helper.
- `internal/services/state_persistence.go` — `storedAppState` field, `storedAppStateFrom()` and `appState()` conversions.

### Persistence flow
1. `saveLocked()` calls `storedAppStateFrom(s.state)` which copies `HeartbeatConfigs` into the stored struct.
2. JSON marshal writes `heartbeatConfigs` key to `state.json`.
3. On load, `json.Unmarshal` populates `storedAppState.HeartbeatConfigs`, then `appState()` copies it back into `AppState`.

### Cloning
`cloneState` uses `cloneHeartbeatConfigs` to deep-copy the map (values are value types so shallow copy per entry is sufficient). Always check for nil before cloning.

### Default init
`defaultAppState()` initializes `HeartbeatConfigs` as `make(map[string]HeartbeatConfig)` to avoid nil-map panics on write.
