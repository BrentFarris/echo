import type { IRange } from "monaco-editor";
import { refKey, type FileRef } from "./types";
import { OutlineController } from "./outlineController";
import { outlineSymbolIcon } from "./outlineSymbols";
import type { OutlineFile, OutlineSnapshot, OutlineSymbol, OutlineTarget } from "./outlineTypes";
import { escapeHTML } from "./ui";

type Row = { key: string; parent?: string; depth: number; label: string; detail?: string; icon: string;
  expanded?: boolean; file: OutlineFile; symbol?: OutlineSymbol; message?: boolean };
const rowHeight = 22;
const defaultRatio = 1 / 3;

export class OutlineView {
  private readonly abort = new AbortController();
  private readonly controller: OutlineController;
  private readonly toggle: HTMLButtonElement;
  private readonly body: HTMLElement;
  private readonly status: HTMLElement;
  private readonly scroller: HTMLElement;
  private readonly canvas: HTMLElement;
  private readonly divider: HTMLElement;
  private readonly resize: ResizeObserver;
  private opened = false;
  private ratio = defaultRatio;
  private snapshot: OutlineSnapshot = { grouped: false, loading: false, selectionEmpty: true, files: [], completed: 0 };
  private collapsedFiles = new Set<string>();
  private expandedSymbols = new Set<string>();
  private rows: Row[] = [];
  private focusedKey: string | null = null;
  private renderFrame = 0;

  constructor(private readonly host: HTMLElement, private readonly options: {
    selection(): OutlineTarget[];
    list: ConstructorParameters<typeof OutlineController>[0]["list"];
    resolve: ConstructorParameters<typeof OutlineController>[0]["resolve"];
    label(ref: FileRef): string;
    navigate(ref: FileRef, range: IRange): Promise<unknown>;
    saveSize(): void;
  }) {
    host.classList.add("code-outline");
    host.innerHTML = `<div class="code-outline-divider" role="separator" aria-label="Resize Outline" aria-orientation="horizontal" tabindex="0" hidden></div>
      <button class="code-outline-toggle" type="button" aria-expanded="false" aria-controls="code-outline-body"><span class="codicon codicon-chevron-right" aria-hidden="true"></span><strong>OUTLINE</strong></button>
      <div class="code-outline-body" id="code-outline-body" hidden>
        <div class="code-outline-status" role="status" aria-live="polite"></div>
        <div class="code-outline-tree" role="tree" aria-label="Outline symbols" tabindex="0"><div class="code-outline-canvas"></div></div>
      </div>`;
    this.toggle = host.querySelector("button")!;
    this.body = host.querySelector(".code-outline-body")!;
    this.status = host.querySelector(".code-outline-status")!;
    this.scroller = host.querySelector(".code-outline-tree")!;
    this.canvas = host.querySelector(".code-outline-canvas")!;
    this.divider = host.querySelector(".code-outline-divider")!;
    this.controller = new OutlineController({ ...options, change: (state) => {
      this.snapshot = state;
      this.scheduleRender();
    } });
    const signal = this.abort.signal;
    this.toggle.addEventListener("click", () => this.setOpen(!this.opened), { signal });
    this.scroller.addEventListener("scroll", () => this.renderWindow(), { signal, passive: true });
    this.scroller.addEventListener("keydown", (event) => this.keydown(event), { signal });
    this.scroller.addEventListener("click", (event) => {
      const target = (event.target as Element).closest<HTMLElement>("[data-outline-row]");
      const row = this.rows.find((item) => item.key === target?.dataset.outlineRow);
      if (!row) return;
      this.focusedKey = row.key;
      this.scroller.focus();
      if (!row.symbol || (event.target as Element).closest("[data-outline-chevron]")) this.toggleRow(row);
      else this.navigate(row);
      this.renderWindow();
    }, { signal });
    this.installDivider();
    this.resize = new ResizeObserver(() => { this.layout(); this.renderWindow(); });
    this.resize.observe(host.parentElement!);
    this.resize.observe(this.scroller);
  }

  restoreSize(value: number | undefined): void {
    this.ratio = typeof value === "number" && Number.isFinite(value) ? Math.max(0.1, Math.min(0.9, value)) : defaultRatio;
    this.layout();
  }

  get sizeRatio(): number { return this.ratio; }

  collapse(): void { if (this.opened) this.setOpen(false); }

  private setOpen(open: boolean): void {
    this.opened = open;
    this.toggle.setAttribute("aria-expanded", String(open));
    this.toggle.querySelector("span")!.className = `codicon codicon-chevron-${open ? "down" : "right"}`;
    this.body.hidden = !open;
    this.divider.hidden = !open;
    this.host.classList.toggle("is-open", open);
    if (open) {
      this.collapsedFiles.clear();
      this.expandedSymbols.clear();
      this.focusedKey = null;
      this.scroller.scrollTop = 0;
      void this.controller.open(this.options.selection());
    } else {
      this.controller.close();
      if (this.renderFrame) cancelAnimationFrame(this.renderFrame);
      this.renderFrame = 0;
    }
    this.layout();
  }

  private availableHeight(): number { return Math.max(26, (this.host.parentElement?.clientHeight || 0) - 58); }

  private layout(): void {
    const available = this.availableHeight();
    const minimum = Math.min(80, available / 2);
    const height = this.opened ? Math.max(minimum, Math.min(available - minimum, available * this.ratio)) : 26;
    this.host.style.height = `${height}px`;
    this.divider.setAttribute("aria-valuemin", "10");
    this.divider.setAttribute("aria-valuemax", "90");
    this.divider.setAttribute("aria-valuenow", String(Math.round(this.ratio * 100)));
    this.divider.setAttribute("aria-valuetext", `${Math.round(this.ratio * 100)} percent`);
  }

  private installDivider(): void {
    const signal = this.abort.signal;
    const set = (ratio: number) => { this.ratio = Math.max(0.1, Math.min(0.9, ratio)); this.layout(); };
    let drag: { id: number; y: number; height: number } | undefined;
    this.divider.addEventListener("pointerdown", (event) => {
      if (event.button !== 0) return;
      event.preventDefault();
      drag = { id: event.pointerId, y: event.clientY, height: this.host.clientHeight };
      this.divider.setPointerCapture(event.pointerId);
      this.divider.classList.add("is-dragging");
    }, { signal });
    this.divider.addEventListener("pointermove", (event) => {
      if (drag) set((drag.height + drag.y - event.clientY) / this.availableHeight());
    }, { signal });
    const finish = () => {
      if (!drag) return;
      const id = drag.id;
      drag = undefined;
      if (this.divider.hasPointerCapture(id)) this.divider.releasePointerCapture(id);
      this.divider.classList.remove("is-dragging");
      this.options.saveSize();
    };
    this.divider.addEventListener("pointerup", finish, { signal });
    this.divider.addEventListener("pointercancel", finish, { signal });
    this.divider.addEventListener("lostpointercapture", finish, { signal });
    this.divider.addEventListener("dblclick", () => { set(defaultRatio); this.options.saveSize(); }, { signal });
    this.divider.addEventListener("keydown", (event) => {
      if (!["ArrowUp", "ArrowDown", "Home", "End"].includes(event.key)) return;
      event.preventDefault();
      set(event.key === "Home" ? 0.1 : event.key === "End" ? 0.9 : this.ratio + (event.key === "ArrowUp" ? 0.05 : -0.05));
      this.options.saveSize();
    }, { signal });
  }

  private scheduleRender(): void {
    if (!this.opened || this.renderFrame || this.abort.signal.aborted) return;
    this.renderFrame = requestAnimationFrame(() => { this.renderFrame = 0; this.render(); });
  }

  private render(): void {
    const state = this.snapshot;
    this.status.textContent = state.selectionEmpty ? "Select files or folders to view their outline."
      : state.loading ? `Loading outline… ${state.completed} file${state.completed === 1 ? "" : "s"} processed`
      : !state.files.length ? "No files found." : "";
    this.status.hidden = !this.status.textContent;
    this.scroller.setAttribute("aria-busy", String(state.loading));
    this.rows = [];
    const appendSymbols = (file: OutlineFile, symbols: OutlineSymbol[], depth: number, parent?: string) => {
      for (const symbol of symbols) {
        const key = `${parent || refKey(file.ref)}:${JSON.stringify([symbol.name, symbol.kind, symbol.range])}`;
        const expanded = symbol.children.length ? this.expandedSymbols.has(key) : undefined;
        this.rows.push({ key, parent, depth, label: symbol.name, detail: symbol.detail, icon: outlineSymbolIcon(symbol.kind), expanded, file, symbol });
        if (expanded) appendSymbols(file, symbol.children, depth + 1, key);
      }
    };
    const files = [...state.files].sort((a, b) => a.label.localeCompare(b.label, undefined, { numeric: true, sensitivity: "base" }) || refKey(a.ref).localeCompare(refKey(b.ref)));
    for (const file of files) {
      const key = refKey(file.ref);
      if (state.grouped) {
        const expanded = !this.collapsedFiles.has(key);
        this.rows.push({ key, depth: 0, label: file.label, icon: "file-code", expanded, file });
        if (!expanded) continue;
      }
      const depth = state.grouped ? 1 : 0;
      if (file.symbols.length) appendSymbols(file, file.symbols, depth, state.grouped ? key : undefined);
      else this.rows.push({ key: `${key}:status`, parent: state.grouped ? key : undefined, depth, file, message: true,
        label: file.status === "loading" ? "Loading symbols…" : file.message || "No symbols found.",
        icon: file.status === "loading" ? "loading codicon-modifier-spin" : file.status === "error" ? "warning" : "info" });
    }
    if (!this.rows.some((row) => row.key === this.focusedKey)) this.focusedKey = this.rows[0]?.key || null;
    this.canvas.style.height = `${this.rows.length * rowHeight}px`;
    this.renderWindow();
  }

  private renderWindow(): void {
    if (!this.opened) return;
    const start = Math.max(0, Math.floor(this.scroller.scrollTop / rowHeight) - 5);
    const end = Math.min(this.rows.length, Math.ceil((this.scroller.scrollTop + this.scroller.clientHeight) / rowHeight) + 5);
    this.canvas.innerHTML = this.rows.slice(start, end).map((row, offset) => {
      const index = start + offset;
      const selected = row.key === this.focusedKey;
      return `<div id="code-outline-row-${index}" class="code-outline-row${selected ? " is-selected" : ""}${row.message ? " is-message" : ""}" role="treeitem"
        aria-level="${row.depth + 1}" aria-selected="${selected}"${row.expanded === undefined ? "" : ` aria-expanded="${row.expanded}"`}
        data-outline-row="${escapeHTML(row.key)}" title="${escapeHTML([row.label, row.detail].filter(Boolean).join(" — "))}"
        style="top:${index * rowHeight}px;padding-left:${6 + row.depth * 14}px">
        <span aria-hidden="true" class="code-outline-chevron${row.expanded === undefined ? "" : ` codicon codicon-chevron-${row.expanded ? "down" : "right"}`}"${row.expanded === undefined ? "" : " data-outline-chevron"}></span>
        <span class="codicon codicon-${row.icon}" aria-hidden="true"></span><span class="code-outline-name">${escapeHTML(row.label)}</span>
        ${row.detail ? `<span class="code-outline-detail">${escapeHTML(row.detail)}</span>` : ""}</div>`;
    }).join("");
    const focus = this.rows.findIndex((row) => row.key === this.focusedKey);
    if (focus >= start && focus < end) this.scroller.setAttribute("aria-activedescendant", `code-outline-row-${focus}`);
    else this.scroller.removeAttribute("aria-activedescendant");
  }

  private toggleRow(row: Row): void {
    if (row.expanded === undefined) return;
    const keys = row.symbol ? this.expandedSymbols : this.collapsedFiles;
    if (keys.has(row.key)) keys.delete(row.key); else keys.add(row.key);
    this.render();
  }

  private navigate(row: Row): void {
    if (row.symbol) void this.options.navigate(row.file.ref, row.symbol.selectionRange).catch((error) => {
      this.status.textContent = error instanceof Error ? error.message : String(error);
      this.status.hidden = false;
    });
  }

  private keydown(event: KeyboardEvent): void {
    if (event.altKey || event.ctrlKey || event.metaKey || !this.rows.length) return;
    let index = Math.max(0, this.rows.findIndex((row) => row.key === this.focusedKey));
    const row = this.rows[index];
    switch (event.key) {
      case "ArrowDown": index = Math.min(this.rows.length - 1, index + 1); break;
      case "ArrowUp": index = Math.max(0, index - 1); break;
      case "Home": index = 0; break;
      case "End": index = this.rows.length - 1; break;
      case "ArrowRight":
        if (row.expanded === false) this.toggleRow(row);
        else if (row.expanded) index = Math.min(this.rows.length - 1, index + 1);
        break;
      case "ArrowLeft":
        if (row.expanded) this.toggleRow(row);
        else if (row.parent) index = Math.max(0, this.rows.findIndex((item) => item.key === row.parent));
        break;
      case "Enter": if (row.symbol) this.navigate(row); else this.toggleRow(row); break;
      case " ": this.toggleRow(row); break;
      default: return;
    }
    event.preventDefault();
    event.stopPropagation();
    this.focusedKey = this.rows[index]?.key || null;
    if (index * rowHeight < this.scroller.scrollTop) this.scroller.scrollTop = index * rowHeight;
    else if ((index + 1) * rowHeight > this.scroller.scrollTop + this.scroller.clientHeight) this.scroller.scrollTop = (index + 1) * rowHeight - this.scroller.clientHeight;
    this.renderWindow();
  }

  dispose(): void {
    this.controller.dispose();
    this.abort.abort();
    this.resize.disconnect();
    if (this.renderFrame) cancelAnimationFrame(this.renderFrame);
  }
}
