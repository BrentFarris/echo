---
name: spell-check-context-menu-integration
description: 'Spell check context menu integration: rendering misspelling detection, suggestion buttons, Add to dictionary action, and the event handler wiring through the editor context menu callback chain.'
triggers:
    - spell check context menu
    - misspelling detection
    - editor spell suggestions
    - context menu callback wiring
    - add to dictionary
    - applySpellSuggestion
    - spellCheckIgnoreList
---

## Spell Check Context Menu Integration

Wires CodeMirror spell check misspelling data through the existing editor context menu callback chain so right-clicking a misspelled word shows suggestions and an "Add to dictionary" option.

### Data Flow

1. **Detection** (`codeView/lsp.ts`): The `contextmenu` handler in `lspDefinitionExtension` calls `getMisspellingAtPosition(view, coords)` to detect misspellings at the click position. If found, it calls `getSuggestions(word, 5)` from the dictionary module.

2. **Callback** (`codeView/types.ts`): The `showEditorSymbolContextMenu` signature includes optional spell check parameters:
   - `spellCheckWord?: string` — the misspelled word
   - `spellCheckSuggestions?: string[]` — up to 5 suggestions
   - `spellCheckFrom?: number` — document position start of the misspelling
   - `spellCheckTo?: number` — document position end of the misspelling

3. **State** (`app/types.ts`): `ContextMenuState` includes the same optional spell check fields.

4. **Wiring** (`app/bootstrap.ts`): The `showEditorSymbolContextMenu` implementation passes spell check data through to `showContextMenu()`.

5. **Rendering** (`app/contextMenu.ts`): `renderEditorContextMenu` renders:
   - **"Add to dictionary" button** — always shown when `spellCheckWord` is present, uses `data-action="editor-spell-add-dictionary"` with `data-spell-word` attribute
   - **Suggestion buttons** — shown below a divider when `spellCheckSuggestions` has entries, use `data-action="editor-spell-suggest"`
   - Each button includes `data-workspace-id`, `data-editor-path`, and relevant data attributes

6. **Actions** (`app/actions.ts`):
   - `editor-spell-add-dictionary`: calls `addToSpellCheckDictionary(workspaceID, word)` from `state.ts`. Lowercases/trim the word, adds to `spellCheckIgnoreList`, persists via exported `saveSpellCheckIgnoreList`, shows a toast, and triggers full re-render. Returns whether the word was newly added.
   - `editor-spell-suggest`: calls `applySpellSuggestion()` which validates the mounted editor matches workspace/path, reads `spellCheckFrom`/`spellCheckTo` from `state.contextMenu`, and dispatches `view.dispatch({ changes: { from, to, insert } })`.

### Dictionary Persistence (`codeView/state.ts`)

- `spellCheckIgnoreList` is a `Set<string>` per workspace in `CodeWorkspaceState`.
- Persisted to `localStorage` under key `echo:spell-check-ignore-list` as JSON array.
- `addToSpellCheckDictionary(workspaceID, word)`: exported function that adds the lowercased/trimmed word (rejects single characters), persists, and returns whether it was newly added.
- `saveSpellCheckIgnoreList(list)`: exported so `actions.ts` can persist changes outside normal editor state updates.

### Key Patterns

- Spell check data flows through the existing callback chain; no new parallel callbacks are needed.
- Position range (`from`/`to`) is stored in `ContextMenuState` and accessed by the action handler via `state.contextMenu`, not through DOM data attributes.
- "Add to dictionary" appears **before** suggestions so it's always available even when no suggestions exist.
- After adding to dictionary, a full app re-render (`getAppCallbacks().render()`) triggers CodeMirror to re-run the spell check plugin on the next viewport update, removing the red squiggle.

### Important Files

| File | Role |
|------|------|
| `frontend/src/app/types.ts` | `ContextMenuState` definition with spell check fields |
| `frontend/src/codeView/types.ts` | `CodeViewCallbacks.showEditorSymbolContextMenu` signature |
| `frontend/src/codeView/lsp.ts` | Context menu handler detecting misspellings |
| `frontend/src/codeView/spellCheck.ts` | `spellCheckExtension` ViewPlugin, `getMisspellingAtPosition`, diagnostics |
| `frontend/src/codeView/dictionary.ts` | `checkWord`, `suggest`, identifier splitting |
| `frontend/src/codeView/state.ts` | `spellCheckIgnoreList`, `addToSpellCheckDictionary`, `saveSpellCheckIgnoreList` |
| `frontend/src/app/bootstrap.ts` | Callback wiring |
| `frontend/src/app/contextMenu.ts` | Spell check button rendering |
| `frontend/src/app/actions.ts` | `editor-spell-add-dictionary` and `editor-spell-suggest` handlers |
| `frontend/src/styles.css` | `.workspace-context-menu-section-label` styling |

### Pitfalls

- `menu.spellCheckWord` is `string | undefined` — use `?? ""` with `escapeHtml`/`escapeAttribute`.
- The action handler for suggestions reads position data from `state.contextMenu`, not from DOM attributes.
- Always validate that the mounted editor workspace and path match before dispatching changes.
- CodeMirror's spell check plugin runs on viewport/doc changes. Adding to the ignore list requires a re-render cycle for diagnostics to update — there's no direct "force re-check" API.
- `saveSpellCheckIgnoreList` must be exported (not private) for `actions.ts` to persist dictionary changes.
