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

import { closeWorkspaceSession, openWorkspaceSession } from "../js/chat.js";

function emit(type: string, message: any) {
  for (const handler of socket.handlers.get(type) ?? []) handler(message);
}

function sessionEvent(sequence: number, event: any) {
  emit("session_event", {
    type: "session_event", workspaceId: "workspace-plan", chatId: "chat-plan", sequence, event,
  });
}

const questionSet = {
  questionSetId: "call-questions",
  questions: [
    { id: "scope", question: "Which scope?", options: ["Core", "Extended"] },
    { id: "language", question: "Which language?", options: [] },
  ],
};

describe("Plan-mode clarifying questions", () => {
  let log: HTMLElement;

  beforeEach(() => {
    socket.send.mockClear();
    log = document.createElement("div");
    document.body.append(log);
    openWorkspaceSession(log, "workspace-plan");
    socket.send.mockClear();
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-plan", sequence: 1,
      activeChatId: "chat-plan", tabs: [{ chatId: "chat-plan", preview: "Plan", busy: false }], turns: [],
    });
  });

  afterEach(() => {
    closeWorkspaceSession(log);
    document.body.innerHTML = "";
  });

  it("submits answers and collapses them inline after resolution", () => {
    sessionEvent(2, { type: "turn_started", turnId: "turn-plan", message: "Plan it" });
    sessionEvent(3, { type: "assistant_turn_start", turnId: "turn-plan", turn: 0 });
    sessionEvent(4, { type: "assistant_turn_end", turnId: "turn-plan", turn: 0, hasToolCalls: true });
    sessionEvent(5, {
      type: "tool_call", turnId: "turn-plan", turn: 0, callId: "call-questions", callOrder: 0,
      tool: "ask_user_questions", status: "awaiting_input", planQuestions: questionSet,
      arguments: JSON.stringify({ questions: questionSet.questions }),
    });

    const card = log.querySelector<HTMLDetailsElement>(".chat-plan-question-item")!;
    expect(card).not.toBeNull();
    expect(card.open).toBe(true);
    const fields = [...card.querySelectorAll<HTMLFieldSetElement>(".chat-plan-question-field")];
    const extended = fields[0].querySelector<HTMLInputElement>('input[type="radio"][value="1"]')!;
    extended.checked = true;
    extended.dispatchEvent(new Event("change", { bubbles: true }));
    const language = fields[1].querySelector<HTMLInputElement>(".chat-plan-question-custom")!;
    language.value = "Go";
    language.dispatchEvent(new Event("input", { bubbles: true }));
    card.querySelector<HTMLFormElement>("form")!.requestSubmit();

    expect(socket.send).toHaveBeenLastCalledWith(expect.objectContaining({
      type: "plan_questions_submit", workspaceId: "workspace-plan", chatId: "chat-plan",
      questionSetId: "call-questions",
      answers: [
        { questionId: "scope", optionIndex: 1 },
        { questionId: "language", optionIndex: -1, text: "Go" },
      ],
    }));

    const answers = [
      { questionId: "scope", optionIndex: 1 },
      { questionId: "language", optionIndex: -1, text: "Go" },
    ];
    sessionEvent(6, {
      type: "plan_questions_resolved", turnId: "turn-plan", turn: 0,
      callId: "call-questions", callOrder: 0, planQuestions: questionSet, answers, skipped: false,
    });
    expect(card.open).toBe(false);
    expect(card.textContent).toContain("Answered");
    expect(card.textContent).toContain("Extended");
    expect(card.textContent).toContain("Go");

    sessionEvent(7, {
      type: "tool_result", turnId: "turn-plan", turn: 0, callId: "call-questions", callOrder: 0,
      tool: "ask_user_questions", success: true, answers, skipped: false,
      content: JSON.stringify({ tool: "ask_user_questions", success: true, output: { answers } }),
    });
    sessionEvent(8, { type: "assistant_turn_start", turnId: "turn-plan", turn: 1 });
    sessionEvent(9, { type: "token", turnId: "turn-plan", turn: 1, content: "Final plan." });
    sessionEvent(10, { type: "assistant_turn_end", turnId: "turn-plan", turn: 1, hasToolCalls: false });
    sessionEvent(11, { type: "turn_finished", turnId: "turn-plan", status: "done" });

    expect(card.closest(".chat-work-disclosure")).toBeNull();
    expect(card.nextElementSibling?.classList.contains("chat-final-content")).toBe(true);
  });

  it("sends the skip action", () => {
    sessionEvent(2, { type: "turn_started", turnId: "turn-skip", message: "Plan it" });
    sessionEvent(3, {
      type: "tool_call", turnId: "turn-skip", turn: 0, callId: "call-questions", callOrder: 0,
      tool: "ask_user_questions", status: "awaiting_input", planQuestions: questionSet,
    });
    const buttons = [...log.querySelectorAll<HTMLButtonElement>(".chat-plan-question-actions button")];
    buttons.find((button) => button.textContent === "Skip")!.click();
    expect(socket.send).toHaveBeenLastCalledWith(expect.objectContaining({
      type: "plan_questions_skip", workspaceId: "workspace-plan", chatId: "chat-plan",
      questionSetId: "call-questions",
    }));
  });

  it("restores answered questions as a collapsed inline disclosure", () => {
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-plan", sequence: 20,
      activeChatId: "chat-plan", tabs: [{ chatId: "chat-plan", preview: "Stored", busy: false }],
      turns: [{
        id: "stored", userContent: "Plan it", status: "done",
        assistantTurns: [
          {
            number: 0, hasToolCalls: true, tools: [{
              callId: "call-questions", callOrder: 0, name: "ask_user_questions", status: "complete",
              success: true, planQuestions: questionSet,
              answers: [{ questionId: "scope", optionIndex: 0 }, { questionId: "language", optionIndex: -1, text: "Rust" }],
              result: JSON.stringify({
                tool: "ask_user_questions", success: true,
                output: { answers: [{ questionId: "scope", optionIndex: 0 }, { questionId: "language", optionIndex: -1, text: "Rust" }] },
              }),
            }],
          },
          { number: 1, content: "Stored final plan.", hasToolCalls: false },
        ],
      }],
    });

    const card = log.querySelector<HTMLDetailsElement>(".chat-plan-question-item")!;
    expect(card.open).toBe(false);
    expect(card.textContent).toContain("Core");
    expect(card.textContent).toContain("Rust");
    expect(card.closest(".chat-work-disclosure")).toBeNull();
    expect(card.nextElementSibling?.classList.contains("chat-final-content")).toBe(true);
  });
});
