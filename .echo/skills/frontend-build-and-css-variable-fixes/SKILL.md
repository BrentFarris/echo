---
name: frontend-build-and-css-variable-fixes
description: Post-merge frontend build verification, TypeScript import resolution for backend services, and valid CSS variable naming conventions in styles.css.
triggers:
    - build fix
    - npm run build
    - tsc error
    - TS2304
    - missing import
    - CSS variable
    - styles.css merge conflict
    - ClearChat import
    - --color-surface
    - --color-danger
---

# Frontend Build Verification & CSS Variable Fixes

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
