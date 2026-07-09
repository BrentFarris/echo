---
name: liveness-enforcement-service
description: Liveness enforcement service detects stalled Kanban cards, classifies recovery as reset or escalation, and acts accordingly.
triggers:
    - liveness enforcement
    - stall detection
    - auto-reset
    - escalation
    - EnforceLiveness
    - LivenessConfig
    - stalled card recovery
    - Kanban liveness
---

## Liveness Enforcement Service

File: `internal/services/liveness.go`

### Purpose
Detects stalled InProgress Kanban cards and automatically recovers them — either by resetting to Ready (auto-retry) or escalating to Blocked after exhausting retries.

### Key Types

- **`LivenessConfig`** — `Enabled`, `StallTimeout` (default 10m), `MaxAutoRetries` (default 3), `CheckInterval` (default 1m).
- **`LivenessEvent`** — emitted via `echo:liveness:event`; types: `stalled_reset`, `stalled_escalated`, `check_no_stalls`.

### Workflow

1. `EnforceLiveness(workspaceID, cfg)` acquires `s.mu`, scans InProgress cards for the workspace.
2. A card is **stalled** when its last `ProgressTranscript` entry is older than `StallTimeout`, or it has no entries at all.
3. On first detection, `StalledAt` is set on the card.
4. **Recovery classification** (`classifyRecovery`): if `AutoRetriesUsed < MaxAutoRetries` → reset; otherwise → escalate.
5. **Reset**: increments `AutoRetriesUsed`, sets `RecoveryType = "auto-reset"`, moves to Ready, clears `StalledAt`, cancels running agent.
6. **Escalate**: sets `RecoveryType = "escalated"`, moves to Blocked, cancels running agent.
7. Events are emitted per-card with stall duration and retry count; a `_board` suffix event includes the updated board.

### Important Implementation Details

- Works by index into `s.state.KanbanCards` — never modifies copies. The `stalledIndex` struct holds `{index, cardID}` to operate on real state pointers.
- Agent cancellation uses `kanbanAgentKey(workspaceID, cardID)` and locks `s.chatMu`.
- `isStalledCard` is a pure function taking `KanbanCard` by value — safe for iteration.

### Dependencies

- Card-19 recovery fields: `AutoRetriesUsed`, `RecoveryType`, `StalledAt` on `KanbanCard`.
- `kanbanAgentKey()` from scheduler for agent cancellation.

### Tests

File: `internal/services/liveness_test.go` — 17 tests covering defaults, stall detection, classification boundaries, reset/escalation flows, mixed scenarios, and disabled config.
