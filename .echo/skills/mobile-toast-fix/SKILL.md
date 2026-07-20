---
name: mobile-toast-fix
description: Fix for toast notifications being suppressed and obscured on mobile screens (≤720px).
triggers:
    - toast notification
    - bottom nav
    - mobile overlay
    - fixed positioning
    - z-index mobile
    - toast-region
    - pushToast
---

## Problem
On mobile screens (≤720px), toast notifications had two issues:
1. **JavaScript suppression**: `pushToast` in `toasts.ts` had a blanket early return for viewports ≤720px, silently suppressing all toasts.
2. **CSS overlap**: `.toast-region` used `bottom: 18px`, placing toasts behind the fixed bottom navigation bar (~64px tall).

## Fixes
1. **TypeScript** (`frontend/src/app/toasts.ts`): Removed the viewport guard in `pushToast`:
   ```typescript
   // REMOVED:
   if (window.innerWidth <= 720) {
     return;
   }
   ```
2. **CSS** (`frontend/src/styles.css`): Inside `@media (max-width: 720px)`, override `.toast-region` to `bottom: 82px`, clearing the nav bar entirely.

## Invariants
- `pushToast` must NOT suppress toasts based on viewport width — CSS handles positioning.
- Mobile toast region must sit above the fixed bottom nav (~64px + clearance).

## Verification
- `cd frontend; npm run build` passes.
- Desktop layout unaffected — CSS override only applies inside the 720px breakpoint.

## Key Files
- `frontend/src/app/toasts.ts` — `pushToast()` function
- `frontend/src/styles.css` — `.toast-region` mobile positioning in the 720px media query
