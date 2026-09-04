import type * as Monaco from "monaco-editor";
import { api } from "../../js/api.js";
import type { DebugSnapshot } from "../debug/types";
import type { TerminalSnapshot } from "../terminal/terminalApi";
import { monaco } from "./language";
import type { FileRef } from "./types";
import { fromLSPRange } from "./lspClient";
import type { LSPRange } from "./lspTypes";
import { beginGoTestLensComposition } from "./codeLensDedupe";
import { GoCoverageController } from "./goCoverage";

export type GoTestTarget = {
  kind: "package_tests" | "file_tests" | "test" | "example" | "fuzz" | "subtest"
    | "package_benchmarks" | "file_benchmarks" | "benchmark" | "subbenchmark";
  name?: string;
  path?: string[];
};

type GoLens = { range: LSPRange; title: string; action: "run" | "debug"; target: GoTestTarget };

type Options = {
  workspaceId: string;
  refForModel(model: Monaco.editor.ITextModel): FileRef | null;
  saveAll(): Promise<boolean>;
  acceptTestSnapshot(snapshot: TerminalSnapshot): void;
  acceptDebugSnapshot(snapshot: DebugSnapshot): void;
  openDebugSettings(): void;
  message(value: string, sticky?: boolean): void;
};

export function registerGoTestCodeLens(options: Options): Monaco.IDisposable {
  const coverage = new GoCoverageController({
    workspaceId: options.workspaceId, refForModel: options.refForModel, message: options.message,
  });
  const provider = monaco.languages.registerCodeLensProvider("go", {
    async provideCodeLenses(model, token) {
      const ref = options.refForModel(model);
      if (!ref || !ref.path.toLowerCase().endsWith("_test.go") || token.isCancellationRequested) return { lenses: [], dispose() {} };
      const completeComposition = beginGoTestLensComposition(model);
      try {
        const result = await api(`/api/workspaces/${encodeURIComponent(options.workspaceId)}/testing/go/lenses`, {
          method: "POST", body: { ref, text: model.getValue() },
        }) as { lenses?: GoLens[] };
        completeComposition(result.lenses || []);
        if (token.isCancellationRequested) return { lenses: [], dispose() {} };
        const seen = new Set<string>();
        const lenses = (result.lenses || []).flatMap((item) => {
          const key = `${item.range.start.line}:${item.range.start.character}:${item.title}:${JSON.stringify(item.target)}`;
          if (seen.has(key)) return [];
          seen.add(key);
          return [{
            range: fromLSPRange(item.range),
            command: {
              id: item.action === "debug" ? "echo.goTest.debug" : "echo.goTest.run",
              title: item.title,
              arguments: [ref, item.target],
            },
          }];
        });
        return { lenses, dispose() {} };
      } catch (error) {
        completeComposition([]);
        if (!token.isCancellationRequested) options.message(errorMessage(error));
        return { lenses: [], dispose() {} };
      }
    },
  });
  const runCommand = monaco.editor.registerCommand("echo.goTest.run", (_accessor, ref: FileRef, target: GoTestTarget) => {
    void execute("run", ref, target);
  });
  const debugCommand = monaco.editor.registerCommand("echo.goTest.debug", (_accessor, ref: FileRef, target: GoTestTarget) => {
    void execute("debug", ref, target);
  });

  const execute = async (action: "run" | "debug", ref: FileRef, target: GoTestTarget) => {
    if (!(await options.saveAll())) return;
    try {
      const endpoint = action === "debug" ? "debug-sessions" : "runs";
      const result = await api(`/api/workspaces/${encodeURIComponent(options.workspaceId)}/testing/go/${endpoint}`, {
        method: "POST", body: { ref, target },
      }) as { session?: TerminalSnapshot; snapshot?: DebugSnapshot };
      if (action === "run" && result.session) options.acceptTestSnapshot(result.session);
      if (action === "debug" && result.snapshot) options.acceptDebugSnapshot(result.snapshot);
    } catch (error) {
      const message = errorMessage(error);
      options.message(message, true);
      if (action === "debug" && /no enabled go debug adapter/i.test(message)) options.openDebugSettings();
    }
  };

  return {
    dispose() {
      provider.dispose();
      runCommand.dispose();
      debugCommand.dispose();
      coverage.dispose();
    },
  };
}

function errorMessage(error: unknown): string { return error instanceof Error ? error.message : String(error); }
