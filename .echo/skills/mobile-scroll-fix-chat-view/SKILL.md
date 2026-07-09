---
name: mobile-scroll-fix-chat-view
description: 'Mobile scroll fix for chat view: workspace-panel overflow, split-panels/chat-panel height removal, and .main-content height constraint in @media (max-width: 720px).'
triggers:
    - mobile scroll
    - chat view scroll
    - workspace-panel overflow
    - split-panels height
    - chat-panel height
    - touch scrolling
    - media query 720px
    - main-content height
---

## Mobile Scroll Fix in Chat View

### Problem
On mobile viewports (≤720px), the chat view was not scrollable via touch events. Three issues blocked scrolling:

1. `.workspace-panel` had `overflow: hidden` in the first `@media (max-width: 720px)` block, preventing scroll propagation.
2. Redundant `height: 100%` rules on `.split-panels` and `.chat-panel` in the second `@media (max-width: 720px)` block constrained heights so content couldn't flow naturally.
3. `.main-content` had `overflow: auto` but lacked `height: 100%`, so it didn't fill its grid-constrained wrapper and couldn't create a scrollable area.

### Fix (in `frontend/src/styles.css`)

**First media query block** (`@media (max-width: 720px)` starting ~line 4988):
- Changed `.workspace-panel` overflow to `visible` (was `hidden`).
- Added `height: 100%` to `.main-content` so it fills its grid-constrained wrapper and `overflow: auto` creates a proper scrollable area.

```css
.main-content {
  height: 100%; /* added */
  padding: var(--space-lg-md);
  padding-bottom: 6var(--space-sm);
  overflow: auto;
}

.workspace-panel {
  height: 100%;
  min-height: 100%;
  overflow: visible; /* was hidden */
}
```

**Second media query block** — Removed these blocks entirely:
- `.split-panels { height: 100%; overflow: hidden; }`
- `.chat-panel { height: 100%; }`

### Why It Works
With `overflow: visible` on `.workspace-panel`, scroll events propagate up to `.main-content`. The `height: 100%` ensures `.main-content` fills the grid-constrained parent, so `overflow: auto` has a bounded height against which to create scrolling. Removing fixed height constraints on child panels allows content to flow naturally.

### Verification
Run `npm run build` in `frontend/` to confirm CSS validity. Test on a mobile viewport (≤720px) that touch scrolling works when content exceeds viewport height.
