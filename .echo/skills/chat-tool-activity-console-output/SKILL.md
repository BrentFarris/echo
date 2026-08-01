---
name: chat-tool-activity-console-output
description: 'ChatToolActivity ConsoleOutput: backend population for shell_command tool calls and frontend rendering with terminal-style CSS.'
triggers:
    - ChatToolActivity
    - ConsoleOutput
    - shell_command console output
    - tool activity UI
    - formatShellConsoleOutput
    - extractShellConsoleOutput
    - updateToolActivity
    - console-output CSS
    - renderToolCall
    - terminal styling chat
---

## ChatToolActivity ConsoleOutput

`ChatToolActivity` (in `internal/services/chat.go`) has a `ConsoleOutput string` field (`json:"consoleOutput,omitempty"`) that provides human-readable console output for tool calls visible in the chat UI.

### Backend population

`updateToolActivity` accepts a `consoleOutput string` parameter and sets it on the activity. All callers must pass this parameter:

- Non-shell tools pass `""`.
- `shell_command` uses `extractShellConsoleOutput(call, result)` at completion.

### Running preview

After setting status to `"running"`, if the tool is `shell_command`, arguments are parsed as JSON to extract the `command` field. A `ChatStreamEvent` with type `"tool_event"` emits `ConsoleOutput: "⏳ Running: <command>"`.

### Completion output

`extractShellConsoleOutput` (package-level helper) type-asserts `result.Output` to `map[string]any` (because JSON unmarshaling produces float64 for numbers), extracts `command`, `stdout`, `stderr`, `exitCode`, and `durationMilliseconds`, then calls `formatShellConsoleOutput`.

`formatShellConsoleOutput` builds:
```
> <command>
<stdout lines>
<stderr lines if any>

exit code: X  duration: Yms
```

### Frontend rendering

In `frontend/src/app/chat/index.ts`:

- `renderToolCall` renders `toolCall.consoleOutput` as `<pre class="console-output" data-console-output>` with `escapeHtml()`, placed between the arguments `<code>` block and the error/result blocks.
- `chatFileLinkTargets` includes `.chat-message.from-assistant .tool-call .console-output` in its selector list so file paths inside console output are linkified to workspace files.

In `frontend/src/styles.css`:

- `.tool-call .console-output` uses terminal-style styling: monospace font, dark background (`var(--color-surface-1)`), blue left border accent, `max-height: 300px` with scroll, `white-space: pre-wrap`, and `word-break: break-word`.

### Bindings workflow

Adding or changing fields on `ChatToolActivity` requires `wails generate module` to regenerate `frontend/wailsjs/go/models.ts`. The generated class includes the field and constructor assignment.

### Key facts

- `result.Output` is `any`; after JSON round-trip through `json.Marshal`, numeric fields become `float64`. Use type assertions to `map[string]any` and cast float64 values.
- The `ConsoleOutput` field is `omitempty` so non-shell tools don't send empty strings over the wire.
- All callers of `updateToolActivity` must include the `consoleOutput` parameter (8 total parameters).
