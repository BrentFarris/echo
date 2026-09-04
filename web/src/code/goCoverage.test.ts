import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  handlers: new Map<string, Array<(value: object) => void>>(),
  stateHandlers: [] as Array<(value: string) => void>,
  models: [] as any[],
  created: [] as Array<(model: any) => void>,
  disposing: [] as Array<(model: any) => void>,
}));

vi.mock("../../js/api.js", () => ({ api: mocks.api }));
vi.mock("../../js/ws.js", () => ({
  on: (name: string, handler: (value: object) => void) => {
    const handlers = mocks.handlers.get(name) || [];
    handlers.push(handler);
    mocks.handlers.set(name, handlers);
    return () => mocks.handlers.set(name, (mocks.handlers.get(name) || []).filter((candidate) => candidate !== handler));
  },
  onState: (handler: (value: string) => void) => {
    mocks.stateHandlers.push(handler);
    return () => { mocks.stateHandlers = mocks.stateHandlers.filter((candidate) => candidate !== handler); };
  },
}));
vi.mock("./language", () => ({
  monaco: {
    Range: class Range {
      constructor(
        public startLineNumber: number, public startColumn: number,
        public endLineNumber: number, public endColumn: number,
      ) {}
    },
    editor: {
      TrackedRangeStickiness: { NeverGrowsWhenTypingAtEdges: 1 },
      getModels: () => mocks.models,
      onDidCreateModel: (handler: (model: any) => void) => {
        mocks.created.push(handler);
        return { dispose: () => { mocks.created = mocks.created.filter((candidate) => candidate !== handler); } };
      },
      onWillDisposeModel: (handler: (model: any) => void) => {
        mocks.disposing.push(handler);
        return { dispose: () => { mocks.disposing = mocks.disposing.filter((candidate) => candidate !== handler); } };
      },
    },
  },
}));

import { GoCoverageController } from "./goCoverage";

function model(path: string) {
  let sequence = 0;
  return {
    path,
    isDisposed: () => false,
    deltaDecorations: vi.fn((_old: string[], next: unknown[]) => next.map(() => `decoration-${++sequence}`)),
  };
}

function emit(name: string, value: object): void {
  for (const handler of mocks.handlers.get(name) || []) handler(value);
}

const coverage = {
  revision: 2,
  sessionId: "session",
  package: { rootId: "root", path: "pkg" },
  mode: "set" as const,
  files: [{
    ref: { rootId: "root", path: "pkg/logic.go" },
    ranges: [
      { start: { line: 1, character: 0 }, end: { line: 1, character: 8 }, statements: 1, count: 1 },
      { start: { line: 2, character: 2 }, end: { line: 3, character: 1 }, statements: 2, count: 0 },
    ],
  }],
};

describe("Go coverage controller", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.handlers.clear();
    mocks.stateHandlers = [];
    mocks.models = [];
    mocks.created = [];
    mocks.disposing = [];
  });

  it("restores covered and uncovered statement ranges", async () => {
    const source = model("pkg/logic.go");
    mocks.models = [source];
    mocks.api.mockResolvedValue({ revision: 2, coverage });
    const controller = new GoCoverageController({
      workspaceId: "workspace",
      refForModel: (candidate) => ({ rootId: "root", path: (candidate as any).path }),
      message: vi.fn(),
    });
    await vi.waitFor(() => expect(source.deltaDecorations).toHaveBeenCalled());
    const decorations = source.deltaDecorations.mock.calls.at(-1)?.[1] as any[];
    expect(decorations).toHaveLength(2);
    expect(decorations[0]).toMatchObject({
      range: { startLineNumber: 2, startColumn: 1, endLineNumber: 2, endColumn: 9 },
      options: { inlineClassName: "go-coverage-covered", hoverMessage: { value: "Covered" } },
    });
    expect(decorations[1].options.inlineClassName).toBe("go-coverage-uncovered");
    controller.dispose();
    expect(source.deltaDecorations.mock.calls.at(-1)?.[1]).toEqual([]);
  });

  it("decorates files opened later but never test files", async () => {
    mocks.api.mockResolvedValue({ revision: 2, coverage });
    const controller = new GoCoverageController({
      workspaceId: "workspace",
      refForModel: (candidate) => ({ rootId: "root", path: (candidate as any).path }),
      message: vi.fn(),
    });
    await vi.waitFor(() => expect(mocks.api).toHaveBeenCalled());
    const source = model("pkg/logic.go");
    const test = model("pkg/logic_test.go");
    for (const handler of mocks.created) handler(source);
    for (const handler of mocks.created) handler(test);
    expect(source.deltaDecorations.mock.calls.at(-1)?.[1]).toHaveLength(2);
    expect(test.deltaDecorations.mock.calls.at(-1)?.[1]).toEqual([]);
    controller.dispose();
  });

  it("clears coverage and ignores older events", async () => {
    const source = model("pkg/logic.go");
    mocks.models = [source];
    mocks.api.mockResolvedValue({ revision: 2, coverage });
    const message = vi.fn();
    const controller = new GoCoverageController({
      workspaceId: "workspace",
      refForModel: (candidate) => ({ rootId: "root", path: (candidate as any).path }),
      message,
    });
    await vi.waitFor(() => expect(source.deltaDecorations).toHaveBeenCalled());
    emit("go_test_coverage", { type: "go_test_coverage", workspaceId: "workspace", revision: 3, state: "cleared" });
    expect(source.deltaDecorations.mock.calls.at(-1)?.[1]).toEqual([]);
    const callCount = source.deltaDecorations.mock.calls.length;
    emit("go_test_coverage", { type: "go_test_coverage", workspaceId: "workspace", revision: 2, state: "ready", coverage });
    expect(source.deltaDecorations).toHaveBeenCalledTimes(callCount);
    emit("go_test_coverage", { type: "go_test_coverage", workspaceId: "workspace", revision: 4, state: "error", message: "profile failed" });
    expect(message).toHaveBeenCalledWith("profile failed", true);
    controller.dispose();
  });
});
