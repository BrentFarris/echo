---
name: rebuild-relaunch-bat-debug-log
description: Windows .bat launcher debug logging for rebuild-and-relaunch PowerShell errors
triggers:
    - rebuild relaunch
    - debug log
    - bat launcher
    - PowerShell stderr redirect
    - rebuild-relaunch-debug.log
---

## Debug log redirect in rebuild-relaunch.bat

`launchDetachedRebuild` in `internal/services/rebuild_relaunch_windows.go` generates a `.bat` file that runs the PowerShell relaunch script and self-deletes. The bat content redirects all PowerShell output (stdout + stderr) to a debug log so errors are visible after the hidden window closes:

```go
debugLog := filepath.Join(filepath.Dir(scriptPath), "rebuild-relaunch-debug.log")
batContent := fmt.Sprintf("@powershell -NoProfile -NonInteractive -ExecutionPolicy Bypass -File \"%s\" 2>&1 >> \"%s\"\r\ndel \"%s\"\r\n", scriptPath, debugLog, batPath)
```

The `2>&1 >> "debugLog"` redirect captures stderr and stdout to `%LOCALAPPDATA%\Echo\rebuild-relaunch-debug.log`. Without this, errors flash in a hidden console window and are lost.

The `.bat` still self-deletes after launching; users see "batch file cannot be found" which is expected — the debug log has the real output.
