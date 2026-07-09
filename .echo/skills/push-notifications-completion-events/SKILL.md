---
name: push-notifications-completion-events
description: How to add Web Notification API push notifications for completion events in Echo, including backend settings fields, frontend notification module, event wiring, and settings UI toggles.
triggers:
    - push notifications
    - Web Notification API
    - browser notifications
    - completion notifications
    - chat completion notification
    - kanban complete notification
    - notification settings
    - OS-level alerts
---

## Push notifications for chat/Kanban completion

Echo uses the Web Notification API to send OS-level alerts when chat responses complete and when all Kanban cards finish. Each notification type has an independent toggle in Settings > Notifications.

### Architecture

**Backend settings** (`internal/llm/settings.go`):
- `EnableChatCompletionNotifications bool` — defaults to true (enabled)
- `EnableKanbanCompleteNotifications bool` — defaults to true (enabled)
- Both use direct-positive naming and `omitempty`; Go zero value is false, so the frontend helper defaults to enabled when undefined.

**Frontend notification module** (`frontend/src/app/notifications.ts`):
- `requestPushNotificationPermission()` — requests permission once; stores `pushNotificationPermissionRequested` flag to avoid spamming.
- `sendPushNotification(title, body)` — checks `Notification.permission === 'granted'`, creates `new Notification()`.
- `maybeSendChatCompletionNotification(workspaceID)` — checks settings, requests permission, sends notification with workspace display name.
- `maybeSendKanbanCompleteNotification(workspaceID)` — same pattern for Kanban scheduler_complete events.

**Event wiring**:
- Chat: In `applyChatStreamEvent` in `frontend/src/app/chat/index.ts`, calls `maybeSendChatCompletionNotification(event.workspaceId)` on type `"complete"`.
- Kanban: In `applyKanbanEvent` in `frontend/src/app/kanban/index.ts`, calls `maybeSendKanbanCompleteNotification(event.workspaceId)` on type `"scheduler_complete"`.

**Settings UI** (`frontend/src/app/settings/index.ts`):
- Three toggles in Notifications section: sounds (existing), chat completion, kanban complete.
- `renderPushNotificationPermissionStatus()` shows a re-prompt link when permission is denied or default.
- Action `request-push-notification-permission` wired in `actions.ts`.

**State helpers** (`frontend/src/app/state.ts`):
- `chatCompletionNotificationsEnabled(settings)` — returns true unless explicitly false (defaults enabled).
- `kanbanCompleteNotificationsEnabled(settings)` — same pattern.

### Key patterns

1. **Default-on booleans**: Use direct-positive naming (`enableX`) and check `!== false` in helpers so the feature is enabled by default when the field is absent from persisted state.
2. **Permission gating**: `sendPushNotification` silently no-ops if permission isn't granted; `maybeSend*` functions request permission on first trigger so the prompt appears contextually.
3. **Workspace display name**: Resolved from `state.appState?.workspaces` by ID; falls back to "Echo".
4. **Wails model bindings**: New settings fields must be added to both the Go struct and the TypeScript class in `frontend/wailsjs/go/models.ts` (both the property declaration and constructor assignment).

### Hazards

- Web Notification API requires secure context (HTTPS/localhost). Wails uses `wails://` which may behave differently per browser engine.
- Notifications may be suppressed when the tab is not visible on mobile Safari/Chrome.
- The `Notification` constructor must be wrapped in try/catch as it can throw if permission changes or the tab is hidden.
