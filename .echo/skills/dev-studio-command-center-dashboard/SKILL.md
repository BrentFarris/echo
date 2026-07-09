---
name: dev-studio-command-center-dashboard
description: Dev Studio command center dashboard layout replacing widget grid with unified status bar, backlog panel, kanban lanes, and chat section.
triggers:
    - dashboard redesign
    - command center layout
    - Dev Studio dashboard
    - backlog panel
    - kanban panel
    - chat section
    - status bar indicators
    - widget grid replacement
---

# Dev Studio Command Center Dashboard

## Overview

The dashboard (`frontend/src/app/dashboard/grid.ts`) renders a unified "Dev Studio" command center layout when `view === "dashboard"`. Other view modes (chat, tasks, kanban, code, git, settings) fall back to the existing widget grid within the same `renderDashboardWidgets()` function.

## Architecture

### Entry Point: `renderDashboard(view?: AppMode)` in `index.ts`

Delegates entirely to `renderDashboardWidgets(v)` from `grid.ts`. No branching logic in index.ts.

### Command Center Routing (in `grid.ts`)

- `view === "dashboard"` → renders 3-section command center layout
- Other views → legacy widget grid with edit mode support
- No workspace → empty state message

### Three Sections

1. **Status Bar** (`renderStatusBar`): Workspace name with green dot, heartbeat interval, watchdog interval, token budget (using `formatTokenCount`, `getBudgetProgress`)
2. **Backlog Panel** (`renderBacklogPanel`): Open tasks grouped by priority (P0/P1/P2), max 10 tasks, priority badges + status indicators
3. **Kanban Panel** (`renderKanbanPanel`): 4 lanes (Done, In Progress, Ready, Blocked) with counts and compact `.kanban-card` styled cards
4. **Chat Section** (`renderChatSection`): Last 6 messages from active chat session, role bubbles truncated to ~140 chars

### Data Sources

- `taskBoardFor(workspace.id).tasks` → backlog tasks
- `kanbanBoardFor(workspace.id)` → kanban lanes/cards
- `chatSessionFor(workspace.id).messages` → chat messages
- `state.heartbeatIntervals.get(workspace.id)` → heartbeat interval ms
- `state.watchdogIntervals.get(workspace.id)` → watchdog interval ms
- `tokenBudgets.get(workspace.id)` → token budget

### CSS Classes (in `frontend/src/styles.css`)

Already present: `.dashboard-status-bar`, `.dashboard-main-grid` (1fr 2fr), `.dashboard-backlog-panel`, `.dashboard-kanban-panel`, `.dashboard-lanes-grid`, `.dashboard-lane`, `.dashboard-chat-section`, `.backlog-task-row`, `.backlog-status-badge`, `.dashboard-chat-message`

Responsive breakpoints at ≤1200px, ≤720px, ≤480px.

### Widget System Preservation

- `widgetRegistry`, `availableWidgetsForView`, `availableWidgets` export all intact
- Legacy widget grid rendering preserved for non-dashboard views
- `getDashboardWidgets` imported at bottom of grid.ts to avoid circular deps

## Pitfalls

- `KanbanProgressEntry` has fields: `type`, `title`, `content`, `status`, `timestamp` — NO `name` field. Use `last.content` for status text.
- CSS classes (`.dashboard-backlog-panel`, etc.) already existed in styles.css — don't duplicate styles.
- The command center uses inline helper functions (`laneDotClass`, `kanbanCardProgressPercent`) rather than importing from kanban/index.ts to keep grid.ts self-contained.
