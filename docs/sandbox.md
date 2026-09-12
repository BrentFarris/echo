# Echo Workspace Linux Sandbox

Echo can optionally run one workspace's commands and graphical applications in a Docker-backed Linux environment. It is disabled by default and does not change older workspaces.

The v1 acceptance targets are:

| Echo host | Requirement | Status |
| --- | --- | --- |
| Windows x64 | Docker Desktop in Linux-container mode | Supported |
| Linux x86-64 | An accessible Docker Engine | Supported |
| macOS | Docker Desktop | Experimental; compile/probe only |

Echo diagnoses Docker but never installs or reconfigures it. Podman, ARM images, hardware-VM isolation, and portable per-volume disk quotas are not part of v1.

Official nightly binaries embed immutable GHCR digest references. Builds made directly from the source tree use the public `protocol-3` channel for each image, allowing the one-click source launchers to pull protocol-compatible images without requiring a local Docker build. CI moves that channel only after the image test and scan stages pass; the packaged nightly binaries continue to use the exact digests produced by that run.

## What is isolated

Each enabled workspace gets two `linux/amd64` containers on a dedicated internal network:

- **Runtime:** the pinned Playwright 1.62.1 Ubuntu 24.04 base, Xfce, TigerVNC, Chromium, Thunar, Mousepad, Bash, Git/Fossil, SSH client, ripgrep, compilers, Python, Node 22, Go 1.26, LSPs, terminals, and builds/tests. Shell and GUI applications share the `echo` user, home, processes, installed tools, session D-Bus, display, and `localhost`.
- **Egress gateway:** the only container attached to an externally routed Docker network. It provides filtered DNS, HTTP CONNECT/forward proxying, and SOCKS5 with remote DNS.

Registered host folders remain the canonical files. Echo mounts them read-write at stable `/workspace/<root-id>` paths. Each root's `.echo` directory is over-mounted read-only, so setup approval, workspace policy, skills, and other Echo metadata cannot be changed from Linux commands. `/exchange` is a shared persistent volume for browser downloads and transfers with the host workspace.

Echo never mounts the host home directory, SSH/config directories, host credentials, devices, host namespaces, or the Docker socket. Management, VNC, browser-bridge, lease, and proxy endpoints bind random ports on `127.0.0.1`; their high-entropy credentials exist only in Echo memory and root-only container tmpfs files.

This is container isolation, not a hardware virtual machine. Docker Engine and the host kernel remain part of the trusted computing base.

## Enable and operate it

1. Install/start Docker Desktop or Docker Engine yourself.
2. Open **Sandbox** in Echo's activity bar for the selected workspace.
3. Pull the pinned images if Echo reports that they are missing.
4. Choose CPU, memory, and idle-stop limits, then select **Enable sandbox**.

Echo checks the Docker daemon, Linux-container mode, `amd64` architecture, all image references, every workspace mount, workspace write access, and the read-only `.echo` mask before saving `enabled: true`. A failed check leaves the workspace disabled.

An enabled sandbox starts lazily on the first sandbox terminal, Git/LSP operation, agent command, web fetch, GUI tool, or Desktop visit. Echo stops it after the configured idle period only when it has no running tool/process, terminal client, desktop viewer, or graphical lease. A guest also exits when Echo's heartbeat has been absent for two minutes.

Docker becoming unavailable never causes host fallback. Sandbox operations return stable errors until Docker recovers or the owner explicitly disables the sandbox. Disabling cancels active sandbox work, stops terminal/LSP/Git processes, stops the containers, and then restores the normal host execution target.

Portable configuration lives in `.echo/workspace.json`:

```json
{
  "sandbox": {
    "enabled": true,
    "cpuLimit": 4,
    "memoryMiB": 6144,
    "idleTimeoutMinutes": 30
  }
}
```

The accepted ranges are 1–16 CPUs, 4096–32768 MiB, and 0–1440 idle minutes; zero disables idle stopping. Missing configuration means disabled and is not written back as a migration.

## Persistence and resets

Machine-local state is stored beside Echo's `echo.json` at `sandboxes/<workspace-id>/state.json`. It contains image references/digests, volume/container names, setup results, the approved setup digest, and exact network grants. It never stores runtime credentials or desktop tokens.

| Action | Unified home | Browser | Exchange | Upgrade recovery | Host workspace |
| --- | --- | --- | --- | --- | --- |
| Stop/start or recreate | Kept | Kept | Kept | Kept | Kept |
| Reset environment | Deleted | Kept | Kept | Kept | Kept |
| Reset browser data | Kept | Deleted | Kept | Kept | Kept |
| Delete sandbox data | Deleted | Deleted | Deleted | Deleted | **Kept** |

`reset_workbench` remains an API alias for `reset_environment`. Both reset the unified home/container. The gateway retains its existing CPU and memory reservation; the runtime receives the remaining budget.

## Upgrading an existing sandbox

Protocol-2 sandboxes show **Upgrade required**. Start, reset, and recreate never silently replace their data or execute on the host. Select **Upgrade sandbox** to restart into protocol 3 (machine-state schema 2). This stops sandbox commands, terminals, debugging and language servers; save work first. Progress is available in the Sandbox view and its status/events API.

Echo journals the upgrade in `sandboxes/<workspace-id>/migration.json`, snapshots the original machine state, and copies into new versioned volumes. A failure or restart leaves the original volumes and stopped containers intact. Retry **Upgrade sandbox** to rebuild the candidate from the originals. State commits atomically only after the runtime and its command, display, and browser services validate.

The old workbench home is the baseline. Desktop-only files are added. Desktop Xfce, GTK, Thunar settings and keyrings take precedence; other conflicts retain the workbench version. Both versions of conflicting files are saved under `~/sandbox-migration-conflicts-*/workbench/` and `desktop/`, with `report.json` explaining each choice. Permissions, owners and symlinks are copied without following symlinks out of the source trees. Browser data and exchange files receive their own new volumes; network grants and workspace configuration are retained.

Container operating-system layers are not merged. Additional system packages must be installed again through the reviewed setup script. Original containers remain stopped for recovery of those layers. Recovery names are recorded in `state.json` and the journal. Ordinary recreation/reset keeps all recovery resources; only the owner's **Delete sandbox data** action removes them.

The UI reports aggregate container memory/process usage and Docker volume/writable-layer disk use. Disk numbers are informational because Docker has no consistent Windows/Linux per-volume quota.

## Reproducible setup

Create `.echo/sandbox/setup.sh` when a workspace needs additional packages or configuration. Echo hashes the file and requires an authenticated owner approval before executing a new digest as root. Approval covers both the script digest and sandbox protocol. Review existing scripts for the unified environment before approving them again. It runs once with this role:

```text
ECHO_SANDBOX_ROLE=runtime
```

An approved digest is automatically reapplied after container recreation or either reset. If the recipe changed, Echo leaves the sandbox running but reports `approval_required`; it never runs the changed file implicitly. Ad-hoc `sudo` changes in the runtime survive stop/start but disappear when its container is replaced unless captured in the recipe.

Echo executes the exact approved recipe bytes through Bash (maximum 1 MiB), preventing a file change between digest approval and execution. The guest-visible source location is also provided as `ECHO_SANDBOX_SETUP_RECIPE` for recipes that need to locate adjacent workspace files.

## Commands, Git, LSP, and tools

Chat, Code Chat, Goal and research agents receive explicit sandbox and Linux Bash guidance. Shell tools inherit `DISPLAY`, `DBUS_SESSION_BUS_ADDRESS`, `XDG_RUNTIME_DIR`, `HOME` and the toolchain `PATH`. Use `echo-browser URL` or the desktop browser shortcut to open the same managed Chromium session. A server started through `shell_command` is reachable through browser tools at `127.0.0.1`.

For an enabled workspace, Echo routes integrated terminals, `shell_command`, every Git subprocess, LSP processes, builds, tests, and formatters to the runtime. Host-side path-confined file reads/edits/searches and file watching continue to operate on the canonical bind mounts. LSP `file:` URIs and workspace edits are translated recursively between host paths and stable guest paths, including Windows drives, spaces, Unicode names, and multi-root workspaces.

Git UI rendering remains in Echo, but hooks, filters, credential helpers, and external diff programs execute only in the runtime. Native plugin tools and Echo's host-level restart tool are unavailable for sandbox-enabled workspaces; there is no silent host escape. UI-only plugins and confined plugin authoring remain available.

`web_fetch` uses the authenticated loopback endpoint of the workspace egress gateway for the initial request and all redirects.

## Network policy

The legacy names `workbench` and `desktop` remain network aliases for `runtime`, preserving workspace service configurations.

The runtime has only the internal sandbox network and cannot route directly to the internet. The gateway rejects loopback, private, carrier-grade NAT, link-local, multicast, reserved/documentation, cloud-metadata, 6to4, and NAT64 destinations after every DNS resolution. This repeated check prevents a hostname from passing validation and later rebinding to a private address.

An owner can add a machine-local grant for one exact hostname or IP and one TCP port, with an optional non-reserved sandbox alias. Wildcards and subnets are invalid. Revocation rewrites the live gateway policy and takes effect without container recreation. A DNS answer does not bypass port enforcement.

Configured SearXNG and ComfyUI connections remain explicit Echo host services; enabling the sandbox does not silently expose the host to the guest.

## Desktop, takeover, and signed-in sessions

The Sandbox view renders noVNC's RFB canvas directly inside Echo. VNC itself is never exposed as a web application. Authenticated devices can observe the desktop, but only the workspace-wide lease owner can send input.

- The first graphical tool in a chat turn acquires the AI lease.
- **Take Control** blocks new AI model requests and tool actions across all workspace chats, Goals, and research workers, including host file edits. It cancels in-flight GUI interactions. Already-running non-GUI commands, user terminals, applications, and background jobs continue with their existing timeout/cancellation rules.
- Chats show **Paused while you control the desktop**. Hold time does not count as active Goal execution; Stop still cancels a waiting task.
- **Return Control** automatically resumes waiting tasks. Pending calls from the previous ownership generation are recorded as interrupted and discarded. The model is notified of the changed environment, must obtain fresh GUI context, and must reread existing files before host file edits. Old browser element references are invalidated.
- Taking control from another authenticated browser session requires confirmation.
- A connected controller does not expire; disconnecting starts a two-minute grace period.

Human VNC keystrokes—including passwords—do not become chat messages, tool arguments, or Trajectory entries. Echo shows a warning before the first desktop connection: once control is returned, the AI can use the authenticated Chromium profile with the authority of those signed-in sessions. Browser data is protected by host/Docker storage permissions rather than Echo encryption. Use **Reset Browser Data** to remove it independently.

Chromium runs headed with a persistent profile and a 1 GiB `/dev/shm`. The runtime applies Moby's default seccomp policy plus only `clone`, `setns`, and `unshare` for Chromium's user-namespace sandbox. Dangerous capabilities, privileged mode, raw networking, host namespaces, and host devices remain disabled.

## Reliable UI tools

General and Goal modes expose `ui_observe`, `ui_act`, `ui_verify`, and `ui_locate`, alongside `browser_open`, `browser_tabs`, and `browser_upload`. Custom modes keep their explicit allowlists; the previous browser and `desktop_control` tools remain registered for compatibility. Plan mode does not expose GUI execution tools.

The normal workflow is to observe a surface, choose a target using its role, label and ancestor context, act once, and check an expected result. `ui_observe` accepts `surface: "browser"` for Chromium or `surface: "desktop"` for native applications. `list: true` discovers tabs/windows; `search`, `scopeRef` and `nextCursor` keep large trees inspectable. Expanding a scope requires its `observationId`. Act and verify references must belong to the supplied observation, workspace and chat turn.

Browser targets use Playwright 1.62.1 AI accessibility references, normalized semantic locators, and context fingerprints. Open shadow roots and iframe controls are included. Unrelated DOM mutations do not invalidate targets. A detached control can be replaced once only when its normalized locator is unambiguous and its semantic context still matches. Changed rows, ambiguous replacements and frame navigation require a new observation. Action calls use native cancellation signals; timeouts never cause automatic repetition of input.

Native controls use a persistent Python GI/AT-SPI helper, started on the session accessibility bus before applications. The helper runs as `echo`, listens on a private Unix socket, and is accessible through the authenticated sandbox agent. It retains accessible object identities rather than tree indexes. It supports activation, editable text, check state, focus and selection when the application exposes those interfaces. A defunct or changed object is rejected. Traversal, stored references, D-Bus calls and request duration are bounded. Unsupported controls return an explicit capability error; canvas/custom widgets can use visual assistance.

`ui_act` separates input delivery from verification:

| Field | Meaning |
| --- | --- |
| `execution: not_started` | Preconditions failed before input was attempted. |
| `execution: completed` | The backend completed the requested input operation. |
| `execution: unknown` | Input may have occurred, but its completion could not be established. Observe before deciding what to do next. |
| `verification.status: passed` | The specified condition was observed; inspect `method` to distinguish deterministic evidence from model assessment. |
| `verification.status: unverified` | No postcondition was supplied or established. A click alone does not mean the task succeeded. |

Fill, check, select and focus have intrinsic checks. Explicit predicates cover visible/hidden state, exact text/value, checked/selected state, focus, browser URL/dialog, and native window title as supported by each backend. Verification defaults to five seconds. Fresh observations accompany action and verification results when the surface remains accessible. The runtime caches action outcomes by host tool-call ID, including unknown outcomes. This prevents a transport retry from submitting twice within that runtime; it is not an exactly-once guarantee across crashes or a newly generated tool-call ID.

All canonical and legacy GUI calls share one cancellable queue and ownership lease. Take Control invalidates the observation generation and cancels execution/verification. Turn completion discards host observation records. Diagnostic tool results record the backend, execution, verification evidence, recovery and specialist token usage; screenshot bytes use the existing transcript media storage and retention.

### Optional visual assistance

Capture with `ui_observe` and `screenshot: true`, then pass its `observationId` and a precise `description` to `ui_locate`. This sends only the screenshot and bounded target description to the endpoint selected for **Vision**. It does not send planner history or tools, and it remains a separate request when Vision and Chat use the same model. A missing or image-incapable endpoint returns `ui_vision_unavailable`; semantic control remains available.

The specialist must return a bounded rectangle, `absent`, or `ambiguous`. Invalid output gets at most one format correction. A rectangle creates a target reference without executing input. Screenshot metadata preserves source dimensions, coordinate space and transforms. Image transport fits within 1280 × 800 while preserving aspect ratio. A `region` in source pixels provides a detailed crop before resizing. Browser points use viewport coordinates; native points use display coordinates.

Before visual input, Echo captures the surface again and checks geometry and pixels inside the target plus a small margin. Changes elsewhere do not invalidate it. A changed target requires a fresh observation and location. Visual references support click, hover, type, key, scroll and drag; a visual drag requires both references in the same observation. Exact pixel comparison is deliberately conservative for animated targets, and visual grounding can still select the wrong control. Supply a postcondition or use `ui_verify` with `visual: true` and a description of the visible result. Such verification is explicitly `model_assessed`.

Screenshot previews are tagged `gui_preview` and displayed/persisted separately from model input. They do not reroute the main conversation to Vision and are excluded from media hydration after reload or compression.

### Runtime compatibility

This is an additive protocol-3 update. Browser/runtime health responses advertise UI capabilities. Older images keep legacy operations and report `ui_capability_unavailable` for new operations. Build/pull the updated runtime, then use the existing image refresh/recreate flow; existing home, Chromium profile and exchange volumes are retained. Publishing the new image and replacing an already-running workspace are separate deployment operations.

The deterministic browser suite runs with `node --test sandbox/images/runtime/browser-ui.test.mjs` after installing the pinned browser through `web`. The Docker lifecycle acceptance test also checks real GTK controls, Mousepad/Thunar discovery, window movement, defunct targets and duplicate submissions. For the opt-in model comparison and its limits, see [UI evaluation](ui-evaluation.md).

## Building development images

From the repository root on a `linux/amd64` Docker Engine:

```bash
docker build --platform linux/amd64 -f sandbox/images/runtime/Dockerfile -t echo-sandbox-runtime:dev .
docker build --platform linux/amd64 -f sandbox/images/gateway/Dockerfile -t echo-sandbox-egress:dev .
ECHO_SANDBOX_INTEGRATION=1 go test ./internal/sandbox -run TestDockerIntegration -count=1 -v
```

Development builds intentionally use local `:dev` tags. Nightly CI builds two candidate images, scans them, emits SPDX SBOM/license data, and requires real Docker acceptance on Linux before publishing. It embeds immutable `name@sha256:digest` references into Windows, Linux, and macOS binaries through Go linker values.

Each image carries an OCI source label that links its GHCR package to this public repository. GitHub creates a container package as private on its first publication, so the package owner must change each of the two packages to **Public** once in its GHCR package settings. CI uses a clean anonymous Docker configuration to verify every digest and the source-build `protocol-3` tags before releasing binaries; later releases fail closed if package visibility regresses.

Windows Docker Desktop acceptance is optional and does not gate image publication or application releases. To run it, open **Actions → Nightly Builds → Run workflow** and enable **Run Windows Docker Desktop acceptance** (`windows_sandbox_acceptance`). This input defaults to false; scheduled nightlies skip the Windows job. When enabled, it tests the same candidate images on a self-hosted runner labeled `self-hosted`, `Windows`, `X64`, and `echo-sandbox`, with Docker Desktop already running Linux containers. Register and start that runner before opting in. Echo's installer still installs only Echo.

The protocol-2 channels remain unchanged. Candidate images are transferred between acceptance jobs as artifacts; only accepted images are published to protocol-3 and embedded by digest.

## Common diagnostics

| Code/state | Meaning |
| --- | --- |
| `docker_unavailable` | Echo cannot reach the configured Docker Engine. |
| `docker_linux_engine_required` | Docker Desktop is in Windows-container mode. |
| `docker_architecture_unsupported` | The Engine is not `linux/amd64`. |
| `sandbox_images_missing` | Pull/build the two image references shown in the Sandbox view. |
| `workspace_mount_probe_failed` | Docker cannot mount/write a registered root or enforce the `.echo` mask. |
| `sandbox_protocol_mismatch` | Recreate with images built for this Echo protocol. |
| `setup_approval_required` | Review and approve the current `setup.sh` digest. |
| `user_control_active` | A browser session currently owns graphical input. |
| `sandbox_upgrade_required` | Use the explicit Upgrade sandbox action. |
| `user_control_interrupted` | A pending AI action was discarded after takeover. |
| `file_context_stale` | Reread the file before editing it after takeover. |

Stopping Echo gracefully stops active sandbox groups. On its next start, Echo reconciles labeled containers, stops incompatible or orphaned groups, preserves their volumes for manual recovery, and rotates all memory-only runtime credentials.
