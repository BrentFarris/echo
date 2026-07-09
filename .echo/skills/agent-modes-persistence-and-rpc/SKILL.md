---
name: agent-modes-persistence-and-rpc
description: Agent modes stored per-workspace on disk in .echo/modes-{workspaceID}/ as JSON; global AppState.AgentModes removed with automatic migration from legacy state files and legacy .echo/modes/ directories. All mode functions propagate workspaceID for scoped directory resolution.
triggers:
    - agent mode persistence
    - legacy modes migration
    - workspace-scoped modes
    - modes directory migration
    - .echo/modes
    - ensureWorkspaceFolderCache
    - migrateLegacyModesToWorkspaceScoped
    - AppState serialization
    - webserver RPC whitelist
    - agent modes disk storage
    - storedAppState
    - migrateGlobalAgentModesToDisk
---

## Agent Modes Persistence

Agent modes are stored per-workspace on disk in `<folder>/.echo/modes-{workspaceID}/` directories. Global `AppState.AgentModes` was removed; modes now live exclusively on workspace disk.

### Directory Structure

```
<workspace-folder>/.echo/
  modes-{workspaceID}/
    {uuid}/
      mode.json
```

### Migration Pathways

Two migrations run during application load:

1. **State file → Disk** (`migrateGlobalAgentModesToDisk` in `agent_modes.go`): Runs in `system.go` `load()`. Detects `agentModes` key in legacy `state.json`, writes user-defined modes to the first available workspace folder's scoped directory. Assigns new UUIDs and filters out built-ins.

2. **Legacy `.echo/modes/` → Scoped `.echo/modes-{workspaceID}/`** (`migrateLegacyModesToWorkspaceScoped` in `workspace_cache.go`): Runs inside `ensureWorkspaceFolderCache` before creating cache directories. Detects legacy `.echo/modes/` and renames it to `.echo/modes-{workspaceID}/`. Skips if the scoped target already contains modes (prevents data loss). No-op when no legacy directory exists.

### Key Functions

- `workspaceModeDirName(workspaceID)` → `"modes-{workspaceID}"`
- `migrateLegacyModesToWorkspaceScoped(workspaceID, folder)` — renames `.echo/modes/` to `.echo/modes-{workspaceID}/`; skips if scoped target has content
- `ensureWorkspaceFolderCache(workspaceID, folder)` — calls migration before creating directories
- All mode functions (`workspaceModeExistingRoot`, `catalogWorkspaceModes`, `writeWorkspaceModeFile`, etc.) propagate `workspaceID`

### Storage Format

Each mode is stored as `<workspace>/.echo/modes-{workspaceID}/<uuid>/mode.json`:
```json
{
  "id": "<uuid>",
  "name": "My Mode",
  "prompt": "...",
  "permissions": { ... },
  "toolPermissions": [...],
  "pathPermissions": [...]
}
```

### Catalog Validation

- `catalogWorkspaceModes` skips directories that aren't exactly 36 characters (UUID format).
- ID in `mode.json` must match directory name.
- Legacy flat permissions auto-migrate to per-tool Permissions map on load.

### Locking

- All CRUD methods acquire `s.mu` then use `resolveWorkspaceLocked()`.
- Migration runs inside the locked `load()` context after `s.state = state`.

### Web Access RPC

Agent mode methods are whitelisted in `internal/webserver/server.go` via `allowedRPCMethods`. All mode functions propagate `workspaceID` for scoped directory resolution.

### Verification

- `TestMigrateLegacyModesToWorkspaceScopedMovesDirectory` — verifies rename occurs
- `TestMigrateLegacyModesSkipsWhenScopedExistsWithContent` — verifies skip when target populated
- `TestMigrateLegacyModesNoOpWhenNoLegacyDirectory` — verifies no-op when no legacy dir
- `TestAgentModeMigrationFromGlobalState` — end-to-end state file migration
