---
name: fossil-provider-performance
description: 'Rules for keeping the Fossil source-control provider fast: --hash semantics, O(checkout) cost of any `changes` call, the snapshot/records cache + background push architecture, and the frontend optimistic-status prediction.'
triggers:
    - fossil provider
    - fossil changes --hash
    - source control slow
    - rawStatus
    - describeHunks
    - protected changes latency
    - status cache
---

# Fossil provider performance contract (internal/sourcecontrol/fossil)

## `fossil changes` flag rules (verified empirically against fossil 2.29)
- **`--hash` is required for change detection.** Without it, fossil relies on recorded mtime/size and **silently misses same-size edits** (e.g. `xyz` → `wxy`), even with a fresh mtime. Never drop `--hash` from a `changes` call whose result feeds change detection.
- **`fossil changes` is O(checkout size) no matter how few paths it is scoped to** (~25µs/file measured; a 1-file scoped call costs the same as a full scan). `fossil info`/`finfo` stay O(1) (~30ms). This asymmetry drives the whole architecture: interactive requests must never wait on a `changes` spawn.
- **`fossil extras` is redundant**: `changes --differ` already lists extra (untracked) files with the `EXTRA` code. Never add a separate `extras` spawn.

## Cache architecture (status.go)
- `repositoryState` carries a status snapshot cache (keyed to the revision token) and a change-records cache, plus single-flight background refresh (`queueStatusRefresh`, throttled by `refreshMinInterval`) that pushes `source_control_status` via the notifier (wired by `sourcecontrol.Service.Register` through `NotifyingProvider`).
- **Caching is gated on `cachingAllowed` (active subscription)**: without a subscriber there are no file-event invalidations, so every status recomputes. This keeps direct-disk-mutation semantics (integration tests, external edits with no watcher) correct.
- `Status()` = recovery → fresh cache (≤2s) → stale-while-revalidate (serve last-known + background refresh + push) → synchronous compute (cold). `computeStatus` double-checks the cache under `computeMu` so concurrent callers share one checkout scan.
- `rawStatus`/`rawStatusPaths` answer from the records cache (TTL 5s) when fresh; `pathIsConflicted` (diff hot path) does too. Partial scoped results are never cached.
- `afterMutation` (actions.go, in the Action defer after the revision bump): checkpoint-only actions (protect/unprotect/hunks) **rebuild the snapshot from cached records + the just-written manifest** (`rebuildStatusCache`) so the next status is instant; checkout-changing actions invalidate everything (the service's async post-action push recomputes).
- `InvalidateWorkspace` bumps the revision and drops caches; recovery of an interrupted protected commit invalidates too (journal existed).
- `service.Action` pushes the post-action status **asynchronously** (goroutine) — never put a full VCS scan back on the action request path.

## Hot-path rules
- The file-click diff (`Diff` → `diffState` → `describeHunks`) must not run checkout-wide commands: conflict check via records cache, F-card permissions via `cachedManifestPermissions` (provider-level memo keyed by revision+path; fossil artifacts are immutable).
- `loadStatus` runs `fossil info` and `changes --hash` concurrently; the hash pass is the one allowed O(checkout) op (background refresh only).
- Measured at 20k files (provider-level probe): status 1855ms→~0ms cached, diff 1303ms→76ms, protect 5127ms→76ms, unprotect 3648ms→35ms.

## Frontend
- Fossil has `supportsOptimisticStatus` + `predictStatus` (`web/src/code/fossilState.ts`, mirrors `gitState.ts`): after a protect/unprotect/commit_protected/discard response, rows move groups immediately; pushes reconcile via the revision guard in `acceptStatus`. Prediction must only mirror guaranteed membership changes.
