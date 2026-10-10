---
name: workspace-agent-modes
description: How workspace-scoped agent modes and the per-workspace default mode for new chat tabs are stored, served, and consumed.
triggers:
    - agent modes
    - agent-modes.json
    - default mode
    - new tab mode
---

# Workspace agent modes

- Storage: per-workspace `.echo/agent-modes.json` (via `internal/agentmodes`). The on-disk shape is `{"defaultModeId": "...", "modes": [...]}` (`agentmodes.store`). `defaultModeId` is optional/omitted when unset; `load()` also migrates the legacy bare-JSON-array format in memory, so never assume the file is an array when reading it.
- `defaultModeId` is the workspace-scoped default agent mode for new chat tabs. Unset/invalid values resolve to `general` (`agentmodes.GeneralID`). `Manager.SetDefault` validates the ID against built-ins + custom modes; explicitly choosing General stores an empty value (setting omitted). `Manager.Delete` clears the default if it pointed at the deleted mode.
- API: `GET /api/agent-modes?workspaceId=` returns `{workspaceId, modes, tools, defaultModeId}`. `PUT /api/agent-modes/default` with `{workspaceId, modeId}` sets the default (registered as a literal path in `server.go` so it does not collide with `PUT /api/agent-modes/{id}`).
- Frontend consumers: `web/js/views/home.js` (`defaultAgentModeId` module var; used by `restoreCurrentComposer` for new tabs and as fallback for invalid carry-overs) and `web/src/chatSurface.ts` (preselects `data.defaultModeId` on the mode select; saved drafts still win via `restoreDraft`). Settings UI lives in `renderAgentModes` in `web/js/views/settings.js` (`data-default-mode` select).
