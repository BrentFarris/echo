---
name: chat-send-stop-button
description: How the unified send/stop button replaces separate plan toggle and send button in the chat composer.
triggers:
    - send button
    - stop button
    - dual-purpose
    - send-stop
    - chat composer
    - plan toggle removal
    - chat controls
    - patchChatControls
    - handleSendStopClick
---

## Unified Send/Stop Button Architecture

The chat composer uses a single dual-purpose button (`[data-action="send-stop"]`) at the right edge that toggles between send and stop behavior based on session state.

### Key Components

**Render (renderChatPanel)**: The button renders as type="button" (not submit), positioned outside the form wrapper. Icon and title switch based on session.busy || executing:
- Idle: shows icons.send, title "Send"
- Busy/Executing: shows icons.stop, title "Stop stream"
- When executing (Kanban triage): always disabled

**Event Binding (bindChatEvents)**: Click handler bound via [data-action="send-stop"] selector → handleSendStopClick.

**Handler Logic (handleSendStopClick)**:
```ts
export async function handleSendStopClick(event: Event) {
  if (session.busy || executing) {
    // Stop: calls StopChatStream(workspace.id), patches panel
    state.chatSessions.set(workspace.id, await StopChatStream(workspace.id));
    patchChatPanel();
    return;
  }
  // Send: inline SendChatMessageWithAttachments call (mirrors form submit logic)
  // Clears draft, images, video drafts, mentions; updates session state
}
```

**Live Updates (patchChatControls)**: Iterates all [data-action="send-stop"] buttons to update icon, title, aria-label, and disabled state:
- Busy/executing: stop icon, "Stop stream", not disabled
- Idle: checks draft/images/videos content -> disable if empty, send icon otherwise

### Design Decisions
- No form submission: Button is type="button" — sending happens entirely in JS click handler. Enter-to-send still works via the textarea's own keydown listener calling input.form?.requestSubmit().
- Dual action preserved: Both send and stop route through the same event target, keeping DOM minimal.
- Disabled during execution: When Kanban cards are executing (triage phase), the button stays disabled regardless of chat state.
- Import requirement: StopChatStream must be imported from "../../backend/services" alongside other service functions.

### Files Modified
- frontend/src/app/chat/index.ts — render, bind, handler, patch functions
- frontend/src/styles.css — removed .chat-plan-toggle rules (~46 lines)
- frontend/src/app/actions.ts — unchanged (still handles legacy stop-chat action)

### Notes
- The old handleChatPlanModeChange function remains defined but unbound (no [data-chat-plan-toggle] element exists). Harmless dead code.
- Plan mode state (state.chatPlanModes) persists independently — users who had plan mode toggled before this change will have their preference stored but the UI no longer exposes it.
