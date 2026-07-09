---
name: mobile-kaban-modal-clearance
description: CSS fixes for Kanban modals overlapping the fixed bottom nav on mobile screens (≤720px).
triggers:
    - mobile
    - bottom nav
    - modal overlap
    - kanban
    - card detail
    - create card dialog
    - change review
    - fixed nav
    - z-index
    - max-height
---

## Problem
Kanban modals (card detail view, create card dialog, change-review drawer) extend fully to viewport bottom on mobile (≤720px), obscuring their lower content behind the fixed 64px bottom navigation bar.

## Root Cause
- Backdrops use position: fixed; inset: 0 with low z-index values (10-20), conflicting with the mobile nav's z-index 10.
- Panel max-heights use calc(100vh - 32px) which doesn't account for the 64px fixed nav bar.

## Fix
Inside the existing @media (max-width: 720px) block in frontend/src/styles.css, added three overrides:

1. All three backdrop classes (.card-detail-backdrop, .kanban-card-create-backdrop, .change-review-backdrop) get z-index: 100 to ensure they render above the nav bar.

2. Three panel classes (.card-detail, .kanban-card-create-dialog, .change-review) get max-height: calc(100vh - 96px) to leave breathing room above the nav.

3. The expanded change-review drawer (.change-review.is-expanded) gets height/max-height: calc(100vh - 64px) since it fills full width without side padding.

## Verification
npm run build passes (TypeScript compilation + Vite production build). Desktop layout unaffected - override only applies inside the 720px media query.

## Key Files
- echo/frontend/src/styles.css -- lines ~5444-5460 (the new overrides inside the 720px breakpoint)
