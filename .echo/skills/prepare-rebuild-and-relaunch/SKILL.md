---
name: prepare-rebuild-and-relaunch
description: 'PowerShell rebuild-and-relaunch script: path quoting, logging, launch verification, and error handling requirements'
triggers:
    - rebuild and relaunch
    - PowerShell script
    - wails build
    - Start-Process
    - relaunch log
    - binary verification
    - CREATE_NO_WINDOW
    - syscall constant
---

## PrepareRebuildAndRelaunch Service Method

`PrepareRebuildAndRelaunch(workspaceID string) error` is an exported method on `*SystemService` that prepares a rebuild-and-relaunch script and triggers graceful shutdown.

### Files

| File | Purpose |
|------|---------|
| `internal/services/rebuild_relaunch.go` | Shared logic: validation, script generation, `runtime.Quit()` call |
| `internal/services/rebuild_relaunch_windows.go` | Windows detached process launch |
| `internal/services/rebuild_relaunch_default.go` | Unix detached process launch (Setpgid) |
| `internal/services/rebuild_relaunch_test.go` | Unit tests for all validation scenarios |

### Flow

1. Validate `workspaceID` is non-empty
2. Look up workspace via `workspaceByID()`
3. Iterate workspace folders to find one containing `wails.json`
4. Generate platform-specific relaunch script to `%LOCALAPPDATA%\Echo\rebuild-relaunch.ps1` (Windows) or `~/.echo/rebuild-relaunch.sh` (Unix)
5. Script waits up to 10 seconds for graceful shutdown, force-kills remaining processes, runs `wails build`, then launches the rebuilt binary
6. Launch script as detached background process
7. Call `runtime.Quit(ctx)` to trigger graceful Wails shutdown

### Windows Launch (`launchDetachedRebuild`)

**Launch pwsh directly with DETACHED_PROCESS | CREATE_BREAKAWAY_FROM_JOB:**

```go
func launchDetachedRebuild(scriptPath string) error {
    pwsh, err := exec.LookPath("pwsh.exe")
    if err != nil {
        pwsh, err = exec.LookPath("powershell.exe")
        if err != nil {
            return fmt.Errorf("PowerShell not found: %w", err)
        }
    }

    cmd := exec.Command(pwsh, "-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass", "-File", scriptPath)
    cmd.SysProcAttr = &syscall.SysProcAttr{
        CreationFlags: 0x00000008 | 0x01000000, // DETACHED_PROCESS | CREATE_BREAKAWAY_FROM_JOB
    }
    if err := cmd.Start(); err != nil {
        return fmt.Errorf("start rebuild process: %w", err)
    }
    _ = cmd.Process.Release()
    return nil
}
```

**DO NOT use `cmd /c start`** — it mangles argument binding and causes `"Cannot validate argument on parameter 'ArgumentList'"` errors.

### PowerShell Script Requirements (`buildRebuildPowerShellScript`)

- **Log file in `%LOCALAPPDATA%\Echo\rebuild-relaunch.log`**
- **Use single quotes for workspace paths** — double quotes cause backslash escape interpretation
- **Launch binary via `Start-Process -FilePath $binaryPath`** — `-FilePath` is required
- **`$ErrorActionPreference = 'Continue'`** — "Stop" kills script silently on non-terminating errors
- **Wrap build/launch in try-catch** with logging
- **Ensure log directory exists and clear previous log at startup**

### Known Pitfalls

- Never clean up the script file after launch — deferred removal deletes it before the detached process starts
- The `ctx` may be nil in test environments; `runtimeQuit()` handles nil context gracefully

### Verification

```powershell
go test ./internal/services/ -run TestPrepareRebuildAndRelaunch -v
```
