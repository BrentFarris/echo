---
name: mobile-workspace-dropdown-pointer-fix
description: Fix for workspace dropdown pointer-down dismissal bug affecting both mobile and desktop selectors, and context menu interference.
triggers:
    - mobile nav
    - context menu
    - show-in-explorer
    - right-click workspace
    - workspace dropdown
---

## Problem
On touch devices, tapping a workspace dropdown item did not select the workspace — the menu closed but no selection occurred. The same issue affected the desktop left-nav workspace dropdown: clicking an option would close the dropdown before the `click` handler could activate the workspace.

Additionally, right-clicking a workspace in the dropdown to show the context menu (e.g. "Show in Explorer") would not work — clicking the context menu item did nothing. The `pointerdown` handler that dismisses the workspace dropdown would fire first, triggering a full re-render that destroyed the context menu DOM node before its `click` event could be processed.

## Root Cause
The global `pointerdown` capture listener in `frontend/src/app/events.ts` only checked for the mobile dropdown (`.mobile-nav-workspace-dropdown`). The desktop dropdown uses `[data-workspace-dropdown]`. Since `pointerdown` fires before `click`, clicking inside the unrecognized desktop dropdown caused premature dismissal and re-render, preventing the subsequent `click` event from selecting the workspace.

For the context menu issue: when the workspace dropdown is open and the user right-clicks a workspace item, both the dropdown AND the context menu (`[data-context-menu]`) are visible. Clicking inside the context menu's "Show in Explorer" button fires `pointerdown` first. The handler would close the workspace dropdown (since the context menu is not inside the dropdown), which triggers `render()` and destroys the context menu element before the `click` event reaches it.

## Fix
The containment check in the `pointerdown` handler now covers both dropdown variants AND the context menu:

```ts
const mobileDropdown = appRoot.querySelector<HTMLElement>(".mobile-nav-workspace-dropdown");
const desktopDropdown = appRoot.querySelector<HTMLElement>("[data-workspace-dropdown]");
const contextMenu = appRoot.querySelector<HTMLElement>("[data-context-menu]");
const isInMobileDropdown = mobileDropdown && mobileDropdown.contains(target);
const isInDesktopDropdown = desktopDropdown && desktopDropdown.contains(target);
const isInContextMenu = contextMenu && contextMenu.contains(target);
if (!isInPill && !isInMobileDropdown && !isInDesktopDropdown && !isInContextMenu) { ... dismiss ... }
```

## Key Takeaway
When implementing "tap outside to dismiss" patterns, the containment check must cover ALL interactive regions that should keep an overlay open. This includes:
- Mobile dropdown variants
- Desktop dropdown variants
- Any overlay elements (context menus, dialogs) that may be visible simultaneously

Using `||` to combine element references causes only the first truthy element to be checked — always test each region independently with `contains`.

## File
- `frontend/src/app/events.ts` — global `pointerdown` capture listener (~line 62)
