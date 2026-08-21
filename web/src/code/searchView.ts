import * as editorAPI from "./editorApi";
import type { APIError } from "./editorApi";
import type {
  FileRef, TextReplaceTarget, TextReplaceUpdate, TextSearchFileResult, TextSearchMatch,
  TextSearchOverlay, TextSearchRequest, TextSearchResponse,
} from "./types";
import { refKey } from "./types";
import { escapeHTML, toast } from "./ui";

type ReplaceConfirmation = {
  scope: "match" | "file" | "all";
  matches: number;
  files: number;
  dirtyFiles: number;
};

const initialRenderedMatches = 200;
const additionalRenderedMatches = 200;

export type SearchViewOptions = {
  workspaceId: string;
  signal: AbortSignal;
  getOverlays(): TextSearchOverlay[];
  openResult(ref: FileRef, match: TextSearchMatch, pin: boolean): void | Promise<void>;
  confirmReplace(details: ReplaceConfirmation): Promise<boolean>;
  applyUpdates(updates: TextReplaceUpdate[]): void | Promise<void>;
  focusEditor(): void;
};

export class SearchView {
  private readonly host: HTMLElement;
  private readonly options: SearchViewOptions;
  private queryInput!: HTMLInputElement;
  private replaceInput!: HTMLInputElement;
  private includeInput!: HTMLInputElement;
  private excludeInput!: HTMLInputElement;
  private resultsHost!: HTMLElement;
  private summary!: HTMLElement;
  private detailsOpen = false;
  private replaceOpen = false;
  private caseSensitive = false;
  private wholeWord = false;
  private regex = false;
  private response: TextSearchResponse | null = null;
  private lastRequest: TextSearchRequest | null = null;
  private collapsedFiles = new Set<string>();
  private requestController: AbortController | null = null;
  private debounceTimer = 0;
  private indexingTimer = 0;
  private generation = 0;
  private replacing = false;
  private renderedMatchLimit = initialRenderedMatches;

  constructor(host: HTMLElement, options: SearchViewOptions) {
    this.host = host;
    this.options = options;
    this.render();
    options.signal.addEventListener("abort", () => this.dispose(), { once: true });
  }

  open(options: { replace?: boolean; seed?: string } = {}): void {
    if (options.replace) this.setReplaceOpen(true);
    if (!this.queryInput.value && options.seed) {
      this.queryInput.value = options.seed.replace(/[\r\n]+/g, " ");
      this.scheduleSearch(0);
    }
    requestAnimationFrame(() => {
      this.queryInput.focus();
      this.queryInput.select();
    });
  }

  refresh(): void {
    this.scheduleSearch(0);
  }

  toggleDetails(): void {
    this.detailsOpen = !this.detailsOpen;
    this.syncControls();
    if (this.detailsOpen) requestAnimationFrame(() => this.includeInput.focus());
  }

  navigateResult(direction: number): void {
    const rows = [...this.resultsHost.querySelectorAll<HTMLButtonElement>("[data-search-result]")];
    if (!rows.length) return;
    const active = rows.indexOf(document.activeElement as HTMLButtonElement);
    const next = rows[(active + direction + rows.length) % rows.length];
    next?.focus();
    next?.click();
  }

  private render(): void {
    this.host.innerHTML = `
      <header class="code-explorer-header">
        <span>SEARCH</span>
        <div class="code-header-actions">
          <button type="button" title="Refresh Search" aria-label="Refresh Search" data-search-action="refresh"><span class="codicon codicon-refresh"></span></button>
          <button type="button" title="Clear Search Results" aria-label="Clear Search Results" data-search-action="clear"><span class="codicon codicon-clear-all"></span></button>
        </div>
      </header>
      <section class="code-search-controls" aria-label="Workspace search controls">
        <div class="code-search-row">
          <button type="button" class="code-search-expand" title="Toggle Replace" aria-label="Toggle Replace" aria-expanded="false" data-search-action="toggle-replace"><span class="codicon codicon-chevron-right"></span></button>
          <div class="code-search-input-wrap">
            <input type="text" aria-label="Search workspace" placeholder="Search" autocomplete="off" spellcheck="false" data-search-query>
            <div class="code-search-toggles">
              <button type="button" title="Match Case (Alt+C)" aria-label="Match Case" aria-pressed="false" data-search-toggle="case">Aa</button>
              <button type="button" title="Match Whole Word (Alt+W)" aria-label="Match Whole Word" aria-pressed="false" data-search-toggle="word"><span class="codicon codicon-whole-word"></span></button>
              <button type="button" title="Use Regular Expression (Alt+R)" aria-label="Use Regular Expression" aria-pressed="false" data-search-toggle="regex"><span class="codicon codicon-regex"></span></button>
            </div>
          </div>
        </div>
        <div class="code-search-row code-search-replace-row" data-search-replace-row hidden>
          <span class="code-search-row-spacer"></span>
          <div class="code-search-input-wrap">
            <input type="text" aria-label="Replace in workspace" placeholder="Replace" autocomplete="off" spellcheck="false" data-search-replacement>
            <button type="button" class="code-search-inline-action" title="Replace All" aria-label="Replace All" data-search-action="replace-all"><span class="codicon codicon-replace-all"></span></button>
          </div>
        </div>
        <button type="button" class="code-search-details-toggle" title="Toggle Search Details" aria-label="Toggle Search Details" aria-expanded="false" data-search-action="toggle-details"><span class="codicon codicon-ellipsis"></span></button>
        <div class="code-search-details" data-search-details hidden>
          <label><span>files to include</span><input type="text" aria-label="Files to include" placeholder="e.g. *.go, src/**" spellcheck="false" data-search-include></label>
          <label><span>files to exclude</span><input type="text" aria-label="Files to exclude" placeholder="e.g. **/generated/**" spellcheck="false" data-search-exclude></label>
        </div>
      </section>
      <div class="code-search-summary" aria-live="polite" data-search-summary>Enter text to search the workspace.</div>
      <div class="code-search-results" role="tree" aria-label="Workspace search results" data-search-results></div>
    `;
    this.queryInput = this.host.querySelector<HTMLInputElement>("[data-search-query]")!;
    this.replaceInput = this.host.querySelector<HTMLInputElement>("[data-search-replacement]")!;
    this.includeInput = this.host.querySelector<HTMLInputElement>("[data-search-include]")!;
    this.excludeInput = this.host.querySelector<HTMLInputElement>("[data-search-exclude]")!;
    this.resultsHost = this.host.querySelector<HTMLElement>("[data-search-results]")!;
    this.summary = this.host.querySelector<HTMLElement>("[data-search-summary]")!;
    this.installEvents();
    this.syncControls();
  }

  private installEvents(): void {
    const signal = this.options.signal;
    const searchInputChanged = () => this.scheduleSearch(180);
    this.queryInput.addEventListener("input", searchInputChanged, { signal });
    this.replaceInput.addEventListener("input", searchInputChanged, { signal });
    this.includeInput.addEventListener("input", searchInputChanged, { signal });
    this.excludeInput.addEventListener("input", searchInputChanged, { signal });
    this.host.addEventListener("click", (event) => this.handleClick(event), { signal });
    this.host.addEventListener("dblclick", (event) => {
      const row = (event.target as Element).closest<HTMLElement>("[data-search-result]");
      if (row) void this.openResult(row, true);
    }, { signal });
    this.host.addEventListener("keydown", (event) => {
      if (event.key === "Enter" && event.target instanceof HTMLInputElement) {
        event.preventDefault();
        this.scheduleSearch(0);
      } else if (event.key === "Escape" && event.target instanceof HTMLInputElement) {
        event.preventDefault();
        this.options.focusEditor();
      } else if (event.altKey && event.key.toLowerCase() === "c") {
        event.preventDefault();
        this.toggle("case");
      } else if (event.altKey && event.key.toLowerCase() === "w") {
        event.preventDefault();
        this.toggle("word");
      } else if (event.altKey && event.key.toLowerCase() === "r") {
        event.preventDefault();
        this.toggle("regex");
      }
    }, { signal });
  }

  private handleClick(event: Event): void {
    const target = event.target as Element;
    const action = target.closest<HTMLElement>("[data-search-action]")?.dataset.searchAction;
    if (action === "refresh") this.refresh();
    else if (action === "clear") this.clear();
    else if (action === "toggle-replace") this.setReplaceOpen(!this.replaceOpen);
    else if (action === "toggle-details") this.toggleDetails();
    else if (action === "replace-all") void this.replace("all");
    else if (action === "show-more") {
      this.renderedMatchLimit += additionalRenderedMatches;
      this.renderResults();
    }

    const toggle = target.closest<HTMLElement>("[data-search-toggle]")?.dataset.searchToggle;
    if (toggle) this.toggle(toggle);
    const fileHeader = target.closest<HTMLElement>("[data-search-file]");
    if (fileHeader && !target.closest("[data-search-file-replace]")) {
      const key = fileHeader.dataset.searchFile || "";
      if (this.collapsedFiles.has(key)) this.collapsedFiles.delete(key);
      else this.collapsedFiles.add(key);
      this.renderResults();
    }
    const fileReplace = target.closest<HTMLElement>("[data-search-file-replace]");
    if (fileReplace) void this.replace("file", Number(fileReplace.dataset.fileIndex));
    const matchReplace = target.closest<HTMLElement>("[data-search-match-replace]");
    if (matchReplace) void this.replace("match", Number(matchReplace.dataset.fileIndex), Number(matchReplace.dataset.matchIndex));
    const result = target.closest<HTMLElement>("[data-search-result]");
    if (result && !target.closest("[data-search-match-replace]")) void this.openResult(result, false);
  }

  private toggle(kind: string): void {
    if (kind === "case") this.caseSensitive = !this.caseSensitive;
    if (kind === "word") this.wholeWord = !this.wholeWord;
    if (kind === "regex") this.regex = !this.regex;
    this.syncControls();
    this.scheduleSearch(0);
  }

  private setReplaceOpen(open: boolean): void {
    this.replaceOpen = open;
    this.syncControls();
    if (open) requestAnimationFrame(() => this.replaceInput.focus());
    this.scheduleSearch(0);
  }

  private syncControls(): void {
    const replaceRow = this.host.querySelector<HTMLElement>("[data-search-replace-row]");
    const replaceToggle = this.host.querySelector<HTMLButtonElement>("[data-search-action=toggle-replace]");
    if (replaceRow) replaceRow.hidden = !this.replaceOpen;
    if (replaceToggle) {
      replaceToggle.setAttribute("aria-expanded", String(this.replaceOpen));
      replaceToggle.querySelector(".codicon")!.className = `codicon codicon-chevron-${this.replaceOpen ? "down" : "right"}`;
    }
    const details = this.host.querySelector<HTMLElement>("[data-search-details]");
    const detailsToggle = this.host.querySelector<HTMLButtonElement>("[data-search-action=toggle-details]");
    if (details) details.hidden = !this.detailsOpen;
    detailsToggle?.setAttribute("aria-expanded", String(this.detailsOpen));
    const states: Record<string, boolean> = { case: this.caseSensitive, word: this.wholeWord, regex: this.regex };
    this.host.querySelectorAll<HTMLButtonElement>("[data-search-toggle]").forEach((button) => {
      const active = states[button.dataset.searchToggle || ""] || false;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-pressed", String(active));
    });
  }

  private currentRequest(): TextSearchRequest {
    const splitGlobs = (value: string) => value.split(",").map((item) => item.trim()).filter(Boolean);
    return {
      query: this.queryInput.value,
      replacement: this.replaceInput.value,
      regex: this.regex,
      caseSensitive: this.caseSensitive,
      wholeWord: this.wholeWord,
      include: splitGlobs(this.includeInput.value),
      exclude: splitGlobs(this.excludeInput.value),
      overlays: this.options.getOverlays(),
    };
  }

  private scheduleSearch(delay: number): void {
    window.clearTimeout(this.debounceTimer);
    window.clearTimeout(this.indexingTimer);
    this.requestController?.abort();
    this.requestController = null;
    this.generation++;
    if (!this.queryInput.value) {
      this.response = null;
      this.lastRequest = null;
      this.summary.textContent = "Enter text to search the workspace.";
      this.resultsHost.innerHTML = "";
      this.host.classList.remove("is-searching");
      return;
    }
    this.debounceTimer = window.setTimeout(() => void this.search(), delay);
  }

  private async search(): Promise<void> {
    const request = this.currentRequest();
    if (!request.query || this.replacing) return;
    const generation = ++this.generation;
    this.requestController?.abort();
    const controller = new AbortController();
    this.requestController = controller;
    this.summary.textContent = "Searching…";
    this.host.classList.add("is-searching");
    try {
      const response = await editorAPI.searchText(this.options.workspaceId, request, controller.signal);
      if (controller.signal.aborted || generation !== this.generation) return;
      this.response = response;
      this.lastRequest = request;
      this.renderedMatchLimit = initialRenderedMatches;
      this.renderResults();
      if (response.indexing) this.indexingTimer = window.setTimeout(() => void this.search(), 450);
    } catch (error) {
      if (controller.signal.aborted || generation !== this.generation) return;
      this.response = null;
      this.resultsHost.innerHTML = "";
      this.summary.textContent = error instanceof Error ? error.message : String(error);
      this.summary.classList.add("is-error");
    } finally {
      if (generation === this.generation) this.host.classList.remove("is-searching");
    }
  }

  private renderResults(): void {
    const response = this.response;
    this.summary.classList.remove("is-error");
    if (!response) {
      this.resultsHost.innerHTML = "";
      return;
    }
    const visibleFiles: Array<{ file: TextSearchFileResult; index: number; matchCount: number }> = [];
    let displayedMatches = 0;
    for (let index = 0; index < response.files.length && displayedMatches < this.renderedMatchLimit; index++) {
      const file = response.files[index];
      const matchCount = Math.min(file.matches.length, this.renderedMatchLimit - displayedMatches);
      if (!matchCount) continue;
      visibleFiles.push({ file, index, matchCount });
      displayedMatches += matchCount;
    }
    const suffix = response.truncated ? " (results truncated)" : response.indexing ? " (indexing…)" : "";
    const displaySuffix = displayedMatches < response.matchCount
      ? ` (showing first ${displayedMatches.toLocaleString()})`
      : "";
    this.summary.textContent = response.matchCount
      ? `${response.matchCount.toLocaleString()} result${response.matchCount === 1 ? "" : "s"} in ${response.files.length.toLocaleString()} file${response.files.length === 1 ? "" : "s"}${suffix}${displaySuffix}`
      : `No results found${suffix}`;
    const roots = new Map<string, Array<{ file: TextSearchFileResult; index: number; matchCount: number }>>();
    visibleFiles.forEach(({ file, index, matchCount }) => {
      const slash = file.referencePath.indexOf("/");
      const root = slash < 0 ? file.referencePath : file.referencePath.slice(0, slash);
      const items = roots.get(root) || [];
      items.push({ file, index, matchCount });
      roots.set(root, items);
    });
    const moreResults = response.matchCount - displayedMatches;
    this.resultsHost.innerHTML = [...roots.entries()].map(([root, files]) => `
      <section class="code-search-root" role="group" aria-label="${escapeHTML(root)}">
        ${roots.size > 1 ? `<div class="code-search-root-label">${escapeHTML(root)}</div>` : ""}
        ${files.map(({ file, index, matchCount }) => this.renderFile(file, index, matchCount)).join("")}
      </section>
    `).join("") + (moreResults > 0 ? `
      <button type="button" class="code-search-show-more" data-search-action="show-more">
        Show ${Math.min(additionalRenderedMatches, moreResults).toLocaleString()} more result${Math.min(additionalRenderedMatches, moreResults) === 1 ? "" : "s"}
        <span>${moreResults.toLocaleString()} remaining</span>
      </button>` : "");
  }

  private renderFile(file: TextSearchFileResult, fileIndex: number, matchCount: number): string {
    const key = refKey(file.ref);
    const collapsed = this.collapsedFiles.has(key);
    const slash = file.referencePath.lastIndexOf("/");
    const directory = slash < 0 ? "" : file.referencePath.slice(0, slash);
    return `
      <div class="code-search-file-group" role="treeitem" aria-expanded="${!collapsed}">
        <div class="code-search-file-header" data-search-file="${escapeHTML(key)}">
          <span class="codicon codicon-chevron-${collapsed ? "right" : "down"}"></span>
          <span class="codicon codicon-file-code code-search-file-icon"></span>
          <span class="code-search-file-name">${escapeHTML(file.name)}</span>
          <span class="code-search-file-directory" title="${escapeHTML(directory)}">${escapeHTML(directory)}</span>
          <span class="code-search-count">${file.matches.length}</span>
          ${this.replaceOpen ? `<button type="button" title="Replace All in ${escapeHTML(file.name)}" aria-label="Replace All in ${escapeHTML(file.name)}" data-search-file-replace data-file-index="${fileIndex}"><span class="codicon codicon-replace-all"></span></button>` : ""}
        </div>
        <div class="code-search-file-matches" role="group"${collapsed ? " hidden" : ""}>
          ${file.matches.slice(0, matchCount).map((match, matchIndex) => this.renderMatch(match, fileIndex, matchIndex)).join("")}
        </div>
      </div>`;
  }

  private renderMatch(match: TextSearchMatch, fileIndex: number, matchIndex: number): string {
    const before = match.preview.slice(0, match.previewMatchStart).replace(/^[\t ]+/, "");
    const highlighted = match.preview.slice(match.previewMatchStart, match.previewMatchEnd);
    const after = match.preview.slice(match.previewMatchEnd);
    const replacementPreview = match.replacementPreview.replace(/^[\t ]+/, "");
    return `<div class="code-search-match-row">
      <button type="button" class="code-search-match" role="treeitem" data-search-result data-file-index="${fileIndex}" data-match-index="${matchIndex}" aria-label="Line ${match.line}: ${escapeHTML(match.preview)}">
        <span class="code-search-line">${match.line}</span>
        <span class="code-search-preview"><span>${escapeHTML(before)}</span><mark>${escapeHTML(highlighted)}</mark><span>${escapeHTML(after)}</span>
          ${this.replaceOpen ? `<small><span class="codicon codicon-arrow-right"></span>${escapeHTML(replacementPreview)}</small>` : ""}
        </span>
      </button>
      ${this.replaceOpen ? `<button type="button" class="code-search-match-replace" title="Replace this result" aria-label="Replace result on line ${match.line}" data-search-match-replace data-file-index="${fileIndex}" data-match-index="${matchIndex}"><span class="codicon codicon-replace"></span></button>` : ""}
    </div>`;
  }

  private async openResult(element: HTMLElement, pin: boolean): Promise<void> {
    const file = this.response?.files[Number(element.dataset.fileIndex)];
    const match = file?.matches[Number(element.dataset.matchIndex)];
    if (file && match) await this.options.openResult(file.ref, match, pin);
  }

  private async replace(scope: "match" | "file" | "all", fileIndex?: number, matchIndex?: number): Promise<void> {
    if (!this.response || !this.lastRequest || this.replacing) return;
    let files = this.response.files;
    if (scope !== "all") {
      const file = this.response.files[fileIndex ?? -1];
      if (!file) return;
      files = [file];
    }
    const targets: TextReplaceTarget[] = files.map((file) => ({
      ref: file.ref, revision: file.revision, contentRevision: file.contentRevision,
      ...(scope === "match" ? { matchIds: [file.matches[matchIndex ?? -1]?.id].filter(Boolean) } : {}),
    }));
    const matches = scope === "match" ? 1 : files.reduce((total, file) => total + file.matches.length, 0);
    const dirtyFiles = files.filter((file) => file.overlay).length;
    if ((scope === "all" || dirtyFiles > 0) && !await this.options.confirmReplace({ scope, matches, files: files.length, dirtyFiles })) return;
    this.replacing = true;
    this.requestController?.abort();
    this.host.classList.add("is-replacing");
    this.summary.textContent = "Replacing…";
    try {
      const search = { ...this.currentRequest(), query: this.lastRequest.query };
      const response = await editorAPI.replaceText(this.options.workspaceId, { search, scope, targets });
      await this.options.applyUpdates(response.updated || []);
      toast(`Replaced ${matches.toLocaleString()} result${matches === 1 ? "" : "s"}.`);
      this.scheduleSearch(0);
    } catch (error) {
      const apiError = error as APIError;
      const partial = apiError.payload?.code === "replace_partial"
        ? apiError.payload.details?.updated || []
        : [];
      if (partial.length) await this.options.applyUpdates(partial);
      toast(error instanceof Error ? error.message : String(error), { sticky: true });
      this.scheduleSearch(0);
    } finally {
      this.replacing = false;
      this.host.classList.remove("is-replacing");
    }
  }

  private clear(): void {
    this.queryInput.value = "";
    this.response = null;
    this.lastRequest = null;
    this.requestController?.abort();
    this.requestController = null;
    this.generation++;
    this.host.classList.remove("is-searching");
    this.summary.textContent = "Enter text to search the workspace.";
    this.resultsHost.innerHTML = "";
    this.queryInput.focus();
  }

  private dispose(): void {
    window.clearTimeout(this.debounceTimer);
    window.clearTimeout(this.indexingTimer);
    this.requestController?.abort();
  }
}
