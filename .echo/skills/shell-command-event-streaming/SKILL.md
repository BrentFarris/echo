---
name: shell-command-event-streaming
description: How RunShellCommand/StopShellCommand provide async shell execution with per-line event streaming through SystemService, including the ShellCommandEvent model, goroutine lifecycle, and cleanup patterns.
triggers:
    - RunShellCommand
    - StopShellCommand
    - shell command streaming
    - echo:shell:event
    - ShellCommandEvent
    - async shell execution
    - per-line stdout stderr
    - shell event streaming
---

## Shell Command Event Streaming Architecture

`RunShellCommand` and `StopShellCommand` on `SystemService` provide async shell execution with per-line stdout/stderr event streaming, separate from the synchronous `shell_command` tool in `internal/tools`.

### Key Files
- `internal/services/shell_command.go` — `RunShellCommand`, `StopShellCommand`, `ShellCommandEvent` model, goroutine executor
- `internal/services/events.go` — `ShellRuntimeEventName = "echo:shell:event"` constant
- `internal/webserver/server.go` — `"RunShellCommand": true` and `"StopShellCommand": true` in `allowedRPCMethods`

### ShellCommandEvent Model
```go
type ShellCommandEvent struct {
    WorkspaceID string                `json:"workspaceId"`
    ID          string                `json:"id"`           // "workspaceID:seq"
    Type        ShellCommandEventType `json:"type"`         // "started", "stdout", "stderr", "completed"
    Data        any                   `json:"data,omitempty"`
}

type ShellCommandCompletedData struct {
    ExitCode             int   `json:"exitCode"`
    TimedOut             bool  `json:"timedOut"`
    DurationMilliseconds int64 `json:"durationMilliseconds"`
}
```

### SystemService State
- `shellCommandRuns map[string]context.CancelFunc` — keyed by run ID, protected by `chatMu`
- `shellCommandSeq uint64` — monotonic counter for unique run IDs

### RunShellCommand Flow
1. Validate workspace exists via `workspaceAndSettings(workspaceID)`
2. Resolve working directory (defaults to first workspace folder; supports labeled paths)
3. Clamp timeout (default 30s, max 300s) and output bytes (default 64KB, max 256KB)
4. Generate run ID as `fmt.Sprintf("%s:%d", workspaceID, seq)` under `chatMu`
5. Store cancel func in `shellCommandRuns`
6. Emit `"started"` event synchronously
7. Launch goroutine that:
   - Creates `exec.CommandContext` with timeout
   - Pipes stdout/stderr through `bufio.Scanner`, emits per-line `"stdout"`/`"stderr"` events
   - On completion, emits `"completed"` with exit code/timedOut/duration
   - Calls `cleanupShellRun(runID)` via defer to remove from map

### StopShellCommand Flow
1. Lock `chatMu`, look up cancel func by run ID
2. Call `cancel()` and delete from map
3. The goroutine's `cmd.Wait()` returns, emits `"completed"` with timedOut=true if applicable

### Important Patterns
- `scanLines` accepts `io.Reader` (not `*os.File`) because `cmd.StdoutPipe()` returns `io.ReadCloser`
- `configureProcess(cmd)` is a no-op on Unix; Windows build tag file adds `SysProcAttr` to hide console window
- Shell invocation reuses the same pwsh/powershell/sh detection logic as `internal/tools/shell_command.go` but is duplicated in services (separate package, different purpose)
- Run ID cleanup happens both in `cleanupShellRun` (deferred in goroutine) and `StopShellCommand` — map deletion is idempotent

### Wails Bindings
When `wails generate` is unavailable (v2.11+), manually add to:
- `frontend/wailsjs/go/services/SystemService.d.ts` — TypeScript declaration
- `frontend/wailsjs/go/services/SystemService.js` — JS wrapper

### Web Access RPC
Both methods are whitelisted in `allowedRPCMethods` for HTTP access. The event stream (`echo:shell:event`) is delivered through the existing SSE endpoint at `/api/events`.
