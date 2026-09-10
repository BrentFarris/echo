import type * as Monaco from "monaco-editor";
import { api } from "../../js/api.js";
import type { DebugSnapshot } from "../debug/types";
import type { TerminalSnapshot } from "../terminal/terminalApi";
import { monaco } from "./language";
import type { FileRef } from "./types";
import { fromLSPRange } from "./lspClient";
import type { LSPRange } from "./lspTypes";
import { CCoverageController } from "./cCoverage";

type CLens = { range: LSPRange; title: string; action: "run" | "debug"; targetId: string };

type Options = {
  workspaceId: string;
  refForModel(model: Monaco.editor.ITextModel): FileRef | null;
  saveAll(): Promise<boolean>;
  acceptTestSnapshot(snapshot: TerminalSnapshot): void;
  acceptDebugSnapshot(snapshot: DebugSnapshot): void;
  openDebugSettings(): void;
  message(value: string, sticky?: boolean): void;
};

export function registerCTestCodeLens(options: Options): Monaco.IDisposable {
  const coverage = new CCoverageController({
    workspaceId: options.workspaceId, refForModel: options.refForModel, message: options.message,
  });
  const provider = monaco.languages.registerCodeLensProvider("cpp", {
    async provideCodeLenses(model, token) {
      const ref = options.refForModel(model);
      if (!ref || !ref.path.toLowerCase().endsWith(".c") || token.isCancellationRequested) return { lenses: [], dispose() {} };
      try {
        const result = await api(`/api/workspaces/${encodeURIComponent(options.workspaceId)}/testing/c/lenses`, {
          method: "POST", body: { ref, text: model.getValue() },
        }) as { lenses?: CLens[] };
        if (token.isCancellationRequested) return { lenses: [], dispose() {} };
        return {
          lenses: (result.lenses || []).map((item) => ({
            range: fromLSPRange(item.range),
            command: {
              id: item.action === "debug" ? "echo.cTest.debug" : "echo.cTest.run",
              title: item.title,
              arguments: [item.targetId],
            },
          })),
          dispose() {},
        };
      } catch (error) {
        if (!token.isCancellationRequested) options.message(errorMessage(error));
        return { lenses: [], dispose() {} };
      }
    },
  });
  const runCommand = monaco.editor.registerCommand("echo.cTest.run", (_accessor, targetId: string) => void execute("run", targetId));
  const debugCommand = monaco.editor.registerCommand("echo.cTest.debug", (_accessor, targetId: string) => void execute("debug", targetId));

  const execute = async (action: "run" | "debug", targetId: string) => {
    if (!(await options.saveAll())) return;
    try {
      const endpoint = action === "debug" ? "debug-sessions" : "runs";
      const result = await api(`/api/workspaces/${encodeURIComponent(options.workspaceId)}/testing/c/${endpoint}`, {
        method: "POST", body: { targetId },
      }) as { session?: TerminalSnapshot; snapshot?: DebugSnapshot };
      if (action === "run" && result.session) options.acceptTestSnapshot(result.session);
      if (action === "debug" && result.snapshot) options.acceptDebugSnapshot(result.snapshot);
    } catch (error) {
      const message = errorMessage(error);
      options.message(message, true);
      if (action === "debug" && /no enabled.*lldb|lldb.*not.*enabled/i.test(message)) options.openDebugSettings();
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
