# Echo acceptance gates

Run `build.ps1 -Release` from the repository root on Windows. It must succeed before distributing a release. The script requires the native toolchains instead of silently skipping missing installations. It builds `echo.exe` and `echo-windows-amd64.zip` on Windows AMD64. The previous application stays in the ignored `OLD/` reference folder and is excluded from release packaging.

For Linux/macOS, run `sh build.sh --release` on the destination OS. Linux requires bubblewrap and usable user namespaces; macOS requires Seatbelt. `.github/workflows/native.yml` defines native Windows/Linux/macOS regression jobs. Cross-compilation is a build check, not runtime acceptance. Do not mark a native platform accepted because its named browser fixture passed on another OS.

## Swarm acceptance

The Go integration fixtures use separate profiles, secret stores and HTTP servers; transport unit tests exercise actual duplex links. Browser acceptance runs three Echo instances and one deterministic model, with independent remote resources.

| Area | Automated evidence |
|---|---|
| Routing/execution | Three identically named Kaiju bots; remote-only dispatch without an entry model; per-machine overrides; remote Echo creates a workspace-bound bot only on its own host |
| Coordination | Bidirectional handoffs; fixed participant rosters and authority checks; competing homes release partial reservations and finish without overlap/deadlock; transactional send/reply/drain delivery hooks |
| Identity/topology | Persistent node identity; shared name collisions and rename-safe saved references; A–B–C relay discovery; password boundaries; routing cycles; duplicate and simultaneous reciprocal connections |
| Authentication | Pairing requires a password on loopback; explicit activation; discovery cannot enable a disabled/disconnected peer; incompatible versions fail; revocation removes all grants; directory redaction; receiver challenge rejects captured request frames |
| Recovery | Lost/repeated acceptance; home restart with independent worker completion; worker restart interrupted without rerunning tools; durable stop/delete during outage; no late history resurrection; source checkpoint rollback/stale rejection |
| Owner isolation | Entry identity survives remote-home dispatch; a relay's credentials cannot substitute for origin authorization; worker tokens cannot call owner state; cross-host file references do not grant local filesystem access |
| Browser | Existing remote chats and machine chips; simultaneous local/remote Monaco; dirty buffers through disconnection and owner-specific saves; remote iframe storage bridge; selected-host schedules/models; remote Development reconnect without page reload |

Useful focused commands:

```sh
go test ./internal/swarm ./internal/coordinator ./internal/store -count=1
go test ./internal/server -run TestSwarm -count=1 -timeout=5m
# Race instrumentation makes WASI fixture construction substantially slower.
go test -race ./internal/swarm ./internal/coordinator ./internal/store -count=1
go test -race ./internal/server -run TestSwarm -count=1 -timeout=15m
cd web && npm run test:e2e -- e2e/swarm.spec.ts
```

Native Linux testing was performed in Ubuntu under WSL: the clean build, Go tests/vet, 223 frontend tests, 54 browser tests, bubblewrap containment, private-profile denial, filtered egress/direct bypass rejection, descendant cancellation, scoped Go builds/Git operations, WASI authoring, Unix vault persistence and build/relaunch behavior. Linux race checks cover transport, durable coordination/storage and representative multi-host dispatch/recovery. Windows native acceptance covers the existing toolchain/SCM/credential suites plus the Swarm suites. macOS native runtime acceptance remains required on an actual Mac; no Mac runtime was available during this implementation. The Darwin ARM64 cross-build must not be presented as a Seatbelt/runtime test.

## Automated coverage

- Apps: explicit registration and icon validation; UI template and HTML-only authoring; registration digests and rollback; bot-scoped launch checks; authenticated, serialized dock mutations and restart persistence; cross-browser dock synchronization; singleton embedded/floating views without iframe state resets; inactive/pending status and retained pins; registration removal and restoration; draft/attachment/thread preservation during streaming; square icons, keyboard/wheel scrolling and mobile layout.

ComfyUI coverage includes the ported workflow/client suite, actual WASI invocation against mock ComfyUI servers, text/image/video workflow selection, inline/path/default precedence, all template parameters, attached-image uploads, nested output paths and custom GIF storage, persisted media/downloads, workspace saves and overwrite protection, bot/folder isolation, disabled-plugin cancellation, and model image hydration with metadata-only video results. Browser acceptance checks image/video/GIF results outside Show work, previews/downloads, reload, mobile layout, and the five plugin settings. These tests use local mock servers and do not require a GPU or a running ComfyUI installation.

- Go: compressed incremental persistence, interrupted work, deletion without resurrection, directory locking, scope boundaries, optimistic file revisions, explicit handoffs beyond 32 requests, duplicate requests, cycle detection, model runs beyond 128 rounds, cancellation, per-bot histories, provider availability, WASI capability checks, no ambient filesystem/environment, termination, plugin revocation, origin/authentication gates and credential redaction across stream chunks.
- Calendar: DST gaps/folds, local-day intervals, persisted missed reviews, occurrence deduplication, disabled schedules, transactional selected/skipped batches and concurrency `1`/`0`.
- Chat input/model settings: real clipboard image paste, attachment picker/removal/preview, draft isolation when switching chats, attachment-only sends, persisted previews after reload, deletion of attachment bytes with chats, multimodal provider payloads, safe text download headers, cross-chat/recipient isolation, composer growth to half height on desktop/mobile, upward menus and keyboard dismissal, a unified send/stop icon, endpoint parameter persistence, sampling overrides, configurable compression, request deadlines and idle heartbeat handling.
- Conversation presentation: completed work folds behind each bot's final reply, reasoning and tools remain inspectable, keyboard expansion survives subsequent messages, reload reconstructs the disclosure, handoffs stay attributed to separate bots, and stopped/failed/unfinished runs do not hide partial work. Desktop and 390-pixel mobile screenshots verify compact cards, with no routine completion badges or duplicate tool headings.
- Browser: chat-driven bot creation/listing/deletion; keyboard `@`/`#` picking and a referenced file edit; stream recovery after reload; stop/deletion; missed-job selection, unlimited batches and Skip all; bot grouping and a correlated API handoff; generated draggable calculator with opaque-origin isolation; hostile Markdown sanitization; 121 visible/reachable sidebar entries; dark colors and a 390-pixel mobile layout; schedules surviving chat deletion.
- Monaco: legacy unit coverage for preview/permanent/pinned tabs, MRU, tree reconciliation/selection, search, previews, bookmarks, outline, navigation, indentation and conversions. Backend coverage includes bot/root isolation, empty/revoked roots, traversal/junction escapes, private-state exclusion, revision conflicts, create-only concurrency, UTF-8 BOM/EOL, binary/large files, media classification, trash collisions/restoration, filtered search/replace with overlays, and watcher release. Real-Monaco browser coverage checks bot-isolated buffers, undo across presentation switches, recovery, scoped hotkeys and Ctrl+D, file creation/rename/trash/restore, conflict comparison/overwrite, bookmarks, bundled TypeScript outline, search replacement, preferences, previews, external updates and desktop/mobile layout. App regression tests cover iframe identity and chat draft/attachment/thread/stream preservation during embedded navigation.
- Windows: scoped PowerShell reads/writes; denied sibling files and junction escape; native child containment; cancellation and ACL journal cleanup; Git reftable init/add/commit/status; Go build/run; Node execution; .NET SDK/MSBuild build/run; protected credential round-trip/deletion; and Go/WASI backend-template compilation within AppContainer.

The deterministic model is only a test fixture. These tests validate harness behavior and tool execution, not a real model's ability to choose the right tools or implement a game feature. Real-model smoke tests require a configured local/cloud connection and representative projects.

## Supported configuration limits

Changed-file summaries cover direct and plugin file operations and bounded native-command snapshots, including partial effects from failed/stopped work. Tests check net-change grouping, safe filenames, persistence/restart, deletion, workspace revocation, rejected cross-chat/unrecorded paths, remote reveal rejection, and keyboard/mobile presentation outside Show work. Native reveal uses Windows Shell item identifiers to select files; deleted paths resolve to an existing allowed containing folder.

Native scoped Shell commands have no network capability and require dependencies/toolchains already available within their grants. Git/Fossil/P4 can receive owner-granted Internet/private-network capabilities without removing filesystem isolation. Native tests verify the actual capability SIDs and denied sibling reads, literal argument preservation and standard input, real scoped Git/Fossil init/add/commit/diff workflows, and a temporary Perforce depot with client forms, add, submit, edit and diff. Perforce networking is tested using the unbound actor because Windows blocks AppContainer loopback by default; its client also runs inside AppContainer for version checks. Settings tests cover permission expansion denial, per-bot toggles, protected ticket storage, output redaction and credential invalidation on destination changes.

On Windows, existing Git loose-ref repositories and tools requiring denied ancestor metadata can fail explicitly; new scoped Git repositories use reftable. No automatic repository conversion, loopback exemption or unrestricted fallback is performed. Portable toolchain copies are bounded to 512 MiB and reject links. Linux/macOS native tools use their platform sandbox and filtered HTTP(S)/SOCKS egress; tools that ignore proxy settings cannot make direct network connections. Unix SSH/Perforce network compatibility is not covered by the Windows depot acceptance test.

Custom WASI providers and iframe renderers use the documented host contracts. Acceptance includes native compilation and streaming through a provider, endpoint and credential confinement, rejection before dispatch when a recipient disables the provider, hostile custom-renderer output, and per-bot plain-text fallback. Other provider protocols use bounded HTTP responses; compatible endpoints use native SSE streaming.

Echo listens on port 3740 by default and retains the `%LOCALAPPDATA%\Echo2` Windows profile. Unix uses the platform's user configuration directory with the same `Echo2` basename. Exercise the actual configured models and intended projects on each target OS and check their toolchain compatibility. Startup services are opt-in and must be tested separately under the intended user account. Legacy source and local files remain available in `OLD/` for reference.
