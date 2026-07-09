---
name: unified-left-nav-single-panel
description: Desktop unified left nav with workspace dropdown and icon buttons, single-panel layout driven by appMode, replacing the old gutter + split-panels architecture.
triggers:
    - unified left nav
    - single panel layout
    - appMode
    - left-nav
    - workspace dropdown
    - split panels removed
    - buildMain
    - renderWorkspacePanels
    - nav-icon-button
---

## Layout Architecture

The desktop UI uses a two-column grid: `.left-nav` (72px fixed) + `.main-content` (flex). The left nav is rendered into the `left-nav` region; main content goes into the `main` region.

### Left Nav Structure (`buildLeftNav`)

```
<aside class="left-nav">
  <div class="left-nav-workspace">         <!-- workspace dropdown trigger + dropdown -->
    <button class="nav-icon-button">       <!-- shows active workspace icon or + if none -->
    <div class="workspace-dropdown">       <!-- shown when state.workspaceDropdownOpen -->
      <!-- workspace options + divider + "Add workspace" -->
  <nav class="left-nav-buttons">           <!-- primary views -->
    <button data-action="switch-view" data-view="chat">
    <button data-action="switch-view" data-view="kanban">
  <div class="left-nav-actions">           <!-- utility views, pinned to bottom -->
    <button data-action="open-code-view" / "close-code-view">
    <button data-action="open-git-changes" / "close-git-changes">
    <button data-action="open-settings">
```

Active state is driven by `state.appMode`: `"chat" | "kanban" | "code" | "git" | "settings"`. The workspace dropdown toggles via `state.workspaceDropdownOpen` and the `toggle-workspace-dropdown` action.

### Single-Panel Main Content (`buildMain`)

`buildMain` renders exactly one panel based on `appMode`:
- `"code"` → `renderCodeView(workspace)` — replaces everything
- `"git"` → `renderGitRepositoryDrawer(workspace, ...)` — replaces everything
- `"chat"` or `"kanban"` → `renderWorkspacePanels(workspace, ...)`

The old split-panels layout (side-by-side chat + kanban with expand/collapse) is eliminated. `renderWorkspacePanels` now returns only the active panel:
- `appMode === "chat"` → full-width chat panel (`renderChatPanel(workspace, true)`)
- `appMode === "kanban"` → full-width kanban panel

Kanban detail drawer, create-card dialog, and change-review drawer are always rendered alongside the main panel when relevant (they're overlays/drawers, not part of the single-panel invariant).

### CSS Key Classes

- `.left-nav` — flex column, 72px wide, border-right, background surface
- `.nav-icon-button` — 44x44 icon button with hover/focus/active states
- `.workspace-dropdown` — absolute-positioned dropdown below trigger
- `.workspace-panel` — single-row grid (`minmax(0, 1fr)`), fills viewport height
- `.workspace-heading-actions` — `display: none` (obsolete; replaced by left nav buttons)

### State Invariants

- `state.appMode` is the single source of truth for which desktop panel is shown
- `state.mobileNavView` mirrors appMode for mobile bottom nav sync
- Code/git views use toggle actions (`open-code-view`/`close-code-view`, `open-git-changes`/`close-git-changes`) that restore to the last active chat/kanban tab via `getActiveChatKanbanTab()`
- Workspace dropdown is closed on workspace activation (`activate-workspace` action sets `state.workspaceDropdownOpen = false`)

### Pitfalls

- Don't reintroduce split-panels or expand/collapse logic — the single-panel model is enforced by appMode
- The workspace heading row with Git/Code buttons is no longer rendered; those functions moved to left nav icon buttons
- When adding new views, update both `AppMode` type and the left nav button set
- Mobile bottom nav still uses inline SVG icons (not the shared `icons` object) — keep in sync when icon paths change
