---
name: codemirror-extension-integration
description: How to add CodeMirror 6 extensions, specifically the @replit/codemirror-minimap package, to the Echo code editor with correct API usage and theming.
triggers:
    - CodeMirror extension
    - add editor feature
    - minimap
    - '@replit/codemirror-minimap'
    - editor.ts extensions
---

## CodeMirror Extension Integration Pattern

### Adding Extensions to the Editor

The editor lives in `frontend/src/codeView/editor.ts`. Extensions are added to the `extensions` array inside `mountActiveCodeEditor()`, which builds the list before creating the `EditorState`.

**Extension placement:** Add new extensions alongside existing ones like `crosshairCursor()` and `rectangularAltSelectionExtension()`. Place them before the `EditorView.updateListener.of(...)` entry.

### Package API Patterns

Different CodeMirror 6 extension packages use different APIs:

- **Function-based extensions** (most common): e.g., `crosshairCursor()`, `minimap()` — call as functions and add the result to extensions array.
- **Facet-based extensions**: e.g., `@replit/codemirror-minimap` exports `showMinimap` which is a facet requiring `.of({ create: () => ({ dom: document.createElement("div") }) })`.

Always verify the actual export by checking the package source — the card description may reference a non-existent function name.

### Minimap-Specific Notes

- `@replit/codemirror-minimap` latest is `^0.5.2` (not v1).
- Uses `showMinimap.of(...)` facet pattern, not `minimap()` function.
- The minimap mounts itself into `view.scrollDOM` — it does not need a separate DOM container in the editor mount element.
- It adds a right scroll margin automatically via `EditorView.scrollMargins.of(...)`.

### CSS Styling

Minimap classes used by `@replit/codemirror-minimap`:
- `.cm-minimap` — outer container (inherits `.cm-gutters` and `.cm-minimap-gutter`)
- `.cm-minimap-inner` — inner wrapper containing the canvas
- `.cm-minimap-overlay` / `.cm-minimap-overlay-container` — viewport indicator overlay
- `.cm-minimap-line`, `.cm-minimap-selected` — line/selection styling

Style in `frontend/src/styles.css` using existing `--code-editor-*` CSS variables. Use standard kebab-case CSS properties (not camelCase) to avoid esbuild CSS minifier warnings.
