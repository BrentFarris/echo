import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const chat = vi.hoisted(() => ({
  activateChatTab: vi.fn(() => true),
  canClearChat: vi.fn(() => true),
  clearChat: vi.fn(() => true),
  closeChatTab: vi.fn(() => true),
  closeWorkspaceSession: vi.fn(),
  createChatTab: vi.fn(() => true),
  isStreaming: vi.fn(() => false),
  onChatCommandError: vi.fn(() => vi.fn()),
  onChatWorkspaceChange: vi.fn((callback: (state: any) => void) => {
    callback({
      workspaceId: "workspace-1", activeChatId: "chat-1", hasSnapshot: true,
      tabs: [{ chatId: "chat-1", preview: "New chat", busy: false }],
    });
    return vi.fn();
  }),
  onStreamingChange: vi.fn((callback: (streaming: boolean) => void) => {
    callback(false);
    return vi.fn();
  }),
  openWorkspaceSession: vi.fn(),
  sendMessage: vi.fn(),
  stopStream: vi.fn(),
}));

vi.mock("../js/chat.js", () => chat);

vi.mock("../js/api.js", () => ({
  get: vi.fn(async (path: string) => path === "/api/settings"
    ? { settings: { endpoints: [] } }
    : { modes: [{ id: "general", name: "General", builtIn: true }] }),
}));

vi.mock("../js/workspaces.js", () => ({
  getActive: vi.fn(() => ({ id: "workspace-1", name: "Workspace" })),
  loadWorkspaces: vi.fn(async () => undefined),
  openAddWorkspaceModal: vi.fn(),
  openWorkspaceDropdown: vi.fn(),
  renderWorkspaceIcon: vi.fn(() => "W"),
  setActiveWorkspace: vi.fn(),
}));

import { mount, unmount } from "../js/views/home.js";

describe("clear current chat menu action", () => {
  let root: HTMLElement;

  beforeEach(async () => {
    root = document.createElement("div");
    document.body.appendChild(root);
    mount(root);
    await Promise.resolve();
    await Promise.resolve();
  });

  afterEach(() => {
    unmount();
    document.body.innerHTML = "";
    vi.restoreAllMocks();
    chat.clearChat.mockClear();
  });

  function openClearAction(): HTMLButtonElement {
    root.querySelector<HTMLButtonElement>("[data-chat-more-trigger]")!.click();
    return document.querySelector<HTMLButtonElement>("[data-clear-chat-button]")!;
  }

  it("clears only after the user confirms", () => {
    const confirm = vi.spyOn(window, "confirm").mockReturnValue(true);
    const input = root.querySelector<HTMLElement>("[data-chat-input]")!;
    input.textContent = "unsent draft";

    openClearAction().click();

    expect(confirm).toHaveBeenCalledWith("Clear the current chat?");
    expect(chat.clearChat).toHaveBeenCalledOnce();
    expect(input.textContent).toBe("");
  });

  it("leaves the chat and draft untouched when confirmation is canceled", () => {
    vi.spyOn(window, "confirm").mockReturnValue(false);
    const input = root.querySelector<HTMLElement>("[data-chat-input]")!;
    input.textContent = "keep this draft";

    openClearAction().click();

    expect(chat.clearChat).not.toHaveBeenCalled();
    expect(input.textContent).toBe("keep this draft");
  });
});
