---
name: agent-mode-permissions-extraction-bug
description: 'Bug fix: agent mode edit form dropped tools with empty paths (allow-all), causing permissions to be wiped on save.'
triggers:
    - agent mode permissions wiped
    - agent mode edit bug
    - permissions extraction
    - extractPermissionsMap
    - tool permissions lost on save
---

## Agent Mode Permissions Extraction Bug

### Root Cause
`extractPermissionsMap()` in `frontend/src/app/settings/index.ts` silently dropped tools that had empty paths from the `mode.permissions` map:

```typescript
if (perm && perm.paths?.length) {  // BUG: skips tools where paths is nil/empty
  result[toolName] = [...perm.paths];
}
```

The backend stores tools with `Paths: nil` to mean "allow all paths" — this is valid and intentional. But the frontend extraction only included tools with non-empty paths, so those "allow all" tools appeared unchecked in the edit form. On save, they were gone.

### Fix (line ~459)
```typescript
if (perm) {
  result[toolName] = perm.paths ? [...perm.paths] : [];
}
```

Include every tool from the permissions map regardless of whether it has paths. Empty array = allow all, which is correct for both rendering and saving.

### Key Invariant
- `Paths: nil` or `Paths: []` in backend = "allow all paths" for that tool
- The frontend must preserve this distinction — never skip a tool just because its path list is empty
