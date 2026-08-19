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
    type: "session_event", workspaceId: "workspace-research", chatId: "chat-research", sequence, event,
  });
}

describe("research agent activity", () => {
  let log: HTMLElement;

  beforeEach(() => {
    log = document.createElement("div");
    document.body.append(log);
    openWorkspaceSession(log, "workspace-research");
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-research", sequence: 1,
      activeChatId: "chat-research", tabs: [{ chatId: "chat-research", preview: "Research", busy: false }], turns: [],
    });
  });

  afterEach(() => {
    closeWorkspaceSession(log);
    document.body.innerHTML = "";
  });

  it("shows transient status and retains attributed work in the disclosure", () => {
    sessionEvent(2, { type: "turn_started", turnId: "turn-research", message: "Investigate" });
    sessionEvent(3, {
      type: "research_agent_status", turnId: "turn-research",
      researchAgent: { id: "agent-1", name: "Docs scout", status: "running", phase: "investigating", taskLabel: "Read docs" },
    });
    expect(log.querySelector(".chat-research-status")?.textContent).toContain("Docs scout");

    sessionEvent(4, {
      type: "research_reasoning", turnId: "turn-research", agentId: "agent-1",
      agentName: "Docs scout", content: "Checking primary sources.",
    });
    sessionEvent(5, {
      type: "tool_call", turnId: "turn-research", callId: "agent-1:fetch", callOrder: 0,
      tool: "web_fetch", arguments: "{\"url\":\"https://example.com\"}",
      status: "running", agentId: "agent-1", agentName: "Docs scout", research: true,
    });
    sessionEvent(6, {
      type: "tool_result", turnId: "turn-research", callId: "agent-1:fetch", callOrder: 0,
      tool: "web_fetch", success: true, content: "source", agentId: "agent-1", agentName: "Docs scout", research: true,
    });
    sessionEvent(7, { type: "research_agents_clear", turnId: "turn-research" });
    expect(log.querySelector(".chat-research-status")).toBeNull();

    sessionEvent(8, { type: "assistant_turn_start", turnId: "turn-research", turn: 0 });
    sessionEvent(9, { type: "token", turnId: "turn-research", turn: 0, content: "Final synthesis." });
    sessionEvent(10, { type: "assistant_turn_end", turnId: "turn-research", turn: 0, hasToolCalls: false });
    sessionEvent(11, { type: "turn_finished", turnId: "turn-research", status: "done" });

    const work = log.querySelector<HTMLDetailsElement>(".chat-work-disclosure")!;
    expect(work).not.toBeNull();
    expect(work.textContent).toContain("Docs scout thinking");
    expect(work.textContent).toContain("Checking primary sources.");
    expect(work.textContent).toContain("Docs scout · web_fetch");
    expect(log.querySelector(".chat-final-content")?.textContent).toContain("Final synthesis.");
  });

  it("restores persisted attributed research work", () => {
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-research", sequence: 20,
      activeChatId: "chat-research", tabs: [{ chatId: "chat-research", preview: "Stored", busy: false }],
      turns: [{
        id: "stored", userContent: "Investigate", status: "done",
        assistantTurns: [{ number: 0, content: "Stored synthesis.", hasToolCalls: false }],
        researchReasoning: [{ agentId: "agent-1", agentName: "Code scout", reasoning: "Inspected the code." }],
        researchTools: [{
          callId: "agent-1:read", callOrder: 0, name: "filesystem_read_text", status: "complete",
          success: true, result: "file", agentId: "agent-1", agentName: "Code scout",
        }],
      }],
    });

    const work = log.querySelector<HTMLDetailsElement>(".chat-work-disclosure")!;
    expect(work.textContent).toContain("Code scout thinking");
    expect(work.textContent).toContain("Inspected the code.");
    expect(work.textContent).toContain("Code scout · filesystem_read_text");
  });
});
