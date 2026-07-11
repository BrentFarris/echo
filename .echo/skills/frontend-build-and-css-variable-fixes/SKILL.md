---
name: frontend-build-and-css-variable-fixes
description: 'Wails binding regeneration: when wails generate is unavailable, how to manually add TypeScript types and service bindings in the generated wailsjs directory.'
triggers:
    - wails generate
    - binding regeneration
    - SystemService.ts
    - models.ts
    - TypeScript binding
    - frontend wailsjs
    - manual binding
    - TS1127
    - tab indentation
---

# Frontend Build Verification & Wails Binding Regeneration

## Post-Merge Build Verification
Run both frontend and backend builds after resolving merge conflicts:
```bash
# Frontend
cd echo/frontend && npm run build

# Backend
cd echo && go build ./...
```

## TypeScript Import Resolution (TS2304)
When `tsc` reports `Cannot find name 'X'` for a backend service function:

1. Confirm the function is exported in `echo/frontend/src/backend/services.ts`
2. Add it to the existing import block from `"../backend/services"` in the consuming file (e.g., `actions.ts`)
3. Example: `ClearChat` must be explicitly imported alongside other service calls like `PruneChatMessage`, `StopChatStream`, etc.

## Valid CSS Variables in styles.css
During merge conflict resolution, these variable names were corrected:

| Invalid (broken) | Valid replacement |
|---|---|
| `--color-surface-raised` | `--color-surface` |
| `--color-error` | `--color-danger` |

CSS variables are defined in `:root` of `echo/frontend/src/styles.css`. Always verify against existing tokens before introducing new ones. See `css-typography-spacing-tokens` skill for the full token system.

## Common Merge Conflict Patterns
- Broken `Nvar(...)` syntax artifacts from merge conflicts → replace with proper `var(--token-name)`
- Undefined CSS variable references → cross-check against `:root` declarations
- Missing TypeScript imports after service function additions → search `backend/services.ts` for the export and add to import statement

## Wails Binding Regeneration

### Standard approach
Run `wails dev` or `wails build` from the repo root — bindings regenerate automatically in `frontend/wailsjs/go/`.

### When wails generate is unavailable
Wails v2.11+ replaced `wails generate` (bindings) with `wails generate module/template`. If you cannot run `wails dev` or `wails build` (e.g., in a headless agent environment), add bindings manually:

**New types go in `frontend/wailsjs/go/models.ts`:**
- Add inside the appropriate namespace (e.g., `services`) as an `export class`.
- Follow the existing pattern: fields, `static createFrom()`, constructor with JSON parsing.
- Use `\t` tab indentation consistently — literal `\t` escape sequences cause TS1127 errors.

**New methods go in two files:**
- `frontend/wailsjs/go/services/SystemService.d.ts`: add the TypeScript declaration (alphabetically).
- `frontend/wailsjs/go/services/SystemService.js`: add the JS wrapper calling `window['go']['services']['SystemService']['MethodName'](...)`.

### Verification
After manual edits, run `cd frontend; npm run build` to confirm TypeScript compiles. Also run `go build ./...` to ensure Go is clean.
