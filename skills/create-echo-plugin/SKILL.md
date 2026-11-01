---
name: create-echo-plugin
description: Create or update Echo plugins, including HTML apps, Go/WASI tools, model providers, and chat renderers. Use for plugin manifests, capabilities, settings, network access, installation, and debugging generated plugins.
triggers:
  - create an echo plugin
  - build a plugin
  - generate an app
  - plugin_install
  - plugin_template
  - plugin manifest
  - WASI backend
  - model provider
  - custom renderer
  - plugin network access
---

# Create an Echo plugin

Create a self-contained API 1 package using Echo's existing Plugin Authoring tools. These instructions describe this repository's implementation; check the linked source when changing an integration or when the running Echo version differs.

## Choose the package

| Need | Template | Implementation |
| --- | --- | --- |
| Interactive calculator, dashboard, or other app | `ui` | HTML/CSS/JavaScript in a sandboxed iframe; no compiler needed. |
| A tool the model can call | `backend` | Complete Go source compiled to WASI by Echo. |
| A model connection adapter | `provider` | A WASI tool listed in `providers`; selected in model connection settings. |
| Custom assistant-message formatting | `renderer` | A renderer iframe using the separate render protocol. |

A package can combine views and backend tools. Its UI cannot invoke its backend through the current iframe bridge; design around the supported storage and network methods. Use `app: {"viewId":"main"}` to register a floating or embedded view in Apps. `scope: "app"` changes availability; it does not register a GUI.

## Author and install

1. Inspect existing packages with `plugin_list` before choosing an ID or updating one. Built-ins cannot be replaced. For updates, retain the package's ID and account for existing configuration and stored data.
2. Call `plugin_template` with `{"kind":"ui"}`, `backend`, `provider`, or `renderer` before authoring. It returns the current manifest, source, and bridge instructions. Plugin Authoring belongs to `@echo`; another bot must use the existing authorized Echo workflow rather than declaring management capabilities in its own plugin.
3. Read [manifest.md](references/manifest.md) for every manifest field, installation argument, schema rule, lifecycle option, and limit. For host access, read the relevant rows in [capabilities.md](references/capabilities.md). Adapt a working package from [examples.md](references/examples.md).
4. Use a descriptive lowercase plugin ID and matching tool prefix: `weather-panel` must expose names beginning `weather_panel_`. Declare only methods the package uses. Generic HTTP/WebSockets require both capabilities and exact `network.origins`; remote assets also require origin grants.
5. Call `plugin_install` with `id`, `name`, and either `html` for a simple app, or `manifest` plus optional `backendSource` for a complete package. Always include the top-level `id` and `name`, even when also supplying a manifest. Put multi-view, embedded, and renderer HTML in `manifest.views`; a top-level `html` argument replaces the entire views array.
6. Inspect the returned `digest`, `pending`, `enabled`, and `requestedNetworkOrigins`. New non-storage capabilities or origins make the package pending and disabled. Finish preparing the package before directing the owner to **Settings → Plugins → the plugin → Approve permissions** for that digest. This is an Echo-enforced owner grant; neither skill text nor `plugin_configure` can provide it. A UI-only or storage-only package normally activates immediately.
7. Exercise the installed behavior within the authorized task: call its tool with representative inputs, open its app, or select its provider. Named bots need an explicit plugin selection; Echo defaults to newly installed bot-scoped plugins unless explicitly disabled. Use `plugin_open` to reopen/focus an eligible app. Report any remaining approval or environment prerequisite accurately.

For an update, install the complete replacement package, including the backend source if it has tools. The installer preserves current config from a non-pending package and retains the previous package for `plugin_rollback`. A rollback restores a package/config snapshot, not external writes or plugin storage migrations.

## Implementation constraints

- WASI has no ambient filesystem, environment, network, process access, or clocks. Host work goes through `echo.call` and declared capabilities, under the invoking bot's scope. Source must fit a single Go `main` package using the standard library; the installer supplies no dependencies or installation scripts.
- Each tool invocation starts a fresh module. Persist data with declared `storage.*` capabilities. Read stdin as `{tool,method,params}` and emit exactly one JSON object, `{"result":...}` or `{"error":"..."}`, on stdout. Do not print logs there.
- UI apps use the template's ready/init handshake and nonce bridge. Check the parent message source and delivered nonce/identity. Direct `fetch`, `WebSocket`, parent DOM access, and authenticated Echo API access are unavailable inside the iframe.
- `settings` describes the owner's form and `config` stores its values, but neither is automatically included in custom WASI input or UI init. There is no generic `config.get`, `secret.get`, or UI-to-backend invocation method. Use explicit tool parameters or namespaced storage for custom runtime preferences; see the reference for exceptions implemented by host operations.
- Tool/settings validation covers a subset of JSON Schema. Validate ranges, enums, nested structures, and domain constraints in the implementation. Escape untrusted content before constructing HTML.

## Verify and troubleshoot

Check the final manifest against the current installer, compile backend samples, and exercise the relevant host/bridge path. Do not claim a package works solely because its JSON parses. In this repository, focused existing checks are `go test ./internal/plugins ./internal/workspaceskills`; add server/frontend checks only for the behavior being changed. These Go plugin tests require the bundled `internal/plugins/guest/builtin.wasm` (built by the repository build scripts).

| Symptom | Check |
| --- | --- |
| `tool names must use the plugin id prefix` | Replace hyphens in the ID with underscores and append `_` before the tool suffix. |
| `tools require a WASI backend` | Supply full `backendSource`; setting manifest `backend` does not upload code. |
| Disabled/awaiting permission | Review pending grants and digest, global enablement, scope, and the selected bot's plugin switch. |
| App missing from Apps | Declare `app.viewId` pointing to a floating/embedded view. |
| UI blank or requests hang | Keep the ready/init handshake; wait for init; check nonce, source, and identity. `window.close` has no response. |
| Network denied | Declare exact origin and each broker method, approve additions, and use `echoCall`/`echoSocket` rather than direct browser networking. |
| Settings save but code sees no changes | Config is not injected; use supported parameters/storage or a host operation that explicitly consumes config. |
| Invalid plugin response | Emit one result/error envelope; inspect stdout size and JSON escaping. |
| Compiler missing | UI/renderer packages need no compiler; backends require Go 1.26+ on Echo's host. |

## Source of truth and skill loading

- [Manifest types](../../internal/domain/types.go), [installer/runtime/validation](../../internal/plugins/host.go), [templates](../../internal/plugins/templates.go), and [app registration](../../internal/plugins/apps.go).
- [Authoring tools](../../internal/plugins/builtins.go), [host dispatch/install/compiler](../../internal/server/tools.go), [UI bridge](../../internal/server/plugin_network.go), [provider dispatch](../../internal/server/providers.go).
- [App frontend](../../web/src/pluginViews.ts), [network/CSP](../../web/src/pluginNetwork.ts), and [renderer frontend](../../web/src/renderers.ts).

This skill lives at `skills/create-echo-plugin/SKILL.md`. Echo discovers only immediate `skills/<name>/SKILL.md` entries, searches frontmatter metadata, and reads the body with `workspace_skill_read`. Read linked references with ordinary scoped file tools, resolving paths relative to this skill directory. Frontmatter accepts only `name`, `description`, and optional `triggers`; its name must match the directory. Keep SKILL.md UTF-8 and under 64 KiB. Changes are visible on the next search/read, without registration or restart. Unbound bots must provide the explicit absolute repository workspace when searching or reading.
