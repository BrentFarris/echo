import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { DebugPersistentState, DebugSession } from "./types";
import type { DebugViewOptions } from "./debugView";

const mocks = vi.hoisted(() => ({ request: vi.fn(), save: vi.fn(), prompt: vi.fn() }));
vi.mock("../../js/ws.js", () => ({ on: () => () => {}, onState: () => () => {}, send: vi.fn() }));
vi.mock("../code/language", () => ({ monaco: { languages: { getLanguages: () => [] } } }));
vi.mock("../code/ui", () => ({
  escapeHTML: (value: string) => value, promptDialog: mocks.prompt, toast: vi.fn(),
}));
vi.mock("../terminal", () => ({ registerWorkbenchPanel: () => () => {}, openWorkbenchPanel: vi.fn() }));
vi.mock("./api", () => ({ dapRequest: mocks.request, saveDebugState: mocks.save }));

import { DebugView } from "./debugView";

let view: DebugView;
let host: HTMLElement;
let controller: AbortController;
const session: DebugSession = {
  id: "session", workspaceId: "workspace", configuration: "Main", adapterProfileId: "fake",
  request: "launch", status: "stopped", revision: 2, stopGeneration: 1,
  startedAt: "2026-01-01T00:00:00Z",
};

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  document.body.innerHTML = "<aside></aside><header></header>";
  host = document.querySelector("aside")!;
  controller = new AbortController();
  const disposable = () => ({ dispose() {} });
  view = new DebugView({
    workspaceId: "workspace", host, toolbarHost: document.querySelector("header")!, signal: controller.signal,
    editor: {
      onMouseDown: disposable, onContextMenu: disposable, onDidChangeModel: disposable,
      onDidChangeModelContent: disposable, getModel: () => null, deltaDecorations: () => [],
    } as unknown as DebugViewOptions["editor"],
    activeFile: () => null, selectedText: () => "", saveAll: async () => true,
    showSidebar() {}, openSource: async () => {}, openVirtualSource: async () => {},
  });
  view.acceptExternalSnapshot({ workspaceId: "workspace", sequence: 1, sessions: [session], groups: [], state: { revision: 0 } });
  mocks.prompt.mockResolvedValue("x");
  mocks.save.mockImplementation(async (_workspace: string, revision: number, state: DebugPersistentState) => ({ ...state, revision: revision + 1 }));
});

afterEach(() => {
  controller.abort();
  view.dispose();
  document.body.innerHTML = "";
});

it.each([false, true])("renders a newly added watch after evaluation without another debugger event (error=%s)", async (fails) => {
  let resolve!: (value: unknown) => void;
  let reject!: (reason: Error) => void;
  mocks.request.mockImplementation(() => new Promise((done, fail) => { resolve = done; reject = fail; }));
  host.querySelector<HTMLButtonElement>("[data-debug-action=add-watch]")!.click();
  await vi.waitFor(() => expect(mocks.request).toHaveBeenCalledWith("workspace", "session", "evaluate", 2, 1, {
    expression: "x", frameId: undefined, context: "watch",
  }));
  expect(host.querySelector(".debug-watch-row")?.textContent).toBe("x");
  if (fails) reject(new Error("Unknown expression"));
  else resolve({ body: { result: "42", type: "int", variablesReference: 0 } });
  await vi.waitFor(() => expect(host.querySelector(".debug-watch-row")?.textContent).toContain(fails ? "Unknown expression" : "42"));
});
