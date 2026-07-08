---
name: kanban-board-streaming-patch
description: How patch-based DOM updates prevent mobile kanban board blinking during agent streaming by adding card-level and board-level patches before falling back to full render.
triggers:
    - kanban
    - blinking
    - mobile
    - streaming
    - card_progress
    - patch
    - render
    - DOM
    - performance
    - SSE event
---

## Problem
Rapid `card_progress` SSE events during agent execution trigger full app re-renders via `renderKanbanEventPreservingScroll()` → `render()`, which rebuilds all 4 DOM regions (left-nav, main, mobile-nav, overlays) through `innerHTML`. On mobile viewports (≤720px), this causes visible blinking/flicker.

## Solution Architecture
Three-tier patch cascade in `applyKanbanEvent()` for `card_progress` events:

1. **`patchOpenCardProgress(event)`** — existing: patches progress transcript section inside card detail panel when a card is selected/open
2. **`patchCardProgress(card)`** — new: locates the single `<article.kanban-card>` by `data-card-id`, updates `.kanban-card-status-text` percentage and `.kanban-card-progress-bar` fill width/label inline
3. **`patchKanbanBoard(workspaceID)`** — new: patches only `.kanban-board` via `patchChildrenFromHtml` (morph-based diffing) and updates `.panel-heading` via targeted innerHTML

Full render is the fallback when all three patches return false or for non-progress events.

## Key Implementation Details

### File: `frontend/src/app/kanban/index.ts`

**`patchCardProgress(card)`**:
- Finds card article via `button.kanban-card-open[data-card-id="..."]` → `.closest("article.kanban-card")`
- First tries with lane filter, falls back without (card may have changed lanes between events)
- Updates status text by replacing the element in-place
- For inProgress cards: updates progress fill `style.width`, label `innerHTML`, and injects missing progress bars

**`patchKanbanBoard(workspaceID)`**:
- Locates `.kanban-panel` inside app root
- Patches `.kanban-board` using existing `patchChildrenFromHtml` / `morphChildren` infrastructure
- Replaces `.panel-heading` innerHTML with fresh heading (runtime, buttons)
- Returns true when kanban panel exists; false triggers full-render fallback

**`applyKanbanEvent()` flow**:
```typescript
if (event.type === "card_progress") {
  if (patchOpenCardProgress(event)) return;
  const card = kanbanCards(board).find((c) => c.id === event.cardId);
  if (card && patchCardProgress(card)) return;
  if (patchKanbanBoard(event.workspaceId)) return;
}
renderKanbanEventPreservingScroll(); // fallback
```

## Constraints Preserved
- No new diffing library — reuses `patchChildrenFromHtml` / `morphChildren` from `markdown.ts`
- `bindEvents()` is NOT called during incremental patches (avoids listener duplication)
- Non-progress events (`card_status_changed`, `lane_moved`, etc.) still trigger full render
- Existing `patchOpenCardProgress` behavior unchanged for card detail panel

## Important Imports
- `changeReviewFor` imported from `../state` (not exported from `../changes`)
- Uses existing `escapeAttribute`, `escapeHtml`, `kanbanCards`, `getLastToolCallName` helpers
