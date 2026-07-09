---
name: editor-context-menu-definition-validation
description: Editor context menu Go-to-Definition button is disabled/grayed out when selected text has no LSP definition mapping, with a custom CSS tooltip explaining why, via async validation after menu open.
triggers:
    - go to definition
    - context menu disabled
    - editor context menu
    - LSP definition validation
    - grayed out button
    - tooltip
    - disabled tooltip
---

## Editor Context Menu: Go-to-Definition Button Validation

The "Go to Definition" button in the editor context menu is **disabled and grayed out** when the selected text does not map to an LSP definition, and shows a **custom CSS tooltip** explaining why.

### Flow

1. `showContextMenu(menu)` renders the menu immediately with the button **disabled** (pending state).
2. If `menu.editorPath` and `menu.editorPosition` are present, it sets `editorPositionValidating: true`, re-renders, then fires `validateEditorDefinition(menu)` asynchronously.
3. `validateEditorDefinition`:
   - Reads file content from the active CodeMirror tab via `codeStates`.
   - Calls `FindWorkspaceFileDefinition(workspaceId, { filePath, content, position })`.
   - If response has `found && targetPath`, sets `editorPositionValid = true`; otherwise `false`.
   - Always clears `editorPositionValidating: false` on completion.
   - Only re-renders if the menu is still open for the same file (guards against stale updates).
4. `renderEditorContextMenu` computes `isDisabled = !hasSymbol || (!isDefinitionValid && !isPending)`.

### Tooltip States

The button uses a `data-tooltip` attribute + CSS `::after` pseudo-element (native `title` does not fire reliably on `:disabled` elements). Three messages:

| State | Tooltip Message |
|---|---|
| No symbol at cursor | `"No symbol detected at cursor position"` |
| LSP validation in progress (`editorPositionValidating === true`) | `"Checking for definition..."` |
| Symbol found but no definition (`editorPositionValid === false`) | `"No definition found for this symbol"` |

### Key Files

- `frontend/src/app/contextMenu.ts` — rendering, `showContextMenu`, `validateEditorDefinition`
- `frontend/src/app/types.ts` — `ContextMenuState.editorPositionValid?: boolean`, `editorPositionValidating?: boolean`
- `frontend/src/styles.css` — `.workspace-context-menu-item:disabled` with `pointer-events: auto`, and `[data-tooltip]:disabled::after` pseudo-element tooltip

### CSS Details

```css
.workspace-context-menu-item:disabled {
  pointer-events: auto; /* allow hover for tooltip */
}
.workspace-context-menu-item[data-tooltip]:disabled::after {
  content: attr(data-tooltip);
  position: absolute;
  left: 100%;
  top: 50%;
  transform: translateY(-50%);
  /* ...styling... */
}
```

### Pitfalls

- The button starts **disabled** and is only enabled on successful LSP response. This avoids flicker and prevents clicks before validation completes.
- `validateEditorDefinition` must guard against the menu being dismissed or switched — check `state.contextMenu.editorPath === menu.editorPath`.
- Always clear `editorPositionValidating: false` in the validation callback, even when the result value hasn't changed (the flag itself needs clearing).
- Use `services.WorkspaceDefinitionRequest.createFrom()` pattern consistently with other callers in `codeView/lsp.ts`.
- Use `data-tooltip` instead of native `title` for disabled elements — browsers suppress `title` tooltips on `:disabled` inputs/buttons.
- Set `pointer-events: auto` on the disabled element so hover events fire and trigger the CSS tooltip.
