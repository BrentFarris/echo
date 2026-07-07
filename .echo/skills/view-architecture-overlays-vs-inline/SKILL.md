---
name: view-architecture-overlays-vs-inline
description: 'Document Echo''s dual view architecture: inline panel views (Chat, Kanban, Code, Git Changes, Settings) vs overlay/modal views (Change Review, Kanban dialogs), with guidance for converting overlays to inline views.'
triggers:
    - git changes view
    - settings view
    - inline view
    - overlay
    - popup
    - modal
    - drawer
    - view architecture
    - rendering pattern
    - convert overlay to inline
    - card-3
    - card-4
---

# Echo View Architecture: Inline Panels vs Overlay Popups

## Overview

Echo uses two distinct rendering patterns for its major views:

### Inline Panel Views
Rendered inside the main `<main>` content area, flowing with the page layout:

- **Chat** — `renderChatPanel()` in `frontend/src/app/render.ts`
- **Kanban** — `.kanban-panel` section in `renderWorkspacePanels()`
- **Code** — `.code-workspace` section toggled via `state.appMode === "code"`
- **Git Changes** — `renderGitChangesPanel()` rendered as `<aside class="git-changes-panel">` sibling to main content (card-3 converted from overlay)
  - Controlled by `openGitChangeWorkspaces` Set per workspace
  - Main content gets `has-git-changes` CSS class → grid split layout
  - Desktop: 480px sidebar on right; Mobile ≤720px: bottom sheet with slide-up animation
  
- **Settings** — `renderSettingsPanel()` rendered as `<aside class="settings-sidebar">` sibling to main content (card-4 converted from overlay)
  - Controlled by single boolean `state.settingsOpen`
  - Main content gets `has-settings` CSS class → grid split layout
  - Desktop: 480px sidebar on right; Mobile ≤720px: bottom sheet with slide-up animation

These use standard DOM flow; no backdrop or dialog role needed.

### Overlay/Modal Popup Views
Still rendered as full-screen overlays with `role="dialog"` and `aria-modal="true"`:

- **Change Review** — `renderChangeReviewDrawer()` in `frontend/src/app/changes/index.ts`
  - Uses `aside.change-review-backdrop` with expanded class toggle
  - Controlled by `openChangeReviewWorkspaces` Set per workspace
  - Rendered inside `renderWorkspacePanels()`, not alongside main content
  
- **Kanban dialogs** — create-card and card-detail also use overlay backdrops

## State Variables

| View | State Key | Type | Location |
|------|-----------|------|----------|
| Settings | `settingsOpen` | `boolean` | `state.ts` |
| Git Changes | `openGitChangeWorkspaces` | `Set<string>` | `state.ts` |
| Change Review | `openChangeReviewWorkspaces` | `Set<string>` | `state.ts` |
| Code Mode | `appMode` | `"chat"|"code"` | `state.ts` |

## Converting Overlays to Inline Views

When converting an overlay view to an inline view (like cards 3 and 4 did):

1. Remove the backdrop/dialog wrapper (`change-review-backdrop`, `div.overlay`) from the render function
2. Wrap content in `<aside class="<name>-sidebar">` with inner `<section class="change-review ...">`
3. In `render.ts`: add a `show<Noun>` boolean derived from state, add `has-<noun>` class to `<main>`, render aside as inline sibling
4. Add CSS: `.main-content.has-<noun>` sets `grid-template-columns: minmax(0, 1fr) 480px`; `.sidepanel-name` provides border-left, background, overflow, and `slideInRight` animation
5. Mobile ≤720px: single-column grid, border shifts to top, `slideUpBottom` animation, max-height 55vh
6. Ensure mobile nav tab switches work consistently (see B-030 for lessons learned)
7. Consider whether the view needs per-workspace state or global state

### Target Files for New Inline Conversions
- `frontend/src/app/render.ts` — main render entry point, add show flag + has-class + conditional aside
- `frontend/src/styles.css` — desktop grid split + mobile bottom-sheet rules

### Verification Checklist
- Frontend build: `cd frontend && npm run build` succeeds (TypeScript compiles, Vite bundles)
- Go tests: `go test ./...` passes (if backend touched)
- No visual regression: settings content, inputs, save/close behavior identical
- Mobile responsive: bottom sheet at ≤720px, sidebar at wider screens
