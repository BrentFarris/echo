# Echo Workspace Linux Sandbox

Echo can optionally run one workspace's commands and graphical applications in a Docker-backed Linux environment. It is disabled by default and does not change older workspaces.

The v1 acceptance targets are:

| Echo host | Requirement | Status |
| --- | --- | --- |
| Windows x64 | Docker Desktop in Linux-container mode | Supported |
| Linux x86-64 | An accessible Docker Engine | Supported |
| macOS | Docker Desktop | Experimental; compile/probe only |

Echo diagnoses Docker but never installs or reconfigures it. Podman, ARM images, hardware-VM isolation, and portable per-volume disk quotas are not part of v1.

Official nightly binaries embed immutable GHCR digest references. Builds made directly from the source tree use the public `protocol-1` channel for each image, allowing the one-click source launchers to pull protocol-compatible images without requiring a local Docker build. CI moves that channel only after the image test and scan stages pass; the packaged nightly binaries continue to use the exact digests produced by that run.

## What is isolated

Each enabled workspace gets three `linux/amd64` containers on a dedicated internal network:

- **Workbench:** Ubuntu 24.04, Bash, Git, SSH client, ripgrep, compilers, Python, Node 22, Go 1.26, LSPs, terminals, and builds/tests.
- **Desktop:** Xfce, TigerVNC, Chromium for Testing, Playwright 1.62.1, Thunar, and Mousepad.
- **Egress gateway:** the only container attached to an externally routed Docker network. It provides filtered DNS, HTTP CONNECT/forward proxying, and SOCKS5 with remote DNS.

Registered host folders remain the canonical files. Echo mounts them read-write at stable `/workspace/<root-id>` paths. Each root's `.echo` directory is over-mounted read-only, so setup approval, workspace policy, skills, and other Echo metadata cannot be changed from Linux commands. `/exchange` is a shared persistent volume for browser downloads and transfers between the workbench and desktop.

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

| Action | Workbench home | Desktop home | Browser profile | Exchange | Host workspace |
| --- | --- | --- | --- | --- | --- |
| Stop/start | Kept | Kept | Kept | Kept | Kept |
| Recreate containers | Kept | Kept | Kept | Kept | Kept |
| Reset workbench | Deleted | Kept | Kept | Kept | Kept |
| Reset browser data | Kept | Kept | Deleted | Kept | Kept |
| Delete sandbox data | Deleted | Deleted | Deleted | Deleted | **Kept** |

The UI reports aggregate container memory/process usage and Docker volume/writable-layer disk use. Disk numbers are informational because Docker has no consistent Windows/Linux per-volume quota.

## Reproducible setup

Create `.echo/sandbox/setup.sh` when a workspace needs additional packages or configuration. Echo hashes the file and requires an authenticated owner approval before executing a new digest as root. It runs once with each role:

```text
ECHO_SANDBOX_ROLE=workbench
ECHO_SANDBOX_ROLE=desktop
```

An approved digest is automatically reapplied after container recreation or either reset. If the recipe changed, Echo leaves the sandbox running but reports `approval_required`; it never runs the changed file implicitly. Ad-hoc `sudo` changes in the workbench survive stop/start but disappear when its container is replaced unless captured in the recipe.

Echo executes the exact approved recipe bytes through Bash (maximum 1 MiB), preventing a file change between digest approval and execution. The guest-visible source location is also provided as `ECHO_SANDBOX_SETUP_RECIPE` for recipes that need to locate adjacent workspace files.

## Commands, Git, LSP, and tools

For an enabled workspace, Echo routes integrated terminals, `shell_command`, every Git subprocess, LSP processes, builds, tests, and formatters to the workbench. Host-side path-confined file reads/edits/searches and file watching continue to operate on the canonical bind mounts. LSP `file:` URIs and workspace edits are translated recursively between host paths and stable guest paths, including Windows drives, spaces, Unicode names, and multi-root workspaces.

Git UI rendering remains in Echo, but hooks, filters, credential helpers, and external diff programs execute only in the workbench. Native plugin tools and Echo's host-level restart tool are unavailable for sandbox-enabled workspaces; there is no silent host escape. UI-only plugins and confined plugin authoring remain available.

`web_fetch` uses the authenticated loopback endpoint of the workspace egress gateway for the initial request and all redirects.

## Network policy

Workbench and Desktop have only the internal sandbox network and cannot route directly to the internet. The gateway rejects loopback, private, carrier-grade NAT, link-local, multicast, reserved/documentation, cloud-metadata, 6to4, and NAT64 destinations after every DNS resolution. This repeated check prevents a hostname from passing validation and later rebinding to a private address.

An owner can add a machine-local grant for one exact hostname or IP and one TCP port, with an optional non-reserved sandbox alias. Wildcards and subnets are invalid. Revocation rewrites the live gateway policy and takes effect without container recreation. A DNS answer does not bypass port enforcement.

Configured SearXNG and ComfyUI connections remain explicit Echo host services; enabling the sandbox does not silently expose the host to the guest.

## Desktop, takeover, and signed-in sessions

The Sandbox view renders noVNC's RFB canvas directly inside Echo. VNC itself is never exposed as a web application. Authenticated devices can observe the desktop, but only the workspace-wide lease owner can send input.

- The first graphical tool in a chat turn acquires the AI lease.
- **Take Control** immediately cancels in-flight AI GUI work and makes all other viewers read-only.
- **Return Control** releases the user lease. Canceled AI actions are not replayed.
- Taking control from another authenticated browser session requires confirmation.
- A connected controller does not expire; disconnecting starts a two-minute grace period.

Human VNC keystrokes—including passwords—do not become chat messages, tool arguments, or Trajectory entries. Echo shows a warning before the first desktop connection: once control is returned, the AI can use the authenticated Chromium profile with the authority of those signed-in sessions. Browser data is protected by host/Docker storage permissions rather than Echo encryption. Use **Reset Browser Data** to remove it independently.

Chromium runs headed with a persistent profile and a 1 GiB `/dev/shm`. The desktop applies Moby's default seccomp policy plus only `clone`, `setns`, and `unshare` for Chromium's user-namespace sandbox. Dangerous capabilities, privileged mode, raw networking, host namespaces, and host devices remain disabled.

## Building development images

From the repository root on a `linux/amd64` Docker Engine:

```bash
docker build --platform linux/amd64 -f sandbox/images/workbench/Dockerfile -t echo-sandbox-workbench:dev .
docker build --platform linux/amd64 -f sandbox/images/desktop/Dockerfile -t echo-sandbox-desktop:dev .
docker build --platform linux/amd64 -f sandbox/images/gateway/Dockerfile -t echo-sandbox-egress:dev .
ECHO_SANDBOX_INTEGRATION=1 go test ./internal/sandbox -run TestDockerIntegrationLifecycle -count=1 -v
```

Development builds intentionally use local `:dev` tags. Nightly CI builds/publishes all three images first, runs the real Docker acceptance test, scans each image, emits SPDX SBOM/license data, and embeds immutable `name@sha256:digest` references into Windows, Linux, and macOS binaries through Go linker values.

Each image carries an OCI source label that links its GHCR package to this public repository. GitHub creates a container package as private on its first publication, so the package owner must change each of the three packages to **Public** once in its GHCR package settings. CI uses a clean anonymous Docker configuration to verify every digest and the source-build `protocol-1` tags before releasing binaries; later releases fail closed if package visibility regresses.

The release-blocking Windows job expects a self-hosted runner labeled `self-hosted`, `Windows`, `X64`, and `echo-sandbox`, with Docker Desktop already running Linux containers. Echo's installer still installs only Echo.

## Common diagnostics

| Code/state | Meaning |
| --- | --- |
| `docker_unavailable` | Echo cannot reach the configured Docker Engine. |
| `docker_linux_engine_required` | Docker Desktop is in Windows-container mode. |
| `docker_architecture_unsupported` | The Engine is not `linux/amd64`. |
| `sandbox_images_missing` | Pull/build the three image references shown in the Sandbox view. |
| `workspace_mount_probe_failed` | Docker cannot mount/write a registered root or enforce the `.echo` mask. |
| `sandbox_protocol_mismatch` | Recreate with images built for this Echo protocol. |
| `setup_approval_required` | Review and approve the current `setup.sh` digest. |
| `user_control_active` | A browser session currently owns graphical input. |

Stopping Echo gracefully stops active sandbox groups. On its next start, Echo reconciles labeled containers, stops incompatible or orphaned groups, preserves their volumes for manual recovery, and rotates all memory-only runtime credentials.
