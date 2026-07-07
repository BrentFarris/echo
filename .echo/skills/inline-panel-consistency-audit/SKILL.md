---
name: inline-panel-consistency-audit
description: Audit checklist and patterns for verifying inline panel consistency across Git Changes, Settings, and other side panels — ensuring no residual overlay markup, unified Escape-key handling, and consistent mobile-nav-view state updates.
triggers:
    - audit inline panels
    - overlay cleanup
    - Escape key closing
    - mobile nav view consistency
    - panel toggle patterns
    - nav-tab click consistency
---

# Inline Panel Consistency Audit

## Overview
After converting Git Changes (card-3) and Settings (card-4) from overlay/drawer patterns to inline panels, card-5 performed a final audit confirming all legacy overlay patterns were eliminated and nav behavior is consistent.

## Key Findings & Patterns

### Markup (no overlays remain)
- **Git Changes**: Renders as `<aside class="git-changes-panel">` sibling inside `<main>` under conditional `${showGitChanges ? ...}`
- **Settings**: Renders as `<aside class="settings-sidebar">` sibling inside `<main>` under conditional `${showSettings ? ...}`
- Neither uses `role="dialog"`, backdrops, or overlay divs anymore
- CSS classes used: `.has-git-changes`, `.has-settings` on `<main>` for grid layout; `.git-changes-panel`, `.settings-sidebar` for the aside elements

### State management
- **Git Changes**: Controlled by `state.openGitChangeWorkspaces` (Set of workspace IDs) and `state.expandedGitChangeWorkspaces`
- **Settings**: Controlled by single boolean `state.settingsOpen`
- **Mobile nav**: Uses `state.mobileNavView` ("chat"/"kanban"/"code"/"git"/"settings")
  - Git Changes sets `mobileNavView = "git"` on open, `"kanban"` on close
  - Settings now sets `mobileNavView = "settings"` on open (added in card-5)
  - Close handler does not set mobileNavView (returns to previous view)

### Escape key handling (events.ts `handleGlobalKeydown`)
- Settings: `if (state.settingsOpen)` → closes and re-renders
- Git Changes: checks workspace + `state.openGitChangeWorkspaces.has(workspace.id)` → clears all git-related states and re-renders
- Change Review: similar pattern via `state.openChangeReviewWorkspaces`
- Card detail: falls through to `closeSelectedCardDetail`

### Action handlers (actions.ts)
- `open-git-changes`: calls `openWorkspaceGitRepository()`, sets `mobileNavView = "git"`
- `close-git-changes`: clears open/expanded/loading sets, resets `mobileNavView = "kanban"`
- `toggle-git-changes-size`: toggles expanded Set membership
- `open-settings`: clones settings draft, loads web access status, sets `mobileNavView = "settings"`
- `close-settings`: clears settingsOpen, formError, editing endpoint ID

### Mobile nav tab rendering (renderMobileBottomNav)
- Active state computed via `activeMobileView = showGitChanges ? "git" : mobileNavView`
- Five tabs: chat, kanban, code, git, settings — each with `data-mobile-nav-tab-index` 0-4
- Code tab switches between open/close based on current activeMobileView
- Git tab same pattern (open/close toggle)
- Settings always opens (always `open-settings` action regardless of active state)

## Verification Checklist
1. Search for `backdrop`, `overlay`, `drawer`, `role.*dialog` in render.ts — should find nothing related to Git Changes or Settings
2. Check CSS for old overlay rules — should find none
3. Confirm Escape keyhandler covers both panels in events.ts
4. Run `npm run build` to verify TypeScript compilation
5. Verify mobile nav tab indices cover all inline panels
