---
name: orchestration-groups-dispatch
description: 'How the Orchestration page works end-to-end: workspace groups persisted in echo.json, the group dispatcher that starts Group Worker chats server-side, the chat-composer-style queue input, and the reusable EchoMapView embed/filter options.'
triggers:
    - orchestration
    - workspace group
    - pending task
    - group worker
    - dispatch
    - sendOrchestrated
    - workspace_groups_changed
    - chat-composer
---

# Orchestration: workspace groups + pending-task dispatch

## Storage
- `internal/workspacegroups` wraps the shared `*appdata.Store`; groups live as one JSON payload in `appdata.File.WorkspaceGroups` (json.RawMessage). All mutations go through `data.Update`. `RemoveWorkspace(wsID)` strips the workspace from every group and deletes groups left with 0 members (called from `handleDeleteWorkspace`).

## API (standard envelope)
- `GET/POST /api/workspace-groups`, `PUT/DELETE /api/workspace-groups/{id}`
- `POST /api/workspace-groups/{id}/tasks`, `PUT/DELETE .../tasks/{taskId}` (task fields: prompt, model, reasoningEffort, temperature, ready, dispatchedWorkspaceId/ChatID)
- Every mutation broadcasts `workspace_groups_changed` over the WS hub.

## Group Worker mode
- 5th immutable built-in in `internal/agentmodes` (`GroupWorkerID = "group-worker"`): Pair Programming prompt + branch-off-trunk / merge-trunk-in / squash-merge-back / delete-branch workflow.

## Dispatcher (`internal/server/orchestrator.go`)
- `groupOrchestrator` loop (5s ticker + `kick()` from task mutations and turn finishes; `stop()` in `Server.Shutdown`). `dispatchReadyTasks` sends each ready, undispatched task to the first workspace in the group with no in-flight turns (`sessions.workspaceBusy`) and no active Group Worker (`sessions.groupWorkerBusy`).
- `sessions.sendOrchestrated(workspaceID, msg)` creates a background tab (`createBackgroundTab`, does NOT change activeChatID) and calls `send(nil, msg)` — nil client is safe: `commandErrorFor*` no-op and `sendSnapshot` returns early.
- `inboundMessage.LockAgentMode` (orchestrated sends) skips the decision-model agent-mode override and turns a Group-Worker-busy collision into a silent skip (retry next pass) instead of a command error.

## Frontend
- Route `#/orchestration` → `web/src/orchestration/orchestrationView.ts`; nav button `data-nav="orchestration"` (codicon-organization) sits under the map button in `primaryNav.ts`; shortcut Ctrl+Shift+O in `primaryNavShortcuts.ts`. Every view's bindNavigation must wire `data-nav=orchestration` (home.js, settings.js, codeView.ts, sandboxView.ts, echoMapView.ts, pluginHost.ts).
- `EchoMapView` accepts `EchoMapViewOptions { workspaceFilter?, embed? }` — embed mode renders only card sections into the root (no shell), used by the Orchestration page filtered to group member IDs.
- The bottom composer reuses the global `.chat-composer` classes from `web/css/app.css` (loaded for every route via `web/main.ts`): contenteditable `[data-chat-input]` editor (placeholder via `:empty::before` + `data-placeholder`), plus/mic (`mountSpeechRecognition` from `src/speechRecognition.ts`), `.model-selector` triggers, and `.send-button`. Model + mode dropdowns are fixed-position `.model-dropdown` overlays appended to `document.body` (positioned above the trigger), mirroring home.js. The mode dropdown lists Group Worker selected and all other built-ins `disabled` + `.is-locked`, plus the same thinking ladder (`thinkingLadder`, 0-6 slider) and temperature slider (0-1 step 0.05, seeded from the endpoint's temperature). There is intentionally no 3-dot menu and no attachments (plus button toasts).

## Testing notes
- Orchestrator tests must `t.Cleanup(s.orchestrator.stop)` (now in `newTestServer`) and stop the loop before releasing gated streamers, or the turn-finish kick dispatches tasks after assertions and races TempDir cleanup.
- `newTestServer` sets a real LLM client via `initLLM`; set `s.llm = nil` explicitly to test the no-LLM path.
