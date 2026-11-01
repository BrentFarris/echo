# Host capabilities and protocols

Source: `internal/server/tools.go`, `internal/server/plugin_network.go`, `internal/server/providers.go`, `internal/model/request.go`, `internal/broker/network_*.go`, and `internal/plugins/templates/*`. This lists the current host dispatch surface; a capability string is not a general RPC endpoint. Declare each method actually called in `manifest.capabilities`.

## Custom WASI methods

The backend import is `echo.call(inputPointer, inputLength, outputPointer, outputCapacity) -> uint32`. With capacity zero, submit UTF-8 JSON `{"method":"...","params":{...}}`; the return value is response byte length. Then call with null input and a suitably sized output buffer to copy the response. Read/copy that response before the next request. Responses are `{"result":...}` or `{"error":"..."}`; unwrap once, or forward the complete envelope to stdout. Use the helper from `plugin_template(kind=backend)`.

Optional parameters are marked `?`. General file/process operations retain bot scope and host restrictions; app scope does not remove those restrictions.

| Method | Parameters | Result/behavior |
| --- | --- | --- |
| `file.read` | `path` | `{path,content,revision}`; text up to 1 MiB. |
| `file.write` | `path`, `content`, `revision?` | Atomic replacement; supplied revision must match the previous read. Omitted revision is an unconditional write, not create-only. Up to 1 MiB. |
| `file.list` | `path` | Scoped directory listing. |
| `file.search` | `path`, `query`, `contents?` | Search names or, when `contents:true`, file contents. |
| `file.delete` | `path` | Removes a file or empty directory; cannot remove a workspace root. |
| `file.move` | `from`, `to` | Moves within one allowed root; cannot move a root itself. |
| `file.mkdir` | `path` | Creates scoped directories. |
| `storage.get` | `key` | Stored JSON value; a missing key is an error, not automatically null. |
| `storage.set` | `key`, `value` | Stores any JSON value. |
| `storage.delete` | `key` | Deletes the key. |
| `web.get` | `url` | GET using this custom plugin's approved origins; same response shape as `web.request`. |
| `web.request` | `url`, `method?`, `headers?`, `body?`, `encoding?` | HTTP request; detailed contract below. |
| `websocket.open` | `url`, `headers?`, `protocols?` | `{handle,protocol}` in the current network session. |
| `websocket.send` | `handle`, `data`, `encoding?`, `binary?` | `{sent:true}`. |
| `websocket.receive` | `handle` | Waits for `{data,encoding,binary}`; one pending receive per socket. |
| `websocket.close` | `handle` | `{closed:true}`; handle becomes invalid. |
| `process.run` | `command`, `directory?`, `timeoutSeconds?` | Native shell command (PowerShell on Windows, POSIX shell elsewhere), using the Shell plugin's toolchain grants and bot restrictions. Timeout defaults to 30 seconds and caps at 300. Returns `output`, `exitCode`, `truncated`, and optional `stdout`/`stderr`; check the exit code. No separate `args`, `env`, or stdin parameter. |
| `web.search` | `query` | SearXNG search at this manifest's `config.url`; host appends `/search?q=...&format=json`. Dedicated configured-URL operation, not a generic-network alias. |
| `workspace.bind` | `botId`, `roots` | Only Echo can change bot folders; active-target management checks also apply. Empty roots removes folder restrictions. Use only for requested workspace management. |
| `calendar.list` | `{}` | This bot's schedules; Echo can see all. |
| `calendar.save` | `prompt`, `recurrence`, `id?`, `botId?`, `timezone?`, `paused?` | Create/update schedule. Only Echo can choose another `botId`. Omitted timezone falls back to this plugin's `config.timezone`. |
| `calendar.delete` | `id` | Deletes an owned schedule; Echo can delete any. |
| `calendar.missed` | `{}` | Missed jobs visible to the bot and the owner's selection instruction. |

Storage namespace is `plugin-data:<pluginId>:<botId>` for both UI and WASI. `scope: app` does not make storage global across bots. Calendar recurrence accepts `daily HH:MM`, `weekly MON HH:MM`, `every N minutes/hours/days`, or an RFC3339 instant; timezone is an IANA name. See `internal/calendar/calendar.go` for parsing details. File writes/moves/deletes participate in existing source-control/file-change coordination, so results can also contain registration metadata.

## HTTP and WebSockets

Declare origins such as `https://api.example.com`, `http://127.0.0.1:8188`, or `wss://stream.example.com`. Origins are scheme + exact ASCII hostname/IP + optional port. No credentials, wildcards, non-root paths, queries, or fragments. A trailing slash is normalized away; default ports are normalized. Use punycode for international hostnames and brackets for IPv6. HTTP(S) and WS(S) are separate grants. An approved origin covers every path, including private addresses it resolves to.

```json
{
  "capabilities": ["web.request", "websocket.open", "websocket.send", "websocket.receive", "websocket.close"],
  "network": {"origins": ["https://api.example.com", "wss://stream.example.com"]}
}
```

This is a manifest fragment. `web.request` supports `GET` (default), `HEAD`, `POST`, `PUT`, `PATCH`, `DELETE`, and `OPTIONS`. Headers are string-to-string. `body` is a string, encoded as `text` (default) or `base64`; serialize JSON yourself and set `Content-Type: application/json`. Returns `{url,status,headers,contentType,content,encoding}`; response headers contain arrays of strings. Check `status` explicitly: HTTP error statuses are returned, not automatically thrown. Decode `content` using its returned encoding before parsing JSON.

Requests/responses are limited to 1 MiB of payload and 32 KiB of headers. Reserved request headers: `Cookie`, `Cookie2`, `Host`, `Connection`, `Upgrade`, `Content-Length`, `Transfer-Encoding`, `Proxy-Authorization`, `Proxy-Connection`, `Origin`, `Referer`, `Trailer`, `Te`, and any `Sec-` or `Proxy-` prefix. The host sets `User-Agent: Echo/1.0`; response `Set-Cookie`/`Set-Cookie2` are removed. Generic networking provides no ambient owner cookies or vault credential injection. Every redirect must remain at an approved origin; crossing origins drops all caller-supplied headers. Environment proxies are not used.

WebSocket `protocols` accepts up to 16 nonempty subprotocol strings of at most 128 bytes. `encoding` is text/base64; set `binary:true` for binary frames. Incoming binary messages use base64. Serialize sends and keep one receive pending; concurrent sends are rejected and close the socket. Handshake redirects are not followed. Handles belong to one WASI invocation or one app relay session and close on session termination/revocation. Use `echoSocket` from the UI template for event-based consumption.

HTTP/WebSocket broker connections originate from the plugin's Echo machine. HTTP(S) origin grants also permit browser-loaded scripts, styles, images, fonts, and media from those origins; these assets originate from the viewing device and remain subject to browser restrictions. Remote scripts run with the plugin's bridge permissions. A static remote image needs an origin grant but no broker-method capability. Custom renderer iframes do not inherit these app asset grants.

## UI bridge and navigation

App frames have `sandbox="allow-scripts"` and opaque origins. CSP permits inline script/style and declared HTTP(S) assets, but blocks direct connections, frames, workers, objects, and form actions. Data URLs are permitted for images. Do not depend on parent DOM, host cookies, localStorage, or a same-origin Echo API.

1. Register the message listener, then send `parent.postMessage({type:'echo-plugin-ready'}, '*')`.
2. Accept `echo-plugin-init` only from `parent`. Save `{nonce,pluginId,viewId,botId,feature,theme}`. Theme has `background` and `text`. Preserve the delivered IDs, including remote identities.
3. Send `{type:'echo-plugin-request',nonce,pluginId,viewId,id,method,params}` to parent. `id` is a unique request ID.
4. Accept `{type:'echo-plugin-response',nonce,id,result}` or `{...,error}` only from parent with the saved nonce; resolve/reject the matching pending request. Responses do not include pluginId/viewId.
5. Accept later `{type:'echo-plugin-navigate',nonce,pluginId,viewId,feature}` only with matching source, nonce, and identities. Show the feature without reinitializing the app. The bundled template dispatches `echo-navigate` DOM events for init/navigation.

Available UI methods are **only** `storage.get/set/delete`, `web.get`, `web.request`, `websocket.open/send/receive/close`, and `window.close`. All except `window.close` require declared capabilities. `window.close` is handled by the parent and produces no response; send it without awaiting `echoCall`. UI cannot call file, process, calendar, model, plugin management, or arbitrary backend tools, even if those capabilities are declared. The trusted parent manages digest checks and authenticated relay connections.

## Model provider methods

Only a tool listed in `providers`, invoked for the currently selected model endpoint, can use these methods. They do not grant an ordinary backend tool access to a model. Model endpoint credentials stay in the host; there is no way to select another endpoint/credential in these calls. These methods use the selected endpoint's policy rather than generic `network.origins`.

| Method | Parameters | Result |
| --- | --- | --- |
| `model.complete` | `messages`, `tools` (provider template forwards all input params) | Compatible streaming client completion, including usage/context rejection; selected endpoint and generation are host-owned. |
| `model.request` | `path`, `method?`, `body?`, `headers?`, `authHeader?` | `{status,contentType,body}` with body as text. Non-2xx is a broker error. |
| `model.emit` | `content?`, `reasoning?` | Emits incremental text and returns true. Full final text must still be returned. |

Provider tool input params are `{messages,tools,generation}`. Messages use `role`, `content`, optional `tool_calls`, and `tool_call_id`; see `internal/model/client.go`. Generation has `endpointId`, `model`, `thinking`, and `temperature`. `model.request.path` is relative to the selected endpoint's base path and cannot escape it. Method is `POST` by default, or `GET`; body is a JSON value, not the text-only generic HTTP body. Headers cannot override Authorization, Cookie, Host, Proxy-Authorization, x-api-key, or api-key. `authHeader` selects `Authorization` (default Bearer), `x-api-key`, or `api-key`; Echo injects the saved credential. Request deadline is 120 seconds, within the invocation deadline.

Return a single envelope like:

```json
{
  "result": {
    "content": "The final answer",
    "reasoning": "",
    "toolCalls": [],
    "finishReason": "stop",
    "usage": {"inputTokens": 100, "outputTokens": 20, "totalTokens": 120}
  }
}
```

Tool calls use `{id,type:"function",function:{name,arguments}}`, with `arguments` a JSON **string**. IDs must be unique (blank IDs are generated); names and valid arguments are required. Usage is optional and describes this request, not a cumulative total. The current `domain.TokenUsage` also supports optional `cachedInputTokens`, `inputMissing`, and `outputMissing`; omit unknown usage rather than fabricating counts. Genuine context rejection may return `result.overflow:{message,limitTokens?,inputTokens?,outputTokens?}`. Do not classify rate limits or arbitrary payload errors as context overflow.

## Renderer protocol

Renderer views have no host bridge or ambient networking, even if the manifest declares capabilities/origins. Send `{type:'renderer-ready'}` to parent; accept `{type:'init',nonce,theme}` from parent. On `{type:'render',nonce,id,text}`, return `{type:'rendered',nonce,id,html}` to parent with the same nonce and ID. Escape untrusted text before constructing output; Echo sanitizes every response. Responses above 2 MiB are rejected.

An enabled custom renderer formats assistant messages for eligible bots and takes precedence over built-in Markdown. The first eligible custom plugin ID alphabetically wins (then renderer ID). This is not a floating app and is not opened using `plugin_open`.

## Host-reserved methods

These names exist in host dispatch but are not usable by generated plugins merely by declaring them:

| Methods | Required host identity |
| --- | --- |
| `plugin.template`, `plugin.list`, `plugin.configure`, `plugin.open`, `plugin.rollback`, `plugin.install` | Echo executing the built-in `plugin-authoring` plugin. |
| `git.run`, `fossil.run`, `p4.run` | Matching built-in source-control plugin. |
| `go.run`, `go.inspect` | Built-in `go` plugin. |
| `confluence.search`, `confluence.read`, `confluence.users.search`, `confluence.users.current` | Built-in `confluence` plugin. |
| `jira.search`, `jira.issue`, `jira.comments` | Built-in `jira` plugin. |
| `comfyui.generate`, `comfyui.generate_video`, `comfyui.save_image`, `comfyui.save_video` | Built-in `comfyui` plugin and appropriate chat context. |

Native Monaco/Terminal/Notes apps, Go language/debug services, Swarm, hosted-server controls, credentials, and host module registration are repository integrations, not manifest extension points. There are no custom lifecycle hooks, arbitrary native frontend imports, startup scripts, uninstall hooks, or additional broker methods implied by their built-in existence. A new native capability requires deliberate host/frontend source changes and cannot be supplied by an installed WASI package alone.
