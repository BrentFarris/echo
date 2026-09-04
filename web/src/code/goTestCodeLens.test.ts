import { beforeEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({
  api: vi.fn(),
  provider: null as any,
  commands: new Map<string, (...args: any[]) => void>(),
  adopt: vi.fn(),
  disposeCoverage: vi.fn(),
}));

vi.mock("../../js/api.js", () => ({ api: mocks.api }));
vi.mock("./goCoverage", () => ({
  GoCoverageController: class { dispose = mocks.disposeCoverage; },
}));
vi.mock("./language", () => ({
  monaco: {
    Range: class Range {
      constructor(
        public startLineNumber: number, public startColumn: number,
        public endLineNumber: number, public endColumn: number,
      ) {}
    },
    languages: {
      registerCodeLensProvider: vi.fn((_language: string, provider: any) => {
        mocks.provider = provider;
        return { dispose() {} };
      }),
    },
    editor: {
      registerCommand: vi.fn((id: string, command: (...args: any[]) => void) => {
        mocks.commands.set(id, command);
        return { dispose() {} };
      }),
    },
  },
}));

import { registerGoTestCodeLens } from "./goTestCodeLens";

describe("Go test CodeLens", () => {
  beforeEach(() => {
    mocks.api.mockReset();
    mocks.adopt.mockReset();
    mocks.disposeCoverage.mockReset();
    mocks.commands.clear();
    mocks.provider = null;
  });

  it("maps server lenses to clickable Monaco commands", async () => {
    mocks.api.mockResolvedValue({ lenses: [{
      range: { start: { line: 4, character: 0 }, end: { line: 4, character: 0 } },
      title: "run test", action: "run", target: { kind: "test", name: "TestOne", path: ["TestOne"] },
    }] });
    const disposable = registerGoTestCodeLens({
      workspaceId: "workspace", refForModel: () => ({ rootId: "root", path: "sample_test.go" }),
      saveAll: vi.fn(async () => true), acceptTestSnapshot: mocks.adopt,
      acceptDebugSnapshot: vi.fn(), openDebugSettings: vi.fn(), message: vi.fn(),
    });
    const result = await mocks.provider.provideCodeLenses({
      uri: { toString: () => "file:///workspace/sample_test.go" }, getVersionId: () => 1,
      getValue: () => "package sample",
    }, { isCancellationRequested: false });

    expect(mocks.api).toHaveBeenCalledWith("/api/workspaces/workspace/testing/go/lenses", {
      method: "POST", body: { ref: { rootId: "root", path: "sample_test.go" }, text: "package sample" },
    });
    expect(result.lenses[0]).toMatchObject({
      range: { startLineNumber: 5, startColumn: 1 },
      command: { id: "echo.goTest.run", title: "run test" },
    });
    disposable.dispose();
    expect(mocks.disposeCoverage).toHaveBeenCalledOnce();
  });

  it("saves before running and adopts the shared output session", async () => {
    const saveAll = vi.fn(async () => true);
    const session = { id: "test-session", workspaceId: "workspace", shell: "go", workingDirectory: "/workspace", status: "running", lastSequence: 0, output: [] };
    mocks.api.mockResolvedValue({ session });
    registerGoTestCodeLens({
      workspaceId: "workspace", refForModel: () => null, saveAll,
      acceptTestSnapshot: mocks.adopt, acceptDebugSnapshot: vi.fn(), openDebugSettings: vi.fn(), message: vi.fn(),
    });
    const ref = { rootId: "root", path: "sample_test.go" };
    const target = { kind: "test", name: "TestOne", path: ["TestOne"] };

    mocks.commands.get("echo.goTest.run")?.({}, ref, target);
    await vi.waitFor(() => expect(mocks.adopt).toHaveBeenCalledWith(session));

    expect(saveAll).toHaveBeenCalledOnce();
    expect(mocks.api).toHaveBeenCalledWith("/api/workspaces/workspace/testing/go/runs", { method: "POST", body: { ref, target } });
  });
});
