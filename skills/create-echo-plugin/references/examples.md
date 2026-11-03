# Plugin authoring examples

Use `plugin_template` first to get the running host's contract. These examples show concrete packages and adaptations. JavaScript blocks below assemble **tool arguments**; they are not scripts to execute inside the chat or installation hooks. Serialize the assembled object and pass it to `plugin_install`. Go/HTML source must be supplied as string contents, not filesystem paths. All example IDs can be renamed; update their tool prefixes too.

## 1. Persistent HTML app with navigation

This app uses storage without privileged host capabilities, registers in Apps, and supports a `note` shortcut destination. The same bridge supports approved networking when the manifest is extended as in example 3. It needs no backend or compiler.

```javascript
const html = String.raw`<!doctype html>
<html lang="en"><meta charset="utf-8"><title>Quick Note</title>
<style>body{font:16px system-ui;padding:16px}textarea{box-sizing:border-box;width:100%;min-height:140px}button{font:inherit;margin-top:8px}output{display:block;margin-top:8px}</style>
<h1>Quick Note</h1>
<label for="note">Note for this bot</label><textarea id="note"></textarea>
<button id="save" disabled>Save</button><button id="close" disabled>Close</button>
<output id="status" aria-live="polite">Connecting to Echo...</output>
<script>
let echo;
const pending = new Map();
let readyResolve;
const ready = new Promise(resolve => { readyResolve = resolve; });
const note = document.querySelector('#note');
const status = document.querySelector('#status');
function navigate(feature) { if (feature === 'note') note.focus(); }
async function echoCall(method, params = {}) {
  await ready;
  const id = crypto.randomUUID();
  return new Promise((resolve, reject) => {
    pending.set(id, {resolve, reject});
    parent.postMessage({type:'echo-plugin-request', nonce:echo.nonce,
      pluginId:echo.pluginId, viewId:echo.viewId, id, method, params}, '*');
  });
}
addEventListener('message', event => {
  if (event.source !== parent || !event.data) return;
  const data = event.data;
  if (data.type === 'echo-plugin-init' && !echo) {
    echo = data;
    document.body.style.background = echo.theme.background;
    document.body.style.color = echo.theme.text;
    readyResolve();
    navigate(echo.feature);
    document.querySelector('#close').disabled = false;
    echoCall('storage.get', {key:'note'}).then(value => {
      note.value = typeof value === 'string' ? value : '';
      status.textContent = 'Loaded';
    }).catch(error => {
      // Missing keys return an error; show the actual diagnostic as well.
      status.textContent = 'No saved note loaded: ' + error.message;
    }).finally(() => { document.querySelector('#save').disabled = false; });
    return;
  }
  if (!echo || data.nonce !== echo.nonce) return;
  if (data.type === 'echo-plugin-navigate' &&
      data.pluginId === echo.pluginId && data.viewId === echo.viewId) {
    navigate(data.feature);
  }
  if (data.type === 'echo-plugin-response') {
    const request = pending.get(data.id);
    if (!request) return;
    pending.delete(data.id);
    if (data.error) request.reject(new Error(data.error));
    else request.resolve(data.result);
  }
});
document.querySelector('#save').onclick = async () => {
  try { await echoCall('storage.set', {key:'note', value:note.value}); status.textContent = 'Saved'; }
  catch (error) { status.textContent = error.message; }
};
document.querySelector('#close').onclick = () => {
  parent.postMessage({type:'echo-plugin-request', nonce:echo.nonce,
    pluginId:echo.pluginId, viewId:echo.viewId, id:crypto.randomUUID(),
    method:'window.close', params:{}}, '*');
};
parent.postMessage({type:'echo-plugin-ready'}, '*');
</script></html>`;
const installArgs = {
  id: 'quick-note', name: 'Quick Note',
  manifest: {
    api: 1, id: 'quick-note', name: 'Quick Note', version: '1.0.0', scope: 'bot',
    capabilities: ['storage.get', 'storage.set'], tools: [],
    settings: {type:'object', properties:{}, additionalProperties:false}, config: {},
    views: [{id:'main', title:'Quick Note', kind:'floating', html,
      features:[{id:'note', title:'Edit note'}]}],
    app: {viewId:'main'}
  }
};
```

Pass `installArgs` to `plugin_install`. Change view `kind` to `embedded` for an initial main-area app; users can switch presentation later without losing the live document. For a simple nonpersistent UI, the shorthand is `{"id":"my-app","name":"My App","html":"complete HTML source"}`; it auto-registers `main` in Apps. Use a full manifest for capabilities, network, features, or embedded views.

Smoke check: open the app, save text, close/reopen, and confirm persistence in the same bot context. A different bot intentionally gets a different note. Test the shortcut's `note` destination and floating/embedded switching.

## 2. Go/WASI backend tool

This tool sums an array. It illustrates that nested array/range validation belongs in the backend; it needs no capabilities or owner grant.

```json
{
  "id": "number-tools",
  "name": "Number Tools",
  "manifest": {
    "api": 1,
    "id": "number-tools",
    "name": "Number Tools",
    "version": "1.0.0",
    "scope": "bot",
    "capabilities": [],
    "tools": [{
      "name": "number_tools_sum",
      "description": "Sum a nonempty array of at most 1000 finite numbers.",
      "parameters": {
        "type": "object",
        "properties": {"values": {"type": "array", "items": {"type": "number"}}},
        "required": ["values"],
        "additionalProperties": false
      }
    }],
    "settings": {"type": "object", "properties": {}, "additionalProperties": false},
    "config": {}
  }
}
```

Add the following complete source as the outer `backendSource` string in that request:

```go
package main

import (
    "encoding/json"
    "fmt"
    "io"
    "math"
    "os"
)

func fail(message string) {
    _ = json.NewEncoder(os.Stdout).Encode(map[string]any{"error": message})
}

func main() {
    data, err := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
    if err != nil { fail("cannot read input"); return }
    var input struct {
        Tool string `json:"tool"`
        Params struct { Values []any `json:"values"` } `json:"params"`
    }
    if json.Unmarshal(data, &input) != nil { fail("invalid input"); return }
    if input.Tool != "number_tools_sum" { fail("unknown tool"); return }
    if len(input.Params.Values) == 0 || len(input.Params.Values) > 1000 {
        fail("values must contain 1 to 1000 numbers"); return
    }
    total := 0.0
    for _, value := range input.Params.Values {
        number, ok := value.(float64)
        if !ok || math.IsNaN(number) || math.IsInf(number, 0) {
            fail("every value must be a finite number"); return
        }
        total += number
    }
    if math.IsInf(total, 0) { fail("sum overflow"); return }
    result, err := json.Marshal(map[string]any{"result": map[string]any{"sum": total}})
    if err != nil { fail("cannot encode result"); return }
    fmt.Print(string(result))
}
```

Call `number_tools_sum` with `{"values":[2,3,4]}` and expect `{"sum":9}` as the tool result. `{"values":[]}` and `{"values":[2,"bad"]}` must fail. Emit no extra stdout before/after the result envelope.

For a host-backed tool, start with `plugin_template({"kind":"backend"})`. Its returned source already contains the two-call WASI `call` helper. For example, replace its final encode statement with:

```go
os.Stdout.Write(call("file.read", input.Params))
```

Then declare `capabilities:["file.read"]`, name the tool with your plugin prefix, and give it an object schema requiring string `path`. This forwards the broker's complete envelope; wrapping it again in `{"result":...}` would incorrectly nest the result. A file-reading package needs the owner grant before execution and obeys the invoking bot's folders.

## 3. HTTP and WebSocket app adaptation

Start from `plugin_template({"kind":"ui"})` so `echoCall` and `echoSocket` exist. Add only the capabilities the app needs and actual service origins. For an HTTP JSON API:

```javascript
// Merge these fields into the full manifest; these are example destinations.
const networkFields = {
  capabilities: ['web.request'],
  network: {origins:['https://api.example.com']}
};
// Inside the template's script, after init (echoCall waits for readiness):
async function loadStatus() {
  const response = await echoCall('web.request', {
    url:'https://api.example.com/status', method:'GET',
    headers:{Accept:'application/json'}
  });
  if (response.status < 200 || response.status >= 300) {
    throw new Error('HTTP ' + response.status);
  }
  const text = response.encoding === 'base64'
    ? new TextDecoder().decode(Uint8Array.from(atob(response.content), c => c.charCodeAt(0)))
    : response.content;
  return JSON.parse(text);
}
```

For JSON POST, set `method:'POST'`, `headers:{'Content-Type':'application/json'}`, and `body:JSON.stringify(payload)` with `encoding:'text'`. Put error messages in the app's own UI. Do not put secrets into generated source, manifest config, or samples.

For WebSockets, add `websocket.open`, `websocket.send`, `websocket.receive`, and `websocket.close`, plus the actual `wss://...` origin. Using the bundled helper:

```javascript
async function connectFeed() {
  const socket = await echoSocket('wss://stream.example.com', {protocols:['json']});
  socket.addEventListener('message', event => {
    // event.detail is {data,encoding,binary}; decode before parsing.
    document.querySelector('#result').textContent = event.detail.data;
  });
  socket.addEventListener('error', event => {
    document.querySelector('#result').textContent = event.detail;
  });
  await socket.send(JSON.stringify({subscribe:'status'}));
  return socket; // Await socket.close() when the app is finished with it.
}
```

The example domains identify where your real service goes; these network snippets are not a live service demo. Installation will be pending until the owner approves the new destinations/capabilities. Test both successful requests and a denied undeclared origin. Assets may use ordinary tags such as `<img src="https://api.example.com/logo.png">` with the same approved HTTP(S) origin.

## 4. Model provider

Call `plugin_template` with `{"kind":"provider"}`. Its returned package already defines:

```json
{
  "id": "my-provider",
  "capabilities": ["model.complete"],
  "providers": ["my_provider_complete"]
}
```

This excerpt is not the full manifest. Preserve the returned tools, settings, and other fields. The returned `backendSource` forwards `input.Params` to `model.complete` using `os.Stdout.Write(call("model.complete", input.Params))`. Install with outer `id` and `name` matching that full manifest, plus its full `backendSource`; do not include `html`. After approval, select `my_provider_complete` in the intended model connection settings and exercise a completion. It is hidden from the model's ordinary tool list.

For a different provider protocol, use the same backend helper, declare `model.request` and optionally `model.emit`, and adapt the response into Echo's result shape. A host request looks like:

```go
response := call("model.request", map[string]any{
    "path": "messages",
    "method": "POST",
    "authHeader": "x-api-key",
    "headers": map[string]string{"Accept": "application/json"},
    "body": map[string]any{"messages": input.Params["messages"]},
})
// Decode the result/error envelope, then parse result.body for the provider's
// protocol. This fragment is not a complete model adapter.
_ = response
```

The actual request body and result conversion depend on the chosen provider. Do not assume Echo's message objects are accepted unchanged by every API. The host injects the selected endpoint credential; generated code must not request or embed it. See [capabilities.md](capabilities.md) for all provider result fields, usage, tool-call encoding, streaming, and context overflow handling.

## 5. Chat renderer

This renderer escapes assistant text and adds line breaks. It uses its own protocol, not `echo-plugin-request`.

```javascript
const html = String.raw`<!doctype html><meta charset="utf-8"><script>
let nonce;
const escape = text => String(text).replace(/[&<>"']/g,
  c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
addEventListener('message', event => {
  if (event.source !== parent || !event.data) return;
  const data = event.data;
  if (data.type === 'init' && !nonce) { nonce = data.nonce; return; }
  if (data.type === 'render' && nonce && data.nonce === nonce) {
    parent.postMessage({type:'rendered', nonce, id:data.id,
      html:'<p>' + escape(data.text).replace(/\n/g, '<br>') + '</p>'}, '*');
  }
});
parent.postMessage({type:'renderer-ready'}, '*');
</script>`;
const installArgs = {
  id:'plain-renderer', name:'Plain Renderer',
  manifest:{
    api:1, id:'plain-renderer', name:'Plain Renderer', version:'1.0.0', scope:'bot',
    capabilities:[], tools:[], renderers:['main'],
    views:[{id:'main', title:'Plain Renderer', kind:'renderer', html}],
    settings:{type:'object', properties:{}, additionalProperties:false}, config:{}
  }
};
```

Pass `installArgs` to `plugin_install` without a top-level `html` argument or app registration. Enable it for a test bot and inspect an assistant message containing `<script>` text and multiple lines: text must remain escaped, with line breaks. Echo sanitizes the returned HTML, and the first eligible custom renderer alphabetically takes precedence over built-in Markdown.
