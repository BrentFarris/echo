---
name: chat-composer-mode-selector-isolation
description: Model and mode selector dropdowns share CSS class .model-dropdown-option causing cross-fire when clicking mode options. Bind model handlers only to elements with data-model-id attribute.
triggers:
    - model selector
    - mode selector
    - agent mode
    - composer mode
    - dropdown cross-fire
    - data-model-id
---

## Model vs Mode Dropdown Selector Isolation

Both the model selector dropdown (`data-model-dropdown`) and mode selector dropdown (`data-mode-dropdown`) are rendered inline in the chat toolbar. Their `<li>` options share class `model-dropdown-option`.

### The Bug

`bindModelDropdownEvents()` uses selector `.model-dropdown-option` which matches **both** model options (with `data-model-id`) and mode options (with `data-mode-value`). Clicking a Plan/Edit mode option fires both handlers — the mode handler sets composer mode, then the model handler reads `dataset.modelId` (undefined on mode options) and calls `selectChatModel("")`, resetting the chat endpoint.

### The Fix

`bindModelDropdownEvents` must use `.model-dropdown-option[data-model-id]` to select only model options:

```ts
export function bindModelDropdownEvents(root: ParentNode) {
  root.querySelectorAll<HTMLLIElement>(".model-dropdown-option[data-model-id]").forEach((option) => {
    option.addEventListener("click", () => {
      const modelID = option.dataset.modelId ?? "";
      selectChatModel(modelID);
      dismissModelDropdown();
    });
  });
}
```

`bindModeDropdownEvents` already uses `[data-mode-value]` which is correct.

### Files

- `frontend/src/app/chat/index.ts` — `bindModelDropdownEvents`, `bindModeDropdownEvents`, `renderModelOptions`, `renderModeOptions`

### Verification

Change agent mode (Plan/Edit) → model selector button label must not change.
