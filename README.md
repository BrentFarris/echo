# Echo

The chat-first Echo application now lives at the repository root. Echo, named bots, model streaming, scoped file operations and deterministic bot coordination form the core. Features are installed plugins. The previous application is kept temporarily in the Git-ignored `OLD/` folder for reference; its source, scripts, dependencies and local files are archived there.

Open **Apps → Monaco Editor** for the built-in code editor. Choose a bot above the explorer to browse its configured folders; each bot keeps separate tabs, pins, drafts and undo history. A single click opens an italic preview, double-click keeps it open, and pinning protects it from closing. **Open in Window** and **Open Embedded** switch any app's presentation while retaining its contents. **Back to chat** hides the editor; **Close** asks about unsaved work. Editor Settings stores preferences in the Monaco plugin configuration. The editor includes bundled language services, search/replace, bookmarks, outline, Markdown and media previews, with inline AI chat; the old side chat, debugger and external-language-server integrations remain absent. The Terminal plugin supplies the persistent bottom dock across chat and apps.

Press **Ctrl+I** (**Cmd+I** on macOS) in an editable file or untitled buffer to open inline chat at your cursor or selection. The bot selected above the explorer answers and proposes changes using its own instructions and folders. Attach read-only file/folder context with `@`, ask follow-up questions, and review the original red/green inline previews. The checkmark accepts one undoable change into the editor buffer; **Ctrl+S** saves it. **X** discards the proposal, and **Stop** ends generation while retaining the last complete preview. Editing or changing the file invalidates an old proposal; use **Regenerate from current file** after resolving any disk conflict.

Inline chat uses the same model connection, model identifier, thinking, and temperature controls as the main chat. Editor choices are remembered separately per bot and machine in this browser, initially copied from the current chat's choices for that machine (or its first configured connection). Remote editors use their owning machine's connections. Inline conversations stay with their open tabs across bot switches and embedded/window presentation changes, and are cleared on acceptance, rejection, tab/editor closure, or reload. They do not create sidebar chats. Bot multitasking and admission rules also apply to inline requests.

## Run on Windows

Run `echo.exe`. Echo opens the installed desktop app when available, falling back to **http://localhost:3740** in your browser. This happens only on the initial launch; Development reloads, rebuilds and updates keep the existing window. Use `-no-open` or `ECHO_NO_OPEN=1` for headless startup. The app is named Echo; data continues to use `%LOCALAPPDATA%\Echo2` and its existing database and credential identifiers. Additive, versioned storage migrations preserve existing bots, chats, model settings and credentials. Stop the console with Ctrl+C to flush active streams and terminate child processes. Only one server may open a data directory.

```powershell
.\echo.exe
# Optional separate test profile:
.\echo.exe -port 3740 -data C:\path\to\echo-data
```

## Terminal

The built-in **Terminal** plugin restores the original bottom dock and starts enabled. Expand its compact bar from any main view, use **Apps → Terminal**, or press **Ctrl+`** (**Cmd+`** on macOS). Drag its upper edge or use the focused separator's arrow keys to resize; maximize/restore and collapse keep the shell alive. **Kill** ends it; **Restart**, or Enter after exit, starts another shell.

Choose a bot in the bar to switch between independent sessions and saved-command lists. Remote bots show their machine name and require that machine's `native-terminal/1` capability. A **Change folder** dropdown appears only for bots with multiple folders. It sends a safely quoted directory-change command to the existing shell, just like typing it; use it at a shell prompt. The shortcut does not track manually entered directory changes. New sessions start in the first available configured folder, or the server user's home if the bot has no folders.

**Saved** adds, edits, deletes and runs named commands in their saved order. Commands persist on the owning server across restarts; no old Echo data is imported. Ctrl+C copies highlighted terminal text and clears the selection after success; without a selection it sends an interrupt. Ctrl+V pastes through xterm, retaining bracketed-paste behavior. Native clipboard events and a copy fallback support browsers without the clipboard API. The terminal retains clickable links, 5,000 lines of scrollback and light/dark colors.

These are owner-operated shells with the normal permissions of the Echo server user. Bot folder settings supply starting directories and shortcuts, not containment for the Terminal plugin. AI shell tools retain their separate restrictions. Browsers viewing the same bot share its server-owned shell. Collapse, navigation, browser closure and temporary network loss leave it running; reconnect restores buffered output without replaying input. Plugin disablement, bot deletion and server shutdown stop affected processes. Dock preferences are browser-local; processes and the bounded output replay do not survive a server restart.

## Connect machines with Swarm

Swarm is a built-in, app-scoped plugin and starts inactive. On each remote machine, run Echo and configure **Settings → Hosted Server** with a reachable listening address and owner password. Use HTTPS when the connection crosses an untrusted network: the initial URL/password pairing uses the chosen HTTP or HTTPS origin.

Open **Settings → Swarm** on the machine serving your browser. Enter the remote Echo origin, its owner password, and an optional shared machine name such as `linux`. Explicit connection enables the installed Swarm plugin on both ends, creates reciprocal access, and remembers it across restarts. The password is required even for a loopback URL. Edit the advertised name in that machine's Swarm settings. Names use lowercase letters, digits, underscores and hyphens; duplicate names gain a stable node-ID suffix. Saved references survive renaming.

Use one composer to address bots on any authorized machine:

```text
@kaiju @linux.kaiju @mac.kaiju pull the latest and ensure the engine is still building
@linux.echo Create a kaiju bot that works in the workspace /path/to/workspace
```

Bare handles always refer to the machine serving your browser, including when you open a remote chat. A remote Echo manages only its own bots and folders. Every worker uses its own model connection, credentials, plugins, tools and filesystem permissions. Use the machine selector in Model Settings to override each machine's model for a shared chat. A remote-only request does not require a model connection on the entry machine. Bot handoffs can address only participants selected in the current user message.

The combined sidebar includes existing remote chats and machine chips, with an optional machine filter. Each chat retains one home machine; new chats belong to the entry machine. Settings has an explicit machine selector. Apps, dock entries and Monaco windows stay bound to their owning machine, including their editor buffers, recovery drafts, bookmarks and file subscriptions. Remote Development controls restart and reconnect their selected machine without reloading the entire interface. File references retain their owning machine; attach a file explicitly when it needs to be transferred. Native file reveal remains restricted to a local connection on the file's host.

Remote inventory refreshes in the background, so an offline or slow machine does not delay local chat actions. Cached bots and chats remain visible with an Offline label. Offline bots cannot be selected or sent work; a message mentioning both available and offline bots is rejected before any bot starts, preserving the draft. Reconnection happens automatically in the background. Previously opened remote transcripts remain readable while their machine is offline.

Swarm discovers topology through connected peers, with direct links preferred and encrypted relay transport when necessary. Peers with matching passwords can authenticate automatically. A different-password destination requires its own password from each entry point: access through an intermediate machine does not grant access to that destination. No network scan is performed. **Disconnect** retains credentials but prevents automatic reconnection; **Reconnect** reverses it. **Revoke access**, a password change, or disabling Swarm invalidates or suspends access. Automatic discovery cannot reactivate disabled or revoked connections; explicit pairing can.

Reachable workers continue when another worker disconnects. Running workers can finish if their chat home disappears; results reconcile when it returns. Opened transcripts remain readable from the entry machine's cache, while new messages require the home to be reachable. There is no ownership failover. Accepted execution is durable and retries do not rerun tools; a worker crash leaves interrupted work. Stop and deletion persist pending cancellation during outages, and deletion tombstones prevent late results from recreating history. Mixed-machine work reserves its bots across hosts before starting; busy reservations are released and retried without holding a partial set indefinitely.

## Run on Linux or macOS

Build with `sh build.sh` or use the native `echo` executable, then run `./echo -port 3740`. The default profile is `$XDG_CONFIG_HOME/Echo2` (normally `~/.config/Echo2`) on Linux and `~/Library/Application Support/Echo2` on macOS. Override it with `-data /absolute/profile/path`. SIGTERM and Ctrl+C shut down gracefully.

Linux native tools require [bubblewrap](https://github.com/containers/bubblewrap) and working user namespaces. On Debian/Ubuntu, install the `bubblewrap` package. Distribution policies may restrict unprivileged user namespaces; Echo reports containment failures instead of running unconfined. macOS uses the system Seatbelt launcher, `/usr/bin/sandbox-exec`; absence or policy failure is explicit. Native macOS acceptance must be run on an actual Mac before distributing a macOS release; cross-compilation alone is insufficient.

Windows uses Credential Manager. Unix stores credentials in an unattended per-profile AES-GCM vault: owner-only directories/files, atomic writes, and a protected encryption key in the profile. Ordinary restarts need no interactive unlock or repeated peer passwords. Keep the complete profile protected and backed up together; copying only its encrypted credential files cannot recover them.

Background startup is optional and never installed by building or pairing:

```powershell
# Windows: explicitly opt into startup at your login.
.\scripts\startup.ps1 -Install -Executable C:\path\to\echo.exe
# Remove that login task:
.\scripts\startup.ps1 -Uninstall
```

```sh
# Linux user systemd service / macOS user LaunchAgent:
sh scripts/startup.sh --install /absolute/path/to/echo /absolute/profile/path 3740
```

The service definitions contain no owner password. Linux startup before login additionally requires user lingering configured by the system administrator. Manage the installed service with `systemctl --user`, or the macOS LaunchAgent with `launchctl`.

## Model connections

Connect an existing compatible model server in **Settings → Local Models**, using its API base URL, for example `http://127.0.0.1:1234/v1`. Echo does not download or launch models. Model Settings contains connection/model selection, thinking and temperature; each send snapshots the selection for recipients on that machine. Proactive context compaction can be enabled per connection and never deletes stored history. Both regular and inline editor chats use provider-reported input tokens when available, estimating newly added context until the next response. Context-overflow recovery remains enabled even when proactive compression is off: Echo summarizes older context and retries the model request without rerunning tools. A tool response that cannot fit in minimal context is replaced with a model-facing notice to request smaller output; the original tool output and attachments remain in the regular chat transcript.

### Install Echo on your desktop

Open **http://localhost:3740** in Edge or Chrome and choose **Install Echo** in the sidebar when offered, or use the browser's app installation option. Echo opens in its own desktop window with the artwork from `echo.png` as its app icon and favicon. Keep using the same address and port for the installed app. Remote installations require HTTPS; local `localhost` and `127.0.0.1` addresses work over HTTP.

Keep `echo.exe` running when using the installed app. If the server is unreachable, Echo shows a reconnect page. Only that public page is cached for offline use; chats, credentials, attachments and API responses are not cached by the service worker. App code is fetched from the server on each launch so builds and relaunches load the current version.

The icon files in `web/public/icons` are generated from the repository's `echo.png`. After replacing the source image, run `powershell -File .\scripts\generate-icons.ps1` and rebuild.

### Chat notifications and sounds

Open **Settings → Notifications** on each browser or installed Echo app. Completion notifications, question notifications, and their two original sounds have independent controls. **Enable notifications on this device** requests browser permission and registers background push; enabling one device does not enable another. Use the notification and sound test buttons to check the setup.

Each bot's successful final reply produces its own completion alert, even while other bots in the group are working. Structured questions alert immediately. Clicking an alert opens its chat, selects the bot tab, and reveals the reply or question. When you are already viewing that conversation, Echo plays the sound without a popup. Elsewhere in Echo it shows a clickable toast; in the background it uses system notifications. Failed, stopped, interrupted, and intermediate tool responses do not produce completion alerts.

Mobile background delivery requires HTTPS, notification permission, internet access, and an Echo server that remains running. On iPhone/iPad, add Echo to the Home Screen and enable notifications inside that installed app. Android supports push through compatible browsers/installations. A plain HTTP LAN address does not support push; HTTP localhost works on the computer hosting Echo. The original WAV sounds play when a running page can play audio. Closed or suspended apps use the operating system's notification sound and notification settings; websites cannot select those custom WAV sounds for a locked phone.

Each enabled device receives its own alerts, including remote Swarm chats while no browser is open. The entry server must remain running and connected to the authorized chat-home machine. Delivery retries temporary push-service failures, expires after 24 hours, and stops for revoked or expired device sessions. Reopening Echo refreshes its subscription. A push already accepted by the platform may still appear after you return to the chat. Notification previews include the bot/chat identity and a short reply or question excerpt.

The notification journal, device registrations, and delivery state use an additive database migration. VAPID signing keys remain in the profile's credential store. Existing completed history is not replayed on upgrade. The service worker still caches only the public offline page; its notification deduplication database stores alert IDs/timestamps, not transcripts.

The composer follows the original Echo layout: compact controls, upward-opening menus, a single paper-plane/stop icon, and a text area that grows until the composer occupies half the chat height. Enter sends; Shift+Enter inserts a new line. Use **+ → Image / Video / File**, paste an image from the clipboard, or drop files onto the composer. Attachment cards show previews, filenames and sizes; click a preview to enlarge it, or remove it before sending. Draft attachments stay with their chat while switching conversations.

Uploads support PNG, JPEG, GIF, WebP, MP4, WebM, MOV, PDF, and UTF-8 text/code files. Limits are eight files, 32 MiB per file, 64 MiB per message, and 1 MiB for text. Images/video/PDFs are sent using the compatible provider's multimodal content format; the selected model must support that format. Unsupported media capabilities return the provider's error. Text files are included as text. Accepted attachments persist once as compressed blobs, survive reload/restart, and are deleted with their chat. Unsent uploaded files expire after 24 hours when uploads next run. Custom WASI providers retain their 4 MiB input limit.

The endpoint editor includes temperature defaults, Top K/P, Min P, context length, compression threshold, max output tokens, frequency/presence/repetition penalties, reasoning effort, thinking-token budget, and request/stream idle timeouts. Blank optional sampling fields are omitted from requests. Named reasoning effort overrides the local `thinking_token_budget`; unsupported settings are never silently downgraded. A stream idle timeout of `-1` disables the inactivity limit; arriving data and SSE heartbeats reset it. The request timeout remains an overall limit.

Add a compatible cloud endpoint in **Settings → Cloud Models**, then select the connection and model in the chat just like a local model. No global or per-bot enable switch is required; the chat's selection applies to recipients on that machine. Enter API keys in its password field; keys use Windows Credential Manager or the Unix profile vault. Changing the destination clears the existing credential binding unless a replacement key is supplied.

Installed model providers appear in the connection editor's Provider field. The `provider` authoring template delegates to the streaming client; other protocols can use bounded, endpoint-confined host requests without receiving credentials. The `renderer` template formats assistant replies inside an isolated frame. Both extension points support per-bot toggles.

Named bots receive the installed plugin defaults when created. Plugins added later stay off for those bots until selected in **Settings → Bots → Edit → Plugins**. Upgrades preserve selections for previously installed plugins. Echo automatically receives new plugins and generated apps unless explicitly disabled for Echo; app-scoped plugins use their global settings.

## Work through chat

Completed agent responses show a small footer with elapsed time, provider-reported input/output tokens, tool calls, and model calls across the run. **Settings → General → Show turn statistics** controls the footer for this browser and defaults on. Token tracking continues when the footer is hidden.

General also shows combined input/output totals for online machines, including chat, inline editing, and context summaries. Hover, focus, or tap a total to see exact counts and the machine breakdown without moving the layout. Missing usage is marked partial; tracking starts fresh when this feature is installed. **Settings → Development → Reset token totals…** asks for confirmation and resets reachable machines, preserving historical chat statistics. Offline machines keep their counts and contribute them again when reconnected.

Chats have no turn limit, and bots can ask clarifying questions as often as needed. Model/tool rounds and bot-to-bot questions and handoffs have no count limit. Use Stop to end active work.

- Ask Echo to create, list, configure or delete bots. Unmentioned messages go to Echo even inside a named-bot chat.
- **Settings → Bots → Edit → Enable Multitasking** lets a bot work in several chats simultaneously. It defaults to off for new and existing bots, including Echo; requests then queue in arrival order. You can also ask “Create a bot named kaiju with multitasking enabled” or ask Echo to change an existing bot’s setting. Available bots in a group request start immediately while busy bots wait. A bot stays reserved until the whole request finishes, including handoffs. Changing only this checkbox preserves active work: enabling releases queued requests; disabling waits for already active requests to finish. Stop cancels queued work, and unfinished requests are marked interrupted after a server restart.
- Mention `@kaiju` anywhere to address that bot. Mention several bots for independent work or explicit handoffs. Unknown handles are rejected before execution.
- Type `#` to choose files from the recipients' accessible folders. Suggestions label which bots can access each file. Both references and permissions are checked again on submission.
- Ask a bot to schedule work: “Every day at 3pm, have @kaiju report how many tests pass.” Schedules retain the prompt, model settings and resolved timezone and survive deletion of the source chat.
- Custom plugins can request HTTP APIs, remote assets and WebSockets. **Settings → Plugins → the plugin → Network access** lists exact destinations and highlights local/private addresses. Approve new destinations before activation; each origin grant covers all paths, including private-network addresses. API calls and WebSockets connect from the plugin’s Echo machine; assets load from your viewing device. Older custom plugins using `web.get` need an updated manifest with `network.origins` before their network calls work.
- Ask Echo to create a draggable calculator or another plugin. UI packages need no compiler; Go/WASI backends need an installed Go toolchain. `plugin_template` supplies working source and the bridge contract.

The sidebar shows every chat once under its accumulated named-bot combination. Echo management replies do not add another group. Stop or delete a chat to cancel its entire dispatch, including handoffs and native child processes. Deleting a bot removes its schedules and stops its work; historical author names remain.

When a bot finishes its final reply, its progress messages, reasoning, handoffs and tool activity fold into **Show work** above that reply. Each bot has its own group in a shared chat. Expand it to inspect the complete activity; the stored transcript is unchanged. Running, failed and interrupted work stays visible. Chat cards use compact spacing, omit routine completion badges, and show tool names once.

Hover over a finished message, focus it with the keyboard, or tap it on touch screens to reveal **Copy, Fork, Edit, Rerun, Delete** in its top-right corner. Copy preserves raw Markdown; a grouped response copies only its final answer. Actions are unavailable while the chat is running.

**Rerun** uses the circular refresh icon. Confirming it resends the original user prompt with its uploads, file references, recipients, and recorded model settings, replacing its old response and all later messages across bot tabs. On a bot response, it reruns that response's original user message; the button is disabled if that user message was deleted. Your unsent composer draft is kept. File access is checked again, and workspace changes made by earlier tools are not undone.

**Fork** opens an independent chat named `<original title> (fork)`, containing the whole chat in stored order through the selected message, including other bots and hidden work. A user-message fork ends before its answer. The source and its unsent draft are kept, and no response starts automatically. Forks inherit saved model settings and keep their own copies of uploads and generated media, so deleting the source does not break them. If required remote media cannot be copied, no partial fork is created; retry when that machine is available. Workspace files are not copied or reverted. Remote follow-ups require workers supporting `fork-history/1`; update older peers before continuing a fork on them.

Editing an assistant response saves its text without generating a new answer. Editing a user message offers **Save & Resubmit**, which keeps its uploads, removes that message and everything after it, and starts a fresh response with the chat's current model settings. Use Ctrl/Cmd+Enter to save or Esc to cancel. Delete asks for confirmation and removes only the selected message (plus results belonging to its tool calls); deleting a user message keeps its responses. Changes appear in other connected browsers and in subsequent local and Swarm model context. Tool messages and items inside **Show work** have no action toolbar.

**Files changed** appears below each finished response, outside Show work. It lists Created, Edited and Deleted files, combines repeated edits and omits changes that were fully reverted. Click a row (or focus it and press Enter) to open the file in the built-in code editor under the bot that changed it. Removed files show their nearest existing containing folder in the editor's explorer. When Monaco Editor is disabled, rows fall back to the host native file manager; that fallback requires a local connection to Echo on that computer. Editor access uses the bot's configured workspace folders and also works from remote browsers. Existing file permissions are checked again when clicking, including after workspace changes.

File tools and plugin file capabilities record changes directly. Shell and Git/Fossil/P4 commands compare the bot's workspace folders before and after execution; unbound bots compare the command's working directory. Scans skip source-control metadata, dependency/cache folders, links and Echo's private state, and are limited to 20,000 entries and two seconds per snapshot. Small files use content fingerprints; files over 1 MiB use size, modification time and permissions. An incomplete scan is labeled. Failed or stopped work still lists observed changes. The compact metadata persists with the transcript and is removed with its chat; pre-existing transcripts are not retrospectively scanned.

## Plugins and permissions

**Apps**, beside Settings, lists plugins that explicitly register a GUI application. Click **Open** to launch its view (Terminal expands the bottom dock); opening the same app again focuses that window and preserves its contents. Apps opens in the main area, and selecting a chat returns to the conversation with its draft and attachments intact. A registered app is still the same plugin, with the same settings, permissions and enablement.

Use **Dock** on an app card to add its square icon above Apps and Settings. The dock scrolls horizontally with touch, the mouse wheel, or keyboard navigation. Dock choices and their order are shared across browsers connected to this Echo profile and survive server restarts. Disabled apps and apps awaiting permission remain in the catalog with a Settings shortcut; their dock icons return when enabled again. **Undock** also works on inactive entries. Window positions remain local to each browser.

Each installed plugin has a settings section. Bot settings include applicable per-bot switches. Web Get, SearXNG, Markdown View, Calendar, Workspaces, Shell, Git, Fossil, P4 (Perforce), Hosted Server and Plugin Authoring start enabled. Local and cloud model connections are core settings, not plugins. SearXNG requires the URL of an existing instance. Web Get allows public HTTP(S) by default; private destinations require exact origin grants in its settings.

Backend plugins run in WASI with no ambient filesystem, environment, network or process access. New capabilities require a direct owner response. Approve the reviewed package in settings, or send `/approve plugin-id digest` using the digest returned by installation. Updates keep the previous working package for rollback. Generated packages never run installation scripts.

Floating views have opaque iframe origins and cannot read Echo's DOM or cookies. Echo owns window dragging, resizing, placement and theme delivery. All renderer output is sanitized before display. Disabling Markdown View restores plain text unless another renderer is enabled for that bot.

Hosted Server is installed but remote listening is inactive. Set a password of at least 12 bytes, explicitly enable remote access, and restart. Optional certificate/key paths enable HTTPS. Remote APIs and WebSockets require an authenticated session. Successful sign-in is remembered with a protected browser cookie for 30 days, renewed each time you launch Echo in that browser; the password itself is not saved in browser storage. **Settings → Hosted Server → Connected devices** lists saved browser sessions, addresses, sign-in/last-active times, and live connection status. Use **Close session** to sign out a browser and disconnect its live connections. Password changes revoke all other sessions. Local access on the server can still sign in automatically.

## Confluence search and reading

Open **Settings → Confluence** and choose **Cloud** or **Data Center**. One connection is shared by the bots on that Echo machine; use the global plugin switch and each bot's Confluence switch to control access. For Cloud, save the site URL (with or without `/wiki`), account email and token type. For Data Center, save the full HTTP(S) base URL, including a context path such as `https://docs.example.com/confluence`.

Create a Cloud API token in [Atlassian account settings](https://id.atlassian.com/manage-profile/security/api-tokens), or a Data Center personal access token in your Confluence profile. Save the connection settings, enter the token in the protected field, then select **Test connection**. Classic Cloud tokens use the configured site; scoped tokens require the site's Cloud ID and use `https://api.atlassian.com/ex/confluence/{cloudId}/wiki`. See [Atlassian's scoped-token setup](https://support.atlassian.com/confluence/kb/scoped-api-tokens-in-confluence-cloud/). Data Center tokens use Bearer authentication. Echo keeps tokens in the host credential vault, and changing the connection clears the saved token. No password or token belongs in chat. Clear or replace it directly in settings.

For scoped Cloud tokens, grant `read:content-details:confluence` for [CQL search, user lookup](https://developer.atlassian.com/cloud/confluence/rest/v1/api-group-search/) and current-user identity, `read:content.metadata:confluence` for content ID/type lookup, `read:page:confluence` for pages and blog posts, `read:comment:confluence` for comments, and `read:attachment:confluence` for attachment metadata. The account also needs permission to view the corresponding content. Search can succeed even when an individual read lacks its required scope; the connection test verifies only CQL search.

Ask a bot: “Search Confluence for `type = page AND space = ENG`, then read the full onboarding page,” or “Find blog posts about the release and read their comments.” `confluence_search` accepts CQL and a result limit (default 25, maximum 100). Follow `nextContinuation` with the same query and limit. Search returns IDs, titles, links, excerpts and available space/version metadata. `confluence_read` accepts a numeric content ID and returns Markdown by default; use `format: "storage"` for the original Confluence markup. Comments can be found through CQL, for example `type = comment AND container = 12345`.

Long bodies are delivered in chunks of up to 24,000 Unicode characters. Continue with the same ID and format until `complete` is true. Continuations are bound to the connection and content version; if the page changes, restart the read. Upstream responses are limited to 16 MiB. Markdown conversion preserves common formatting and code, marks unsupported macros and retains their body text; it does not execute macros or fetch external resources. Attachments return metadata and links only. Editing, file downloads, indexing and synchronization are not provided.

Confluence access uses the existing WASI capability bridge with credentials injected only by the host. The configured endpoint is the network grant, including private-network Data Center servers. HTTP redirects and pagination links outside the configured search endpoint are rejected; HTTPS uses normal certificate validation. Requests have a 30-second timeout and stop when the run is canceled, the plugin is disabled, or connection/token settings change. Errors report invalid CQL, missing permissions, unavailable content, rate limits (including `Retry-After`) and server failures without automatic retries. Swarm bots use their own machine's connection and credentials.

For author requests such as “Find the last three pages Brent Farris authored,” bots use `confluence_search_users` with a partial visible full/public name, then search `type = page AND creator = "<identifier>" ORDER BY created DESC` with `limit: 3`. Cloud returns `accountId`; Data Center returns `username`/`userKey`. Multiple matches remain separate candidates so the bot can ask which person you mean. User search has its own `nextContinuation`, bound to the name, limit and connection. Cloud uses `/rest/api/search/user`; Data Center uses user CQL through its general search endpoint.

`confluence_get_current_user` identifies the configured token account, which may be a service account rather than the person chatting. For that account's pages, use `creator = currentUser()` directly. Private profile fields remain absent when Confluence hides them. Cloud does not provide email-to-account-ID lookup or arbitrary legacy-username matching; try the person's visible name instead. Both tools reuse the saved token, per-bot switches, cancellation and destination restrictions; no additional connection settings are needed.

## ComfyUI images and video

**Settings → ComfyUI** connects an existing ComfyUI server, for example `http://127.0.0.1:8188`. Set its default checkpoint filename and optional text-to-image, image-to-image and video workflow JSON paths. Use workflows exported in **API format**. The plugin starts enabled; enable or disable it globally or for individual bots in **Settings → Bots → Edit**.

Ask a bot to generate an image, edit an attached image, or animate an image into a video. The plugin preserves the original `comfyui_generate`, `comfyui_generate_video`, `save_image` and `save_video` tools. Inline `workflowJSON` overrides `workflowPath`, which overrides the configured workflow. Image inputs select the image-to-image default; video requests select the dedicated video default. Images have a built-in checkpoint workflow, including image-to-image support. Video requires a configured or explicitly supplied workflow.

Workflow templates support `{{PROMPT}}`, `{{NEGATIVE_PROMPT}}`, `{{MODEL}}`, `{{IMAGE}}`, `{{WIDTH}}`, `{{HEIGHT}}`, `{{STEPS}}`, `{{CFG_SCALE}}`, `{{SEED}}`, `{{FRAMES}}`, `{{FPS}}`, `{{FORMAT}}`, `{{DURATION}}`, `{{ASPECT_RATIO}}` and `{{MEGAPIXELS}}`. Numeric placeholders become numbers. Defaults are 512×512, 20 steps, CFG 7.5, a random seed when omitted or negative, 16 frames, 8 FPS, MP4, 5 seconds, `16:9 (Widescreen)` and 0.4 megapixels. Explicit seed `0` is preserved. Duration/aspect ratio/megapixels work with workflows that compute their own dimensions and frame counts.

`imagePath` uploads a workspace image and takes precedence over `attachedImageIndex`, a zero-based index into the latest image-bearing user message addressed to the bot. Pasted and attached images upload directly from chat storage. Explicit and configured workflow paths, input-image paths, and save destinations all obey the bot's current folder permissions. Relative paths use its first bound root; unbound bots use absolute paths. Workflow JSON is limited to 500 KiB, input-image files to 100 MiB, and each generated artifact to 256 MiB. Composer uploads retain their existing 32 MiB limit.

Generated images, videos and animated GIFs stay visible outside **Show work**, with enlarged previews, video controls, and download links. The first output is downloaded into chat storage; the tool result lists all output paths. Results survive reload/restart and disappear when the chat is deleted. Images are supplied to the model for follow-up work; generated videos stay metadata-only in model context. Ask to save a result to a workspace using its `imageId` or `videoId`; existing files require `overwrite: true`, and saves appear in **Files changed**. Supported playback depends on the browser's codec support.

Only the owner settings can change the server URL, and HTTP redirects are rejected. ComfyUI tools run through the WASI plugin capability bridge. Stop, plugin disable, and scope revocation cancel Echo's HTTP requests and polling; they do not issue a global interrupt to the shared ComfyUI server. Generation waits up to five minutes and reports workflow execution errors. Echo does not install ComfyUI, models, or custom nodes. The old application's settings/data are not imported automatically; copy the five connection/workflow settings into this plugin.

## Jira

Open **Settings → Jira** on the intended machine, choose **Jira Cloud** or **Jira Data Center**, enter the Jira site base URL, and save settings. Cloud requires HTTPS and your Atlassian account email. For a scoped Cloud API token, also enter the Cloud ID; Echo sends API requests through `https://api.atlassian.com/ex/jira/{cloudId}`. Data Center uses a personal access token and supports installation paths such as `https://jira.example.com/jira`, including explicitly configured HTTP sites. TLS certificate verification remains enabled.

Save your API token/PAT in the protected field, use **Test connection** to verify the authenticated identity, and enable Jira. The plugin starts disabled. One connection is shared per Echo machine, and **Bots → Edit → Jira** controls each bot's access. Settings machine selection also applies to token saving and connection testing. Tokens use the host credential vault and are never provided to bots or plugin code. Changing deployment, URL, email or Cloud ID clears the saved token; unrelated enable/disable changes preserve it. Use **Clear credential** to remove it.

Ask a bot “Search Jira for `project = APP AND statusCategory != Done`” or “Read APP-123 and its full discussion.” The `jira_search`, `jira_get_issue`, and `jira_get_comments` tools read accessible standard/custom fields, descriptions, comments, issue links and attachment metadata. Cloud rich text retains its structure and includes readable text. Search/comment pages default to 50 items, with a maximum of 100. Full-ticket reads gather comments until complete or the 1 MiB result budget is reached; explicit continuation cursors let the bot retrieve the rest. Very large fields require selecting fewer fields; very large comment pages require a smaller page size. Permission failures and incomplete discussions are reported explicitly.

Jira access is read-only: ticket edits, posting comments, transitions, attachment downloads and complete change history are not exposed. Account permissions and token scopes determine visibility. Requests reject redirects and stop with the run or a plugin/settings change. Echo does not store a separate Jira index; retrieved information remains in the chat's normal tool history.

## Git, Fossil and Perforce

These built-ins run the installed native CLIs through the same process broker as Shell, with independent per-bot switches. Each exposes `*_command`, `*_status`, and `*_diff`. Command tools take a subcommand, an array of literal arguments, a working directory, optional standard input, and a timeout of up to 300 seconds. They stream output and cancel with the chat; Shell need not be enabled. For example, ask “@kaiju show the Git diff,” “@kaiju commit the Fossil changes with message …,” or “@backend open shield.cpp for edit in Perforce.”

Install `git`, `fossil`, or `p4` on PATH (`.exe` on Windows), or choose its absolute executable in that plugin's settings. Use a dedicated CLI folder; its runtime folder receives read/execute access. Additional runtime folders can be granted in settings. Set Git author name/email or Fossil user there. Fossil's repository database and checkout must both be within the bot's folders. Editors and interactive prompts are unavailable; supply commit messages and Perforce `-i` forms explicitly. Read [Git's command reference](https://git-scm.com/docs/git) and [Fossil's environment documentation](https://fossil-scm.org/home/doc/004c5507/www/env-opts.md) for native behavior.

P4 settings include server, user, client and optional charset/trust file. Save a ticket or password through the separate protected field after saving the server/user. Echo stores it in the host credential vault, injects it only into P4's process environment, and redacts it from streaming/final output. Changing the server/user clears the saved credential. P4's `login`, `tickets`, `passwd`, and `set` commands are excluded to keep authentication out of chat; obtain a ticket outside Echo, then save it in settings. The plugin supports workspace operations against the configured server; DVCS/bootstrap commands and shell transports are excluded so they cannot redirect the saved credential. SSL trust files must be within the bot's folders. See [P4 global options](https://help.perforce.com/helix-core/server-apps/cmdref/current/Content/CmdRef/global.options.html) and [ticket authentication](https://help.perforce.com/helix-core/server-apps/cmdref/current/Content/CmdRef/P4PASSWD.html).

Workspace-bound source-control commands start without network access. Grant Internet and/or private-network access directly in the relevant plugin's settings for remote operations. These grants include native descendants; the private-network Windows capability permits both connections and listeners. Filesystem restrictions remain enforced. Chat tools cannot enlarge these grants. Windows loopback isolation can still prevent bound bots from reaching a server on the same machine; Echo does not add machine-wide exemptions or fall back to an unconfined process. Unbound bots retain their unrestricted host scope. [Microsoft's AppContainer isolation reference](https://learn.microsoft.com/en-us/windows/win32/secauthz/appcontainer-isolation) describes these OS boundaries.

## Workspace skills

Reusable project guidance lives in `<workspace>/skills/<skill-name>/SKILL.md`. Each workspace owns its skills; bots with access to the same folder share them. Root ordering does not select a skill destination or give one workspace precedence. Skills are ordinary files that can be reviewed and committed with the repository, subject to its ignore rules.

At the start of a run, bound bots receive up to three relevant skill summaries from all their roots. They read full instructions with `workspace_skill_read` before using them and can use `workspace_skill_search` for further discovery. Search accepts an optional `workspace`, `query`, and `limit` (default 10, maximum 50); an empty query lists skills. Only immediate `skills/<name>/SKILL.md` entries are discovered. Scans inspect at most 256 entries per workspace and report malformed files, inaccessible directories, or truncation without blocking other roots. Changes made outside Echo are visible on the next search or read.

`workspace_skill_read` requires `workspace` and `name`, and returns metadata, body, absolute path, and a revision. `workspace_skill_save` requires `workspace`, `name`, `description`, and `body`, with optional `triggers` and `revision`. Omitting the revision means create-only; updating an existing file requires its current revision. Conflicting saves fail instead of silently replacing another bot's work. Saves appear in **Files changed**, and the existing workspace editor can open and edit the files.

Reads and saves require an explicit absolute workspace path. For bound bots, it must match a configured root. Unbound bots must also provide a workspace for searches; Echo does not scan their computer. Bots choose the destination from the task context and ask when it is ambiguous. Remote bots use folders on their own host. Existing scope restrictions and private-state protections still apply, including links that would escape the selected workspace.

For example, `skills/build-project/SKILL.md` could contain:

```markdown
---
name: build-project
description: Build and verify this project before release.
triggers:
  - release checks
---

Run the project's documented build and acceptance checks before packaging.
```

Names must match their directory and use lowercase kebab-case (at most 64 characters). Descriptions are required and limited to 500 characters. Optional triggers allow up to 12 nonempty phrases of 120 characters each. The Markdown body must be nonempty, and the complete UTF-8 file must not exceed 64 KiB. Supporting scripts, references, and assets may live alongside `SKILL.md` and are accessed through ordinary file tools.

Bots may save stable guidance useful across future tasks during authorized work. Skills are reference material, not permission grants: user instructions take precedence, and stale guidance must be checked against the current project. Secrets, temporary task status, and raw conversations should not be saved as skills.

Legacy `.echo/skills` directories are not read or migrated automatically. To retain an old skill, deliberately move its complete folder from `.echo/skills/<name>/` to `skills/<name>/`, review its contents, and resolve any existing destination before moving. No special Echo root or built-in plugin-authoring skill is required.

## Native workspace restrictions

Unbound bots have the requested host filesystem scope, except Echo's protected private profile. Binding folders confines filesystem tools through `os.Root` and native commands through the platform sandbox. Windows uses a separate AppContainer identity and cancellable Job Object; Echo journals its own ACL entries and cleans them after execution or at startup after a crash. Linux uses bubblewrap namespaces, and macOS uses Seatbelt. Workspaces being disabled never removes these restrictions.

Linux/macOS network access uses a filtered HTTP(S)/SOCKS proxy and checks the destination against the bot's Internet/private-network grants. Direct network connections remain denied. HTTP-based Git operations can use these proxy settings; tools and protocols that ignore them, including direct SSH or Perforce transports, cannot connect through this sandbox. Native tool compatibility must be checked on the destination OS.

Installed Go, Git, Node and .NET toolchain directories are detected once and displayed in Shell settings as read/execute grants. Toolchains that cannot admit an AppContainer may use a read-only private copy, limited to 512 MiB. Build dependencies must already be present: scoped Shell processes have no network capability. Source-control plugins can receive the explicit network grants described above. Unsupported configurations return errors and never fall back to unrestricted execution.

Echo's build scripts prepare Go dependencies in the checkout's `.cache/go-mod` and compiled packages in `.cache/go-build`. Scoped commands reuse these caches for the nearest `go.mod` when the directories already exist inside the bot's folders. Run a normal build after dependency changes to refresh them; the bot can then compile and run Go tests offline. Caches outside its folders, including caches reached through links or Windows junctions, are excluded. Give test commands enough time with `timeoutSeconds` (up to 300). Some server integration tests exercise Git clones and nested folder grants that require ancestor metadata denied by AppContainer; those tests still require a local run outside the scoped shell.

Frontend unit tests use `cd web; npm test`. Their separate native JavaScript configuration preserves symlink paths and uses isolated worker threads, avoiding the child-process pipe hang in older Node/libuv versions under Windows AppContainer. The scoped Windows dependency check uses Node's resolver because Vitest's install probe inspects denied ancestor folders; missing dependencies still fail normally. Run `npm ci --ignore-scripts` outside the scoped shell when frontend dependencies change.

**Compatibility:** the Windows acceptance suite exercises PowerShell, Git init/add/commit/status with **reftable**, a Go build/run, a Node program and a .NET SDK/MSBuild build/run. Scoped commands default new Git repositories to reftable and configure Node's loader to preserve symlinks; Windows still enforces target access. Git's older loose-ref backend and programs that demand metadata access to denied ancestor folders can fail in this sandbox. Existing repositories are never converted automatically. Validate each project's toolchain before relying on it for unattended work.

## Calendar and downtime

Supported recurrences are `daily HH:MM`, `weekly MON HH:MM`, `every N minutes/hours/days`, and an RFC3339 one-time instant. Daily/weekly schedules preserve local wall time through DST; a nonexistent wall time is skipped and an ambiguous wall time runs once. The server's Windows timezone is resolved to an IANA name when saving.

After downtime, every missed occurrence awaits review. Select jobs, set concurrency (default `1`; `0` means unlimited), then choose **Run selected**. Unselected jobs are skipped. **Skip all** dismisses the entire pending batch. Equivalent chat commands are `/missed run <concurrency> <occurrence IDs>` and `/missed skip-all`. Reviews survive restart; intentionally paused/disabled schedules do not accumulate missed work. Interrupted work is identified and never automatically replays uncertain tools.

## Development settings

**Settings → Development** lets you develop Echo using Echo. Save the absolute path to the new Echo Git checkout on the server computer; a valid checkout beside the executable or at its working directory is suggested automatically. Git worktrees are supported. These controls are available to local owners and authenticated Hosted Server users.

**Check for Updates** pins the selected machine’s configured upstream commit and checks each participating machine. **Update scope** defaults to **Connected swarm** when Swarm is enabled; choose **This machine only** for an individual update. Updating is offered when any eligible machine needs that revision, even if the selected machine is already current. The confirmation lists the commit, machines and exclusions. Each machine fetches its own upstream and must be able to fast-forward to the same commit with a clean checkout, including no untracked files outside ignored build directories. Echo never resets, stashes, switches branches, changes remotes, or discards edits.

The browser’s entry machine coordinates using its own owner grants. Updates include authorized connected peers, including relayed peers; offline machines are reported, and disconnected/revoked connections are excluded. Machines build independently, then restart one at a time, with relay destinations first and the entry machine last. Before restarting, a machine temporarily refuses new chat work and defers schedules while accepted work and handoffs finish. It never force-stops bots for a coordinated update. Progress, logs and verified running revisions survive browser closure and process replacement.

Failures do not stop other eligible machines. **Retry unsuccessful machines** retries the same pinned commit; **Cancel pending updates** releases drains and cancels unlaunched builds. A completed checkout fast-forward remains in place, and an already-launched replacement finishes. Offline machines are not silently updated when they reconnect. Older peers without `development-update/1` require a one-time local update using their existing updater before joining coordinated rollouts.

**Build & Relaunch** builds the current checkout (including local edits), its plugin runtime, and frontend, then launches the repository's `echo.exe` on Windows or `echo` on Unix. It requires Git, Go, Node.js, npm, and PowerShell on Windows or `/bin/sh` on Unix. **Relaunch** restarts the current executable without building; **Terminate Echo** stops it without restarting. Neither requires a source checkout. Restarting and stopping interrupt active work. Echo remains available during builds and closes gracefully only after a successful build and launcher preparation. The browser waits for a replacement process; an entry-host restart reloads the tab, while a remote restart refreshes that instance's resources.

Build, relaunch and terminate remain selected-machine actions; their interruption behavior does not apply to the coordinated updater’s wait-until-idle flow. The panel shows progress and the latest 16 KiB of operation output. Full logs live under the existing data directory’s `development/<operation-id>/` folder, alongside launcher diagnostics and replacement-process stdout/stderr. If a build fails after an update, the checkout stays updated and the previous Echo process stays running. If a restart fails, inspect those logs and start Echo manually. Operations are serialized across browsers. Enable **Automatically search for updates** to check the saved repository at launch and every hour, even with no browser open. This setting is saved per machine and starts disabled; enabling it also checks immediately. Checks never install or restart Echo. A green dot on Settings, Development, and the update controls guides you to available updates. The settings machine selector identifies machines with updates; the dot clears when a check confirms the running build is current.

## Build and validate

Requires Go 1.26+, Node.js compatible with Vite 8, and npm. Windows release acceptance additionally requires Git with reftable support, Fossil, Node and a .NET SDK on PATH, plus Perforce client/server binaries. Put `p4.exe` and `p4d.exe` on PATH or set `ECHO2_TEST_P4` and `ECHO2_TEST_P4D` to portable copies. Perforce tests start a temporary loopback-only depot and remove it afterward; no real depot is modified. These external CLIs are not bundled into the Echo ZIP.

```powershell
.\build.ps1                         # Build echo.exe from the repository root
.\build.ps1 -OutputPath .\staged.exe # Build to an alternate path
.\build.ps1 -Test -BrowserTests     # Go, type checking, unit tests, Playwright
.\build.ps1 -Release                # All checks, required native tests, release ZIP
.\scripts\test-development.ps1      # Isolated real build/relaunch/terminate acceptance
.\scripts\test-development.ps1 -Coordinated # Also verify a real pinned update and recovery
```

The Windows build reinstalls frontend dependencies with `npm ci`. Before installation, `build.ps1` logs and stops Node processes holding native modules from this checkout's `web/node_modules`, then waits for their file locks to be released. This can stop this checkout's Vite or Vitest sessions; restart them after building if needed. Other checkouts' Node processes are left running. **Build & Relaunch** keeps Echo running while building a staged executable and replaces it after graceful shutdown. If a locking process cannot be stopped, installation does not begin and the build reports its process ID.

```sh
sh build.sh                           # Native Linux/macOS executable
sh build.sh --output /tmp/staged-echo  # Alternate output
sh build.sh --test --native-tests --browser-tests
sh build.sh --release                 # Full native gates and .tar.gz archive
```

The browser tests use three isolated Echo fixtures on ports 3742, 3744 and 3745 and a deterministic model on 3743. They do not contact a cloud model or use your application data. Fixture names `linux` and `mac` identify simulated peers; the browser fixture does not prove native execution on those operating systems. For frontend iteration, run the backend on 3740 and `npm run dev` from `web` (Vite proxies `/api`). Run commands from the repository root. Releases are `echo-windows-amd64.zip` or `echo-<os>-<architecture>.tar.gz`; external CLIs are not bundled. `OLD/` is excluded. See [architecture](ARCHITECTURE.md) and [acceptance](ACCEPTANCE.md).
