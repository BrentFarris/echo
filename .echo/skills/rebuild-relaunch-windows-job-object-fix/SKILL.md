---
name: rebuild-relaunch-windows-job-object-fix
description: 'Windows rebuild-and-relaunch process detachment: VBScript via wscript.exe to escape Wails job object without leftover processes, why other methods fail'
triggers:
    - rebuild and relaunch
    - job object
    - wscript.exe
    - VBScript launcher
    - Windows process detachment
    - runtime.Quit child killed
---

## Windows Rebuild-and-Relaunch Process Detachment

### Current Approach: VBScript launcher via `wscript.exe`

`launchDetachedRebuild` in `rebuild_relaunch_windows.go` generates a `.bat` + `.vbs`, then starts the vbs with `wscript.exe`. The VBScript runs the .bat hidden (window style 0) and self-deletes. This escapes Wails' job object cleanly with zero leftover processes or windows.

```go
baseName := strings.TrimSuffix(scriptPath, ".ps1")
batPath := baseName + ".bat"
vbsPath := baseName + ".vbs"

// Write bat content...
// Write vbs that runs bat hidden and self-deletes...
cmd := exec.Command("wscript.exe", vbsPath)
return cmd.Start()  // fire-and-forget, no context
```

### Why other approaches fail
- **`DETACHED_PROCESS | CREATE_BREAKAWAY_FROM_JOB`** — Wails' job object rejects `CREATE_BREAKAWAY_FROM_JOB`; child is killed on parent exit.
- **`cmd /c start "" batfile.bat`** — escapes job object but leaves a cmd.exe process running forever.
- **`cmd /c start /b "" batfile.bat`** — still leaves orphaned processes.
- **`schtasks`** — `schtasks /Delete` kills any running instance of that task.

### Pitfalls
- **Extension stripping**: Always use `strings.TrimSuffix(scriptPath, ".ps1")`. Hardcoded `[:len-3]` produces `.pbat`/`.plog`.
- **Use `cmd.Start()`, not `cmd.Run()`** — the launcher must return immediately without waiting for the rebuild to finish.

### Verification
- `go test ./internal/services/ -run TestPrepareRebuildAndRelaunch -v` — all tests pass
