---
name: chat-debug-section-sticky-scroll
description: CSS sticky headers and bounded scroll for chat thinking/tools collapsible sections, including mobile viewport adjustments and themed scrollbar styling.
triggers:
    - sticky header
    - bounded scroll
    - debug section
    - thinking collapsible
    - tools collapsible
    - chat reasoning scroll
    - tool call overflow
    - max-height vh
    - scrollbar styling
    - mobile responsive chat
---

## Chat debug section sticky header + bounded scroll

Thinking (`data-debug-section="reasoning"`) and Tools (`data-debug-section="tools"`) `<details>` sections use CSS-only sticky headers and internal scrolling to prevent content from pushing the toggle out of view.

### Key selectors in `frontend/src/styles.css`

- `.debug-section summary` — sticky header with `position: sticky; top: 0; z-index: 2; background: var(--color-surface)`. Preserves existing cursor, padding, color, font-size, font-weight.
- `.debug-content` — reasoning content area, `max-height: 35vh; overflow-y: auto`
- `.tool-list` — tool call results grid, `max-height: 50vh; overflow-y: auto`
- Both have themed `::-webkit-scrollbar` styling: 8px width, transparent track, `var(--color-border)` thumb, 4px border-radius

### Mobile adjustments

Inside `@media (max-width: 720px)` block:
- `.debug-content { max-height: 30vh; }`
- `.tool-list { max-height: 40vh; }`

### HTML structure

Both chat view (`app/chat/index.ts`) and inline code view (`codeView/inlineChat.ts`) render the same structure:
```html
<details class="debug-section" data-debug-section="reasoning|tools">
  <summary>Thinking|Tools</summary>
  <div class="debug-content">...</div>   <!-- reasoning -->
  <div class="tool-list">...</div>        <!-- tools -->
</details>
```

### Invariants

- CSS-only — no JS changes for scroll behavior
- `overflow-y: auto` means scrollbar only appears when content exceeds max-height
- Sticky summary requires a solid background (`var(--color-surface)`) to prevent content bleed-through
- Max-height uses `vh` units so it scales with viewport; mobile gets reduced values
