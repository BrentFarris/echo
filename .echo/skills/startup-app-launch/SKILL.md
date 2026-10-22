---
name: startup-app-launch
description: How Echo opens the installed PWA (echo:// scheme, Start Menu shortcut fallback) or the browser on startup; relaunches never spawn a window and the old window reloads.
triggers:
    - open app on startup
    - echo://
    - protocol handler
    - no-open
    - ECHO_NO_OPEN
    - PWA launch
    - startup browser
    - PWA shortcut
    - relaunch two windows
    - close window on relaunch
---

# Echo startup app launch (echo:// protocol scheme)

When the Echo process starts, `main.go` calls `launch.Open(port)` (package `internal/launch`) in a goroutine after the server goroutine starts. It:

1. Skips if `ECHO_NO_OPEN=1` or the `-no-open` flag is set.
2. Polls `http://127.0.0.1:<port>/api/health` (up to 10s) before launching anything.
3. Opens, in order of preference:
   - the **installed Echo PWA** via `echo://open?port=<port>` when the OS has a handler for the `echo:` scheme;
   - (Windows) the **installed PWA's Start Menu shortcut** directly — found via a PowerShell scan for `*.lnk` whose target is a `*_proxy.exe` with `--app-id=`, name-matched against "Echo" (exact `Echo.lnk` preferred). Verified working on Windows via `cmd /c start "" <lnk>` from Go exec.
   - the **default browser** at `http://localhost:<port>/`.

## Critical: relaunches never spawn a window
`rebuild.sanitizeArguments` always appends `-no-open` to the detached relaunch arguments. The window that requested the relaunch stays open and reloads itself via `waitForReplacementServer` + `reloadForReplacementServer` (original behavior in `web/js/views/settings.js`). Only launching the exe separately auto-opens the app. Caveat: if the user closes the window during a relaunch, nothing reopens — they must start the exe again. Do NOT try `window.close()` from the relaunch handlers: it is unreliable (Brave refused it even for the installed PWA) and caused the two-window problem it was meant to fix.

## How the echo: handler gets registered
- The PWA manifest (`web/public/manifest.json`) declares `protocol_handlers: [{protocol: "echo", url: "/?echo=%s"}]`. Chromium registers the OS handler at PWA install time — **but Brave does not register manifest protocol_handlers at all** (verified on Windows: no `HKCU\Software\Classes\echo` even after a fresh reinstall). The Start Menu shortcut fallback is what fires on Brave machines.
- Windows detection: registry probe `HKCU`/`HKLM Software\Classes\echo\shell\open\command` (check first — `start` with an unhandled scheme pops an OS dialog instead of failing).
- macOS: try `open echo://...` and fall back on non-zero exit (no dry-run exists).
- Linux: `xdg-mime query default x-scheme-handler/echo` non-empty output.

## Frontend
`web/src/protocolLaunch.ts` (`applyProtocolLaunch`, called from `web/js/app.js` bootstrap) strips the `?echo=` parameter the manifest maps the deep link to; if the deep link targets a different port than the PWA origin, it redirects to `http://localhost:<port>/`.

## Testing
- `internal/launch/launch_test.go` uses a fake `commandRunner`; the platform branch is selected via the package-level `goos` variable (tests set and restore it). The PowerShell shortcut-scan command string is built from the `pwaShortcutScript` const so tests can key fake responses to it.
- `web/src/rebuildRelaunch.test.ts` covers `waitForReplacementServer`. Settings test files (`settingsRebuild.test.ts`, `settingsUpdate.test.ts`, `pluginSettings.test.ts`) mock `./rebuildRelaunch.ts` — flush enough microtask ticks for the handler chain after the POST resolves.
- `web/src/protocolLaunch.test.ts` covers the pure `resolveProtocolLaunch`; `web/src/pwaManifest.test.ts` asserts the handler stays in the manifest.
- Echo's own stdout is not captured by the Windows relaunch launcher, so check `%APPDATA%\Echo\rebuild-relaunch.log` for relauncher messages and the process command line (`Get-CimInstance Win32_Process -Filter "Name='echo.exe'"`) to see actual launch flags.
- Known pre-existing frontend failure unrelated to this area: `src/chatTabs.test.ts` "locks Main Chat to an active goal..." (fails on a clean tree).
