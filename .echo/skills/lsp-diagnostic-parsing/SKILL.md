---
name: lsp-diagnostic-parsing
description: How LSP push notifications (textDocument/publishDiagnostics) are parsed and emitted as runtime events in the Go backend.
triggers:
    - LSP diagnostics
    - publishDiagnostics
    - textDocument/publishDiagnostics
    - LSP push notification
    - diagnostic event
    - echo:lsp:diagnostics
---

## LSP Diagnostic Push Notifications

### Architecture

The `lspClient` reads framed JSON-RPC messages from the language server stdout in `readLoop`. Messages with a `method` field are pushed to `handleServerMessage`, which now handles both:

1. **Requests** (have an `id`) — reply to workspace/configuration, workspace/workspaceFolders
2. **Push notifications** (no `id`) — route `textDocument/publishDiagnostics`

### Key Files

- `internal/services/lsp.go` — diagnostic structs, `handleServerMessage`, `handlePublishDiagnostics`, `workspaceDiagnosticFromLSP`, client fields (`workspaceID`, `resolveFilePath`, `onDiagnostics`)
- `internal/services/events.go` — `lspDiagnosticsEventName` constant and exported `LSPDiagnosticsEventName`
- `internal/services/system.go` — `emitLSPDiagnosticsEvent` on `SystemService`

### Wiring the Callback

1. `SystemService.workspaceLSPClient` creates an `onDiagnostics` closure that calls `s.emitLSPDiagnosticsEvent(...)` and a `resolveFilePath` closure wrapping `workspaceRelativePath(workspace, ...)`.
2. Both closures are passed to `startWorkspaceLSPClient`, which stores them on the `lspClient`.
3. When `handlePublishDiagnostics` fires, it converts the file URI to an absolute path, resolves it to a workspace-relative path via `resolveFilePath`, then calls `onDiagnostics(workspaceID, filePath, diagnostics)`.

### Diagnostic Types

- **LSP wire types** (private): `lspDiagnostic`, `lspDiagnosticRelatedInformation`, `lspCodeDescription`, `lspDiagnosticParams`
- **Workspace event types** (exported): `WorkspaceDiagnostic`, `WorkspaceDiagnosticRange`, `WorkspaceDiagnosticPosition`, `WorkspaceDiagnosticRelatedInfo`, `WorkspaceDiagnosticLocation`, `LSPDiagnosticsPayload`

### Event Payload

```go
type LSPDiagnosticsPayload struct {
    WorkspaceID string                `json:"workspaceId"`
    FilePath    string                `json:"filePath"`
    Diagnostics []WorkspaceDiagnostic `json:"diagnostics"`
}
```

Emitted on event name `echo:lsp:diagnostics` via both `emitRuntimeEvent` (for web SSE subscribers) and `runtime.EventsEmit` (for Wails desktop).

### Pitfalls

- Push notifications have `message.ID == nil`; the old `handleServerMessage` returned early on this, so all push notifications were silently dropped. The fix checks `message.Method == ""` first, then handles push notifications when `ID == nil`, then handles requests.
- Diagnostic `code` can be a string or number; `lspCodeValue` tries both and returns `any`.
- Always use the `resolveFilePath` callback to convert absolute paths to workspace-relative paths before emitting events.

### Verification

```powershell
go build ./...
go test ./...
cd frontend; npm run build
```
