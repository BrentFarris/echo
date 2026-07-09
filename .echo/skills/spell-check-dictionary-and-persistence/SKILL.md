---
name: spell-check-dictionary-and-persistence
description: Spell check dictionary module and CodeWorkspaceState ignore list persistence for the code editor.
triggers:
    - spell check
---

## Spell Check Dictionary & Persistence

### Files
- `frontend/src/codeView/dictionary.ts` — dictionary module with word validation and identifier splitting.
- `frontend/src/codeView/types.ts` — `CodeWorkspaceState` includes `spellCheckIgnoreList: Set<string>`.
- `frontend/src/codeView/state.ts` — persistence helpers (`loadSpellCheckIgnoreList`, `saveSpellCheckIgnoreList`) using `localStorage` with key `echo:spell-check-ignore-list`.
- `frontend/package.json` — `@codemirror/lint` dependency.

### Dictionary Module Exports
- **`splitIdentifier(text: string): string[]`** — Splits camelCase, PascalCase, and snake_case into constituent words. Uses regex `/[A-Z]?[a-z]+|[A-Z]+(?=[A-Z][a-z]|\d|\b)|\d+/g`.
- **`checkWord(word: string): boolean`** — Returns `true` if the word is valid (in built-in dictionary, all-uppercase, single character, or purely numeric). Uses a `Set<string>` of common English and programming words.
- **`getSuggestions(word: string, maxSuggestions?: number): string[]`** — Returns up to `maxSuggestions` (default 5) corrections using Levenshtein distance against the dictionary.

### Persistence Pattern
The ignore list follows the same localStorage pattern as `explorerWidth`:
1. `loadSpellCheckIgnoreList()` reads from `localStorage`, parses JSON array, filters to strings, returns a `Set<string>`.
2. `saveSpellCheckIgnoreList(list: Set<string>)` serializes the set to an array and stores as JSON.
3. `ensureCodeState()` initializes `spellCheckIgnoreList` from storage on first access for each workspace.

### Key Decisions
- Used `localStorage` instead of backend persistence — it's a client-side preference that doesn't need to survive browser resets beyond session data.
- Levenshtein distance uses two-row optimization for memory efficiency.

### Verification
- `npm run build` in `frontend/` confirms TypeScript compilation.
- `go test ./...` confirms no backend regressions.
