---
name: chat-message-buttons-touch-visibility
description: Touch device visibility for chat message action buttons using hover/pointer media queries, with 44x44px touch target enforcement.
triggers:
    - touch device
    - mobile chat buttons
    - hover reveal
    - pointer coarse
    - hover none
    - 44px touch target
    - message action buttons
---

## Chat Message Action Buttons - Touch Device Visibility

### Problem
Chat message action icon buttons (copy, edit, regenerate, prune, kanban) used `opacity: 0` with hover-reveal (`opacity: 1` on `.chat-message:hover`). This made them invisible and unusable on touch devices where `:hover` is unreliable or non-existent.

### Solution
A media query targeting touch devices was added to `frontend/src/styles.css`:

```css
@media (hover: none), (pointer: coarse) {
  .chat-message header .icon-button {
    opacity: 1;
    width: 44px;
    height: 44px;
  }

  .chat-message:hover header .icon-button.chat-copy-trigger {
    opacity: 1;
  }
}
```

### Key Decisions
- **Media query**: `@media (hover: none), (pointer: coarse)` covers both non-hover-capable devices and coarse-pointer (touch) devices. This is the standard pattern for touch-first styling.
- **Opacity override**: Buttons are always visible (`opacity: 1`) on touch devices, eliminating the need for hover to reveal them.
- **Touch target size**: Width and height set to `44px` on touch devices, meeting WCAG minimum touch target guidelines. The base `.icon-button` already had `width: 44px; aspect-ratio: 1`, but `.chat-message header .icon-button` overrode it to `30x30px`.
- **Copy button dimming fix**: On touch devices, a tap-hold can trigger `:hover`, which would dim the copy button (`opacity: 0.5`). The override prevents this.
- **Desktop preserved**: Mouse users retain the original hover-reveal behavior since the media query doesn't match them.

### Files
- `frontend/src/styles.css` — media query added near end of file, before `prefers-reduced-motion` block (line ~5014)

### Related Selectors
- `.chat-message header .icon-button` — message action buttons (30x30px, opacity 0 by default)
- `.chat-message:hover header .icon-button` — hover reveal for desktop
- `.chat-message:hover header .icon-button.chat-copy-trigger` — copy button dimming on hover
- Base `.icon-button` has `width: 44px; aspect-ratio: 1` — the touch media query restores this size

### Verification
- Frontend build: `cd frontend; npm run build`
- No Go test impact (pure CSS change)
