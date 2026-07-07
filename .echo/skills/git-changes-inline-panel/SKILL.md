---
name: git-changes-inline-panel
description: How the Git Changes panel was converted from a full-screen backdrop overlay to an inline collapsible sidebar panel alongside the main content area, including CSS grid layout, animations, and responsive mobile bottom-sheet behavior.
triggers:
    - git changes view
    - inline panel
    - overlay to inline
    - side panel
    - sidebar layout
    - responsive git panel
    - mobile bottom sheet
    - change review drawer
---

# Git Changes Inline Panel Architecture

## Overview
Git Changes (previously `renderGitRepositoryDrawer`) was converted from a full-screen backdrop overlay to an inline collapsible panel alongside the main content area. This aligns it with other inline views (Code, Settings) in the app layout grid.

## Files Changed

### `frontend/src/app/render.ts`
- Import renamed: `renderGitRepositoryDrawer` → `renderGitChangesPanel`
- Main content wrapper gets conditional class: `<main class="main-content ${showGitChanges ? 'has-git-changes' : ''}">`
- Git panel rendered inside `<main>` as sibling to `<section class="workspace-panel">`, not outside it

### `frontend/src/app/git/index.ts`
- Function renamed: `renderGitRepositoryDrawer` → `renderGitChangesPanel`
- Outer element changed: `<aside class="change-review-backdrop" role="dialog">` → `<aside class="git-changes-panel">`
- Inner element changed: `<section class="change-review git-repository">` → `<div class="change-review git-repository">`
- Removed `role="dialog"` and `aria-modal="true"` semantics — no longer a modal overlay

### `frontend/src/styles.css`
- Replaced `.change-review-backdrop` (fixed, inset: 0, z-index: 10, backdrop blur) with `.main-content.has-git-changes` grid layout
- Desktop: `grid-template-columns: minmax(0, 1fr) 480px;` — sidebar sits beside content at fixed width
- Added slide-in-right animation on `.git-changes-panel`
- Mobile (≤720px): grid switches to single column, panel becomes bottom sheet (`border-top`, `max-height: 55vh`, slide-up animation)
- Removed old `.change-review-backdrop.is-expanded` fullscreen styles

### `frontend/src/.echo/backlog.md`
- B-033 marked from `backlog` → `done`

## State Management
Existing state keys continue to work unchanged:
- `state.openGitChangeWorkspaces` — Set of workspace IDs with panel open
- `state.expandedGitChangeWorkspaces` — Set of workspace IDs with expanded panel
- Toggle via `toggle-git-changes-size` action
- Open/close via `open-git-changes` / `close-git-changes` actions

## Navigation Integration
Mobile bottom nav already had a "git" tab that toggles `mobileNavView = "git"` and fires `open-git-changes`/`close-git-changes`. No changes needed here — the existing toggle behavior works identically, just renders inline instead of as overlay.

## Key Patterns
- The `data-change-review` attribute is preserved for scroll snapshotting and change navigation (arrow key handlers, `scrollChangeReview`, etc.)
- All data-action handlers remain identical (`open-git-changes`, `close-git-changes`, `toggle-git-changes-size`, etc.)
- Diff rendering, file expand/collapse, hunk buttons all work unchanged since HTML structure within the panel is preserved

## Verification
- Frontend TypeScript build: `cd frontend && npm run build` ✓
- No backend changes required — no Wails bindings affected
