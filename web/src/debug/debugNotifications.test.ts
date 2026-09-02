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

vi.mock("../../js/ws.js", () => ({ on: socket.on }));
const workspaces = vi.hoisted(() => ({ activeId: "workspace-2", setActive: vi.fn(async (id: string) => { workspaces.activeId = id; }) }));
vi.mock("../../js/workspaces.js", () => ({
  getActive: () => ({ id: workspaces.activeId }),
  setActiveWorkspace: workspaces.setActive,
}));

describe("debug stop notifications", () => {
  let module: typeof import("./debugNotifications");
  let permission: NotificationPermission;
  let created: Array<{ title: string; options?: NotificationOptions; onclick: (() => void) | null; close: ReturnType<typeof vi.fn> }>;

  const stopped = (overrides: Record<string, unknown> = {}) => ({
    type: "debug_stopped", phase: "stopped", workspaceId: "workspace-1", sessionId: "session-1",
    configuration: "Editor", stopGeneration: 3, stoppedReason: "breakpoint", ...overrides,
  });

  beforeEach(async () => {
    vi.useFakeTimers();
    vi.resetModules();
    socket.handlers.clear();
    socket.on.mockClear();
    workspaces.activeId = "workspace-2";
    workspaces.setActive.mockClear();
    permission = "granted";
    created = [];
    class FakeNotification {
      static get permission() { return permission; }
      static requestPermission = vi.fn(async () => permission);
      onclick: (() => void) | null = null;
      close = vi.fn();
      constructor(public title: string, public options?: NotificationOptions) { created.push(this); }
    }
    vi.stubGlobal("Notification", FakeNotification);
    vi.spyOn(document, "hasFocus").mockReturnValue(false);
    vi.spyOn(window, "focus").mockImplementation(() => undefined);
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    localStorage.clear();
    window.location.hash = "#/home";
    module = await import("./debugNotifications");
    module.startDebugStopNotifications();
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    localStorage.clear();
    window.location.hash = "";
  });

  it("notifies a background browser and focuses the stopped session when clicked", async () => {
    localStorage.setItem("echo.debug.ui.v1:workspace-1", JSON.stringify({ collapsed: ["watch"] }));
    socket.emit("debug_stopped", stopped());
    await vi.advanceTimersByTimeAsync(400);

    expect(created).toHaveLength(1);
    expect(created[0].title).toBe("Breakpoint hit");
    expect(created[0].options?.body).toBe("Editor");
    created[0].onclick?.();
    await Promise.resolve();
    await Promise.resolve();

    expect(window.focus).toHaveBeenCalledOnce();
    expect(workspaces.setActive).toHaveBeenCalledWith("workspace-1");
    expect(window.location.hash).toBe("#/code?sidebar=debug");
    expect(JSON.parse(localStorage.getItem("echo.debug.ui.v1:workspace-1") || "{}")).toEqual({ collapsed: ["watch"], selectedSessionId: "session-1" });
    expect(created[0].close).toHaveBeenCalledOnce();
  });

  it("uses the hydrated source location and does not duplicate a stop", async () => {
    socket.emit("debug_stopped", stopped());
    socket.emit("debug_stopped", stopped({ phase: "location", location: { name: "main.go", line: 42, column: 3 } }));
    await vi.runAllTimersAsync();
    socket.emit("debug_stopped", stopped({ phase: "location", location: { name: "main.go", line: 42 } }));

    expect(created).toHaveLength(1);
    expect(created[0].options?.body).toBe("Editor — main.go:42");
  });

  it("suppresses stops that occur while Echo is focused", async () => {
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "visible" });
    vi.mocked(document.hasFocus).mockReturnValue(true);
    socket.emit("debug_stopped", stopped());
    Object.defineProperty(document, "visibilityState", { configurable: true, value: "hidden" });
    vi.mocked(document.hasFocus).mockReturnValue(false);
    socket.emit("debug_stopped", stopped({ phase: "location", location: { name: "main.go", line: 9 } }));
    await vi.runAllTimersAsync();

    expect(created).toHaveLength(0);
  });

  it("honors the browser-local preference and browser permission", async () => {
    module.setDebugStopNotificationsEnabled(false);
    socket.emit("debug_stopped", stopped());
    await vi.runAllTimersAsync();
    expect(created).toHaveLength(0);

    module.setDebugStopNotificationsEnabled(true);
    permission = "denied";
    socket.emit("debug_stopped", stopped({ stopGeneration: 4 }));
    await vi.runAllTimersAsync();
    expect(created).toHaveLength(0);
  });
});
