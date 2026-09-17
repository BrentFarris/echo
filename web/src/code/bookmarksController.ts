import type { editor as Editor, IDisposable } from "monaco-editor";
import { api } from "../../js/api.js";
import { on as onSocket, onState as onSocketState } from "../../js/ws.js";
import { monaco } from "./language";
import { bookmarkLabel, type Bookmark, type BookmarkAction, type BookmarkPosition, type BookmarkState } from "./bookmarkTypes";
import { refKey, type FileRef } from "./types";

type Binding = { model: Editor.ITextModel; ref: FileRef; decorations: Map<string, string>; subscriptions: IDisposable[] };
export type BookmarksControllerOptions = {
  workspaceId: string;
  refForModel(model: Editor.ITextModel): FileRef | null;
  changed(bookmarks: Bookmark[], loaded: boolean, error: string): void;
  reportError(message: string): void;
};

export class BookmarksController {
  private state: BookmarkState = { version: 1, revision: -1, bookmarks: [] };
  private loaded = false;
  private error = "";
  private disposed = false;
  private bindings = new Map<Editor.ITextModel, Binding>();
  private pending = new Map<string, BookmarkPosition>();
  private retryActions: BookmarkAction[] = [];
  private subscriptions: Array<() => void> = [];
  private timer: ReturnType<typeof setTimeout> | undefined;
  private queue: Promise<unknown> = Promise.resolve();
  private flushing: Promise<boolean> | null = null;

  constructor(private readonly options: BookmarksControllerOptions) {
    this.subscriptions.push(onSocket("bookmarks_changed", (raw: object) => {
      const event = raw as { workspaceId?: string; state?: BookmarkState };
      if (event.workspaceId === options.workspaceId && event.state) this.accept(event.state);
    }));
    this.subscriptions.push(onSocketState(state => { if (state === "open") void this.refresh(); }));
    const created = monaco.editor.onDidCreateModel(model => {
      // CodeView attaches a newly created model to its file tab synchronously.
      queueMicrotask(() => { if (!this.disposed && !model.isDisposed()) this.attach(model); });
    });
    this.subscriptions.push(() => created.dispose());
    const pagehide = () => { void this.flush(); };
    window.addEventListener("pagehide", pagehide);
    this.subscriptions.push(() => window.removeEventListener("pagehide", pagehide));
    for (const model of monaco.editor.getModels()) this.attach(model);
    void this.refresh();
  }

  get bookmarks(): Bookmark[] {
    return this.state.bookmarks.map(mark => ({ ...mark, ...this.pending.get(mark.id) }));
  }

  private notify(): void {
    if (!this.disposed) this.options.changed(this.bookmarks, this.loaded, this.error);
  }

  private fail(error: unknown): void {
    this.error = `Bookmarks could not be saved or loaded: ${error instanceof Error ? error.message : String(error)}`;
    this.options.reportError(this.error);
    this.notify();
  }

  async refresh(): Promise<void> {
    try {
      const state = await api("/api/plugins/bookmarks/state", { query: { workspaceId: this.options.workspaceId } }) as BookmarkState;
      if (!this.disposed) {
        this.accept(state);
        if (!this.pending.size && !this.retryActions.length) this.error = "";
        this.notify();
      }
    } catch (error) { if (!this.disposed) this.fail(error); }
  }

  async retry(): Promise<void> {
    await this.refresh();
    if (!await this.flush()) return;
    const actions = this.retryActions.splice(0);
    for (const action of actions) await this.mutate(action);
  }

  private accept(state: BookmarkState): void {
    if (this.disposed || state.revision < this.state.revision) return;
    this.state = state;
    this.loaded = true;
    for (const id of this.pending.keys()) {
      if (!state.bookmarks.some(mark => mark.id === id)) this.pending.delete(id);
    }
    this.reconcile();
    this.notify();
  }

  private async mutate(action: BookmarkAction): Promise<boolean> {
    const operation = this.queue.then(async () => {
      if ("id" in action) {
        this.retryActions = this.retryActions.filter(retry => !("id" in retry) || retry.id !== action.id);
      }
      try {
        const state = await api("/api/plugins/bookmarks/state", {
          method: "POST", query: { workspaceId: this.options.workspaceId }, body: action, keepalive: true,
        }) as BookmarkState;
        if (!this.retryActions.length && (action.action === "positions" || !this.pending.size)) this.error = "";
        this.accept(state);
        return true;
      } catch (error) {
        // Only idempotent actions can be retried after an uncertain response.
        if (action.action === "rename" || action.action === "delete" || action.action === "remap") this.retryActions.push(action);
        this.fail(error);
        return false;
      }
    });
    this.queue = operation;
    return operation;
  }

  async toggle(ref: FileRef, line: number, preview: string): Promise<void> {
    if (await this.flush()) await this.mutate({ action: "toggle", ref, line, preview: preview.trim().slice(0, 800) });
  }

  async rename(id: string, label: string): Promise<void> { await this.mutate({ action: "rename", id, label }); }
  async remove(id: string): Promise<void> { await this.mutate({ action: "delete", id }); }

  async remap(previousRef: FileRef, nextRef: FileRef): Promise<void> {
    if (!await this.flush() || this.retryActions.some(action => action.action === "remap")) {
      this.retryActions.push({ action: "remap", previousRef, nextRef });
      return;
    }
    await this.mutate({ action: "remap", previousRef, nextRef });
  }

  flush(): Promise<boolean> {
    clearTimeout(this.timer);
    if (this.flushing) return this.flushing.then(ok => ok && this.pending.size ? this.flush() : ok);
    const positions = [...this.pending.values()];
    if (!positions.length) return Promise.resolve(true);
    this.flushing = this.mutate({ action: "positions", positions }).then(ok => {
      if (ok) {
        for (const position of positions) {
          if (this.pending.get(position.id) === position) this.pending.delete(position.id);
        }
        if (!this.disposed) { this.reconcile(); this.notify(); }
      }
      return ok;
    }).finally(() => { this.flushing = null; });
    return this.flushing;
  }

  private attach(model: Editor.ITextModel): void {
    if (this.bindings.has(model)) return;
    const ref = this.options.refForModel(model);
    if (!ref?.path) return;
    const binding: Binding = { model, ref, decorations: new Map(), subscriptions: [] };
    this.bindings.set(model, binding);
    binding.subscriptions.push(model.onDidChangeContent(event => {
      // setValue (for example reloading from disk) clears Monaco decorations.
      if (event?.isFlush) { binding.decorations.clear(); this.reconcileBinding(binding); }
      this.capture(binding);
    }));
    binding.subscriptions.push(model.onWillDispose(() => {
      this.capture(binding);
      void this.flush();
      this.bindings.delete(model);
      binding.subscriptions.forEach(item => item.dispose());
    }));
    this.reconcileBinding(binding);
  }

  private capture(binding: Binding): void {
    if (this.disposed) return;
    for (const [id, decoration] of binding.decorations) {
      const range = binding.model.getDecorationRange(decoration);
      const saved = this.state.bookmarks.find(mark => mark.id === id);
      if (!range || !saved) continue;
      const line = Math.min(binding.model.getLineCount(), Math.max(1, range.startLineNumber));
      const preview = binding.model.getLineContent(line).trim().slice(0, 800);
      // Do not remove an existing pending entry: an in-flight write may still
      // contain a different position (including when this edit was undo).
      if (this.pending.has(id) || line !== saved.line || preview !== saved.preview) {
        this.pending.set(id, { id, ref: binding.ref, line, preview });
      }
    }
    this.notify();
    clearTimeout(this.timer);
    if (this.pending.size) this.timer = setTimeout(() => { void this.flush(); }, 300);
  }

  private reconcile(): void {
    for (const binding of this.bindings.values()) this.reconcileBinding(binding);
  }

  private reconcileBinding(binding: Binding): void {
    const marks = this.state.bookmarks.filter(mark => refKey(mark.ref) === refKey(binding.ref));
    for (const [id, decoration] of binding.decorations) {
      if (!marks.some(mark => mark.id === id)) {
        binding.model.deltaDecorations([decoration], []);
        binding.decorations.delete(id);
      }
    }
    for (const mark of marks) {
      const line = Math.max(1, Math.min(binding.model.getLineCount(), mark.line));
      const options: Editor.IModelDecorationOptions = {
        glyphMarginClassName: "echo-bookmark-glyph",
        glyphMargin: { position: monaco.editor.GlyphMarginLane.Left, persistLane: true },
        glyphMarginHoverMessage: { value: bookmarkLabel(mark) },
        stickiness: monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges,
      };
      const decoration = binding.decorations.get(mark.id);
      if (decoration) {
        const range = binding.model.getDecorationRange(decoration);
        const target = this.pending.has(mark.id) && range ? range : new monaco.Range(line, 1, line, 1);
        const previousHover = binding.model.getDecorationOptions(decoration)?.glyphMarginHoverMessage;
        if (range?.startLineNumber !== target.startLineNumber || (previousHover as { value?: string })?.value !== bookmarkLabel(mark)) {
          binding.decorations.set(mark.id, binding.model.deltaDecorations([decoration], [{ range: target, options }])[0]);
        }
      } else {
        binding.decorations.set(mark.id, binding.model.deltaDecorations([], [{ range: new monaco.Range(line, 1, line, 1), options }])[0]);
      }
    }
  }

  dispose(): void {
    if (this.disposed) return;
    for (const binding of this.bindings.values()) this.capture(binding);
    void this.flush();
    this.disposed = true;
    clearTimeout(this.timer);
    this.subscriptions.forEach(unsubscribe => unsubscribe());
    for (const binding of this.bindings.values()) {
      binding.subscriptions.forEach(item => item.dispose());
      if (!binding.model.isDisposed()) binding.model.deltaDecorations([...binding.decorations.values()], []);
    }
    this.bindings.clear();
  }
}
