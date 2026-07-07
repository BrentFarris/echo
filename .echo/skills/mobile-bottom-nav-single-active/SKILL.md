---
name: mobile-bottom-nav-single-active
description: How the mobile bottom nav enforces single-active-tab state and renders as a unified tab bar with workspace dropdown on mobile.
triggers:
    - mobile nav
    - bottom nav
    - view switching
    - single active tab
    - activeMobileView
    - mobileNavView
    - git changes tab
    - unified tab bar
    - single surface navigation
    - mobile layout
    - responsive nav
---

## Mobile Bottom Nav Active-Tab State & Unified Layout

### Location
- `frontend/src/app/render.ts` → `renderMobileBottomNav()` function
- `frontend/src/styles.css` → `@media (max-width: 720px)` block

### Single Source of Truth
```ts
const activeMobileView = showGitChanges ? "git" : mobileNavView;
```

This computed value is the only thing that determines which tab shows `is-active`. Every tab derives its active state from `activeMobileView` via simple equality comparison.

### Tab Mapping
| Index | Label        | data-action         | isActive check              |
|-------|-------------|---------------------|----------------------------|
| 0     | Chat        | switch-view (chat)  | `activeMobileView === "chat"` |
| 1     | Kanban      | switch-view (kanban)| `activeMobileView === "kanban"` |
| 2     | Code        | open/close-code-view| `activeMobileView === "code"`   |
| 3     | Git Changes | open/close-git-changes| `activeMobileView === "git"`  |
| 4     | Settings    | open-settings       | `activeMobileView === "settings"` |

### Key Invariant
Only one nav tab can be active at any time. The `activeMobileView` computation ensures mutual exclusion by resolving the `showGitChanges` overlay before comparing against `state.mobileNavView`.

### Rendered HTML Structure (Unified Tab Bar)
The nav uses a single-surface layout replacing the old dual-zone approach:
```html
<nav class="mobile-bottom-nav">
  <div class="mobile-nav-brand">
    <button class="mobile-nav-pill" ...>Workspace Name</button>
    <span class="mobile-nav-app-name">Echo</span>
  </div>
  <div class="mobile-nav-workspace-dropdown" ...>...</div> <!-- conditional -->
  <div class="mobile-nav-tabs">
    <button class="mobile-nav-tab..." ...>Chat icon</button>
    <button class="mobile-nav-tab..." ...>Kanban icon</button>
    <button class="mobile-nav-tab..." ...>Code icon</button>
    <button class="mobile-nav-tab..." ...>Git icon</button>
    <button class="mobile-nav-tab..." ...>Settings icon</button>
  </div>
</nav>
```

### Key CSS Selectors (inside @media max-width 720px)
- `.mobile-bottom-nav` — fixed bottom, flex row, gap 6px, padding 6px 10px
- `.mobile-nav-brand` — flex row, holds pill + brand label
- `.mobile-nav-pill` — rounded pill button, max-width 120px, overflow ellipsis
- `.mobile-nav-app-name` — muted uppercase label
- `.mobile-nav-workspace-dropdown` — absolutely positioned above nav bar, z-index 20
- `.mobile-nav-workspace-option` — list item buttons, active state highlighted
- `.mobile-nav-tabs` — flex grow container for 5 tabs
- `.mobile-nav-tab` — equal-width flex items, icon SVGs, active state accent-colored

### Removed Classes (Dual-Zone Legacy)
These classes no longer exist in the rendered HTML and must not be reintroduced:
- `.mobile-nav-workspaces` (scrollable workspace icons zone)
- `.mobile-nav-utility` (fixed utility buttons zone)
- `.mobile-nav-item` (generic nav item class)

### Related State
- `state.mobileNavView`: Stores the current view ("chat", "kanban", "code", "git", "settings")
- `state.appMode`: Mirrors `mobileNavView` for non-g Views ("chat", "kanban", "code", "settings")
- `showGitChanges`: Boolean param indicating whether git changes drawer is open — overrides `mobileNavView` to "git" for rendering purposes

### Navigation Actions (from actions.ts)
- `switch-view` (with `data-view`): Sets `appMode` and `mobileNavView`
- `open-code-view` / `close-code-view`: Toggles code mode, sets `mobileNavView` accordingly
- `open-git-changes` / `close-git-changes`: Opens/closes git drawer, sets `mobileNavView` to "git" or "kanban"
- `open-settings`: Triggers settings overlay (no `mobileNavView` change needed since it's handled separately)
