---
name: chat-composer-dropdown-unification
description: Unified CSS architecture for chat composer dropdown menus (model, mode, attachment, overflow) sharing grid containers and flex item styles.
triggers:
    - dropdown styles
    - chat composer CSS
    - model dropdown
    - mode dropdown
    - attachment menu
    - more menu
    - grid layout
    - CSS unification
---

## Chat Composer Dropdown CSS Unification

All four chat composer dropdowns share a common design system in `frontend/src/styles.css`:

### Shared container pattern (grid)
- `.model-dropdown:not([hidden])` and `.mode-dropdown:not([hidden])` — shared base block with `display: grid`, `gap: 1px`, `padding: var(--space-sm)`, `border-radius: 8px`, `box-shadow: var(--shadow-modal)`
- `.chat-more-menu` — same grid pattern (JS-positioned, no static anchors)
- `.chat-attachment-menu:not([hidden])` — same grid pattern (JS-positioned)

Each container keeps individual sizing rules in separate blocks (e.g. model gets `min-width: 200px; max-height: 40vh`; mode gets `min-width: 80px`).

### Shared item/button pattern (flex)
- `.model-dropdown-option` and `.chat-more-menu button` share one selector block: `display: flex`, `align-items: center`, `gap: var(--space-md)`, `padding: var(--space-sm) var(--space-md)`, `border-radius: 4px`, `font-size: var(--text-sm)`
- `.chat-attachment-menu button` mirrors the same pattern in its own block
- No legacy `border-bottom` dividers — items are separated by the 1px grid gap

### Key pitfalls
- Mode dropdown `<li>` options reuse class `model-dropdown-option` (see `renderModeOptions`), so they inherit the shared item styles automatically.
- Mobile media query hides `.chat-attachment-menu` with `display: none !important`; desktop styles must not conflict.
- `.chat-more-menu` and `.chat-attachment-menu` are JS-positioned (no CSS `top`/`left`/`right`/`bottom`); model and mode dropdowns use CSS `bottom: calc(100% + 4px)`.
- Box-shadow is `var(--shadow-modal)` across all containers — do not hardcode values.
