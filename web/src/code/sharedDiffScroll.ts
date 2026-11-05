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
      // Subtract our existing padding without temporarily removing it. Setting
      // it to zero clamps the shorter pane's scroll position and emits delayed
      // scroll/layout events that can snap both panes back to the beginning.
      const metrics = this.panes.map(pane => {
        const width = Math.max(1, pane.getOption(monaco.editor.EditorOption.fontInfo).typicalHalfwidthCharacterWidth);
        const columns = pane.getOption(monaco.editor.EditorOption.scrollBeyondLastColumn);
        const content = Math.max(0, Math.ceil(pane.getContentWidth() - columns * width));
        return { pane, width, columns, content, viewport: pane.getLayoutInfo().contentWidth };
      });
      const extent = Math.max(0, ...metrics.map(metric => metric.content - metric.viewport));
      for (const { pane, width, columns, content, viewport } of metrics) {
        const padding = Math.ceil(Math.max(0, extent + viewport - content) / width);
        if (padding !== columns) pane.updateOptions({ scrollBeyondLastColumn: padding });
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
