# Plugin authoring

## Package layout

An installable plugin is a self-contained directory:

```text
echo-plugin.json
README.md
LICENSE
assets/
ui/<view>/index.html
ui/<view>/app.js
ui/<view>/style.css
backend/<goos>-<goarch>/<executable>
```

Source, tests, and CI configuration may also be present. Echo never invokes them during installation. All declared entries must be regular files under the package root; absolute paths, traversal, symlinks, special files, oversized archives, duplicate contributions, core route overrides, and core tool collisions fail validation.

## Manifest

`echo-plugin.json` is strict JSON. Unknown fields are rejected so misspelled security-sensitive declarations do not silently pass. The published [API v1 JSON Schema](echo-plugin.schema.json) is suitable for editor and CI validation; Echo's package validator remains authoritative for on-disk paths, files, namespace ownership, and collisions with the installed core tool set.

```json
{
  "manifestVersion": 1,
  "id": "example-plugin",
  "name": "Example Plugin",
  "version": "1.0.0",
  "description": "A narrowly scoped Echo extension",
  "echo": { "api": "^1" },
  "permissions": [],
  "runtime": {
    "protocol": "echo-jsonrpc-1",
    "targets": {
      "windows-amd64": {
        "path": "backend/windows-amd64/example-plugin.exe",
        "args": []
      }
    }
  },
  "contributes": {
    "views": [],
    "tools": [],
    "settings": [],
    "rpc": []
  }
}
```

IDs are lowercase kebab-case and versions are semantic versions. Runtime target keys are explicit `windows-amd64`, `linux-amd64`, `linux-arm64`, `darwin-amd64`, or `darwin-arm64` entries. Commands are started directly, without a shell. A native plugin must contain a usable artifact for the machine on which it is installed.

Plugin dependencies, arbitrary server routes, host CSS/JavaScript injection, core replacement, settings UI, and lifecycle hooks are not part of v1.

## Contributions

A view has a lowercase kebab-case ID, `page` or `floating` kind, title, local HTML entry, optional local SVG/PNG/WebP icon, and optional default/minimum dimensions. All v1 views are singleton. Page routes use `#/plugins/<plugin-id>/<view-id>`; floating windows persist across core route changes.

A tool declares a unique name prefixed by its normalized plugin ID (`example-plugin` → `example_plugin_`), description, object input JSON Schema, optional output schema, backend method, timeout, and `readOnly` or `mutating` classification. Echo validates arguments/results and enforces limits. Plan and research never receive plugin tools; restricted custom modes must name them explicitly.

Settings use `string`, `url`, `number`, `boolean`, `select`, or `secret`, with `global` or `workspace` scope. The host renders and validates every field. Secret defaults are forbidden. A secret may come from the OS credential store, an environment-variable reference, or session memory.

RPC contributions are the exact backend methods a sandboxed UI may invoke. Undeclared methods are rejected before runtime dispatch.

## Chat-driven workflow

Echo merges the read-only built-in skill `builtin/echo-plugins` into every workspace. When asked to build a plugin, the agent should:

1. Call `echo_plugin_scaffold` for a `ui-only`, `tool-only`, or `hybrid` workspace-confined source folder.
2. Implement and test with normal workspace tools and `shell_command`.
3. Call `echo_plugin_validate` to inspect compatibility, permissions, contributions, and digest.
4. Call `echo_plugin_stage` to create a real pending stage.
5. Stop at the trusted approval card. Only the authenticated owner can install and optionally enable the snapshot.

`echo_plugin_status` reports installed, pending, missing, conflicting, and effective state. None of these authoring tools can approve, enable, or execute newly staged code.
