---
name: token-budget-service
description: Token budget service tracks per-workspace token limits, usage, and pause state; persists in state.json.
triggers:
    - token budget
    - token usage tracking
    - budget per workspace
    - SetTokenBudget
    - CheckTokenBudget
    - RecordTokenUsage
    - ResetTokenBudget
    - budget persistence
---

## Token Budget Service

Location: `internal/services/budget.go`

### Structures

- `TokenBudget` — per-workspace budget with `Limit` (0 = unlimited), `Used`, and `Paused` fields.
- `TokenBudgetService` — holds `map[string]TokenBudget` keyed by workspace ID. No separate mutex; all access is under `SystemService.mu`.

### SystemService Methods

| Method | Purpose |
|---|---|
| `SetTokenBudget(workspaceID, limit)` | Set/update budget limit for a workspace. Resets used/paused. |
| `CheckTokenBudget(workspaceID)` | Returns `(allowed bool, remaining int64)`. Auto-pauses on exceed. |
| `RecordTokenUsage(workspaceID, tokens)` | Adds tokens to used count; auto-pauses if exceeded. |
| `ResetTokenBudget(workspaceID)` | Clears used to 0, unpauses. Preserves limit. |
| `GetTokenBudget(workspaceID)` | Returns current budget (empty struct if none set). |

### Persistence

- Serialized in `state.json` under the `tokenBudgets` key (`map[string]TokenBudget`).
- `storedAppState.TokenBudgets` added to `internal/services/state_persistence.go`.
- `saveLocked()` copies `s.tokenBudget.budgets` into the stored state.
- `load()` restores budgets from `stored.TokenBudgets`.
- Budget entry is deleted on workspace deletion in `DeleteWorkspace`.

### Concurrency

All budget methods acquire `s.mu` (the main service lock). `TokenBudgetService` has no separate mutex — this prevents deadlocks since `saveLocked()` is always called with `s.mu` held.

### Pitfalls

- Do not add a separate mutex to `TokenBudgetService`; it will deadlock with `saveLocked()`.
- Map value assignment (`s.tokenBudget.budgets[id].Paused = true`) fails in Go — copy, mutate, reassign.
- Budget check returns `remaining=0` when paused; caller should treat `allowed=false` as the signal.
