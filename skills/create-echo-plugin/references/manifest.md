# Plugin manifest and lifecycle reference

API 1, implemented by `internal/domain/types.go`, `internal/plugins/host.go`, `internal/plugins/apps.go`, and `internal/server/tools.go`. Paths in this reference are repository-relative. Contents: authoring calls, manifest fields, schemas/settings, activation/update behavior, and limits.

## Authoring tool arguments

These tools are exposed by the built-in `plugin-authoring` plugin to Echo. Optional arguments are marked `?`. They are model tool names, not methods available to generated plugins.

| Tool | Arguments | Result/behavior |
| --- | --- | --- |
| `plugin_template` | `kind`: `ui`, `backend`, `provider`, `renderer` | `manifest`, `instructions`, and applicable `html` or `backendSource`. |
| `plugin_list` | `{}` | Saved manifests, including current config and activation state. |
| `plugin_install` | `id`, `name`, `html?`, `manifest?`, `backendSource?` | Installed manifest with digest and approval state. `id` and `name` are required by the tool schema. |
| `plugin_configure` | `id`, `enabled?`, `config?` | Changes global enablement or replaces the config object after validation. Cannot approve a pending package or expand protected built-in settings. |
| `plugin_open` | `id`, `viewId?` | Emits a chat app-open event. Defaults to registered app view, otherwise first floating/embedded view. Renderer views cannot open this way. |
| `plugin_rollback` | `id` | Restores the previous saved package; swaps current and previous snapshots. |

`plugin_install` starts with API 1, version `1.0.0`, scope `bot`, empty config, and an empty object settings schema. A supplied manifest overlays that base, including its ID and name; keep them consistent with the required outer arguments. A nonempty top-level `html` replaces all views with one `main` floating view. It auto-registers `app.viewId: main` only when no `manifest` argument was supplied. For a full manifest, omit `html` and populate `views[].html` directly.

`backendSource` is a complete Go source string, not a path, base64 module, or shell command. Echo writes a temporary `main.go` and Go 1.26 module, then builds `GOOS=wasip1 GOARCH=wasm CGO_ENABLED=0 go build -trimpath -o backend.wasm .` with a two-minute deadline. There is no package download/install-script step and no raw-WASM upload argument in this model tool.

## Every manifest field

| Field | Type / default | Meaning |
| --- | --- | --- |
| `api` | integer, use `1` | Only API 1 is accepted. |
| `id` | string, required | Unique plugin identity. Installer regex: `^[a-z][a-z0-9-]{0,63}$`; prefer ordinary kebab-case. Built-in IDs cannot be replaced. |
| `name` | nonblank string, required | Settings and Apps display name. |
| `version` | string, installer base `1.0.0` | Package version label; not an automatic update mechanism. Use semantic versions by convention. |
| `scope` | `bot` (default), `echo`, `app` | `bot`: per-bot availability. `echo`: additionally restricts execution to Echo. `app`: global availability independent of per-bot selections; still has a bot execution/storage context. |
| `capabilities` | string array | Exact host methods used, e.g. `storage.set`, `web.request`. Declaring a string does not implement it or bypass built-in/provider restrictions. |
| `network` | optional object | Only supported member is `origins`, an array of exact HTTP(S)/WS(S) origins. See capabilities reference. |
| `tools` | array of tool objects | Model-callable backend tools, excluding any named in `providers`. Nonempty tools require a WASI backend on every install. |
| `views` | optional array of view objects | Floating/embedded apps or renderer HTML documents. |
| `app` | optional object | Registers one primary GUI in Apps: `viewId` (required), `icon` (optional). Does not grant capabilities. |
| `renderers` | optional string array | Each entry must name a declared `kind: renderer` view. |
| `providers` | optional string array | Each entry must name a declared backend tool. Hidden from the model's normal tool list and offered as a model provider. |
| `settings` | object schema, empty schema by default | Schema for the owner settings form and config validation. |
| `config` | object, `{}` by default | Non-secret settings values. Existing non-pending config is preserved on update. Not injected into guest/iframe code. |
| `builtin` | host-owned boolean | Always reset to `false` for custom installs. Cannot opt into native privileges. |
| `enabled` | host-owned boolean at install | Recomputed: true unless new grants make it pending. Subsequently configurable. Supplying false at install does not keep it disabled. |
| `pending` | host-owned boolean | Recomputed from capability/origin additions; blocks execution until owner approval. |
| `requestedNetworkOrigins` | host-owned string array | Newly requested destinations for the approval UI; cleared on approval. |
| `digest` | host-owned string | Computed package hash; identifies the reviewed executable package. Do not set it to request trust or choose a version. |
| `backend` | host-owned string | Set to `backend.wasm` when compiled bytes exist; otherwise empty. Not a source-file selector. |

Each tool has exactly these options:

| Field | Meaning |
| --- | --- |
| `name` | Unique within package; must start with `id.replaceAll('-', '_') + '_'`. |
| `description` | Explanation shown to the model; describe when to use it and what it returns. |
| `parameters` | JSON object schema; its `type` must be `object`. |
| `capability` | Optional default method passed as stdin `method`. If nonempty, must also occur in manifest capabilities. It does not automatically execute that host method in custom code; the backend must call the broker. |

Each view has exactly these options:

| Field | Meaning |
| --- | --- |
| `id` | Unique within package, same ID regex as plugin IDs. |
| `title` | Window/view label. |
| `html` | Complete HTML/CSS/JavaScript source string, up to 2 MiB per view. |
| `kind` | `floating` (default if omitted/empty), `embedded`, or `renderer`. |
| `features` | Optional array of `{id,title}` navigation destinations for app shortcuts. IDs use the same regex and must be unique per view; titles must be nonblank and at most 120 bytes. Renderer views cannot have features. |

`app.viewId` must name a floating/embedded view. Optional `app.icon` is a nonempty SVG, PNG, JPEG, or WebP `data:image/...` URL, at most 256 KiB including the header. URL-encoded payloads and base64 are accepted; `charset=utf-8` is optional. External icon URLs, GIFs, and renderer targets are unsupported. No icon means an initials fallback.

## Schema and settings behavior

Use `{"type":"object","properties":{...},"required":[...],"additionalProperties":false}` for tools and settings. Property types recognized by host validation are `string`, `number`, `integer`, `boolean`, `array`, and `object`. It checks required top-level keys, unknown keys when forbidden, top-level types, integer integrality, and string-array item types. It does **not** fully implement JSON Schema: nested requirements, enums, ranges, patterns, formats, lengths, unions, and other array item schemas need implementation-level validation. Adding `default` does not populate config or arguments.

The generic settings form uses each property's `description` as a label (falling back to its key): boolean → checkbox; array → newline-separated strings; integer → number input; others → text input. Number values are converted numerically on save. There is no generic nested-object editor, enum selector, secret field, or conditional form. Use explicit `config` values for initial defaults. Do not put secrets in config; custom plugins have no generic credential-vault API.

```json
{
  "settings": {
    "type": "object",
    "properties": {
      "url": {"type": "string", "description": "SearXNG base URL"}
    },
    "required": ["url"],
    "additionalProperties": false
  },
  "config": {"url": "https://search.example.com"}
}
```

This is a **manifest fragment**, not a complete package. `web.search` explicitly reads `manifest.config.url`. `calendar.save` similarly uses `config.timezone` as a fallback. Arbitrary keys have no automatic runtime consumer. UI init contains identity/theme/feature, and WASI stdin contains tool/method/params; neither includes config. Use tool arguments or `storage.get/set` for ordinary custom runtime preferences shared between that plugin's UI and backend in the same bot context.

## Availability, approval, and updates

- Global `enabled`, `pending`, scope, and per-bot selection all gate execution. App scope bypasses only the per-bot switch. Echo treats an absent bot selection as enabled; named bots treat it as disabled. New plugins are not automatically granted to existing named bots.
- New `storage.get/set/delete` capabilities do not need a privileged grant. Other newly declared capabilities and all newly added network origins do. Pending installs remain disabled. Reinstalling a pending package does not turn its unapproved capabilities into grants.
- Owner approval uses `POST /api/v1/plugins/{id}/approve` with `{"digest":"the reviewed digest"}`; stale digests are rejected. Use the existing Settings approval UI during normal authoring. There is no `plugin_approve` model tool.
- A package with unchanged approved grants can activate immediately on update. Installation computes activation afresh, so it can enable a formerly disabled package. Config is preserved from the current non-pending package; explicitly migrate new settings rather than assuming changed defaults replace saved values.
- The digest covers the normalized install manifest and backend bytes. Echo stores immutable `manifest.json` and optional `backend.wasm` in its profile package store. Source files in the repository are authoring inputs, not a watched plugin installation directory.
- Saving/disabling a plugin cancels its active invocations; bot revocation and run cancellation also terminate relevant work. Digest/availability changes invalidate live app instances. Rollback restores the previous package snapshot; it does not undo file, network, process, or storage effects.
- Apps launch with Echo's bot context from Apps/dock. Chat launches retain their bot context. Floating and embedded presentations can switch without reloading. Bot-scoped app instances are isolated per plugin/view/bot; app-scoped instances are shared per plugin/view in a browser.
- Owner-configured slash commands can invoke an exact plugin/tool pair or open a declared app feature. There is no manifest `commands` field. Standalone tool invocations cannot use `model.*`, `comfyui.*`, `plugin.open`, or `plugin.install`; providers require a model run. Feature navigation uses `echo-plugin-init.feature` and `echo-plugin-navigate`.

## Limits to design around

| Resource | Current limit |
| --- | --- |
| Backend compiled module | 32 MiB at install |
| Backend memory / invocation | 64 MiB / five-minute deadline |
| Backend stdout / host response envelope | 2 MiB each |
| Host call input / provider tool input | 4 MiB each |
| View HTML / app icon data URL | 2 MiB / 256 KiB |
| Storage key / JSON value / namespace | 128 bytes / 1 MiB / 8 MiB |
| HTTP or WebSocket payload / network wire envelope | 1 MiB / 2 MiB |
| Network origin list | 128 entries, 2,048 bytes per input origin |
| HTTP timeout / WebSocket handshake / WebSocket send | 30 seconds / 30 seconds / 10 seconds |
| WebSocket session / incoming socket queue | 16 sockets / 4 MiB or 256 messages per socket |
| UI network bridge | 32 concurrent calls, 4 MiB outstanding request/response queues |
| Provider request / response / emitted stream | 4 MiB / 2 MiB / 8 MiB; broker envelope and final stdout caps still apply |

The storage quota currently sums the incoming value and all existing namespace records before writing, including the old value of a replaced key. Leave headroom for updates. Backend module state and WebSocket handles do not survive an invocation.
