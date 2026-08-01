---
name: comfyui-client-timeout-fix
description: ComfyUI client HTTP timeout fix to prevent indefinite hangs when ComfyUI is unreachable
triggers:
    - comfyui timeout
    - comfyui hang
    - comfyui unreachable
    - http client timeout
    - comfyui error handling
---

## Problem

The ComfyUI client used `http.DefaultClient` which has no timeout. When ComfyUI was unreachable, HTTP requests hung indefinitely and the tool call appeared to cancel without ever returning an error to the UI.

## Fix

Added `httpDoer()` method on `*Client` in `internal/comfyui/client.go`:

```go
const defaultHTTPTimeout = 30 * time.Second

func (c *Client) httpDoer() *http.Client {
    if c.HTTPClient != nil {
        return c.HTTPClient
    }
    return &http.Client{Timeout: defaultHTTPTimeout}
}
```

Replaced all three `c.HTTPClient / http.DefaultClient` fallbacks with `c.httpDoer()` across:
- `client.go`: `Generate()` and `GetHistory()` methods
- `queue.go`: `FetchImageBytes()` method

## Files
- `internal/comfyui/client.go`
- `internal/comfyui/queue.go`

## Behavior
- Custom `HTTPClient` on the struct still takes precedence (for tests)
- Default fallback now has a 30-second timeout covering connection + read
- Errors surface as `comfyui_error` with descriptive message instead of silent cancellation
