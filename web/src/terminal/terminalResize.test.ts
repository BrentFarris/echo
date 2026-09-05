import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { TerminalSnapshot } from "./terminalApi";

const mocks = vi.hoisted(() => ({
  handlers: new Map<string, (message: object) => void>(),
  observers: [] as Array<() => void>,
  cols: 120, rows: 30,
  start: vi.fn(), sync: vi.fn(), resize: vi.fn(), restart: vi.fn(), toast: vi.fn(),
}));
vi.mock("../../js/ws.js", () => ({
  on: (type: string, handler: (message: object) => void) => mocks.handlers.set(type, handler),
  onState: vi.fn(), send: vi.fn(),
}));
vi.mock("../code/ui", () => ({ toast: mocks.toast }));
vi.mock("./terminalApi", () => ({
  startTerminal: mocks.start, syncTerminal: mocks.sync, resizeTerminal: mocks.resize,
  restartTerminal: mocks.restart, listTerminalSessions: vi.fn(async () => []),
  listSavedCommands: vi.fn(async () => []), writeTerminal: vi.fn(), stopTerminal: vi.fn(),
  createSavedCommand: vi.fn(), deleteSavedCommand: vi.fn(), updateSavedCommand: vi.fn(),
}));
vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    options = {}; cols = 80; rows = 24; element?: HTMLElement;
    private resized?: (size: { cols: number; rows: number }) => void;
    loadAddon(addon: { activate?(terminal: unknown): void }) { addon.activate?.(this); }
    onData() {} reset() {} focus() {} write() {} writeln() {} dispose() {}
    onResize(handler: typeof this.resized) { this.resized = handler; }
    open(host: HTMLElement) { this.element = host; }
    resize(cols: number, rows: number) {
      if (this.cols === cols && this.rows === rows) return;
      this.cols = cols;
      this.rows = rows;
      this.resized?.({ cols, rows });
    }
  },
}));
vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class {
    private terminal?: { resize(cols: number, rows: number): void };
    activate(terminal: typeof this.terminal) { this.terminal = terminal; }
    fit() { this.terminal?.resize(mocks.cols, mocks.rows); }
  },
}));
vi.mock("@xterm/addon-web-links", () => ({ WebLinksAddon: class {} }));

const workspace = { id: "workspace", name: "Project", mainPath: "/project" };
const snapshot = (id = "shell", status = "running"): TerminalSnapshot => ({
  workspaceId: workspace.id, id, kind: "default", status, shell: "PowerShell",
  workingDirectory: "/project", lastSequence: 0, output: [],
});
function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}
function resizePanel(cols: number, rows = 30) {
  mocks.cols = cols;
  mocks.rows = rows;
  mocks.observers.forEach((notify) => notify());
}

describe("terminal PTY size synchronization", () => {
  let dock: typeof import("./index");
  beforeEach(async () => {
    vi.resetModules();
    vi.resetAllMocks();
    vi.useFakeTimers();
    mocks.handlers.clear();
    mocks.observers.length = 0;
    mocks.cols = 120;
    mocks.rows = 30;
    localStorage.clear();
    sessionStorage.clear();
    localStorage.setItem("echo.terminalDock.v1", JSON.stringify({ workspace: { open: true } }));
    vi.spyOn(HTMLElement.prototype, "clientWidth", "get").mockReturnValue(1000);
    vi.spyOn(HTMLElement.prototype, "clientHeight", "get").mockReturnValue(300);
    vi.stubGlobal("ResizeObserver", class {
      constructor(notify: () => void) { mocks.observers.push(notify); }
      observe() {} disconnect() {}
    });
    mocks.start.mockResolvedValue(snapshot());
    mocks.sync.mockResolvedValue(snapshot());
    mocks.restart.mockResolvedValue(snapshot("replacement"));
    mocks.resize.mockResolvedValue(undefined);
    dock = await import("./index");
    document.body.innerHTML = '<div id="dock"></div>';
    dock.mountTerminalDock(document.getElementById("dock"), workspace);
  });
  afterEach(() => {
    dock.detachTerminalDock();
    vi.clearAllTimers();
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  async function connect() {
    mocks.handlers.get("terminal_subscribed")!({ workspaceId: workspace.id });
    await vi.advanceTimersByTimeAsync(200);
  }

  it("fits before starting and sends a size changed while start was pending", async () => {
    const start = deferred<TerminalSnapshot>();
    mocks.start.mockReturnValue(start.promise);
    await connect();
    expect(mocks.start).toHaveBeenCalledWith(workspace.id, 120, 30);
    resizePanel(160, 40);
    await vi.advanceTimersByTimeAsync(200);
    expect(mocks.resize).not.toHaveBeenCalled();
    start.resolve(snapshot());
    await vi.advanceTimersByTimeAsync(200);
    expect(mocks.resize).toHaveBeenCalledExactlyOnceWith(workspace.id, "shell", 160, 40);
  });

  it("resends unchanged dimensions on reconnect and when attaching an existing shell", async () => {
    await connect();
    expect(mocks.resize).toHaveBeenCalledExactlyOnceWith(workspace.id, "shell", 120, 30);
    mocks.resize.mockClear();
    await connect();
    expect(mocks.sync).toHaveBeenCalledWith(workspace.id, "shell", 0);
    expect(mocks.resize).toHaveBeenCalledExactlyOnceWith(workspace.id, "shell", 120, 30);
  });

  it("resizes a debug terminal even if it already fits before its snapshot arrives", async () => {
    await connect();
    const sync = deferred<TerminalSnapshot>();
    mocks.sync.mockReturnValue(sync.promise);
    mocks.resize.mockClear();
    mocks.handlers.get("terminal_event")!({
      workspaceId: workspace.id, sessionId: "debug", kind: "debug", event: "started", name: "Kaiju",
    });
    await vi.advanceTimersByTimeAsync(200);
    expect(mocks.resize).not.toHaveBeenCalled();
    sync.resolve({ ...snapshot("debug"), kind: "debug" });
    await vi.advanceTimersByTimeAsync(200);
    expect(mocks.resize).toHaveBeenCalledExactlyOnceWith(workspace.id, "debug", 120, 30);
  });

  it("serializes resizes and coalesces intermediate dimensions", async () => {
    await connect();
    const first = deferred<void>();
    mocks.resize.mockClear();
    mocks.resize.mockReturnValueOnce(first.promise);
    resizePanel(130);
    await vi.advanceTimersByTimeAsync(200);
    resizePanel(140);
    await vi.advanceTimersByTimeAsync(200);
    resizePanel(150);
    await vi.advanceTimersByTimeAsync(200);
    expect(mocks.resize).toHaveBeenCalledTimes(1);
    first.resolve();
    await vi.advanceTimersByTimeAsync(0);
    expect(mocks.resize.mock.calls).toEqual([
      [workspace.id, "shell", 130, 30], [workspace.id, "shell", 150, 30],
    ]);
  });

  it("does not send stale dimensions to a replacement session", async () => {
    await connect();
    mocks.resize.mockClear();
    resizePanel(150);
    await vi.advanceTimersByTimeAsync(20);
    document.querySelector<HTMLButtonElement>('[data-terminal-action="restart"]')!.click();
    await vi.advanceTimersByTimeAsync(200);
    expect(mocks.resize).toHaveBeenCalledExactlyOnceWith(workspace.id, "replacement", 150, 30);
  });

  it("does not resize an exited terminal or a detached panel", async () => {
    await connect();
    mocks.resize.mockClear();
    resizePanel(150);
    await vi.advanceTimersByTimeAsync(20);
    dock.detachTerminalDock();
    await vi.advanceTimersByTimeAsync(200);
    expect(mocks.resize).not.toHaveBeenCalled();
    mocks.handlers.get("terminal_event")!({ workspaceId: workspace.id, sessionId: "shell", event: "exited" });
    dock.mountTerminalDock(document.getElementById("dock"), workspace);
    await vi.advanceTimersByTimeAsync(200);
    expect(mocks.resize).not.toHaveBeenCalled();
  });

  it("reports a resize failure without entering a resync loop and retries on the next fit", async () => {
    mocks.resize.mockRejectedValueOnce(new Error("Resize failed"));
    await connect();
    await vi.advanceTimersByTimeAsync(1000);
    expect(mocks.resize).toHaveBeenCalledTimes(1);
    expect(mocks.sync).not.toHaveBeenCalled();
    expect(mocks.toast).toHaveBeenCalledWith("Resize failed");
    resizePanel(120);
    await vi.advanceTimersByTimeAsync(200);
    expect(mocks.resize).toHaveBeenCalledTimes(2);
  });
});
