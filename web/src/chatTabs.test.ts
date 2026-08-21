import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const chat = vi.hoisted(() => {
  let workspaceHandler: ((state: any) => void) | null = null;
  let commandErrorHandler: ((message: any) => boolean) | null = null;
  return {
    activateChatTab: vi.fn(() => true),
    canClearChat: vi.fn(() => false),
    canCompressChat: vi.fn(() => true),
    clearChat: vi.fn(() => true),
    compressChat: vi.fn(() => true),
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
        tabs: [
          { chatId: "chat-one", preview: "New chat", busy: false },
          { chatId: "chat-two", preview: "Second chat", busy: false },
        ],
      });
      return () => { workspaceHandler = null; };
    }),
    onStreamingChange: vi.fn((callback: (streaming: boolean) => void) => {
      callback(false);
      return vi.fn();
    }),
    openWorkspaceSession: vi.fn(),
    sendMessage: vi.fn((_log: unknown, _text: string, _model?: string, _mode?: string, _options?: any) => true),
    stopStream: vi.fn(),
    emitWorkspace(state: any) { workspaceHandler?.(state); },
    emitCommandError(message: any) { return commandErrorHandler?.(message); },
  };
});

const api = vi.hoisted(() => ({ post: vi.fn() }));
const ui = vi.hoisted(() => ({ toast: vi.fn(), showContextMenu: vi.fn() }));
const editorAPI = vi.hoisted(() => ({
  getRoots: vi.fn(async () => [{
    id: "root", label: "Echo", referenceLabel: "echo", hostPath: "C:/Echo",
  }]),
  revealEntry: vi.fn(async () => undefined),
  searchEntries: vi.fn(async () => ({
    items: [{
      ref: { rootId: "root", path: "main.go" }, name: "main.go",
      hostPath: "C:/Echo/main.go", referencePath: "echo/main.go", kind: "file", score: 1000,
    }],
    indexing: false, indexed: 1, truncated: false,
  })),
}));
const gitBadge = vi.hoisted(() => {
  const stop = vi.fn();
  return { stop, watchGitBadge: vi.fn(() => stop) };
});

vi.mock("../js/chat.js", () => chat);
vi.mock("./gitBadge.ts", () => ({ watchGitBadge: gitBadge.watchGitBadge }));
vi.mock("./code/editorApi.ts", () => editorAPI);
vi.mock("./code/ui.ts", () => ({ toast: ui.toast, showContextMenu: ui.showContextMenu }));
vi.mock("./completionNotifications.ts", () => ({ prepareCompletionNotificationPermission: vi.fn() }));

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
  loadWorkspaces: vi.fn(async () => [{ id: "workspace-tabs", name: "Workspace" }]),
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
    editorAPI.searchEntries.mockClear();
    editorAPI.revealEntry.mockClear();
    gitBadge.watchGitBadge.mockClear();
    gitBadge.stop.mockClear();
    ui.toast.mockClear();
    ui.showContextMenu.mockClear();
    window.location.hash = "";
  });

  function placeCaretAtEnd(element: HTMLElement) {
    const range = document.createRange();
    range.selectNodeContents(element);
    range.collapse(false);
    const selection = window.getSelection()!;
    selection.removeAllRanges();
    selection.addRange(range);
  }

  async function insertMention(query = "main") {
    const editor = root.querySelector<HTMLElement>("[data-chat-input]")!;
    editor.textContent = `Review @${query}`;
    editor.focus();
    placeCaretAtEnd(editor);
    editor.dispatchEvent(new Event("input", { bubbles: true }));
    await new Promise((resolve) => window.setTimeout(resolve, 120));
    await Promise.resolve();
    root.querySelector<HTMLButtonElement>("[data-chat-mention-option]")!.click();
    return editor;
  }

  it("inserts, restores, sends, and opens a file reference chip", async () => {
    chat.emitWorkspace(twoTabs("chat-one"));
    const editor = await insertMention();
    const chip = editor.querySelector<HTMLElement>("[data-chat-file-mention]")!;
    expect(chip.textContent).toContain("main.go");

    chat.emitWorkspace(twoTabs("chat-two"));
    expect(editor.querySelector("[data-chat-file-mention]")).toBeNull();
    chat.emitWorkspace(twoTabs("chat-one"));
    const restored = editor.querySelector<HTMLElement>("[data-chat-file-mention]")!;
    expect(restored.dataset.referencePath).toBe("echo/main.go");

    root.querySelector<HTMLButtonElement>(".send-button")!.click();
    expect(chat.sendMessage.mock.calls.at(-1)?.[1]).toBe("Review @echo/main.go ");
    expect(chat.sendMessage.mock.calls.at(-1)?.[3]).toBe("general");

    chat.sendMessage.mockClear();
    const reopenedEditor = await insertMention();
    reopenedEditor.querySelector<HTMLElement>("[data-chat-file-mention]")!.click();
    expect(window.location.hash).toBe("#/code?rootId=root&path=main.go");
    reopenedEditor.replaceChildren();
    reopenedEditor.dispatchEvent(new Event("input", { bubbles: true }));
  });

  it("opens folder chips through the confined file-browser API", async () => {
    editorAPI.searchEntries.mockResolvedValueOnce({
      items: [{
        ref: { rootId: "root", path: "docs" }, name: "docs",
        hostPath: "C:/Echo/docs", referencePath: "echo/docs", kind: "directory", score: 1000,
      }],
      indexing: false, indexed: 1, truncated: false,
    });
    const editor = await insertMention("docs");
    editor.querySelector<HTMLElement>("[data-chat-file-mention]")!.click();
    await Promise.resolve();
    expect(editorAPI.revealEntry).toHaveBeenCalledWith("workspace-tabs", { rootId: "root", path: "docs" });
    editor.replaceChildren();
    editor.dispatchEvent(new Event("input", { bubbles: true }));
  });

  it("tracks the active workspace's Git changes and stops tracking on unmount", () => {
    expect(gitBadge.watchGitBadge).toHaveBeenCalledWith(root, "workspace-tabs");

    unmount();
    expect(gitBadge.stop).toHaveBeenCalledOnce();
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

  it("activates and cleans an exact completion deep link", async () => {
    unmount();
    chat.activateChatTab.mockClear();
    window.location.hash = "#/home?workspaceId=workspace-tabs&chatId=chat-two";
    mount(root);
    await Promise.resolve();

    expect(chat.activateChatTab).toHaveBeenCalledWith("chat-two");
    chat.emitWorkspace(twoTabs("chat-two"));
    expect(window.location.hash).toBe("#/home");
  });

  it("picks media, restores it per tab, removes drafts, and sends without text", async () => {
    chat.emitWorkspace(twoTabs("chat-one"));
    const editor = root.querySelector<HTMLElement>("[data-chat-input]")!;
    editor.replaceChildren();
    editor.dispatchEvent(new Event("input", { bubbles: true }));
    root.querySelector<HTMLButtonElement>("[data-chat-attachment-trigger]")!.click();
    expect(document.querySelector<HTMLElement>(".chat-attachment-menu")!.hidden).toBe(false);
    document.querySelector<HTMLButtonElement>("[data-attachment-type='image']")!.click();

    const picker = document.querySelector<HTMLInputElement>("[data-chat-media-input='image']")!;
    const image = new File([new Uint8Array([137, 80, 78, 71])], "screen.png", { type: "image/png" });
    const secondImage = new File([new Uint8Array([71, 73, 70, 56])], "animation.gif", { type: "image/gif" });
    Object.defineProperty(picker, "files", { configurable: true, value: [image, secondImage] });
    picker.dispatchEvent(new Event("change"));
    await vi.waitFor(() => expect(root.querySelectorAll(".chat-attachment-draft")).toHaveLength(2));
    expect(root.querySelector(".chat-attachment-draft")?.textContent).toContain("screen.png");

    chat.emitWorkspace(twoTabs("chat-two"));
    expect(root.querySelectorAll(".chat-attachment-draft")).toHaveLength(0);
    chat.emitWorkspace(twoTabs("chat-one"));
    expect(root.querySelectorAll(".chat-attachment-draft")).toHaveLength(2);

    root.querySelector<HTMLButtonElement>(".send-button")!.click();
    const options = chat.sendMessage.mock.calls.at(-1)?.[4];
    expect(chat.sendMessage.mock.calls.at(-1)?.[1]).toBe("");
    expect(options?.images).toEqual([
      expect.objectContaining({ name: "screen.png", mediaType: "image/png", bytes: 4 }),
      expect.objectContaining({ name: "animation.gif", mediaType: "image/gif", bytes: 4 }),
    ]);
    expect(options?.images[0].dataUrl).toMatch(/^data:image\/png;base64,/);
    expect(options?.videos).toEqual([]);
    expect(root.querySelectorAll(".chat-attachment-draft")).toHaveLength(0);

    const pastedVideo = new File([new Uint8Array([0, 0, 0, 1])], "clip.mp4", { type: "video/mp4" });
    const paste = new Event("paste", { bubbles: true, cancelable: true });
    Object.defineProperty(paste, "clipboardData", {
      value: { items: [{ kind: "file", getAsFile: () => pastedVideo }] },
    });
    root.querySelector<HTMLElement>("[data-chat-input]")!.dispatchEvent(paste);
    await vi.waitFor(() => expect(root.querySelectorAll(".chat-attachment-draft.is-video")).toHaveLength(1));
    expect(paste.defaultPrevented).toBe(true);
    root.querySelector<HTMLButtonElement>(".chat-attachment-draft-remove")!.click();
    expect(root.querySelectorAll(".chat-attachment-draft")).toHaveLength(0);
  });

  it("shows a validation toast and keeps oversized image files out of the draft", async () => {
    chat.emitWorkspace(twoTabs("chat-one"));
    root.querySelector<HTMLButtonElement>("[data-chat-attachment-trigger]")!.click();
    document.querySelector<HTMLButtonElement>("[data-attachment-type='image']")!.click();
    const picker = document.querySelector<HTMLInputElement>("[data-chat-media-input='image']")!;
    const oversized = new File([new Uint8Array([137, 80, 78, 71])], "huge.png", { type: "image/png" });
    Object.defineProperty(oversized, "size", { configurable: true, value: 10 * 1024 * 1024 + 1 });
    Object.defineProperty(picker, "files", { configurable: true, value: [oversized] });
    picker.dispatchEvent(new Event("change"));
    await vi.waitFor(() => expect(ui.toast).toHaveBeenCalledWith(
      "huge.png is larger than 10.0 MB.", { sticky: true },
    ));
    expect(root.querySelectorAll(".chat-attachment-draft")).toHaveLength(0);
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

  it("opens trajectory from the view switcher and a tab context menu", () => {
    chat.emitWorkspace(twoTabs("chat-one"));
    const chatPane = root.querySelector<HTMLElement>("[data-chat-view-pane='chat']")!;
    const trajectoryPane = root.querySelector<HTMLElement>("[data-chat-view-pane='trajectory']")!;
    chatPane.querySelector<HTMLButtonElement>("[data-chat-view='trajectory']")!.click();
    expect(chatPane.hidden).toBe(true);
    expect(trajectoryPane.hidden).toBe(false);

    trajectoryPane.querySelector<HTMLButtonElement>("[data-trajectory-view='chat']")!.click();
    expect(chatPane.hidden).toBe(false);
    expect(trajectoryPane.hidden).toBe(true);

    root.querySelector<HTMLButtonElement>("[data-chat-tab-activate='chat-two']")!
      .dispatchEvent(new MouseEvent("contextmenu", { bubbles: true, clientX: 20, clientY: 30 }));
    expect(ui.showContextMenu).toHaveBeenCalledWith(20, 30, expect.any(Array));
    const actions = ui.showContextMenu.mock.calls.at(-1)?.[2];
    actions.find((action: { label: string }) => action.label === "Open trajectory").run();
    expect(chat.activateChatTab).toHaveBeenCalledWith("chat-two");
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
