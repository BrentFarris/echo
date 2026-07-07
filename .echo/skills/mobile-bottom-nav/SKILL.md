---
name: mobile-bottom-nav
description: Mobile bottom nav with five icon-only tabs, single-active-view invariant enforced via state.mobileNavView, workspace pill/dropdown, B-029/B-030 bugs documented.
triggers:
    - mobile nav
    - bottom nav
    - view switching
    - workspace dropdown
    - keyboard navigation
    - focus trap
    - ARIA
    - touch target
    - accessibility
    - icon-only
    - text label
    - single active tab
---

Mobile bottom nav has five icon-only tabs (Chat, Kanban, Code, Git Changes, Settings) on screens under 720px. B-028 completed: span elements removed from each button in renderMobileBottomNav() at frontend/src/app/render.ts. No CSS cleanup needed since no rules targeted .mobile-nav-tab span. Accessible via title and aria-label attributes. Known bugs: B-029 (workspace dropdown transparent background/unselectable items), B-030 (multiple nav tabs can be active simultaneously). Each tab uses role=tab, data-mobile-nav-tab-index (0-4), data-action, title, and aria-label. Tab switching calls bindActionEvents after render because render destroys/recreates DOM. Desktop layout unaffected.

Implementation note (2026-07-03): Fix applied by adding MobileNavView type ("chat"|"kanban"|"code"|"settings"|"git") and state.mobileNavView field. RenderMobileBottomNav() now checks state.mobileNavView === '...' for is-active class on each tab. Actions (open-code-view, close-code-view, switch-view, open-git-changes, close-git-changes) all set state.mobileNavView before calling render(). Key files: types.ts (AppMode/MobileNavView), state.ts (mobileNavView field), render.ts (is-active computation), actions.ts (all handlers that change views).
