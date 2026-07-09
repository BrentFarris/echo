---
name: codemirror-lint-integration
description: How LSP diagnostics from the Go backend are mapped to CodeMirror lint squiggles and tooltips in the frontend editor, including event subscription, severity mapping, and view lifecycle tracking.
triggers:
    - CodeMirror lint
    - diagnostic squiggles
    - lsp diagnostics frontend
    - echo:lsp:diagnostics
    - lint extension
    - codeView/diagnostics
---

## CodeMirror Lint Integration for LSP Diagnostics

### Overview

The frontend displays LSP diagnostics as CodeMirror lint squiggles and tooltips. The Go backend emits `echo:lsp:diagnostics` events (see skill `lsp-diagnostic-parsing`), and the frontend subscribes, maps severity, and feeds results into `@codemirror/lint`.

### Key Files

- `frontend/src/codeView/diagnostics.ts` — Diagnostic extension module. Subscribes to backend events, maintains per-file state, creates the CodeMirror lint extension.
- `frontend/src/codeView/editor.ts` — Mounts the diagnostic extension alongside other LSP extensions in `mountActiveCodeEditor`.
- `frontend/package.json` — Contains `@codemirror/lint` dependency.

### Architecture

1. **Event subscription**: A single global `EventsOn("echo:lsp:diagnostics", ...)` listener stores incoming diagnostics in a `Map<string, LSPDiagnostic[]>` keyed by `workspaceID\u0000filePath`.

2. **View tracking**: Active editor views are tracked per key so that when new diagnostics arrive, a no-op `view.dispatch({})` triggers the linter to re-run on affected editors.

3. **Linter source**: `linter((view) => ...)` reads from the global state map and maps LSP diagnostics to CodeMirror `LintDiagnostic` objects. The linter function is called by CodeMirror on each update cycle.

4. **Severity mapping**:
   - LSP 1 (Error) → `"error"`
   - LSP 2 (Warning) → `"warning"`
   - LSP 3 (Info) → `"info"` (NOT `"information"`)
   - LSP 4 (Hint) → `"hint"`

5. **Position mapping**: LSP positions are 0-based; CodeMirror `doc.line()` is 1-based. Add 1 to line numbers before calling `view.state.doc.line()`. Clamp `from`/`to` to document length.

6. **Extension composition**: The diagnostic extension is wrapped in `Prec.high([...])` and added conditionally (only for non-untitled, non-external files) in `editor.ts`.

### Pitfalls

- CodeMirror severity type is `"error" | "warning" | "info" | "hint"` — `"information"` will cause a TypeScript error.
- `ViewUpdate` does not have a `viewType` property. Use `updateListener` with tracking flags and check `view.dom.isConnected` for cleanup instead.
- The event subscription is global (singleton pattern). Multiple editors share the same listener; per-file filtering happens in the linter source via the composite key.
- When the editor view is destroyed, remove from both `diagnosticViews` and `activeEditors` to prevent stale dispatch calls on disconnected DOM elements.

### Verification

- TypeScript: `cd frontend; npx tsc --noEmit`
- Build: `cd frontend; npm run build`
- Backend tests: `go test ./...`
