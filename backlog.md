# Backlog

## Custom agent modes — workspace specificity

**Status:** Already implemented ✅

Custom agent modes are already workspace-specific:

- **Backend storage**: Per-workspace disk storage in `<workspace>/.echo/modes/<uuid>/mode.json` (`internal/services/workspace_agent_modes.go`)
- **Frontend state**: `state.agentModes` is a `Map<string, AgentMode[]>` keyed by workspace ID; modes reload on workspace switch
- **Global state removed**: Legacy `AppState.AgentModes` was removed from `state.json`; migration helper exists for existing installs

No further action required. If this item was raised due to observed global behavior, investigate whether the specific symptom is in a different layer (e.g., agent mode tool creation during Kanban execution not scoping to workspace).
