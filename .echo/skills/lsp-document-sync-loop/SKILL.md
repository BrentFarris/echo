---
name: lsp-document-sync-loop
description: How the editor sends debounced textDocument/didChange notifications to the LSP server so diagnostics stay current as the user types.
triggers:
    - diagnostic squiggles persist
    - textDocument/didChange
    - SyncLSPDocument
    - debounced LSP sync
    - diagnostics don't clear
    - LSP document sync
---

## LSP Document Sync Loop

### Problem

Diagnostics (red squiggles) persist after code fixes because the LSP server retains its old view of the file. The editor must send `textDocument/didChange` notifications during editing, not just on save.

### Architecture

1. **Editor update listener** (`editor.ts`): On every `update.docChanged`, triggers a debounced call to `SyncLSPDocument` for LSP source files (`.go`, `.c`, `.cpp`, etc.). Untitled and external files are skipped.

2. **Debouncing** (`debouncedSyncLSPDocument`): A module-level singleton timer (500ms delay) coalesces rapid edits into one sync call. The timer is cancelled and reset on each edit. Before syncing, it verifies the editor is still mounted and showing the same file to avoid stale calls after tab switches.

3. **Backend** (`SyncLSPDocument` in `lsp.go`): Accepts a `WorkspaceDefinitionRequest` (reuses this type; only `FilePath` and `Content` are used — `Position` is ignored). Resolves the workspace path, locates the LSP client, and calls `syncDocumentNoLock`.

4. **LSP Client** (`syncDocumentNoLock`): If the document hasn't been opened yet, sends `textDocument/didOpen`. Otherwise compares content; if changed, increments version and sends `textDocument/didChange` with full document text.

5. **Round-trip**: LSP server re-analyzes → pushes `textDocument/publishDiagnostics` → backend emits `echo:lsp:diagnostics` event → frontend updates squiggles (see `codemirror-lint-integration` skill).

### Key Files

- `frontend/src/codeView/editor.ts` — `debouncedSyncLSPDocument`, update listener wiring
- `frontend/src/codeView/utils.ts` — `debounce<T>` utility
- `frontend/src/codeView/lsp.ts` — exported `isLspSourcePath` for gating sync to relevant files
- `internal/services/lsp.go` — `SyncLSPDocument`, `syncDocumentNoLock`
- `internal/webserver/server.go` — `SyncLSPDocument` must be in `allowedRPCMethods` for web-access mode

### Pitfalls

- `SyncLSPDocument` reuses `WorkspaceDefinitionRequest` which requires a `position` field. The frontend passes `position: 0` since the backend ignores it.
- The debounce timer is a **module-level singleton**, not per-editor. This works because only one editor view is mounted at a time (`mountedEditor`). If multiple editors were supported, the timer would need to be keyed by workspace+path.
- Always verify `mountedEditorWorkspaceID === workspaceID && mountedEditorPath === path` before syncing to avoid sending content for a file that's no longer active.
- `isLspSourcePath` must be exported from `lsp.ts` for the editor to gate sync calls; don't duplicate the extension list.

### Verification

```powershell
go build ./...
cd frontend; npm run build
go test ./...
```
