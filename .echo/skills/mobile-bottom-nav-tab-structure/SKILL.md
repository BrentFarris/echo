---
name: mobile-bottom-nav-tab-structure
description: Mobile bottom nav tab structure, tab indices, and how to add new tabs between existing ones. Covers the 7-tab layout (chat, kanban, code, tasks, git, dashboard, settings) with proper index numbering and action binding patterns.
triggers:
    - mobile nav
    - bottom nav
    - tab structure
    - add tab
    - mobile navigation
    - render.ts mobile
    - dashboard tab
---

## Mobile Bottom Nav Tab Structure

### Current Layout (7 tabs, left to right)
1. **Chat** (index 0) - `data-action="switch-view" data-view="chat"`
2. **Kanban** (index 1) - `data-action="switch-view" data-view="kanban"`
3. **Code** (index 2) - `data-action="${activeMobileView === "code" ? "close-code-view" : "open-code-view"}"`
4. **Tasks** (index 3) - `data-action="switch-view" data-view="tasks"`
5. **Git** (index 4) - `data-action="switch-view" data-view="git"`
6. **Dashboard** (index 5) - `data-action="${activeMobileView === "dashboard" ? "close-dashboard" : "open-dashboard"}"`
7. **Settings** (index 6) - `data-action="open-settings"`

### Adding a New Tab
1. Add tab button in `frontend/src/app/render.ts` mobile nav section
2. Set unique `data-mobile-nav-tab-index` (sequential from 0)
3. Use existing icon from `icons.ts` or inline SVG
4. Bind to appropriate action (`open-dashboard`/`close-dashboard` pattern for toggleable views, `switch-view` for standard views)
5. Update `MobileNavView` type in `types.ts` if needed (already includes "dashboard")

### Key Patterns
- Toggleable views use conditional actions: `${activeMobileView === "xxx" ? "close-xxx" : "open-xxx"}`
- Standard views use: `data-action="switch-view" data-view="xxx"`
- Active state via class: `class="mobile-nav-tab${activeMobileView === "xxx" ? " is-active" : ""}"`
- Accessibility: aria-pressed, role="tab", aria-selected, tabindex management

### CSS Classes
- `.mobile-bottom-nav` - container
- `.mobile-nav-tabs` - tab list
- `.mobile-nav-tab` - individual tab button
- `.is-active` - active state modifier
