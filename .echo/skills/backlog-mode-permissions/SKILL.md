---
name: backlog-mode-permissions
description: Permission constraints for Backlog mode, granting full read/inspect access while restricting writes exclusively to .echo/backlog.md.
triggers:
    - backlog mode
    - backlog permissions
    - restricted write
---

# Backlog Mode Permissions

Backlog mode enforces a strict read-heavy permission set with highly restricted write capabilities to prevent unintended side effects during planning and backlog management.

## Allowed Capabilities (Full Workspace Access)
- **Read/Inspect**: List directories, read text/images/video files, search text across the workspace.
- **Metadata & Navigation**: Inspect file metadata (`stat`), Git history inspection (`git_inspect`), LSP code navigation (`lsp_query`).
- **External/Context**: Web fetch, web search, workspace context retrieval, and skill lookup.

## Restricted Capabilities
- **Write Access**: Strictly limited to creating, editing, or deleting files at `.echo/backlog.md`.
- **Blocked Tools**: `shell_command`, `restart`, `create_agent_mode`, `workspace_skill_record`.
- Any tool attempting to write outside of `.echo/backlog.md` is blocked.

## Usage Guidance
Use this permission profile when the agent needs to analyze the codebase, research web resources, and manage backlog items without risking filesystem modifications or executing arbitrary commands.
