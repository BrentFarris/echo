---
name: mobile-chat-controls
description: How mobile chat controls (stop/execute/clear) are rendered, synced during streaming, and styled with sticky positioning near the composer on mobile viewports.
triggers:
    - mobile chat
---

## Mobile Chat Controls Architecture

Mobile chat controls (stop/execute/clear) are rendered in `chat-mobile-controls` div between the chat log and composer, visible only at `max-width: 720px`.

### Key files
- `frontend/src/app/chat/index.ts` — `renderMobileChatControls()` renders the buttons; `patchChatControls()` updates state
- `frontend/src/styles.css` — `.chat-mobile-controls` base (`display: none`) and mobile media query (sticky, visible)

### Control sync during streaming
`patchChatControls()` must update ALL matching buttons across desktop heading and mobile controls. Use `querySelectorAll` for:
- `.stop-button` — disabled when not busy
- `.execute-button` — disabled when busy/executing/empty; toggles `is-busy` class, spinner icon
- `[data-action="clear-chat"]` — disabled when busy/executing/creating-skill/empty

Do NOT use single-element `querySelector` for these selectors—mobile controls will fall out of sync.

### Visual duplication prevention
On mobile (`max-width: 720px`), desktop heading controls are hidden via CSS so only the sticky mobile controls bar is visible. The hiding rule is scoped to `.chat-actions .stop-button, .chat-actions .execute-button` — **never use unscoped class selectors** (e.g., `.stop-button { display: none; }`) because both the desktop heading buttons and the mobile controls buttons share the same classes, causing all instances to be hidden.

### Sticky positioning
Mobile controls use `position: sticky; top: 0; z-index: 5; background: var(--color-surface)` to stay pinned at the top of the chat scroll container during scrolling. Border is on the bottom (`border-bottom`) to visually separate from content below.

### Pitfall: Shared class names
Desktop heading buttons (`.stop-button`, `.execute-button`) and mobile control buttons use identical class names. Any CSS rule targeting these classes must be scoped to avoid affecting both locations. The bug in B-007 was caused by an unscoped `.stop-button, .execute-button { display: none; }` rule that hid all instances including the mobile controls.

### Verification
- `cd frontend; npm run build` — TypeScript compilation
- Manual: resize browser to ≤720px, verify mobile controls appear at top and heading controls hide
