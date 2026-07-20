---
name: frontend-chat-stream-events
description: How to add new chat stream event types in the frontend, including type definitions, event handler logic, and DOM patching for real-time updates.
triggers:
    - chat stream event
    - image_attached
    - video_attached
    - SSE event handler
    - applyChatStreamEvent
    - patchChatMessage
    - frontend event handling
    - DOM patching
    - real-time chat update
---

## Adding new chat stream event types

### Architecture

Chat stream events flow through three layers:

1. **Type definition** (`frontend/src/app/types.ts`): `ChatStreamEvent` is the union type for all stream events. Add new optional fields here (e.g., `imageAttachment?: services.ChatImageAttachment`).

2. **Event subscription** (`frontend/src/app/bootstrap.ts`): `EventsOn("echo:chat:event", ...)` routes every event to `applyChatStreamEvent()`.

3. **Event handler** (`frontend/src/app/chat/index.ts`, `applyChatStreamEvent`): Dispatches on `event.type`, mutates the message model, then queues a DOM patch via `queueChatStreamPatch`.

### Pattern for new event types

```typescript
// 1. Add field to ChatStreamEvent in types.ts
export type ChatStreamEvent = {
  // ...existing fields...
  myAttachment?: services.SomeAttachment;
};

// 2. Handle in applyChatStreamEvent (chat/index.ts)
if (event.type === "my_attached" && event.myAttachment) {
  const items = message.items ?? [];
  items.push(services.SomeAttachment.createFrom(event.myAttachment));
  message.items = items;
}

// 3. Patch DOM in patchChatMessage (chat/index.ts)
const container = element.querySelector<HTMLElement>(".my-container");
if (container) {
  const template = document.createElement("template");
  template.innerHTML = renderMyItems(message);
  element.replaceChild(template.content, container);
} else if (message.items?.length) {
  const template = document.createElement("template");
  template.innerHTML = renderMyItems(message);
  element.insertBefore(template.content, content || error);
}
```

### Key DOM patching rules

- Use `document.createElement("template")` + `template.innerHTML` to parse HTML strings into a `DocumentFragment`, then use `replaceChild` or `insertBefore`.
- Render functions (e.g., `renderChatMessageImages`) return the full wrapper div. Never set `innerHTML` directly on an existing container — it would nest the wrapper inside itself.
- The `patchChatMessage` function is called from `applyPendingChatStreamPatch` after every queued patch flushes. It handles all visible DOM updates for a message.

### Files

| File | Responsibility |
|------|---------------|
| `frontend/src/app/types.ts` | `ChatStreamEvent` type definition |
| `frontend/src/app/bootstrap.ts` | Event subscription wiring |
| `frontend/src/app/chat/index.ts` | `applyChatStreamEvent`, `patchChatMessage`, render functions |

### Attachment types

- `services.ChatImageAttachment`: `{ id, source, name, path?, mediaType, bytes, dataUrl? }`
- `services.ChatVideoAttachment`: same shape as image attachment
- Both use `createFrom()` static method to construct from raw event data
