import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => {
  class Terminal {
    static instances: Terminal[] = [];
    options = {}; cols = 80; rows = 24; element?: HTMLElement;
    textarea?: HTMLTextAreaElement;
    selection = "";
    selectionListeners = new Set<() => void>();
    keyHandler!: (event: KeyboardEvent) => boolean;
    constructor() { Terminal.instances.push(this); }
    loadAddon() {} onData() {} onResize() {} reset() {} write() {} writeln() {} dispose() {}
    attachCustomKeyEventHandler(handler: typeof this.keyHandler) { this.keyHandler = handler; }
    open(host: HTMLElement) {
      this.element = host;
      this.textarea = document.createElement("textarea");
      host.appendChild(this.textarea);
    }
    focus() { this.textarea?.focus(); }
    hasSelection() { return this.selection.length > 0; }
    getSelection() { return this.selection; }
    selectText(text: string) {
      this.selection = text;
      this.selectionListeners.forEach((listener) => listener());
    }
    clearSelection = vi.fn(() => this.selectText(""));
    onSelectionChange(listener: () => void) {
      this.selectionListeners.add(listener);
      return { dispose: () => this.selectionListeners.delete(listener) };
    }
  }
  return {
    Terminal,
    handlers: new Map<string, (message: object) => void>(),
    clipboard: vi.fn(), toast: vi.fn(), execCommand: vi.fn(),
  };
});

vi.mock("@xterm/xterm", () => ({ Terminal: mocks.Terminal }));
vi.mock("@xterm/addon-fit", () => ({ FitAddon: class { fit() {} } }));
vi.mock("@xterm/addon-web-links", () => ({ WebLinksAddon: class {} }));
vi.mock("../../js/ws.js", () => ({
  on: (type: string, handler: (message: object) => void) => mocks.handlers.set(type, handler),
  onState: vi.fn(), send: vi.fn(),
}));
vi.mock("../code/ui", async (original) => ({
  ...await original<typeof import("../code/ui")>(), toast: mocks.toast,
}));
vi.mock("./terminalApi", () => ({
  startTerminal: vi.fn(), syncTerminal: vi.fn(), resizeTerminal: vi.fn(), restartTerminal: vi.fn(),
  listTerminalSessions: vi.fn(async () => []), listSavedCommands: vi.fn(async () => []),
  writeTerminal: vi.fn(), stopTerminal: vi.fn(), createSavedCommand: vi.fn(),
  deleteSavedCommand: vi.fn(), updateSavedCommand: vi.fn(),
}));

function ctrlC(type = "keydown", modifiers: KeyboardEventInit = {}) {
  return new KeyboardEvent(type, { key: "c", code: "KeyC", ctrlKey: true, bubbles: true, cancelable: true, ...modifiers });
}

describe("terminal selection copying", () => {
  const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, "clipboard");
  let dock: typeof import("./index");
  let terminal: InstanceType<typeof mocks.Terminal>;
  beforeEach(async () => {
    vi.resetModules();
    vi.resetAllMocks();
    mocks.Terminal.instances.length = 0;
    mocks.handlers.clear();
    localStorage.clear();
    sessionStorage.clear();
    localStorage.setItem("echo.terminalDock.v1", JSON.stringify({ workspace: { open: true } }));
    vi.stubGlobal("ResizeObserver", class { observe() {} disconnect() {} });
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: mocks.clipboard } });
    mocks.clipboard.mockResolvedValue(undefined);
    Object.defineProperty(document, "execCommand", { configurable: true, value: mocks.execCommand });
    dock = await import("./index");
    document.body.innerHTML = '<div id="dock"></div>';
    dock.mountTerminalDock(document.getElementById("dock"), { id: "workspace", name: "Project", mainPath: "/project" });
    terminal = mocks.Terminal.instances[0];
    terminal.focus();
  });
  afterEach(() => {
    dock.detachTerminalDock();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
    if (clipboardDescriptor) Object.defineProperty(navigator, "clipboard", clipboardDescriptor);
    else Reflect.deleteProperty(navigator, "clipboard");
    Reflect.deleteProperty(document, "execCommand");
    document.body.innerHTML = "";
  });

  it.each(["partial text", "first line\r\nsecond line", "  spaced text  ", "   "])("copies the exact selection %j and clears it after success", async (selection) => {
    let finish!: () => void;
    mocks.clipboard.mockReturnValue(new Promise<void>((resolve) => { finish = resolve; }));
    terminal.selectText(selection);
    const event = ctrlC();
    const stop = vi.spyOn(event, "stopPropagation");
    expect(terminal.keyHandler(event)).toBe(false);
    expect(event.defaultPrevented).toBe(true);
    expect(stop).toHaveBeenCalledOnce();
    expect(mocks.clipboard).toHaveBeenCalledExactlyOnceWith(selection);
    expect(terminal.clearSelection).not.toHaveBeenCalled();
    finish();
    await vi.waitFor(() => expect(terminal.clearSelection).toHaveBeenCalledOnce());
    expect(terminal.hasSelection()).toBe(false);
    expect(terminal.selectionListeners.size).toBe(0);
    expect(document.activeElement).toBe(terminal.textarea);
    expect(terminal.keyHandler(ctrlC())).toBe(true);
  });

  it("passes Ctrl+C through when there is no selection", () => {
    const event = ctrlC();
    expect(terminal.keyHandler(event)).toBe(true);
    expect(event.defaultPrevented).toBe(false);
    expect(mocks.clipboard).not.toHaveBeenCalled();
  });

  it.each([
    ["keyup", {}], ["keypress", {}], ["keydown", { shiftKey: true }],
    ["keydown", { altKey: true }], ["keydown", { metaKey: true }],
    ["keydown", { ctrlKey: false }], ["keydown", { key: "x", code: "KeyX" }],
  ] as [string, KeyboardEventInit][])("passes through %s with %j", (type, modifiers) => {
    terminal.selectText("selected");
    const event = ctrlC(type, modifiers);
    expect(terminal.keyHandler(event)).toBe(true);
    expect(event.defaultPrevented).toBe(false);
    expect(mocks.clipboard).not.toHaveBeenCalled();
    expect(terminal.hasSelection()).toBe(true);
  });

  it.each(["different text", "original"])("preserves a newer selection containing %j while the clipboard is pending", async (newSelection) => {
    let finish!: () => void;
    mocks.clipboard.mockReturnValue(new Promise<void>((resolve) => { finish = resolve; }));
    terminal.selectText("original");
    terminal.keyHandler(ctrlC());
    terminal.selectText(newSelection);
    finish();
    await vi.waitFor(() => expect(terminal.selectionListeners.size).toBe(0));
    expect(terminal.clearSelection).not.toHaveBeenCalled();
    expect(terminal.getSelection()).toBe(newSelection);
  });

  it.each(["unavailable", "denied"])("uses the clipboard fallback and restores focus when the clipboard API is %s", async (mode) => {
    if (mode === "unavailable") Object.defineProperty(navigator, "clipboard", { configurable: true, value: undefined });
    else mocks.clipboard.mockRejectedValue(new Error("denied"));
    mocks.execCommand.mockImplementation(() => {
      const textarea = document.body.lastElementChild as HTMLTextAreaElement;
      expect(textarea.value).toBe("selected");
      textarea.focus();
      return true;
    });
    terminal.selectText("selected");
    expect(terminal.keyHandler(ctrlC())).toBe(false);
    await vi.waitFor(() => expect(terminal.clearSelection).toHaveBeenCalledOnce());
    expect(mocks.execCommand).toHaveBeenCalledExactlyOnceWith("copy");
    expect(document.querySelectorAll("textarea")).toHaveLength(1);
    expect(document.activeElement).toBe(terminal.textarea);
  });

  it.each([false, "throw"])("retains the selection and restores focus when the fallback fails with %j", async (result) => {
    mocks.clipboard.mockRejectedValue(new Error("denied"));
    mocks.execCommand.mockImplementation(() => {
      (document.body.lastElementChild as HTMLTextAreaElement).focus();
      if (result === "throw") throw new Error("copy failed");
      return false;
    });
    terminal.selectText("selected");
    expect(terminal.keyHandler(ctrlC())).toBe(false);
    await vi.waitFor(() => expect(mocks.toast).toHaveBeenCalledOnce());
    expect(mocks.toast).toHaveBeenCalledWith(expect.stringContaining("Could not copy terminal selection:"));
    expect(terminal.clearSelection).not.toHaveBeenCalled();
    expect(terminal.getSelection()).toBe("selected");
    expect(terminal.selectionListeners.size).toBe(0);
    expect(document.querySelectorAll("textarea")).toHaveLength(1);
    expect(document.activeElement).toBe(terminal.textarea);
  });
});
