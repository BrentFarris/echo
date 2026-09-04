import type * as Monaco from "monaco-editor";
import { api } from "../../js/api.js";
import { on as onSocket, onState as onSocketState } from "../../js/ws.js";
import { monaco } from "./language";
import { refKey, type FileRef } from "./types";

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

type GoCoverageEvent = {
  type: "go_test_coverage";
  workspaceId: string;
  revision: number;
  state: "ready" | "cleared" | "error";
  coverage?: GoCoverageSnapshot;
  message?: string;
};

type Options = {
  workspaceId: string;
  refForModel(model: Monaco.editor.ITextModel): FileRef | null;
  message(value: string, sticky?: boolean): void;
};

export class GoCoverageController implements Monaco.IDisposable {
  private readonly unsubscribe: Array<() => void> = [];
  private readonly decorations = new Map<Monaco.editor.ITextModel, string[]>();
  private coverage: GoCoverageSnapshot | null = null;
  private revision = -1;
  private refreshPromise: Promise<void> | null = null;
  private refreshAgain = false;
  private disposed = false;

  constructor(private readonly options: Options) {
    this.unsubscribe.push(onSocket("go_test_coverage", (raw: object) => this.receive(raw as GoCoverageEvent)));
    this.unsubscribe.push(onSocket("terminal_event", (raw: object) => {
      const event = raw as { workspaceId?: string; kind?: string; event?: string };
      if (event.workspaceId === this.options.workspaceId && event.kind === "test" && event.event === "exited") {
        void this.refresh();
      }
    }));
    this.unsubscribe.push(onSocketState((state) => {
      if (state === "open") void this.refresh();
    }));
    const created = monaco.editor.onDidCreateModel((model) => this.applyModel(model));
    const disposing = monaco.editor.onWillDisposeModel((model) => {
      this.decorations.delete(model);
    });
    this.unsubscribe.push(() => created.dispose(), () => disposing.dispose());
    void this.refresh();
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    for (const unsubscribe of this.unsubscribe) unsubscribe();
    this.clearDecorations();
    this.coverage = null;
  }

  private receive(event: GoCoverageEvent): void {
    if (event.workspaceId !== this.options.workspaceId || event.revision < this.revision) return;
    this.revision = event.revision;
    if (event.state === "ready" && event.coverage) {
      this.coverage = event.coverage;
      this.applyAllModels();
      return;
    }
    this.coverage = null;
    this.clearDecorations();
    if (event.state === "error" && event.message) this.options.message(event.message, true);
  }

  private refresh(): Promise<void> {
    if (this.disposed) return Promise.resolve();
    if (this.refreshPromise) {
      this.refreshAgain = true;
      return this.refreshPromise;
    }
    this.refreshPromise = api(`/api/workspaces/${encodeURIComponent(this.options.workspaceId)}/testing/go/coverage`)
      .then((result) => {
        if (this.disposed) return;
        const response = result as { revision?: number; coverage?: GoCoverageSnapshot | null };
        const revision = Number(response.revision || 0);
        if (revision < this.revision) return;
        this.revision = revision;
        this.coverage = response.coverage || null;
        if (this.coverage) this.applyAllModels();
        else this.clearDecorations();
      })
      .catch((error) => {
        if (!this.disposed) this.options.message(`Could not restore Go coverage: ${errorMessage(error)}`);
      })
      .finally(() => {
        this.refreshPromise = null;
        if (this.refreshAgain && !this.disposed) {
          this.refreshAgain = false;
          void this.refresh();
        }
      });
    return this.refreshPromise;
  }

  private applyAllModels(): void {
    for (const model of monaco.editor.getModels()) this.applyModel(model);
  }

  private applyModel(model: Monaco.editor.ITextModel): void {
    if (this.disposed || model.isDisposed()) return;
    const previous = this.decorations.get(model) || [];
    const ref = this.options.refForModel(model);
    const file = ref && !ref.path.toLowerCase().endsWith("_test.go")
      ? this.coverage?.files.find((candidate) => refKey(candidate.ref) === refKey(ref))
      : undefined;
    const next: Monaco.editor.IModelDeltaDecoration[] = (file?.ranges || []).flatMap((range) => {
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
    const ids = model.deltaDecorations(previous, next);
    if (ids.length) this.decorations.set(model, ids);
    else this.decorations.delete(model);
  }

  private clearDecorations(): void {
    for (const [model, ids] of this.decorations) {
      if (!model.isDisposed()) model.deltaDecorations(ids, []);
    }
    this.decorations.clear();
  }
}

function validRange(range: GoCoverageRange): boolean {
  if (!Number.isInteger(range.start.line) || !Number.isInteger(range.start.character)
    || !Number.isInteger(range.end.line) || !Number.isInteger(range.end.character)) return false;
  if (range.start.line < 0 || range.start.character < 0 || range.end.line < range.start.line) return false;
  return range.end.line !== range.start.line || range.end.character >= range.start.character;
}

function errorMessage(error: unknown): string { return error instanceof Error ? error.message : String(error); }
