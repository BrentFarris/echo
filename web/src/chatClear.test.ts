import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const chat = vi.hoisted(() => ({
  activateChatTab: vi.fn(() => true),
  canClearChat: vi.fn(() => true),
  canCompressChat: vi.fn(() => true),
  clearChat: vi.fn(() => true),
  compressChat: vi.fn(() => true),
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

const api = vi.hoisted(() => ({
  post: vi.fn(async () => ({ name: "chat-streaming" })),
}));

vi.mock("../js/chat.js", () => chat);
vi.mock("./code/editorApi.ts", () => ({
  getRoots: vi.fn(async () => []),
  revealEntry: vi.fn(),
  searchEntries: vi.fn(async () => ({ items: [], indexing: false, indexed: 0, truncated: false })),
}));

vi.mock("../js/api.js", () => ({
	post: api.post,
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
    chat.compressChat.mockClear();
    api.post.mockClear();
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

  it("queues context compression with the composer model without confirmation", () => {
    root.querySelector<HTMLButtonElement>("[data-chat-more-trigger]")!.click();
    document.querySelector<HTMLButtonElement>("[data-compress-chat-button]")!.click();

    expect(chat.compressChat).toHaveBeenCalledWith(expect.any(HTMLElement), null);
    expect(document.querySelector<HTMLElement>(".chat-more-menu")!.hidden).toBe(true);
  });

  it("creates a skill from the captured active chat and reports success", async () => {
    root.querySelector<HTMLButtonElement>("[data-chat-more-trigger]")!.click();
    document.querySelector<HTMLButtonElement>("[data-create-skill-button]")!.click();

    expect(api.post).toHaveBeenCalledWith("/api/workspaces/workspace-1/chats/chat-1/skills", {});
    expect(document.querySelector<HTMLElement>(".chat-more-menu")!.hidden).toBe(true);
    await Promise.resolve();
    await Promise.resolve();
    expect(document.querySelector(".code-toast")?.textContent).toContain('Created skill "chat-streaming".');
  });

  it("shows skill creation failures in a sticky toast", async () => {
    api.post.mockRejectedValueOnce(new Error("Skill creation returned an invalid result."));
    root.querySelector<HTMLButtonElement>("[data-chat-more-trigger]")!.click();
    document.querySelector<HTMLButtonElement>("[data-create-skill-button]")!.click();

    await Promise.resolve();
    await Promise.resolve();
    expect(document.querySelector(".code-toast")?.textContent).toContain("Skill creation returned an invalid result.");
  });

  it("prevents duplicate creation and clearing while a skill is being generated", async () => {
    let finish!: (value: { name: string }) => void;
    api.post.mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }));
    root.querySelector<HTMLButtonElement>("[data-chat-more-trigger]")!.click();
    document.querySelector<HTMLButtonElement>("[data-create-skill-button]")!.click();

    root.querySelector<HTMLButtonElement>("[data-chat-more-trigger]")!.click();
    expect(document.querySelector<HTMLButtonElement>("[data-create-skill-button]")!.disabled).toBe(true);
    expect(document.querySelector<HTMLButtonElement>("[data-clear-chat-button]")!.disabled).toBe(true);
    document.querySelector<HTMLButtonElement>("[data-create-skill-button]")!.click();
    expect(api.post).toHaveBeenCalledOnce();

    finish({ name: "chat-streaming" });
    await Promise.resolve();
    await Promise.resolve();
  });
});
