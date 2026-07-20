---
name: comfyui-execution-error-handling
description: 'ComfyUI execution error handling: status.messages[] parsing for execution_error events, priority-ordered error detection, node-level fallbacks, completed-but-no-images detection, poll count safety net in WaitForCompletionPoll'
triggers:
    - comfyui error
    - comfyui execution failure
    - comfyui status messages
    - WaitForCompletionPoll
    - GetHistory error detection
    - comfyui timeout
    - poll safety net
    - execution_error event
---

## ComfyUI Execution Error Handling

### Error Detection Order in `GetHistory` (`internal/comfyui/client.go`)

The status error check uses this **priority order**:

1. **`status.messages[]` execution_error event** — ComfyUI puts errors in `status.messages` as `[event_type, data]` tuples where `data` contains `node_id`, `exception_type`, `exception_message`, and `traceback`. This is the primary source of error details.
2. **`status.error` map/object** — extracts `message`, falls back to first `traceback` line.
3. **`status.error` string** — direct error string.
4. **Node-level errors in `outputs`** — each node can have an `error` field with `error_message`.
5. **`status.status_str == "error"`** — only a signal that *something* failed; use sources above for details.

### Bug: `status.messages` was never checked

ComfyUI 0.24+ puts execution errors in `status.messages` as `[event_type, data]` tuples, not in `status.error`. The original code only checked `status.error` and `status_str`, so it missed the actual error details entirely and returned generic `"execution status is error"` messages.

**Fix**: Scan `status.messages` for entries where `arr[0] == "execution_error"`, then extract `node_id`, `exception_type`, `exception_message` from `arr[1]`.

### Bug: WaitForCompletionPoll spun forever on completed-but-failed

When `GetHistory` returned successfully with zero output images (execution completed but failed), the poller treated it as "still running" and looped until context timeout.

**Fix in `internal/comfyui/queue.go`**:
1. After `GetHistory` returns successfully, if there are no output images, immediately return `"generation completed but produced no output images"` — the prompt being in history means execution finished.
2. Added `maxPolls` safety net: `maxPolls := int(timeout.Seconds()) + 1`, with a counter that triggers `"generation timed out: exceeded maximum poll count"`.

### Key files
- `internal/comfyui/client.go`: `GetHistory` status error detection (check messages array first)
- `internal/comfyui/queue.go`: `WaitForCompletionPoll` completion logic and poll limits
- `internal/tools/comfyui_generate.go`: tool returns `comfyui_error` for any execution error

### Verification
```powershell
go test ./internal/comfyui/... -v -timeout 60s
go test ./internal/tools/... -v -run "Comfyui" -timeout 60s
```

Tests cover: messages-array execution errors, map-format status errors (with message, traceback-only), `status_str == "error"`, completed-but-no-images detection, max poll count bounds.
