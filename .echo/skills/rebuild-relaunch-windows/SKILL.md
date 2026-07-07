---
name: rebuild-relaunch-windows
description: 'Windows rebuild-and-relaunch flow: bat-file launcher via cmd /c start to escape Wails job object, script generation, and launch verification'
triggers:
    - rebuild
    - relaunch
    - restart
    - rebuild-relaunch.bat
    - cmd /c start
    - job object
    - runtime.Quit
---

## Windows Rebuild & Relaunch Flow

### Architecture
1. `PrepareRebuildAndRelaunch(workspaceID)` writes `.ps1` + `.bat` to `%LOCALAPPDATA%\Echo\`, launches bat via `cmd /c start`, then calls `runtime.Quit()`.
2. The `.bat` runs pwsh with the `.ps1`, self-deletes after completion.
3. The `.ps1` waits for Echo to exit, force-kills remaining processes, runs `wails build`, launches new binary.

### Files
- `internal/services/rebuild_relaunch.go` — cross-platform orchestrator, script builders
- `internal/services/rebuild_relaunch_windows.go` — Windows-specific detached launcher (`cmd /c start`)
- `internal/services/rebuild_relaunch_unix.go` — Unix-specific detached launcher

### Critical Constraints
- **Job object**: Wails/WebView2 creates a restrictive job object that kills all children on `runtime.Quit()`. Only `cmd /c start "" batfile.bat` escapes it.
- **Argument mangling**: `start`'s argument parsing mangles inline arguments. Use a `.bat` file containing the pwsh command to avoid this.
- **schtasks is unusable**: `schtasks /Delete` kills any running instance of that task — don't use it for detachment.

### Verification
```powershell
go build ./...           # compiles
go test ./...            # all packages pass
wails build              # produces working binary
```
