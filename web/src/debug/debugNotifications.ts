import * as ws from "../../js/ws.js";
import { getActive, setActiveWorkspace } from "../../js/workspaces.js";
import { codeRouteHash } from "../navigation";
import type { DebugLocation } from "./types";

const preferenceKey = "echo.debug.stop-notifications.v1";
const pending = new Map<string, { message: DebugStoppedMessage; timer: number }>();
const ignored = new Set<string>();
const shown = new Set<string>();
let started = false;
let permissionRequest: Promise<DebugNotificationPermission> | null = null;

export type DebugNotificationPermission = NotificationPermission | "unsupported";

export type DebugStoppedMessage = {
  type: "debug_stopped";
  phase: "stopped" | "location";
  workspaceId: string;
  sessionId: string;
  groupId?: string;
  configuration: string;
  stopGeneration: number;
  stoppedReason?: string;
  stoppedText?: string;
  location?: DebugLocation;
};

export function debugStopNotificationsEnabled(): boolean {
  try { return window.localStorage.getItem(preferenceKey) !== "false"; }
  catch { return true; }
}

export function setDebugStopNotificationsEnabled(enabled: boolean): void {
  try { window.localStorage.setItem(preferenceKey, enabled ? "true" : "false"); }
  catch { /* Browser storage is optional. */ }
}

export function debugStopNotificationPermission(): DebugNotificationPermission {
  return typeof Notification === "undefined" ? "unsupported" : Notification.permission;
}

export async function requestDebugStopNotificationPermission(): Promise<DebugNotificationPermission> {
  const permission = debugStopNotificationPermission();
  if (permission !== "default") return permission;
  if (permissionRequest) return permissionRequest;
  permissionRequest = (async () => {
    try { return await Notification.requestPermission(); }
    catch { return "unsupported"; }
    finally { permissionRequest = null; }
  })();
  return permissionRequest;
}

export function debugWindowNeedsAttention(): boolean {
  return document.visibilityState !== "visible" || !document.hasFocus();
}

function stopKey(message: DebugStoppedMessage): string {
  return `${message.workspaceId}:${message.sessionId}:${message.stopGeneration}`;
}

function remember(collection: Set<string>, key: string): void {
  collection.add(key);
  while (collection.size > 200) collection.delete(collection.values().next().value!);
}

function sourceLabel(location?: DebugLocation): string {
  const value = String(location?.name || location?.path || "").trim();
  if (!value) return "";
  const parts = value.split(/[\\/]/);
  const name = parts[parts.length - 1] || value;
  return location?.line ? `${name}:${location.line}` : name;
}

function notificationTitle(message: DebugStoppedMessage): string {
  if (message.stoppedReason === "breakpoint" || message.stoppedReason === "function breakpoint" || message.stoppedReason === "data breakpoint" || message.stoppedReason === "instruction breakpoint") return "Breakpoint hit";
  if (message.stoppedReason === "exception") return "Debugger stopped on an exception";
  return "Debugger paused";
}

function notificationBody(message: DebugStoppedMessage): string {
  const parts = [message.configuration || "Debug session", sourceLabel(message.location), String(message.stoppedText || "").trim()].filter(Boolean);
  const body = parts.join(" — ");
  return body.length > 220 ? `${body.slice(0, 217)}…` : body;
}

function rememberSelectedSession(message: DebugStoppedMessage): void {
  try {
    const key = `echo.debug.ui.v1:${message.workspaceId}`;
    const current = JSON.parse(window.localStorage.getItem(key) || "{}");
    const preferences = current && typeof current === "object" && !Array.isArray(current) ? current : {};
    window.localStorage.setItem(key, JSON.stringify({ ...preferences, selectedSessionId: message.sessionId }));
  } catch { /* Browser storage is optional. */ }
}

function showNotification(message: DebugStoppedMessage): boolean {
  if (!debugStopNotificationsEnabled() || !debugWindowNeedsAttention() || debugStopNotificationPermission() !== "granted") return false;
  try {
    const notification = new Notification(notificationTitle(message), { body: notificationBody(message), tag: `echo-debug-stop:${message.workspaceId}:${message.sessionId}` });
    notification.onclick = () => {
      notification.close();
      rememberSelectedSession(message);
      window.focus();
      const openStoppedSession = () => { window.location.hash = codeRouteHash("debug"); };
      if (getActive()?.id === message.workspaceId) openStoppedSession();
      else void setActiveWorkspace(message.workspaceId).catch(() => undefined).finally(openStoppedSession);
    };
    return true;
  } catch {
    return false;
  }
}

function flush(key: string): void {
  const entry = pending.get(key);
  if (!entry) return;
  window.clearTimeout(entry.timer);
  pending.delete(key);
  remember(showNotification(entry.message) ? shown : ignored, key);
}

function handleDebugStopped(input: object): void {
  const value = input as Partial<DebugStoppedMessage>;
  if (value.type !== "debug_stopped" || (value.phase !== "stopped" && value.phase !== "location")
    || !value.workspaceId || !value.sessionId || !Number.isFinite(value.stopGeneration)) return;
  const message = value as DebugStoppedMessage;
  const key = stopKey(message);
  if (ignored.has(key) || shown.has(key)) return;

  if (message.phase === "stopped") {
    if (!debugStopNotificationsEnabled() || !debugWindowNeedsAttention()) {
      remember(ignored, key);
      return;
    }
    const previous = pending.get(key);
    if (previous) window.clearTimeout(previous.timer);
    pending.set(key, { message, timer: window.setTimeout(() => flush(key), 400) });
    return;
  }

  const previous = pending.get(key);
  if (!previous) return;
  window.clearTimeout(previous.timer);
  pending.set(key, { message, timer: window.setTimeout(() => flush(key), 0) });
}

export function startDebugStopNotifications(): void {
  if (started) return;
  started = true;
  ws.on("debug_stopped", handleDebugStopped);
}
