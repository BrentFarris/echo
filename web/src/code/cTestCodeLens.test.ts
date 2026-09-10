import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  api: vi.fn(), provider: null as any, commands: new Map<string, (...args: any[]) => void>(),
  disposeCoverage: vi.fn(), acceptTest: vi.fn(), acceptDebug: vi.fn(), openDebug: vi.fn(), message: vi.fn(),
}));

vi.mock("../../js/api.js", () => ({ api: mocks.api }));
vi.mock("./cCoverage", () => ({ CCoverageController: class { dispose = mocks.disposeCoverage; } }));
vi.mock("./language", () => ({
  monaco: {
    Range: class Range {
      constructor(
        public startLineNumber: number, public startColumn: number,
        public endLineNumber: number, public endColumn: number,
      ) {}
    },
    languages: { registerCodeLensProvider: vi.fn((_language: string, provider: any) => { mocks.provider = provider; return { dispose() {} }; }) },
    editor: { registerCommand: vi.fn((id: string, command: (...args: any[]) => void) => { mocks.commands.set(id, command); return { dispose() {} }; }) },
  },
}));

import { registerCTestCodeLens } from "./cTestCodeLens";

function options() {
  return {
    workspaceId: "workspace", refForModel: () => ({ rootId: "root", path: "tests/test_main.c" }),
    saveAll: vi.fn(async () => true), acceptTestSnapshot: mocks.acceptTest, acceptDebugSnapshot: mocks.acceptDebug,
    openDebugSettings: mocks.openDebug, message: mocks.message,
  };
}

describe("C test CodeLens", () => {
  beforeEach(() => {
    mocks.api.mockReset(); mocks.commands.clear(); mocks.acceptTest.mockReset(); mocks.acceptDebug.mockReset();
    mocks.openDebug.mockReset(); mocks.message.mockReset(); mocks.provider = null;
  });

  it("anchors named targets and starts a run", async () => {
    mocks.api.mockResolvedValueOnce({ lenses: [{
      range: { start: { line: 3, character: 4 }, end: { line: 3, character: 4 } },
      title: "run C tests: Unit tests", action: "run", targetId: "unit",
    }] });
    const setup = options();
    registerCTestCodeLens(setup);
    const result = await mocks.provider.provideCodeLenses({ getValue: () => "int main(void) {}" }, { isCancellationRequested: false });
    expect(result.lenses[0]).toMatchObject({
      range: { startLineNumber: 4, startColumn: 5 },
      command: { id: "echo.cTest.run", title: "run C tests: Unit tests", arguments: ["unit"] },
    });
    const session = { id: "c-session" };
    mocks.api.mockResolvedValueOnce({ session });
    mocks.commands.get("echo.cTest.run")?.({}, "unit");
    await vi.waitFor(() => expect(mocks.acceptTest).toHaveBeenCalledWith(session));
    expect(mocks.api).toHaveBeenLastCalledWith("/api/workspaces/workspace/testing/c/runs", { method: "POST", body: { targetId: "unit" } });
  });

  it("opens Debug Settings when CodeLLDB is unavailable", async () => {
    mocks.api.mockRejectedValue(new Error("no enabled lldb debug adapter profile is configured"));
    registerCTestCodeLens(options());
    mocks.commands.get("echo.cTest.debug")?.({}, "unit");
    await vi.waitFor(() => expect(mocks.openDebug).toHaveBeenCalledOnce());
  });
});
