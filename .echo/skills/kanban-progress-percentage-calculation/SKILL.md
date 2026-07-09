---
name: kanban-progress-percentage-calculation
description: 'Kanban card progress percentage calculation: how the frontend derives 0-100% from transcript entries so the bar reflects proportional completion instead of plateauing at 99%.'
triggers:
    - kanban progress
    - progress percentage
    - progress bar
    - 99% plateau
    - tool_call count
    - kanbanCardProgressPercent
---

## Kanban Progress Percentage Calculation

### Location
`frontend/src/app/kanban/index.ts` — function `kanbanCardProgressPercent(card)`

### Formula (current as of 2026-07-08)
```ts
// Card done → 100%
// Card ready/blocked → 0%
// Card inProgress:
const toolCallCount = transcript.filter(e => e.type === "tool_call").length;
if (criteriaLen > 0) {
  return Math.min(Math.round((toolCallCount / criteriaLen) * 95), 97);
} else {
  return Math.min(Math.round((toolCallCount / 10) * 100), 80);
}
```

### Design rationale
- Only `tool_call` entries count as real work — they represent actual implementation steps (file reads, edits, shell commands). Status/thinking/message/verification entries are overhead and must not inflate the percentage.
- With criteria: each tool call counts as ~1/criteriaLen of the work, scaled to fill 0–95%. Hard-capped at 97% so the final 3% is reserved for verification/cleanup.
- Without criteria: estimates ~10 tool calls as a rough full-card baseline, capped at 80%.

### Pitfall (previous implementation)
Counting ALL transcript entries against acceptance criteria caused immediate plateau at 99% because status/thinking/message entries accumulate faster than criteria are satisfied. Always filter to `tool_call` type before computing progress.

### Verification
- Frontend: `cd frontend; npm run build`
- Backend: `go test ./...` (no backend changes for this fix)
