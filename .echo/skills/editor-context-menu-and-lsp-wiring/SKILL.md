---
name: editor-context-menu-and-lsp-wiring
description: Editor context menu rendering, LSP symbol position wiring, go-to-definition action flow, and CodeMirror DOM event handler return-value convention.
triggers:
    - editor context menu
---

## Editor Context Menu Architecture

### Overview
Right-clicking in the CodeMirror editor triggers a custom context menu with LSP-powered "Go to Definition". The flow spans the editor extension → callback system → context menu rendering → action handler.

### Data Flow

1. **Editor `contextmenu` event** (`codeView/lsp.ts`, `lspDefinitionExtension`):
   - Captured via `EditorView.domEventHandlers({ contextmenu })`
   - Converts mouse coords to editor position via `view.posAtCoords()`
   - If identifier found at position, converts to file content offset via `goToLspDefinitionAtPosition()`
   - Calls `callbacks.showEditorSymbolContextMenu(workspaceID, path, requestPos | null, clientX, clientY)`
   - **Returns `true`** to prevent the browser's native context menu from appearing. Returning `false` would allow both the custom and native menus to show simultaneously.

2. **Callback wiring** (`app/bootstrap.ts`, `codeViewCallbacks()`):
   - `showEditorSymbolContextMenu` calls `showContextMenu()` with an editor context menu state:
     ```ts
     { workspaceId, displayPath, editorPath: path, editorPosition: position, x, y }
     ```

3. **Context menu rendering** (`app/contextMenu.ts`, `renderContextMenu`):
   - Routes on `menu.editorPath !== undefined` → `renderEditorContextMenu()`
   - Renders "Go to Definition" button with `data-action="editor-go-to-definition"`
   - Button is `disabled` when `editorPosition === null` (no symbol at click)
   - Position stored in `data-editor-position` attribute

4. **Action handler** (`app/actions.ts`, `handleAction`):
   - Reads `data-editor-path`, `data-editor-position` from target element
   - Gets file content from `codeStates.get(workspaceID).tabs`
   - Calls `goToLspDefinitionFromContext()` which invokes `FindWorkspaceFileDefinition` backend service

### Key Types

- `ContextMenuState.editorPath?: string` — distinguishes editor menus from file tree menus
- `ContextMenuState.editorPosition?: number | null` — file content byte offset of symbol, or `null` if no identifier at click

### Important Files
- `frontend/src/app/types.ts` — `ContextMenuState` definition
- `frontend/src/app/contextMenu.ts` — `renderContextMenu`, `renderEditorContextMenu`
- `frontend/src/app/bootstrap.ts` — `showEditorSymbolContextMenu` callback wiring
- `frontend/src/app/actions.ts` — `editor-go-to-definition` action handler
- `frontend/src/codeView/lsp.ts` — `lspDefinitionExtension` contextmenu handler, `goToLspDefinitionFromContext`, `hasIdentifierAtPosition`, `goToLspDefinitionAtPosition`
- `frontend/src/codeView/types.ts` — `CodeViewCallbacks.showEditorSymbolContextMenu` signature

### Pitfalls
- **CodeMirror DOM event handlers must return `true`** to prevent the browser default behavior. Returning `false` allows the native browser action (e.g., native context menu) alongside the custom handler. This applies to all `EditorView.domEventHandlers` callbacks, not just `contextmenu`.
- `editorPath !== undefined` check is required (not truthy) because empty string is valid
- Position stored as file content byte offset (not editor position), converted via `goToLspDefinitionAtPosition` which handles identifier bounds
- When no symbol at click, position is `null` and button should be disabled
- File content comes from the in-memory tab state; if tab is closed, content won't be available
