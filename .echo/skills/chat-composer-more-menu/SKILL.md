---
name: chat-composer-more-menu
description: How the chat composer more options menu (three-dot button) is structured, styled, and wired up including toggle handler, dismissal, outside-click handling, dynamic JS positioning, menu items, and patterns for extending with new buttons.
triggers:
    - more menu
    - three dots
    - ellipsis button
    - composer toolbar left
    - new chat
    - clear chat from toolbar
    - create skill
    - workspace skill from chat
    - chat-more-toggle
    - chat-more-menu
    - dropdown positioning
---

## Chat Composer More Menu (`...` Button)

### Location
- HTML: `frontend/src/app/chat/index.ts` → `renderChatPanel()` inside `.chat-composer-toolbar-left`, after the execute button (separator + button + menu)
- JS handlers: same file, `handleChatMoreToggle()`, `dismissChatMoreMenu()`, `bindChatEvents()`
- CSS: `frontend/src/styles.css` — `.chat-more-menu` only; no dedicated toggle class

### Structure
The left toolbar (`.chat-composer-toolbar-left`) contains the More menu near its end. Left-to-right order of all items:
1. Attach file button + attachment menu popup
2. Agent mode toggle
3. Model selector dropdown
4. Mode selector dropdown
5. Execute plan button
6. **More toggle** button — `data-chat-more-toggle`, class `chat-toolbar-icon` only (no `chat-more-toggle` class), uses `icons.moreHorizontal`
7. **More menu** popup — `data-chat-more-menu`, class `chat-more-menu`, hidden by default

The right toolbar (`.chat-composer-toolbar-right`) contains only the Send/stop button.

### Menu Items
- **"New chat"** — `data-clear-chat-button`, bound in `bindClearChatButton()`. Dismisses menu, shows confirmation dialog, calls `ClearChat()` from backend services.
- **"Create skill"** — `data-create-skill-button`, bound in `bindCreateSkillButton()`. Dismisses menu, guards against duplicates via `state.creatingChatSkills`, calls `CreateSkillFromChat(workspace.id)`. Disabled when chat is busy, plan is executing, or skill creation is in progress. Uses `icons.star`.

### State & Dismission
- `chatMoreMenuOpen` module-level boolean tracks open/closed state
- `handleChatMoreToggle()` toggles visibility with dynamic JS positioning
- `dismissChatMoreMenu()` closes menu and resets aria-expanded
- Outside-click dismissal is handled in `bindChatAttachmentMenuDismissal()` document click listener

### Dynamic Positioning
The dropdown uses JS-set `style.left` and `style.top` on toggle, positioned relative to `.chat-composer-toolbar-left` (which has `position: relative`). The toggle handler measures menu height via a hidden visibility pass, then computes offset from button bounding rect minus container bounding rect. CSS does not set static `right` or `bottom` values — positioning is entirely JS-driven.

### CSS Notes
- `.chat-more-menu` is `position: absolute` with no static horizontal/vertical anchors (left/right/top/bottom are set by JS)
- The toggle button inherits all styling from `.chat-toolbar-icon` (26×26px, 15px SVG) — no dedicated `.chat-more-toggle` rules exist
- Menu buttons use flex row with icon + text layout

### Extending the Menu
Add new `<button type="button" data-your-button>` elements inside the `[data-chat-more-menu]` div in `renderChatPanel()`. Bind them with a dedicated function (e.g., `bindYourButton()`) called from `bindChatEvents()`. The binding should:
1. Call `event.stopPropagation()` and `dismissChatMoreMenu()` on click
2. Guard against duplicate/in-progress operations
3. Call the backend service wrapper from `frontend/src/backend/services.ts`
4. Re-render via `patchChatPanel()` on completion

Menu buttons are disabled with `${session.busy || executing || inProgress ? "disabled" : ""}` pattern.
