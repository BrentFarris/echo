---
name: spell-check-viewplugin-architecture
description: 'Spell check ViewPlugin architecture: Lezer tree traversal, decoration-based diagnostics, StateField query API, and debouncing pattern for the CodeMirror editor.'
triggers:
    - spell check ViewPlugin
    - Lezer tree traversal
    - SyntaxNodeRef
    - decoration diagnostics
    - StateField query API
    - getMisspellingAtPosition
    - CodeMirror spell check
---

## Spell Check ViewPlugin Architecture

### Files
- `frontend/src/codeView/spellCheck.ts` — Main module: `ViewPlugin`, `StateField`, query API, and word-finding logic.
- `frontend/src/codeView/editor.ts` — Integration point; `spellCheckExtension(workspaceID)` pushed into the editor extensions array.
- `frontend/src/styles.css` — `.cm-spell-error` CSS class with wavy red underline (`border-bottom: 2px wavy var(--color-danger)`).

### Architecture Overview

The spell check system has two layers:

1. **Dictionary layer** (`dictionary.ts`) — Word validation, identifier splitting, and suggestions (card-1).
2. **ViewPlugin layer** (`spellCheck.ts`) — Syntax tree traversal, decoration rendering, and external query API (card-2).

### ViewPlugin Pattern

The spell check uses a `ViewPlugin.fromClass` with an inner class pattern:

```ts
ViewPlugin.fromClass(
  class {
    inner: SpellCheckPlugin;
    constructor(view) { this.inner = new SpellCheckPlugin(view, workspaceID); }
    get decorations() { return this.inner.decorations; }
    update(update) { this.inner.update(update); }
  },
  { decorations: (plugin) => plugin.decorations },
),
```

The inner `SpellCheckPlugin` class holds state (`decorations`, debounce timer, workspace ID). The anonymous outer class satisfies the `ViewPlugin.fromClass` type contract.

### StateField for External Queries

A `StateField<MisspellingInfo[]>` + `StateEffect.define<MisspellingInfo[]>()` pattern exposes misspellings to external consumers (e.g., context menu):

```ts
const updateSpellDiagnosticsEffect = StateEffect.define<MisspellingInfo[]>();
export const spellCheckField = StateField.define<MisspellingInfo[]>({ ... });
```

After each debounced scan, the plugin dispatches `updateSpellDiagnosticsEffect.of(misspellings)`. External code queries via `getMisspellingAtPosition(view, pos)` which reads `view.state.field(spellCheckField)`.

### Lezer Syntax Tree Traversal

Use `syntaxTree(view.state).iterate({ from, to, enter: (node: SyntaxNodeRef) => { ... } })`.

**Key API fact**: `SyntaxNodeRef` is exported from `@lezer/common`, not `@codemirror/language`. The iterate callback receives a single `SyntaxNodeRef` object with properties `{ type, from, to, parent }`, NOT separate `(type, nodeFrom, nodeTo)` parameters.

Traversal skips children of already-processed parent nodes using a `lastProcessedTo` guard per visible range.

### Node Type Detection

Three sets of Lezer node-type names determine what gets spell-checked:
- `COMMENT_TYPES`: Comment, LineComment, BlockComment, DocComment, Shebang, HTMLComment, XMLComment
- `STRING_TYPES`: String, StringLiteral, StringExpression, TemplateElement, Text, Regex, CharLiteral, Character
- `IDENTIFIER_TYPES`: Name, PropertyName, VariableName, TypeName, LabelName, FieldName, AttributeName

Comments and strings are checked for whitespace-separated words. Identifiers are split via `splitIdentifier()` (camelCase/snake_case) then each sub-word is checked.

### Decoration Pattern

Uses `Decoration.mark({ class: "cm-spell-error" })` rendered through a `RangeSetBuilder<Decoration>`. The custom CSS class `.cm-spell-error` applies the wavy red underline in `styles.css`. This avoids reliance on `@codemirror/lint`'s default diagnostic rendering for consistent styling.

### Debouncing

A 350ms debounce timer prevents excessive re-checks during rapid edits or scrolling. Timer is cleared and rescheduled on each `docChanged` or `viewportChanged` event.

### Pitfalls
- **SyntaxNodeRef import**: Must come from `@lezer/common`, not `@codemirror/language`.
- **Iterate callback signature**: Single `(node: SyntaxNodeRef)` parameter, not destructured `(type, nodeFrom, nodeTo)`.
- **StateField type constraint**: The extension factory should return `Extension[]`, not overly complex generic types that confuse TypeScript.

### Verification
- `npm run build` in `frontend/` — clean TypeScript compilation.
- `go test ./...` — no backend regressions.
