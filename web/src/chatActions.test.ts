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
  const message = {
    workspaceId: "workspace-actions",
    activeChatId: "chat-actions",
    tabs: [{ chatId: "chat-actions", preview: "Actions", busy: false }],
    turns,
    sequence: 1,
  };
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
    vi.spyOn(window, "confirm").mockReturnValue(true);
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

  it("requests deletion of the selected assistant response", () => {
    snapshot([{
      id: "turn-delete",
      userContent: "Question",
      status: "done",
      assistantTurns: [{ number: 0, content: "Answer", hasToolCalls: false }],
    }]);
    socket.send.mockClear();

    log.querySelector<HTMLButtonElement>(".chat-message-assistant [data-message-action='delete']")!.click();

    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining("tool calls"));
    expect(socket.send).toHaveBeenCalledWith({
      type: "chat_message_delete",
      workspaceId: "workspace-actions",
      chatId: "chat-actions",
      turnId: "turn-delete",
      role: "assistant",
    });
  });

  it("requests deletion of the selected user message", () => {
    snapshot([{
      id: "turn-delete-user",
      userContent: "Question",
      status: "done",
      assistantTurns: [{ number: 0, content: "Answer", hasToolCalls: false }],
    }]);
    socket.send.mockClear();

    log.querySelector<HTMLButtonElement>(".chat-message-user [data-message-action='delete']")!.click();

    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining("attachments"));
    expect(socket.send).toHaveBeenCalledWith({
      type: "chat_message_delete",
      workspaceId: "workspace-actions",
      chatId: "chat-actions",
      turnId: "turn-delete-user",
      role: "user",
    });
  });

  it.each([
    ["user", ".chat-message-user", "Its response and all later messages"],
    ["assistant", ".chat-message-assistant", "preceding user message"],
  ])("reruns the turn from the %s action", (_role, selector, confirmation) => {
    snapshot([{
      id: "turn-rerun",
      userContent: "Question",
      status: "done",
      assistantTurns: [{ number: 0, content: "Answer", hasToolCalls: false }],
    }]);
    socket.send.mockClear();

    log.querySelector<HTMLButtonElement>(`${selector} [data-message-action='rerun']`)!.click();

    expect(window.confirm).toHaveBeenCalledWith(expect.stringContaining(confirmation));
    expect(socket.send).toHaveBeenCalledWith({
      type: "chat_message_rerun",
      workspaceId: "workspace-actions",
      chatId: "chat-actions",
      turnId: "turn-rerun",
    });
  });

  it("does not render deleted halves restored from a snapshot", () => {
    snapshot([
      {
        id: "turn-user-deleted", userDeleted: true, userContent: "", status: "done",
        assistantTurns: [{ number: 0, content: "Keep response", hasToolCalls: false }],
      },
      {
        id: "turn-assistant-deleted", assistantDeleted: true, userContent: "Keep prompt", status: "done",
        assistantTurns: [],
      },
    ]);

    expect(log.querySelector(".chat-message-user[data-turn-id='turn-user-deleted']")).toBeNull();
    expect(log.querySelector(".chat-message-assistant[data-turn-id='turn-user-deleted']")?.textContent).toContain("Keep response");
    expect(log.querySelector(".chat-message-user[data-turn-id='turn-assistant-deleted']")?.textContent).toContain("Keep prompt");
    expect(log.querySelector(".chat-message-assistant[data-turn-id='turn-assistant-deleted']")).toBeNull();
  });
});
