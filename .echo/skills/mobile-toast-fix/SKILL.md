---
name: mobile-toast-fix
description: Fix for toast notifications being obscured by the fixed bottom navigation bar on mobile screens (<=720px).
triggers:
    - toast notification
    - bottom nav
    - mobile overlay
    - fixed positioning
    - z-index mobile
    - toast-region
---

## Problem
On mobile screens (<=720px), the .toast-region uses position: fixed; bottom: 18px, placing toast notifications directly behind the fixed bottom navigation bar (z-index: 10) which is approximately 64px tall.

## Root Cause
The base .toast-region style sets bottom: 18px. Inside the @media (max-width: 720px) block, the .mobile-bottom-nav is positioned at bottom: 0 with no explicit height but renders ~64px (matching the .main-content { padding-bottom: 64px } value). Toastes appear at 18px from viewport bottom -- fully obscured.

## Fix
Inside the existing @media (max-width: 720px) block, added an override to move .toast-region to bottom: 82px, clearing the nav bar entirely.

## Verification
Frontend build passes: npm run build inside echo/frontend directory (TypeScript compilation + Vite production build succeed). Desktop layout unaffected -- override only applies inside the 720px media query.

## Key Files
- echo/frontend/src/styles.css -- lines ~4913-4916 (the override inside the 720px breakpoint)
