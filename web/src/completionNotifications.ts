import notificationSoundUrl from "../notification-1.wav";
import { get } from "../js/api.js";
import * as ws from "../js/ws.js";
import {
  CHAT_ROUTE, CODE_ROUTE, chatCompletionRouteHash, routePathFromHash,
  type ChatCompletionTarget,
} from "./navigation";

type CompletionSettings = {
  disableNotificationSounds?: boolean;
  enableChatCompletionNotifications?: boolean | null;
};

export type ChatCompletedMessage = ChatCompletionTarget & {
  type: "chat_completed";
  workspaceName: string;
  turnId: string;
  preview?: string;
  completedAt: string;
};

type VisibleSession = ChatCompletionTarget;
export type CompletionNotificationPermission = NotificationPermission | "unsupported";

let soundsEnabled = true;
let notificationsEnabled = true;
let started = false;
let permissionRequest: Promise<CompletionNotificationPermission> | null = null;
let visibleSession: VisibleSession | null = null;

const notificationSound = typeof Audio === "function" ? new Audio(notificationSoundUrl) : null;
if (notificationSound) notificationSound.preload = "auto";

function settingsValue(value: unknown): CompletionSettings {
  if (value && typeof value === "object" && "settings" in value) {
    return ((value as { settings?: CompletionSettings }).settings || {});
  }
  return (value as CompletionSettings | null) || {};
}

export function updateCompletionNotificationSettings(value: unknown): void {
  const settings = settingsValue(value);
  soundsEnabled = settings.disableNotificationSounds !== true;
  notificationsEnabled = settings.enableChatCompletionNotifications !== false;
}

export async function refreshCompletionNotificationSettings(): Promise<void> {
  try {
    updateCompletionNotificationSettings(await get("/api/settings"));
  } catch (error) {
    console.warn("failed to load completion notification settings", error);
  }
}

export function completionNotificationPermission(): CompletionNotificationPermission {
  return typeof Notification === "undefined" ? "unsupported" : Notification.permission;
}

export async function requestCompletionNotificationPermission(): Promise<CompletionNotificationPermission> {
  const permission = completionNotificationPermission();
  if (permission !== "default") return permission;
  if (permissionRequest) return permissionRequest;
  permissionRequest = (async () => {
    try {
      return await Notification.requestPermission();
    } catch {
      return "unsupported";
    } finally {
      permissionRequest = null;
    }
  })();
  return permissionRequest;
}

/** Call synchronously from a user-initiated send so browsers may show their permission prompt. */
export function prepareCompletionNotificationPermission(): void {
  if (notificationsEnabled && completionNotificationPermission() === "default") {
    void requestCompletionNotificationPermission();
  }
}

function playCompletionSound(): void {
  if (!soundsEnabled || !notificationSound) return;
  const audio = notificationSound.cloneNode(true) as HTMLAudioElement;
  void audio.play().catch(() => {
    // Browsers can reject audio before the user has interacted with Echo.
  });
}

function elementIsDisplayed(element: HTMLElement | null): boolean {
  for (let current: HTMLElement | null = element; current; current = current.parentElement) {
    if (current.hidden) return false;
  }
  return element !== null;
}

export function isCompletedChatVisible(message: ChatCompletedMessage): boolean {
  if (document.visibilityState !== "visible" || !document.hasFocus()) return false;
  if (!visibleSession
    || visibleSession.workspaceId !== message.workspaceId
    || visibleSession.chatId !== message.chatId
    || visibleSession.surface !== message.surface) return false;

  const route = routePathFromHash(window.location.hash);
  if (message.surface === "code") {
    return route === CODE_ROUTE
      && elementIsDisplayed(document.querySelector<HTMLElement>("[data-code-chat-dock]"));
  }
  return (route === CHAT_ROUTE || route === "/")
    && elementIsDisplayed(document.querySelector<HTMLElement>("[data-chat-view-pane='chat']"));
}

function notificationBody(message: ChatCompletedMessage): string {
  const workspace = message.workspaceName?.trim() || "Echo";
  const preview = String(message.preview || "").trim().replace(/\s+/g, " ");
  if (!preview) return workspace;
  return `${workspace} — ${preview.length > 160 ? `${preview.slice(0, 157)}…` : preview}`;
}

function showCompletionNotification(message: ChatCompletedMessage): void {
  if (!notificationsEnabled || isCompletedChatVisible(message)) return;
  if (completionNotificationPermission() !== "granted") return;
  try {
    const notification = new Notification(message.surface === "code" ? "Code Chat ready" : "Chat ready", {
      body: notificationBody(message),
    });
    notification.onclick = () => {
      notification.close();
      window.focus();
      window.location.hash = chatCompletionRouteHash(message);
    };
  } catch {
    // Permission and platform support can change while Echo is open.
  }
}

function handleSessionSnapshot(value: object): void {
  const message = value as Record<string, unknown>;
  const surface = message.surface === "code" ? "code" : "chat";
  const workspaceId = typeof message.workspaceId === "string" ? message.workspaceId : "";
  const chatId = typeof message.activeChatId === "string"
    ? message.activeChatId
    : typeof message.chatId === "string" ? message.chatId : "";
  visibleSession = workspaceId && chatId ? { workspaceId, chatId, surface } : null;
}

function handleChatCompleted(input: object): void {
  const value = input as Record<string, unknown>;
  const surface = value.surface === "code" ? "code" : value.surface === "chat" ? "chat" : null;
  if (!surface || typeof value.workspaceId !== "string" || typeof value.chatId !== "string") return;
  const message: ChatCompletedMessage = {
    type: "chat_completed",
    workspaceId: value.workspaceId,
    workspaceName: typeof value.workspaceName === "string" ? value.workspaceName : "Echo",
    surface,
    chatId: value.chatId,
    turnId: typeof value.turnId === "string" ? value.turnId : "",
    preview: typeof value.preview === "string" ? value.preview : "",
    completedAt: typeof value.completedAt === "string" ? value.completedAt : "",
  };
  playCompletionSound();
  showCompletionNotification(message);
}

export async function startCompletionNotifications(): Promise<void> {
  if (!started) {
    started = true;
    ws.on("session_snapshot", handleSessionSnapshot);
    ws.on("chat_completed", handleChatCompleted);
  }
  await refreshCompletionNotificationSettings();
}
