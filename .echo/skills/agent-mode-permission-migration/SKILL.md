---
name: agent-mode-permission-migration
description: Agent mode permissions migrated from flat lists to per-tool map with ToolScopeChecker enforcement in registry.
triggers:
    - agent mode
---

# Agent Mode Permission Migration

## Overview

`AgentMode` uses `Permissions map[string]tools.ToolPermission` instead of flat `ToolPermissions []string` and `PathPermissions []string`. Legacy fields remain for backward-compatible API responses.

## Key types

- `AgentMode.Permissions` — per-tool permission map (`internal/services/agent_modes.go`)
- `tools.ToolPermission` — has `Name string` and `Paths []string`; stores raw glob patterns (not compiled matcher) for JSON serialization
- `tools.ToolScopeChecker` — enforces per-tool+path permissions at execution time

## Migration flow

1. `migrateAgentMode(mode)` converts legacy flat lists → Permissions map; clears legacy fields
2. `UnmarshalJSON` on AgentMode calls `migrateAgentMode` automatically on load
3. `buildPermissionsMap(toolPermissions, pathPermissions)` creates new Permissions maps for Create/Update
4. `cloneAgentModes` populates both Permissions and legacy fields from Permissions for API backward compatibility

## Enforcement

- `checkPermissions` in `registry.go` prefers `ctx.ToolScopes` (ToolScopeChecker); falls back to legacy ToolPermissions/PathPermissions
- `buildToolScopes(permissions)` converts Permissions map → ToolScopeChecker
- `executeTrackedToolCall` passes only `toolScopes *ToolScopeChecker`; callers build from mode.Permissions

## Important files

- `internal/services/agent_modes.go` — AgentMode struct, migration, helpers
- `internal/tools/permissions.go` — ToolPermission, ToolScopeChecker, PathMatcher
- `internal/tools/registry.go` — checkPermissions enforcement
- `internal/services/file_changes.go` — executeTrackedToolCall signature
- `internal/services/chat.go` — chat execution flow

## Pitfalls

- ToolPermission stores raw `Paths []string`, not a compiled `*PathMatcher`, because PathMatcher has unexported fields and won't serialize to JSON
- `permissionsMapToolNames` sorts tool names; tests must not assume insertion order
- Legacy fields are populated from Permissions in cloneAgentModes for backward-compatible API responses
