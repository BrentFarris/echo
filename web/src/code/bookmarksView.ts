import { bookmarkLabel, groupBookmarks, type Bookmark } from "./bookmarkTypes";
import { refKey, type WorkspaceRoot } from "./types";
import { escapeHTML } from "./ui";

export type BookmarksViewOptions = {
  roots(): WorkspaceRoot[];
  toggle(): void;
  open(bookmark: Bookmark): void;
  rename(id: string, label: string): void;
  remove(id: string): void;
  retry(): void;
};

export class BookmarksView {
  private bookmarks: Bookmark[] = [];
  private collapsed = new Set<string>();
  private editing: string | null = null;
  private error = "";
  private loaded = false;
  private readonly abort = new AbortController();

  constructor(private readonly host: HTMLElement, private readonly options: BookmarksViewOptions) {
    host.addEventListener("click", event => this.click(event), { signal: this.abort.signal });
    this.render();
  }

  update(bookmarks: Bookmark[], loaded: boolean, error: string): void {
    this.bookmarks = bookmarks;
    this.loaded = loaded;
    this.error = error;
    // Remote updates and editor changes must not steal an in-progress rename.
    if (this.editing && bookmarks.some(mark => mark.id === this.editing)) return;
    this.editing = null;
    this.render();
  }

  dispose(): void { this.abort.abort(); this.host.replaceChildren(); }

  private render(): void {
    const roots = this.options.roots();
    const groups = groupBookmarks(this.bookmarks);
    this.host.innerHTML = `
      <header class="code-bookmarks-header"><span>Bookmarks</span>
        <button type="button" data-bookmark-toggle title="Bookmarks: Toggle (Ctrl+K, K)" aria-label="Toggle bookmark"><span class="codicon codicon-bookmark"></span></button>
      </header>
      <div class="code-bookmarks-status" role="status"${this.error ? "" : " hidden"}>${escapeHTML(this.error)} <button type="button" data-bookmark-retry>Retry</button></div>
      <div class="code-bookmarks-list" aria-label="Bookmarks by file">
      ${!this.loaded ? '<p class="code-bookmarks-empty">Loading bookmarks…</p>' : groups.length ? groups.map(group => {
        const key = refKey(group.ref);
        const expanded = !this.collapsed.has(key);
        const slash = group.ref.path.lastIndexOf("/");
        const name = group.ref.path.slice(slash + 1);
        const directory = group.ref.path.slice(0, Math.max(0, slash));
        const root = roots.find(item => item.id === group.ref.rootId)?.label || group.ref.rootId;
        const description = [roots.length > 1 ? root : "", directory].filter(Boolean).join("/");
        return `<section class="code-bookmark-group">
          <button type="button" class="code-bookmark-file" data-bookmark-group="${escapeHTML(key)}" aria-expanded="${expanded}" title="${escapeHTML(root + "/" + group.ref.path)}">
            <span class="codicon codicon-chevron-${expanded ? "down" : "right"}"></span><span class="codicon codicon-file-code"></span>
            <strong>${escapeHTML(name)}</strong><small>${escapeHTML(description)}</small>
          </button>
          <div class="code-bookmark-children"${expanded ? "" : " hidden"}>
          ${group.bookmarks.map(mark => `<div class="code-bookmark-row" data-bookmark-id="${escapeHTML(mark.id)}">
            <button type="button" class="code-bookmark-open" data-bookmark-open title="${escapeHTML(bookmarkLabel(mark) + " — " + root + "/" + mark.ref.path + ":" + mark.line)}">
              <span class="codicon codicon-bookmark"></span><span class="code-bookmark-label">${escapeHTML(bookmarkLabel(mark))}</span><small>${mark.line}</small>
            </button>
            <span class="code-bookmark-actions">
              <button type="button" data-bookmark-rename aria-label="Rename bookmark" title="Rename bookmark"><span class="codicon codicon-edit"></span></button>
              <button type="button" data-bookmark-remove aria-label="Delete bookmark" title="Delete bookmark"><span class="codicon codicon-close"></span></button>
            </span>
          </div>`).join("")}</div>
        </section>`;
      }).join("") : `<div class="code-bookmarks-empty"><p>No bookmarks yet.</p><p>Place the caret on a code line and press <kbd>Ctrl+K, K</kbd> (Command on Mac).</p><button type="button" data-bookmark-toggle>Bookmarks: Toggle</button></div>`}
      </div>`;
  }

  private click(event: MouseEvent): void {
    const target = event.target instanceof Element ? event.target : null;
    if (!target || target.closest("[data-bookmark-input]")) return;
    if (target.closest("[data-bookmark-toggle]")) { this.options.toggle(); return; }
    if (target.closest("[data-bookmark-retry]")) { this.options.retry(); return; }
    const group = target.closest<HTMLElement>("[data-bookmark-group]")?.dataset.bookmarkGroup;
    if (group) { this.collapsed.has(group) ? this.collapsed.delete(group) : this.collapsed.add(group); this.render(); return; }
    const row = target.closest<HTMLElement>("[data-bookmark-id]");
    const mark = this.bookmarks.find(item => item.id === row?.dataset.bookmarkId);
    if (!row || !mark) return;
    if (target.closest("[data-bookmark-remove]")) this.options.remove(mark.id);
    else if (target.closest("[data-bookmark-rename]")) this.beginRename(row, mark);
    else if (target.closest("[data-bookmark-open]")) this.options.open(mark);
  }

  private beginRename(row: HTMLElement, mark: Bookmark): void {
    this.editing = mark.id;
    const input = document.createElement("input");
    input.dataset.bookmarkInput = "";
    input.setAttribute("aria-label", "Bookmark name");
    input.value = mark.label || bookmarkLabel(mark);
    input.maxLength = 512;
    row.replaceChildren(input);
    let finished = false;
    const finish = (save: boolean) => {
      if (finished) return;
      finished = true;
      this.editing = null;
      if (save) this.options.rename(mark.id, input.value.trim());
      this.render();
      this.host.querySelector<HTMLElement>(`[data-bookmark-id="${mark.id}"] [data-bookmark-open]`)?.focus();
    };
    input.addEventListener("blur", () => finish(true));
    input.addEventListener("keydown", event => {
      if (event.key === "Enter" || event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        finish(event.key === "Enter");
      }
    });
    input.focus();
    input.select();
  }
}
