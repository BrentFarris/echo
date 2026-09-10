import type * as Monaco from "monaco-editor";
import { api } from "../../js/api.js";
import { on as onSocket, onState as onSocketState } from "../../js/ws.js";
import { monaco } from "./language";
import type { FileRef } from "./types";

type CoverageEvent<T> = {
  workspaceId: string;
  revision: number;
  state: "ready" | "cleared" | "error";
  coverage?: T;
  message?: string;
};

export type CoverageControllerOptions<T> = {
  workspaceId: string;
  eventType: string;
  endpoint: string;
  restoreLabel: string;
  refForModel(model: Monaco.editor.ITextModel): FileRef | null;
  decorations(model: Monaco.editor.ITextModel, ref: FileRef | null, coverage: T | null): Monaco.editor.IModelDeltaDecoration[];
  message(value: string, sticky?: boolean): void;
};

export class CoverageController<T> implements Monaco.IDisposable {
  private readonly unsubscribe: Array<() => void> = [];
  private readonly modelDecorations = new Map<Monaco.editor.ITextModel, string[]>();
  private coverage: T | null = null;
  private revision = -1;
  private refreshPromise: Promise<void> | null = null;
  private refreshAgain = false;
  private disposed = false;

  constructor(private readonly options: CoverageControllerOptions<T>) {
    this.unsubscribe.push(onSocket(options.eventType, (raw: object) => this.receive(raw as CoverageEvent<T>)));
    this.unsubscribe.push(onSocket("terminal_event", (raw: object) => {
      const event = raw as { workspaceId?: string; kind?: string; event?: string };
      if (event.workspaceId === options.workspaceId && event.kind === "test" && event.event === "exited") void this.refresh();
    }));
    this.unsubscribe.push(onSocketState((state) => { if (state === "open") void this.refresh(); }));
    const created = monaco.editor.onDidCreateModel((model) => this.applyModel(model));
    const disposing = monaco.editor.onWillDisposeModel((model) => this.modelDecorations.delete(model));
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

  private receive(event: CoverageEvent<T>): void {
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
    this.refreshPromise = api(this.options.endpoint)
      .then((result) => {
        if (this.disposed) return;
        const response = result as { revision?: number; coverage?: T | null };
        const revision = Number(response.revision || 0);
        if (revision < this.revision) return;
        this.revision = revision;
        this.coverage = response.coverage || null;
        if (this.coverage) this.applyAllModels();
        else this.clearDecorations();
      })
      .catch((error) => {
        if (!this.disposed) this.options.message(`Could not restore ${this.options.restoreLabel} coverage: ${errorMessage(error)}`);
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
    const previous = this.modelDecorations.get(model) || [];
    const next = this.options.decorations(model, this.options.refForModel(model), this.coverage);
    const ids = model.deltaDecorations(previous, next);
    if (ids.length) this.modelDecorations.set(model, ids);
    else this.modelDecorations.delete(model);
  }

  private clearDecorations(): void {
    for (const [model, ids] of this.modelDecorations) if (!model.isDisposed()) model.deltaDecorations(ids, []);
    this.modelDecorations.clear();
  }
}

function errorMessage(error: unknown): string { return error instanceof Error ? error.message : String(error); }
