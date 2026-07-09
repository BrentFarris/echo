---
name: agent-mode-storage-locations
description: 'Agent mode storage locations: legacy vs workspace-scoped directories, and why modes may not appear in the UI.'
triggers:
    - agent mode not showing
    - mode dropdown missing
    - catalogWorkspaceModes
    - workspace-scoped modes
    - legacy modes directory
    - modes migration
---

## Agent Mode Storage

### Workspace-scoped (current)
- Location: `<workspace>/.echo/modes-{workspaceID}/<uuid>/mode.json`
- Scanned by `catalogWorkspaceModes()` → appears in the UI dropdown
- Created via `CreateAgentMode`, `CreateAgentModePerTool`, or `CreateAgentModeFromChat`

### Legacy (deprecated)
- Location: `<workspace>/.echo/modes/<uuid>/mode.json`
- NOT scanned by `catalogWorkspaceModes()` — modes here will **not appear** in the UI
- Migration function `migrateLegacyModesToWorkspaceScoped()` runs during `ensureWorkspaceFolderCache`, but only moves the entire directory; if a mode was manually created or placed in the legacy location after migration already ran, it stays invisible

### Troubleshooting missing modes
1. Check workspace ID from `state.json` → `activeWorkspaceId`
2. Verify mode exists at `.echo/modes-{workspaceID}/<uuid>/mode.json`
3. If found in `.echo/modes/` instead, move the UUID directory to the scoped location and restart Echo
