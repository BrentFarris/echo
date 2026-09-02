# Echo

<p align="center">
  <strong>A local-first AI coding workspace that runs in your browser.</strong>
</p>

<p align="center">
  <a href="https://github.com/BrentFarris/echo/actions/workflows/nightly-builds.yml"><img alt="Nightly builds" src="https://github.com/BrentFarris/echo/actions/workflows/nightly-builds.yml/badge.svg"></a>
  <a href="LICENSE"><img alt="MIT license" src="https://img.shields.io/badge/license-MIT-2563eb.svg"></a>
  <img alt="Go" src="https://img.shields.io/badge/backend-Go-00ADD8.svg">
  <img alt="TypeScript" src="https://img.shields.io/badge/frontend-TypeScript-3178C6.svg">
</p>

<p align="center">
  <a href="#quick-start">Quick start</a> ·
  <a href="#configure-a-model">Model setup</a> ·
  <a href="#linux-sandbox">Linux sandbox</a> ·
  <a href="#build-from-source">Build from source</a> ·
  <a href="#security">Security</a>
</p>

![Echo Chat](docs/screenshots/echo-chat.png)

Echo brings chat, code editing, source control, and terminals together in one responsive web interface. The Go server works directly with folders on its machine, persists each workspace's conversations and agent configuration, and serves the TypeScript application as part of a single binary.

Connect Echo to a local or remote provider that exposes an OpenAI-compatible `/chat/completions` API. Echo supports streamed responses, reasoning content, and tool calls without requiring an Echo account or hosted Echo service.

> **Project status:** Echo is under active development. Nightly builds are unsigned snapshots of the current `master` branch.

## What Echo can do

| Area | Current capabilities |
| --- | --- |
| **Chat** | Persistent workspace conversations, multiple chat tabs, per-chat model and agent-mode selection, streamed Markdown, reasoning, tool activity, stop and clear controls, skill creation, and an inspectable Trajectory event stream |
| **Agent modes** | Built-in General and read-only Plan modes plus workspace-defined system instructions, tool allowlists, and path restrictions |
| **Code** | Monaco editing, generic opt-in LSP and DAP debugging, file tabs, workspace trees, quick open, text and file search, create/rename/save operations, external-change detection, recoverable trash, and browser hot-exit buffers |
| **Git** | Repository discovery, working-tree status, staged and unstaged diffs, staging, commits, branches, remotes, fetch/pull/push/sync, history, tags, and stashes |
| **Terminal** | Workspace-aware PTY sessions powered by xterm.js, resize, restart, stop, and reusable saved commands |
| **Linux sandbox** | Optional per-workspace Docker workbench plus visible Xfce/Chromium desktop, browser takeover, persistent Linux/browser state, and deny-by-default egress |
| **Agent tools** | Workspace-scoped file inspection and editing, shell commands, text and file search, image/video reads, web fetch/search, image generation, and reusable workspace skills |
| **Plugins** | Reviewed local/GitHub packages with sandboxed page or floating views, optional native JSON-RPC tools, typed settings, secret references, immutable snapshots, and chat-driven authoring |
| **Connections** | Multiple OpenAI-compatible LLM endpoints, custom HTTP headers, interaction routing, optional SearXNG research, and optional ComfyUI image generation |
| **Access** | Owner-password authentication, remembered browser sessions, session revocation, WebSocket updates, and desktop/mobile browser layouts |

### Workspace-scoped agents

Echo gives the selected model tools for the active workspace rather than unrestricted filesystem context. The built-in Plan mode exposes inspection and research tools without mutation tools. Custom modes can change the system instructions, choose exactly which tools are available, and limit path-aware tools with workspace-relative glob patterns.

Reusable skills live in `.echo/skills/` inside a workspace. Echo can search and read them during a chat, record new skills through tool calls, or synthesize a skill from the current conversation.

Echo also ships the read-only `builtin/echo-plugins` skill. You can ask Chat to scaffold, implement, validate, and stage an optional plugin; Echo then presents a trusted owner approval card before any generated or downloaded code can run. See the [Plugin API v1 guide](docs/plugins/README.md).

### Editor, Git, and terminal in one place

![Echo Code and Source Control](docs/screenshots/echo-code-git.png)

The Code view uses Monaco for editing and reviewing diffs. Its explorer watches the filesystem for changes, keeps unsaved browser buffers recoverable, and moves deleted entries into Echo's restorable trash. The source-control view supports common repository workflows without leaving the browser, while the integrated terminal provides a full PTY on the Echo server.

### Optional Linux sandbox and desktop

On Windows x64 with Docker Desktop in Linux-container mode, or Linux x86-64 with Docker Engine, a workspace can opt into an isolated Ubuntu workbench and visible Xfce/Chromium desktop. Commands, terminals, Git, LSPs, builds, tests, and web fetches then run through the sandbox with no host fallback; registered workspace files remain canonical host bind mounts. The user can watch the AI, take over the desktop to sign in without logging keystrokes, and return the same persistent browser profile to the AI. See the [Linux sandbox guide](docs/sandbox.md) for setup, persistence, network grants, reset boundaries, and the container-isolation security model.

### Language servers

Open **Settings → Language Servers** to copy the built-in `gopls`, `clangd`, or `lua-language-server` template, edit its executable, arguments, selectors, environment, initialization options, and settings, then enable it for the current workspace. Echo starts enabled servers in the workspace's main folder and does not install or download executables.

Profiles are reusable and stored globally in `echo.json`; workspace enablement, replacement overrides, and formatting controls are stored in `.echo/workspace.json`. Custom servers use the same profile fields, so adding another stdio LSP server requires no Echo code changes. Commands and arguments are executed directly without shell expansion. Format on save is off by default and fails open with a three-second default timeout.

When the same file is open in multiple browsers, the first active editor owns its unsaved LSP document. The other browser can use **Take Over** in the Code status bar when it intentionally needs language-server control.

### Debugging

Open **Run and Debug** in Echo Code to configure a user-installed Delve, debugpy, js-debug, CodeLLDB, or custom DAP adapter. Debug profiles are machine-local; launch/attach configurations and compounds are portable workspace settings; personal breakpoints and watches remain outside the repository. Echo supports concurrent and child sessions, revision-safe multi-browser controls, reconnectable debug terminals, the standard stepping and inspection workflow, and capability-gated advanced breakpoints, mutation, reverse execution, memory, disassembly, modules, and virtual sources. See [Debugging in Echo](docs/debugging.md) for configuration, VS Code import, keybindings, sandbox behavior, and cleanup guarantees.

## Quick start

### Run from source with one click

After cloning the repository or downloading and extracting its source archive, use the launcher for your operating system:

| Platform | Double-click this file |
| --- | --- |
| Windows | `install-echo-windows.cmd` |
| Linux | `install-echo-linux.sh` |
| macOS | `install-echo-macos.command` |

The launcher checks for Go 1.26+ and Node.js 22+ with npm. If a requirement is missing or outdated, it downloads a checksum-verified, portable copy into the ignored `.echo-tools/` directory without changing the system Go or Node.js installation. Build caches also stay in that directory. It then installs the locked frontend packages, builds the embedded web application and Echo server, and starts Echo at [http://localhost:3740](http://localhost:3740). Leave the terminal window open while using Echo. On a minimal Linux installation that lacks basic download/archive utilities, the launcher may ask for `sudo` so the system package manager can install them.

On Linux, choose **Run** if the file manager asks whether to display or execute the script. Git checkouts preserve the executable bit; if an extracted archive does not, run `chmod +x install-echo-linux.sh` once. The launchers can also be run from a terminal and pass command-line options through to Echo, for example `./install-echo-linux.sh -port 8080`.

### Download a nightly build

Open [GitHub Releases](https://github.com/BrentFarris/echo/releases) and download the nightly archive for your platform.

| Platform | Release asset |
| --- | --- |
| Windows | `echo-windows-nightly.zip` |
| Linux | `echo-linux-nightly.tar.gz` |
| macOS | `echo-macos-nightly.tar.gz` |

Echo is now a standalone web server. It does not require Wails, WebView2, GTK, or WebKitGTK.

On Windows PowerShell:

```powershell
Expand-Archive .\echo-windows-nightly.zip -DestinationPath .\Echo
.\Echo\echo.exe
```

On Linux or macOS:

```bash
tar -xzf echo-linux-nightly.tar.gz # use echo-macos-nightly.tar.gz on macOS
chmod +x echo
./echo
```

The nightly binaries are not currently code-signed or notarized. Your operating system may ask you to confirm that you trust the downloaded file.

### Complete first-run setup

1. Start Echo and leave its terminal window open. It prints the web address and a one-time authentication setup code.
2. Open [http://localhost:3740](http://localhost:3740) in a modern browser.
3. Enter the setup code, name the browser or device, and choose an owner password between 12 and 128 characters.
4. Open **Settings → LLM Endpoints** and configure a model provider.
5. Add a workspace by giving Echo a name and one or more existing folders on the server machine.
6. Return to Chat, choose a model and agent mode, and ask Echo to inspect or change the workspace.

If the owner password is lost, stop Echo and restart it with `-reset-auth`. This clears the password and all remembered sessions, then prints a new setup code.

### Install Echo as a desktop app

With the Echo server running, open [http://localhost:3740](http://localhost:3740) in Chrome or Edge and select the **Install Echo** icon in the address bar. The installed app opens in its own window without the browser toolbar. If Windows does not pin it automatically, right-click Echo in the taskbar and choose **Pin to taskbar**.

The installed icon opens the web app but does not start the Echo server. Continue to start Echo with the launcher and leave its terminal window running while you use the installed app. Browser installation is available from `localhost`, `127.0.0.1`, or an HTTPS origin; a plain HTTP LAN address is not eligible.

## Linux sandbox

Echo can opt an individual workspace into a Docker-backed Ubuntu environment with a browser-accessible Linux desktop. It is disabled by default, and workspaces that do not enable it continue running exactly as before without requiring Docker.

| Host platform | Sandbox requirement | Support |
| --- | --- | --- |
| Windows x64 | Docker Desktop running Linux containers | Supported |
| Linux x86-64 | An accessible Docker Engine | Supported |
| macOS | Docker Desktop | Experimental |

Echo checks and diagnoses Docker but never installs or reconfigures it. The initial images are `linux/amd64`; ARM hosts and Podman are not currently supported. This is container isolation rather than a hardware virtual machine, so Docker Engine and the host kernel remain trusted components.

Official nightly binaries pin all three images by immutable digest. Builds made directly from the source tree use the public `protocol-1` image channel so the one-click source launchers can install the matching images; release builds never rely on a mutable tag.

### Enable a workspace sandbox

1. Install and start Docker Desktop or Docker Engine.
2. Select the workspace, then open **Linux Sandbox** from Echo's activity bar.
3. Pull the pinned Workbench, Desktop, and Egress images when prompted.
4. Choose CPU, memory, and idle-stop limits, then select **Enable sandbox**.

Before enabling, Echo verifies the Docker engine, Linux mode, x86-64 architecture, image compatibility, every workspace mount, write access, and the read-only `.echo` mask. A failed preflight leaves the workspace disabled. The default allocation is 4 CPUs, 6144 MiB of memory, and a 30-minute idle timeout; an idle timeout of zero keeps the sandbox running until it is stopped explicitly.

The sandbox starts lazily when a terminal, agent command, Git or LSP operation, web fetch, GUI tool, or Desktop visit needs it. If Docker later becomes unavailable, Echo reports the failure and does **not** silently run the operation on the host. Disabling the sandbox stops active sandbox processes before returning that workspace to normal host execution.

### Runtime layout

| Component | Runs |
| --- | --- |
| **Echo host** | Authentication, UI/API serving, path-confined file editing and search, file watching, and the canonical workspace files |
| **Workbench** | Ubuntu 24.04, Bash and PTYs, Git, LSPs, builds, tests, formatters, Python, Node 22, Go 1.26, and common development tools |
| **Desktop** | Xfce, headed Chromium, Playwright browser automation, a file manager, and a text editor |
| **Egress gateway** | The sandbox's only external network route, with filtered DNS, HTTP proxying, and SOCKS5 |

Registered host folders are mounted read-write at stable `/workspace/<root-id>` paths and remain the source of truth. Their `.echo` directories are masked read-only. A persistent `/exchange` volume is shared by the workbench and desktop; Chromium downloads are saved under `/exchange/downloads`.

Echo does not give the containers the host home directory, SSH configuration, host credentials, devices, host namespaces, or the Docker socket. Integrated terminals, `shell_command`, Git subprocesses, LSPs, builds, tests, formatting, and `web_fetch` use the sandbox target. Host-side file editing, search, and watching continue against the confined bind mounts.

### Desktop access and AI takeover

The Sandbox view displays the live Xfce desktop through an authenticated noVNC connection, so the user can watch browser and desktop actions directly in Echo. One workspace-wide lease controls graphical input:

- An AI GUI action acquires control for its chat turn.
- **Take Control** immediately preempts and cancels in-flight AI GUI actions.
- The user can sign in or handle sensitive prompts, then select **Return Control** when finished.
- Other authenticated devices remain view-only; a disconnected controller has a two-minute reconnect grace period.

Human VNC keystrokes, including passwords, are not copied into chat requests, tool arguments, or Trajectory logs. However, after control is returned, the AI can use the same persistent Chromium profile and therefore the authority of any signed-in website sessions. Browser data is protected by host and Docker storage permissions rather than Echo encryption.

### Network access

The Workbench and Desktop containers have no direct external route. The gateway blocks loopback, host, LAN/private, link-local, carrier-grade NAT, reserved, and cloud-metadata destinations, rechecking every DNS result to prevent rebinding.

The owner can grant one exact hostname or IP and TCP port from the Sandbox view. Wildcards and subnets are not accepted, and revocation takes effect without recreating the containers. Configured SearXNG and ComfyUI endpoints remain explicit Echo host services and are not exposed to the sandbox automatically.

### Persistence, setup, and resets

Workbench state, desktop state, the Chromium profile, and `/exchange` use separate Docker volumes. Their reset boundaries are intentionally independent:

| Action | Workbench home | Desktop home | Browser profile | Exchange | Host workspace files |
| --- | --- | --- | --- | --- | --- |
| Stop/start or recreate containers | Kept | Kept | Kept | Kept | Kept |
| Reset workbench | Deleted | Kept | Kept | Kept | Kept |
| Reset browser data | Kept | Kept | Deleted | Kept | Kept |
| Delete sandbox data | Deleted | Deleted | Deleted | Deleted | **Kept** |

For reproducible customization, add `.echo/sandbox/setup.sh`. Echo hashes the recipe and requires owner approval before running a new digest as root for both `ECHO_SANDBOX_ROLE=workbench` and `ECHO_SANDBOX_ROLE=desktop`. Approved recipes rerun after relevant resets or image replacement. Ad-hoc `sudo` changes can survive a stop, but not container recreation unless they are captured in the recipe.

Portable resource settings live in `.echo/workspace.json`; image digests, approved setup state, network grants, and volume names are machine-local beside Echo's global configuration. Runtime VNC, agent, lease, and proxy credentials remain memory-only.

See the [Echo Workspace Linux Sandbox guide](docs/sandbox.md) for the full security model, setup recipe behavior, image build instructions, diagnostics, and implementation details.

## Configure a model

Echo appends `/chat/completions` to the endpoint URL unless it is already present. The provider must accept OpenAI-compatible chat-completion requests and, for interactive chat, streamed server-sent events and OpenAI-style tool calls.

Common local base URLs include:

| Provider | Typical base URL |
| --- | --- |
| Ollama | `http://localhost:11434/v1` |
| LM Studio | `http://localhost:1234/v1` |
| llama.cpp server | `http://localhost:8080/v1` |
| Remote provider | The provider's OpenAI-compatible HTTPS URL |

Each endpoint profile has its own model name, generation settings, request timeout, stream-idle timeout, optional system-prompt appendage, and custom headers. **Reasoning Effort** sends the OpenAI-compatible `reasoning_effort` request field; named efforts take precedence over Echo's local-model thinking-token budget. Use **Provider default / token budget** for servers that instead support `chat_template_kwargs.thinking_token_budget`. Echo does not detect supported effort levels or retry with a lower value when a provider rejects one. The stream-idle watchdog resets on provider data and SSE heartbeats; set it to `-1` for providers that legitimately remain silent for longer than the configured interval. Custom headers can carry provider-specific authentication such as `Authorization: Bearer ...`.

Endpoint routing lets Chat, Research, Vision, and Inline Code use different profiles. The model picker in Chat can override the routed Chat model for the next conversation turn.

Optional connections are configured under **Settings → External Connections**:

- A SearXNG URL enables the `web_search` agent tool.
- A ComfyUI URL and workflow files enable text-to-image and image-to-image tool calls.

## Command-line options

```text
-port int
    HTTP port to listen on (default 3740)
-web string
    Serve SPA assets from this directory instead of the embedded production build
-data string
    Use this application-data JSON file instead of the platform config directory
-reset-auth
    Clear the owner password and remembered sessions and issue a new setup code
-safe-mode
    Start with all optional plugins disabled while keeping plugin management available
```

Echo listens on the selected port, including non-loopback interfaces. Control network access with the host firewall or a reverse proxy.

## Build from source

### Requirements

- Go 1.26 or newer
- Node.js 22 and npm
- Git
- A modern browser

Docker is optional. Sandbox-enabled workspaces require Docker Desktop (Windows x64, Linux-container mode) or Docker Engine (Linux x86-64); Echo diagnoses but does not install Docker.

Clone the repository, build the frontend, and then compile the Go server so the production assets are embedded in the executable:

```bash
git clone https://github.com/BrentFarris/echo.git
cd echo/web
npm ci
npm run build
cd ..
go build .
./echo
```

On Windows, `go build .` creates `echo.exe`; run it with `.\echo.exe`.

### Development

Run the backend and Vite development server in separate terminals.

Terminal 1, from the repository root:

```bash
go run .
```

Terminal 2:

```bash
cd web
npm ci
npm run dev
```

Open [http://localhost:5173](http://localhost:5173). Vite proxies `/api` and `/ws` to the Go server on port 3740.

### Tests

Run the Go and frontend suites:

```bash
go test ./...
cd web
npm test
npm run build
```

The authenticated browser acceptance tests use Playwright and build an isolated Echo runtime:

```bash
cd web
npx playwright install chromium
npm run test:e2e
```

## Architecture

| Layer | Implementation |
| --- | --- |
| **Host** | Go HTTP server with JSON endpoints, authenticated WebSockets, embedded production assets, and graceful shutdown |
| **Frontend** | Frameworkless TypeScript and Vite, with Monaco for editing and diffs and xterm.js for PTY terminals |
| **LLM** | OpenAI-compatible chat-completions client with streaming, reasoning, tool-call orchestration, model routing, and configurable headers |
| **Workspace services** | Path-scoped filesystem operations, indexed search, file watching, recoverable trash, Git, terminals, custom agent modes, and skills |
| **Sandbox runtime** | Per-workspace Docker lifecycle, authenticated Linux agents and PTYs, Xfce/Chromium desktop bridging, graphical control leases, path translation, and deny-by-default egress |
| **Plugin host** | Server-owned package/lifecycle manager, dynamic tool registry, supervised stdio JSON-RPC runtimes, sandboxed iframe sessions, and host-owned plugin navigation/windows |
| **Persistence** | Atomic JSON stores for global configuration and workspace-owned state |

The same SPA is served in production from the Go binary. During development, Vite serves the frontend and proxies API and WebSocket traffic to the backend.

## Data and storage

Echo's global data is stored in `Echo/echo.json` under the current user's platform configuration directory. It contains settings, endpoint profiles, registered workspaces, authentication records, and saved terminal commands. Use `-data` to choose a different file for testing or isolated installations.

The main folder of each workspace owns a `.echo/` directory. Current workspace files include:

```text
.echo/
├── workspace.json
├── chat-workspace.json
├── agent-modes.json
├── plugins.json
├── trajectories/
│   └── <chat-id>.jsonl
└── skills/
    └── <skill-name>/SKILL.md
```

`workspace.json` is the authoritative workspace configuration. Its paths are
stored relative to the `.echo` directory when possible (`mainPath` and the
main `folders` entry are `../`), then resolved to absolute host paths whenever
Echo opens the workspace or supplies paths to tools and other services.

Machine-local plugin packages, immutable snapshots, logs, namespaced data, approvals, and non-secret global configuration live in a `plugins/` directory beside `echo.json`. Workspace `plugins.json` contains only portable public-GitHub pins, activation, non-secret configuration, and secret-source references; it never contains a local source path or secret value.

Configuration and transcript snapshots are written atomically. Long-running Chat, Code Chat, vision, and research-agent loops use configurable provider-portable [context compression](docs/context-compression.md) while retaining canonical raw history. Trajectory logs are append-only JSONL audit streams containing the exact secret-free model requests, raw provider chunks, reasoning, tool activity, usage, and timing observed by Echo. Custom endpoint headers and credentials are never copied into trajectory records. Clearing or closing a chat deletes its trajectory; otherwise logs currently have no automatic retention limit. See [Trajectory storage and behavior](docs/trajectory.md) for the event model, APIs, legacy behavior, and current limitations.

Deleted workspace entries are moved to a separate trash store beside the global data file so they can be restored from the Code view. Browser hot-exit state is local to that browser and is cleared when signing out.

## Security

Echo is a single-owner development tool, not a multi-tenant service.

- The first process start prints a memory-only setup code. Completing setup creates an Argon2id-protected owner password.
- Browser sessions are remembered, expire over time, and can be reviewed or revoked under **Settings → Security**.
- Authentication controls access but does not encrypt plain HTTP. Use Echo only on a trusted network unless a TLS reverse proxy protects the connection.
- Every authenticated device can edit workspace files, run Git operations, and execute arbitrary commands through terminals and enabled agent tools.
- Agent modes reduce what is offered to a model, but the General mode is intentionally powerful. Use trusted models, keep work in version control, and expose only the folders Echo needs.
- Custom endpoint headers may contain secrets and are stored in the local application-data file. Protect that file with the same care as other developer credentials.
- Sandboxed plugin views cannot access Echo's DOM or APIs directly, but optional native plugin backends run with the Echo owner's OS permissions. Permissions are review disclosures rather than OS containment; install native plugins only from code you trust.
- The optional workspace Linux sandbox isolates command and GUI execution from host files outside registered roots, but containers are not hardware VMs. Protect Docker and Echo's machine-local sandbox volumes; a signed-in browser profile can be used by the AI after control is returned.

## Contributing

Issues and focused pull requests are welcome. Keep backend and frontend changes covered by their respective tests, run the production frontend build before compiling a distributable binary, and include Playwright coverage for important authenticated browser workflows.

Echo is available under the [MIT License](LICENSE).
