---
name: create-slash-command
description: Create or edit Echo slash commands for prompt text, standalone plugin tools, or app views and features. Includes every command option, registration examples, and how to generate a backing plugin when needed.
triggers:
  - slash command
  - create slash command
  - custom command
  - prompt shortcut
  - plugin command
  - app command
  - generate plugin
  - plugin authoring
---

# Create an Echo slash command

Use an existing tool or app when it already does the requested work. Generate a plugin only when the command needs new behavior. A slash command is an owner-defined shortcut saved on an Echo machine; adding a skill or plugin file does not register a command.

## Workflow

1. Identify the machine that owns the command and, for plugin/app actions, the selected or fixed bot. Commands belong to that machine's profile and are shared across its browsers. Chat suggestions follow the selected bot's machine, with the chat's home machine as fallback. Settings has its own machine selector. Remote machines must advertise `slash-commands/1`.
2. Read existing commands and the destination catalog through the owner API, or inspect **Settings → General → Slash commands**. Reuse actual plugin, tool, view, feature, and bot IDs from that machine. The catalog includes tool parameter schemas and `unavailable` reasons; disabled or pending plugins can still appear.
3. Choose `text`, `plugin`, or `app` using the complete options below. For a new plugin, read [Plugin generation examples](references/plugin-examples.md), obtain `plugin_template`, install the package, then register the command separately.
4. Create or edit the definition in Settings, or use the owner API below when an authorized owner session is available. Echo currently exposes no `slash_command_save` model tool. `plugin_install` installs packages, not commands. Do not invent a tool, write the private settings database, or describe a prepared definition as already registered.
5. Verify the saved definition and exercise its intended behavior. Invoking a plugin command executes its tool immediately, so use suitable sample arguments and the intended bot. Report the command name, owning machine, action, and any remaining registration or package-approval step.

## Complete command options

Create/update JSON has this shape: `{"name":"review","description":"Review the draft","action":{...}}`.

| Field | Type | Rules and defaults |
| --- | --- | --- |
| `name` | string, required | Enter without `/`. Trimmed and lowercased on save. Pattern `^[a-zA-Z][a-zA-Z0-9_-]{0,63}$`: starts with a letter, at most 64 ASCII characters, then letters/digits/`_`/`-`. Unique ignoring case, including names of removed built-ins. |
| `description` | string, optional | Human-readable suggestion text; trimmed; at most 1,000 UTF-8 bytes on the server. Empty is allowed. |
| `action` | object, required | Exactly one of the action types below. Saving retains only the fields relevant to that type. |
| `id` | string, server-managed | Generated on create. Update/delete by this ID in the URL, not by the slash name. A body `id` does not select the command to update. |
| `builtin` | boolean, server-managed | Custom saves always set `false`; clients cannot create a built-in this way. |
| `enabled` | boolean, server-managed | Custom saves always set `true`. There is no custom disable toggle; delete custom commands. Removed built-ins remain listed with `false` and can be restored. |

### Text action

| `action` field | Type | Rules and defaults |
| --- | --- | --- |
| `type` | string | Required value `"text"`. |
| `text` | string | Required, nonempty literal text, at most 1 MiB (1,048,576 UTF-8 bytes). Include any needed spaces/newlines explicitly. |
| `position` | string | `"insert"` (default when omitted/empty), `"append"`, or `"prepend"`. Insert at the removed command's location, end of the draft, or beginning of the draft, respectively. |

The slash token is removed before applying the edit. Existing text and file references are preserved; reference positions shift with the edit. This edits the draft without sending it or starting a model run. Undo restores the edit. Text is literal: no variables, templates, argument parsing, automatic separators, or deduplication.

Sample create bodies covering all three positions:

```json
{
  "name": "review",
  "description": "Insert a code-review request at the cursor",
  "action": {
    "type": "text",
    "position": "insert",
    "text": "Review this code for correctness and regressions. "
  }
}
```

```json
{
  "name": "concise",
  "description": "Add a concise-answer instruction",
  "action": {
    "type": "text",
    "position": "append",
    "text": "\n\nKeep the answer concise."
  }
}
```

```json
{
  "name": "explain",
  "description": "Prefix the draft with an explanation request",
  "action": {
    "type": "text",
    "position": "prepend",
    "text": "Explain the following step by step:\n\n"
  }
}
```

The current built-in is `/plan` (ID `plan`), a text append action with exact text `"\n\n You are to plan this request out, do not make any changes until the plan is approved"`. Its definition cannot be edited; it can be removed/restored. Removal does not free its reserved name. Each invocation appends another copy; the caret stays where the token was removed.

### Plugin action

| `action` field | Type | Rules and defaults |
| --- | --- | --- |
| `type` | string | Required value `"plugin"`. |
| `pluginId` | string | Required installed plugin ID on the owning machine. |
| `tool` | string | Required exact tool name declared by that plugin, eligible for standalone execution. |
| `arguments` | JSON object | Saved arguments passed to the tool. Defaults to `{}` when omitted; `null`, arrays, and scalar values are invalid. Must satisfy the tool's parameter validation. |
| `botId` | string, optional | Fixed bot ID on the same machine. Omit/empty to use the selected chat bot. The invoke endpoint falls back to `echo` if no bot is supplied. A saved fixed bot overrides the invocation's bot. |

Example using the built-in Calendar plugin:

```json
{
  "name": "schedules",
  "description": "List schedules using Echo's bot context",
  "action": {
    "type": "plugin",
    "pluginId": "calendar",
    "tool": "schedule_list",
    "arguments": {},
    "botId": "echo"
  }
}
```

Remove `botId` for selected-bot behavior. Arguments are fixed JSON: the composer text, text following `/name`, selection, and chat ID are not interpolated or passed to the tool. Use an app view for interactive input, or a text action for an instruction the user will send to the model.

Execution uses the saved exact plugin/tool pair with the bot's existing scope and plugin permissions. Results/errors appear in the invoking browser's per-chat command-results panel, separate from the draft and model conversation. It creates no model run. Save-time validation does not guarantee availability at invocation: package approval, enablement, bot access, package digest, and arguments are checked again.

Standalone commands cannot call model-provider tools, capabilities beginning `model.` or `comfyui.`, or `plugin.open` / `plugin.install`. These require a model run, saved chat artifacts, or chat app events. The restriction also applies to backend-to-host calls; wrapping a blocked capability in a new tool does not make it usable. Open an app with an `app` action.

### App action

| `action` field | Type | Rules and defaults |
| --- | --- | --- |
| `type` | string | Required value `"app"`. |
| `pluginId` | string | Required installed plugin with an `app` registration. A plugin having views alone is insufficient. |
| `viewId` | string, optional | A declared floating/embedded view ID. Omit/empty to resolve the registered `app.viewId`; the resolved ID is saved. Renderer views are not launch targets. |
| `feature` | string, optional | Exact feature ID declared in the selected view's `features` list. Omit/empty for the default view. It is not a URL, arbitrary route, or tool name. |
| `botId` | string, optional | Fixed local bot ID, or omit/empty for the selected chat bot, as above. |

Example using Monaco Editor:

```json
{
  "name": "scm",
  "description": "Open the editor's Source Control view",
  "action": {
    "type": "app",
    "pluginId": "monaco-editor",
    "viewId": "main",
    "feature": "source-control"
  }
}
```

Select feature IDs from the live catalog. App commands open/focus an Echo app in its supported presentation and can navigate an already-open view without reloading it. They do not launch external application links. Apps with feature destinations must both declare those features and handle their navigation messages; see the linked plugin examples.

## Registration and owner API

In **Settings → General → Slash commands**, choose **Add command**, supply name/description, select **Insert text**, **Plugin action**, or **Open Echo app**, and fill the corresponding fields. Plugin/app actions offer **Selected chat bot** or a fixed bot; app features offer **Default view** or the selected view's declared features. Save, then check the command under the same machine's chat suggestions.

The following paths include the server's `/api/v1` prefix. Use the target machine's authorized owner API; remote access needs its authenticated session. Browser `InstanceClient` examples elsewhere in the repository omit the prefix because the client supplies it. API payload IDs are machine-local; strip browser-qualified swarm identities through the existing client helpers when working in the frontend.

| Method and path | Body / result |
| --- | --- |
| `GET /api/v1/slash-commands` | Returns built-ins (including removed entries) and custom definitions. Also exposed as `slashCommands` in `/api/v1/state`. |
| `GET /api/v1/slash-commands/catalog` | Returns plugin `id`, `name`, optional `unavailable`, `tools` (including `parameters`, `capability`, optional `unavailable`), and eligible app `views` with `features`. View HTML is stripped. |
| `POST /api/v1/slash-commands` | Create with `{name, description, action}`; returns the normalized saved command and generated `id`. |
| `PUT /api/v1/slash-commands/{id}` | Replace an existing custom definition with the complete `{name, description, action}` body. This is not a partial patch. |
| `DELETE /api/v1/slash-commands/{id}` | Delete a custom command, or mark a built-in removed. Returns `true`. |
| `POST /api/v1/slash-commands/{id}/restore` | Restore a removed built-in; no body required. Custom commands cannot be restored this way. Returns `true`. |
| `POST /api/v1/slash-commands/{id}/invoke` | Plugin actions only. Body `{botId, digest}`; returns the tool result. Does not accept override arguments. |

For plugin invocation, `botId` is optional selected-bot context; `digest` is the current installed plugin's exact package digest (from `plugin_list` or `/api/v1/state`'s plugin inventory, not the slash catalog). These are invocation fields, not saved action options. A stale/missing digest that does not match the installed package is rejected. Example shape: `{"botId":"echo","digest":"<current plugin digest>"}`. Text editing and app opening are browser operations and do not use this invoke endpoint.

Definitions persist in the existing `settings/slash-commands` record, containing `custom` and `removed`. Use the API/UI rather than changing storage. There is no filesystem command discovery, manifest `slashCommands` registration, command alias field, per-command keyboard shortcut, chain of actions, dynamic argument prompt, or automatic send option in the current contract.

## Verify behavior

- Read back the saved definition, checking normalized name, action, IDs, and fixed/selected bot choice. Confirm the destination plugin is enabled and any requested permissions have been approved for the current package.
- In the main chat composer, type `/name` and click the suggestion or press Enter on the selected suggestion. Typing/sending/pasting/restoring slash text alone does not execute it. Space, Tab, and Escape dismiss the picker. Paths, code spans/fences, and file-reference ranges are excluded; inline editor chat does not have this command picker.
- For text, check insertion position, spaces/newlines, preserved references, Undo, and that no message was sent. For plugins, check the result panel and intended bot scope. For apps, check initial launch and navigation of an already-open view.
- When only delivering a definition or when registration is unavailable, provide the exact create body and Settings fields, and state that registration is still needed.

## Source of truth

Recheck these repository files when extending the feature or when behavior differs from this reference:

- [Command types and built-in definitions](../../internal/domain/slash_commands.go).
- [Validation, catalog, persistence, and API routes](../../internal/server/slash_commands.go).
- [Standalone restrictions](../../internal/plugins/standalone.go) and [app view resolution](../../internal/plugins/apps.go).
- [Settings form](../../web/src/slashCommandSettings.ts), [composer edits](../../web/src/slashCommands.ts), and command selection/invocation in [main.ts](../../web/src/main.ts).
- [Plugin templates](../../internal/plugins/templates.go), [package validation](../../internal/plugins/host.go), and plugin installation in [server tools](../../internal/server/tools.go).
- [Echo skill format](../../internal/workspaceskills/format.go) and [discovery/read service](../../internal/workspaceskills/service.go). Echo reads this folder directly; it accepts only `name`, `description`, and optional `triggers` in frontmatter. No `.echo/skills` placement or Codex-specific metadata is needed.
