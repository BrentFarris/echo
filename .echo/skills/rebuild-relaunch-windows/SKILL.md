---
name: rebuild-relaunch-windows
description: 'Windows rebuild-and-relaunch flow: VBScript launcher via wscript.exe to escape Wails job object without leftover console processes'
triggers:
    - rebuild
    - relaunch
    - restart
    - rebuild-relaunch.bat
    - wscript.exe
    - VBScript launcher
    - job object
    - runtime.Quit
    - extension stripping
---

## Windows Rebuild & Relaunch Flow

### Architecture
1. `PrepareRebuildAndRelaunch(workspaceID)` writes `.ps1` to `%LOCALAPPDATA%\Echo\`, then `launchDetachedRebuild` in `rebuild_relaunch_windows.go` generates a `.bat` + `.vbs` launcher and starts it via `wscript.exe`.
2. Echo calls `runtime.Quit()` immediately after launching.
3. The VBScript runs the .bat hidden (window style 0), then self-deletes.
4. The .bat runs pwsh with the .ps1, rebuilds, launches new binary, then self-deletes.

### Files
- `internal/services/rebuild_relaunch.go` — cross-platform orchestrator, script builders (`prepareRebuildScript`, `buildRebuildPowerShellScript`, `buildRebuildShellScript`)
- `internal/services/rebuild_relaunch_windows.go` — Windows-specific detached launcher (VBScript + .bat via wscript.exe)
- `internal/services/rebuild_relaunch_unix.go` — Unix-specific detached launcher

### Critical Constraints
- **Job object**: Wails/WebView2 creates a restrictive job object that kills all children on `runtime.Quit()`. The VBScript approach (`wscript.exe`) escapes this cleanly without console windows.
- **Extension stripping**: `scriptPath` ends in `.ps1` (4 characters). Always use `strings.TrimSuffix(scriptPath, ".ps1")` — never hardcoded slice like `[:len-3]` which produces `.pbat`/`.plog`.
- **No leftover processes**: VBScript runs hidden and self-deletes. The .bat also self-deletes. No console windows or hanging cmd.exe processes remain.

### Previous Anti-Patterns (DO NOT USE)
- `cmd /c start "" batfile.bat` — works for job object escape but leaves a cmd.exe process forever
- `cmd /c start /b "" batfile.bat` — still leaves orphaned processes
- `DETACHED_PROCESS | CREATE_BREAKAWAY_FROM_JOB` — Wails' job object rejects the breakaway flag
- `schtasks` — `/Delete` kills any running instance

### Verification
```powershell
go build ./...           # compiles
go test ./...            # all packages pass
wails build              # produces working binary
```
