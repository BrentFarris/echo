---
name: agent-modes-infrastructure
description: Tool permission enforcement in Registry.Execute validates tool names and path arguments against ExecutionContext allowlists before running handlers. ToolScopeChecker provides unified per-tool path constraints.
triggers:
    - agent mode
    - permission helper
    - path matcher
    - tool permission
    - glob matching
    - allowlist
    - ExecutionContext permissions
    - checkPermissions
    - extractWorkspacePaths
    - ToolScopeChecker
    - ToolPermission
    - NewToolScopeChecker
---

## Tool Permission Enforcement at Execution Time

### ExecutionContext permissions (`internal/tools/types.go`)

`ExecutionContext` carries three permission-related fields:

- **`ToolScopes *ToolScopeChecker`** — unified per-tool permission and path-scope checker. Use this instead of the legacy flat fields. Each `ToolPermission` entry pairs a tool name with an optional `*PathMatcher`.
- **`ToolPermissions *ToolPermissionChecker`** — deprecated; flat tool-name allowlist. Marked `json:"-"`.
- **`PathPermissions *PathMatcher`** — deprecated; global path glob allowlist. Marked `json:"-"`.

When all three are nil (default), no restrictions apply — backward compatible.

### ToolScopeChecker (`internal/tools/permissions.go`)

```go
type ToolPermission struct {
    Name        string
    PathMatcher *PathMatcher  // nil = allow all paths for this tool
}

type ToolScopeChecker struct { /* private */ }
```

- `NewToolScopeChecker([]ToolPermission)` — empty slice = allow all.
- `Allowed(toolName, path) bool` — checks tool membership then per-tool path scope. Empty path skips path evaluation. Nil receiver returns true.
- `HasTool(toolName) bool` — reports whether a tool has an explicit entry.

### Enforcement in Registry.Execute (`internal/tools/registry.go`)

Before looking up or running a tool, `Execute()` calls `checkPermissions(ctx, name, arguments)`:

1. **Tool allowlist check** — if `ctx.ToolPermissions` is non-nil and the tool name is not allowed, returns `tool_not_allowed` error immediately without invoking the handler.
2. **Path scope check** — if `ctx.PathPermissions` is non-nil, extracts workspace-relative paths from arguments via `extractWorkspacePaths()`, then validates each against the glob matcher. Returns `path_not_allowed` on first mismatch.

Both checks return structured `ExecutionError` with code and message; the underlying tool handler is never invoked on rejection.

**Migration note:** `checkPermissions` currently uses only the legacy `ToolPermissions`/`PathPermissions` fields. A follow-up card should update it to prefer `ToolScopes.Allowed(toolName, path)` for unified evaluation.

### Path extraction (`extractWorkspacePaths`)

- Decodes JSON arguments via `DecodeToolArguments`.
- Identifies path fields by key name: `path`, `workingDirectory`, `repository`, `base`, `revision`, `target` (via `isPathArgKey()`).
- For labeled paths (e.g., `echo/src/main.go`), resolves against workspace roots and extracts the relative portion. Uses a `matched` flag to avoid falling through to the plain-path fallback when a label was found.
- For plain relative paths, trims `./` prefix and skips `.` entries.

### Service wiring (`internal/services/file_changes.go`)

`executeTrackedToolCall` accepts `toolPermissions *tools.ToolPermissionChecker` and `pathPermissions *tools.PathMatcher` as parameters and passes them into `ExecutionContext`. All callers (chat, kanban, inline code) currently pass `nil, nil`; the infrastructure is ready for AgentMode-based enforcement.

### Error codes

| Code | Meaning |
|---|---|
| `tool_not_allowed` | Tool name not in current mode's allowlist |
| `path_not_allowed` | Path argument outside current mode's path globs |

### Pitfalls

- `extractWorkspacePaths` must track whether a labeled root matched — otherwise the fallback adds the full labeled path as an extra entry that won't match workspace-relative globs.
- Tool check runs before path check; `tool_not_allowed` takes precedence over `path_not_allowed`.
- Permission checkers are nil-safe: nil means "allow all" at each level independently.
- Legacy `ToolPermissions` and `PathPermissions` on `ExecutionContext` are deprecated with `json:"-"` tags; new code should populate `ToolScopes` instead.
