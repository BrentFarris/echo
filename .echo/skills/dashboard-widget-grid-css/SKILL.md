---
name: dashboard-widget-grid-css
description: 'Dashboard widget grid CSS layout: 4-column grid, size span classes, responsive breakpoints, edit mode styling.'
triggers:
    - dashboard widget grid
---

## Widget Grid System

The dashboard widget grid uses a **4-column CSS Grid** layout defined in `frontend/src/styles.css`.

### Core Classes

- `.dashboard-widget-grid` — 4-column grid (`repeat(4, minmax(0, 1fr))`), auto rows min 120px, gap `var(--space-lg)`
- `.dashboard-widget-card` / `.widget-card` — surface background, border, rounded corners, flex column layout with padding and gap

### Size Span Classes

| Class | Columns (desktop) | Rows |
|---|---|---|
| `.dashboard-widget-small` / `.widget-size-small` | span 1 | 1 |
| `.dashboard-widget-medium` / `.widget-size-medium` | span 2 | 1 |
| `.dashboard-widget-large` / `.widget-size-large` | span 2 | 2 |
| `.dashboard-widget-wide` / `.widget-size-wide` | span 4 (full) | 1 |

Inline `grid-column` and `grid-row` styles are applied by `frontend/src/app/dashboard/grid.ts` (`gridColumnSpan()` / `gridRowSpan()`).

### Responsive Breakpoints

- **≥1440px**: 4 columns (default)
- **≤1440px**: 2 columns; large/medium/wide span 2, small spans 1
- **≤720px**: 2 columns; small spans 1, others span 2
- **≤375px**: single column; all widgets full-width via `grid-column: 1 / -1`

### Edit Mode

When `.dashboard-widget-grid` has class `is-edit-mode`, widget cards get dashed borders with accent tint. The grid renderer (`grid.ts`) shows reorder (up/down) and remove buttons per card, plus a widget picker sidebar (`.widget-add-panel`, `.widget-picker-list`).

### Key Files

- `frontend/src/styles.css` — all CSS rules
- `frontend/src/app/dashboard/grid.ts` — grid renderer, span helpers, edit controls
- `frontend/src/app/dashboard/widgets.ts` — widget registry
- `frontend/src/app/dashboard/index.ts` — dashboard entry point

### Invariants

- All colors use `var()` custom properties — no hardcoded color values
- The 12-column grid is legacy; the current system is 4-column
- Size span classes are aliased: both `.dashboard-widget-*` and `.widget-size-*` selectors exist for forward/backward compatibility
