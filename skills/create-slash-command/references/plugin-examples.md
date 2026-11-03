# Generate a plugin for a slash command

Read this when an existing plugin cannot supply the requested tool or app. Slash-command options and registration are in [SKILL.md](../SKILL.md). The examples below create destinations; registering the corresponding slash command is a separate step.

## Authoring tools and installation

Echo's **Plugin Authoring** plugin provides `plugin_template`, `plugin_list`, `plugin_install`, `plugin_configure`, `plugin_open`, and `plugin_rollback`. Plugin management belongs to the Echo bot with Plugin Authoring enabled. A named bot should use the existing Echo handoff workflow when it needs installation rather than assuming it has these tools.

Call `plugin_template` with `{"kind":"backend"}` or `{"kind":"ui"}` before authoring, and use its returned source and current SDK contract. The other template kinds, `provider` and `renderer`, are not standalone slash-command destinations.

`plugin_install` requires `id` and `name`; its optional inputs are `manifest` (complete API 1 manifest object), `backendSource` (complete Go/WASI source), and `html` (a convenience single floating view). With a full manifest, keep its ID/name consistent with the outer inputs. A nonempty top-level `html` replaces `manifest.views` with one `main` view, losing declared features and other views. For a custom app manifest, put the actual HTML in `views[].html` and omit top-level `html`.

New capabilities or network origins leave a package pending direct owner approval in plugin settings. Use the returned package digest for that approval; do not attempt to self-approve through a model tool. Pure computation/UI examples below request no capabilities. Enable the installed plugin for the intended bot where needed; named bots do not automatically opt into newly installed plugins. `plugin_configure` controls package enabled state/non-secret configuration; bot plugin selection is separate.

## Standalone backend example

Call `plugin_template({"kind":"backend"})`. Its default backend returns the supplied `params` as `{"result":...}`, which is sufficient for this sample. Use that exact `backendSource` with the following `manifest`, and outer install arguments `id: "command-demo"`, `name: "Command Demo"`:

```json
{
  "api": 1,
  "id": "command-demo",
  "name": "Command Demo",
  "version": "1.0.0",
  "scope": "bot",
  "capabilities": [],
  "tools": [
    {
      "name": "command_demo_echo",
      "description": "Return supplied text",
      "parameters": {
        "type": "object",
        "properties": {"text": {"type": "string"}},
        "required": ["text"],
        "additionalProperties": false
      }
    }
  ],
  "settings": {"type": "object", "properties": {}},
  "config": {}
}
```

After installing, create this command on the same machine:

```json
{
  "name": "demo",
  "description": "Run the sample standalone plugin",
  "action": {
    "type": "plugin",
    "pluginId": "command-demo",
    "tool": "command_demo_echo",
    "arguments": {"text": "Hello from a slash command"}
  }
}
```

The returned tool result is `{"text":"Hello from a slash command"}`. For real functionality, replace the template's computation and dispatch on `input.Tool` when exporting multiple tools. Backend input is `{"tool":"command_demo_echo","params":{...}}`; emit exactly one JSON object containing `result` or `error` on stdout. Keep diagnostics out of stdout.

Custom tools require a WASI backend, even when they declare a host capability. Tool names must start with the plugin ID converted from hyphens to underscores plus `_`, and be unique within the plugin. Each tool's `parameters` must be an object schema; if `capability` is set, it must also occur in the manifest's `capabilities`. Keep argument schemas accurate and validate business rules in the backend: Echo's validator checks required properties, basic types, string-array items, and `additionalProperties: false`, not every JSON Schema keyword or nested constraint.

`backendSource` compilation requires installed Go 1.26 or newer; the installer builds `GOOS=wasip1`, `GOARCH=wasm`, `CGO_ENABLED=0` without third-party dependencies or install scripts. The backend has no ambient host filesystem, network, environment, process, or clock access. Use the template's `call(method, params)` SDK for declared capabilities. Execution limits are 64 MiB memory, 2 MiB output, and a five-minute deadline; cancellation terminates execution. Model/media/chat-app-event capabilities remain blocked in standalone mode.

## App with a feature destination

Call `plugin_template({"kind":"ui"})`. Starting from its returned manifest and HTML, make the following changes before calling `plugin_install`:

1. Set manifest `id` to `command-app`, `name` to `Command App`, `app` to `{"viewId":"main"}`, and `capabilities` to `[]` for this local UI example. Keep the template's other API/version/scope/settings/config fields.
2. Keep view `id: "main"`, choose `kind: "floating"` or `"embedded"`, and set its `features` to `[{"id":"history","title":"History"}]`.
3. In the template HTML, replace the calculator markup and its operation click handler with the sample below. Keep the template's ready/init bridge and message handlers. Place this navigation listener before the final `parent.postMessage({type:'echo-plugin-ready'}, '*')` announcement.
4. Set `manifest.views[0].html` to the resulting complete HTML. Call `plugin_install` with `id`, `name`, and this `manifest`, omitting top-level `html` so the feature declaration survives installation.

```html
<section id="home"><h2>Command App</h2><p>Default view.</p></section>
<section id="history" hidden><h2>History</h2><p>No entries yet.</p></section>
<script>
document.addEventListener('echo-navigate', event => {
  const history = event.detail === 'history';
  document.querySelector('#home').hidden = history;
  document.querySelector('#history').hidden = !history;
});
</script>
```

Create this command after installation:

```json
{
  "name": "history",
  "description": "Open Command App history",
  "action": {
    "type": "app",
    "pluginId": "command-app",
    "viewId": "main",
    "feature": "history"
  }
}
```

An empty/omitted feature opens the default view. Features are navigation identifiers, not separately executable tools. The template dispatches `echo-navigate` on initial `echo-plugin-init` and subsequent `echo-plugin-navigate` messages. Preserve its `event.source === parent` check and the nonce/pluginId/viewId checks on subsequent navigation. Handle both startup and later navigation so an already-open app can switch features without a reload.

App registration requires `app.viewId` to identify a declared floating/embedded view. View IDs must be unique lowercase kebab-case; omitted view kind defaults to `floating`. Features need unique lowercase kebab-case IDs within their view and nonblank titles of at most 120 UTF-8 bytes; renderer views cannot declare them. The optional `app.icon` is an SVG, PNG, JPEG, or WebP image data URL, at most 256 KiB. These are plugin manifest fields, not extra slash-command options.

UI-only apps need no compiler. Echo owns presentation, drag/resize, and theme delivery; the sandboxed iframe cannot read the parent DOM or cookies. For network behavior use the template bridge (`echoCall` / `echoSocket`), declare the required broker capabilities and exact HTTP(S)/WS(S) origins in `network.origins`, and obtain the package's owner approval. Direct frame `fetch`/`WebSocket` calls are blocked. API/socket requests run on the plugin's Echo machine; allowed remote assets load from the viewing browser.

## Check the generated destination

Read `plugin_list` and the slash-command catalog after installation. Confirm the intended tool schema or app view/features appears, the current package is available to the target bot, and the command uses those exact IDs. Use a non-mutating sample invocation for the backend example. For the app, check default launch, direct launch into History, and navigation to History while the app is already open. Registering a command never substitutes for declaring or implementing its tool/feature.
