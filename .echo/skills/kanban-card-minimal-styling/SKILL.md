---
name: kanban-card-minimal-styling
description: 'Minimal card rendering for both Kanban and Task boards: compact layouts, inline status badges, progress bars, hover effects, priority badges, shared CSS patterns, and dashboard-style visual consistency.'
triggers:
    - kanban card styling
    - task card compact
    - minimal card view
    - status dot
    - priority badge
    - card hover effects
    - kanban status text
    - color-coded status
    - progress bar
    - in-progress card
---

## Minimal Kanban Card Architecture

### Rendering (frontend/src/app/kanban/index.ts)
- `renderKanbanCard()` produces a collapsed card: title row with status dot, contextual status text, and optional progress bar for in-progress cards
- Hidden from card view: description, acceptance criteria, file changes, progress transcript, dependencies
- Click opens existing detail modal via `data-action="open-card"` on `.kanban-card-open` button
- Helper functions: `kanbanCardProgressPercent()` (estimates from transcript/criteria), `laneDotClass()`, `renderKanbanCardStatus()` (per-lane status text), `renderInProgressProgressBar()`

### Per-Lane Status Messages (`renderKanbanCardStatus`)
Each card shows a color-coded `<p class="kanban-card-status-text">` below the title, dispatched by lane:

- **Done** (`renderDoneStatus`): Green check icon + "verification passed, N files changed". Parses file count from last verification transcript entry's content lines starting with "- ". Falls back to red for failed verifications, orange hourglass for skipped.
- **InProgress** (`renderInProgressStatus`): Shows just the progress percentage (e.g., "78%"). Accent color. The full progress bar with tool name renders separately via `renderInProgressProgressBar()`.
- **Ready** (`renderReadyStatus`): Uses `DependencyStatuses[]` populated by backend `enrichKanbanCard()`. Green check "ready" when no deps, green check "all dependencies met" when all satisfied, orange hourglass naming specific unmet dependency with its lane status.
- **Blocked** (`renderBlockedStatus`): Red text. Checks `recoveryType === "escalated"` first, then scans transcript for block reason content, falls back to "blocked by dependencies" or "agent stopped early".

### In-Progress Progress Bar (`renderInProgressProgressBar`)
Only rendered for cards in the `inProgress` lane. Produces a column-layout container:
- Track with warning-colored fill at computed percentage width
- Label showing "X% complete" followed by active tool name in monospace `<span class="kanban-card-tool-label">`
- Tool name extracted via `getLastToolCallName()` from the last transcript entry with `type: "tool_call"` and title format "Tool call: <function_name>"
- Non-in-progress lanes (Done, Ready, Blocked) do NOT render a progress bar

### Data Sources for Status Text
- Verification results: Last `KanbanProgressEntry` with `type: "verification"` in `progressTranscript`. Title contains "passed"/"failed"/"skipped". Content has changed paths as "- path" lines.
- Tool call names: Last `KanbanProgressEntry` with `type: "tool_call"`. Title format: "Tool call: <function_name>". Split on ": " to extract the name via `getLastToolCallName()`.
- Dependencies: `dependencyStatuses[]` array with `{id, title, status, done}` — enriched server-side by `enrichKanbanCard()`.
- Block reasons: Transcript entries with type "status"/"message", card `recoveryType`, and `blockedBy[]`.

### CSS Classes (frontend/src/styles.css)
- `.kanban-card`: `border-radius: var(--space-lg)`, `background: var(--color-surface-muted)`, hover lift at 120ms ease
- `.kanban-card-title-row`: flex row with status dot, title strong, and card ID
- `.kanban-card-status-dot`: 8px circle, lane-specific colors (ready=muted, inprogress=accent+pulse, blocked=danger, done=success)
- `.kanban-card-progress-bar`: column container for the progress track + label (in-progress cards only); matches dashboard mockup layout
- `.kanban-card-progress-track`: 6px height, `border-radius: 3px`, muted background (`color-mix(in srgb, var(--color-text-muted) 12%, transparent)`)
- `.kanban-card-progress-fill`: warning color fill (`var(--color-warning)`), `border-radius: 3px`, 300ms width transition
- `.kanban-card-progress-label`: `--text-xs` size, flex row with gap, muted color, tabular-nums
- `.kanban-card-tool-label`: monospace font family, `--text-xs`, muted color at 70% opacity
- `.kanban-card-id`: muted card ID suffix
- `@keyframes echo-pulse-dot`: opacity pulse for in-progress dots

### Status Text CSS
- `.kanban-card-status-text`: base styles — `font-size: var(--text-sm)`, `font-weight: 600`, flex layout with icon gap
- `.status-success` → `var(--color-success)` (green)
- `.status-warning` → `var(--color-warning)` (orange/amber)
- `.status-error` → `var(--color-danger)` (red)
- `.status-inprogress` → `var(--color-accent-strong)` (accent blue)

### Compact Layout Overrides
- `.kanban-card-open`: reduced `gap: var(--space-md)`, `padding: var(--space-lg-md)`
- `.kanban-card-title-row strong`: `font-size: var(--text-xl-lg)`
- `.kanban-card-title-row`: tighter `gap: var(--space-md-sm)`

### Shared CSS Patterns (Kanban + Task)
- Both use `border-radius: var(--space-lg)`, `background: var(--color-surface-muted)`
- Both hover with accent border tint, accent bg tint, `translateY(-1px)`, 120ms ease
- Status indicators use theme CSS custom properties

### Responsive Breakpoints (Kanban)
- 1440px: `.kanban-board` columns narrow to `minmax(180px, 1fr)`
- 720px: 2-column grid, smaller lanes, reduced title font size
- 375px: 2-column auto grid, hidden card IDs, tighter padding

### Pitfalls
- Status text functions (`renderKanbanCardStatus`, `renderDoneStatus`, etc.) must exist and be referenced — removing them causes TS2304 build errors
- Progress bar is rendered ONLY for in-progress lane cards via the `card.lane === "inProgress"` guard; done/ready/blocked cards get no progress bar
- The verification transcript content format is "Changed paths:\n- path1\n- path2" — file counting parses lines starting with "- "
- Tool call title format is "Tool call: <name>" — split on ": " to extract the name via `getLastToolCallName()`
- `dependencyStatuses` is populated server-side by `enrichKanbanCard()` in `boardForWorkspace()`, not stored directly on cards
- The `.kanban-card-open` button wraps the header; card detail modal functionality is unchanged
- Status text uses SVG icons from `icons.check` and `icons.x`; hourglass emoji (`\u23F3`) is used for pending/skipped states

### Verification
- Frontend: `cd frontend && npm run build` must pass with no TypeScript errors
- Backend: `go test ./...` must pass (no Go changes expected for rendering-only work)
