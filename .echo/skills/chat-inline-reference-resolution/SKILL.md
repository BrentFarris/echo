---
name: chat-inline-reference-resolution
description: Post-render DOM resolution pattern for inline chat references (@task:<id>, @path) — render placeholder spans in markdown, resolve against workspace state after DOM insertion, bind click handlers.
triggers:
    - chat reference
    - inline mention
    - task reference
    - post-render DOM
    - renderInlineMarkdown
    - resolveChatTaskRefs
    - linkifyAssistantFilePaths
    - data-task-ref
---

## Chat inline reference resolution pattern

Inline references in chat messages (e.g., `@task:<id>`, file paths) use a two-phase rendering approach:

### Phase 1: Markdown rendering (`frontend/src/markdown.ts`)

`renderInlineMarkdown` converts reference patterns to placeholder `<span>` elements with data attributes during HTML generation. The span contains the raw pattern text as fallback display.

```typescript
// @task:<id> example
html = html.replace(/@task:([A-Za-z0-9_-]+)/g, (_match, taskID) => {
  const escapedId = escapeAttribute(taskID);
  return `<span class="chat-task-ref" data-task-ref="${escapedId}" data-task-id="${escapedId}">@task:${taskID}</span>`;
});
```

Key: The regex must run **before** other inline replacements (code, bold, italic) so references aren't consumed by backtick or asterisk patterns.

### Phase 2: Post-DOM resolution (`frontend/src/app/chat/index.ts`)

After the message HTML is inserted into the DOM, `resolveChatTaskRefs(root)` walks `[data-task-ref]` elements:

1. Guards against double-processing via `data-task-ref-bound`
2. Looks up task by ID in `taskBoardFor(workspace.id).tasks`
3. Replaces textContent with `@<title>` if found; keeps fallback otherwise
4. Sets `title` attribute for tooltip
5. Binds click handler (`handleChatTaskRefClick`)

### Resolution call sites

Match the existing file-link resolution pattern — call `resolveChatTaskRefs` wherever `linkifyAssistantFilePaths` is called:

- `patchChatPanel()` — full panel re-render
- Streaming message patches — after `patchMarkdownElement` on content/reasoning elements
- Debug section toggles — when reasoning sections open

### Click navigation

Click handlers set state and trigger re-render:
```typescript
state.selectedTaskCards.set(workspace.id, taskID);
state.appMode = "tasks";
getAppCallbacks().render();
```

### CSS styling

Reference chips use `.chat-task-ref` class in `styles.css`: inline-flex layout, info-colored background tint, pointer cursor, hover transition. Ensure the rule block is properly closed — a missing `}` before this block previously broke surrounding code styles.

### Pitfalls

- Task board may not be loaded yet when chat renders; resolution gracefully falls back to raw ID display
- During streaming, messages are patched incrementally — call resolve on each patch target element, not just the root
- The `data-task-ref-bound` guard prevents re-binding click listeners on repeated patches
