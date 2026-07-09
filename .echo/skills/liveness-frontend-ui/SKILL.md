---
name: liveness-frontend-ui
description: 'Liveness frontend UI: settings configuration toggle, card recovery badges, detail panel section with clear action, echo:liveness:event handling, and ClearKanbanCardRecovery backend method.'
triggers:
    - liveness UI
    - liveness settings
    - recovery badge
    - card detail liveness
    - clear recovery state
    - echo:liveness:event
    - applyLivenessEvent
    - ClearKanbanCardRecovery
    - SetLivenessConfig
    - GetLivenessConfig
    - stall timeout
    - liveness enforcement toggle
---

## Liveness Frontend UI Architecture

### Settings Configuration (card-31)
- Module: `frontend/src/app/liveness.ts` — standalone module following `budget.ts` pattern
- Config map: `livenessConfigs` (`Map<string, services.LivenessConfig>`) keyed by workspace ID
- Load/save: `loadLivenessConfig()`, `setLivenessConfig()` call `GetLivenessConfig` / `SetLivenessConfig` wrappers in `backend/services.ts`
- Render: `renderLivenessSettingsSection()` — toggle for enabled, number inputs for stall timeout (minutes), max auto retries, check interval (minutes)
- Input handler: `handleLivenessInput()` with `data-liveness-field` / `data-liveness-field-name` attributes; converts minutes ↔ nanoseconds via `durationToMinutes()` / `minutesToDuration()`
- Wired in: `settings/index.ts` imports + section in nav array + render call + input handler in `handleSettingsInput`; `bootstrap.ts` and `actions.ts` load on startup/workspace switch
- Default values when config not yet loaded: enabled=false, stallTimeout=10min, maxAutoRetries=3, checkInterval=1min

### Event Flow
- Backend emits `echo:liveness:event` via `emitLivenessEvent()` in `liveness.go`
- Frontend subscribes in `bootstrap.ts` → `applyLivenessEvent()` in `kanban/index.ts`
- Event types: `check_no_stalls` (silent), `stalled_reset` (toast + render), `stalled_escalated` (error toast + render), `*_board` variants (reload board)

### Card Recovery Badges
- Function: `renderRecoveryBadge(card)` in `kanban/index.ts`
- Shows on cards with `recoveryType` set (`auto-reset` or `escalated`)
- Badge positioned absolute top-right on `.kanban-card`
- CSS classes: `.recovery-badge.is-reset` (accent color) and `.recovery-badge.is-escalated` (danger color)
- Card gets class `has-recovery-badge` when badge present

### Detail Panel Liveness Section
- Function: `renderLivenessSection(card, workspaceID)` renders after progress transcript
- Shows when card has `recoveryType` or `autoRetriesUsed > 0`
- Displays status icon, label, retries count, and stalled timestamp
- Includes "Clear recovery state" button with `data-action="clear-card-recovery"`

### Clear Recovery Action
- Backend: `ClearKanbanCardRecovery(workspaceID, cardID)` in `liveness.go` — clears `autoRetriesUsed`, `recoveryType`, `stalledAt`
- Frontend wrapper: `ClearKanbanCardRecovery` in `backend/services.ts` (manual RPC for web mode)
- Action handler: `clear-card-recovery` in `actions.ts`

### CSS Locations
- Recovery badges: after `.kanban-card.is-unavailable` in `styles.css`
- Liveness section: `.liveness-section`, `.liveness-status`, `.liveness-status-icon`, `.liveness-status-text`, `.clear-recovery-button`

### Key Files
- `internal/services/liveness.go` — backend enforcement, events, ClearKanbanCardRecovery, Get/SetLivenessConfig
- `frontend/src/app/liveness.ts` — settings config module (load/save/render/handler)
- `frontend/src/app/kanban/index.ts` — render functions, applyLivenessEvent
- `frontend/src/app/actions.ts` — clear-card-recovery handler, load on workspace switch
- `frontend/src/backend/services.ts` — Get/SetLivenessConfig, ClearKanbanCardRecovery wrappers
- `frontend/src/styles.css` — badge and liveness section styles
- `frontend/wailsjs/go/models.ts` — LivenessConfig model (enabled, stallTimeout ns, maxAutoRetries, checkInterval ns)

### Pitfalls
- Go `time.Duration` serializes as nanoseconds; frontend must convert to/from minutes for user-facing inputs
- Wails bindings must be regenerated after Go struct changes
- Badge uses absolute positioning on `.kanban-card` which needs `position: relative` (already present)
- Liveness section only renders when recovery data exists — don't show empty section
- Settings config is workspace-scoped; always use `activeWorkspace().id` as the key
