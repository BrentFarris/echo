import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const api = vi.hoisted(() => ({ get: vi.fn() }));
vi.mock("../js/api.js", () => ({ get: api.get }));

const ws = vi.hoisted(() => {
  const handlers = new Map<string, Set<(data: unknown) => void>>();
  return {
    on: vi.fn((type: string, handler: (data: unknown) => void) => {
      if (!handlers.has(type)) handlers.set(type, new Set());
      handlers.get(type)!.add(handler);
      return () => handlers.get(type)?.delete(handler);
    }),
    send: vi.fn(() => true),
    emit(type: string, data: unknown = {}) {
      handlers.get(type)?.forEach((handler) => handler(data));
    },
  };
});
vi.mock("../js/ws.js", () => ws);

import { mount, unmount } from "./echoMapView";

type EchoMapChatFixture = Record<string, unknown>;

function chatFixture(overrides: EchoMapChatFixture = {}): EchoMapChatFixture {
  return {
    workspaceId: "ws-1", workspaceName: "Alpha", chatId: "chat-1", surface: "chat",
    title: "Implement the map", state: "completed", pendingQuestions: 0,
    lastActivityAt: "2026-08-27T14:00:00Z", turns: [],
    ...overrides,
  };
}

function turnFixture(index: number): Record<string, unknown> {
  return {
    id: `turn-${index}`, userContent: `Prompt ${index}`, status: "done",
    assistantTurns: [{
      number: 1, content: `Answer ${index}`, reasoning: "Deep thought",
      tools: [{ callId: `call-${index}`, name: "shell", status: "done", success: true, result: "ok" }],
    }],
  };
}

function mountView(): HTMLElement {
  const host = document.createElement("div");
  document.body.append(host);
  mount(host);
  return host;
}

beforeEach(() => {
  api.get.mockReset();
  ws.send.mockReset().mockReturnValue(true);
  location.hash = "";
});

afterEach(() => {
  unmount();
  document.body.innerHTML = "";
});

describe("Echo Map dashboard", () => {
  it("groups chats into pending, in-progress, and completed sections", async () => {
    api.get.mockResolvedValue({
      chats: [
        chatFixture({ chatId: "pending-chat", title: "Waiting chat", state: "pending_questions", pendingQuestions: 2 }),
        chatFixture({ workspaceId: "ws-2", workspaceName: "Beta", chatId: "code-chat", surface: "code", title: "Code work", state: "in_progress" }),
        chatFixture({ chatId: "done-chat", title: "Done chat", state: "completed" }),
      ],
      warnings: [],
    });
    const host = mountView();

    await vi.waitFor(() => expect(host.querySelectorAll("[data-echo-map-card]")).toHaveLength(3));
    const pendingSection = host.querySelector('[data-echo-map-section="pending_questions"]');
    const progressSection = host.querySelector('[data-echo-map-section="in_progress"]');
    const completedSection = host.querySelector('[data-echo-map-section="completed"]');
    expect(pendingSection?.querySelector(".echo-map-card-title")?.textContent).toBe("Waiting chat");
    expect(progressSection?.querySelector(".echo-map-card-title")?.textContent).toBe("Code work");
    expect(completedSection?.querySelector(".echo-map-card-title")?.textContent).toBe("Done chat");

    const codeCard = progressSection?.querySelector("[data-echo-map-card]");
    expect(codeCard?.querySelector(".echo-map-card-surface")?.textContent).toBe("(code)");
    expect(codeCard?.querySelector(".echo-map-card-workspace-name")?.textContent).toBe("Beta");
    expect(codeCard?.querySelector(".echo-map-card-pending")).toBeNull();
  });

  it("shows a pending-questions button that jumps to the chat", async () => {
    api.get.mockResolvedValue({
      chats: [chatFixture({ chatId: "pending-chat", title: "Waiting chat", state: "pending_questions", pendingQuestions: 2 })],
      warnings: [],
    });
    const host = mountView();

    const button = (await vi.waitFor(() => {
      const found = host.querySelector<HTMLButtonElement>(".echo-map-card-pending");
      if (!found) throw new Error("pending button not rendered yet");
      return found;
    }));
    expect(button.textContent).toBe("Pending Questions (2)");
    button.click();
    expect(location.hash).toBe("#/home?workspaceId=ws-1&chatId=pending-chat");
  });

  it("deep-links from the card title with a full-string tooltip", async () => {
    api.get.mockResolvedValue({ chats: [chatFixture()], warnings: [] });
    const host = mountView();

    const title = (await vi.waitFor(() => {
      const found = host.querySelector<HTMLButtonElement>(".echo-map-card-title");
      if (!found) throw new Error("title not rendered yet");
      return found;
    }));
    expect(title.title).toBe("Implement the map");
    title.click();
    expect(location.hash).toBe("#/home?workspaceId=ws-1&chatId=chat-1");
  });

  it("renders turns with collapsed tools and thinking", async () => {
    const turns = Array.from({ length: 10 }, (_, index) => turnFixture(index));
    api.get.mockResolvedValue({ chats: [chatFixture({ turns })], warnings: [] });
    const host = mountView();

    await vi.waitFor(() => expect(host.querySelectorAll(".chat-message-user")).toHaveLength(10));
    expect(host.querySelectorAll(".chat-message-assistant")).toHaveLength(10);
    const tools = [...host.querySelectorAll<HTMLDetailsElement>(".chat-tool-item")];
    const thinking = [...host.querySelectorAll<HTMLDetailsElement>(".chat-reasoning-item")];
    expect(tools).toHaveLength(10);
    expect(thinking).toHaveLength(10);
    for (const details of [...tools, ...thinking]) expect(details.open).toBe(false);
    expect(tools[0].querySelector(".chat-activity-name")?.textContent).toBe("shell");
    expect(tools[0].querySelector(".chat-activity-status")?.textContent).toBe("Completed");
    const finalContents = [...host.querySelectorAll<HTMLElement>(".chat-final-content")];
    expect(finalContents).toHaveLength(10);
    expect(finalContents[9].textContent).toContain("Answer 9");
  });

  it("refreshes when global completion and question broadcasts arrive", async () => {
    api.get.mockResolvedValue({ chats: [], warnings: [] });
    mountView();
    await vi.waitFor(() => expect(api.get).toHaveBeenCalledTimes(1));

    ws.emit("chat_completed");
    await vi.waitFor(() => expect(api.get).toHaveBeenCalledTimes(2));
    ws.emit("plan_questions_awaiting");
    await vi.waitFor(() => expect(api.get).toHaveBeenCalledTimes(3));
    expect(api.get).toHaveBeenCalledWith("/api/echo-map", expect.objectContaining({ signal: expect.any(AbortSignal) }));
  });

  it("stops polling after unmount", async () => {
    vi.useFakeTimers();
    try {
      api.get.mockResolvedValue({ chats: [], warnings: [] });
      mountView();
      await vi.advanceTimersByTimeAsync(0);
      expect(api.get).toHaveBeenCalledTimes(1);
      unmount();
      await vi.advanceTimersByTimeAsync(3000 * 3);
      expect(api.get).toHaveBeenCalledTimes(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("surfaces workspace warnings", async () => {
    api.get.mockResolvedValue({
      chats: [],
      warnings: [{ workspaceId: "broken", workspaceName: "Broken repo", message: "Chat history is unavailable." }],
    });
    const host = mountView();

    const warning = await vi.waitFor(() => {
      const found = host.querySelector<HTMLElement>(".echo-map-warning");
      if (!found) throw new Error("warning not rendered yet");
      return found;
    });
    expect(warning.textContent).toContain("Broken repo");
  });

  it("confirms before closing a chat and sends chat_tab_close", async () => {
    api.get.mockResolvedValue({ chats: [chatFixture()], warnings: [] });
    const host = mountView();

    const close = await vi.waitFor(() => {
      const found = host.querySelector<HTMLButtonElement>(".echo-map-card-close");
      if (!found) throw new Error("close button not rendered yet");
      return found;
    });
    expect(close.getAttribute("aria-label")).toBe("Close chat: Implement the map");
    close.click();

    const dialog = document.querySelector<HTMLDialogElement>(".chat-close-dialog");
    expect(dialog).not.toBeNull();
    expect(dialog?.querySelector("h2")?.textContent).toBe("Close this chat?");
    expect(ws.send).not.toHaveBeenCalled();

    dialog?.querySelector<HTMLButtonElement>("[data-chat-close-choice='cancel']")?.click();
    expect(document.querySelector(".chat-close-dialog")).toBeNull();
    expect(ws.send).not.toHaveBeenCalled();

    close.click();
    document.querySelector<HTMLButtonElement>("[data-chat-close-choice='confirm']")?.click();
    expect(document.querySelector(".chat-close-dialog")).toBeNull();
    expect(ws.send).toHaveBeenCalledWith({ type: "chat_tab_close", workspaceId: "ws-1", chatId: "chat-1" });
  });

  it("offers to stop and close a running chat", async () => {
    api.get.mockResolvedValue({
      chats: [chatFixture({ chatId: "busy-chat", state: "in_progress" })],
      warnings: [],
    });
    const host = mountView();

    const close = await vi.waitFor(() => {
      const found = host.querySelector<HTMLButtonElement>(".echo-map-card-close");
      if (!found) throw new Error("close button not rendered yet");
      return found;
    });
    close.click();

    const dialog = document.querySelector<HTMLDialogElement>(".chat-close-dialog");
    expect(dialog?.querySelector("h2")?.textContent).toBe("Chat is still running");
    dialog?.querySelector<HTMLButtonElement>("[data-chat-close-choice='confirm']")?.click();
    expect(ws.send).toHaveBeenCalledWith({
      type: "chat_tab_close", workspaceId: "ws-1", chatId: "busy-chat", stopIfBusy: true,
    });
  });

  it("omits the close button on code chats, which cannot be closed", async () => {
    api.get.mockResolvedValue({
      chats: [chatFixture({ workspaceId: "ws-2", chatId: "code-chat", surface: "code" })],
      warnings: [],
    });
    const host = mountView();

    await vi.waitFor(() => expect(host.querySelectorAll("[data-echo-map-card]")).toHaveLength(1));
    expect(host.querySelector(".echo-map-card-close")).toBeNull();
  });

  it("toasts close rejections for rendered chats", async () => {
    api.get.mockResolvedValue({ chats: [chatFixture()], warnings: [] });
    const host = mountView();
    await vi.waitFor(() => expect(host.querySelectorAll("[data-echo-map-card]")).toHaveLength(1));

    ws.emit("command_error", { workspaceId: "ws-1", chatId: "chat-1", code: "goal_transcript_locked", error: "clear the current goal before closing this chat" });
    await vi.waitFor(() => expect(document.querySelector(".code-toast")?.textContent).toContain("clear the current goal"));

    // Errors for chats that are not on the dashboard are ignored.
    ws.emit("command_error", { workspaceId: "other", chatId: "unrelated", error: "ignore me" });
    expect(document.querySelectorAll(".code-toast")).toHaveLength(1);
  });
});
