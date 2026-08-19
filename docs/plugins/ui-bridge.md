# Sandboxed UI bridge: `echo-ui-bridge-1`

Plugin HTML runs in an opaque-origin iframe. It cannot import Echo modules or call authenticated Echo APIs directly. All privileged interaction crosses a message bridge controlled by the host.

## Handshake and identity

On load, the iframe posts:

```js
parent.postMessage({
  type: "echo-plugin-ready",
  protocol: "echo-ui-bridge-1"
}, "*");
```

The host replies with `echo-plugin-init`, containing a short-lived nonce, plugin/view/session identity, active workspace ID, non-secret configuration, and Echo theme tokens. Accept it only when `event.source === parent`. Every request must repeat the exact nonce, plugin ID, and view ID:

```js
parent.postMessage({
  type: "echo-plugin-request",
  nonce,
  pluginId,
  viewId,
  id: "unique-request-id",
  method: "storage.get",
  params: { scope: "workspace", key: "layout" }
}, "*");
```

The host answers with `echo-plugin-response`. Backend events arrive as `echo-plugin-event` with a topic and data. Theme changes arrive as `echo-plugin-theme` with the same nonce/plugin/view identity and a fresh `theme` map. Sessions expire, slide while active, and become invalid immediately when a plugin is disabled, updated, or uninstalled.

## Allowed operations

| Method | Purpose |
| --- | --- |
| `rpc.invoke` | Call a method declared in `contributes.rpc`. |
| `storage.get`, `storage.set`, `storage.delete` | Use quota-limited global/workspace JSON storage under the plugin namespace. |
| `window.close`, `window.setTitle` | Control the host-owned page/window shell. |
| `notification.show` | Show a host notice with approved `ui.notifications`. |
| `clipboard.write` | Write text with approved `ui.clipboard-write`. |
| `external.open` | Ask to open an HTTP(S) URL with approved `ui.external-links` and confirmation. |

Unknown bridge methods and undeclared backend RPC methods fail before process dispatch. Secret values are excluded from initialization context.

Theme keys include `--echo-background`, `--echo-surface`, `--echo-surface-raised`, `--echo-border`, `--echo-text`, `--echo-text-muted`, `--echo-accent`, `--echo-accent-contrast`, and `--echo-danger`. Copy them to the iframe document root and provide fallbacks in CSS.

Floating placement, drag, resize, focus, keyboard movement/resizing, viewport recovery, and browser-local layout persistence belong to Echo. Plugin code should render responsively inside the allocated viewport.
