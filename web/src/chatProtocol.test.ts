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

import {
  activateChatTab, canClearChat, clearChat, closeChatTab, closeWorkspaceSession,
  createChatTab, getChatWorkspaceState, onChatWorkspaceChange, openWorkspaceSession,
  sendMessage, stopStream,
} from "../js/chat.js";

function emit(type: string, message: any) {
  for (const handler of socket.handlers.get(type) ?? []) handler(message);
}

describe("multi-chat WebSocket protocol", () => {
  let log: HTMLElement;

  beforeEach(() => {
    socket.send.mockClear();
    log = document.createElement("div");
    document.body.append(log);
    openWorkspaceSession(log, "workspace-tabs");
    socket.send.mockClear();
  });

  afterEach(() => {
    closeWorkspaceSession(log);
    document.body.innerHTML = "";
  });

  it("routes commands to the active or explicit chat", () => {
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-tabs", sequence: 7,
      activeChatId: "chat-two",
      tabs: [
        { chatId: "chat-one", preview: "First", busy: false },
        { chatId: "chat-two", preview: "Second", busy: false },
      ],
      turns: [{
        id: "stored", userContent: "Question", status: "done",
        assistantTurns: [{ number: 0, content: "Answer", hasToolCalls: false }],
      }],
    });

    expect(sendMessage(log, "next question", "model-a", "general")).toBe(true);
    expect(socket.send).toHaveBeenLastCalledWith(expect.objectContaining({
      type: "chat_send", workspaceId: "workspace-tabs", chatId: "chat-two",
      message: "next question", model: "model-a", agentModeId: "general",
    }));
    expect(canClearChat(log)).toBe(true);
    expect(clearChat(log)).toBe(true);
    expect(socket.send).toHaveBeenLastCalledWith({
      type: "chat_clear", workspaceId: "workspace-tabs", chatId: "chat-two",
    });

    expect(createChatTab()).toBe(true);
    expect(activateChatTab("chat-one")).toBe(true);
    expect(closeChatTab("chat-one", true)).toBe(true);
    expect(socket.send).toHaveBeenLastCalledWith({
      type: "chat_tab_close", workspaceId: "workspace-tabs", chatId: "chat-one", stopIfBusy: true,
    });
  });

  it("tracks an inactive stream without replacing the active transcript", () => {
    const states: any[] = [];
    const unsubscribe = onChatWorkspaceChange((state: any) => states.push(state));
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-tabs", sequence: 2,
      activeChatId: "chat-one",
      tabs: [
        { chatId: "chat-one", preview: "Visible", busy: false },
        { chatId: "chat-two", preview: "New chat", busy: false },
      ],
      turns: [],
    });
    const emptyText = log.textContent;

    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-two", sequence: 3,
      event: { type: "turn_started", turnId: "inactive-turn", message: "  background\n task  " },
    });
    expect(log.textContent).toBe(emptyText);
    expect(getChatWorkspaceState()?.tabs[1]).toMatchObject({
      chatId: "chat-two", preview: "background task", busy: true,
    });

    emit("session_event", {
      type: "session_event", workspaceId: "workspace-tabs", chatId: "chat-two", sequence: 4,
      event: { type: "turn_finished", turnId: "inactive-turn", status: "done" },
    });
    expect(states.at(-1)?.tabs[1].busy).toBe(false);
    unsubscribe();
  });

  it("stops only the active stream", () => {
    emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-tabs", sequence: 0,
      activeChatId: "chat-one",
      tabs: [{ chatId: "chat-one", preview: "Running", busy: true }],
      turns: [],
      activeTurn: { id: "turn-one", userContent: "Run", status: "streaming", assistantTurns: [] },
    });
    stopStream();
    expect(socket.send).toHaveBeenLastCalledWith({
      type: "chat_stop", workspaceId: "workspace-tabs", chatId: "chat-one",
    });
  });

  it("isolates the code surface and sends editor context", () => {
    closeWorkspaceSession(log);
    openWorkspaceSession(log, "workspace-tabs", { surface: "code" });
    expect(socket.send).toHaveBeenLastCalledWith({
      type: "session_subscribe", workspaceId: "workspace-tabs", surface: "code",
    });
    socket.send.mockClear();

    emit("session_snapshot", {
      type: "session_snapshot", surface: "chat", workspaceId: "workspace-tabs", sequence: 9,
      activeChatId: "chat-one", tabs: [{ chatId: "chat-one", preview: "Main", busy: false }], turns: [],
    });
    expect(getChatWorkspaceState()?.hasSnapshot).toBe(false);

    emit("session_snapshot", {
      type: "session_snapshot", surface: "code", workspaceId: "workspace-tabs", sequence: 3,
      activeChatId: "code-chat-one", tabs: [{ chatId: "code-chat-one", preview: "Code", busy: false }], turns: [],
    });
    const editorContext = {
      tabs: [{ kind: "file", title: "main.go", active: true, ref: { rootId: "root", path: "main.go" } }],
    };
    expect(sendMessage(log, "review this", "model-a", "general", { editorContext })).toBe(true);
    expect(socket.send).toHaveBeenLastCalledWith(expect.objectContaining({
      type: "chat_send", surface: "code", workspaceId: "workspace-tabs", chatId: "code-chat-one",
      message: "review this", editorContext,
    }));
    emit("session_snapshot", {
      type: "session_snapshot", surface: "code", workspaceId: "workspace-tabs", sequence: 4,
      activeChatId: "code-chat-one", tabs: [{ chatId: "code-chat-one", preview: "Code", busy: false }],
      turns: [{ id: "stored", userContent: "review this", status: "done", assistantTurns: [] }],
    });
    clearChat(log);
    expect(socket.send).toHaveBeenLastCalledWith({
      type: "chat_clear", surface: "code", workspaceId: "workspace-tabs", chatId: "code-chat-one",
    });
  });
});
