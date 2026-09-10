import type * as Monaco from "monaco-editor";
import { monaco } from "./language";
import { refKey, type FileRef } from "./types";
import { CoverageController } from "./coverageController";

export type GoCoveragePosition = { line: number; character: number };
export type GoCoverageRange = {
  start: GoCoveragePosition;
  end: GoCoveragePosition;
  statements: number;
  count: number;
};
export type GoCoverageFile = { ref: FileRef; ranges: GoCoverageRange[] };
export type GoCoverageSnapshot = {
  revision: number;
  sessionId: string;
  package: FileRef;
  mode: "set" | "count" | "atomic";
  files: GoCoverageFile[];
};

type Options = {
  workspaceId: string;
  refForModel(model: Monaco.editor.ITextModel): FileRef | null;
  message(value: string, sticky?: boolean): void;
};

export class GoCoverageController implements Monaco.IDisposable {
  private readonly controller: CoverageController<GoCoverageSnapshot>;

  constructor(options: Options) {
    this.controller = new CoverageController({
      workspaceId: options.workspaceId,
      eventType: "go_test_coverage",
      endpoint: `/api/workspaces/${encodeURIComponent(options.workspaceId)}/testing/go/coverage`,
      restoreLabel: "Go",
      refForModel: options.refForModel,
      message: options.message,
      decorations: (_model, ref, coverage) => goDecorations(ref, coverage),
    });
  }

  dispose(): void { this.controller.dispose(); }
}

function goDecorations(ref: FileRef | null, coverage: GoCoverageSnapshot | null): Monaco.editor.IModelDeltaDecoration[] {
	const file = ref && !ref.path.toLowerCase().endsWith("_test.go")
		? coverage?.files.find((candidate) => refKey(candidate.ref) === refKey(ref))
		: undefined;
	return (file?.ranges || []).flatMap((range) => {
		if (!validRange(range)) return [];
		return [{
			range: new monaco.Range(
				range.start.line + 1, range.start.character + 1,
				range.end.line + 1, range.end.character + 1,
			),
			options: {
				inlineClassName: range.count > 0 ? "go-coverage-covered" : "go-coverage-uncovered",
				hoverMessage: { value: range.count > 0 ? "Covered" : "Not covered" },
				stickiness: monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges,
			},
		}];
	});
}

function validRange(range: GoCoverageRange): boolean {
  if (!Number.isInteger(range.start.line) || !Number.isInteger(range.start.character)
    || !Number.isInteger(range.end.line) || !Number.isInteger(range.end.character)) return false;
  if (range.start.line < 0 || range.start.character < 0 || range.end.line < range.start.line) return false;
  return range.end.line !== range.start.line || range.end.character >= range.start.character;
}
