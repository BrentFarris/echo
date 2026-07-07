---
name: chat-toolbar-popup-pattern
description: 'Chat toolbar popup architecture: model/mode dropdowns vs more menu. Menu items must use dedicated handlers with stopPropagation to avoid bubbling into parent toggles.'
triggers:
    - chat toolbar popup
    - more menu
    - model selector
    - mode selector
    - dropdown not closing
    - popup stays open
    - event bubbling chat
---

## Chat Toolbar Popup Architecture

The chat toolbar has three popup types:
1. **Model selector** (`[data-model-selector]` + `[data-model-dropdown]`) — dropdown with `<li>` items
2. **Mode selector** (`[data-mode-selector]` + `[data-mode-dropdown]`) — dropdown with `<li>` items  
3. **More menu** (`[data-chat-more-toggle]` + `[data-chat-more-menu]`) — popup with `<button>` items

### Critical pattern for menu items inside popups

Menu items inside the more menu use `<button>` elements. When clicked, the event bubbles up through the DOM. If a menu item uses `data-action`, the generic action handler fires AND the click can bubble to parent elements that re-trigger the popup toggle.

**Solution:** Menu items inside `[data-chat-more-menu]` must:
1. Use dedicated data attributes (e.g., `data-clear-chat-button`) NOT `data-action`
2. Have their own click handlers bound via functions like `bindClearChatButton(root)`
3. Call `event.stopPropagation()` to prevent bubbling into the more menu toggle
4. Call `dismissChatMoreMenu()` immediately before any async work or confirm dialogs

This matches how `bindModelDropdownEvents` and `bindModeDropdownEvents` work — they bind direct click handlers on their `<li>` items that dismiss the dropdown before doing work, rather than using the generic action system.

### Binding location

All popup bindings go in `bindChatEvents(root)` in `frontend/src/app/chat/index.ts`. New menu item handlers should be added there alongside `bindModelSelector`, `bindModeSelector`, `bindModelDropdownEvents`, `bindModeDropdownEvents`.
