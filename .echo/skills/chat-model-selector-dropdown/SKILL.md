---
name: chat-model-selector-dropdown
description: CSS styling architecture for .model-selector base class shared by model and mode selector buttons, including hover/disabled states and mobile responsive overrides.
triggers:
    - model selector
---

## Model Selector CSS Architecture

### File
- `echo/frontend/src/styles.css` — base `.model-selector` rules (~line 1171), mobile overrides in `@media (max-width: 720px)` (~line 5867)

### Base `.model-selector` class
Shared button styles for both model selector (`chat-toolbar-model`) and mode selector (`mode-selector chat-toolbar-mode`) buttons. Both use class `model-selector` plus additional semantic classes.

```css
.model-selector {
  display: flex;
  align-items: center;
  gap: var(--space-xs);
  height: 24px;
  padding: 0 var(--space-md);
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--color-text-muted);
  font-size: var(--text-sm);
  font-weight: 500;
  cursor: pointer;
  white-space: nowrap;
  transition: background 100ms ease, color 100ms ease;
}

.model-selector:hover:not(:disabled) {
  background: var(--color-surface-muted);
  color: var(--color-text);
}

.model-selector:disabled {
  cursor: default;
  opacity: 0.35;
}
```

### Child elements
- `.model-selector-label` — max-width 160px, ellipsis overflow (80px on mobile)
- `.model-selector-chevron svg` — 10x10 chevron icon with stroke styling; hidden on mobile

### Refactor rationale
Previously used compound selector `.model-selector.chat-toolbar-model` for all styles. Changed to base `.model-selector` so both the model and mode selector buttons share identical styling. Hover and disabled states now target `.model-selector` directly without requiring the `chat-toolbar-model` qualifier.

### HTML classes
- Model selector button: `class="model-selector chat-toolbar-model"` with `data-model-selector`
- Mode selector button: `class="model-selector mode-selector chat-toolbar-mode"` with `data-mode-selector`

Both inherit from `.model-selector` base; additional classes (`chat-toolbar-model`, `mode-selector`, `chat-toolbar-mode`) are present for potential future differentiation but carry no current CSS rules.

### Mobile overrides
Inside `@media (max-width: 720px)`:
- `.model-selector` — compact inline button with `position: relative`, `flex-shrink: 0`, smaller padding, `border-width: 1px`, `font-size: var(--text-xs)`
- `.model-selector-label` — max-width reduced to 80px
- `.model-selector-chevron` — hidden

### Verification
Frontend build via `npm run build` (echo/frontend).
