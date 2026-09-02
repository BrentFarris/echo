# Debugging in Echo

Echo Code has a workspace-scoped Debug Adapter Protocol (DAP) client. It can run several sessions at once, launch compounds, reconnect browser tabs to server-owned sessions, and use the same host or Linux-sandbox execution target as the workspace.

Echo does not download debug adapters. Install each adapter in the environment where the workspace runs, then add one of Echo's Delve, debugpy, js-debug, or CodeLLDB profile templates under **Run and Debug → Settings**. A machine-local profile describes the adapter executable and transport. The workspace only stores which profiles it enables and its launch/attach configuration, so a teammate can supply a different executable override on another machine.

Use **Test** beside a profile to start it and complete a DAP `initialize` handshake in the workspace's real host or sandbox environment. A sandbox-enabled workspace never falls back to a host adapter. Spawned TCP adapters bind a private guest loopback port and are bridged over the authenticated sandbox-agent stream; Echo does not publish a Docker port.

## Configuration and persistence

Portable configuration lives in `.echo/workspace.json`:

```json
{
  "debug": {
    "version": 1,
    "enabledAdapterProfileIds": ["delve"],
    "overrides": {},
    "configurations": [
      {
        "id": "go-main",
        "name": "Go: Main",
        "adapterProfileId": "delve",
        "request": "launch",
        "arguments": {
          "program": "${workspaceFolder}"
        },
        "preLaunch": {
          "command": "go",
          "args": ["build", "./..."],
          "timeoutMs": 300000
        }
      }
    ],
    "compounds": [],
    "inputs": []
  }
}
```

The machine-local `echo.json` stores adapter profiles and workspace-keyed breakpoints, exception selections, watch expressions, and the selected configuration. It does not modify the repository for personal breakpoints or watches. Panel layout, selected session/frame, collapsed sidebar sections, and Debug Console history stay in browser storage.

Launch and attach arguments are adapter-specific JSON. Echo recursively expands `${workspaceFolder}`, named workspace folders, active-file variables, `${selectedText}`, `${env:NAME}`, and `${input:id}`. Inputs can prompt for text or a secret, offer a choice, or select a process from the actual host/sandbox process list. `${command:...}` is rejected rather than executed. Lifecycle hooks are direct executable-plus-argv requests with optional cwd, environment, and timeout; they are never shell strings.

The settings dialog provides a common-fields editor for a configuration's ID, name, adapter, and launch/attach request. Adapter arguments and lifecycle hooks remain JSON so adapter-specific fields are preserved; the complete portable document is also available for compounds, inputs, overrides, and other advanced edits. Custom machine profiles can be created alongside the four supplied templates.

Before launch, Echo saves dirty disk-backed editors. Revision conflicts use the normal compare/reload/overwrite flow. If a launch needs an active-file variable while the active editor is untitled, Echo offers Save As and does not start until the file has a workspace path.

## Importing VS Code launches

**Preview VS Code Import** reads `.vscode/launch.json` as JSONC and, only for the preview, `.vscode/tasks.json`. It maps known adapter types, inputs, compounds, and safe process/npm tasks. The common VS Code process picker becomes Echo's host/sandbox process picker. Shell expressions, background tasks, problem-matcher-dependent tasks, unknown adapters, and other command variables are called out for manual correction.

The preview never writes automatically and VS Code files are not a second live configuration source. Review the generated JSON and warnings, then save it to make `.echo/workspace.json` authoritative.

## Workbench

Open **Run and Debug** from the activity bar or with `Ctrl+5`. Its sidebar contains Variables, Watch, Call Stack, Breakpoints, and capability-gated Modules and Loaded Sources views. The floating toolbar only appears while a session exists. Terminal, Debug Console, and Output share the resizable lower panel.

The editor gutter supports normal, conditional, hit-count, log, function, exception, data, and instruction breakpoints. Breakpoint verification and relocation are aggregated across compatible sessions. While stopped, Echo provides stack paging, lazy/paged variables, watch evaluation, editable variables and expressions, inline values, hover evaluation, exception details, virtual adapter sources, memory, and disassembly. Unsupported controls and views remain hidden or disabled according to the adapter's advertised capabilities.

Default keys:

| Key | Action |
| --- | --- |
| `Ctrl+5` | Open Run and Debug |
| `F8` | Start, pause, or continue the active session |
| `Shift+F8` | Stop or disconnect the active session |
| `Ctrl+Shift+F8` | Restart the active session |
| `F9` | Toggle a source breakpoint |
| `F10` | Step over |
| `F11` | Step into |
| `Shift+F11` | Step out |

The command palette also exposes start-new-instance, start-without-debugging, pause/continue all, compound controls, reverse execution, run to cursor, console/output navigation, and settings. Browser defaults are suppressed only while Echo Code owns keyboard focus and no input or modal is active.

**Debug Settings → Debugger attention** can enable browser-local stop notifications. When a breakpoint, exception, or other debugger stop occurs while Echo is hidden or unfocused, Echo shows a browser notification. Clicking it focuses Echo, opens Run and Debug, and restores the stopped session. Echo does not notify for stops that occur while its browser window is already focused, and browser notification permission must be allowed for that browser profile.

## Sessions, terminals, and cleanup

Sessions live in the Echo server rather than in a browser tab. Every authenticated browser subscribed to the workspace receives sequenced debug events and may issue revision-checked controls. A sequence gap or reconnect triggers a REST snapshot; refreshing or closing the initiating browser does not stop the debuggee. Active debug PTYs are separately discoverable and reattach with buffered output after refresh.

Stop terminates a launch debuggee by default. Stop on an attach configuration disconnects without terminating; adapters that advertise process termination expose an explicit **Terminate Process** action. Echo uses an adapter-native restart when available and otherwise performs a clean relaunch. Adapter processes, owned launch processes, pre/post hooks, child sessions, and debug terminals are tied to workspace removal/rebind, sandbox reset/transition, and server shutdown. Host processes are terminated as process trees; sandbox processes are terminated as guest process groups.

Adapter stderr and hook output go to **Output**. Program stdout/stderr and REPL results go to **Debug Console**. Telemetry never enters the Debug Console. The Output panel can opt an active session into a bounded DAP trace; expression values, variables, memory/source content, output, environments, authorization, and common secret/token fields are redacted.
