---
name: echo-plugins
description: Build, validate, package, and stage optional Echo plugins using the Echo Plugin API v1 security and lifecycle contracts.
triggers:
  - build an Echo plugin
  - create a plugin
  - plugin view
  - plugin tool
  - floating window
  - Echo integration
  - package a plugin
  - stage a plugin
---

# Echo Plugin Authoring

Use this skill whenever the user asks to create or change an optional Echo plugin. Chat, Trajectory, Code, Git, Settings, Terminal, workspace services, and existing Echo tools are core features; do not recreate or override them with plugins.

## Non-negotiable safety boundary

- Work only under a path inside the active workspace.
- Use `echo_plugin_scaffold` to create a reviewable source skeleton, normal workspace file tools and `shell_command` to implement/build/test it, `echo_plugin_validate` to inspect the finished package, and `echo_plugin_stage` only when the package is ready for human review.
- Never claim a stage is installed, enabled, or running. Staging returns a real pending-stage ID. Only the owner can approve it through Echo's trusted review UI.
- No model-callable operation can approve, enable, update, or execute newly downloaded/generated native code.
- Never place a secret value in the manifest, source, workspace configuration, logs, UI messages, tool output, or tests. Declare a `secret` setting and use the resolved runtime configuration.
- Native backends run with the Echo owner's OS permissions. Manifest permissions disclose intent and require approval; they are not an OS sandbox.

## Package layout

Every installable package is self-contained:

```text
echo-plugin.json
README.md
LICENSE
assets/
ui/<view>/index.html
ui/<view>/app.js
ui/<view>/style.css
backend/<goos>-<goarch>/<executable>
```

Source-only folders may additionally contain `backend-src/`, tests, and `.github/workflows/`. GitHub releases/repositories must already contain usable artifacts: Echo never runs remote build scripts, package-manager hooks, or lifecycle scripts.

## Manifest v1

The published editor/CI schema lives at `docs/plugins/echo-plugin.schema.json`. Use it for early feedback, then run `echo_plugin_validate`; the host validator is authoritative for package files, path confinement, namespaces, and core collisions.

```json
{
  "manifestVersion": 1,
  "id": "example-plugin",
  "name": "Example Plugin",
  "version": "1.0.0",
  "description": "What it adds",
  "echo": { "api": "^1" },
  "permissions": [],
  "runtime": {
    "protocol": "echo-jsonrpc-1",
    "targets": {
      "windows-amd64": { "path": "backend/windows-amd64/example-plugin.exe", "args": [] },
      "linux-amd64": { "path": "backend/linux-amd64/example-plugin", "args": [] },
      "darwin-amd64": { "path": "backend/darwin-amd64/example-plugin", "args": [] },
      "darwin-arm64": { "path": "backend/darwin-arm64/example-plugin", "args": [] }
    }
  },
  "contributes": { "views": [], "tools": [], "settings": [], "rpc": [] }
}
```

IDs and view IDs are lowercase kebab-case. Versions are semantic versions. Paths are package-relative and may not traverse outside the package. Symlinks are rejected. A package cannot inject host CSS/JavaScript, register HTTP routes, replace core services/routes/tools, or depend on another plugin in v1. One version per plugin ID can be installed.

### Views

Views are singleton contributions. A `page` uses `#/plugins/<plugin-id>/<view-id>` and fills Echo's normal content area. A `floating` view opens a host-owned draggable/resizable window and survives core route changes.

```json
{
  "id": "dashboard",
  "kind": "page",
  "title": "Dashboard",
  "icon": "assets/icon.svg",
  "entry": "ui/dashboard/index.html",
  "defaultSize": { "width": 900, "height": 650 },
  "minimumSize": { "width": 320, "height": 300 }
}
```

UI runs in an opaque-origin iframe with exactly `sandbox="allow-scripts"`: no same-origin access, forms, popups, downloads, host DOM, cookies, or direct host APIs. Use external local JS/CSS files; the plugin CSP forbids network connections and inline scripts.

### UI bridge (`echo-ui-bridge-1`)

On load, post `{type:"echo-plugin-ready", protocol:"echo-ui-bridge-1"}` to the parent. Accept initialization only from `window.parent`. It supplies `nonce`, `pluginId`, `viewId`, `sessionId`, non-secret `config`, workspace ID, and `theme` CSS tokens. Every request must echo the exact nonce/plugin/view identity:

```js
parent.postMessage({
  type: "echo-plugin-request", nonce, pluginId, viewId,
  id: crypto.randomUUID(), method: "storage.get",
  params: { scope: "workspace", key: "layout" }
}, "*");
```

Responses use `echo-plugin-response`; backend events use `echo-plugin-event`; theme changes use `echo-plugin-theme`. Validate their nonce/plugin/view identity before accepting them. Supported operations are:

- `rpc.invoke`: `{method, params}`; the method must be declared in `contributes.rpc`.
- `storage.get`, `storage.set`, `storage.delete`: `{scope:"global"|"workspace", key, value?}`.
- `window.close` and `window.setTitle`.
- `notification.show`, `clipboard.write`, and `external.open` only with approved `ui.notifications`, `ui.clipboard-write`, or `ui.external-links` permission declarations.

Theme token names include `--echo-background`, `--echo-surface`, `--echo-surface-raised`, `--echo-border`, `--echo-text`, `--echo-text-muted`, `--echo-accent`, `--echo-accent-contrast`, and `--echo-danger`.

### Agent tools

Tools are static, reviewable manifest entries. Names must start with the normalized plugin namespace (`my-plugin` becomes `my_plugin_`). Declare object input JSON Schema, optional output schema, backend method, timeout, and exactly one display classification: `readOnly` or `mutating`.

```json
{
  "name": "example_plugin_lookup",
  "description": "Look up an example record.",
  "inputSchema": {
    "type": "object",
    "properties": { "id": { "type": "string" } },
    "required": ["id"],
    "additionalProperties": false
  },
  "outputSchema": { "type": "object" },
  "method": "tools.lookup",
  "timeoutSeconds": 30,
  "readOnly": true
}
```

Inputs and outputs are schema-validated and size-limited. Plugin tools never appear in Plan mode or research workers. General/unrestricted modes receive active tools approved for the installed digest. Restricted custom modes must explicitly include a plugin tool. Echo rechecks activation and approval at execution time.

### Settings and permissions

Settings are host-rendered; plugins cannot inject Settings UI. Supported types: `string`, `url`, `number`, `boolean`, `select`, and `secret`. Each entry needs `key`, `type`, `scope` (`global` or `workspace`), `label`, and optional `help`, `required`, validation, and non-secret default. Secret defaults are forbidden.

Declare permissions narrowly with a name and owner-facing reason. Native `network`, `filesystem`, `process`, and `secrets` declarations disclose backend intent. Include specific hosts in `hosts` for network access. UI bridge permissions use the names above.

## Backend protocol (`echo-jsonrpc-1`)

The backend is an executable started directly without a shell, with a minimal environment and its plugin data directory as its working directory. Stdout is newline-delimited JSON-RPC 2.0 only; log to stderr. Each message must fit on one line. Do not print banners to stdout.

Echo first calls `echo.initialize` with protocol/host/plugin/digest/feature information. Return an object acknowledging protocol support. Echo then calls declared tool/RPC methods with an invocation ID, scope, resolved configuration (including secrets only at this boundary), deadline semantics from the request context, and permitted workspace metadata. Handle `$/cancelRequest` notifications, `echo.configChanged`, and `echo.shutdown`. Backends may notify `echo.uiEvent` with `{sessionId, topic, data}`. Exit promptly after shutdown or EOF.

Use strict JSON decoding, bound internal allocations, never execute strings through a shell, and return JSON-RPC errors without leaking secrets. Echo bounds protocol output, rotates stderr logs, cancels calls on disable/update, and marks repeatedly crashing runtimes unhealthy until explicit reload.

## Workflow

1. Search/read this skill and clarify the smallest required surfaces (UI-only, tool-only, or hybrid).
2. Call `echo_plugin_scaffold` with a workspace-labeled destination such as `my-workspace/plugins/my-plugin`.
3. Implement with normal file/shell tools. Keep all referenced files inside the package. For native Go backends, build explicit target artifacts; do not rely on install-time builds.
4. Test UI logic, JSON schemas, backend framing/handshake/cancellation, failure cases, and secret redaction. Run the package's target build and tests.
5. Call `echo_plugin_validate`. Fix every validation error and confirm current-platform compatibility, requested permissions, contributions, and digest.
6. Call `echo_plugin_stage`. Report the pending stage ID and tell the owner to review the trusted Echo approval card. Do not try to approve it.
7. After the owner approves, verify navigation/tool visibility and exercise the plugin. Approval may install globally, in the active workspace, or disabled.

For sharing, commit the package to a public GitHub repository, include a license/readme, build all advertised target artifacts in CI, pin dependencies, and publish immutable commit references. Updates are manual staged candidates and always require review when code, digest, permissions, or tools change.
