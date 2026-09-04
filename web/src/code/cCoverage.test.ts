import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  handlers: new Map<string, Array<(value: object) => void>>(),
  models: [] as any[],
  created: [] as Array<(model: any) => void>,
}));

vi.mock("../../js/api.js", () => ({ api: mocks.api }));
vi.mock("../../js/ws.js", () => ({
  on: (name: string, handler: (value: object) => void) => {
    const handlers = mocks.handlers.get(name) || [];
    handlers.push(handler);
    mocks.handlers.set(name, handlers);
    return () => {};
  },
  onState: () => () => {},
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
      onDidCreateModel: (handler: (model: any) => void) => { mocks.created.push(handler); return { dispose() {} }; },
      onWillDisposeModel: () => ({ dispose() {} }),
    },
  },
}));

import { CCoverageController } from "./cCoverage";

function model(path: string) {
  let sequence = 0;
  return {
    path,
    isDisposed: () => false,
    getLineCount: () => 8,
    getLineMaxColumn: (line: number) => line + 10,
    deltaDecorations: vi.fn((_old: string[], next: unknown[]) => next.map(() => `decoration-${++sequence}`)),
  };
}

function emit(name: string, value: object): void {
  for (const handler of mocks.handlers.get(name) || []) handler(value);
}

const coverage = {
  revision: 4, sessionId: "session", targetId: "unit", provider: "gcov" as const,
  files: [{ ref: { rootId: "root", path: "src/logic.c" }, lines: [
    { line: 1, executionCount: 3, state: "covered" as const },
    { line: 2, executionCount: 1, state: "partial" as const },
    { line: 3, executionCount: 0, state: "uncovered" as const },
  ] }],
};

describe("C coverage controller", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.handlers.clear();
    mocks.models = [];
    mocks.created = [];
  });

  it("restores all three whole-line states and decorates later models", async () => {
    const source = model("src/logic.c");
    mocks.models = [source];
    mocks.api.mockResolvedValue({ revision: 4, coverage });
    const controller = new CCoverageController({
      workspaceId: "workspace", refForModel: (candidate) => ({ rootId: "root", path: (candidate as any).path }), message: vi.fn(),
    });
    await vi.waitFor(() => expect(source.deltaDecorations).toHaveBeenCalled());
    const decorations = source.deltaDecorations.mock.calls.at(-1)?.[1] as any[];
    expect(decorations.map((item) => item.options.className)).toEqual([
      "c-coverage-covered", "c-coverage-partial", "c-coverage-uncovered",
    ]);
    expect(decorations[0]).toMatchObject({
      range: { startLineNumber: 2, startColumn: 1, endLineNumber: 2, endColumn: 12 },
      options: { isWholeLine: true, hoverMessage: { value: "Covered · executed 3 times" } },
    });
		const laterSource = model("src/logic.c");
		for (const handler of mocks.created) handler(laterSource);
		expect(laterSource.deltaDecorations.mock.calls.at(-1)?.[1]).toHaveLength(3);
    const header = model("src/logic.h");
    for (const handler of mocks.created) handler(header);
    expect(header.deltaDecorations.mock.calls.at(-1)?.[1]).toEqual([]);
    controller.dispose();
  });

  it("clears current coverage and rejects stale ready events", async () => {
    const source = model("src/logic.c");
    mocks.models = [source];
    mocks.api.mockResolvedValue({ revision: 4, coverage });
    const controller = new CCoverageController({
      workspaceId: "workspace", refForModel: (candidate) => ({ rootId: "root", path: (candidate as any).path }), message: vi.fn(),
    });
    await vi.waitFor(() => expect(source.deltaDecorations).toHaveBeenCalled());
    emit("c_test_coverage", { type: "c_test_coverage", workspaceId: "workspace", revision: 5, state: "cleared" });
    const calls = source.deltaDecorations.mock.calls.length;
    emit("c_test_coverage", { type: "c_test_coverage", workspaceId: "workspace", revision: 4, state: "ready", coverage });
    expect(source.deltaDecorations).toHaveBeenCalledTimes(calls);
    controller.dispose();
  });
});
