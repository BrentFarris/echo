---
name: global-notifications
description: 'How Echo surfaces cross-surface/cross-tab alerts to the user: global WebSocket broadcasts (chat_completed, plan_questions_awaiting) mirrored by frontend notification modules that play sounds, show OS/browser Notifications, and deep-link on click.'
triggers:
    - notification
    - chat_completed
    - plan_questions_awaiting
    - global broadcast
    - notification sound
    - deep-link chat tab
---

# Global notifications (cross-surface / cross-tab alerts)

Echo has two kinds of alerts that must reach the user even when they are NOT looking at the affected chat tab or surface. Session-scoped `session_event`s (`tool_call` with `status: awaiting_input`, etc.) are only broadcast to clients subscribed to that workspace+surface+active tab, so they CANNOT be used for global alerts. Instead Echo uses **global Hub broadcasts** + **frontend listener modules**.

## The chat-completion pattern (model to copy)
- Backend (`internal/server/chat_sessions.go`): on turn finish with `status == "done"`, call `s.manager.server.hub.Broadcast(chatCompletedMessage{...})` with `type: "chat_completed"`, `workspaceId`, `workspaceName`, `surface` (`chat|code`), `chatId`, `turnId`, `preview`. `Hub.Broadcast` (in `ws.go`) fans out to **all** clients (no subscription needed).
- Frontend (`web/src/completionNotifications.ts`, started in `web/js/app.js` bootstrap via `startCompletionNotifications()`): listens `ws.on("chat_completed", ...)`, plays `notification-1.wav` sound, and shows a browser `Notification`. Clicking it does `window.location.hash = chatCompletionRouteHash({workspaceId, chatId, surface})`, which deep-links to the exact chat tab. Tab activation is already handled: `home.js` (chat surface) and `codeView.ts` (code surface, `?chat=open`) read `chatCompletionTargetFromHash` and call `activateChatTab`/`expectedChatId` + `onExpectedChatResolved`.
- `chatCompletionRouteHash`/`chatCompletionTargetFromHash` live in `web/src/navigation.ts`.

## Plan-question notifications (mirrors the completion pattern)
When a Plan-mode `ask_user_questions` call starts awaiting input:
- Backend `chat_sessions.go` (~line 2103) broadcasts `planQuestionsMessage` with `type: "plan_questions_awaiting"` + the same target metadata (`workspaceId`, `workspaceName`, `surface`, `chatId`, `turnId`, `callId`) plus `questions` (array of `{id, question, options}`). This is additive; the session-scoped `tool_call` event remains for the active-view rendering.
- Frontend `web/src/planQuestionNotifications.ts` (started via `startPlanQuestionNotifications()` in `app.js`) listens `ws.on("plan_questions_awaiting", ...)`, plays the question sound (delegated to `playPlanQuestionSound()`), and shows a notification. Clicking deep-links via `chatCompletionRouteHash` like completions.

## Settings toggles (web/js/views/settings.js `state.messaging`, persisted in internal/llm/settings.go)
- `notificationSounds` -> `disableNotificationSounds` (global sound master)
- `planQuestionSounds` -> `disablePlanQuestionSounds` (governs the question sound only; honored inside `playPlanQuestionSound()`)
- `planQuestionNotifications` -> `enablePlanQuestionNotifications` (`*bool`, defaults enabled) — governs the plan-question OS notification
- `chatCompletionNotifications` -> `enableChatCompletionNotifications` (`*bool`)
When a toggle changes, settings.js calls the matching `update...Settings(buildSettings())` and requests browser permission if enabled. On send, `prepareCompletionNotificationPermission()` and `preparePlanQuestionNotificationPermission()` are called from `home.js` and `chatSurface.ts` so the browser may show its permission prompt on a user gesture.

## Test patterns
- Backend: `internal/server/plan_questions_test.go` `TestPlanQuestionsPauseBroadcastsGlobally` — an **unsubscribed** observer client receives `plan_questions_awaiting` via `readUntilMessageType`, mirroring `TestSuccessfulChatCompletionBroadcastsExactInactiveAndCodeTargets` in `shared_sessions_test.go`.
- Frontend: `web/src/planQuestionNotifications.test.ts` mocks `ws.on`, `api.get`, and `./planQuestionSound`; asserts sound always plays, notification gating, and click deep-link. `settingsNotifications.test.ts` asserts the toggles persist the right settings keys.

When adding a new global alert, follow this exact pipeline: backend `hub.Broadcast(<struct>)` -> `app.js` `start...()` -> frontend listener module -> settings toggle + `update...Settings` + permission request + deep-link reuse.
