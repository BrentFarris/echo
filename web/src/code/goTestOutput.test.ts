import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  handlers: new Map<string, Array<(value: object) => void>>(),
  stateHandlers: [] as Array<(value: string) => void>,
  panel: null as any,
  openPanel: vi.fn(),
  listSessions: vi.fn(),
  syncTerminal: vi.fn(),
  stopTerminal: vi.fn(),
  toast: vi.fn(),
}));

vi.mock("../../js/api.js", () => ({ api: mocks.api }));
vi.mock("../../js/ws.js", () => ({
  on: (name: string, handler: (value: object) => void) => {
    const values = mocks.handlers.get(name) || [];
    values.push(handler);
    mocks.handlers.set(name, values);
    return () => mocks.handlers.set(name, (mocks.handlers.get(name) || []).filter((candidate) => candidate !== handler));
  },
  onState: (handler: (value: string) => void) => {
    mocks.stateHandlers.push(handler);
    return () => { mocks.stateHandlers = mocks.stateHandlers.filter((candidate) => candidate !== handler); };
  },
  send: vi.fn(),
}));
vi.mock("../terminal", () => ({
  registerWorkbenchPanel: vi.fn((_workspaceId: string, panel: any) => {
    mocks.panel = panel;
    return vi.fn();
  }),
  openWorkbenchPanel: mocks.openPanel,
  subscribeTerminalWorkspace: vi.fn(async () => {}),
}));
vi.mock("../terminal/terminalApi", () => ({
  listTerminalSessions: mocks.listSessions,
  syncTerminal: mocks.syncTerminal,
  stopTerminal: mocks.stopTerminal,
}));
vi.mock("./ui", async (importOriginal) => {
  const original = await importOriginal<Record<string, unknown>>();
  return { ...original, toast: mocks.toast };
});

import { GoTestOutput } from "./goTestOutput";

function emit(name: string, value: object): void {
  for (const handler of mocks.handlers.get(name) || []) handler(value);
}

function encoded(value: string): string {
  return btoa(value);
}

describe("Go test output panel", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.handlers.clear();
    mocks.stateHandlers = [];
    mocks.panel = null;
    mocks.openPanel.mockReset();
    mocks.listSessions.mockReset().mockResolvedValue([]);
    mocks.syncTerminal.mockReset();
    mocks.stopTerminal.mockReset().mockResolvedValue(undefined);
    mocks.toast.mockReset();
    document.body.replaceChildren();
  });

  afterEach(() => document.body.replaceChildren());

  it("streams status and output and supports clear, stop, and rerun", async () => {
    const beforeRun = vi.fn(async () => true);
    const output = new GoTestOutput("workspace", beforeRun);
    const host = document.createElement("div");
    document.body.append(host);
    mocks.panel.mount(host);
	await vi.waitFor(() => expect(mocks.listSessions).toHaveBeenCalledWith("workspace"));

    emit("terminal_event", {
      type: "terminal_event", workspaceId: "workspace", sessionId: "first", kind: "test", event: "started", taskStatus: "running",
    });
    emit("terminal_event", {
      type: "terminal_event", workspaceId: "workspace", sessionId: "first", kind: "test", event: "data", sequence: 1, data: encoded("PASS output\r\n"),
    });
    expect(host.textContent).toContain("Running");
    expect(host.textContent).toContain("PASS output");

    host.querySelector<HTMLButtonElement>("[data-test-output-action=clear]")?.click();
    expect(host.textContent).not.toContain("PASS output");

    host.querySelector<HTMLButtonElement>("[data-test-output-action=stop]")?.click();
    await vi.waitFor(() => expect(mocks.stopTerminal).toHaveBeenCalledWith("workspace", "first"));
    expect(host.textContent).toContain("Stopped");

    emit("terminal_event", {
      type: "terminal_event", workspaceId: "workspace", sessionId: "first", kind: "test", event: "exited", taskStatus: "failed", exitCode: 1,
    });
    mocks.api.mockResolvedValue({
      session: { id: "second", workspaceId: "workspace", kind: "test", shell: "go", workingDirectory: "/workspace", status: "running", taskStatus: "running", lastSequence: 0, output: [] },
    });
    host.querySelector<HTMLButtonElement>("[data-test-output-action=rerun]")?.click();
    await vi.waitFor(() => expect(mocks.api).toHaveBeenCalledWith(
      "/api/workspaces/workspace/testing/go/runs/first/rerun", { method: "POST", body: {} },
    ));
    expect(beforeRun).toHaveBeenCalledOnce();
    expect(host.textContent).toContain("Running");
    output.dispose();
  });
});
