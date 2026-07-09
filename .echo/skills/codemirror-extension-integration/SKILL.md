---
name: codemirror-extension-integration
description: How to add CodeMirror 6 extensions to the Echo code editor, including keymaps, DOM event handlers, symbol detection utilities, and minimap integration with correct API usage and theming.
triggers:
    - CodeMirror extension
    - add editor feature
    - minimap
    - '@replit/codemirror-minimap'
    - editor.ts extensions
    - lsp definition
    - contextmenu handler
    - symbol detection
---

# CodeMirror 6 Extension Integration

## Adding Extensions to the Editor

The editor lives in `frontend/src/codeView/editor.ts`. Extensions are added through extension functions in `lsp.ts` that return `Extension` arrays.

### Key Pattern: Conditional Extensions

Extensions should check if the file supports LSP before returning active extensions:

```ts
export function lspDefinitionExtension(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
  openCodeFile: OpenCodeFileForDefinition,
): Extension {
  if (!isLspSourcePath(path)) {
    return [];
  }
  // ... return extensions
}
```

### Keymap Extensions

Use `Prec.highest()` to ensure keybindings take priority:

```ts
return Prec.highest([
  keymap.of([{
    key: "F12",
    run: (view) => { /* handler */ },
  }]),
]);
```

### DOM Event Handlers

Add event handlers through `EditorView.domEventHandlers`:

```ts
EditorView.domEventHandlers({
  contextmenu(event, view) {
    const coords = view.posAtCoords({ x: event.clientX, y: event.clientY });
    // handle event
    return false; // don't prevent default unless needed
  },
}),
```

### Combining Extensions

Return arrays of extensions from extension functions. Multiple extensions can be combined:

```ts
return Prec.highest([
  keymap.of([...]),
  EditorView.domEventHandlers({...}),
]);
```

## Symbol Detection Utilities

`hasIdentifierAtPosition(state, position)` - Returns the identifier string at a cursor position, or null.
`goToLspDefinitionAtPosition(state, position)` - Returns the file content offset for LSP definition requests, or null.

Both use `identifierBoundsAt()` internally to find word boundaries using Unicode identifier character detection.

## Adding New Editor Features

1. Create extension function in `lsp.ts` returning `Extension`
2. Add to `mountActiveCodeEditor` in `editor.ts` alongside existing extensions
3. Use `Prec.highest()` for keybindings that should override defaults
4. Check `isLspSourcePath(path)` for LSP-dependent features

## Minimap-Specific Notes

- `@replit/codemirror-minimap` latest is `^0.5.2` (not v1).
- Uses `showMinimap.of(...)` facet pattern, not `minimap()` function.
- The minimap mounts itself into `view.scrollDOM` — it does not need a separate DOM container in the editor mount element.
- It adds a right scroll margin automatically via `EditorView.scrollMargins.of(...)`.

### CSS Styling for Minimap

Minimap classes used by `@replit/codemirror-minimap`:
- `.cm-minimap` — outer container (inherits `.cm-gutters` and `.cm-minimap-gutter`)
- `.cm-minimap-inner` — inner wrapper containing the canvas
- `.cm-minimap-overlay` / `.cm-minimap-overlay-container` — viewport indicator overlay
- `.cm-minimap-line`, `.cm-minimap-selected` — line/selection styling

Style in `frontend/src/styles.css` using existing `--code-editor-*` CSS variables. Use standard kebab-case CSS properties (not camelCase) to avoid esbuild CSS minifier warnings.
