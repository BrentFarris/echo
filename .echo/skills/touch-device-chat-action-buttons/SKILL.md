---
name: touch-device-chat-action-buttons
description: 'Fixes chat message action button visibility on touch devices using @media (hover: none) CSS override.'
triggers:
    - touch device
    - mobile chat buttons
    - hover reveal
    - pointer coarse
    - hover none
    - message action buttons
---

## Problem
Chat message action buttons (copy, edit, retry, kanban, prune) are hidden by default (`opacity: 0`) and only revealed via `.chat-message:hover header .icon-button`. This makes them invisible on touch devices that lack hover.

## Solution
Added a `@media (hover: none)` block in `frontend/src/styles.css` (after line ~637) that overrides the opacity rule to keep buttons always visible on non-hovering devices:

```css
@media (hover: none) {
  .chat-message header .icon-button {
    opacity: 1;
  }
  /* Preserves subtle defaults and hover effects within the media query */
}
```

Within the same media query, secondary hover styles (background color changes, red prune color, copy-trigger dimming at 0.5) are also replicated so they still function on touch when tapped.

## Key Design Decisions
- Used `@media (hover: none)` rather than `@media (pointer: coarse)` — `(hover: none)` more reliably targets devices without hover capability including hybrid tablets with touch-first interaction.
- Only the minimum necessary rules were overridden inside the media query; all other desktop-only hover behaviors remain unchanged.
- No JavaScript or class toggling needed — purely declarative CSS.

## Files Modified
- `echo/frontend/src/styles.css` — added `@media (hover: none)` block after the existing `.chat-message header .icon-button` rules (~line 638).

## Verification
- Frontend build passes: `cd frontend; npm run build` (TypeScript + Vite).
