---
name: chat-panel-grid-layout
description: 'Chat panel grid layout: two-row template pinning the composer at the bottom while the log scrolls internally.'
triggers:
    - chat panel
    - grid layout
    - composer pinning
    - chat log scroll
    - CSS grid template rows
    - work-panel
---

## Chat Panel Grid Layout

`.chat-panel` in `frontend/src/styles.css` uses a **two-row CSS grid** to keep the composer pinned at the bottom:

```css
.chat-panel {
  grid-template-rows: minmax(0, 1fr) auto;
  align-content: stretch;
  min-width: 0;
}
```

### Children mapping

The `renderChatPanel` function (`frontend/src/app/chat/index.ts`) renders exactly two direct children inside `.chat-panel`:

| Grid Row | Selector | Purpose |
|----------|----------|---------|
| `minmax(0, 1fr)` | `.chat-log` (first child) | Fills available height; scrolls internally via existing `overflow-y: auto` |
| `auto` | `.chat-composer` (second child) | Pinned at bottom; sized to content |

### Invariant

`.chat-panel` must have exactly **two** grid children. Adding or removing a direct child will break the row mapping. The older 3-row template (`auto minmax(0, 1fr) auto`) was incorrect and has been removed — do not revert to it.

### No media-query overrides

There are no `@media` overrides for `.chat-panel`'s `grid-template-rows`. Mobile responsiveness is handled separately via the mobile-chat-composer-css rules at 720px breakpoint.
