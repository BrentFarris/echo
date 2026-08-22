import { get } from "../js/api.js";
import * as ws from "../js/ws.js";
import {
  chatCompletionRouteHash, type ChatCompletionTarget,
} from "./navigation";
import { playPlanQuestionSound } from "./planQuestionSound";

type PlanQuestionNotificationSettings = {
  enablePlanQuestionNotifications?: boolean | null;
};

export type PlanQuestionsAwaitingMessage = ChatCompletionTarget & {
  type: "plan_questions_awaiting";
  workspaceName: string;
  turnId: string;
  callId: string;
  questions?: Array<{ id?: string; question?: string; options?: string[] }>;
};

export type PlanQuestionNotificationPermission = NotificationPermission | "unsupported";

let notificationsEnabled = true;
let started = false;
let permissionRequest: Promise<PlanQuestionNotificationPermission> | null = null;

function settingsValue(value: unknown): PlanQuestionNotificationSettings {
  if (value && typeof value === "object" && "settings" in value) {
    return ((value as { settings?: PlanQuestionNotificationSettings }).settings || {});
  }
  return (value as PlanQuestionNotificationSettings | null) || {};
}

export function updatePlanQuestionNotificationSettings(value: unknown): void {
  const settings = settingsValue(value);
  notificationsEnabled = settings.enablePlanQuestionNotifications !== false;
}

export async function refreshPlanQuestionNotificationSettings(): Promise<void> {
  try {
    updatePlanQuestionNotificationSettings(await get("/api/settings"));
  } catch (error) {
    console.warn("failed to load plan question notification settings", error);
  }
}

export function planQuestionNotificationPermission(): PlanQuestionNotificationPermission {
  return typeof Notification === "undefined" ? "unsupported" : Notification.permission;
}

export async function requestPlanQuestionNotificationPermission(): Promise<PlanQuestionNotificationPermission> {
  const permission = planQuestionNotificationPermission();
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
export function preparePlanQuestionNotificationPermission(): void {
  if (notificationsEnabled && planQuestionNotificationPermission() === "default") {
    void requestPlanQuestionNotificationPermission();
  }
}

function firstQuestionText(message: PlanQuestionsAwaitingMessage): string {
  const questions = Array.isArray(message.questions) ? message.questions : [];
  for (const question of questions) {
    const text = String(question?.question || "").trim();
    if (text) return text;
  }
  return "";
}

function notificationBody(message: PlanQuestionsAwaitingMessage): string {
  const workspace = message.workspaceName?.trim() || "Echo";
  const question = firstQuestionText(message);
  if (!question) return `${workspace} — Echo is waiting for your answers.`;
  return `${workspace} — ${question.length > 160 ? `${question.slice(0, 157)}…` : question}`;
}

function showPlanQuestionNotification(message: PlanQuestionsAwaitingMessage): void {
  if (!notificationsEnabled || planQuestionNotificationPermission() !== "granted") return;
  try {
    const notification = new Notification(message.surface === "code" ? "Code Chat question" : "Clarifying question", {
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

function handlePlanQuestionsAwaiting(input: object): void {
  const value = input as Record<string, unknown>;
  const surface = value.surface === "code" ? "code" : value.surface === "chat" ? "chat" : null;
  if (!surface
    || typeof value.workspaceId !== "string" || !value.workspaceId
    || typeof value.chatId !== "string" || !value.chatId) return;
  const message: PlanQuestionsAwaitingMessage = {
    type: "plan_questions_awaiting",
    workspaceId: value.workspaceId,
    workspaceName: typeof value.workspaceName === "string" ? value.workspaceName : "Echo",
    surface,
    chatId: value.chatId,
    turnId: typeof value.turnId === "string" ? value.turnId : "",
    callId: typeof value.callId === "string" ? value.callId : "",
    questions: Array.isArray(value.questions) ? value.questions as PlanQuestionsAwaitingMessage["questions"] : [],
  };
  // The sound is governed by the existing plan-question-sounds setting inside
  // playPlanQuestionSound, matching the user's choice to keep that toggle.
  playPlanQuestionSound();
  showPlanQuestionNotification(message);
}

export async function startPlanQuestionNotifications(): Promise<void> {
  if (!started) {
    started = true;
    ws.on("plan_questions_awaiting", handlePlanQuestionsAwaiting);
  }
  await refreshPlanQuestionNotificationSettings();
}
