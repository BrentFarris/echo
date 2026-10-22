---
name: startup-app-launch
description: How Echo opens the installed PWA (echo:// scheme, Start Menu shortcut fallback) or the browser on startup; relaunches must reopen the app.
triggers:
    - open app on startup
    - echo://
    - protocol handler
    - no-open
    - ECHO_NO_OPEN
    - PWA launch
    - startup browser
    - PWA shortcut
    - relaunch does not reopen
---

# Echo startup app launch (echo:// protocol scheme)

When the Echo process starts, `main.go` calls `launch.Open(port)` (package `internal/launch`) in a goroutine after the server goroutine starts. It:

1. Skips if `ECHO_NO_OPEN=1` or the `-no-open` flag is set.
2. Polls `http://127.0.0.1:<port>/api/health` (up to 10s) before launching anything.
3. Opens, in order of preference:
   - the **installed Echo PWA** via `echo://open?port=<port>` when the OS has a handler for the `echo:` scheme;
   - (Windows) the **installed PWA's Start Menu shortcut** directly — found via a PowerShell scan for `*.lnk` whose target is a `*_proxy.exe` with `--app-id=`, name-matched against "Echo" (exact `Echo.lnk` preferred). Verified working on Windows via `cmd /c start "" <lnk>` from Go exec.
   - the **default browser** at `http://localhost:<port>/`.

## Critical: relaunches must reopen the app
`rebuild.sanitizeArguments` must NOT append `-no-open` to relaunch arguments. A relaunch kills the process serving the app window, so the relaunched process has to reopen it. Appending `-no-open` there caused "nothing opens at all" after an in-app rebuild-relaunch (the flag also persists into that process's own stdout-only session). `-no-open` is an opt-in user flag / `ECHO_NO_OPEN=1` env only.

## How the echo: handler gets registered
- The PWA manifest (`web/public/manifest.json`) declares `protocol_handlers: [{protocol: "echo", url: "/?echo=%s"}]`. Chromium registers the OS handler at PWA install time — **but Brave does not register manifest protocol_handlers at all** (verified on Windows: no `HKCU\Software\Classes\echo` even after a fresh reinstall). The Start Menu shortcut fallback is what fires on Brave machines.
- Windows detection: registry probe `HKCU`/`HKLM Software\Classes\echo\shell\open\command` (check first — `start` with an unhandled scheme pops an OS dialog instead of failing).
- macOS: try `open echo://...` and fall back on non-zero exit (no dry-run exists).
- Linux: `xdg-mime query default x-scheme-handler/echo` non-empty output.

## Frontend
`web/src/protocolLaunch.ts` (`applyProtocolLaunch`, called from `web/js/app.js` bootstrap) strips the `?echo=` parameter the manifest maps the deep link to; if the deep link targets a different port than the PWA origin, it redirects to `http://localhost:<port>/`.

## Testing
- `internal/launch/launch_test.go` uses a fake `commandRunner`; the platform branch is selected via the package-level `goos` variable (tests set and restore it). The PowerShell shortcut-scan command string is built from the `pwaShortcutScript` const so tests can key fake responses to it.
- `web/src/protocolLaunch.test.ts` covers the pure `resolveProtocolLaunch`; `web/src/pwaManifest.test.ts` asserts the handler stays in the manifest.
- Echo's own stdout is not captured by the Windows relaunch launcher, so check `%APPDATA%\Echo\rebuild-relaunch.log` for relauncher messages and the process command line (`Get-CimInstance Win32_Process -Filter "Name='echo.exe'"`) to see actual launch flags.
