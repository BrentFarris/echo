# Plugin security model

Plugins are optional code, not a weaker ownership tier for Echo core. The plugin manager owns every registration, associates it with a plugin ID and content digest, and can dispose it without affecting immutable core routes, services, or tools.

## Approval boundary

Install and update are two-phase operations. Echo first resolves a public GitHub ref to an immutable commit or snapshots a local folder, rejects unsafe content, validates the manifest/current platform, and hashes the package without running it. A host-rendered review then shows source, commit, digest, native-code warning, permissions, tools, settings, and changes from the installed digest.

Only an authenticated approval request can commit that stage. Agent tools can create a pending stage but cannot approve, enable, or execute it. Remote builds and repository install/lifecycle scripts are never run. Updates are manual and pinned.

## Native runtime trust

Native backends run as the Echo owner with normal operating-system permissions. `network`, `filesystem`, `process`, and `secrets` declarations communicate intent and form a review boundary; they are not an OS sandbox.

Echo reduces ambient authority by starting the executable directly, using a plugin data directory as its working directory, and passing a minimal environment without Echo credentials or unrelated variables. Resolved configuration and permitted workspace metadata are supplied only with an invocation. Disable/update/uninstall blocks new calls, closes UI sessions, cancels work, shuts down the process, and disposes registrations.

Treat native plugins like any executable downloaded from the internet. Review source and immutable commit where possible.

## UI isolation

Views run in an iframe with exactly `sandbox="allow-scripts"`; they receive an opaque origin and no forms, popups, downloads, same-origin access, or host DOM APIs. A restrictive plugin CSP disables network connections, embedding, navigation, and inline execution. Assets come from expiring tokenized, path-confined URLs with digest cache busting.

The host accepts bridge messages only from the exact iframe `Window` and checks the session token, nonce, plugin ID, view ID, active digest, and declared method. Notifications, clipboard writes, and external links require separately approved permissions; external links also require a user confirmation.

## Secrets and retained data

Secret values are never returned in catalogs, plugin UI initialization, workspace recipes, `registry.json`, API responses, trajectory records, or logs. The OS credential store is preferred. If unavailable, users may select an environment reference or a session-only value; there is no plaintext fallback. Runtime results/errors and stderr are filtered against resolved secret values as an additional defense.

Uninstall removes executable packages but retains plugin configuration/data by default. “Remove plugin data” is a distinct destructive action that deletes namespaced data, logs, configuration, and known credential-store entries.

## Recovery

Corrupt registries or packages fail closed while the core server continues to start. Repeated runtime crashes mark a plugin unhealthy until an explicit reload. Start Echo with `-safe-mode` to disable all optional plugins while keeping Settings and plugin management available.
