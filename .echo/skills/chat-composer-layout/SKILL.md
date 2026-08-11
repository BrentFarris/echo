---
name: chat-composer-layout
description: Structure and layout of the chat composer including the contenteditable rich input, mention chips, chatComposerText newline handling, toolbar left/right split, button order, data attributes, patch functions, mode dropdown, and mobile responsive rules.
triggers:
    - chat composer
    - contenteditable
    - rich input
    - mention chip
    - file mention
    - toolbar layout
    - execute plan button
    - send stop button
    - composer toolbar
    - mode selector
    - mode dropdown
---

## Chat Composer Layout & Rich Input

### Composer Input is a contenteditable (not a textarea)
`[data-chat-input]` in `renderChatPanel` (frontend/src/app/chat/index.ts) is a `<div contenteditable="true">`, NOT a textarea. Keep this in mind for any code that reads/writes the composer:
- Read the plain-text value with `chatComposerText(editor)` (walks text nodes + mention chips + `<br>`/`<div>` newlines), NOT `editor.value`/`selectionStart`.
- Write/set it by rendering `chatDraftToHTML(draft, workspaceID)` (parses `@path`/`@"path"` tokens into chips, escapes the rest).
- `handleChatInput` syncs `state.chatDrafts` from `chatComposerText` on every input event.
- Caret/selection use DOM `Range`/`getSelection()`, not textarea `selectionStart`/`selectionEnd`. See `chatComposerCaretInfo`/`removeTextBeforeCaret`/`placeComposerCaretAtEnd`.
- Placeholder is a `data-placeholder` attribute shown via `.chat-composer [data-chat-input]:empty::before`.
- Disabling is done via `input.contentEditable = "false"` + `aria-disabled`, not `.disabled`.
- Enter (no shift, desktop) calls `form.requestSubmit()` via `[data-chat-form]`; mobile lets the browser insert a newline.

### chatComposerText newline handling (important)
Contenteditable wraps each line in `<div>` (or uses `<br>`). `chatComposerText` must:
- Insert a `\n` **between** sibling block elements (so multi-line drafts keep `line1\nline2`).
- NOT emit a spurious trailing newline after the last line — otherwise a lone `@` typed at the end serializes to `@\n`, which breaks the mention regex `(^|\s)@([^\s@]*)$` and the picker never opens.
- Trim trailing newlines (`replace(/\n+$/, "")`) as a safety net.
This is why typing `@` did nothing after switching the composer to contenteditable.

### Mention chips (rich `@file` links in the input)
When a user selects a file/folder from the `@` mention picker, `insertChatMention` inserts a `.chat-mention-chip` (contenteditable="false") into the editor instead of plain `@path` text:
- Chip carries `data-chat-file-mention`, `data-workspace-path`, `data-workspace-kind` ("file"/"directory"/"unknown"), `data-workspace-id`.
- Icon: folder (`icons.folder`) for directories, `</>` code (`icons.code`) for files.
- Click (`handleChatFileMentionClick`): file → `openWorkspaceCodeFile` (code view); directory → `OpenWorkspacePathExplorer` (system file browser).
- The plain-text draft still stores `@path`/`@"path"` text so the backend/model sees the same content as before; only the in-input rendering is rich.
- Restored drafts (from `state.chatDrafts`) are re-rendered as chips with kind "unknown" and resolved via backend `WorkspacePathKind(workspaceID, path)` (cached in `chatMentionKindCache`) by `resolveChatComposerMentionKinds`.
- Task refs (`@task:...`) are NOT file mentions — `isFileMentionPath` returns false for them, so they stay plain text.

### Toolbar Structure
The composer toolbar (`chat-composer-toolbar`) is a flex row split into two halves.

**Left toolbar** (`chat-composer-toolbar-left`) — left-to-right, items separated by `<span class="chat-toolbar-separator">`:
1. **Attach file** — `data-chat-attachment-toggle`, opens attachment menu (image/video)
2. **Agent mode** toggle — `data-action="toggle-agent-mode"`
3. **Model selector** — `data-model-selector` / `data-model-dropdown`
4. **Mode selector** — `data-mode-selector` / `data-mode-dropdown` (Plan/Edit)
5. **Execute plan** — `data-action="execute-plan"`, class `execute-button`, disabled when `session.busy || executing || messages.length === 0`
6. **More options** — `data-chat-more-toggle`, with `.chat-more-menu` (`data-chat-more-menu`)

**Right toolbar** (`chat-composer-toolbar-right`) — only the **Send/stop** button (`data-action="send-stop"`).

### Key Data Attributes
- `[data-chat-form]`: form submit binding
- `[data-chat-input]`: contenteditable composer input (rich)
- `[data-chat-file-mention]`: mention chip inside the input
- `[data-action="send-stop"]`, `[data-action="execute-plan"]`
- `[data-model-selector]`/`[data-model-dropdown]`, `[data-mode-selector]`/`[data-mode-dropdown]`, `[data-mode-value]`
- `[data-chat-attachment-toggle]`/`[data-chat-attachment-menu]`, `[data-chat-more-toggle]`/`[data-chat-more-menu]`

### Patch Functions
- `patchChatPanel()`: full re-render of composer HTML (restores draft via `chatDraftToHTML`, caret via `placeComposerCaretAtEnd`)
- `patchChatControls()`: updates busy/disabled states on toolbar buttons and `contentEditable` on the input

### Mobile Responsive
- `@media (max-width: 720px)`: flex-direction column stacking; controls above input via `order: -1`
- All `.icon-button` elements get 44x44px minimum on mobile
- `.mode-dropdown:not([hidden])` on mobile: `bottom: calc(100% + 2px)`, `top: auto`, `max-width: calc(100vw - 16px)`
- The `.chat-composer textarea` CSS rules now also apply to `.chat-composer [data-chat-input]` (shared base block).
