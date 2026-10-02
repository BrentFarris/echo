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
    onState: vi.fn((handler: (state: string) => void) => {
      handler("closed");
      return () => undefined;
    }),
    send: vi.fn(() => true),
  };
});

vi.mock("../js/ws.js", () => ({
  on: socket.on,
  onState: socket.onState,
  send: socket.send,
}));

import { closeWorkspaceSession, openWorkspaceSession } from "../js/chat.js";

function emit(type: string, message: any): void {
  for (const handler of socket.handlers.get(type) ?? []) handler(message);
}

function snapshot(workspaceId: string, turns: any[] = [], sequence = 0): void {
  emit("session_snapshot", { workspaceId, turns, sequence });
}

function event(workspaceId: string, sequence: number, payload: any): void {
  emit("session_event", { workspaceId, sequence, event: payload });
}

describe("chat reasoning blocks", () => {
  let log: HTMLElement;

  beforeEach(() => {
    document.body.textContent = "";
    socket.send.mockClear();
    log = document.createElement("div");
    document.body.appendChild(log);
    openWorkspaceSession(log, "workspace-reasoning");
  });

  afterEach(() => {
    closeWorkspaceSession(log);
    document.body.innerHTML = "";
  });

  it("merges reasoning split only by whitespace into a single thinking block", () => {
    snapshot("workspace-reasoning");
    event("workspace-reasoning", 1, { type: "turn_started", turnId: "turn-live", message: "Inspect" });
    event("workspace-reasoning", 2, { type: "assistant_turn_start", turnId: "turn-live", turn: 0 });
    event("workspace-reasoning", 3, { type: "reasoning", turnId: "turn-live", turn: 0, content: "First" });
    // Whitespace between reasoning must not fragment the disclosure.
    event("workspace-reasoning", 4, { type: "token", turnId: "turn-live", turn: 0, content: " " });
    event("workspace-reasoning", 5, { type: "reasoning", turnId: "turn-live", turn: 0, content: "Second" });
    event("workspace-reasoning", 6, { type: "assistant_turn_end", turnId: "turn-live", turn: 0, hasToolCalls: false });

    const items = log.querySelectorAll(".chat-reasoning-item");
    expect(items).toHaveLength(1);
    expect((items[0] as HTMLElement).textContent).toContain("FirstSecond");
    // Whitespace must not introduce a visible progress text block.
    expect(log.querySelector(".chat-progress-text")).toBeNull();
  });

  it("keeps reasoning separated by real content in distinct blocks", () => {
    snapshot("workspace-reasoning");
    event("workspace-reasoning", 1, { type: "turn_started", turnId: "turn-live", message: "Inspect" });
    event("workspace-reasoning", 2, { type: "assistant_turn_start", turnId: "turn-live", turn: 0 });
    event("workspace-reasoning", 3, { type: "reasoning", turnId: "turn-live", turn: 0, content: "Think about step" });
    event("workspace-reasoning", 4, { type: "token", turnId: "turn-live", turn: 0, content: "Visible text" });
    event("workspace-reasoning", 5, { type: "reasoning", turnId: "turn-live", turn: 0, content: "Think again" });
    event("workspace-reasoning", 6, { type: "assistant_turn_end", turnId: "turn-live", turn: 0, hasToolCalls: false });

    expect(log.querySelectorAll(".chat-reasoning-item")).toHaveLength(2);
  });
});
