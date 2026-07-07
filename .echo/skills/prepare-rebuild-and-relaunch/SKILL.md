---
name: prepare-rebuild-and-relaunch
description: 'PrepareRebuildAndRelaunch: service method that writes a rebuild-and-relaunch script and calls runtime.Quit() for graceful shutdown, with Windows Job Object handling'
triggers:
    - rebuild and relaunch
    - prepare rebuild
    - graceful shutdown
    - wails.json validation
    - relaunch script
    - runtime quit
    - job object
    - CREATE_BREAKAWAY_FROM_JOB
---

## PrepareRebuildAndRelaunch Service Method

`PrepareRebuildAndRelaunch(workspaceID string) error` is an exported method on `*SystemService` that prepares a rebuild-and-relaunch script and triggers graceful shutdown.

### Files

| File | Purpose |
|------|---------|
| `internal/services/rebuild_relaunch.go` | Shared logic: validation, script generation, `runtime.Quit()` call |
| `internal/services/rebuild_relaunch_windows.go` | Windows detached process launch (DETACHED_PROCESS \| CREATE_BREAKAWAY_FROM_JOB) |
| `internal/services/rebuild_relaunch_default.go` | Unix detached process launch (Setpgid) |
| `internal/services/rebuild_relaunch_test.go` | Unit tests for all validation scenarios |

### Flow

1. Validate `workspaceID` is non-empty
2. Look up workspace via `workspaceByID()`
3. Iterate workspace folders to find one containing `wails.json`
4. Generate platform-specific relaunch script to temp/cache directory (`%LOCALAPPDATA%\Echo\rebuild-relaunch.ps1` on Windows, `~/.echo/rebuild-relaunch.sh` on Unix)
5. Script waits up to 10 seconds for graceful shutdown, then force-kills remaining processes, runs `wails build`, then launches the rebuilt binary
6. Launch script as detached background process
7. Call `runtime.Quit(ctx)` to trigger graceful Wails shutdown

### Key Differences from `restart` Tool

- This is a **service method** on `SystemService`, not a registered tool — called directly from the frontend via Wails bindings
- Requires a **workspaceID** parameter and validates the workspace contains `wails.json`
- Uses `runtime.Quit()` for graceful shutdown rather than relying solely on the script to kill the process
- Script file persists after launch (same invariant as restart tool — never defer cleanup)

### Known Pitfalls

- **Windows Job Object**: The child process must break away from the parent's job object using both `DETACHED_PROCESS` (0x00000008) and `CREATE_BREAKAWAY_FROM_JOB` (0x01000000). Without `CREATE_BREAKAWAY_FROM_JOB`, the newly-launched instance is killed by job termination when Echo shuts down. Note: in dev mode, `CREATE_BREAKAWAY_FROM_JOB` can cause silent failure — but production builds require it.
- Never clean up the script file after launch — deferred removal deletes it before the detached process starts
- The `ctx` may be nil in test environments; `runtimeQuit()` handles nil context gracefully

### Verification

```powershell
go test ./internal/services/ -run TestPrepareRebuildAndRelaunch -v
```

Covers: valid workspace with wails.json, missing wails.json, empty workspaceID, whitespace-only workspaceID, nonexistent workspaceID.
