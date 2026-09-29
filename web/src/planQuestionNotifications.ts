import { get } from "../js/api.js";
import * as ws from "../js/ws.js";
import {
  chatCompletionRouteHash, type ChatCompletionTarget,
} from "./navigation";
import { playPlanQuestionSound } from "./planQuestionSound";
import { isCompletedChatVisible } from "./completionNotifications";

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
const notifiedKeys = new Set<string>();
const maxNotifiedKeys = 200;

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
  // Mirror chat completions: the sound always plays, but the OS notification
  // is suppressed when the user is already looking at that exact chat.
  if (!notificationsEnabled || isCompletedChatVisible(message)) return;
  if (planQuestionNotificationPermission() !== "granted") return;
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

function markNotified(key: string): boolean {
  if (notifiedKeys.has(key)) return false;
  notifiedKeys.add(key);
  if (notifiedKeys.size > maxNotifiedKeys) {
    const oldest = notifiedKeys.values().next().value;
    if (oldest !== undefined) notifiedKeys.delete(oldest);
  }
  return true;
}

// The sound is governed by the existing plan-question-sounds setting inside
// playPlanQuestionSound, matching the user's choice to keep that toggle.
function notifyPlanQuestions(message: PlanQuestionsAwaitingMessage): void {
  const key = `${message.workspaceId}|${message.surface}|${message.chatId}|${message.callId}`;
  if (!markNotified(key)) return;
  playPlanQuestionSound();
  showPlanQuestionNotification(message);
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
  notifyPlanQuestions(message);
}

type PendingPlanQuestion = {
  turnId: string;
  callId: string;
  questions: PlanQuestionsAwaitingMessage["questions"];
};

function pendingPlanQuestionsInTurn(value: unknown): PendingPlanQuestion[] {
  if (!value || typeof value !== "object") return [];
  const turn = value as Record<string, unknown>;
  const turnId = typeof turn.id === "string" ? turn.id : "";
  const pending: PendingPlanQuestion[] = [];
  if (!Array.isArray(turn.assistantTurns)) return pending;
  for (const assistant of turn.assistantTurns) {
    if (!assistant || typeof assistant !== "object") continue;
    const tools = (assistant as Record<string, unknown>).tools;
    if (!Array.isArray(tools)) continue;
    for (const tool of tools) {
      if (!tool || typeof tool !== "object") continue;
      const toolRecord = tool as Record<string, unknown>;
      if (toolRecord.name !== "ask_user_questions" || toolRecord.status !== "awaiting_input") continue;
      const callId = typeof toolRecord.callId === "string" ? toolRecord.callId : "";
      if (!callId) continue;
      const set = toolRecord.planQuestions;
      const questions = set && typeof set === "object"
        && Array.isArray((set as Record<string, unknown>).questions)
        ? (set as Record<string, unknown>).questions as PlanQuestionsAwaitingMessage["questions"]
        : [];
      pending.push({ turnId, callId, questions });
    }
  }
  return pending;
}

/**
 * Restores alerts for question sets that were already awaiting input when this
 * client (re)subscribes — e.g. after a reconnect, a tab reveal, or after
 * switching to the chat that owns them. Without this, a question that arrived
 * while the tab was suspended or the user was elsewhere would only be noticed
 * on reveal, with no sound and no notification.
 */
function handleSessionSnapshot(input: object): void {
  const value = input as Record<string, unknown>;
  const surface = value.surface === "code" ? "code" : value.surface === "chat" ? "chat" : null;
  if (!surface) return;
  const workspaceId = typeof value.workspaceId === "string" ? value.workspaceId : "";
  if (!workspaceId) return;
  const chatId = typeof value.activeChatId === "string" && value.activeChatId
    ? value.activeChatId
    : typeof value.chatId === "string" && value.chatId ? value.chatId : "";
  if (!chatId) return;
  const workspaceName = typeof value.workspaceName === "string" ? value.workspaceName : "Echo";
  const turns = Array.isArray(value.turns) ? value.turns : [];
  const activeTurn = value.activeTurn ? [value.activeTurn] : [];
  for (const turn of [...turns, ...activeTurn]) {
    for (const pending of pendingPlanQuestionsInTurn(turn)) {
      notifyPlanQuestions({
        type: "plan_questions_awaiting",
        workspaceId,
        workspaceName,
        surface,
        chatId,
        turnId: pending.turnId,
        callId: pending.callId,
        questions: pending.questions,
      });
    }
  }
}

export async function startPlanQuestionNotifications(): Promise<void> {
  if (!started) {
    started = true;
    ws.on("plan_questions_awaiting", handlePlanQuestionsAwaiting);
    ws.on("session_snapshot", handleSessionSnapshot);
  }
  await refreshPlanQuestionNotificationSettings();
}
