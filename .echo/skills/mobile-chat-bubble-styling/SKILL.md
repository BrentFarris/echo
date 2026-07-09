---
name: mobile-chat-bubble-styling
description: 'Mobile chat bubble styling for .chat-message elements within @media (max-width: 720px), using margin-auto alignment, distinct backgrounds, and tail radii while preserving desktop boxed layout.'
triggers:
    - mobile
    - chat
    - bubble
    - styling
    - responsive
    - css media query
    - user message
    - assistant message
    - alignment
---

## Mobile Chat Bubble Styling (≤720px)

### Overview
Chat messages switch from boxed cards to conversational bubbles on mobile screens (≤720px viewport width). User messages float right; assistant messages float left. Desktop layout is completely unchanged.

### Key Implementation Details

**File**: `echo/frontend/src/styles.css` — inside the existing `@media (max-width: 720px)` block.

**Core selectors**:
- `.chat-message` — removes border, sets `border-radius: 12px`, adds subtle shadow, tightens padding to `10px 12px`, constrains max-width to `calc(100% - 32px)`
- `.chat-message.from-user` — `margin-left: auto; margin-right: 0` pushes right; accent-tinted background; `border-bottom-right-radius: 3px` creates tail effect
- `.chat-message.from-assistant` — `margin-left: 0; margin-right: auto` pushes left; surface-raised background; `border-bottom-left-radius: 3px` creates tail effect

**Action button positioning**:
- User bubbles: `.chat-message-actions` uses `order: -1` and `justify-content: flex-end` (buttons above content, right-aligned)
- Assistant bubbles: buttons stay below content, left-aligned via `justify-content: flex-start`

**Header tightening**: Reduced gap to `6px`, added top/bottom padding of `4px`, scaled down strong/span font sizes.

**Content scaling**: Markdown body at `0.92rem`; images/videos constrained to `max-width: 100%`; tool call code/pre at `0.82rem`.

### Why These Choices
- Uses `margin: auto` rather than `float` or absolute positioning — works naturally within the existing grid-based `.chat-message` structure
- `order: -1` leverages CSS Flexbox ordering without altering HTML markup
- Tail radii (`3px`) mimic real messaging app tails without needing pseudo-elements
- Shadow provides depth against the surface background, compensating for removed borders

### Verification
- `cd frontend; npm run build` must succeed (CSS is valid, no TypeScript changes)
- `go test ./...` unaffected (pure CSS change)
- Visual check: resize to ≤720px width in browser DevTools to confirm bubble alignment and desktop preservation at ≥721px

### Related Skills
- Touch target enforcement for mobile UI elements
- Chat message action button visibility on touch devices
- Kanban modal clearance on mobile screens
