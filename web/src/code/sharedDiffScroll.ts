import type { editor, IDisposable } from 'monaco-editor';
import { monaco } from './language';

/** One visible scrollbar owns the horizontal position of both panes. */
export class SharedDiffScroll {
  private disposables: IDisposable[] = [];
  private updating = false;
  private frame = 0;
  private get panes() { return [this.diff.getOriginalEditor(), this.diff.getModifiedEditor()]; }
  private onScroll = () => {
    if (this.updating) return;
    this.updating = true;
    for (const pane of this.panes) pane.setScrollLeft(this.host.scrollLeft);
    this.updating = false;
  };
  constructor(private diff: editor.IStandaloneDiffEditor, private host: HTMLElement) {
    host.addEventListener('scroll', this.onScroll);
    for (const pane of this.panes) {
      pane.updateOptions({ scrollbar: { horizontal: 'hidden', horizontalScrollbarSize: 0 } });
      this.disposables.push(pane.onDidContentSizeChange(() => this.refresh()), pane.onDidLayoutChange(() => this.refresh()), pane.onDidScrollChange(event => {
        if (!event.scrollLeftChanged || this.updating) return;
        this.host.scrollLeft = event.scrollLeft;
        this.onScroll();
      }));
    }
    this.disposables.push(diff.onDidChangeModel(() => this.refresh()));
  }
  refresh() {
    if (this.updating) return;
    cancelAnimationFrame(this.frame);
    this.frame = requestAnimationFrame(() => {
      this.updating = true;
      const left = this.host.scrollLeft;
      // Measure the real content without the padding used to keep shorter
      // panes scrollable. Padding is never included in the visible range.
      for (const pane of this.panes) pane.updateOptions({ scrollBeyondLastColumn: 0 });
      const extent = Math.max(0, ...this.panes.map(pane => pane.getScrollWidth() - pane.getLayoutInfo().contentWidth));
      for (const pane of this.panes) {
        const width = pane.getOption(monaco.editor.EditorOption.fontInfo).typicalHalfwidthCharacterWidth;
        pane.updateOptions({ scrollBeyondLastColumn: Math.ceil((extent + pane.getLayoutInfo().contentWidth) / Math.max(1, width)) });
      }
      (this.host.firstElementChild as HTMLElement).style.width = `${this.host.clientWidth + extent}px`;
      this.host.scrollLeft = Math.min(left, extent);
      this.updating = false;
      this.onScroll();
    });
  }
  dispose() {
    cancelAnimationFrame(this.frame);
    this.host.removeEventListener('scroll', this.onScroll);
    this.disposables.forEach(d => d.dispose());
  }
}
