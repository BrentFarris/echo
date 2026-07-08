---
name: dashboard-widget-css-architecture
description: 'Dashboard widget CSS architecture: light chrome shell, inline compact content classes, SVG sizing constraints, responsive grid breakpoints, and duplicate-section pitfalls.'
triggers:
    - dashboard CSS
---

# Dashboard Widget CSS Architecture

## File: `frontend/src/styles.css` (lines ~7803–8155)

The dashboard widget section is a single consolidated block containing all widget shell, content, and responsive styles. It does NOT have a separate "Dashboard Widget Content Styles" section — that was removed to eliminate duplication.

## Key Design Principles

- **Light chrome**: Widget headers use `background: transparent` with a thin `border-bottom` separator instead of heavy dark backgrounds
- **Body handles padding**: `.widget-card-body` has its own padding; the card shell has no padding
- **SVG constraint**: `.widget-card-body svg { width: 16px; height: 16px }` prevents giant SVG triangles
- **Inline compact format**: Widget content classes use single-line declarations (e.g., `.widget-chat-recent { display: flex; flex-direction: column; gap: var(--space-md); }`)

## Widget Content Classes (all in one section)

Each widget type has a container class and child element classes:
- Chat: `.widget-chat-recent`, `.widget-chat-msg`, `.from-user`/`.from-assistant`
- Busy status: `.widget-busy-indicator`, `.widget-status-icon`, `.widget-status-text`
- Budget: `.widget-budget-bar`, `.widget-budget-track`, `.widget-budget-fill`, `.budget-ok`/`.budget-warning`/`.budget-critical`
- Kanban summary: `.widget-kanban-summary`, `.widget-lane-badge` (uses `--badge-color` CSS custom property)
- Cards done: `.widget-done-count`, `.widget-done-number` (uses `--text-5xl`)
- Workspaces: `.widget-workspaces`, `.widget-workspace-item`, `.widget-workspace-label`
- Git widgets: `.widget-git-branch`, `.widget-git-commits`, `.widget-git-change-count`
- Priority strip: `.widget-priority-strip`, `.widget-priority-badge.p0/.p1/.p2`

## CSS Custom Properties Used

All standard Echo design tokens: `--color-*`, `--space-*`, `--text-*`. Widget lane badges use `--badge-color` set inline per lane.

## Pitfalls

- **No duplicate sections**: Earlier versions had a separate "Dashboard Widget Content Styles" block that duplicated all widget CSS. This was removed — all widget styles live in the main dashboard section.
- **Widget body SVG sizing is critical**: Without `.widget-card-body svg { width: 16px; height: 16px }`, SVG icons render at their intrinsic size (giant triangles).
- **Card shell has no padding**: Padding moved to `.widget-card-body`. Don't add padding back to the card shell.

## Responsive Breakpoints

- Desktop (>1440px): 4-column grid
- Tablet (≤1440px): 2-column grid, large/medium widgets span 2 columns
- Mobile (≤720px): 2-column grid, toolbar stacks vertically
- Small mobile (≤375px): single column, all widgets full-width
