
import notificationSoundUrl from "../../notification-1.wav";
import { state, notificationSoundsEnabled, chatCompletionNotificationsEnabled, kanbanCompleteNotificationsEnabled } from "./state";

const notificationSound = new Audio(notificationSoundUrl);
notificationSound.preload = "auto";

export function playNotificationSound() {
  if (!notificationSoundsEnabled(state.appState?.settings ?? state.settingsDraft)) {
    return;
  }
  const audio = notificationSound.cloneNode(true) as HTMLAudioElement;
  void audio.play().catch(() => {
    // Browsers can reject audio until the user has interacted with the app.
  });
}

/* ── Push notifications (Web Notification API) ── */

/** Request notification permission if not already requested. Returns the resulting permission. */
export async function requestPushNotificationPermission(): Promise<NotificationPermission> {
  if (typeof Notification === "undefined") {
    return "denied";
  }
  if (Notification.permission === "granted") {
    return "granted";
  }
  if (Notification.permission === "denied") {
    return "denied";
  }
  try {
    const result = await Notification.requestPermission();
    return result;
  } catch {
    return "denied";
  }
}

/** Resolve a workspace display name from the current app state. */
function workspaceDisplayName(workspaceID: string): string {
  const ws = state.appState?.workspaces?.find((w) => w.id === workspaceID);
  return ws?.displayName ?? "Echo";
}

/** Send an OS-level push notification if permission is granted and the setting is enabled. */
export function sendPushNotification(title: string, body: string): void {
  if (typeof Notification === "undefined") return;
  if (Notification.permission !== "granted") return;
  try {
    // Try to use the app icon from the document; fall back to omitting it.
    const icon = document.querySelector<HTMLLinkElement>('link[rel="icon"]')?.href
      ?? document.querySelector<HTMLLinkElement>('link[rel="apple-touch-icon"]')?.href
      ?? undefined;
    new Notification(title, { body, icon });
  } catch {
    // Notification may fail if the tab is hidden or permission changed.
  }
}

/** Trigger a push notification for chat completion (called from event handler). */
export function maybeSendChatCompletionNotification(workspaceID: string): void {
  const settings = state.appState?.settings ?? state.settingsDraft;
  if (!chatCompletionNotificationsEnabled(settings)) return;
  // Request permission on first trigger so the prompt appears contextually.
  void requestPushNotificationPermission();
  sendPushNotification("Echo", `Chat response ready for ${workspaceDisplayName(workspaceID)}`);
}

/** Trigger a push notification for Kanban scheduler completion (called from event handler). */
export function maybeSendKanbanCompleteNotification(workspaceID: string): void {
  const settings = state.appState?.settings ?? state.settingsDraft;
  if (!kanbanCompleteNotificationsEnabled(settings)) return;
  void requestPushNotificationPermission();
  sendPushNotification("Echo", `All Kanban cards completed for ${workspaceDisplayName(workspaceID)}`);
}
