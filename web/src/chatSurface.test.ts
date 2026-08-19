import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const chat = vi.hoisted(() => ({
  canClearChat: vi.fn(() => false),
  clearChat: vi.fn(() => false),
  closeWorkspaceSession: vi.fn(),
  isStreaming: vi.fn(() => false),
  onChatWorkspaceChange: vi.fn((callback: () => void) => { callback(); return () => undefined; }),
  onStreamingChange: vi.fn((callback: (streaming: boolean) => void) => { callback(false); return () => undefined; }),
  openWorkspaceSession: vi.fn(),
  sendMessage: vi.fn(() => true),
  stopStream: vi.fn(),
}));

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
});
