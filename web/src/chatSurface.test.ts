import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const chat = vi.hoisted(() => {
  const streamingListeners: Array<(streaming: boolean) => void> = [];
  let workspaceListener: ((workspace?: any) => void) | null = null;
  return {
    streamingListeners,
    canClearChat: vi.fn(() => false),
    clearChat: vi.fn(() => false),
    closeWorkspaceSession: vi.fn(),
    isStreaming: vi.fn(() => false),
    onChatWorkspaceChange: vi.fn((callback: (workspace?: any) => void) => {
      workspaceListener = callback;
      callback();
      return () => { workspaceListener = null; };
    }),
    onStreamingChange: vi.fn((callback: (streaming: boolean) => void) => {
      streamingListeners.push(callback);
      callback(false);
      return () => streamingListeners.splice(streamingListeners.indexOf(callback), 1);
    }),
    openWorkspaceSession: vi.fn(),
    sendMessage: vi.fn(() => true),
    stopStream: vi.fn(),
    emitWorkspace(workspace: any) { workspaceListener?.(workspace); },
  };
});

vi.mock("../js/chat.js", () => chat);
vi.mock("../js/api.js", () => ({
  api: vi.fn((path: string) => Promise.resolve(path === "/api/agent-modes" ? { modes: [] } : { settings: {} })),
}));
vi.mock("./code/editorApi", () => ({
  getRoots: vi.fn(() => Promise.resolve([])),
  searchEntries: vi.fn(() => Promise.resolve({ items: [] })),
}));

import { mountChatSurface } from "./chatSurface";

describe("compact chat surface", () => {
  let host: HTMLElement;

  beforeEach(() => {
    host = document.createElement("div");
    document.body.append(host);
    chat.sendMessage.mockClear();
    chat.openWorkspaceSession.mockClear();
    chat.canClearChat.mockReturnValue(false);
  });

  afterEach(() => document.body.replaceChildren());

  it("preserves the composer when save preflight cancels the send", async () => {
    const beforeSend = vi.fn(async () => false as const);
    const surface = mountChatSurface(host, { workspaceId: "workspace-1", surface: "code", beforeSend });
    const input = host.querySelector<HTMLElement>("[data-chat-input]")!;
    input.textContent = "Keep this draft";
    input.dispatchEvent(new Event("input", { bubbles: true }));

    host.querySelector<HTMLFormElement>("[data-chat-form]")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await vi.waitFor(() => expect(beforeSend).toHaveBeenCalledOnce());

    expect(input.textContent).toBe("Keep this draft");
    expect(chat.sendMessage).not.toHaveBeenCalled();
    surface.dispose();
  });

  it("opens and sends on the code surface with editor context", async () => {
    const editorContext = { tabs: [{ kind: "untitled" as const, title: "Untitled-1", content: "draft" }] };
    const surface = mountChatSurface(host, {
      workspaceId: "workspace-2",
      surface: "code",
      beforeSend: async () => editorContext,
    });
    expect(chat.openWorkspaceSession).toHaveBeenCalledWith(expect.any(HTMLElement), "workspace-2", { surface: "code" });
    const input = host.querySelector<HTMLElement>("[data-chat-input]")!;
    input.textContent = "Review it";

    host.querySelector<HTMLFormElement>("[data-chat-form]")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await vi.waitFor(() => expect(chat.sendMessage).toHaveBeenCalledOnce());

    expect(chat.sendMessage).toHaveBeenCalledWith(
      expect.any(HTMLElement), "Review it", undefined, "general", { editorContext },
    );
    expect(input.textContent).toBe("");
    surface.dispose();
  });

  it("re-enables New chat whenever a completed stream can be cleared", () => {
    const surface = mountChatSurface(host, { workspaceId: "workspace-3", surface: "code" });
    const newChat = host.querySelector<HTMLButtonElement>("[data-code-chat-new]")!;
    expect(newChat.disabled).toBe(true);

    chat.canClearChat.mockReturnValue(true);
    chat.streamingListeners.at(-1)!(false);
    expect(newChat.disabled).toBe(false);

    chat.canClearChat.mockReturnValue(false);
    chat.streamingListeners.at(-1)!(true);
    expect(newChat.disabled).toBe(true);
    surface.dispose();
  });

  it("reports whether an exact Code Chat completion target still exists", () => {
    const resolved = vi.fn();
    const surface = mountChatSurface(host, {
      workspaceId: "workspace-4", surface: "code", expectedChatId: "code-expected",
      onExpectedChatResolved: resolved,
    });
    chat.emitWorkspace({
      workspaceId: "workspace-4", surface: "code", activeChatId: "code-replacement", hasSnapshot: true,
    });

    expect(resolved).toHaveBeenCalledWith(false);
    surface.dispose();
  });
});
