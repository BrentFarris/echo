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

function snapshot(turns: any[]): void {
  const message = { workspaceId: "workspace-actions", turns, sequence: 1 };
  for (const handler of socket.handlers.get("session_snapshot") ?? []) handler(message);
}

describe("chat message actions", () => {
  let log: HTMLElement;
  let writeText: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    document.body.replaceChildren();
    writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", {
      configurable: true,
      value: { writeText },
    });
    log = document.createElement("div");
    document.body.appendChild(log);
    openWorkspaceSession(log, "workspace-actions");
  });

  afterEach(() => {
    closeWorkspaceSession(log);
    document.body.replaceChildren();
  });

  it("renders the four planned controls on user and assistant messages", () => {
    snapshot([{
      id: "turn-actions",
      userContent: "Question",
      status: "done",
      assistantTurns: [{ number: 0, content: "Answer", hasToolCalls: false }],
    }]);

    for (const message of log.querySelectorAll(".chat-message")) {
      expect(Array.from(message.querySelectorAll("[data-message-action]")).map((button) =>
        (button as HTMLElement).dataset.messageAction,
      )).toEqual(["copy", "edit", "rerun", "delete"]);
    }
  });

  it("copies the raw user message", async () => {
    snapshot([{
      id: "turn-user-copy",
      userContent: "**Keep the Markdown**",
      status: "done",
      assistantTurns: [{ number: 0, content: "Answer", hasToolCalls: false }],
    }]);

    log.querySelector<HTMLButtonElement>(".chat-message-user [data-message-action='copy']")!.click();

    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith("**Keep the Markdown**"));
  });

  it("copies only the assistant's final response", async () => {
    snapshot([{
      id: "turn-final-copy",
      userContent: "Run it",
      status: "done",
      assistantTurns: [
        { number: 0, content: "Intermediate tool narration", reasoning: "Private reasoning", hasToolCalls: true },
        { number: 1, content: "# Final response\n\nDone.", hasToolCalls: false },
      ],
    }]);

    log.querySelector<HTMLButtonElement>(".chat-message-assistant [data-message-action='copy']")!.click();

    await vi.waitFor(() => expect(writeText).toHaveBeenCalledWith("# Final response\n\nDone."));
  });
});
