# Orchestrator Feature Plan

**Status**: Draft
**Date**: 2026-07-06

---

## Overview

Echo is fundamentally **reactive** — the user sends a message, Echo responds. Paperclip is **proactive** — its control plane wakes agents on schedule to continue unfinished work. This plan bridges that gap by adding persistent autonomous execution as an **opt-in first-party feature** inside Echo's existing service layer and UI.

**Core principle**: Orchestration is opt-in. Default behavior remains on-demand. Users toggle autonomous mode when they want persistent background work.

---

## Problem Statement

Today's flow:
1. User chats → gets a plan
2. Plan decomposes into Kanban cards
3. User clicks **Run** → agents execute eligible cards
4. When done, execution stops. No further action until user intervenes.

Gaps:
- **No autonomous continuation**: If you decompose 8 cards and walk away, nothing happens until you return and click Run again.
- **No cost awareness**: Token usage is unbounded; long-running chains can exceed budgets silently.
- **Silent stranded cards**: Interrupted or stalled in-progress cards restore as `blocked` with no visible recovery path.
- **No verification watchdog**: Completed cards are marked done based on post-execution checks only — no ongoing monitoring.

---

## Solution Design

Four features, three implementation phases:

| Feature | Phase | Effort | Impact |
|---|---|---|---|
| Autonomous Heartbeat Loop | 1 | ~400 lines Go + UI | High |
| Budget Hard Stops | 2 | ~200 lines Go + UI | Medium |
| Liveness Contract Enforcement | 3 | ~150 lines Go + UI | Medium |
| Verification Watchdog Mode | 3 | Reuses existing infrastructure | Medium |

---

## Feature 1: Autonomous Heartbeat Loop

### Description

A background ticker that periodically scans for eligible Kanban cards and auto-starts them without user presence. User enables via toggle in the Kanban header.

### Behavior

- **Configurable interval**: 1m, 5m (default), or 15m
- **Per-workspace config**: Each workspace has independent heartbeat settings
- **Eligibility check**: Only starts cards in Ready lane with all dependencies Done
- **Budget gate**: Skips execution if token budget exceeded
- **Idempotent**: If a run is already active, the heartbeat tick is a no-op

### Backend Design

**New file**: `internal/services/heartbeat.go`

```go
type HeartbeatConfig struct {
    Enabled  bool          `json:"enabled"`
    Interval time.Duration `json:"interval"` // 1m, 5m, 15m
}

// HeartbeatHandle manages a running heartbeat for one workspace.
type heartbeatHandle struct {
    ticker *time.Ticker
    cancel context.CancelFunc
}

func (s *SystemService) StartHeartbeat(workspaceID string, cfg HeartbeatConfig) error {
    s.chatMu.Lock()
    defer s.chatMu.Unlock()

    if s.heartbeats[workspaceID] != nil {
        s.heartbeats[workspaceID].cancel()
    }

    ticker := time.NewTicker(cfg.Interval)
    ctx, cancel := context.WithCancel(context.Background())

    go func() {
        for range ticker.C {
            select {
            case <-ctx.Done():
                return
            default:
                s.heartbeatTick(workspaceID)
            }
        }
    }()

    s.heartbeats[workspaceID] = &heartbeatHandle{ticker, cancel}
}

func (s *SystemService) StopHeartbeat(workspaceID string) {
    s.chatMu.Lock()
    defer s.chatMu.Unlock()

    if h := s.heartbeats[workspaceID]; h != nil {
        h.cancel()
        h.ticker.Stop()
        delete(s.heartbeats, workspaceID)
    }
}

func (s *SystemService) heartbeatTick(workspaceID string) {
    board := s.loadKanbanBoard(workspaceID)
    eligible := findEligibleCards(board)

    if len(eligible) == 0 || s.isRunningKanban(workspaceID) {
        return
    }

    // Check budget before starting
    if err := s.checkBudget(workspaceID); err != nil {
        s.emitEvent("echo:heartbeat:event", map[string]any{
            "workspaceId": workspaceID,
            "type":        "budget_exceeded",
            "message":     err.Error(),
        })
        return
    }

    s.runKanbanScheduler(workspaceID, eligible)
}
```

**Modified file**: `internal/services/system.go` — add heartbeat map field:

```go
type SystemService struct {
    // ... existing fields ...
    heartbeats map[string]*heartbeatHandle // workspaceID -> running heartbeat
}
```

**Modified file**: `internal/services/state_persistence.go` — persist/restore heartbeat config.

### State Persistence

Add to `state.json`:

```json
{
  "heartbeatConfig": {
    "workspace-123": {
      "enabled": true,
      "intervalSeconds": 300
    }
  }
}
```

Restore heartbeats on startup if previously enabled.

### UI Design

**Kanban header controls** (between workspace name and Run button):

```
┌─────────────────────────────────────────────────────────┐
│  Kanban                     ⚡ Auto (5m)                │
│  echo                       ▶ Run         □             │
└─────────────────────────────────────────────────────────┘
```

- **Auto toggle button**: Cycles Off → `Auto (1m)` → `Auto (5m)` → `Auto (15m)` → Off
- **Icon**: Lightning bolt (`⚡`) when active, gear (`⚙`) when inactive
- **Label**: Shows interval when active, "Auto" when inactive

**Runtime status header** — extend existing runtime display:

```
● Working 0:42              (current behavior)
⚡ Auto-working 3 cycles    (autonomous mode active)
   Budget: 12K/50K tokens
```

### Events

New event type for SSE streaming to frontend and web clients:

```go
type HeartbeatEvent struct {
    WorkspaceID string `json:"workspaceId"`
    Type        string `json:"type"` // "started", "budget_exceeded", "no_eligible"
    Message     string `json:"message,omitempty"`
}
```

Event name: `echo:heartbeat:event`

---

## Feature 2: Budget Hard Stops

### Description

Per-workspace token budgets with hard-stop behavior. When exceeded, autonomous execution pauses and the user is notified.

### Behavior

- **Configurable limit**: Set per workspace in settings (default: unlimited)
- **Token tracking**: Record usage from LLM client responses after each call
- **Hard stop**: Pause heartbeat + cancel active run when budget exceeded
- **Visual indicator**: Progress bar in Kanban header showing remaining budget
- **Reset**: Budget resets on user action (manual reset or new session)

### Backend Design

**New file**: `internal/services/budget.go`

```go
type TokenBudget struct {
    Limit int64 `json:"limit"`   // 0 = unlimited
    Used  int64 `json:"used"`
    Paused bool `json:"paused"`
}

func (s *SystemService) SetTokenBudget(workspaceID string, limit int64) error
func (s *SystemService) CheckBudget(workspaceID string) error
func (s *SystemService) RecordUsage(workspaceID string, tokens int64) error
func (s *SystemService) ResetBudget(workspaceID string) error

func (b *TokenBudget) Remaining() int64 {
    if b.Limit == 0 {
        return math.MaxInt64 // unlimited
    }
    return max(0, b.Limit-b.Used)
}

func (b *TokenBudget) PercentageRemaining() float64 {
    if b.Limit == 0 {
        return 100
    }
    return float64(b.Remaining()) / float64(b.Limit) * 100
}
```

**Modified file**: `internal/llm/client.go` — surface token usage from LLM responses:

```go
type Usage struct {
    PromptTokens     int64 `json:"prompt_tokens"`
    CompletionTokens int64 `json:"completion_tokens"`
    TotalTokens      int64 `json:"total_tokens"`
}
```

**Modified file**: `internal/services/chat.go` — call `RecordUsage` after each LLM response.

### State Persistence

Add to `state.json`:

```json
{
  "tokenBudgets": {
    "workspace-123": {
      "limit": 50000,
      "used": 12400,
      "paused": false
    }
  }
}
```

### UI Design

**Budget badge in Kanban header**:

```
┌─────────────────────────────────────────────────────┐
│  Budget: ████████████░░░░░░░░ 75% (37.5K/50K)      │
└─────────────────────────────────────────────────────┘
```

- **Green** when > 50% remaining
- **Yellow** when 20–50% remaining
- **Red** when < 20% remaining
- **Clicking opens budget settings panel**

**Budget exceeded toast**: "Budget exceeded (50K tokens). Autonomous execution paused. [Reset] [Settings]"

### Settings Integration

Add budget configuration to workspace settings:

```
Token Budget
─────────────
□ Enable token budget limit
[ 50000 ] tokens per session
[Reset Budget] button
```

---

## Feature 3: Liveness Contract Enforcement

### Description

Periodic scan for stalled or stranded Kanban cards with explicit three-tier recovery paths.

### Three-Tier Recovery Model

| Tier | Condition | Action |
|---|---|---|
| **Auto-retry** | Card stalled, ownership clear, only continuity lost | Reset card to Ready, requeue on next heartbeat |
| **Explicit recovery action** | Problem is bounded but needs judgment | Create visible recovery badge + repair options in card detail |
| **Human escalation** | Board judgment required (ambiguous failure, external dependency) | Surface to user via toast + notification sound + red badge |

### Backend Design

**New file**: `internal/services/liveness.go`

```go
type LivenessConfig struct {
    StalledThreshold time.Duration // default: 3 minutes no progress
    AutoRetryLimit   int           // default: 1 auto-retry per card
}

func (s *SystemService) EnforceLiveness(workspaceID string, cfg LivenessConfig) {
    board := s.loadKanbanBoard(workspaceID)
    now := time.Now()

    for _, card := range board.InProgress {
        if !isStalled(card, now, cfg.StalledThreshold) {
            continue
        }

        switch classifyRecovery(card) {
        case recoveryAutoRetry:
            if card.autoRetriesUsed < cfg.AutoRetryLimit {
                s.resetCard(workspaceID, card.ID)
                addProgressEntry(card, "auto_retry", "Stalled — auto-retrying (attempt "+strconv.Itoa(card.autoRetriesUsed+1)+")")
            } else {
                s.escalateToHuman(workspaceID, card)
            }
        case recoveryExplicit:
            s.createRecoveryAction(workspaceID, card)
        case recoveryEscalate:
            s.escalateToHuman(workspaceID, card)
        }
    }
}

func isStalled(card KanbanCard, now time.Time, threshold time.Duration) bool {
    if len(card.ProgressTranscript) == 0 {
        return false
    }
    lastEntry := card.ProgressTranscript[len(card.ProgressTranscript)-1]
    // Use timestamp from last progress entry or card startedAt
    elapsed := now.Sub(lastEntry.Timestamp)
    return elapsed > threshold
}

func classifyRecovery(card KanbanCard) recoveryType {
    lastEntry := card.ProgressTranscript[len(card.ProgressTranscript)-1]
    switch lastEntry.Status {
    case "error":
        // Transient errors (network, timeout) → auto-retry
        if isTransientError(lastEntry.Content) {
            return recoveryAutoRetry
        }
        // Bounded problems (test failure, compile error) → explicit action
        return recoveryExplicit
    case "timeout":
        return recoveryAutoRetry
    default:
        // No clear signal → escalate to human
        return recoveryEscalate
    }
}
```

**Modified file**: `internal/services/kanban.go` — add recovery fields to card model:

```go
type KanbanCard struct {
    // ... existing fields ...
    AutoRetriesUsed int     `json:"autoRetriesUsed,omitempty"`
    RecoveryType    string  `json:"recoveryType,omitempty"` // "auto_retry", "explicit", "escalate"
    StalledAt       *time.Time `json:"stalledAt,omitempty"`
}
```

### Integration with Heartbeat

Liveness enforcement runs as part of each heartbeat tick, before checking eligibility:

```go
func (s *SystemService) heartbeatTick(workspaceID string) {
    // 1. Enforce liveness first (may reset stalled cards → Ready)
    s.EnforceLiveness(workspaceID, defaultLivenessConfig)

    // 2. Check for eligible cards
    board := s.loadKanbanBoard(workspaceID)
    eligible := findEligibleCards(board)
    // ... rest of heartbeat logic
}
```

### UI Design

**Card badges on Kanban board**:

```
┌──────────────────────────────┐
│ Swap workspace heading       │ CARD-1 ● Auto-retry available
│                              │
│ Modify the render() function │ ⚠ Stalled (3m no progress)
│ ...                          │ 🔴 Needs your attention
└──────────────────────────────┘
```

- **Green dot** (`●`) — auto-retry available
- **Yellow triangle** (`⚠`) — stalled, explicit recovery action needed
- **Red circle** (`🔴`) — human escalation required

**Card detail panel additions**:

```
⚠ Liveness Check
─────────────────
Card stalled for 3 minutes with no progress update.

Last activity: "Running tests..." (3m ago)

[Auto-retry]  [Add direction & reset]  [Escalate to human]
```

**Toast notifications**:
- Auto-retry: Silent (no toast, just happens)
- Explicit recovery: "CARD-1 stalled. Open card for recovery options."
- Human escalation: "CARD-1 needs your attention. Check card details." + notification sound

---

## Feature 4: Verification Watchdog Mode

### Description

Persistent background verification of completed Kanban cards. Extends existing `kanban_verification.go` from post-execution-only to ongoing monitoring.

### Behavior

- **Runs as agent mode**: Uses Echo's built-in tools (read files, run tests, search code)
- **Scheduled checks**: Periodically reviews done cards for regression or false-positive completion
- **Creates repair tasks**: If verification fails, creates a new Ready card describing the issue
- **Configurable frequency**: Separate from heartbeat interval (e.g., every 30 minutes)

### Backend Design

**New file**: `internal/services/watchdog.go`

```go
type WatchdogConfig struct {
    Enabled     bool          `json:"enabled"`
    Interval    time.Duration `json:"interval"` // default: 30m
}

func (s *SystemService) StartWatchdog(workspaceID string, cfg WatchdogConfig) error
func (s *SystemService) StopWatchdog(workspaceID string)

func (s *SystemService) watchdogTick(workspaceID string) {
    board := s.loadKanbanBoard(workspaceID)
    
    // Check recent done cards (within last hour) for verification
    recentDone := filterRecentDone(board.Done, time.Hour)
    
    for _, card := range recentDone {
        if card.verificationWatchdogChecked {
            continue
        }

        result := s.runVerification(workspaceID, card)
        if !result.Passed {
            // Create repair card
            s.createRepairCard(workspaceID, card, result)
        }
    }
}
```

**Reuse**: Existing `kanban_verification.go` `RunVerification` method.

### UI Design

Watchdog status shown in runtime header when active:

```
⚡ Auto-working 3 cycles  🔍 Watchdog active
   Budget: 12K/50K tokens     Next check in 22m
```

Repair cards created by watchdog have a special badge: `🔍 Verification repair`

---

## Implementation Phases

### Phase 1: Heartbeat Loop (Week 1)

**Scope**: Autonomous card execution with configurable interval.

**Files to create**:
- `internal/services/heartbeat.go` — heartbeat service, config model, tick logic

**Files to modify**:
- `internal/services/system.go` — add heartbeat map field
- `internal/services/state_persistence.go` — persist/restore heartbeat config
- `internal/services/kanban_scheduler.go` — expose eligibility check for reuse
- `frontend/src/app/kanban/index.ts` — Auto toggle button, runtime status extension
- `frontend/src/app/state.ts` — heartbeat state fields
- `frontend/src/app/events.ts` — handle `echo:heartbeat:event`

**Verification**:
- `go test ./...` passes
- Heartbeat starts/stops correctly per workspace
- Eligible cards auto-start on interval
- No execution when run already active (idempotent)
- Budget check gates execution (placeholder until Phase 2)

### Phase 2: Budget Hard Stops (Week 2)

**Scope**: Token budget tracking with hard-stop behavior.

**Files to create**:
- `internal/services/budget.go` — token budget model, check/record methods

**Files to modify**:
- `internal/llm/client.go` — surface usage stats from responses
- `internal/services/chat.go` — call RecordUsage after each LLM response
- `internal/services/state_persistence.go` — persist/restore budgets
- `frontend/src/app/kanban/index.ts` — budget badge in header, exceeded toast
- `frontend/src/app/settings/` — budget configuration UI

**Verification**:
- Budget correctly tracks usage across LLM calls
- Execution pauses when limit exceeded
- Visual indicator updates in real-time
- Budget resets on user action

### Phase 3: Liveness & Watchdog (Week 3)

**Scope**: Stalled card detection, three-tier recovery, verification watchdog.

**Files to create**:
- `internal/services/liveness.go` — liveness enforcement, classification logic
- `internal/services/watchdog.go` — verification watchdog service

**Files to modify**:
- `internal/services/kanban.go` — add recovery fields to card model
- `internal/services/heartbeat.go` — integrate liveness check into tick
- `frontend/src/app/kanban/index.ts` — card badges, recovery UI in detail panel
- `frontend/src/app/events.ts` — handle liveness events

**Verification**:
- Stalled cards detected after threshold
- Auto-retry works for transient failures
- Explicit recovery actions create visible options
- Human escalation surfaces via toast + sound
- Watchdog creates repair cards for failed verification

---

## State Model Summary

### New persisted state in `state.json`:

```json
{
  "heartbeatConfig": {
    "<workspace-id>": {
      "enabled": true,
      "intervalSeconds": 300
    }
  },
  "tokenBudgets": {
    "<workspace-id>": {
      "limit": 50000,
      "used": 12400,
      "paused": false
    }
  },
  "watchdogConfig": {
    "<workspace-id>": {
      "enabled": true,
      "intervalSeconds": 1800
    }
  }
}
```

### New fields on `KanbanCard`:

```go
type KanbanCard struct {
    // ... existing fields ...
    AutoRetriesUsed int       `json:"autoRetriesUsed,omitempty"`
    RecoveryType    string    `json:"recoveryType,omitempty"`
    StalledAt       *time.Time `json:"stalledAt,omitempty"`
    WatchdogChecked bool      `json:"watchdogChecked,omitempty"`
}
```

### New event types:

| Event Name | Types | Payload |
|---|---|---|
| `echo:heartbeat:event` | `started`, `budget_exceeded`, `no_eligible` | workspaceId, type, message |
| `echo:liveness:event` | `auto_retry`, `recovery_action`, `escalated` | workspaceId, cardId, type, message |
| `echo:watchdog:event` | `check_complete`, `repair_created` | workspaceId, cardId, type, result |

---

## Risks and Mitigations

| Risk | Mitigation |
|---|---|
| Runaway token usage from autonomous execution | Budget hard stops with configurable limits; default to unlimited until user sets a limit |
| Heartbeat conflicts with active runs | Idempotent tick: skip if scheduler already running |
| Stalled detection false positives | Configurable threshold (default 3m); require no progress update, not just silence |
| Watchdog creates excessive repair cards | Limit to recent done cards (last hour); one check per card |
| State corruption from concurrent heartbeat/scheduler access | Protected by existing `chatMu` lock |
| User confusion about autonomous vs manual mode | Clear visual indicators: icon state, runtime label, toast on mode change |

---

## Out of Scope (Future)

- **Per-card git worktrees**: Isolate card execution in separate worktrees. Requires significant refactoring of filesystem guards and workspace path system.
- **Adapter abstraction layer**: Pluggable agent executors for delegating to external agents. Less urgent since Echo owns its tool registry.
- **Multi-workspace orchestration**: Coordinated execution across multiple workspaces. Requires dependency tracking across workspace boundaries.
- **Cost estimation before execution**: Predict token cost of a card before running. Requires LLM-based estimation.
