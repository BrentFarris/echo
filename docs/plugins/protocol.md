# Native backend protocol: `echo-jsonrpc-1`

An optional backend is one lazy-started process per enabled plugin, shared across workspace scopes. It exchanges newline-delimited JSON-RPC 2.0 on stdin/stdout. Each message occupies one line; stdout is reserved for protocol traffic and stderr is a bounded, rotated, secret-filtered plugin log.

## Initialization

Echo starts the declared current-platform executable directly and calls `echo.initialize`:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "echo.initialize",
  "params": {
    "protocol": "echo-jsonrpc-1",
    "echo": { "api": 1, "version": "0.1.0" },
    "plugin": { "id": "example-plugin", "version": "1.0.0", "digest": "…" },
    "capabilities": ["tool.invoke", "ui.invoke", "ui.events", "cancellation"]
  }
}
```

The backend must answer with `{ "protocol": "echo-jsonrpc-1" }`. An invalid handshake or malformed stdout fails activation.

## Calls

Tool methods receive `invocationId`, validated `arguments`, effective `scope`, resolved `config`, and permitted workspace metadata. Declared UI RPC methods receive `invocationId`, `sessionId`, plugin `params`, effective `scope`, and resolved `config`. Secret settings appear only in this backend configuration object.

Manifest timeouts are capped by the host. On timeout or caller cancellation, Echo sends a `$/cancelRequest` notification with the JSON-RPC request ID. Backends should stop work promptly. Inputs, messages, and results are bounded; schemas are checked before and after dispatch.

Configuration changes are announced with `echo.configChanged`. A backend should invalidate caches; the next call carries the new resolved configuration. Echo calls `echo.shutdown` during orderly disposal and then closes stdin. It may terminate a process that does not exit within the grace period.

## UI events

A backend can target one live UI session with a notification:

```json
{
  "jsonrpc": "2.0",
  "method": "echo.uiEvent",
  "params": {
    "sessionId": "ui-…",
    "topic": "records.changed",
    "data": { "count": 3 }
  }
}
```

The host forwards only the event payload to the matching plugin iframe session. `echo.ui.event` is accepted as a compatibility spelling, but new plugins should use `echo.uiEvent`.

Never log secrets, echo resolved configuration, print startup banners to stdout, or execute user-controlled strings through a shell. See the [showcase backend](../../examples/plugins/showcase/backend-src/main.go) for a small testable implementation.
