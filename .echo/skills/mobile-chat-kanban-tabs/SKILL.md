---
name: mobile-chat-kanban-tabs
description: How the mobile Chat/Kanban tab bar is rendered, how tab state is tracked per-workspace, and how CSS conditionally hides panels based on the active tab.
triggers:
    - mobile chat
    - mobile kanban
    - chat/kanban tab
    - tab bar mobile
    - activeChatKanbanTab
---

## Mobile Chat/Kanban Tab Bar

On mobile viewports (≤920px), a tab bar replaces the side-by-side split-panels layout, letting users toggle between Chat and Kanban panels.

### State
- `state.activeChatKanbanTab: Map<string, ChatKanbanTab>` — per-workspace tab tracking (`"chat"` | `"kanban"`). Default is `"chat"`.
- `getActiveChatKanbanTab(workspaceID)` in `state.ts` returns the stored tab or `"chat"`.

### Render (`render.ts`)
- `renderWorkspacePanels()` adds `data-active-tab="${activeTab}"` on the `.split-panels` container.
- `renderChatKanbanTabs(workspaceID, activeTab)` emits a `<div class="chat-kanban-tabs">` with two tab buttons using `data-action="set-chat-kanban-tab"` and `data-tab="chat"` / `data-tab="kanban"`.
- Tab bar is rendered above the split-panels div; it's hidden on desktop via CSS (only shown inside the mobile media query).

### Action (`actions.ts`)
- `set-chat-kanban-tab` handler reads `target.dataset.tab`, validates it is `"chat"` or `"kanban"`, stores it in `state.activeChatKanbanTab` for the active workspace, and re-renders.

### CSS (`styles.css`)
- Base `.chat-kanban-tabs` and `.tab-button` styles are defined only inside `@media (max-width: 920px)`.
- Active tab gets `.is-active` class with accent-colored text and bottom border.
- Conditional hiding via attribute selector:
  - `.split-panels[data-active-tab="chat"] .kanban-panel { display: none; }`
  - `.split-panels[data-active-tab="kanban"] .chat-panel { display: none; }`

### Key files
- `frontend/src/app/state.ts` — `getActiveChatKanbanTab()` helper
- `frontend/src/app/render.ts` — `renderWorkspacePanels()`, `renderChatKanbanTabs()`
- `frontend/src/app/actions.ts` — `set-chat-kanban-tab` handler
- `frontend/src/styles.css` — mobile-only tab bar and conditional panel styles

### Pitfalls
- Tab state is runtime-only (not persisted); it resets to `"chat"` on reload.
- The `data-active-tab` attribute on `.split-panels` drives the CSS hiding; do not remove it or the mobile toggle breaks.
- Existing expanded-state classes (`is-chat-expanded`, `is-kanban-expanded`) still work alongside the tab system for desktop behavior.
