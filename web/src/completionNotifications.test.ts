import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const socket = vi.hoisted(() => {
  const handlers = new Map<string, Set<(message: object) => void>>();
  return {
    handlers,
    on: vi.fn((type: string, handler: (message: object) => void) => {
      if (!handlers.has(type)) handlers.set(type, new Set());
      handlers.get(type)!.add(handler);
      return () => handlers.get(type)?.delete(handler);
    }),
    emit(type: string, message: object) {
      for (const handler of handlers.get(type) || []) handler(message);
    },
  };
});

const api = vi.hoisted(() => ({
  get: vi.fn(async () => ({ settings: {} })),
}));

vi.mock("../js/ws.js", () => ({ on: socket.on }));
vi.mock("../js/api.js", () => ({ get: api.get }));

describe("chat completion notifications", () => {
  let module: typeof import("./completionNotifications");
  let permission: NotificationPermission;
  let play: ReturnType<typeof vi.fn>;
  let requestPermission: ReturnType<typeof vi.fn>;
  let created: Array<{ title: string; options?: NotificationOptions; onclick: (() => void) | null; close: ReturnType<typeof vi.fn> }>;

  const completion = (overrides: Record<string, unknown> = {}) => ({
    type: "chat_completed", workspaceId: "workspace-1", workspaceName: "Echo repo",
    surface: "chat", chatId: "chat-1", turnId: "turn-1", preview: "Implement notifications",
    completedAt: "2026-08-19T12:00:00Z", ...overrides,
  });

  beforeEach(async () => {
    vi.resetModules();
    socket.handlers.clear();
    socket.on.mockClear();
    api.get.mockReset();
    api.get.mockResolvedValue({ settings: {} });
    play = vi.fn(async () => undefined);
    class FakeAudio {
      preload = "";
      cloneNode() { return new FakeAudio(); }
      play() { return (play as () => Promise<void>)(); }
    }
    permission = "granted";
    created = [];
    requestPermission = vi.fn(async () => {
      permission = "granted";
      return permission;
    });
    class FakeNotification {
      static get permission() { return permission; }
      static requestPermission = requestPermission;
      onclick: (() => void) | null = null;
      close = vi.fn();
      constructor(public title: string, public options?: NotificationOptions) { created.push(this); }
    }
    vi.stubGlobal("Audio", FakeAudio);
    vi.stubGlobal("Notification", FakeNotification);
    vi.spyOn(document, "hasFocus").mockReturnValue(true);
    vi.spyOn(window, "focus").mockImplementation(() => undefined);
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    window.location.hash = "#/home";
    document.body.innerHTML = '<div data-chat-view-pane="chat"></div>';
    module = await import("./completionNotifications");
    await module.startCompletionNotifications();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    document.body.innerHTML = "";
    window.location.hash = "";
  });

  it("plays every successful completion sound but suppresses an alert for the exact visible chat", () => {
    socket.emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-1", surface: "chat", activeChatId: "chat-1",
    });
    socket.emit("chat_completed", completion());

    expect(play).toHaveBeenCalledOnce();
    expect(created).toHaveLength(0);
  });

  it("alerts for another tab and navigates the notification click to its exact target", () => {
    socket.emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-1", surface: "chat", activeChatId: "chat-2",
    });
    socket.emit("chat_completed", completion());

    expect(created).toHaveLength(1);
    expect(created[0].title).toBe("Chat ready");
    expect(created[0].options?.body).toContain("Echo repo — Implement notifications");
    created[0].onclick?.();
    expect(window.focus).toHaveBeenCalledOnce();
    expect(window.location.hash).toContain("#/home?workspaceId=workspace-1&chatId=chat-1");
    expect(created[0].close).toHaveBeenCalledOnce();
  });

  it("alerts when trajectory is visible or the matching Code Chat dock is closed", () => {
    socket.emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-1", surface: "chat", activeChatId: "chat-1",
    });
    document.querySelector<HTMLElement>("[data-chat-view-pane='chat']")!.hidden = true;
    socket.emit("chat_completed", completion());

    window.location.hash = "#/code";
    document.body.innerHTML = '<aside data-code-chat-dock hidden></aside>';
    socket.emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-1", surface: "code", activeChatId: "code-1",
    });
    socket.emit("chat_completed", completion({ surface: "code", chatId: "code-1" }));

    expect(play).toHaveBeenCalledTimes(2);
    expect(created.map((notification) => notification.title)).toEqual(["Chat ready", "Code Chat ready"]);
  });

  it("alerts for the exact chat when Echo is hidden or unfocused", () => {
    socket.emit("session_snapshot", {
      type: "session_snapshot", workspaceId: "workspace-1", surface: "chat", activeChatId: "chat-1",
    });
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    socket.emit("chat_completed", completion());
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    vi.mocked(document.hasFocus).mockReturnValue(false);
    socket.emit("chat_completed", completion({ turnId: "turn-2" }));

    expect(created).toHaveLength(2);
  });

  it("honors both settings and requests permission from a user-initiated send", async () => {
    module.updateCompletionNotificationSettings({ disableNotificationSounds: true, enableChatCompletionNotifications: false });
    socket.emit("chat_completed", completion());
    expect(play).not.toHaveBeenCalled();
    expect(created).toHaveLength(0);

    permission = "default";
    module.updateCompletionNotificationSettings({ enableChatCompletionNotifications: true });
    module.prepareCompletionNotificationPermission();
    await Promise.resolve();
    expect(requestPermission).toHaveBeenCalledOnce();
  });
});
