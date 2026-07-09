---
name: frontend-performance-patching
description: Diagnose and fix UI responsiveness degradation caused by full DOM re-renders during frequent SSE event processing, particularly on mobile viewports.
triggers:
    - performance
    - DOM re-render
    - SSE events
    - kanban
    - responsiveness
    - mobile
    - patchChildrenFromHtml
    - full render
    - touch unresponsive
---

# Frontend Performance: Patching vs Full Re-render

## Architecture Overview

Echo's frontend uses a **targeted patch-first** philosophy for streaming updates but falls back to **full DOM re-render** (`appRoot.innerHTML = ...`) for many update paths. Understanding when each occurs is critical for diagnosing UI responsiveness problems.

### Event Flow

```
SSE event → EventsOn callback → applyXxxEvent()
  ├── Chat stream:   patchChatPanel() + patchChatControls() [TARGETED] ✓
  ├── Kanban board:  renderKanbanEventPreservingScroll() → full render() ✗
  └── Code inline:   applyInlineCodePromptEvent() [TARGETED] ✓
```

### Key Files

- `frontend/src/app/kanban/index.ts` — `applyKanbanEvent()` always triggers full render via `renderKanbanEventPreservingScroll()`
- `frontend/src/app/render.ts` — `render()` does `appRoot.innerHTML = ...` destroying entire DOM tree
- `frontend/src/markdown.ts` — `patchChildrenFromHtml()` / `morphChildren()` / `morphElement()` provide lightweight DOM diffing

### The Problem Pattern

During active agent execution, kanban progress events fire rapidly. Each event triggers:
1. State mutation in `state.kanbanBoards`
2. Full DOM destruction via `appRoot.innerHTML = ...`
3. Complete DOM reconstruction including panels, navigation, modals
4. Scroll position capture/restore across four regions

On mobile (<768px), this DOM churn makes touch interaction unreliable because:
- Touch listeners attached to old DOM nodes are lost
- Layout recalculations compete with paint cycles
- JavaScript thread is busy rebuilding DOM instead of handling input

### Known Partial Fix

`patchOpenCardProgress()` in `kanban/index.ts` uses `patchChildrenFromHtml` for the transcript section when a card detail panel is open. This only helps the transcript area — the rest of the board still gets fully re-rendered.

### Solution Direction

Replace blanket full-render with incremental patches using the existing `morphChildren` infrastructure:

1. Compare previous board state with new board state
2. Update only changed card elements within lane containers
3. Handle additions/removals/moves between lanes
4. Batch rapid events (~100ms debounce)
5. Consider stricter batching on mobile viewports

### Testing Checklist

- [ ] No full `appRoot.innerHTML` replacement during active agent runs
- [ ] Touch interaction responsive on mobile during execution
- [ ] Scroll position preserved after incremental updates
- [ ] No stale/orphaned DOM nodes after patches
- [ ] Build passes: `cd frontend && npm run build`
