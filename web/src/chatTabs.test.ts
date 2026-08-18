import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const chat = vi.hoisted(() => {
  let workspaceHandler: ((state: any) => void) | null = null;
  let commandErrorHandler: ((message: any) => boolean) | null = null;
  return {
    activateChatTab: vi.fn(() => true),
    canClearChat: vi.fn(() => false),
    clearChat: vi.fn(() => true),
    closeChatTab: vi.fn(() => true),
    closeWorkspaceSession: vi.fn(),
    createChatTab: vi.fn(() => true),
    isStreaming: vi.fn(() => false),
    onChatCommandError: vi.fn((callback: (message: any) => boolean) => {
      commandErrorHandler = callback;
      return () => { commandErrorHandler = null; };
    }),
    onChatWorkspaceChange: vi.fn((callback: (state: any) => void) => {
      workspaceHandler = callback;
      callback({
        workspaceId: "workspace-tabs", activeChatId: "chat-one", hasSnapshot: true,
        tabs: [{ chatId: "chat-one", preview: "New chat", busy: false }],
      });
      return () => { workspaceHandler = null; };
    }),
    onStreamingChange: vi.fn((callback: (streaming: boolean) => void) => {
      callback(false);
      return vi.fn();
    }),
    openWorkspaceSession: vi.fn(),
    sendMessage: vi.fn(() => true),
    stopStream: vi.fn(),
    emitWorkspace(state: any) { workspaceHandler?.(state); },
    emitCommandError(message: any) { return commandErrorHandler?.(message); },
  };
});

const api = vi.hoisted(() => ({ post: vi.fn() }));

vi.mock("../js/chat.js", () => chat);

vi.mock("../js/api.js", () => ({
  post: api.post,
  get: vi.fn(async (path: string) => path === "/api/settings"
    ? {
        settings: {
          endpoints: [
            { id: "primary", name: "Primary", model: "model-primary" },
            { id: "fast", name: "Fast", model: "model-fast" },
          ],
          endpointSelection: { chat: "primary" },
        },
      }
    : { modes: [
        { id: "general", name: "General", builtIn: true },
        { id: "review", name: "Review", builtIn: false, prompt: "Review changes" },
      ] }),
}));

vi.mock("../js/workspaces.js", () => ({
  getActive: vi.fn(() => ({ id: "workspace-tabs", name: "Workspace" })),
  loadWorkspaces: vi.fn(async () => undefined),
  openAddWorkspaceModal: vi.fn(),
  openWorkspaceDropdown: vi.fn(),
  renderWorkspaceIcon: vi.fn(() => "W"),
  setActiveWorkspace: vi.fn(),
}));

import { mount, unmount } from "../js/views/home.js";

function twoTabs(activeChatId = "chat-one", busyChatId = "") {
  return {
    workspaceId: "workspace-tabs", activeChatId, hasSnapshot: true,
    tabs: [
      { chatId: "chat-one", preview: "First prompt for this work", busy: busyChatId === "chat-one" },
      { chatId: "chat-two", preview: "Second prompt with a long descriptive label", busy: busyChatId === "chat-two" },
    ],
  };
}

describe("multi-chat tab UI", () => {
  let root: HTMLElement;

  beforeEach(async () => {
    root = document.createElement("div");
    document.body.append(root);
    Element.prototype.scrollIntoView = vi.fn();
    mount(root);
    await Promise.resolve();
    await Promise.resolve();
  });

  afterEach(() => {
    unmount();
    document.body.innerHTML = "";
    vi.restoreAllMocks();
    chat.activateChatTab.mockClear();
    chat.closeChatTab.mockClear();
    chat.createChatTab.mockClear();
    chat.canClearChat.mockReturnValue(false);
    api.post.mockReset();
  });

  it("creates a new tab from the menu and shows the shared active tab", () => {
    const menuTrigger = root.querySelector<HTMLButtonElement>("[data-chat-more-trigger]")!;
    menuTrigger.click();
    document.querySelector<HTMLButtonElement>("[data-new-chat-tab-button]")!.click();

    expect(chat.createChatTab).toHaveBeenCalledOnce();
    expect(document.querySelector<HTMLElement>(".chat-more-menu")!.hidden).toBe(true);

    chat.emitWorkspace(twoTabs("chat-two"));
    const tabs = root.querySelector<HTMLElement>("[data-chat-tabs]")!;
    expect(root.querySelector<HTMLElement>("[data-chat-tabs-shell]")!.hidden).toBe(false);
    expect(tabs.querySelectorAll("[role='tab']")).toHaveLength(2);
    const active = tabs.querySelector<HTMLElement>("[aria-selected='true']")!;
    expect(active.dataset.chatTabActivate).toBe("chat-two");
    expect(active.title).toBe("Second prompt with a long descriptive label");
  });

  it("restores a separate unsent draft when shared activation changes", () => {
    chat.emitWorkspace(twoTabs("chat-one"));
    const editor = root.querySelector<HTMLElement>("[data-chat-input]")!;
    root.querySelector<HTMLButtonElement>("[data-model-trigger]")!.click();
    document.querySelector<HTMLButtonElement>("[data-model-value='model-fast']")!.click();
    root.querySelector<HTMLButtonElement>("[data-mode-trigger]")!.click();
    document.querySelector<HTMLButtonElement>("[data-mode-id='review']")!.click();
    editor.textContent = "draft for one";
    editor.dispatchEvent(new Event("input"));

    chat.emitWorkspace(twoTabs("chat-two"));
    expect(editor.textContent).toBe("");
    expect(root.querySelector("[data-model-label]")?.textContent).toBe("Primary");
    expect(root.querySelector("[data-mode-label]")?.textContent).toBe("General");
    editor.textContent = "draft for two";
    editor.dispatchEvent(new Event("input"));

    chat.emitWorkspace(twoTabs("chat-one"));
    expect(editor.textContent).toBe("draft for one");
    expect(root.querySelector("[data-model-label]")?.textContent).toBe("Fast");
    expect(root.querySelector("[data-mode-label]")?.textContent).toBe("Review");
    chat.emitWorkspace(twoTabs("chat-two"));
    expect(editor.textContent).toBe("draft for two");
  });

  it("uses roving keyboard navigation and exposes a busy indicator", () => {
    chat.emitWorkspace(twoTabs("chat-one", "chat-two"));
    const first = root.querySelector<HTMLButtonElement>("[data-chat-tab-activate='chat-one']")!;
    const second = root.querySelector<HTMLButtonElement>("[data-chat-tab-activate='chat-two']")!;
    expect(first.tabIndex).toBe(0);
    expect(second.tabIndex).toBe(-1);
    expect(second.querySelector(".chat-tab-busy-dot")).not.toBeNull();

    first.focus();
    first.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    expect(chat.activateChatTab).toHaveBeenCalledWith("chat-two");
    expect(document.activeElement).toBe(second);
  });

  it("exposes scroll controls when the tab strip overflows", () => {
    chat.emitWorkspace({
      workspaceId: "workspace-tabs", activeChatId: "overflow-1", hasSnapshot: true,
      tabs: Array.from({ length: 10 }, (_, index) => ({
        chatId: `overflow-${index + 1}`, preview: `Overflow chat ${index + 1}`, busy: false,
      })),
    });
    const shell = root.querySelector<HTMLElement>("[data-chat-tabs-shell]")!;
    const tabs = root.querySelector<HTMLElement>("[data-chat-tabs]")!;
    Object.defineProperty(tabs, "clientWidth", { configurable: true, value: 360 });
    Object.defineProperty(tabs, "scrollWidth", { configurable: true, value: 1200 });
    Object.defineProperty(tabs, "scrollLeft", { configurable: true, value: 0, writable: true });
    tabs.scrollBy = vi.fn();

    window.dispatchEvent(new Event("resize"));
    expect(shell.classList.contains("has-overflow")).toBe(true);
    const next = root.querySelector<HTMLButtonElement>("[data-chat-tabs-scroll='next']")!;
    expect(next.disabled).toBe(false);
    next.click();
    expect(tabs.scrollBy).toHaveBeenCalledWith(expect.objectContaining({ behavior: "smooth" }));
    expect((tabs.scrollBy as ReturnType<typeof vi.fn>).mock.calls[0][0].left).toBeGreaterThan(0);

    tabs.dispatchEvent(new WheelEvent("wheel", { deltaY: 80, bubbles: true, cancelable: true }));
    expect(tabs.scrollLeft).toBe(80);
  });

  it("confirms before stopping and closing a busy tab", async () => {
    chat.emitWorkspace(twoTabs("chat-one", "chat-two"));
    root.querySelector<HTMLButtonElement>("[data-chat-tab-close='chat-two']")!.click();

    expect(chat.closeChatTab).not.toHaveBeenCalled();
    const dialog = document.querySelector<HTMLDialogElement>(".chat-close-dialog")!;
    expect(dialog.textContent).toContain("Chat is still running");
    dialog.querySelector<HTMLButtonElement>("[data-chat-close-choice='confirm']")!.click();
    await Promise.resolve();
    expect(chat.closeChatTab).toHaveBeenCalledWith("chat-two", true);
  });

  it("recovers a busy-state race rejected by the server", async () => {
    chat.emitWorkspace(twoTabs("chat-one"));
    root.querySelector<HTMLButtonElement>("[data-chat-tab-close='chat-two']")!.click();
    expect(chat.closeChatTab).toHaveBeenCalledWith("chat-two", false);

    expect(chat.emitCommandError({ code: "session_busy", chatId: "chat-two" })).toBe(true);
    document.querySelector<HTMLButtonElement>("[data-chat-close-choice='confirm']")!.click();
    await Promise.resolve();
    expect(chat.closeChatTab).toHaveBeenLastCalledWith("chat-two", true);
  });

  it("keeps skill creation targeted at the initiating tab when selection changes", async () => {
    chat.canClearChat.mockReturnValue(true);
    let finish!: (value: { name: string }) => void;
    api.post.mockReturnValueOnce(new Promise((resolve) => { finish = resolve; }));
    chat.emitWorkspace(twoTabs("chat-one"));
    root.querySelector<HTMLButtonElement>("[data-chat-more-trigger]")!.click();
    document.querySelector<HTMLButtonElement>("[data-create-skill-button]")!.click();

    chat.emitWorkspace(twoTabs("chat-two"));
    expect(api.post).toHaveBeenCalledWith("/api/workspaces/workspace-tabs/chats/chat-one/skills", {});

    finish({ name: "chat-streaming" });
    await Promise.resolve();
    await Promise.resolve();
  });
});
