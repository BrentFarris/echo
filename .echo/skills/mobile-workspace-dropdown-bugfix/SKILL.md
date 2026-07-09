---
name: mobile-workspace-dropdown-bugfix
description: Fix for mobile workspace dropdown opacity and tap-selection closing behavior (B-029).
triggers:
    - mobile nav
    - dropdown
    - workspace selector
    - touch device
    - tap interaction
    - opacity
    - pointer-events
    - B-029
---

## Bug B-029: Mobile workspace dropdown visibility and tap interaction

### Problem
The mobile bottom nav workspace dropdown had two issues on touch devices:
1. Transparent background caused items to blend into content behind it — CSS used only var(--color-surface-elevated) without an opaque fallback.
2. Menu did not close after selecting a workspace — the activate-workspace handler never set state.workspaceDropdownOpen = false, leaving the dropdown visually open over the UI.

### Fixes Applied
1. styles.css (.mobile-nav-workspace-dropdown, ~line 5324):
   - Opaque fallback: background: var(--color-surface-elevated, #ffffff);
   - Disabled backdrop filter: -webkit-backdrop-filter: none; backdrop-filter: none;
   - Touch reliability: pointer-events: auto;

2. actions.ts (activate-workspace handler):
   - Added state.workspaceDropdownOpen = false; before calling getAppCallbacks().render() so the dropdown dismisses immediately after selection.

### Files Modified
- echo/frontend/src/styles.css
- echo/frontend/src/app/actions.ts

### Verification
- npm run build (frontend TypeScript/Vite) passes cleanly.
- go test ./... (all Go packages) passes.
