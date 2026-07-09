---
name: mobile-workspace-dropdown-pointer-fix
description: Fix for workspace dropdown pointer-down dismissal bug affecting both mobile and desktop selectors.
triggers:
    - mobile nav
---

## Problem
On touch devices, tapping a workspace dropdown item did not select the workspace — the menu closed but no selection occurred. The same issue affected the desktop left-nav workspace dropdown: clicking an option would close the dropdown before the `click` handler could activate the workspace.

## Root Cause
The global `pointerdown` capture listener in `frontend/src/app/events.ts` only checked for the mobile dropdown (`.mobile-nav-workspace-dropdown`). The desktop dropdown uses `[data-workspace-dropdown]`. Since `pointerdown` fires before `click`, clicking inside the unrecognized desktop dropdown caused premature dismissal and re-render, preventing the subsequent `click` event from selecting the workspace.

## Fix
The containment check in the `pointerdown` handler now covers both dropdown variants:
```ts
const mobileDropdown = appRoot.querySelector<HTMLElement>(".mobile-nav-workspace-dropdown");
const desktopDropdown = appRoot.querySelector<HTMLElement>("[data-workspace-dropdown]");
const isInMobileDropdown = mobileDropdown && mobileDropdown.contains(target);
const isInDesktopDropdown = desktopDropdown && desktopDropdown.contains(target);
if (!isInPill && !isInMobileDropdown && !isInDesktopDropdown) { ... dismiss ... }
```

## Key Takeaway
When implementing "tap outside to dismiss" patterns, the containment check must cover ALL interactive regions that should keep an overlay open across both mobile and desktop UI variants. Using `||` to combine element references causes only the first truthy element to be checked — always test each region independently with `contains`.

## File
- `frontend/src/app/events.ts` — global `pointerdown` capture listener (~line 60)
