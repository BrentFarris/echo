---
name: heartbeat-kanban-scheduler
description: 'Liveness enforcement integrated into heartbeat tick: EnforceLiveness runs before eligibility check, with SSE event emission and frontend handling for stalled card recovery.'
triggers:
    - heartbeat
    - liveness integration
    - EnforceLiveness
    - heartbeatTick
    - stalled card reset
    - stalled card escalation
    - echo:liveness:event
    - LivenessConfig
    - liveness heartbeat
---

## Liveness-Heartbeat Integration

`heartbeatTick` now calls `EnforceLiveness` at the very start of each tick, before the run-active guard and eligibility check. This ensures stalled cards are reset or escalated on every heartbeat cycle.

### Tick flow (ordered)

1. `GetLivenessConfig(workspaceID)` — reads persisted config
2. `EnforceLiveness(workspaceID, cfg)` — resets/escalates stalled InProgress cards; emits `echo:liveness:event` events
3. Run-active guard (`kanbanRuns`)
4. Budget check (`CheckTokenBudget`)
5. Eligibility check (`FindEligibleCards`)
6. Start scheduler or emit skip event

### Liveness Config Persistence

- `AppState.LivenessConfigs map[string]LivenessConfig` — persisted alongside heartbeat/watchdog configs
- `GetLivenessConfig(workspaceID)` — thread-safe under `s.mu`; returns zero value if not set (liveness disabled)
- `SetLivenessConfig(workspaceID, cfg)` — persists config; mirrors heartbeat pattern

### Liveness Events

Event name: `echo:liveness:event` (exported as `LivenessRuntimeEventName` in `events.go`).

Types emitted:
- `check_no_stalls` — liveness ran with no stalled cards (silent in frontend)
- `stalled_reset` — card auto-reset to Ready lane
- `stalled_escalated` — card escalated to Blocked after exhausting retries
- `stalled_reset_board` / `stalled_escalated_board` — board snapshot after changes

Events emit via both `emitRuntimeEvent` (web SSE subscribers) and Wails `runtime.EventsEmit` (desktop frontend).

### Frontend Wiring

- `LivenessEvent` type in `types.ts`
- `applyLivenessEvent()` in `kanban/index.ts` — shows toasts for reset/escalation, reloads board on `_board` events, silent for `check_no_stalls`
- Event listener in `bootstrap.ts`: `EventsOn("echo:liveness:event", applyLivenessEvent)`

### Key invariant

When liveness config is not set (zero `LivenessConfig{}`), `EnforceLiveness` returns immediately because `cfg.Enabled` is false. This means heartbeat ticks continue normally without liveness overhead unless the user explicitly enables it.
