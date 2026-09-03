import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const chat = vi.hoisted(() => {
  const streamingListeners: Array<(streaming: boolean) => void> = [];
  let workspaceListener: ((workspace?: any) => void) | null = null;
  return {
    streamingListeners,
    canClearChat: vi.fn(() => false),
    clearChat: vi.fn(() => false),
    clearGoal: vi.fn(() => true),
    closeWorkspaceSession: vi.fn(),
    editGoal: vi.fn(() => true),
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
    pauseGoal: vi.fn(() => true),
    resumeGoal: vi.fn(() => true),
    sendMessage: vi.fn(() => true),
    startGoal: vi.fn(() => true),
    steerGoal: vi.fn(() => true),
    stopStream: vi.fn(),
    emitWorkspace(workspace: any) { workspaceListener?.(workspace); },
  };
});

vi.mock("../js/chat.js", () => chat);
vi.mock("../js/api.js", () => ({
  api: vi.fn((path: string) => Promise.resolve(path === "/api/agent-modes"
    ? { modes: [{ id: "general", name: "General" }, { id: "goal", name: "Goal" }] }
    : { settings: {} })),
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
    chat.startGoal.mockClear();
    chat.steerGoal.mockClear();
    chat.pauseGoal.mockClear();
    chat.resumeGoal.mockClear();
    chat.editGoal.mockClear();
    chat.clearGoal.mockClear();
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
    expect(chat.openWorkspaceSession).toHaveBeenCalledWith(
      expect.any(HTMLElement), "workspace-2", expect.objectContaining({ surface: "code", onActivateFile: expect.any(Function) }),
    );
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

  it("sends deduplicated structured mentions without changing prompt text", async () => {
    const surface = mountChatSurface(host, { workspaceId: "workspace-mentions", surface: "code" });
    const input = host.querySelector<HTMLElement>("[data-chat-input]")!;
    const mention = () => {
      const chip = document.createElement("span");
      chip.dataset.chatFileMention = "";
      chip.dataset.workspaceId = "workspace-mentions";
      chip.dataset.rootId = "root";
      chip.dataset.workspacePath = "src/main.ts";
      chip.dataset.workspaceKind = "file";
      chip.dataset.referencePath = "echo/src/main.ts";
      chip.dataset.referenceLabel = "main.ts";
      chip.textContent = "main.ts";
      return chip;
    };
    input.append("Review ", mention(), " and ", mention());

    host.querySelector<HTMLFormElement>("[data-chat-form]")!.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await vi.waitFor(() => expect(chat.sendMessage).toHaveBeenCalledOnce());

    expect(chat.sendMessage).toHaveBeenCalledWith(
      expect.any(HTMLElement), "Review @echo/src/main.ts and @echo/src/main.ts", undefined, "general", {
        editorContext: undefined,
        references: [{
          ref: { rootId: "root", path: "src/main.ts" }, kind: "file",
          referencePath: "echo/src/main.ts", label: "main.ts",
        }],
      },
    );
    surface.dispose();
  });

  it("lets plain Enter insert a newline on coarse-pointer devices instead of submitting", async () => {
    const originalMatchMedia = window.matchMedia;
    const setCoarse = (coarse: boolean) => {
      window.matchMedia = ((query: string) => ({
        matches: query === "(pointer: coarse)" ? coarse : false,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
      })) as unknown as typeof window.matchMedia;
    };

    try {
      const surface = mountChatSurface(host, { workspaceId: "workspace-coarse", surface: "code" });
      const input = host.querySelector<HTMLElement>("[data-chat-input]")!;
      input.textContent = "Line one";
      const keydown = new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true });

      setCoarse(true);
      input.dispatchEvent(keydown);
      expect(chat.sendMessage).not.toHaveBeenCalled();
      expect(input.textContent).toBe("Line one");

      surface.dispose();

      const surface2 = mountChatSurface(host, { workspaceId: "workspace-fine", surface: "code" });
      const input2 = host.querySelector<HTMLElement>("[data-chat-input]")!;
      input2.textContent = "Line one";
      setCoarse(false);
      input2.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true, cancelable: true }));
      await vi.waitFor(() => expect(chat.sendMessage).toHaveBeenCalled());
      surface2.dispose();
    } finally {
      window.matchMedia = originalMatchMedia;
    }
  });

  it("shows and clears the selected-context notice", () => {
    const surface = mountChatSurface(host, { workspaceId: "workspace-notice", surface: "code" });
    const notice = host.querySelector<HTMLElement>("[data-chat-context-notice]")!;

    expect(notice.hidden).toBe(true);
    surface.setContextNotice("Selected context: main.go, lines 12\u201327 will be included.");
    expect(notice.hidden).toBe(false);
    expect(notice.textContent).toBe("Selected context: main.go, lines 12\u201327 will be included.");

    surface.setContextNotice(null);
    expect(notice.hidden).toBe(true);
    expect(notice.textContent).toBe("");
    surface.dispose();
  });

  it("routes changed files through the Code Chat reference activator", async () => {
    const onActivateReference = vi.fn();
    const surface = mountChatSurface(host, { workspaceId: "workspace-files", surface: "code", onActivateReference });
    const sessionOptions = chat.openWorkspaceSession.mock.calls.at(-1)?.[2];

    await sessionOptions.onActivateFile({ rootId: "root", path: "src/main.ts" });

    expect(onActivateReference).toHaveBeenCalledWith({
      workspaceId: "workspace-files",
      ref: { rootId: "root", path: "src/main.ts" },
      kind: "file",
      referencePath: "src/main.ts",
      label: "main.ts",
    });
    surface.dispose();
  });

  it("routes historical resources through the Code Chat resource activator", async () => {
    const onActivateHistoricalResource = vi.fn();
    const surface = mountChatSurface(host, {
      workspaceId: "workspace-history", surface: "code", onActivateHistoricalResource,
    });
    const sessionOptions = chat.openWorkspaceSession.mock.calls.at(-1)?.[2];
    const resource = {
      kind: "diff", label: "main.go (Index)", referencePath: "echo/main.go",
      diff: { repositoryId: "repo", path: "main.go", scope: "staged" },
      selection: { side: "original", startLine: 3, startColumn: 1, endLine: 4, endColumn: 2 },
    };

    await sessionOptions.onActivateResource(resource);

    expect(onActivateHistoricalResource).toHaveBeenCalledWith(resource);
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

  it("locks Goal mode, sends steering, and exposes lifecycle controls", async () => {
    const surface = mountChatSurface(host, { workspaceId: "workspace-goal", surface: "code" });
    const mode = host.querySelector<HTMLSelectElement>("[data-code-chat-mode]")!;
    const model = host.querySelector<HTMLSelectElement>("[data-code-chat-model]")!;
    const input = host.querySelector<HTMLElement>("[data-chat-input]")!;
    const form = host.querySelector<HTMLFormElement>("[data-chat-form]")!;
    const goalBar = host.querySelector<HTMLElement>("[data-goal-bar]")!;
    await vi.waitFor(() => expect([...mode.options].map((option) => option.value)).toContain("goal"));

    chat.emitWorkspace({
      workspaceId: "workspace-goal", surface: "code", activeChatId: "code-goal", hasSnapshot: true,
      goal: {
        id: "goal-1", objective: "Ship a verified change", status: "active", model: "",
        activeSeconds: 12, stepCount: 2, pendingSteering: 0,
      },
    });
    expect(goalBar.hidden).toBe(false);
    expect(goalBar.textContent).toContain("Ship a verified change");
    expect(goalBar.textContent).toContain("2 steps");
    expect(mode.value).toBe("goal");
    expect(mode.disabled).toBe(true);
    expect(model.disabled).toBe(true);
    expect(input.dataset.placeholder).toBe("Add guidance to this goal");

    input.textContent = "Run the Code Chat regression";
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await vi.waitFor(() => expect(chat.steerGoal).toHaveBeenCalledOnce());
    expect(chat.steerGoal).toHaveBeenCalledWith(
      expect.any(HTMLElement), "Run the Code Chat regression", { editorContext: undefined },
    );
    expect(chat.startGoal).not.toHaveBeenCalled();

    host.querySelector<HTMLButtonElement>("[data-goal-action='pause']")!.click();
    expect(chat.pauseGoal).toHaveBeenCalledOnce();
    chat.emitWorkspace({
      workspaceId: "workspace-goal", surface: "code", activeChatId: "code-goal", hasSnapshot: true,
      goal: { id: "goal-1", objective: "Ship a verified change", status: "paused", activeSeconds: 13, stepCount: 2, pendingSteering: 1 },
    });
    expect(input.dataset.placeholder).toBe("Queue guidance, then resume when ready");
    host.querySelector<HTMLButtonElement>("[data-goal-action='resume']")!.click();
    expect(chat.resumeGoal).toHaveBeenCalledOnce();

    chat.emitWorkspace({
      workspaceId: "workspace-goal", surface: "code", activeChatId: "code-goal", hasSnapshot: true,
      goal: { id: "goal-1", objective: "Ship a verified change", status: "blocked", activeSeconds: 13, stepCount: 3, pendingSteering: 0 },
    });
    expect(mode.disabled).toBe(false);
    expect(model.disabled).toBe(false);
    host.querySelector<HTMLButtonElement>("[data-goal-action='new']")!.click();
    expect(mode.value).toBe("goal");
    expect(input.dataset.placeholder).toBe("Describe the new goal and its completion criteria");
    input.textContent = "Start a replacement goal";
    form.dispatchEvent(new Event("submit", { bubbles: true, cancelable: true }));
    await vi.waitFor(() => expect(chat.startGoal).toHaveBeenCalledOnce());
    expect(chat.startGoal).toHaveBeenCalledWith(
      expect.any(HTMLElement), "Start a replacement goal", undefined, { editorContext: undefined },
    );
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
