---
name: agent-mode-permission-configuration
description: How to author and configure per-tool path permissions in agent mode JSON files, including schema structure, read vs write tool restrictions, and prompt alignment.
triggers:
    - agent mode permissions
    - mode.json
    - per-tool permissions
    - tool permission schema
    - restrict tool paths
---

# Agent Mode Permission Configuration

## `mode.json` Permissions Schema
Agent modes store per-tool path constraints in the `permissions` map within `<workspace>/.echo/modes-{workspaceID}/<uuid>/mode.json`.

```json
{
  "id": "...",
  "name": "ModeName",
  "prompt": "...",
  "permissions": {
    "filesystem_edit_text": {
      "name": "filesystem_edit_text",
      "paths": [".echo/backlog.md"]
    },
    "filesystem_read_text": {
      "name": "filesystem_read_text"
    }
  }
}
```

## Configuration Rules
- **Per-tool map**: Keys are exact tool names. Values contain `name` (repeated for schema consistency) and an optional `paths` array.
- **Path restrictions**: Provide a glob pattern array to restrict where a tool can operate. If `paths` is omitted or empty, the tool has unrestricted access.
- **Read vs Write tools**: Read/search tools (`filesystem_read_text`, `filesystem_search_workspace`, etc.) typically require no path restrictions. Write/mutation tools (`filesystem_create_text`, `filesystem_edit_text`, `filesystem_delete_file`) should explicitly list allowed paths to prevent unintended modifications.
- **Prompt alignment**: The mode's `prompt` field must accurately reflect the permission boundaries (e.g., "You have full read access but can only write to `.echo/backlog.md`") so the LLM self-enforces constraints before invoking tools.

## Verification & Troubleshooting
- Permissions are enforced at runtime by `ToolScopeChecker` in `Registry.Execute`.
- Changes to `mode.json` require an Echo restart or frontend mode list refresh to take effect.
- Ensure modes reside in the workspace-scoped directory `.echo/modes-{workspaceID}/` rather than the legacy `.echo/modes/` directory, otherwise they will not be loaded by `catalogWorkspaceModes()`.
