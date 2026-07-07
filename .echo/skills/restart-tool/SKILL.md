---
name: restart-tool
description: 'Restart tool: self-rebuild and relaunch Echo from within Echo via detached background process, with Windows Job Object awareness'
triggers:
    - restart
    - self-restart
    - rebuild and relaunch
    - detached process
    - job object
    - CREATE_BREAKAWAY_FROM_JOB
---

## Restart Tool

`restart` is a registered mutating tool in `internal/tools` that lets Echo stop itself, rebuild via `wails build`, and relaunch — all initiated from within Echo. Designed for remote development where the user cannot physically access the desktop to kill/rebuild/relaunch.

### Files

| File | Purpose |
|------|---------|
| `internal/tools/restart.go` | Tool registration, workspace validation, script generation (OS-aware) |
| `internal/tools/restart_windows.go` | Windows detached process launch via `DETACHED_PROCESS` flag only |
| `internal/tools/restart_default.go` | Unix detached process launch via `Setpgid` |
| `internal/tools/registry.go` | `"restart"` entry in `mutatingToolNames` |

### How It Works

1. Validates workspace contains `wails.json` (must be the Echo source directory)
2. Generates a platform-specific script outside the workspace (`%LOCALAPPDATA%\Echo\restart.ps1` on Windows, `~/.echo/restart.sh` on Unix)
3. Script contents: kill `echo.exe` in workspace dir → `wails build` → `Start-Process` / `nohup` the new binary
4. Launches script as a **detached process** so it survives Echo's death
5. Returns immediately with status message; **script file is NOT cleaned up** — it persists for debugging

### Key Invariants

- The detached process must NOT use `exec.CommandContext` — context cancellation would kill the restart script when Echo exits. Use plain `exec.Command` instead.
- **Windows creation flags depend on execution mode:**
  - **Wails dev mode** (interactive): Use `DETACHED_PROCESS` (0x00000008) only. `CREATE_BREAKAWAY_FROM_JOB` causes `cmd.Start()` to fail silently.
  - **Production rebuild relaunch** (`PrepareRebuildAndRelaunch` service method): Combine both flags (`DETACHED_PROCESS | CREATE_BREAKAWAY_FROM_JOB = 0x01000008`). In production builds the parent exits after graceful shutdown, and the child must break away from the Job Object to survive independently. Without this, the newly-launched instance is killed by job termination.
- **Do NOT clean up the script file after launch** — deferring removal deletes it before the detached process starts, causing silent failure. The script persists in `%LOCALAPPDATA%\Echo\` (Windows) or `~/.echo/` (Unix) for debugging.
- Build logs go to `%LOCALAPPDATA%\Echo\restart.log` (Windows) or `~/.echo/restart.log` (Unix) for debugging failed builds.

### Known Pitfalls

- **Deferred cleanup kills the restart**: Removing the script via `defer cleanupScript()` deletes it before the detached PowerShell process can execute. The fix is to never clean up — leave the script in place.
- **Job Object behavior differs by mode**: In dev mode, `CREATE_BREAKAWAY_FROM_JOB` fails silently. In production rebuild flows, omitting it causes the child to die with the parent's job. Always check which flow you're modifying.
- **Error wrapping**: Always wrap `cmd.Start()` errors with `fmt.Errorf("start PowerShell process: %w", err)` so callers see the root cause.

### Tool Schema

No parameters. Returns `map[string]any` with keys: `status`, `message`, `binaryPath`, `scriptPath`, `workspaceDir`.
