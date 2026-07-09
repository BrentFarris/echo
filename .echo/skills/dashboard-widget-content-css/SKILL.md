---
name: dashboard-widget-content-css
description: Dashboard widget content CSS classes covering SVG sizing, status dots, progress bars, badges, Git widgets, workspace items, and toolbar styling appended to styles.css.
triggers:
    - dashboard widget CSS
    - widget content styling
    - SVG icon sizing dashboard
    - widget badge colors
    - kanban progress bar CSS
    - git widget styles
    - workspace widget layout
---

## Dashboard Widget Content CSS

All dashboard widget content styles live at the end of `frontend/src/styles.css`, after the media query blocks (line ~8458+).

### Critical SVG fix
Widget renderers output raw `<svg viewBox="0 0 24 24">` with no dimensions. `.widget-card-body svg` constrains to 16px; `.widget-status-icon svg` is 18px with `echo-spin` animation for busy state.

### Widget content class families
- **Chat:** `.widget-chat-recent`, `.widget-chat-msg`, `.from-user`/`.from-assistant`, `.widget-chat-role`, `.widget-chat-content`
- **Busy status:** `.widget-busy-indicator`, `.widget-status-icon`, `.widget-status-text`, `.widget-subtitle`
- **Budget:** `.widget-budget-bar`, `.widget-budget-track`, `.widget-budget-fill`, `.budget-ok`/`.budget-warning`/`.budget-critical`
- **Kanban summary:** `.widget-kanban-summary`, `.widget-lane-badge` (uses CSS `--badge-color` custom property)
- **Kanban progress:** `.widget-kanban-progress`, `.widget-progress-track`, `.widget-progress-fill`
- **Cards done:** `.widget-done-count`, `.widget-done-number` (var(--text-5xl)), `.widget-trend`
- **Tasks:** `.widget-tasks-summary`, `.widget-p0-list`, `.widget-p0-item`
- **Priority strip:** `.widget-priority-strip`, `.widget-priority-badge.p0`/`.p1`/`.p2`
- **Git branch:** `.widget-git-branch`, `.widget-git-dirty-badge`, `.widget-git-hash` (monospace)
- **Git commits:** `.widget-git-commits`, `.widget-git-commit` (3-col grid: hash, subject, date)
- **Git change count:** `.widget-git-change-count`, `.widget-git-clean`, `.widget-git-dirty`
- **Heartbeat:** `.widget-heartbeat`, `.widget-heartbeat-row`, `.widget-heartbeat-value` (monospace)
- **Workspaces:** `.widget-workspaces`, `.widget-workspace-item`, `.widget-running-badge`
- **Open tabs:** `.widget-open-tabs`, `.widget-tab-item`, `.widget-dirty-dot`
- **Code workspace status:** `.widget-code-workspace-status`, `.widget-cws-missing` (danger badge)
- **Dashboard toolbar:** `.dashboard-toolbar`, secondary button SVG sizing at 14px

### Shared patterns
- Status dots: `.status-dot` (8px circle), modifiers `.status-active`, `.status-inactive`, `.status-running`
- Placeholder: `.widget-placeholder` for empty states
- Monospace font family: `ui-monospace, SFMono-Regular, Menlo, monospace` used for hashes, values, paths
- Color-mix badges use 15% tint with transparent background pattern

### Adding new widget styles
Append after the last existing widget section. Use `var(--space-*)`, `var(--text-*)`, and `var(--color-*)` CSS custom variables. Test with `cd frontend; npm run build`.
