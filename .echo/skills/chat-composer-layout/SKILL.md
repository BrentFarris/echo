---
name: chat-composer-layout
description: Structure and layout of the chat composer toolbar including left/right split, button order, data attributes, patch functions, mode dropdown, and mobile responsive rules.
triggers:
    - chat composer
    - toolbar layout
    - execute plan button
    - send stop button
    - left toolbar
    - right toolbar
    - composer toolbar
    - mode selector
    - mode dropdown
    - plan edit mode
---

## Chat Composer Toolbar Layout

### Toolbar Structure (renderChatPanel in frontend/src/app/chat/index.ts)

The composer toolbar (`chat-composer-toolbar`) is a flex row split into two halves:

**Left toolbar** (`chat-composer-toolbar-left`) — left-to-right order, each item separated by `<span class="chat-toolbar-separator"></span>`:
1. **Attach file** button — `data-chat-attachment-toggle`, opens attachment menu (image/video)
2. **Agent mode** toggle — `data-action="toggle-agent-mode"`
3. **Model selector** dropdown — `data-model-selector` / `data-model-dropdown`
4. **Mode selector** dropdown — `data-mode-selector` / `data-mode-dropdown`. Button has classes `model-selector mode-selector chat-toolbar-mode`. Dropdown `<li>` options have `data-mode-value="plan"` or `data-mode-value="edit"`. Replaced the approvals toggle.
5. **Execute plan** (decompose) button — `data-action="execute-plan"`, class `execute-button`, disabled when `session.busy || executing || messages.length === 0`. Shows spinner during execution (`is-busy` state).
6. **More options** button — `data-chat-more-toggle`, class `chat-toolbar-icon` only (no dedicated CSS class), with adjacent `.chat-more-menu` popup (`data-chat-more-menu`). Uses `icons.moreHorizontal`.

**Right toolbar** (`chat-composer-toolbar-right`) — contains only:
1. **Send/stop** button — `data-action="send-stop"`, dual-purpose send (idle) / stop (busy)

### Mode Selector Dropdown
Follows the model selector pattern:
- `renderModeOptions(workspaceID)` — renders Plan/Edit options using `chatComposerModeFor()`
- `bindModeSelector(root)` — click handler on `[data-mode-selector]` toggles dropdown
- `handleModeSelectorClick()` — toggles `modeDropdownOpen` flag, shows/hides dropdown
- `dismissModeDropdown()` — resets flag, closes dropdown
- `bindModeDropdownEvents(root)` — option clicks call `setChatComposerMode()` then `patchChatPanel()`
- Outside click dismissal handled in `bindChatAttachmentMenuDismissal()` document listener

### Key Data Attributes
- `[data-chat-form]`: form submit binding
- `[data-chat-input]`: textarea input/change/bindings
- `[data-action="send-stop"]`: send/stop click handler
- `[data-action="execute-plan"]`: execute plan button
- `[data-action="toggle-agent-mode"]`: agent mode toggle
- `[data-model-selector]` / `[data-model-dropdown]`: model selector dropdown
- `[data-mode-selector]` / `[data-mode-dropdown]`: mode selector dropdown (Plan/Edit)
- `[data-mode-value]`: individual mode option elements
- `[data-chat-attachment-toggle]` / `[data-chat-attachment-menu]`: media attachment menu
- `[data-chat-more-toggle]` / `[data-chat-more-menu]`: more options menu

### Patch Functions
- `patchChatPanel()`: full re-render of composer HTML
- `patchChatControls()`: updates busy/disabled states on toolbar buttons

### Mobile Responsive
- `@media (max-width: 720px)`: flex-direction column stacking; controls above textarea via `order: -1`
- All `.icon-button` elements get 44x44px minimum on mobile
- `.mode-dropdown:not([hidden])` on mobile: `bottom: calc(100% + 2px)`, `top: auto`, `max-width: calc(100vw - 16px)`
