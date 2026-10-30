import type { editor, IDisposable } from "monaco-editor";
import type { SourceControlHunkAction } from "./sourceControlTypes";
import { diffBlock, type DiffBlock } from "./diffHunks";

export type HunkGutterState = { actions: SourceControlHunkAction[]; dirty: boolean; busy: boolean };
const labels: Record<SourceControlHunkAction, string> = {
  stage_hunk: "Stage Change", unstage_hunk: "Unstage Change", protect_hunk: "Protect Change",
  unprotect_hunk: "Unprotect Change", revert_hunk: "Revert Change",
};

/** Owns only Echo's buttons; diff alignment and edits use Monaco's public API. */
export class DiffHunkGutter {
  private readonly layer = document.createElement("div");
  private readonly disposables: IDisposable[] = [];
  private items: Array<{ block: DiffBlock; node: HTMLElement }> = [];
  private frame = 0;
  private computing = true;
  private signature = "";
  private modelVersion = "";

  constructor(private readonly diff: editor.IStandaloneDiffEditor, private readonly state: () => HunkGutterState,
    private readonly run: (action: SourceControlHunkAction, block: DiffBlock) => void) {
    this.layer.className = "code-diff-hunk-gutter";
    diff.getContainerDomNode().appendChild(this.layer);
    const invalidate = () => { this.computing = true; this.refresh(); };
    this.disposables.push(diff.onDidChangeModel(() => { this.signature = ""; this.items = []; this.layer.replaceChildren(); invalidate(); }),
      diff.onDidUpdateDiff(() => { this.computing = false; this.rebuild(); }));
    for (const editor of [diff.getOriginalEditor(), diff.getModifiedEditor()]) {
      this.disposables.push(editor.onDidChangeModelContent(invalidate), editor.onDidScrollChange(() => this.schedule()),
        editor.onDidLayoutChange(() => this.schedule()), editor.onDidChangeViewZones(() => this.schedule()));
    }
  }

  private version(): string {
    const model = this.diff.getModel();
    return model ? `${model.original.id}:${model.original.getVersionId()}:${model.modified.id}:${model.modified.getVersionId()}` : "";
  }

  refresh(): void {
    const state = this.state();
    if (state.actions.join() !== this.signature && !this.computing) { this.rebuild(); return; }
    const enabled = new Set(state.actions);
    for (const { node } of this.items) {
      node.hidden = !enabled.size;
      for (const button of node.querySelectorAll<HTMLButtonElement>("button")) {
        const action = button.dataset.hunkAction as SourceControlHunkAction;
        const needsSave = state.dirty && (action === "stage_hunk" || action === "protect_hunk");
        button.disabled = this.computing || state.busy || needsSave || !enabled.has(action);
        button.title = needsSave ? `Save this file before ${action === "stage_hunk" ? "staging" : "protecting"} a change.` : labels[action];
      }
    }
    this.schedule();
  }

  private rebuild(): void {
    const state = this.state();
    this.signature = state.actions.join();
    this.modelVersion = this.version();
    this.items = [];
    this.layer.replaceChildren();
    if (state.actions.length) {
      for (const change of this.diff.getLineChanges() || []) {
        const block = diffBlock(change);
        const node = document.createElement("div");
        node.className = "code-diff-hunk-actions";
        node.dataset.hunkStart = String(block.modified.start);
        for (const action of state.actions) {
          const button = document.createElement("button");
          button.type = "button";
          button.dataset.hunkAction = action;
          button.setAttribute("aria-label", labels[action]);
          const icon = document.createElement("span");
          icon.className = `codicon codicon-${action === "revert_hunk" ? "arrow-right" : action.startsWith("un") ? "remove" : "add"}`;
          button.appendChild(icon);
          button.addEventListener("click", () => {
            if (!button.disabled && this.version() === this.modelVersion && this.state().actions.includes(action)) this.run(action, block);
          });
          node.appendChild(button);
        }
        this.items.push({ block, node });
        this.layer.appendChild(node);
      }
    }
    this.refresh();
  }

  private schedule(): void {
    if (!this.frame) this.frame = requestAnimationFrame(() => { this.frame = 0; this.layout(); });
  }

  private layout(): void {
    const original = this.diff.getOriginalEditor(), modified = this.diff.getModifiedEditor();
    const host = this.diff.getContainerDomNode().getBoundingClientRect();
    const originalRect = original.getContainerDomNode().getBoundingClientRect();
    const modifiedRect = modified.getContainerDomNode().getBoundingClientRect();
    const split = originalRect.width > 5 && modifiedRect.left > originalRect.left + 5;
    // Inline mode has two line-number columns; keep the controls ahead of both.
    const left = modifiedRect.left - host.left + (split ? -22 : 2);
    for (const { block, node } of this.items) {
      const useOriginal = split && block.original.end > block.original.start;
      const editor = useOriginal ? original : modified;
      const range = useOriginal ? block.original : block.modified;
      const model = editor.getModel();
      if (!model) { node.hidden = true; continue; }
      const top = (range.start <= model.getLineCount() ? editor.getTopForLineNumber(range.start, true)
        : editor.getBottomForLineNumber(model.getLineCount())) - editor.getScrollTop();
      node.style.left = `${Math.max(0, left)}px`;
      node.style.top = `${top + (useOriginal ? originalRect.top : modifiedRect.top) - host.top}px`;
      node.style.visibility = top < -32 || top > modifiedRect.height - 12 ? "hidden" : "visible";
    }
  }

  dispose(): void {
    if (this.frame) cancelAnimationFrame(this.frame);
    this.disposables.forEach((item) => item.dispose());
    this.layer.remove();
  }
}
