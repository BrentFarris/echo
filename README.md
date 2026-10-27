# Echo

The chat-first Echo application now lives at the repository root. Echo, named bots, model streaming, scoped file operations and deterministic bot coordination form the core. Features are installed plugins. The previous application is kept temporarily in the Git-ignored `OLD/` folder for reference; its source, scripts, dependencies and local files are archived there.

## Run on Windows

Run `echo.exe`, then open **http://localhost:3740**. The app is named Echo; data continues to use `%LOCALAPPDATA%\Echo2` and its existing database and credential identifiers, preserving your bots, chats, model settings and protected credentials without a migration. Stop the console with Ctrl+C to flush active streams and terminate child processes. Only one server may open a data directory.

```powershell
.\echo.exe
# Optional separate test profile:
.\echo.exe -port 3740 -data C:\path\to\echo-data
```

Connect an existing compatible model server in **Settings → Local Models**, using its API base URL, for example `http://127.0.0.1:1234/v1`. Echo does not download or launch models. Model Settings contains connection/model selection, thinking and temperature; each send snapshots the selection for all recipients. Automatic context compaction can be enabled per connection and never deletes stored history.

### Install Echo on your desktop

Open **http://localhost:3740** in Edge or Chrome and choose **Install Echo** in the sidebar when offered, or use the browser's app installation option. Echo opens in its own desktop window with the artwork from `echo.png` as its app icon and favicon. Keep using the same address and port for the installed app. Remote installations require HTTPS; local `localhost` and `127.0.0.1` addresses work over HTTP.

Keep `echo.exe` running when using the installed app. If the server is unreachable, Echo shows a reconnect page. Only that public page is cached for offline use; chats, credentials, attachments and API responses are not cached by the service worker. App code is fetched from the server on each launch so builds and relaunches load the current version.

The icon files in `web/public/icons` are generated from the repository's `echo.png`. After replacing the source image, run `powershell -File .\scripts\generate-icons.ps1` and rebuild.

The composer follows the original Echo layout: compact controls, upward-opening menus, a single paper-plane/stop icon, and a text area that grows until the composer occupies half the chat height. Enter sends; Shift+Enter inserts a new line. Use **+ → Image / Video / File**, paste an image from the clipboard, or drop files onto the composer. Attachment cards show previews, filenames and sizes; click a preview to enlarge it, or remove it before sending. Draft attachments stay with their chat while switching conversations.

Uploads support PNG, JPEG, GIF, WebP, MP4, WebM, MOV, PDF, and UTF-8 text/code files. Limits are eight files, 32 MiB per file, 64 MiB per message, and 1 MiB for text. Images/video/PDFs are sent using the compatible provider's multimodal content format; the selected model must support that format. Unsupported media capabilities return the provider's error. Text files are included as text. Accepted attachments persist once as compressed blobs, survive reload/restart, and are deleted with their chat. Unsent uploaded files expire after 24 hours when uploads next run. Custom WASI providers retain their 4 MiB input limit.

The endpoint editor includes temperature defaults, Top K/P, Min P, context length, compression threshold, max output tokens, frequency/presence/repetition penalties, reasoning effort, thinking-token budget, and request/stream idle timeouts. Blank optional sampling fields are omitted from requests. Named reasoning effort overrides the local `thinking_token_budget`; unsupported settings are never silently downgraded. A stream idle timeout of `-1` disables the inactivity limit; arriving data and SSE heartbeats reset it. The request timeout remains an overall limit.

Add a compatible cloud endpoint in **Settings → Cloud Models**, then select the connection and model in the chat just like a local model. No global or per-bot enable switch is required; the chat's selection applies to every recipient. Enter API keys in its password field; keys are stored in Windows Credential Manager. Changing the destination clears the existing credential binding unless a replacement key is supplied.

Installed model providers appear in the connection editor's Provider field. The `provider` authoring template delegates to the streaming client; other protocols can use bounded, endpoint-confined host requests without receiving credentials. The `renderer` template formats assistant replies inside an isolated frame. Both extension points support per-bot toggles.

## Work through chat

- Ask Echo to create, list, configure or delete bots. Unmentioned messages go to Echo even inside a named-bot chat.
- **Settings → Bots → Edit → Enable Multitasking** lets a bot work in several chats simultaneously. It defaults to off for new and existing bots, including Echo; requests then queue in arrival order. You can also ask “Create a bot named kaiju with multitasking enabled” or ask Echo to change an existing bot’s setting. Available bots in a group request start immediately while busy bots wait. A bot stays reserved until the whole request finishes, including handoffs. Changing only this checkbox preserves active work: enabling releases queued requests; disabling waits for already active requests to finish. Stop cancels queued work, and unfinished requests are marked interrupted after a server restart.
- Mention `@kaiju` anywhere to address that bot. Mention several bots for independent work or explicit handoffs. Unknown handles are rejected before execution.
- Type `#` to choose files from the recipients' accessible folders. Suggestions label which bots can access each file. Both references and permissions are checked again on submission.
- Ask a bot to schedule work: “Every day at 3pm, have @kaiju report how many tests pass.” Schedules retain the prompt, model settings and resolved timezone and survive deletion of the source chat.
- Ask Echo to create a draggable calculator or another plugin. UI packages need no compiler; Go/WASI backends need an installed Go toolchain. `plugin_template` supplies working source and the bridge contract.

The sidebar shows every chat once under its accumulated named-bot combination. Echo management replies do not add another group. Stop or delete a chat to cancel its entire dispatch, including handoffs and native child processes. Deleting a bot removes its schedules and stops its work; historical author names remain.

When a bot finishes its final reply, its progress messages, reasoning, handoffs and tool activity fold into **Show work** above that reply. Each bot has its own group in a shared chat. Expand it to inspect the complete activity; the stored transcript is unchanged. Running, failed and interrupted work stays visible. Chat cards use compact spacing, omit routine completion badges, and show tool names once.

**Files changed** appears below each finished response, outside Show work. It lists Created, Edited and Deleted files, combines repeated edits and omits changes that were fully reverted. Click a row (or focus it and press Enter) to reveal the file in Windows File Explorer. Removed files open their nearest existing containing folder within the bot's current workspace. The action requires a local connection to Echo on that Windows computer; remote browsers receive an explanation. Existing file permissions are checked again when clicking, including after workspace changes.

File tools and plugin file capabilities record changes directly. Shell and Git/Fossil/P4 commands compare the bot's workspace folders before and after execution; unbound bots compare the command's working directory. Scans skip source-control metadata, dependency/cache folders, links and Echo's private state, and are limited to 20,000 entries and two seconds per snapshot. Small files use content fingerprints; files over 1 MiB use size, modification time and permissions. An incomplete scan is labeled. Failed or stopped work still lists observed changes. The compact metadata persists with the transcript and is removed with its chat; pre-existing transcripts are not retrospectively scanned.

## Plugins and permissions

Each installed plugin has a settings section. Bot settings include applicable per-bot switches. Web Get, SearXNG, Markdown View, Calendar, Workspaces, Shell, Git, Fossil, P4 (Perforce), Hosted Server and Plugin Authoring start enabled. Local and cloud model connections are core settings, not plugins. SearXNG requires the URL of an existing instance. Web Get allows public HTTP(S) by default; private destinations require exact origin grants in its settings.

Backend plugins run in WASI with no ambient filesystem, environment, network or process access. New capabilities require a direct owner response. Approve the reviewed package in settings, or send `/approve plugin-id digest` using the digest returned by installation. Updates keep the previous working package for rollback. Generated packages never run installation scripts.

Floating views have opaque iframe origins and cannot read Echo's DOM or cookies. Echo owns window dragging, resizing, placement and theme delivery. All renderer output is sanitized before display. Disabling Markdown View restores plain text unless another renderer is enabled for that bot.

Hosted Server is installed but remote listening is inactive. Set a password of at least 12 bytes, explicitly enable remote access, and restart. Optional certificate/key paths enable HTTPS. Remote APIs and WebSockets require an authenticated session. Successful sign-in is remembered with a protected browser cookie for 30 days, renewed each time you launch Echo in that browser; the password itself is not saved in browser storage. **Settings → Hosted Server → Connected devices** lists saved browser sessions, addresses, sign-in/last-active times, and live connection status. Use **Close session** to sign out a browser and disconnect its live connections. Password changes revoke all other sessions. Local access on the server can still sign in automatically.

## Git, Fossil and Perforce

These built-ins run the installed native CLIs through the same process broker as Shell, with independent per-bot switches. Each exposes `*_command`, `*_status`, and `*_diff`. Command tools take a subcommand, an array of literal arguments, a working directory, optional standard input, and a timeout of up to 300 seconds. They stream output and cancel with the chat; Shell need not be enabled. For example, ask “@kaiju show the Git diff,” “@kaiju commit the Fossil changes with message …,” or “@backend open shield.cpp for edit in Perforce.”

Install `git.exe`, `fossil.exe`, or `p4.exe` on PATH, or choose its absolute executable in that plugin's settings. Use a dedicated CLI folder; its runtime folder receives read/execute access. Additional runtime folders can be granted in settings. Set Git author name/email or Fossil user there. Fossil's repository database and checkout must both be within the bot's folders. Editors and interactive prompts are unavailable; supply commit messages and Perforce `-i` forms explicitly. Read [Git's command reference](https://git-scm.com/docs/git) and [Fossil's environment documentation](https://fossil-scm.org/home/doc/004c5507/www/env-opts.md) for native behavior.

P4 settings include server, user, client and optional charset/trust file. Save a ticket or password through the separate protected field after saving the server/user. Echo stores it in Windows Credential Manager, injects it only into P4's process environment, and redacts it from streaming/final output. Changing the server/user clears the saved credential. P4's `login`, `tickets`, `passwd`, and `set` commands are excluded to keep authentication out of chat; obtain a ticket outside Echo, then save it in settings. The plugin supports workspace operations against the configured server; DVCS/bootstrap commands and shell transports are excluded so they cannot redirect the saved credential. SSL trust files must be within the bot's folders. See [P4 global options](https://help.perforce.com/helix-core/server-apps/cmdref/current/Content/CmdRef/global.options.html) and [ticket authentication](https://help.perforce.com/helix-core/server-apps/cmdref/current/Content/CmdRef/P4PASSWD.html).

Workspace-bound source-control commands start without network access. Grant Internet and/or private-network access directly in the relevant plugin's settings for remote operations. These grants include native descendants; the private-network Windows capability permits both connections and listeners. Filesystem restrictions remain enforced. Chat tools cannot enlarge these grants. Windows loopback isolation can still prevent bound bots from reaching a server on the same machine; Echo does not add machine-wide exemptions or fall back to an unconfined process. Unbound bots retain their unrestricted host scope. [Microsoft's AppContainer isolation reference](https://learn.microsoft.com/en-us/windows/win32/secauthz/appcontainer-isolation) describes these OS boundaries.

## Native workspace restrictions

Unbound bots have the requested unrestricted host scope. Binding folders confines filesystem tools through `os.Root` and native commands through a separate Windows AppContainer identity. Workspaces being disabled never removes those restrictions. Shell commands and descendants belong to a cancellable Job Object. Echo journals its own ACL entries and cleans them after execution or at startup after a crash.

Installed Go, Git, Node and .NET toolchain directories are detected once and displayed in Shell settings as read/execute grants. Toolchains that cannot admit an AppContainer may use a read-only private copy, limited to 512 MiB. Build dependencies must already be present: scoped Shell processes have no network capability. Source-control plugins can receive the explicit network grants described above. Unsupported configurations return errors and never fall back to unrestricted execution.

**Compatibility:** the Windows acceptance suite exercises PowerShell, Git init/add/commit/status with **reftable**, a Go build/run, a Node program and a .NET SDK/MSBuild build/run. Scoped commands default new Git repositories to reftable and configure Node's loader to preserve symlinks; Windows still enforces target access. Git's older loose-ref backend and programs that demand metadata access to denied ancestor folders can fail in this sandbox. Existing repositories are never converted automatically. Validate each project's toolchain before relying on it for unattended work.

## Calendar and downtime

Supported recurrences are `daily HH:MM`, `weekly MON HH:MM`, `every N minutes/hours/days`, and an RFC3339 one-time instant. Daily/weekly schedules preserve local wall time through DST; a nonexistent wall time is skipped and an ambiguous wall time runs once. The server's Windows timezone is resolved to an IANA name when saving.

After downtime, every missed occurrence awaits review. Select jobs, set concurrency (default `1`; `0` means unlimited), then choose **Run selected**. Unselected jobs are skipped. **Skip all** dismisses the entire pending batch. Equivalent chat commands are `/missed run <concurrency> <occurrence IDs>` and `/missed skip-all`. Reviews survive restart; intentionally paused/disabled schedules do not accumulate missed work. Interrupted work is identified and never automatically replays uncertain tools.

## Development settings

**Settings → Development** lets you develop Echo using Echo. Save the absolute path to the new Echo Git checkout on the server computer; a valid checkout beside the executable or at its working directory is suggested automatically. Git worktrees are supported. These controls are available to local owners and authenticated Hosted Server users.

**Check for Updates** fetches the checked-out branch’s configured upstream and reports whether it is up to date, ahead, behind, or diverged. A tracking branch is required: Echo does not choose a remote or switch branches. When behind, **Update & Relaunch** fast-forwards the upstream and builds. Conflicting edits and diverged histories stop the update; Echo never resets, stashes, or discards your edits.

**Build & Relaunch** builds the current checkout (including local edits), its plugin runtime, and frontend, then launches the repository’s `echo.exe`. It requires Git, PowerShell, Go, Node.js, and npm on the server. **Relaunch** restarts the current executable without building; **Terminate Echo** stops it without restarting. Neither requires a source checkout. Restarting and stopping interrupt active work. Echo remains available during builds and closes gracefully only after a successful build and launcher preparation. The requesting browser waits for a replacement process and reloads the same tab.

The panel shows progress and the latest 16 KiB of operation output. Full logs live under the existing data directory’s `development/<operation-id>/` folder, alongside launcher diagnostics and replacement-process stdout/stderr. If a build fails after an update, the checkout stays updated and the previous Echo process stays running. If a restart fails, inspect those logs and start Echo manually. Operations are serialized across browsers; checks run only when requested.

## Build and validate

Requires Go 1.26+, Node.js compatible with Vite 8, and npm. Windows release acceptance additionally requires Git with reftable support, Fossil, Node and a .NET SDK on PATH, plus Perforce client/server binaries. Put `p4.exe` and `p4d.exe` on PATH or set `ECHO2_TEST_P4` and `ECHO2_TEST_P4D` to portable copies. Perforce tests start a temporary loopback-only depot and remove it afterward; no real depot is modified. These external CLIs are not bundled into the Echo ZIP.

```powershell
.\build.ps1                         # Build echo.exe from the repository root
.\build.ps1 -OutputPath .\staged.exe # Build to an alternate path
.\build.ps1 -Test -BrowserTests     # Go, type checking, unit tests, Playwright
.\build.ps1 -Release                # All checks, required native tests, release ZIP
.\scripts\test-development.ps1      # Isolated real build/relaunch/terminate acceptance
```

The browser tests use deterministic providers and isolated test data on ports 3742/3743. They do not contact a cloud model or use your application data. For frontend iteration, run the backend on 3740 and `npm run dev` from `web` (Vite proxies `/api`). Run build and test commands from this repository root; the release ZIP is `echo-windows-amd64.zip` on Windows AMD64. `OLD/` is a local reference archive and is not included in builds, tests or release packages. See [architecture](ARCHITECTURE.md) and [acceptance](ACCEPTANCE.md).
