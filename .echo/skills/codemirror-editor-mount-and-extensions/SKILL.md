---
name: codemirror-editor-mount-and-extensions
description: CodeMirror editor mount lifecycle, conditional extensions (Vim mode, whitespace indicators), re-mount guard, and CodeViewCallbacks wiring pattern.
triggers:
    - CodeMirror editor mount
    - vim keybindings
    - editor extensions
    - re-mount guard
    - CodeViewCallbacks
    - code view editor architecture
    - conditional editor features
    - whitespace indicators
---

## CodeMirror Editor Mount Architecture

The code view uses a single globally mounted `EditorView` instance (`mountedEditor`) in `frontend/src/codeView/editor.ts`. Switching files or workspaces reuses the same mount element (`[data-code-editor-mount]`).

### Mount State Variables (module-level)

```
mountedEditor: EditorView | null
mountedEditorWorkspaceID: string
mountedEditorPath: string
mountedEditorLineSeparator: string
mountedEditorWhitespaceIndicators: boolean
mountedEditorVimMode: boolean
mountedEditorCallbacks: CodeViewCallbacks | null
mountedEditorHooks: EditorFeatureHooks | null
editorMountToken: number  // monotonic counter for async race protection
```

### Re-mount Guard

`mountActiveCodeEditor()` skips full re-creation when ALL of these match:
- `mountedCodeEditorMatches(workspaceID, tab.path)` — same workspace + path
- `mountedEditor` exists
- `mountedEditorLineSeparator === tab.lineSeparator`
- `mountedEditorWhitespaceIndicators === whitespaceIndicators`
- `mountedEditorVimMode === vimEnabled`

If any differ, the editor is destroyed and re-created. This means toggling Vim keybindings or whitespace indicators in settings triggers a full re-mount on next code view activation.

### Extension Ordering (important for key priority)

Extensions are added in this order inside `mountActiveCodeEditor`:

1. Git changed line gutter (conditional)
2. `basicSetup` (default CodeMirror setup)
3. `vim()` (conditional, when `vimEnabled`) — **before custom keymaps so Vim intercepts keys**
4. `highlightSelectionMatches`
5. `tabIndentionExtensions()` — uses `Prec.highest` for Tab autocomplete override
6. EditorState config (lineSeparator, allowMultipleSelections)
7. `EditorView.lineWrapping`, theme, syntax highlighting
8. `codeNavigationHistoryKeymap()` — Alt+Left/Right navigation (`Prec.highest`)
9. `rectangularAltSelectionExtension()` — Alt+click rectangular selection (`Prec.highest`)
10. `crosshairCursor()`
11. Update listener (selection/content tracking)
12. Debug, LSP definition, rename, references, inline chat extensions (conditional for non-untitled/non-external)
13. Leading whitespace indicator extension (conditional)
14. Language extension (async loaded)
15. LSP completion extension (conditional)

**Key invariant:** Custom keymaps using `Prec.highest` (Tab autocomplete, Alt navigation, rectangular selection) override Vim's default behavior. The `vim()` extension itself uses its own internal priority system for normal/insert/visual modes.

### CodeViewCallbacks Pattern

Settings-driven editor features are wired through `CodeViewCallbacks` in `frontend/src/codeView/types.ts`. The pattern:

1. Add callback to `CodeViewCallbacks` interface (e.g., `vimKeybindingsEnabled: () => boolean`)
2. Wire in `bootstrap.ts` `codeViewCallbacks()` using the helper from `state.ts`
3. Call from editor code (e.g., `callbacks.vimKeybindingsEnabled()`)

The callback reads `state.appState?.settings ?? state.settingsDraft`, so it reflects both persisted and draft settings.

### Teardown

`teardownMountedEditor(saveContent)` resets ALL mount state variables including `editorMountToken++`. Call `destroyCodeEditor()` (which calls teardown with saveContent=true) before mounting a new editor.

### Adding New Conditional Extensions

To add another setting-gated extension:
1. Add to `CodeViewCallbacks` in types.ts
2. Wire in bootstrap.ts
3. Add module-level `mountedEditor<Feature>` variable
4. Include in mount-skip guard condition
5. Reset in `teardownMountedEditor`
6. Place in extensions array at correct priority position relative to vim() and custom keymaps
