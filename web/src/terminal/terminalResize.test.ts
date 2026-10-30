import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { TerminalSnapshot } from "./terminalApi";

const mocks = vi.hoisted(() => ({
  handlers: new Map<string, (message: object) => void>(),
  observers: [] as Array<() => void>,
  cols: 120, rows: 30,
  input: undefined as undefined | ((data: string) => void), write: vi.fn(),
  start: vi.fn(), sync: vi.fn(), resize: vi.fn(), restart: vi.fn(), toast: vi.fn(),
}));
vi.mock("./socket", () => ({
  on: (type: string, handler: (message: object) => void) => mocks.handlers.set(type, handler),
  onState: vi.fn(), send: vi.fn(), closeTerminalSocket: vi.fn(),
}));
vi.mock("./ui", () => ({ toast: mocks.toast, copyText: vi.fn() }));
vi.mock("./terminalApi", () => ({
  startTerminal: mocks.start, syncTerminal: mocks.sync, resizeTerminal: mocks.resize,
  restartTerminal: mocks.restart, listTerminalSessions: vi.fn(async () => []),
  listSavedCommands: vi.fn(async () => []), writeTerminal: mocks.write, stopTerminal: vi.fn(),
  createSavedCommand: vi.fn(), deleteSavedCommand: vi.fn(), updateSavedCommand: vi.fn(), changeTerminalFolder: vi.fn(),
}));
vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    options = {}; cols = 80; rows = 24; element?: HTMLElement;
    private resized?: (size: { cols: number; rows: number }) => void;
    loadAddon(addon: { activate?(terminal: unknown): void }) { addon.activate?.(this); }
    onData(handler: (data: string) => void) { mocks.input = handler; } reset() {} focus() {} write() {} writeln() {} dispose() {}
    attachCustomKeyEventHandler() {}
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

const bot = { id: "bot", name: "Project", mainPath: "/project" };
const snapshot = (id = "shell", status = "running"): TerminalSnapshot => ({
  botId: bot.id, id, kind: "default", status, shell: "PowerShell",
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
    localStorage.setItem("echo2.terminalDock.v1", JSON.stringify({ bot: { open: true } }));
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
    dock.mountTerminalDock(document.getElementById("dock"), bot);
  });
  afterEach(() => {
    dock.updateTerminalDock(document.createElement("div"), [], []); dock.detachTerminalDock();
    vi.clearAllTimers();
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  async function connect() {
    mocks.handlers.get("terminal_subscribed")!({ botId: bot.id });
    await vi.advanceTimersByTimeAsync(200);
  }

  it("drops offline and already queued input without replay on reconnect", async () => {
    await connect();
    const pending = deferred<void>();
    mocks.write.mockReturnValueOnce(pending.promise).mockResolvedValue(undefined);
    mocks.input!("first"); await vi.advanceTimersByTimeAsync(20);
    mocks.input!("queued"); await vi.advanceTimersByTimeAsync(20);
    mocks.handlers.get("terminal_offline")!({ botId: bot.id });
    mocks.input!("offline");
    pending.resolve(); await vi.advanceTimersByTimeAsync(20);
    await connect();
    expect(mocks.write.mock.calls.map(call => call[2])).toEqual(["first"]);
    mocks.input!("fresh"); await vi.advanceTimersByTimeAsync(20);
    expect(mocks.write.mock.calls.map(call => call[2])).toEqual(["first", "fresh"]);
  });

  it("does not restart a shell when synchronization fails with a connection error", async () => {
    await connect(); mocks.start.mockClear();
    mocks.sync.mockRejectedValueOnce(new Error("offline"));
    await connect();
    expect(mocks.start).not.toHaveBeenCalled();
  });

  it("repeats synchronization when a new gap arrives during a snapshot", async () => {
    await connect();
    const pending = deferred<TerminalSnapshot>();
    mocks.sync.mockReturnValueOnce(pending.promise).mockResolvedValue({...snapshot(),lastSequence:3});
    mocks.handlers.get("terminal_event")!({botId:bot.id,sessionId:"shell",event:"data",sequence:2,data:""});
    await vi.advanceTimersByTimeAsync(0);
    mocks.handlers.get("terminal_event")!({botId:bot.id,sessionId:"shell",event:"data",sequence:3,data:""});
    pending.resolve({...snapshot(),lastSequence:1});
    await vi.advanceTimersByTimeAsync(200);
    expect(mocks.sync).toHaveBeenCalledTimes(2);
  });

  it("ignores an old session snapshot that finishes after restart", async () => {
    await connect();
    const pending = deferred<TerminalSnapshot>();
    mocks.sync.mockReturnValueOnce(pending.promise);
    mocks.handlers.get("terminal_subscribed")!({botId:bot.id});
    await vi.advanceTimersByTimeAsync(0);
    document.querySelector<HTMLButtonElement>('[data-terminal-action="restart"]')!.click();
    await vi.advanceTimersByTimeAsync(200);
    pending.resolve({...snapshot(),shell:"Stale shell"});
    await vi.advanceTimersByTimeAsync(200);
    expect(document.querySelector('.terminal-session-label')?.textContent).not.toBe('Stale shell');
    mocks.write.mockResolvedValue(undefined);
    mocks.input!("fresh"); await vi.advanceTimersByTimeAsync(20);
    expect(mocks.write).toHaveBeenCalledWith(bot.id,"replacement","fresh");
  });

  it("fits before starting and sends a size changed while start was pending", async () => {
    const start = deferred<TerminalSnapshot>();
    mocks.start.mockReturnValue(start.promise);
    await connect();
    expect(mocks.start).toHaveBeenCalledWith(bot.id, 120, 30);
    resizePanel(160, 40);
    await vi.advanceTimersByTimeAsync(200);
    expect(mocks.resize).not.toHaveBeenCalled();
    start.resolve(snapshot());
    await vi.advanceTimersByTimeAsync(200);
    expect(mocks.resize).toHaveBeenCalledExactlyOnceWith(bot.id, "shell", 160, 40);
  });

  it("resends unchanged dimensions on reconnect and when attaching an existing shell", async () => {
    await connect();
    expect(mocks.resize).toHaveBeenCalledExactlyOnceWith(bot.id, "shell", 120, 30);
    mocks.resize.mockClear();
    await connect();
    expect(mocks.sync).toHaveBeenCalledWith(bot.id, "shell", 0);
    expect(mocks.resize).toHaveBeenCalledExactlyOnceWith(bot.id, "shell", 120, 30);
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
      [bot.id, "shell", 130, 30], [bot.id, "shell", 150, 30],
    ]);
  });

  it("does not send stale dimensions to a replacement session", async () => {
    await connect();
    mocks.resize.mockClear();
    resizePanel(150);
    await vi.advanceTimersByTimeAsync(20);
    document.querySelector<HTMLButtonElement>('[data-terminal-action="restart"]')!.click();
    await vi.advanceTimersByTimeAsync(200);
    expect(mocks.resize).toHaveBeenCalledExactlyOnceWith(bot.id, "replacement", 150, 30);
  });

  it("does not resize an exited terminal or a detached panel", async () => {
    await connect();
    mocks.resize.mockClear();
    resizePanel(150);
    await vi.advanceTimersByTimeAsync(20);
    dock.updateTerminalDock(document.createElement("div"), [], []); dock.detachTerminalDock();
    await vi.advanceTimersByTimeAsync(200);
    expect(mocks.resize).not.toHaveBeenCalled();
    mocks.handlers.get("terminal_event")!({ botId: bot.id, sessionId: "shell", event: "exited" });
    dock.mountTerminalDock(document.getElementById("dock"), bot);
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
