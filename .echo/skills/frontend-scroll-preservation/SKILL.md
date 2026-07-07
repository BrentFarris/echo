---
name: frontend-scroll-preservation
description: How scroll position is captured and restored across re-renders in the frontend UI.
triggers:
    - scroll position
    - textarea scroll
    - re-render scroll
    - frontend render
    - patchChatPanel
    - captureScrollSnapshot
---

## Scroll Preservation Across Re-renders

The frontend uses two render paths that can replace DOM elements, resetting scroll positions:

1. **Full render** (`render()` in `app/render.ts`) — replaces `appRoot.innerHTML` entirely.
2. **Panel patch** (e.g., `patchChatPanel()` in `app/chat/index.ts`) — replaces a single panel section.

### Pattern

Before destroying or replacing DOM, capture scroll state from the affected element. After rendering, restore it on the new element.

**For scrollable containers** (chat log, card detail, change review, settings):
- Use `captureScrollSnapshot(selector)` / `restoreScrollSnapshot(selector, snapshot)` from `app/dom.ts`.
- These handle "stick to bottom" behavior via `atBottom` flag and a 48px threshold.

**For form controls** (textarea inputs):
- Capture `.value` and `.scrollTop` directly before DOM destruction.
- Restore both after render — value for content, scrollTop so users don't jump to the top while typing long messages.
- Example in `render()` (render.ts) and `patchChatPanel()` (chat/index.ts).

### Affected Elements

| Element | Selector | Render path(s) | What's preserved |
|---|---|---|---|
| Chat log | `[data-chat-log]` | Full render, scrollChatToBottom() | Scroll snapshot (atBottom-aware) |
| Card detail | `[data-card-detail]` | Full render | Scroll snapshot |
| Change review | `[data-change-review]` | Full render | Scroll snapshot |
| Settings form | `[data-settings-form]` | Full render | Scroll snapshot |
| Chat input textarea | `textarea[data-chat-input]` / `[data-chat-input]` | Full render, patchChatPanel() | `.value`, `.scrollTop` |

### Pitfalls

- Always capture **before** any DOM mutation that could remove the element.
- Always restore **after** the new element is in the DOM and bindings are attached.
- For textareas, restoring `scrollTop` must happen after setting `.value`, since changing value can reset scroll position in some browsers.
- The `captureScrollSnapshot`/`restoreScrollSnapshot` helpers return/accept `null` gracefully when the selector doesn't match — safe to call unconditionally.

### Verification

- Frontend: `cd frontend; npm run build` (TypeScript + Vite)
- Manual: Type a long message in the chat composer so it scrolls, then trigger a re-render (e.g., paste an image, switch workspace then back). The textarea should stay at the same scroll position.
