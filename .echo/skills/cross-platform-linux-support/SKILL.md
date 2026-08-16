---
name: cross-platform-linux-support
description: 'Echo is cross-platform: every Windows-specific Go file has a !windows fallback, and all internal packages compile for linux/arm64 (verified, e.g. NVIDIA DGX Spark GB10). Covers the platform-file map, verification command, and Linux build prerequisites (cgo/webkit2gtk required for the full Wails app).'
triggers:
    - linux
    - arm64
    - dgx spark
    - cross-compile
    - wails build linux
    - portability
    - webview
---

# Cross-platform / Linux support

Echo is portable Go; the codebase already ships `!windows` fallbacks for every Windows-specific file:

| Windows file | Fallback (`//go:build !windows`) | Notes |
|---|---|---|
| `internal/tools/shell_command_windows.go` | `shell_command_default.go` | POSIX: `Setpgid` + SIGKILL to process group |
| `internal/tools/restart_windows.go` | `restart_default.go` | detached restart via `sh -c` script |
| `internal/services/workspace_command_windows.go` | `workspace_command_default.go` | no-op config on Linux (xdg-open etc. handled in shared code) |
| `internal/services/debug_process_windows.go` | `debug_process_default.go` | DAP debug attach is Windows-only; default is a graceful stub |
| `internal/services/rebuild_relaunch_windows.go` | `rebuild_relaunch_default.go` | wails build/relaunch flow |

Runtime `runtime.GOOS == "windows"` checks (in `shell_command.go`, `terminal_session.go`, `kanban_verification.go`, `workspace_instructions.go`) correctly branch to `/bin/sh` and non-PowerShell guidance on Linux. No hardcoded Windows paths in Go source.

## Verified

```powershell
# From repo root; all internal packages compile for the Spark's architecture:
$env:GOOS='linux'; $env:GOARCH='arm64'; $env:CGO_ENABLED='0'
go build ./internal/...   # exit 0 (verified with Go 1.26.0)
```

`./...` (top level `main.go`) does NOT compile in cross-build because Wails/webview needs cgo + a C compiler; that is expected and not a portability bug. Build natively on the Linux box instead.

## Native Linux build prerequisites (e.g. DGX Spark, Ubuntu 24.04)

- Go >= 1.26, Node.js + npm
- `build-essential pkg-config libwebkit2gtk-4.1-dev` (Wails v2 cgo dependency on Linux)
- Then just: `wails build` (or `wails dev`). No cross-compilation needed when building on-device.

## Headless note

The Spark often runs headless; the desktop window needs a display, but Echo's web-access mode (`internal/webserver`) serves the full UI over LAN HTTP + SSE and works fine without X/Wayland — good fit for running Echo natively on a Spark and using it from a browser.
