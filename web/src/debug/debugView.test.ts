import { afterEach, beforeEach, expect, it, vi } from "vitest";
import type { DAPStackFrame, DebugPersistentState, DebugSession, DebugSource, DebugEvent } from "./types";
import type { DebugViewOptions } from "./debugView";

const mocks = vi.hoisted(() => ({ request: vi.fn(), save: vi.fn(), prompt: vi.fn(), openSource: vi.fn(), openVirtualSource: vi.fn() }));
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
const internals = () => view as unknown as {
  inspectStopped(session: DebugSession): Promise<void>;
  selectFrame(session: DebugSession, threadId: number, frame: DAPStackFrame, generation: number): Promise<void>;
  selectFrameByID(sessionId: string, threadId: number, frameId: number): Promise<void>;
  openDebugSource(source: DebugSource, line?: number, column?: number): Promise<void>;
  invalidateInspection(): void;
  receiveEvent(event: DebugEvent): void;
  frameSelection: { frame: DAPStackFrame; threadId: number } | null;
  frames: Map<string, DAPStackFrame[]>;
  savedFrame: { sessionId: string; threadId: number; frameId: number; stopGeneration: number };
};
const session: DebugSession = {
  id: "session", workspaceId: "workspace", configuration: "Main", adapterProfileId: "fake",
  request: "launch", status: "stopped", revision: 2, stopGeneration: 1,
  startedAt: "2026-01-01T00:00:00Z",
};

beforeEach(() => {
  vi.resetAllMocks();
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
    showSidebar() {}, openSource: mocks.openSource, openVirtualSource: mocks.openVirtualSource,
  });
  view.acceptExternalSnapshot({ workspaceId: "workspace", sequence: 1, sessions: [session], groups: [], state: { revision: 0 } });
  mocks.prompt.mockResolvedValue("x");
  mocks.save.mockImplementation(async (_workspace: string, revision: number, state: DebugPersistentState) => ({ ...state, revision: revision + 1 }));
});

it("steps the selected thread and retains a newer stop when the control ACK arrives late", async () => {
  let acknowledge!: () => void;
  mocks.request.mockImplementation(async (_workspace, _session, command) => {
    if (command === "stepIn") return new Promise<void>((resolve) => { acknowledge = resolve; });
    if (command === "threads") return { body: { threads: [{ id: 42, name: "Selected" }] } };
    if (command === "stackTrace") return { body: { stackFrames: [{ id: 10, name: "C.native", line: 8 }] } };
    return { body: { scopes: [] } };
  });
  await internals().selectFrame(session, 42, { id: 10, name: "main.main", line: 1, column: 1 }, 0);
  const step = view.control("stepIn");
  expect(mocks.request).toHaveBeenCalledWith("workspace", "session", "stepIn", 2, 1, { threadId: 42 });
  const stopped = { ...session, revision: 4, stopGeneration: 3, threadId: 42 };
  view.acceptExternalSnapshot({ workspaceId: "workspace", sequence: 2, sessions: [stopped], groups: [], state: { revision: 0 } });
  await internals().inspectStopped(stopped);
  acknowledge();
  await step;
  expect(internals().frameSelection?.frame.name).toBe("C.native");
});

it("chooses the new stop's top frame even when Delve reuses a saved caller ID", async () => {
  internals().savedFrame = { sessionId: "session", threadId: 2, frameId: 11, stopGeneration: 0 };
  mocks.request.mockImplementation(async (_workspace, _session, command) => {
    if (command === "threads") return { body: { threads: [{ id: 1 }, { id: 2 }] } };
    if (command === "stackTrace") return { body: { stackFrames: [{ id: 10, name: "C.native", line: 8 }, { id: 11, name: "main.main", line: 1 }] } };
    return { body: { scopes: [] } };
  });
  await internals().inspectStopped({ ...session, threadId: 1 });
  expect(internals().frameSelection?.threadId).toBe(1);
  expect(internals().frameSelection?.frame.id).toBe(10);
});

it("discards scope results from a frame selected earlier in the same stop", async () => {
  let oldScopes!: (value: unknown) => void;
  internals().frames.set("session:1", [{ id: 10, name: "C.native", line: 8, column: 1 }, { id: 11, name: "main.main", line: 1, column: 1 }]);
  mocks.request.mockImplementation(async (_workspace, _session, command, _revision, _stop, args) => {
    if (command === "scopes" && args.frameId === 10) return new Promise((resolve) => { oldScopes = resolve; });
    return { body: { scopes: [{ name: "Go locals", variablesReference: 0 }] } };
  });
  const old = internals().selectFrameByID("session", 1, 10);
  await internals().selectFrameByID("session", 1, 11);
  oldScopes({ body: { scopes: [{ name: "Stale C locals", variablesReference: 0 }] } });
  await old;
  expect(host.textContent).toContain("Go locals");
  expect(host.textContent).not.toContain("Stale C locals");
});

it("opens an adapter source before its echoRef and carries the exact source position", async () => {
  mocks.request.mockResolvedValue({ body: { content: "int native;", mimeType: "text/x-c" } });
  const source = { name: "native.c", echoSourceId: "native-13", sourceReference: 13, echoRef: { rootId: "root", path: "native.c" } };
  await internals().openDebugSource(source, 23, 6);
  expect(mocks.openSource).not.toHaveBeenCalled();
  expect(mocks.openVirtualSource).toHaveBeenCalledWith("native.c", "int native;", "text/x-c", { key: '["session","native-13",13]', line: 23, column: 6 });
});

it("does not navigate to a source whose response arrives after resume", async () => {
  let resolveSource!: (value: unknown) => void;
  mocks.request.mockImplementation(() => new Promise((resolve) => { resolveSource = resolve; }));
  const open = internals().openDebugSource({ name: "native.c", echoSourceId: "native-13" }, 23);
  internals().invalidateInspection();
  resolveSource({ body: { content: "stale source" } });
  await open;
  expect(mocks.openVirtualSource).not.toHaveBeenCalled();
});

it("invalidates pending workspace navigation when the selected stop changes", async () => {
  let isCurrent!: () => boolean;
  mocks.openSource.mockImplementation(async (_source, _line, _column, current) => { isCurrent = current; });
  await internals().openDebugSource({ echoRef: { rootId: "root", path: "native.c" } }, 23);
  expect(isCurrent()).toBe(true);
  internals().invalidateInspection();
  expect(isCurrent()).toBe(false);
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
