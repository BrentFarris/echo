---
name: chat-toolbar-overflow-menu-removal
description: Chat toolbar overflow menu removal and migration patterns for removing UI components from the chat composer toolbar.
triggers:
    - remove overflow menu
    - toolbar cleanup
    - chat toolbar buttons
    - details element removal
    - CSS selector cleanup
    - action handler removal
---

## Chat Toolbar Overflow Menu Removal

The first `...` overflow menu (a `<details>` element with `data-chat-overflow`) was removed from the chat composer toolbar. The "more options" three-dot button (`data-chat-more-toggle`) remains as the sole overflow mechanism.

### Files Modified
- `frontend/src/app/chat/index.ts` — Removed the `<details class="chat-overflow">` markup block (separator, details/summary, menu div with create-skill and clear-chat buttons) from `renderChatPanel`. Removed create-skill button update from `patchChatControls`.
- `frontend/src/app/events.ts` — Removed outside-click dismissal handler for `[data-chat-overflow]` in the `pointerdown` listener.
- `frontend/src/app/actions.ts` — Removed `create-chat-skill` and `clear-chat` action handlers; cleaned up unused imports (`ClearChat`, `CreateSkillFromChat`).
- `frontend/src/styles.css` — Removed all `.chat-overflow-*` CSS rules (`.chat-overflow`, `.chat-overflow > summary`, `.chat-overflow > summary::-webkit-details-marker`, `.chat-overflow[open] > summary`, `.chat-overflow-menu`, `.chat-overflow-item`, `.chat-overflow-item:hover/.focus-visible`, `.chat-toolbar-overflow > summary`, `.chat-composer-toolbar .chat-overflow-menu`).

### Important Decisions
- `ClearChat` and `CreateSkillFromChat` backend services remain available — they're still used by `bindClearChatButton` (for the "New chat" button in the more menu) and `bindCreateSkillButton` respectively. Only the action handlers triggered by the removed overflow menu buttons were deleted.
- The `data-clear-chat-button` attribute is still used by the "New chat" button in the more options menu (`data-chat-more-menu`).
- Variable `creatingSkill` in `renderChatPanel` and `patchChatControls` remains in use for the more menu's create-skill button state.

### Pitfalls
- When removing overflow menu buttons, verify that action handlers are truly orphaned before deleting them from `actions.ts`. The "clear chat" functionality exists in both the removed overflow menu (`data-action="clear-chat"`) and the more menu (`data-clear-chat-button` with its own dedicated handler).
- CSS selectors `.chat-toolbar-overflow > summary` and `.chat-composer-toolbar .chat-overflow-menu` are separate from base `.chat-overflow-*` rules and must be removed independently.
