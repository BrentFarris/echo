import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const socket = vi.hoisted(() => {
  const handlers = new Map<string, Set<(message: any) => void>>();
  return {
    handlers,
    on: vi.fn((type: string, handler: (message: any) => void) => {
      if (!handlers.has(type)) handlers.set(type, new Set());
      handlers.get(type)!.add(handler);
      return () => handlers.get(type)?.delete(handler);
    }),
    onState: vi.fn(() => () => undefined),
    send: vi.fn(() => true),
  };
});

vi.mock("../js/ws.js", () => ({ on: socket.on, onState: socket.onState, send: socket.send }));

const toast = vi.hoisted(() => vi.fn());
vi.mock("../src/code/ui.ts", () => ({
  copyText: vi.fn(() => Promise.resolve()),
  toast,
}));

import { closeWorkspaceSession, openWorkspaceSession } from "../js/chat.js";

function emit(type: string, message: any) {
  for (const handler of socket.handlers.get(type) ?? []) handler(message);
}

function sessionEvent(sequence: number, event: any) {
  emit("session_event", {
    type: "session_event", workspaceId: "workspace-plan", sequence, event,
  });
}

function snapshot(turns: any[]) {
  emit("session_snapshot", {
    type: "session_snapshot", workspaceId: "workspace-plan", sequence: 1,
    activeChatId: "chat-plan", tabs: [{ chatId: "chat-plan", preview: "Plan", busy: false }], turns,
  });
}

function planTurn(overrides: Record<string, unknown> = {}) {
  return {
    id: "turn-plan", userContent: "Plan it", status: "done", agentModeId: "plan",
    assistantTurns: [{ number: 0, content: "Here is the plan.", hasToolCalls: false }],
    ...overrides,
  };
}

describe("Plan-mode follow-up actions", () => {
  let log: HTMLElement;

  beforeEach(() => {
    socket.send.mockClear();
    toast.mockClear();
    log = document.createElement("div");
    document.body.append(log);
    openWorkspaceSession(log, "workspace-plan");
    socket.send.mockClear();
  });

  afterEach(() => {
    closeWorkspaceSession(log);
    document.body.innerHTML = "";
  });

  it("renders the two plan actions on a completed plan turn", () => {
    snapshot([planTurn()]);
    const actions = log.querySelector(".chat-message-assistant .chat-plan-actions");
    expect(actions).not.toBeNull();
    const buttons = [...actions!.querySelectorAll("button")];
    expect(buttons.map((button) => button.textContent)).toEqual([
      "Implement the plan",
      "Implement the plan and commit the changes",
    ]);
  });

  it("does not render actions for non-plan or failed/empty plan turns", () => {
    snapshot([
      planTurn({ id: "turn-general", agentModeId: "general" }),
      planTurn({ id: "turn-error", status: "error" }),
      planTurn({ id: "turn-empty", assistantTurns: [{ number: 0, content: "", hasToolCalls: false }] }),
    ]);
    expect(log.querySelectorAll(".chat-plan-actions").length).toBe(0);
  });

  it("renders actions again after restoring from a snapshot", () => {
    snapshot([planTurn()]);
    expect(log.querySelectorAll(".chat-plan-actions").length).toBe(1);
    snapshot([planTurn()]);
    expect(log.querySelectorAll(".chat-plan-actions").length).toBe(1);
  });

  it("sends the plan instruction with the onPlanAction option", () => {
    const onPlanAction = vi.fn(() => true);
    closeWorkspaceSession(log);
    log = document.createElement("div");
    document.body.append(log);
    openWorkspaceSession(log, "workspace-plan", { onPlanAction });
    socket.send.mockClear();
    snapshot([planTurn()]);

    log.querySelector<HTMLButtonElement>(".chat-plan-actions button")!.click();
    expect(onPlanAction).toHaveBeenCalledWith("Implement the plan");
    expect(onPlanAction).toHaveBeenCalledOnce();
  });

  it("falls back to sendMessage with General mode when no callback is registered", () => {
    snapshot([planTurn()]);
    log.querySelector<HTMLButtonElement>(".chat-plan-actions button")!.click();
    expect(socket.send).toHaveBeenCalledWith(expect.objectContaining({
      type: "chat_send",
      workspaceId: "workspace-plan",
      chatId: "chat-plan",
      message: "Implement the plan",
      agentModeId: "general",
    }));
  });

  it("rejects the action while a turn is streaming", () => {
    snapshot([planTurn()]);
    sessionEvent(2, { type: "turn_started", turnId: "turn-busy", message: "New task" });
    const button = log.querySelector<HTMLButtonElement>(".chat-plan-actions button")!;
    expect(button.disabled).toBe(true);
    // Simulate a click that races with the stream starting (guard is belt-and-suspenders).
    button.disabled = false;
    button.click();
    expect(toast).toHaveBeenCalledWith("Wait for the current response to finish before implementing the plan.", { sticky: true });
    expect(socket.send).not.toHaveBeenCalledWith(expect.objectContaining({ type: "chat_send" }));
  });
});
