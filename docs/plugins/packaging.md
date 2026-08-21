# Packaging and distribution

## Local development

A local install tracks a machine-local source directory but executes only immutable snapshots. Build the current-platform runtime artifact, validate, and stage the folder. “Reload” creates and validates another snapshot; a Settings reload is direct owner intent, while an agent-triggered reload remains pending approval. Permission expansion always receives full review. Failed activation restores the previous working snapshot.

Local paths never enter `.echo/plugins.json`. Replace the source with a public GitHub source before sharing a workspace recipe.

## Public GitHub source

GitHub installation accepts a public `owner/repository`, ref, and optional subdirectory. Echo resolves the ref to a commit, downloads the repository archive, extracts it with path/entry/size limits, validates the package, and pins the immutable commit. Private credentials, arbitrary Git remotes, release installers, and remote builds are outside v1.

Commit usable artifacts for every platform you advertise. Echo does not run `npm install`, `go build`, `make`, Actions, or any repository hook. Checking for an update only stages the current ref as a candidate; nothing changes until approval.

## Workspace recipe

`.echo/plugins.json` records enabled plugin IDs, pinned public GitHub repository/commit/subdirectory, non-secret workspace configuration, and secret references. It never stores a local path or secret value. Missing packages produce an installation prompt rather than an automatic download. A different installed commit for the same ID is an inactive conflict because v1 permits one installed version per ID.

Effective activation is global enabled **or** workspace enabled. A repository cannot turn off a globally enabled utility.

## CI template

The scaffold command emits `.github/workflows/build-plugin.yml`. The [showcase workflow](../../examples/plugins/showcase/.github/workflows/build-plugin.yml) is a complete example that tests a Go backend, cross-compiles explicit targets, and assembles a package with the manifest, documentation, assets, UI, and binaries.

Before publishing:

1. Pin and audit dependencies.
2. Test backend protocol errors, cancellation, timeouts, and shutdown.
3. Test the iframe under Echo's CSP and exact sandbox flags.
4. Validate every referenced path and the package on each target.
5. Confirm no build output, logs, fixtures, or defaults contain secrets.
6. Commit artifacts to the installable repository/subdirectory and use immutable commit links in documentation.
