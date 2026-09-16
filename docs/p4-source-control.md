# Perforce in Source Control

Echo's built-in P4 provider uses the installed `p4` command-line client (2024.1 command set) and the same Source Control view and Monaco diff editor as Git and Fossil. No separate plugin is needed.

## Setup

1. Configure and authenticate your P4 client using your normal P4 configuration and tickets. Echo does not store a password or run an interactive login.
2. Open the client folder, or a mapped subfolder, as an Echo workspace folder.
3. In Source Control, use **More Source Control Actions → P4 Connection Settings** for optional server, user, and client overrides. Blank fields inherit native P4 configuration. These settings are machine-local.
4. Enable **automatic tracking** for the discovered client when you want Echo's direct file operations registered with P4. Discovery alone enables inspection.

P4 runs on the host. Sandboxed workspaces report P4 unavailable. Different server/user/client identities remain separate even when they share local paths.

## Changelists and diffs

**Default** remains visible when empty. Numbered pending changelists appear below it, including changelists that have never been shelved. Status comes from native `changes` and `opened` metadata, so files opened for edit remain visible even when unchanged.

Changelists start collapsed. Click a changelist header to expand or collapse it; status refreshes preserve your choices while the view is open.

Choose an **active changelist** for new operations. Existing opened files retain their assignments. Creating a changelist makes an empty list active; use **Move Selected to Changelist** separately to reassign files. Changelist menus provide description editing, checkout, revert, and revert-unchanged actions. Whole-list actions are disabled when files are hidden or results are incomplete.

Click a changed file to use the shared diff editor. Its base is the opened/synced revision, never depot head. Split/inline view, navigation, undo/redo, and editable working-copy buffers behave as they do for other providers. Hunk revert changes the buffer; saving performs checkout as needed. P4 exposes no stage/protect controls. Binary, non-UTF-8, and oversized content use the shared unavailable-file presentation. Resolve-needed state is displayed; resolve files with your normal P4 tools.

## Tracking direct work

Editor and diff saves, find/replace, explorer operations, structured text tools, generated-media saves, and `filesystem_move` use a common mutation coordinator.

- Existing tracked files are opened for edit before writing. A required checkout failure stops the write and leaves the editor buffer intact.
- Eligible new files are added after the local write, using native ignore rules and typemaps.
- Moves retain explicit source/destination pairs and native P4 move metadata, including existing changelist assignments. Directory operations inspect only the affected subtree.
- Deletions retain content in Echo Trash. Restoring a trash item restores its prior pending add/edit state where applicable.
- Revert requires confirmation and copies existing content into an application-data backup before running native P4 revert. Reverting an add leaves the local file present.

Cross-root/client moves, unmapped tracked destinations, directory operations containing symlinks, and unresolved or unsupported native move combinations are rejected before physical movement. These cases require review with native tools.

## Indirect work and reconciliation

Shell, terminal, and external file events become candidates in **Local changes awaiting P4**. They are not automatically registered. Choose **Reconcile Selected**, **Reconcile Echo Changes**, or **Scan Folder**, inspect the preview, and apply it. Applying a preview rechecks the exact reviewed file contents and P4 state. Empty selections never run a current-directory reconcile.

Enabling tracking installs bounded directory watches; it does not hash project contents or run reconciliation. Existing unopened changes need an explicit folder scan. Watcher gaps, coverage limits, restarts, and directory events mark local detection incomplete; Echo never launches a project-wide recovery scan automatically. Ordinary status requests query only changelists, opened files, and their metadata. While Source Control is subscribed, P4 metadata refreshes every five seconds, backing off to thirty seconds after failures.

## Recovery

If writing succeeds but P4 registration fails, Echo reports a successful file result with a pending-registration diagnostic. The exact affected paths remain visible in Source Control. Do not repeat the write. Use **Retry Selected Registrations**, or review a reconciliation preview after a concurrent P4V action. Recovery never replays file contents or silently switches a record to a different client or changelist.

If the client view changed, or its original mapping could not be recorded while offline, review reconciliation before registering the queued paths. Echo also stops Trash restoration if the saved P4 recovery record is damaged or another P4 action changed the file's pending assignment.

Machine-local settings, the atomic operation journal, and revert backups live beside `echo.json` under `source-control/p4/`. The journal is separate from Fossil protection manifests. Pending operations survive restart, tracking disablement, and connection changes. Backup folders include a `paths.json` manifest linking backup names to original paths. Connection failures retain the last status with a stale diagnostic.

## Agent tools

`source_control_inspect` accepts `provider: "p4"` and an optional `group` (`"default"` or a numbered changelist). P4 supports status and working-copy diff inspection. Unsupported history/revision operations return capability errors. `filesystem_move` accepts labeled `path` and `destination` values within one workspace root and validates both permission scopes and sandbox freshness.

## Scope and validation

Submit, shelving/shelf browsing, sync, submitted history, stream administration, and conflict resolution remain outside this integration.

The dedicated P4 CI workflow checks checksum-pinned P4 2024.1 binaries on Windows and Linux. `ECHO_REQUIRE_P4=1` makes missing fixtures a failure; `ECHO_P4D` can point local tests at a temporary `p4d` installation. Fixtures create disposable loopback servers, depots, users, clients, tickets, and configuration. They do not use your existing P4 server.

```sh
go test ./internal/sourcecontrol/p4 -v
cd web
npm test
npm run build
npx playwright test p4-diffs.spec.ts
```
