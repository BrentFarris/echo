---
name: mobile-chat-composer-css
description: Mobile-responsive CSS for chat log scrolling and composer layout under 720px viewport width, including sticky footer positioning above bottom nav, model selector dropdown anchoring, and container structure.
triggers:
    - mobile layout
    - composer bar
    - responsive CSS
    - media query
    - chat log
    - scrolling
    - dropdown positioning
    - sticky footer
    - bottom nav
    - input container
---

Mobile responsive CSS for chat UI under 720px viewport width, located in `@media (max-width: 720px)` blocks in `frontend/src/styles.css`.

Three separate `@media (max-width: 720px)` blocks exist in the file. The first block (~line 5726) handles general mobile layout overrides including chat log scrolling, attachment toggle/menu hiding, model selector/dropdown positioning, composer layout, and sticky footer behavior.

### DOM structure (from `frontend/src/app/chat/index.ts`)
```
form.chat-composer[data-chat-form]              ← sticky on mobile; wraps input + toolbar
  div.chat-composer-main[data-chat-input-wrap]  ← input area with drafts, mention picker, textarea
    .chat-image-drafts / .chat-video-drafts     ← optional attachment previews
    [mention picker]
    textarea[data-chat-input]                   ← message input
  div.chat-composer-toolbar                     ← options row (model selector, buttons, send)
    .chat-composer-toolbar-left                 ← position: relative on mobile (CRITICAL)
      [attachment toggle]                       ← hidden on mobile
      .chat-attachment-menu                     ← hidden on mobile
      [agent mode button]
      .model-selector                           ← compact inline button
      .model-dropdown                           ← absolute, anchored to toolbar-left parent
      [approvals button]
    .chat-composer-toolbar-right
      .send-button
```

The input box (`.chat-composer-main`) and options row (`.chat-composer-toolbar`) share `.chat-composer` as their common container — no additional wrapper div is needed.

### Sticky footer on mobile
- **`.chat-composer { position: sticky; bottom: 56px; z-index: 15; ... }`** — keeps the composer visible at the bottom of the viewport when scrolling long chat logs on mobile. The `bottom: 56px` offset positions it directly above the fixed `.mobile-bottom-nav` (which is 56px tall).
- Sticky requires a scrollable ancestor. On mobile, `.main-content` has `overflow: auto`, and `.chat-log` also scrolls independently.

### Model selector & dropdown in the 720px block
- **`.chat-composer-toolbar-left { position: relative; }`** — CRITICAL: establishes the containing block for the `.model-dropdown` sibling. The dropdown is a child of `.chat-composer-toolbar-left`, NOT `.model-selector`. Without this, the absolutely positioned dropdown escapes up the DOM tree and misaligns.
- `.model-selector-label { max-width: 80px; overflow: hidden; text-overflow: ellipsis; }` — truncated label on small screens.
- `.model-selector-chevron { display: none; }` — chevron hidden on mobile.
- `.model-dropdown:not([hidden]) { position: absolute !important; bottom: calc(100% + 2px); top: auto !important; left: 0; min-width: 200px; max-width: calc(100vw - 16px); max-height: 40vh; z-index: 100; }` — dropdown appears above the toolbar-left area.

### Composer rules in the 720px block
- `.chat-composer-main { display: flex; flex-direction: row; align-items: stretch; gap: var(--space-sm); }`
- `.chat-composer textarea { flex: 1 1 auto; min-height: 36px; max-height: 120px; ... }` — compact single-row input feel.

### Pitfalls
- The dropdown is a child of `.chat-composer-toolbar-left`, not `.model-selector`. Positioning must target `toolbar-left` as the containing block, not the selector button itself.
- Do not add `overflow: hidden` to ancestors of the dropdown, as it will clip the absolutely positioned element.
- Sticky positioning requires a scrollable ancestor — ensure `.main-content` or `.chat-panel` has overflow that allows scrolling, otherwise sticky will not activate.

Verification: `npm run build` (frontend TypeScript + Vite CSS bundling).
