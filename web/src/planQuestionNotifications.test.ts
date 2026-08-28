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

const planQuestionSound = vi.hoisted(() => ({
  playPlanQuestionSound: vi.fn(),
}));

vi.mock("../js/ws.js", () => ({ on: socket.on }));
vi.mock("../js/api.js", () => ({ get: api.get }));
vi.mock("./planQuestionSound", () => planQuestionSound);

describe("plan question notifications", () => {
  let module: typeof import("./planQuestionNotifications");
  let permission: NotificationPermission;
  let created: Array<{ title: string; options?: NotificationOptions; onclick: (() => void) | null; close: ReturnType<typeof vi.fn> }>;

  const awaiting = (overrides: Record<string, unknown> = {}) => ({
    type: "plan_questions_awaiting", workspaceId: "workspace-1", workspaceName: "Echo repo",
    surface: "chat", chatId: "chat-1", turnId: "turn-1", callId: "call-1",
    questions: [{ id: "scope", question: "Which scope?", options: ["Core", "Extended"] }],
    ...overrides,
  });

  beforeEach(async () => {
    vi.resetModules();
    socket.handlers.clear();
    socket.on.mockClear();
    api.get.mockReset();
    api.get.mockResolvedValue({ settings: {} });
    planQuestionSound.playPlanQuestionSound.mockClear();
    permission = "granted";
    created = [];
    class FakeNotification {
      static get permission() { return permission; }
      onclick: (() => void) | null = null;
      close = vi.fn();
      constructor(public title: string, public options?: NotificationOptions) { created.push(this); }
    }
    vi.stubGlobal("Notification", FakeNotification);
    vi.spyOn(window, "focus").mockImplementation(() => undefined);
    window.location.hash = "#/home";
    module = await import("./planQuestionNotifications");
    await module.startPlanQuestionNotifications();
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    window.location.hash = "";
  });

  it("always plays the question sound and shows a granted notification", () => {
    socket.emit("plan_questions_awaiting", awaiting());
    expect(planQuestionSound.playPlanQuestionSound).toHaveBeenCalledOnce();
    expect(created).toHaveLength(1);
    expect(created[0].title).toBe("Clarifying question");
    expect(created[0].options?.body).toContain("Echo repo — Which scope?");
  });

  it("uses a Code Chat title for the code surface and deep-links on click", () => {
    socket.emit("plan_questions_awaiting", awaiting({ surface: "code" }));
    expect(created[0].title).toBe("Code Chat question");
    created[0].onclick?.();
    expect(window.focus).toHaveBeenCalledOnce();
    expect(window.location.hash).toContain("#/code?workspaceId=workspace-1&chatId=chat-1&chat=open");
    expect(created[0].close).toHaveBeenCalledOnce();
  });

  it("suppresses the notification (but keeps the sound) when disabled", () => {
    module.updatePlanQuestionNotificationSettings({ enablePlanQuestionNotifications: false });
    socket.emit("plan_questions_awaiting", awaiting());
    expect(planQuestionSound.playPlanQuestionSound).toHaveBeenCalledOnce();
    expect(created).toHaveLength(0);
  });

  it("does not show a notification when permission is denied", () => {
    permission = "denied";
    socket.emit("plan_questions_awaiting", awaiting());
    expect(created).toHaveLength(0);
    expect(planQuestionSound.playPlanQuestionSound).toHaveBeenCalledOnce();
  });

  it("ignores events missing a chatId or surface", () => {
    socket.emit("plan_questions_awaiting", awaiting({ chatId: "", surface: "chat" }));
    socket.emit("plan_questions_awaiting", awaiting({ surface: "other" }));
    expect(planQuestionSound.playPlanQuestionSound).not.toHaveBeenCalled();
    expect(created).toHaveLength(0);
  });
});
