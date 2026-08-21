# Plugin troubleshooting

## Validation fails

- Confirm `echo-plugin.json` is strict JSON with `manifestVersion: 1`, a semantic version, lowercase kebab-case IDs, and `echo.api` compatible with `^1`.
- Build the runtime path for the current `<goos>-<goarch>` target. Echo never builds it for you.
- Keep every entry/icon/executable under the package root. Remove symlinks and special files.
- Prefix every tool name with the normalized plugin ID and check for duplicates or core tool collisions.
- Use the supported bounded JSON Schema subset: object/array/scalar types, properties, required, additionalProperties, items, oneOf, enum, pattern, and size/range constraints.

`echo_plugin_validate` reports the current target and digest without installing or executing the package.

## Plugin is installed but inactive

Check Settings → Plugins for global/workspace enablement, safe mode, target compatibility, required configuration, health, missing workspace recipes, or pinned-commit conflicts. Global enablement wins over a workspace's disabled entry. A workspace pin that differs from the one installed version remains inactive until the conflict is resolved through staging and review.

## Backend does not start

Open the bounded plugin log in Settings. Common causes are a missing/wrong-architecture artifact, absent Unix executable permission, stdout banners, malformed/multiline JSON-RPC, a handshake that omits `echo-jsonrpc-1`, or a process that exits before replying. Repeated crashes quarantine the runtime; fix the source and explicitly reload it.

Never print secrets to debug. Echo filters already-resolved values, but plugins are responsible for not persisting or transforming sensitive material.

## UI stays blank or bridge calls fail

Use external local JS/CSS files; inline code and remote connections are blocked by CSP. The iframe must announce `echo-plugin-ready`, accept initialization only from `parent`, and echo the exact nonce/plugin/view identity on each call. UI RPC method names must appear in `contributes.rpc`. Notifications, clipboard, and external links require explicit permissions.

If a plugin was disabled or reloaded, its old token and iframe are intentionally invalid. Reopen the view to create a new session.

## Secrets are missing

The OS credential store may be unavailable in a headless session. Select an environment reference or enter a session-only value. Environment references contain only the variable name; set the variable before starting Echo. Session values disappear on restart. Echo intentionally has no plaintext-file fallback.

## Recover Echo

Start with `echo -safe-mode` (or `echo.exe -safe-mode` on Windows). Core features and plugin management remain available while all optional contributions and runtimes stay disabled. You can inspect logs, reject stages, disable/uninstall a package, or remove retained data, then restart normally.
