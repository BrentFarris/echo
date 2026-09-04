import type * as Monaco from "monaco-editor";
import { monaco } from "./language";
import { refKey, type FileRef } from "./types";
import { CoverageController } from "./coverageController";

export type CCoverageLine = {
  line: number;
  executionCount: number;
  state: "covered" | "partial" | "uncovered";
};
export type CCoverageFile = { ref: FileRef; lines: CCoverageLine[] };
export type CCoverageSnapshot = {
  revision: number;
  sessionId: string;
  targetId: string;
  provider: "gcov" | "llvm";
  files: CCoverageFile[];
};

type Options = {
  workspaceId: string;
  refForModel(model: Monaco.editor.ITextModel): FileRef | null;
  message(value: string, sticky?: boolean): void;
};

export class CCoverageController implements Monaco.IDisposable {
  private readonly controller: CoverageController<CCoverageSnapshot>;

  constructor(options: Options) {
    this.controller = new CoverageController({
      workspaceId: options.workspaceId,
      eventType: "c_test_coverage",
      endpoint: `/api/workspaces/${encodeURIComponent(options.workspaceId)}/testing/c/coverage`,
      restoreLabel: "C",
      refForModel: options.refForModel,
      message: options.message,
      decorations: (model, ref, coverage) => cDecorations(model, ref, coverage),
    });
  }

  dispose(): void { this.controller.dispose(); }
}

function cDecorations(model: Monaco.editor.ITextModel, ref: FileRef | null, coverage: CCoverageSnapshot | null): Monaco.editor.IModelDeltaDecoration[] {
	const extension = ref?.path.toLowerCase().match(/\.(c|h)$/);
	const file = ref && extension ? coverage?.files.find((candidate) => refKey(candidate.ref) === refKey(ref)) : undefined;
	return (file?.lines || []).flatMap((line) => {
		if (!Number.isInteger(line.line) || line.line < 0 || line.line >= model.getLineCount()) return [];
		const lineNumber = line.line + 1;
		const label = line.state === "covered" ? "Covered" : line.state === "partial" ? "Partially covered" : "Not covered";
		return [{
			range: new monaco.Range(lineNumber, 1, lineNumber, model.getLineMaxColumn(lineNumber)),
			options: {
				isWholeLine: true,
				className: `c-coverage-${line.state}`,
				hoverMessage: { value: `${label} · executed ${line.executionCount} time${line.executionCount === 1 ? "" : "s"}` },
				stickiness: monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges,
			},
		}];
	});
}
