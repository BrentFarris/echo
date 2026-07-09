---
name: dropdown-dynamic-positioning
description: How the model and mode selector dropdowns achieve dynamic left positioning via JavaScript instead of CSS.
triggers:
    - dropdown positioning
---

## Dropdown Positioning Model

Both the model selector (`data-model-dropdown`) and mode selector (`data-mode-dropdown`) use **JavaScript-calculated positioning** rather than CSS `left: 0`.

### How it works

When a dropdown opens, the click handler computes horizontal offset using `getBoundingClientRect()`:

```ts
const btnRect = button.getBoundingClientRect();
const dropRect = dropdown.getBoundingClientRect();
dropdown.style.left = `${btnRect.left - dropRect.left}px`;
```

This aligns the dropdown's left edge with its trigger button. When closed, `dropdown.style.left` is reset to `""`.

### CSS structure

Both `.model-dropdown:not([hidden])` and `.mode-dropdown:not([hidden])`:
- `position: absolute` — positioned relative to `.chat-composer-toolbar-left` which has `position: relative`
- `bottom: calc(100% + 4px)` — anchors above the toolbar
- **No `left` property** — left position is set via inline JS style
- The mode dropdown shares the `.model-dropdown` class for list styling and uses `.mode-dropdown` for its own sizing

### Key files

- `frontend/src/styles.css` — CSS rules (lines ~1226, ~1289)
- `frontend/src/app/chat/index.ts` — `handleModelSelectorClick()` and `handleModeSelectorClick()` functions

### Pitfalls

- Do not add `left: 0` back in CSS — it overrides the JS positioning.
- The dropdown must be visible (`hidden = false`) before calling `getBoundingClientRect()` on it, otherwise dimensions are 0.
- Reset `dropdown.style.left = ""` on close so repositioning recalculates correctly on next open.
