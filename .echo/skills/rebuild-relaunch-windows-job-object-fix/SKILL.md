---
name: rebuild-relaunch-windows-job-object-fix
description: 'Windows rebuild-and-relaunch process detachment: cmd /c start with bat file to escape Wails job object, why other methods fail'
triggers:
    - rebuild and relaunch
    - job object
    - cmd /c start
    - Windows process detachment
    - runtime.Quit child killed
---

## Windows Rebuild-and-Relaunch Process Detachment

### Current Approach: `cmd /c start "" batfile.bat`

`launchDetachedRebuild` in `rebuild_relaunch_windows.go` writes a `.bat` file containing the pwsh command, then launches it with `cmd /c start ""`. This is the **only** mechanism that escapes Wails' job object.

```go
batPath := scriptPath[:len(scriptPath)-3] + "bat"
batContent := fmt.Sprintf("@powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -File \"%s\"\r\ndel \"%s\"\r\n", scriptPath, batPath)
os.WriteFile(batPath, []byte(batContent), 0644)
exec.Command("cmd.exe", "/c", "start", "", batPath).Run()
```

### Why other approaches fail
- **`DETACHED_PROCESS | CREATE_BREAKAWAY_FROM_JOB`** — Wails' job object rejects `CREATE_BREAKAWAY_FROM_JOB`; child is killed on parent exit.
- **`cmd /c start "" pwsh.exe -File script.ps1`** — `start` mangles inline arguments; requires a `.bat` file workaround.
- **`schtasks`** — `schtasks /Delete` kills any running instance of that task, making it unreliable for detachment.

### Verification
- `go test ./internal/services/ -run TestPrepareRebuildAndRelaunch -v` — all 5 tests pass
