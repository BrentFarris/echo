---
name: frontend-build-and-css-variable-fixes
description: Post-merge frontend build verification, TypeScript import/callback resolution, Go build error patterns, and valid CSS variable naming conventions.
triggers:
    - build fix
    - npm run build
    - tsc error
    - TS2304
    - missing import
    - CSS variable
    - styles.css merge conflict
    - --color-surface
    - callback type mismatch
    - CodeViewCallbacks
    - 'undefined: ctx'
    - operationMu undefined
---

# Post-Merge Build Verification & Fixes

## Post-Merge Build Verification
Run both frontend and backend builds after resolving merge conflicts:
```powershell
# Frontend
cd echo/frontend; npm run build

# Backend
cd echo; go build ./...
```

## TypeScript Import Resolution (TS2304 / TS2552)
When `tsc` reports `Cannot find name 'X'` for a backend service function or codeView symbol:

1. Confirm the function is exported in the target module (e.g., `echo/frontend/src/backend/services.ts`, `echo/frontend/src/codeView/state.ts`, `echo/frontend/src/codeView/lsp.ts`)
2. Add it to the import block in the consuming file (e.g., `actions.ts`)
3. Example: `codeStates`, `getMountedCodeEditor`, `addToSpellCheckDictionary`, `goToLspDefinitionFromContext` must be explicitly imported from their respective modules

## TypeScript Callback Type Mismatches
When a `CodeViewCallbacks` object literal is missing a property required by the interface:

1. Check `echo/frontend/src/codeView/types.ts` for the full `CodeViewCallbacks` interface
2. Add the missing method to the callbacks object in `echo/frontend/src/app/bootstrap.ts`
3. Ensure parameter names match `ContextMenuState` properties exactly (e.g., `spellCheckWord`, not `spellWord`)

## Valid CSS Variables in styles.css
During merge conflict resolution, these variable names were corrected:

| Invalid (broken) | Valid replacement |
|---|---|
| `--color-surface-raised` | `--color-surface` |
| `--color-error` | `--color-danger` |

CSS variables are defined in `:root` of `echo/frontend/src/styles.css`. Always verify against existing tokens before introducing new ones.

## Go Build Errors Post-Merge

### Undefined Context Variable (`undefined: ctx`)
When merging functions that accept a `context.Context` parameter, the call site may reference `ctx` if the function signature was changed to include it but the caller wasn't updated. Fix by using `context.Background()` at the call site if no context is available in scope.

### Missing Struct Field (`X undefined (type *Y has no field or method)`)
When HEAD adds a mutex field (e.g., `operationMu`) to a struct that master doesn't have, remove the lock/unlock calls from methods that use it — master's version handles concurrency differently (e.g., via per-operation channels or other synchronization).

## Common Merge Conflict Patterns
- Broken `Nvar(...)` syntax artifacts from merge conflicts → replace with proper `var(--token-name)`
- Undefined CSS variable references → cross-check against `:root` declarations
- Stray closing braces `}` left over from conflict resolution → verify function scope manually
- Residual `>>>>>>> branch` markers in template literals → search for them explicitly
- Missing TypeScript imports after service/function additions → search the export module and add to import statement
- Callback interface drift between branches → check types.ts for full interface definition
