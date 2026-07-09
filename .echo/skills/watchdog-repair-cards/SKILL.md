---
name: watchdog-repair-cards
description: How watchdog verification failure generates Ready repair cards linked to the original failed Done cards, including metadata, dependencies, run-active guard, and event emission.
triggers:
    - repair card
    - verification failure
    - watchdog repair
    - generateRepairCardsFromVerification
    - kanbanVerificationRepairPrompt
    - watchdog tick verification failed
---

## Watchdog Repair Card Generation

When the watchdog (`watchdogTick` in `watchdog.go`) runs verification on Done cards with `WatchdogChecked == false` and verification fails, it generates Ready repair cards via `generateRepairCardsFromVerification`.

### Flow

1. `watchdogTick` collects unchecked Done card IDs
2. Runs `runKanbanVerification` against workspace file changes
3. Marks all unchecked cards as `WatchdogChecked = true` regardless of result
4. If verification failed (`!kanbanVerificationReportSucceeded(report)`), calls `generateRepairCardsFromVerification(workspaceID, uncheckedCardIDs, report)`

### Repair Card Metadata

Each repair card is created with:
- **ID**: Next sequential `card-N` via `nextKanbanCardNumberLocked()`
- **Title**: `"Repair: <original-title>"` — original title pulled from the failed card
- **Description**: `kanbanVerificationRepairPrompt(report)` — includes "Automatic verification failed" + full report text (changed paths, commands, results)
- **AcceptanceCriteria**: `["Fix the verification failure and re-verify."]`
- **Dependencies**: `[]string{failedCardID}` — depends on the original Done card
- **Lane/Status**: `KanbanLaneReady`
- **ProgressTranscript**: Entry noting `"Created by watchdog after verification failure of <cardID>."`

### Run-Active Guard

`generateRepairCardsFromVerification` checks `s.kanbanRuns[workspaceID]` under `chatMu`. If a run is active, it returns the current board without creating repair cards. This prevents card creation during agent execution.

### Events

After creating repair cards, `card_created` Kanban events are emitted for each repair card with the updated board. The original checked cards still receive their `watchdog_checked` events using the same final board.

### Key invariant

Repair cards depend on the original Done card, so they remain blocked until that card stays in Done (which it already is). This means repair cards become eligible immediately after creation, since their dependency is satisfied.

### Files

- `internal/services/watchdog.go` — `watchdogTick`, `generateRepairCardsFromVerification`
- `internal/services/kanban_verification.go` — `kanbanVerificationRepairPrompt`, `kanbanVerificationReportSucceeded`
- `internal/services/watchdog_test.go` — 4 tests covering failure creation, run-active guard, multiple cards, and no-repair-on-pass
