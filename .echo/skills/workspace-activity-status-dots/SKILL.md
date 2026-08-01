---
name: workspace-activity-status-dots
description: Workspace activity status dots and preview text in desktop/mobile dropdown options showing chat streaming, kanban running, or idle state.
triggers:
    - workspace dropdown
    - activity status
    - status dot
    - chat busy indicator
    - kanban running indicator
    - idle workspace
    - workspace activity summary
    - desktop dropdown
    - mobile dropdown
---

## Workspace Activity Status Dots

Workspace dropdown options (desktop and mobile) display colored status dots + activity text to indicate workspace activity state.

### Files

- `frontend/src/app/render.ts`: Helper functions `getWorkspaceActivityStatus()` and `renderWorkspaceActivityStatus()`, injected into both desktop (`workspace-dropdown-option`) and mobile (`mobile-nav-workspace-option`) dropdown buttons.
- `frontend/src/styles.css`: `.workspace-activity-dot` (idle gray, chat-busy blue, kanban-running green), `@keyframes activity-pulse`, `.workspace-activity-text`, `.workspace-dropdown-option-main` flex wrapper, `.mobile-nav-workspace-option` flex layout.

### Data source

- `state.workspaceActivitySummaries`: `Map<string, services.WorkspaceActivitySummary>` populated by periodic polling via `startActivityRefreshTimer()` in bootstrap.ts.
- Each summary has `isChatBusy`, `isKanbanRunning`, `activeAgentCount`, `lastMessageSnippet`.

### Rendering logic

Priority order: chat busy → kanban running → last message snippet fallback. When no activity and no snippet, only the idle dot renders (no text).

### CSS classes

| Class | State | Color | Animation |
|---|---|---|---|
| `is-idle` | No activity | Gray (#6b7280), opacity 0.4 | None |
| `is-chat-busy` | Chat streaming | Blue (#3b82f6) | `activity-pulse` 1.5s infinite |
| `is-kanban-running` | Kanban agents running | Green (#22c55e) | `activity-pulse` 1.5s infinite |

### Desktop layout

Workspace dropdown options use `.workspace-dropdown-option-main` flex wrapper to align display name + dot + text horizontally with ellipsis overflow on the name.

### Mobile layout

Mobile workspace options use flex layout with `flex-wrap: wrap` and `gap: var(--space-xs)` to accommodate the inline activity indicators.
