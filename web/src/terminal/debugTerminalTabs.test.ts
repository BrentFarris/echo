import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { TerminalEvent, TerminalSnapshot } from "./terminalApi";

const mocks = vi.hoisted(() => ({
  handlers: new Map<string, (message: object) => void>(),
  list: vi.fn(), sync: vi.fn(), start: vi.fn(), dispose: vi.fn(), write: vi.fn(),
}));
vi.mock("../../js/ws.js", () => ({
  on: (type: string, handler: (message: object) => void) => mocks.handlers.set(type, handler),
  onState: vi.fn(), send: vi.fn(),
}));
vi.mock("../code/ui", () => ({ toast: vi.fn() }));
vi.mock("./terminalApi", () => ({
  listTerminalSessions: mocks.list, syncTerminal: mocks.sync, startTerminal: mocks.start,
  listSavedCommands: vi.fn(async () => []), resizeTerminal: vi.fn(async () => {}),
  writeTerminal: vi.fn(), stopTerminal: vi.fn(), restartTerminal: vi.fn(),
  createSavedCommand: vi.fn(), deleteSavedCommand: vi.fn(), updateSavedCommand: vi.fn(),
}));
vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    options = {}; cols = 80; rows = 24; element?: HTMLElement;
    loadAddon() {} onData() {} onResize() {} reset() {} focus() {}
    open(host: HTMLElement) { this.element = host; }
    write = mocks.write; writeln = mocks.write; dispose = mocks.dispose;
  },
}));
vi.mock("@xterm/addon-fit", () => ({ FitAddon: class { fit() {} } }));
vi.mock("@xterm/addon-web-links", () => ({ WebLinksAddon: class {} }));

const workspace = { id: "workspace", name: "Project", mainPath: "/project" };
const snapshot = (id: string, status = "running", name = "Kaiju"): TerminalSnapshot => ({
  workspaceId: workspace.id, id, name, kind: "debug", status, shell: "app",
  workingDirectory: "/project", lastSequence: 0, output: [],
});
function emit(id: string, event: TerminalEvent["event"], name = "Kaiju", workspaceId = workspace.id) {
  mocks.handlers.get("terminal_event")!({ workspaceId, sessionId: id, kind: "debug", event, name, exitCode: 0 });
}
const tabIDs = () => [...document.querySelectorAll<HTMLElement>('[role="tab"]')].map((tab) => tab.dataset.panelId);
const closeButton = (id: string) => document.querySelector<HTMLButtonElement>(`[data-terminal-action="close-panel"][data-panel-id="debug-terminal-${id}"]`);

describe("debug terminal tabs", () => {
  let dock: typeof import("./index");
  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    mocks.handlers.clear();
    sessionStorage.clear();
    localStorage.clear();
    vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
    mocks.list.mockResolvedValue([]);
    mocks.sync.mockImplementation(async (_workspace: string, id: string) => snapshot(id));
    mocks.start.mockResolvedValue({ ...snapshot("shell"), kind: "default" });
    dock = await import("./index");
    document.body.innerHTML = '<div id="dock"></div>';
    dock.mountTerminalDock(document.getElementById("dock"), workspace);
    mocks.handlers.get("terminal_subscribed")!({ workspaceId: workspace.id });
    await vi.waitFor(() => expect(mocks.list).toHaveBeenCalled());
  });
  afterEach(() => {
    dock.detachTerminalDock();
    vi.unstubAllGlobals();
  });

  it("keeps the latest output and replaces finished runs without removing active or unrelated tabs", async () => {
    emit("first", "started");
    await vi.waitFor(() => expect(mocks.sync).toHaveBeenCalled());
    expect(closeButton("first")).toBeNull();
    emit("first", "exited");
    expect(closeButton("first")).not.toBeNull();
    emit("other", "started", "Other launch");
    await vi.waitFor(() => expect(mocks.sync).toHaveBeenCalledWith(workspace.id, "other", 0));
    emit("other", "exited", "Other launch");
    emit("parallel", "started");
    emit("next", "started");
    expect(tabIDs()).toEqual(["terminal", "debug-terminal-other", "debug-terminal-parallel", "debug-terminal-next"]);
    expect(mocks.dispose).toHaveBeenCalledTimes(1);
    expect(mocks.start).not.toHaveBeenCalled();
    expect(document.querySelector('[aria-selected="true"]')?.getAttribute("data-panel-id")).toBe("debug-terminal-next");
  });

  it("closes a completed tab and ignores late events and reconnect snapshots", async () => {
    emit("finished", "started");
    await vi.waitFor(() => expect(mocks.sync).toHaveBeenCalled());
    emit("finished", "exited");
    closeButton("finished")!.click();
    expect(tabIDs()).toEqual(["terminal"]);
    expect(mocks.dispose).toHaveBeenCalledTimes(1);
    emit("finished", "exited");
    mocks.list.mockResolvedValue([snapshot("finished", "exited")]);
    mocks.handlers.get("terminal_subscribed")!({ workspaceId: workspace.id });
    await vi.waitFor(() => expect(mocks.list).toHaveBeenCalledTimes(2));
    expect(tabIDs()).toEqual(["terminal"]);
  });

  it("does not resurrect disposed output when an in-flight sync completes", async () => {
    let resolve!: (value: TerminalSnapshot) => void;
    mocks.sync.mockImplementationOnce(() => new Promise<TerminalSnapshot>((done) => { resolve = done; }));
    emit("pending", "started");
    await vi.waitFor(() => expect(resolve).toBeDefined());
    emit("pending", "exited");
    closeButton("pending")!.click();
    mocks.write.mockClear();
    resolve({ ...snapshot("pending"), output: [{ sequence: 1, data: btoa("late output") }] });
    await new Promise((done) => setTimeout(done, 0));
    expect(tabIDs()).toEqual(["terminal"]);
    expect(mocks.write).not.toHaveBeenCalled();
  });

  it("restores old finished tabs with close controls and respects dismissals after reload", async () => {
    sessionStorage.setItem("echo.dismissedTerminal:workspace:debug:hidden", "true");
    mocks.list.mockResolvedValue([snapshot("old", "exited"), snapshot("hidden", "exited")]);
    mocks.handlers.get("terminal_subscribed")!({ workspaceId: workspace.id });
    await vi.waitFor(() => expect(closeButton("old")).not.toBeNull());
    expect(tabIDs()).toEqual(["terminal", "debug-terminal-old"]);
    closeButton("old")!.click();
    expect(tabIDs()).toEqual(["terminal"]);
  });

  it("keeps a fast-exiting run closable when an older running snapshot arrives", async () => {
    let resolve!: (value: TerminalSnapshot) => void;
    mocks.sync.mockImplementationOnce(() => new Promise<TerminalSnapshot>((done) => { resolve = done; }));
    emit("fast", "started");
    await vi.waitFor(() => expect(resolve).toBeDefined());
    emit("fast", "exited");
    resolve(snapshot("fast"));
    await new Promise((done) => setTimeout(done, 0));
    expect(closeButton("fast")).not.toBeNull();
    mocks.sync.mockRejectedValue(new Error("Session no longer exists"));
    mocks.handlers.get("terminal_subscribed")!({ workspaceId: workspace.id });
    await new Promise((done) => setTimeout(done, 0));
    emit("replacement", "started");
    expect(tabIDs()).not.toContain("debug-terminal-fast");
  });

  it("does not clean up finished terminals belonging to another workspace", async () => {
    emit("retained", "exited");
    emit("new", "started", "Kaiju", "another-workspace");
    expect(tabIDs()).toContain("debug-terminal-retained");
    expect(mocks.dispose).not.toHaveBeenCalled();
  });
});
