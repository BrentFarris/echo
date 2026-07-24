---
name: multi-chat-tabs-architecture
description: Explain and safely modify Echo's tabbed multi-chat architecture, including per-workspace tab state, tab-scoped chat sessions and streams, frontend compound keys, tab lifecycle RPCs, event routing, autosave migration, Wails/web transport, and concurrency behavior. Use for chat tabs, multiple simultaneous chats, ChatWorkspaceState, ChatTabSummary, chatStateKey, tab-scoped chat operations, or the multi-chat refactor.
triggers:
    - multi-chat
    - chat tabs
    - ChatWorkspaceState
    - ChatTabSummary
    - CreateChatTab
    - ActivateChatTab
    - CloseChatTab
    - LoadChatWorkspace
    - chatStateKey
    - activeChatIDFor
    - tab-scoped chat
    - concurrent chat streams
---

# Multi-Chat Tabs Architecture

## Start with the ownership model

Treat a workspace's chat state as an ordered collection of independent sessions:

```text
workspace
  -> chatWorkspaceState
       -> ActiveChatID
       -> TabIDs (display and fallback order)
       -> Sessions[chatID]
            -> messages and LLM history
            -> preview
            -> busy state and stream ID
            -> revision
```

The refactor changed the identity of a chat from `workspaceID` to
`workspaceID + chatID`. A workspace still has one active tab, but inactive tabs
remain live and may continue streaming.

Preserve these invariants:

- Keep at least one chat tab per workspace.
- Keep every `TabIDs` entry present in `Sessions`.
- Keep `ActiveChatID` present in both `TabIDs` and `Sessions`.
- Treat `TabIDs` order as the UI tab order and persistence order.
- Keep each session's messages, history, busy state, stream, preview, and
  revision independent.
- Route delayed or concurrent work by `chatID`; never assume the currently
  active tab owns it.

## Use this file map

| File | Responsibility |
|---|---|
| `internal/services/chat.go` | Define chat workspace/session models, tab lifecycle methods, compatibility wrappers, stream routing, cloning, and lookup helpers. |
| `internal/services/state_persistence.go` | Persist ordered chat workspaces, restore tabs, repair invalid IDs, and migrate legacy single-chat state. |
| `internal/services/system.go` | Own chat maps, initialize them, load workspace autosaves, clean up deleted workspaces, and report workspace activity. |
| `internal/services/chat_tabs_test.go` | Test lifecycle, active-tab restoration, final-tab replacement, simultaneous streams, and isolated busy-tab cancellation. |
| `internal/services/decomposition.go` | Execute a plan from a specified chat tab. |
| `internal/services/kanban.go` | Create a Kanban card from a message in a specified tab. |
| `internal/services/agent_mode_from_chat.go` | Synthesize an agent mode from a specified tab. |
| `internal/services/workspace_skill_from_chat.go` | Synthesize a workspace skill from a specified tab. |
| `internal/services/file_changes.go` | Carry `ChatID` in chat tool provenance so tab-specific attachments and change records use the correct conversation. |
| `internal/services/chat_research.go` | Find the session owning a research message before emitting tab-scoped events. |
| `internal/services/kanban_scheduler.go` | Cancel and normalize every chat session during shutdown and persist all tabs in completion autosaves. |
| `internal/webserver/server.go` | Allow the tab lifecycle and tab-scoped chat methods through authenticated web RPC. |
| `frontend/src/app/state.ts` | Store workspace summaries and per-tab client state under compound keys. |
| `frontend/src/app/chat/index.ts` | Render and operate tabs, send tab-scoped requests, apply snapshots, route events, and patch only the visible tab. |
| `frontend/src/app/actions.ts` | Route plan execution and message actions through the active chat ID. |
| `frontend/src/app/bootstrap.ts` | Load the active workspace's chat workspace, subscribe to chat events, and key dropped media to the active tab. |
| `frontend/src/app/tasks/index.ts` | Write task-generated prompts and references into the active tab's draft. |
| `frontend/src/backend/services.ts` | Route handwritten chat wrappers through Wails on desktop or web RPC in a browser. |
| `frontend/src/app/types.ts` | Include `chatId` and optional `workspaceState` in the frontend event type. |
| `frontend/src/styles.css` | Style the conditional tab strip, busy marker, close confirmation, and three-row tabbed panel layout. |
| `frontend/wailsjs/go/...` | Contain generated bindings and models. Never edit these files by hand. |

## Keep backend workspace state authoritative

Use the internal types in `internal/services/chat.go`:

```go
type chatWorkspaceState struct {
    WorkspaceID  string
    ActiveChatID string
    TabIDs       []string
    Sessions     map[string]*chatSessionState
}

type chatSessionState struct {
    WorkspaceID string
    ChatID      string
    Preview     string
    Messages    []ChatMessage
    History     []llm.Message
    Busy        bool
    StreamID    string
    Revision    uint64
}
```

Treat `SystemService.chatWorkspaces` as the multi-chat source of truth.
`SystemService.chatSessions` remains a workspace-to-active-session alias for
legacy code and tests. Keep that alias synchronized whenever a tab is created,
activated, closed, restored, or lazily initialized.

Use the existing helpers instead of indexing the maps ad hoc:

- `ensureChatWorkspaceLocked(workspaceID)` lazily creates or upgrades a
  workspace to one tab and synchronizes the active-session alias.
- `ensureChatSessionLocked(workspaceID)` returns the active session.
- `chatSessionForIDLocked(workspaceID, chatID)` returns the requested session,
  or the active session when `chatID` is empty.
- `chatSessionByMessageLocked` and `chatSessionByStreamLocked` recover the
  owning tab for asynchronous callbacks.
- `newChatSessionLocked` allocates a globally unique `chat-N` ID.
- `cloneChatWorkspace` returns ordered summaries plus a full clone of only the
  active session.

Do not return the internal session pointers. Continue using clone helpers for
messages, media, tool calls, research state, and LLM content parts.

## Preserve the public wire models

Use these exported models as the backend/frontend contract:

- `ChatSession` includes `workspaceId`, `chatId`, `preview`, messages, busy
  state, stream ID, and revision.
- `ChatTabSummary` includes `chatId`, `preview`, `busy`, and `revision`.
- `ChatWorkspaceState` includes `workspaceId`, `activeChatId`, ordered `tabs`,
  and the full `activeSession`.
- `ChatStreamEvent` includes `chatId` and may carry either a `session` snapshot
  or a `workspaceState` snapshot.

Return `"New chat"` as the display preview for a blank summary while retaining
the empty internal preview. Set the preview from the first sent message using
`chatPreview`.

When an exported model or `SystemService` signature changes, run
`wails generate`. Then update the handwritten frontend service wrapper and web
RPC allowlist. Never hand-edit `frontend/wailsjs`.

## Follow the tab lifecycle

Use `LoadChatWorkspace` for initial workspace hydration and after full
workspace-level changes. It returns all tab summaries and the active session.
Use `LoadChatSessionForTab` to repair or explicitly load one tab.

`CreateChatTab` must:

1. Validate the workspace.
2. Allocate a blank session.
3. append its ID to `TabIDs`.
4. Make it active and update the `chatSessions` compatibility alias.
5. Return and emit a `ChatWorkspaceState` with type `tab_created`.
6. Persist the workspace autosave.

`ActivateChatTab` must:

1. Reject an unknown chat ID.
2. Change only `ActiveChatID` and the active-session alias.
3. Leave every stream and inactive session untouched.
4. Return and emit a workspace snapshot with type `tab_activated`.
5. Persist the active-tab choice.

`CloseChatTab` must:

1. Reject a blank or unknown chat ID.
2. Cancel and remove only that tab's stream.
3. Remove the session and its ordered ID.
4. Keep the current active tab when an inactive tab closes.
5. Select the preceding surviving tab when possible if the active tab closes;
   otherwise select the first remaining tab.
6. Create a fresh blank tab when the final tab closes.
7. Update the active-session alias, emit `tab_closed`, and autosave.

Keep the frontend busy-tab confirmation. Closing a busy tab intentionally
cancels its request, but must not cancel or block streams in other tabs.
Support both the close button and middle-click on a tab.

## Keep old APIs as active-tab compatibility wrappers

Preserve the existing workspace-only methods for callers that have not adopted
chat IDs. They delegate with an empty `chatID`, which resolves to the active
tab.

Use the explicit variants for all new frontend work:

| Compatibility method | Tab-scoped method |
|---|---|
| `LoadChatSession` | `LoadChatSessionForTab` |
| `SendChatMessageWithAttachments` | `SendChatMessageWithAttachmentsToTab` |
| `StopChatStream` | `StopChatStreamForTab` |
| `ClearChat` | `ClearChatForTab` |
| `PruneChatMessage` | `PruneChatMessageForTab` |
| `RetryChatMessage` | `RetryChatMessageForTab` |
| `EditChatMessage` | `EditChatMessageForTab` |
| `ExecutePlan` | `ExecutePlanForTab` |
| `CreateKanbanCardFromChatMessage` | `CreateKanbanCardFromChatMessageForTab` |
| `CreateAgentModeFromChat` | `CreateAgentModeFromChatForTab` |
| `CreateSkillFromChat` | `CreateSkillFromChatForTab` |

Capture the intended `chatID` before starting an asynchronous operation and
pass it through every layer. Do not re-read `ActiveChatID` after an `await` or
inside a goroutine to decide which session receives a result.

## Allow concurrent streams without cross-talk

Key `SystemService.chatStreams` by `chatID`, not `workspaceID`. Store `Busy`,
`StreamID`, and `Revision` on each session. This permits multiple tabs in one
workspace to stream simultaneously.

Pass `chatID` into `runChatTurn`, chat history helpers, media lookup, tool
execution provenance, and downstream generation flows. When only a message or
stream ID is available, locate its owning session with
`chatSessionByMessageLocked` or `chatSessionByStreamLocked`.

Always populate `ChatStreamEvent.ChatID`. The frontend depends on it to update
an inactive tab without repainting the active tab. Keep message and stream IDs
globally unique through the shared `chatSeq`; lookup-by-message remains
unambiguous across tabs.

During workspace deletion, shutdown, or full chat cleanup, iterate every
session in `chatWorkspaces`. Do not clean up only the active-session alias.

## Key frontend state by workspace and chat

Use `activeChatIDFor(workspaceID)` to read the active ID from
`state.chatWorkspaces`.

Use `chatStateKey(workspaceID, chatID)` for every per-tab map:

```ts
export function chatStateKey(
  workspaceID: string,
  chatID = activeChatIDFor(workspaceID),
): string {
  return `${workspaceID}\0${chatID}`;
}
```

The NUL separator prevents ambiguous string concatenation. Keep these values
tab-scoped:

- cached `ChatSession`
- text draft
- image and video drafts
- composer/plan mode
- selected agent mode
- in-progress plan decomposition
- in-progress skill generation
- queued stream patches and session reloads

Keep genuinely workspace-scoped state, such as Kanban boards, tasks, workspace
settings, and the selected Chat/Kanban view, keyed only by `workspaceID`.

When a workspace snapshot removes a tab, delete all of that tab's client-only
state. `applyChatWorkspaceSnapshot` performs this cleanup centrally; explicit
close handling also clears the closed key defensively.

## Apply snapshots before rendering

Use `applyChatSessionSnapshot` for a full session response:

1. Form the compound key from the snapshot's own workspace and chat IDs.
2. Reject a snapshot with a lower revision than the cached session.
3. Replace that tab's cached session.
4. Update its summary preview, busy state, and revision.

Use `applyChatWorkspaceSnapshot` for lifecycle responses:

1. Convert raw transport data through `services.ChatWorkspaceState.createFrom`.
2. Remove local state belonging to deleted tabs.
3. Replace the workspace summary.
4. Apply the included active-session snapshot.

Load the active workspace through `LoadChatWorkspace` during initialization and
workspace switching. Do not initialize multi-chat state through only
`LoadChatSession`, because that loses tab order and active-tab identity.

## Route stream events to the owning tab

In `applyChatStreamEvent`:

- Apply `workspaceState` events as lifecycle snapshots and patch the active
  workspace's panel.
- Apply full `session` events to that session's compound key.
- Use `event.chatId` for incremental message events.
- Update the matching tab summary even when the tab is inactive.
- Patch the DOM only when both the workspace and chat ID match the visible
  panel.

Keep revision repair scoped to the tab. Ignore stale revisions. If an event
skips a revision or references a missing message, call
`LoadChatSessionForTab(workspaceID, chatID)`.

Key `pendingChatStreamPatches` and `chatSessionReloads` by the compound key.
Before applying a delayed patch, re-check the active workspace, active chat ID,
and `[data-chat-panel]` chat ID. A user may have switched tabs while the patch
was queued.

## Preserve the conditional tab UI

Render the tab strip only when the workspace has at least two tabs. With one
tab, retain the original two-row chat panel. With multiple tabs, add
`has-chat-tabs` and use three grid rows:

```text
tab strip
scrolling chat log
composer
```

Create a new tab from the composer's three-dot menu. Keep the menu available
while the current tab is busy so users can open another tab and work
concurrently.

Render each tab with:

- its preview or `New chat`
- active styling
- a busy dot independent from the active tab
- an accessible activation button
- an accessible close button

Keep tab titles ellipsized, the strip horizontally scrollable, and the mobile
width/padding overrides at the 720px breakpoint. Update the active tab's busy
dot in `patchChatControls` without rebuilding the entire app for each token.

## Keep persistence backward-compatible

Persist chat state per workspace in `.echo/autosave.json` with autosave version
2. `workspaceAutosave` contains:

- `chatWorkspace`: the authoritative multi-tab snapshot
- `chatSession`: the active session duplicated for older readers
- `kanbanCards`

Persist `persistedChatWorkspace.Sessions` in `TabIDs` order and store
`ActiveChatID` separately. Do not persist runtime-only `Busy`, `StreamID`,
cancel functions, or active research-agent indicators.

On restore:

- Prefer `chatWorkspace` when present.
- Fall back to the legacy `chatSession` and wrap it in a one-tab workspace.
- Migrate legacy global `state.json` chat sessions into workspace autosaves.
- Allocate IDs for sessions that predate `ChatID`.
- Repair duplicate IDs, an empty session list, and an invalid active ID.
- Normalize interrupted streaming/retrying/compacting messages as canceled.
- Rebuild `chatSessions[workspaceID]` as the restored active-session alias.

Continue writing both persistence fields until compatibility is deliberately
removed in a separate migration.

## Maintain desktop and web parity

Add every exported tab method to all transport surfaces:

1. Implement the `SystemService` method in Go.
2. Run `wails generate`.
3. Add or keep the handwritten wrapper in `frontend/src/backend/services.ts`.
4. Add the method name to `allowedRPCMethods` in
   `internal/webserver/server.go`.
5. Update the frontend transport types when events or models change.

Keep lifecycle and stream events flowing through the same
`echo:chat:event` channel for Wails runtime events and web SSE. Do not build a
desktop-only tab path.

## Avoid common regressions

- Do not key drafts, media, modes, sessions, decomposition, or stream patches
  by only `workspaceID`.
- Do not use the active-session compatibility alias for explicitly targeted
  tab operations.
- Do not decide event ownership from the tab that happens to be active when an
  asynchronous callback finishes.
- Do not cancel every workspace stream when closing one tab.
- Do not hide the New tab action merely because the active tab is busy.
- Do not render incremental events from an inactive tab into the visible DOM.
- Do not discard inactive sessions when returning or applying a workspace
  lifecycle snapshot.
- Do not let closing the final tab leave a workspace without a chat.
- Do not persist runtime cancel functions, busy flags, or stream IDs.
- Do not remove legacy workspace-only RPCs without checking existing callers.
- Do not forget the web RPC allowlist.
- Do not manually edit generated Wails bindings.

## Verify changes

Run focused backend tests:

```powershell
go test ./internal/services -run 'ChatTabs|ConcurrentChatTabs'
```

Run the frontend build for state, rendering, event, style, or wrapper changes:

```powershell
cd frontend
npm run build
```

Regenerate bindings first when exported Go types or methods changed:

```powershell
wails generate
```

Run broader checks when the change crosses persistence, lifecycle, tools,
Kanban, or transport boundaries:

```powershell
go test ./...
cd frontend
npm run build
```

Manually verify:

1. Create a second tab from the three-dot menu.
2. Preserve different drafts, attachments, and modes while switching tabs.
3. Start streams in two tabs and confirm both busy dots update.
4. Switch tabs during streaming and confirm content never crosses tabs.
5. Close one busy tab and confirm only its request is canceled.
6. Close the active, inactive, first, middle, and final tabs.
7. Restart Echo and confirm tab order, previews, messages, and active tab.
8. Repeat lifecycle and concurrent-stream checks through authenticated web
   access.
