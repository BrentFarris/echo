import { api } from "../../js/api.js";
import { on as onSocket, onState as onSocketState, send as sendSocket } from "../../js/ws.js";
import * as sourceControlAPI from "./sourceControlApi";
import { actionKeys, updateSelection, type SelectionState } from "./sourceControlSelection";
import type {
  SourceControlActionRequest, SourceControlActionResult, SourceControlChange, SourceControlChangeGroup,
  SourceControlDiffRequest, SourceControlHistory, SourceControlMetadata, SourceControlOperationEvent, SourceControlRepository,
  SourceControlRevisionDetail, SourceControlStatus,
} from "./sourceControlTypes";
import { normalizeStatus } from "./sourceControlTypes";
import { aggregateSourceControlChangeCount } from "./sourceControlIdentity";
import { presentationFor, supports } from "./sourceControlPresentation";
import type { FileRef, WorkspaceRoot } from "./types";
import { choiceDialog, escapeHTML, promptDialog, showContextMenu, toast } from "./ui";
import { randomUUID } from "../randomUUID";

type SourceControlViewCallbacks = {
  roots(): WorkspaceRoot[];
  openFile(ref: FileRef, pin: boolean): Promise<void>;
  openDiff(repository: SourceControlRepository, target: SourceControlDiffRequest, pin: boolean): Promise<void>;
  updateBadge(count: number): void;
};

type Review = { ref: string; kind: "commit" | "stash"; detail: SourceControlRevisionDetail };
type CommitInputState = {
  repositoryId: string;
  selectionStart: number;
  selectionEnd: number;
  selectionDirection: "forward" | "backward" | "none";
  scrollTop: number;
  scrollLeft: number;
};

const rowHeight = 22;
const historyRowHeight = 38;

export class SourceControlView {
  private readonly host: HTMLElement;
  private readonly workspaceId: string;
  private readonly signal: AbortSignal;
  private readonly callbacks: SourceControlViewCallbacks;
  private repositories: SourceControlRepository[] = [];
  private statuses = new Map<string, SourceControlStatus>();
  private selections = new Map<string, SelectionState>();
  private drafts = new Map<string, string>();
  private expandedRepositories = new Set<string>();
  private expandedGroups = new Set<string>();
  private busyRepositories = new Map<string, SourceControlOperationEvent>();
  private metadata = new Map<string, SourceControlMetadata>();
  private history = new Map<string, SourceControlHistory>();
  private historyExpanded = new Set<string>();
  private reviews = new Map<string, Review>();
  private listScroll = new Map<string, number>();
  private repositoryListExpanded = true;
  private searchParents = false;
  private sidebarScroll = 0;
  private loading = true;
  private disposed = false;

  constructor(host: HTMLElement, workspaceId: string, signal: AbortSignal, callbacks: SourceControlViewCallbacks) {
    this.host = host;
    this.workspaceId = workspaceId;
    this.signal = signal;
    this.callbacks = callbacks;
    this.installEvents();
    this.installSocket();
  }

  async start(): Promise<void> {
    await this.reloadRepositories();
  }

  private installSocket(): void {
    let opened = false;
    const unsubscribeState = onSocketState((state: string) => {
      if (state !== "open") return;
      sendSocket({ type: "source_control_subscribe", workspaceId: this.workspaceId });
      if (opened) void this.reloadRepositories();
      opened = true;
    });
    const unsubscribeStatus = onSocket("source_control_status", (data: object) => {
      const event = data as { workspaceId: string; status?: SourceControlStatus };
      if (event.workspaceId !== this.workspaceId || !event.status) return;
      this.acceptStatus(normalizeStatus(event.status as Parameters<typeof normalizeStatus>[0]));
    });
    // Accept legacy Git events during the compatibility release. This view
    // subscribes only to the provider-neutral stream, so old events are not
    // duplicated on current servers.
    const unsubscribeLegacyStatus = onSocket("git_status", (data: object) => {
      const event = data as { workspaceId: string; status?: SourceControlStatus };
      if (event.workspaceId !== this.workspaceId || !event.status) return;
      this.acceptStatus(normalizeStatus(event.status as Parameters<typeof normalizeStatus>[0]));
    });
    const unsubscribeOperation = onSocket("source_control_operation", (data: object) => {
      const event = data as { workspaceId: string; operation?: SourceControlOperationEvent };
      if (event.workspaceId !== this.workspaceId || !event.operation) return;
      if (event.operation.state === "running") this.busyRepositories.set(event.operation.repositoryId, event.operation);
      else this.busyRepositories.delete(event.operation.repositoryId);
      this.render();
    });
    const unsubscribeResync = onSocket("source_control_resync_required", (data: object) => {
      const event = data as { workspaceId: string; repositoryId?: string };
      if (event.workspaceId !== this.workspaceId) return;
      if (event.repositoryId) void this.refreshStatus(event.repositoryId);
      else void this.reloadRepositories();
    });
    this.signal.addEventListener("abort", () => {
      this.disposed = true;
      unsubscribeState();
      unsubscribeStatus();
      unsubscribeLegacyStatus();
      unsubscribeOperation();
      unsubscribeResync();
      sendSocket({ type: "source_control_unsubscribe", workspaceId: this.workspaceId });
    }, { once: true });
    sendSocket({ type: "source_control_subscribe", workspaceId: this.workspaceId });
  }

  private async reloadRepositories(): Promise<void> {
    try {
      const response = await sourceControlAPI.listRepositories(this.workspaceId);
      if (this.disposed) return;
      this.repositories = response.repositories || [];
      this.searchParents = response.searchParentRepositories;
      if (this.expandedRepositories.size === 0) this.repositories.forEach((repository) => this.expandedRepositories.add(repository.id));
      this.loading = false;
      this.render();
      await Promise.allSettled(this.repositories.filter((repository) => repository.available).map((repository) => this.refreshStatus(repository.id)));
    } catch (error) {
      this.loading = false;
      this.renderError(error);
    }
  }

  private async refreshStatus(repositoryId: string): Promise<void> {
    try {
      this.acceptStatus(await sourceControlAPI.loadStatus(this.workspaceId, repositoryId));
    } catch (error) {
      if (!this.disposed) toast(error instanceof Error ? error.message : String(error));
    }
  }

  private acceptStatus(status: SourceControlStatus): void {
    if (!this.repositories.some((repository) => repository.id === status.repositoryId)) return;
    const current = this.statuses.get(status.repositoryId);
    if (current && status.revision < current.revision) return;
    this.statuses.set(status.repositoryId, status);
    if (current && sameStatusContent(current, status)) return;
    this.render();
  }

  private selectionKey(repositoryId: string, scope: string): string {
    return `${repositoryId}:${scope}`;
  }

  private selection(repositoryId: string, scope: string): SelectionState {
    return this.selections.get(this.selectionKey(repositoryId, scope)) || { selected: new Set(), anchor: null };
  }

  private changeKey(change: SourceControlChange): string {
    return `${change.groupId}:${change.path}`;
  }

  private rememberListScroll(): void {
    this.sidebarScroll = this.host.querySelector<HTMLElement>("[data-git-scroll]")?.scrollTop || 0;
    this.host.querySelectorAll<HTMLElement>("[data-git-scroll-key]").forEach((element) => {
      this.listScroll.set(element.dataset.gitScrollKey || "", element.scrollTop);
    });
  }

  private commitInputState(): CommitInputState | null {
    const input = document.activeElement;
    if (!(input instanceof HTMLTextAreaElement) || !this.host.contains(input) || !input.matches("[data-git-commit-message]")) return null;
    const repositoryId = input.closest<HTMLElement>("[data-git-repository]")?.dataset.gitRepository;
    if (!repositoryId) return null;
    return {
      repositoryId,
      selectionStart: input.selectionStart,
      selectionEnd: input.selectionEnd,
      selectionDirection: input.selectionDirection || "none",
      scrollTop: input.scrollTop,
      scrollLeft: input.scrollLeft,
    };
  }

  private restoreCommitInputState(state: CommitInputState | null): void {
    if (!state) return;
    const repository = [...this.host.querySelectorAll<HTMLElement>("[data-git-repository]")]
      .find((element) => element.dataset.gitRepository === state.repositoryId);
    const input = repository?.querySelector<HTMLTextAreaElement>("[data-git-commit-message]");
    if (!input || input.disabled) return;
    input.focus({ preventScroll: true });
    input.setSelectionRange(state.selectionStart, state.selectionEnd, state.selectionDirection);
    input.scrollTop = state.scrollTop;
    input.scrollLeft = state.scrollLeft;
  }

  private render(): void {
    if (this.disposed) return;
    const commitInputState = this.commitInputState();
    this.rememberListScroll();
    const total = aggregateSourceControlChangeCount(this.repositories, this.statuses);
    this.callbacks.updateBadge(total);
    this.host.innerHTML = `
      <header class="code-explorer-header git-view-header">
        <span>SOURCE CONTROL</span>
        <div class="code-header-actions">
          <button type="button" title="Refresh Source Control" aria-label="Refresh Source Control" data-git-global="refresh"><span class="codicon codicon-refresh"></span></button>
          <button type="button" title="More Actions" aria-label="More Source Control Actions" data-git-global="menu"><span class="codicon codicon-ellipsis"></span></button>
        </div>
      </header>
      <div class="git-sidebar-scroll" data-git-scroll>
        <section class="git-repositories-view">
          <button type="button" class="git-section-title" data-git-global="toggle-repositories" aria-expanded="${this.repositoryListExpanded}">
            <span class="codicon codicon-chevron-${this.repositoryListExpanded ? "down" : "right"}"></span><strong>REPOSITORIES</strong><span>${this.repositories.length}</span>
          </button>
          ${this.repositoryListExpanded ? `<div class="git-repository-list">
            ${this.repositories.map((repository) => this.renderRepositorySelector(repository)).join("")}
          </div>` : ""}
        </section>
        <section class="git-changes-view">
          <div class="git-section-static-title"><strong>CHANGES</strong></div>
          ${this.loading ? `<div class="git-empty"><span class="spinner"></span> Discovering repositories…</div>` : ""}
          ${!this.loading && this.repositories.length === 0 ? this.renderNoRepositories() : ""}
          ${this.repositories.map((repository) => this.renderRepository(repository)).join("")}
        </section>
      </div>
    `;
    const sidebar = this.host.querySelector<HTMLElement>("[data-git-scroll]");
    if (sidebar) sidebar.scrollTop = this.sidebarScroll;
    this.mountVirtualLists();
    this.mountVirtualHistory();
    this.restoreCommitInputState(commitInputState);
  }

  private renderError(error: unknown): void {
    const message = error instanceof Error ? error.message : String(error);
    this.host.innerHTML = `<header class="code-explorer-header"><span>SOURCE CONTROL</span></header><div class="git-empty git-error"><span class="codicon codicon-error"></span><p>${escapeHTML(message)}</p><button type="button" data-git-global="refresh">Retry</button></div>`;
  }

  private renderRepositorySelector(repository: SourceControlRepository): string {
    const status = this.statuses.get(repository.id);
    return `<button type="button" class="git-repository-selector" data-git-repository-toggle="${escapeHTML(repository.id)}" title="${escapeHTML(`${repository.providerLabel}: ${repository.label}`)}">
      <span class="codicon codicon-repo"></span><span>${escapeHTML(repository.label)}</span>
      <em class="source-control-provider">${escapeHTML(repository.providerLabel)}</em>
      ${status?.branch ? `<small><span class="codicon codicon-git-branch"></span>${escapeHTML(status.branch)}</small>` : ""}
      <b>${repository.available ? status?.totalChangeCount || 0 : "!"}</b>
    </button>`;
  }

  private renderNoRepositories(): string {
    return `<div class="git-empty"><span class="codicon codicon-source-control"></span><p>No source control repositories were found in this workspace.</p><button type="button" class="code-primary-button" data-git-global="initialize">Initialize Git Repository</button><button type="button" data-git-global="clone">Clone Git Repository</button><small>Existing Fossil checkouts are detected automatically when Fossil is available.</small></div>`;
  }

  private renderRepository(repository: SourceControlRepository): string {
    const expanded = this.expandedRepositories.has(repository.id);
    const status = this.statuses.get(repository.id);
    const operation = this.busyRepositories.get(repository.id);
    const sync = shouldShowRepositorySync(repository, status);
    const selected = status ? this.selectedChanges(repository.id, status) : [];
    const commit = status ? presentationFor(repository).commit(repository, status, selected) : null;
    const canOfferCommit = supports(repository, "commitAll") || supports(repository, "commitSelected");
    return `<article class="git-repository" data-git-repository="${escapeHTML(repository.id)}">
      <header class="git-repository-header">
        <button type="button" data-git-repository-toggle="${escapeHTML(repository.id)}" aria-expanded="${expanded}">
          <span class="codicon codicon-chevron-${expanded ? "down" : "right"}"></span><span class="codicon codicon-repo"></span>
          <strong>${escapeHTML(repository.label)}</strong><em class="source-control-provider">${escapeHTML(repository.providerLabel)}</em>${repository.parent ? `<em title="Parent repository">parent</em>` : ""}
        </button>
        <div class="git-repository-actions">
          ${status?.branch ? `<span class="git-branch-label" title="${escapeHTML(status.upstream || "No upstream")}"><span class="codicon codicon-git-branch"></span>${escapeHTML(status.branch)}${status.ahead || status.behind ? ` ↑${status.ahead} ↓${status.behind}` : ""}</span>` : ""}
          ${operation ? `<span class="spinner" title="${escapeHTML(operation.action)}"></span>` : ""}
          <button type="button" title="Refresh" data-git-repo-action="refresh"><span class="codicon codicon-refresh"></span></button>
          ${canOfferCommit || sync ? `<button type="button" title="${sync ? "Sync pending commits" : escapeHTML(commit?.title || "Commit changes")}" data-git-repo-action="${sync ? "sync" : commit?.action || "commit"}" ${operation || !repository.available || (!sync && !commit?.enabled) ? "disabled" : ""} class="${operation?.action === "sync" ? "is-syncing" : ""}><span class="codicon codicon-${sync ? "sync" : "check"}"></span></button>` : ""}
          <button type="button" title="More Actions" data-git-repo-action="menu"><span class="codicon codicon-ellipsis"></span></button>
        </div>
      </header>
      ${expanded ? !repository.available ? `<div class="git-warning"><span class="codicon codicon-warning"></span>${escapeHTML(repository.diagnostic || `${repository.providerLabel} is unavailable`)}</div>` : this.renderRepositoryBody(repository, status) : ""}
    </article>`;
  }

  private renderRepositoryBody(repository: SourceControlRepository, status: SourceControlStatus | undefined): string {
    if (!status) return `<div class="git-empty compact"><span class="spinner"></span> Loading changes…</div>`;
    const draft = this.drafts.get(repository.id) || "";
    const busy = this.busyRepositories.has(repository.id);
    const operation = this.busyRepositories.get(repository.id);
    const sync = shouldShowRepositorySync(repository, status);
    const selected = this.selectedChanges(repository.id, status);
    const commit = presentationFor(repository).commit(repository, status, selected);
    const canOfferCommit = supports(repository, "commitAll") || supports(repository, "commitSelected");
    return `<div class="git-repository-body">
      ${canOfferCommit ? `<label class="git-commit-input"><span class="sr-only">Commit message</span><textarea rows="2" data-git-commit-message placeholder="${escapeHTML(commit.placeholder)}" ${busy ? "disabled" : ""}>${escapeHTML(draft)}</textarea></label>
      <button type="button" class="git-commit-button ${busy && operation?.action === "sync" ? "is-syncing" : ""}" data-git-repo-action="${sync ? "sync" : commit.action}" ${busy || (!sync && (!draft.trim() || !commit.enabled)) ? "disabled" : ""}><span class="codicon codicon-${sync ? "sync" : "check"}"></span> ${sync ? "Sync" : escapeHTML(commit.label)}</button>` : ""}
      ${status.hiddenChangeCount ? `<div class="git-warning"><span class="codicon codicon-warning"></span>${status.hiddenChangeCount} tracked change${status.hiddenChangeCount === 1 ? " is" : "s are"} outside this workspace; Commit All is blocked.</div>` : ""}
      ${status.truncated ? `<div class="git-warning"><span class="codicon codicon-warning"></span>Showing the first 10,000 changed files.</div>` : ""}
      ${status.groups.map((group) => this.renderGroup(repository, group, this.groupChanges(status, group))).join("")}
      ${status.totalChangeCount === 0 ? `<div class="git-empty compact"><span class="codicon codicon-check-all"></span>No changes</div>` : ""}
      ${supports(repository, "history") ? this.renderHistory(repository) : ""}
    </div>`;
  }

  private renderGroup(repository: SourceControlRepository, group: SourceControlChangeGroup, changes: SourceControlChange[]): string {
    if (changes.length === 0) return "";
    const scope = changes[0]?.scope || group.id;
    const key = `${repository.id}:${scope}`;
    if (!this.expandedGroups.has(key)) this.expandedGroups.add(key);
    const expanded = this.expandedGroups.has(key);
    const groupAction = presentationFor(repository).groupAction(repository, group);
    const busy = this.busyRepositories.has(repository.id);
    return `<section class="git-change-group" data-git-group="${escapeHTML(scope)}">
      <header>
        <button type="button" data-git-group-toggle="${escapeHTML(scope)}" aria-expanded="${expanded}"><span class="codicon codicon-chevron-${expanded ? "down" : "right"}"></span><span>${escapeHTML(group.label)}</span><b>${changes.length}</b></button>
        ${groupAction ? `<button type="button" title="${escapeHTML(groupAction.label)}" aria-label="${escapeHTML(groupAction.label)}" data-git-group-action="${escapeHTML(groupAction.action)}" ${busy ? "disabled" : ""}><span class="codicon codicon-${escapeHTML(groupAction.icon)}"></span></button>` : ""}
      </header>
      ${expanded ? `<div class="git-change-list" data-git-scroll-key="${escapeHTML(key)}" data-git-list-key="${escapeHTML(key)}" data-git-list-repository="${escapeHTML(repository.id)}" data-git-list-scope="${escapeHTML(scope)}" role="listbox" aria-multiselectable="true"><div class="git-change-canvas"></div></div>` : ""}
    </section>`;
  }

  private groupChanges(status: SourceControlStatus, group: SourceControlChangeGroup): SourceControlChange[] {
    return [...status.conflicts, ...status.staged, ...status.unstaged].filter((change) => change.groupId === group.id);
  }

  private selectedChanges(repositoryId: string, status: SourceControlStatus): SourceControlChange[] {
    const changes = [...status.conflicts, ...status.staged, ...status.unstaged];
    return changes.filter((change) => this.selection(repositoryId, change.scope).selected.has(this.changeKey(change)));
  }

  private renderHistory(repository: SourceControlRepository): string {
    const expanded = this.historyExpanded.has(repository.id);
    const history = this.history.get(repository.id);
    const review = this.reviews.get(repository.id);
    return `<section class="git-history">
      <header><button type="button" data-git-history-toggle aria-expanded="${expanded}"><span class="codicon codicon-chevron-${expanded ? "down" : "right"}"></span><span class="codicon codicon-git-commit"></span><strong>HISTORY</strong></button></header>
      ${expanded ? `<div class="git-history-list">
        ${!history ? `<div class="git-empty compact"><span class="spinner"></span> Loading history…</div>` : history.commits.length ? `<div class="git-history-viewport" data-git-scroll-key="${escapeHTML(`${repository.id}:history`)}" data-git-history-repository="${escapeHTML(repository.id)}"><div class="git-history-canvas"></div></div>` : `<div class="git-empty compact">No commits yet</div>`}
        ${history?.hasMore ? `<button type="button" class="git-load-more" data-git-history-more>Load more</button>` : ""}
        ${review ? this.renderReview(repository, review) : ""}
      </div>` : ""}
    </section>`;
  }

  private renderReview(repository: SourceControlRepository, review: Review): string {
    return `<div class="git-review"><header><strong>${review.kind === "stash" ? "Stash" : "Commit"} ${escapeHTML(shortHash(review.ref))}</strong><button type="button" data-git-review-close aria-label="Close review"><span class="codicon codicon-close"></span></button></header>
      ${review.detail.files.length ? review.detail.files.map((file, index) => `<button type="button" data-git-review-file="${index}"><span class="codicon codicon-file-code"></span><span>${escapeHTML(file.path)}</span><b>${escapeHTML(file.status)}</b></button>`).join("") : `<div class="git-empty compact">No changed files</div>`}
    </div>`;
  }

  private mountVirtualLists(): void {
    this.host.querySelectorAll<HTMLElement>("[data-git-list-key]").forEach((list) => {
      const repositoryId = list.dataset.gitListRepository || "";
      const scope = list.dataset.gitListScope || "";
      const status = this.statuses.get(repositoryId);
      const changes = status ? this.changesForScope(status, scope) : undefined;
      if (!changes) return;
      const key = list.dataset.gitListKey || "";
      const canvas = list.querySelector<HTMLElement>(".git-change-canvas")!;
      canvas.style.height = `${changes.length * rowHeight}px`;
      list.style.height = `${Math.min(changes.length * rowHeight, 286)}px`;
      list.scrollTop = Math.min(this.listScroll.get(key) || 0, Math.max(0, changes.length * rowHeight - list.clientHeight));
      const renderWindow = () => {
        const start = Math.max(0, Math.floor(list.scrollTop / rowHeight) - 6);
        const end = Math.min(changes.length, start + Math.ceil(list.clientHeight / rowHeight) + 12);
        canvas.innerHTML = changes.slice(start, end).map((change, offset) => this.renderChangeRow(repositoryId, scope, change, start + offset)).join("");
      };
      list.addEventListener("scroll", renderWindow, { signal: this.signal, passive: true });
      renderWindow();
    });
  }

  private mountVirtualHistory(): void {
    this.host.querySelectorAll<HTMLElement>("[data-git-history-repository]").forEach((list) => {
      const repositoryId = list.dataset.gitHistoryRepository || "";
      const commits = this.history.get(repositoryId)?.commits || [];
      const key = `${repositoryId}:history`;
      const canvas = list.querySelector<HTMLElement>(".git-history-canvas")!;
      canvas.style.height = `${commits.length * historyRowHeight}px`;
      list.style.height = `${Math.min(commits.length * historyRowHeight, 380)}px`;
      list.scrollTop = Math.min(this.listScroll.get(key) || 0, Math.max(0, commits.length * historyRowHeight - list.clientHeight));
      const renderWindow = () => {
        const start = Math.max(0, Math.floor(list.scrollTop / historyRowHeight) - 5);
        const end = Math.min(commits.length, start + Math.ceil(list.clientHeight / historyRowHeight) + 10);
        canvas.innerHTML = commits.slice(start, end).map((commit, offset) => this.renderHistoryRow(commit, start + offset)).join("");
      };
      list.addEventListener("scroll", renderWindow, { signal: this.signal, passive: true });
      renderWindow();
    });
  }

  private renderHistoryRow(commit: SourceControlHistory["commits"][number], index: number): string {
    return `<button type="button" class="git-history-row" style="transform:translateY(${index * historyRowHeight}px)" data-git-commit="${escapeHTML(commit.hash)}" title="${escapeHTML(commit.hash)}"><span class="git-graph-node"></span><span><strong>${escapeHTML(commit.subject)}</strong><small>${escapeHTML(commit.author)} · ${escapeHTML(formatDate(commit.authoredAt))}</small></span>${commit.refs.slice(0, 2).map((ref) => `<em>${escapeHTML(ref)}</em>`).join("")}</button>`;
  }

  private renderChangeRow(repositoryId: string, scope: string, change: SourceControlChange, index: number): string {
    const key = this.changeKey(change);
    const selected = this.selection(repositoryId, scope).selected.has(key);
    const filename = change.path.split("/").pop() || change.path;
    const directory = change.path.includes("/") ? change.path.slice(0, change.path.lastIndexOf("/")) : "";
    const busy = this.busyRepositories.has(repositoryId);
    const repository = this.repositories.find((candidate) => candidate.id === repositoryId);
    const group = this.statuses.get(repositoryId)?.groups.find((candidate) => candidate.id === change.groupId);
    const changeAction = repository && group ? presentationFor(repository).changeAction(repository, group, change) : null;
    const canDiscard = group?.actions.includes("discard") ?? false;
    return `<div class="git-change-row ${selected ? "is-selected" : ""}" style="transform:translateY(${index * rowHeight}px)" role="option" aria-selected="${selected}" tabindex="${selected ? 0 : -1}" data-git-change-index="${index}">
      <span class="codicon codicon-${change.submodule ? "file-submodule" : "file-code"}"></span><span class="git-change-name">${escapeHTML(filename)}</span><small>${escapeHTML(directory)}</small>
      <div class="git-change-actions">
        ${scope !== "staged" ? `<button type="button" title="Open File" aria-label="Open File" data-git-file-action="open"><span class="codicon codicon-go-to-file"></span></button>` : ""}
        ${canDiscard ? `<button type="button" title="Revert Changes" aria-label="Revert Changes" data-git-file-action="discard" ${busy ? "disabled" : ""}><span class="codicon codicon-discard"></span></button>` : ""}
        ${changeAction ? `<button type="button" title="${changeAction.label}" aria-label="${changeAction.label}" data-git-file-action="${changeAction.action}" ${busy ? "disabled" : ""}><span class="codicon codicon-${changeAction.icon}"></span></button>` : ""}
      </div>
      <b class="git-status-code git-status-${statusClass(change.statusCode)}" title="${escapeHTML(change.status)}">${escapeHTML(change.statusCode)}</b>
    </div>`;
  }

  private changesForScope(status: SourceControlStatus, scope: string): SourceControlChange[] {
    return [...status.conflicts, ...status.staged, ...status.unstaged].filter((change) => change.scope === scope);
  }

  private installEvents(): void {
    this.host.addEventListener("input", (event) => {
      const input = (event.target as Element).closest<HTMLTextAreaElement>("[data-git-commit-message]");
      const repository = input?.closest<HTMLElement>("[data-git-repository]");
      if (input && repository) {
        const repositoryId = repository.dataset.gitRepository || "";
        this.drafts.set(repositoryId, input.value);
        const button = repository.querySelector<HTMLButtonElement>(".git-commit-button");
        const status = this.statuses.get(repositoryId);
        const repositoryModel = this.repositories.find((candidate) => candidate.id === repositoryId);
        const sync = repositoryModel ? shouldShowRepositorySync(repositoryModel, status) : false;
        if (button) button.disabled = this.busyRepositories.has(repositoryId) || (!sync && (!input.value.trim() || !repositoryModel || !status || !this.canCommit(repositoryModel, status)));
      }
    }, { signal: this.signal });
    this.host.addEventListener("keydown", (event) => {
      const input = (event.target as Element).closest<HTMLTextAreaElement>("[data-git-commit-message]");
      if (input && event.key === "Enter" && (event.ctrlKey || event.metaKey)) {
        event.preventDefault();
        const repositoryId = input.closest<HTMLElement>("[data-git-repository]")?.dataset.gitRepository;
        if (repositoryId) void this.runPrimaryAction(repositoryId);
      }
      const row = (event.target as Element).closest<HTMLElement>("[data-git-change-index]");
      if (row && (event.key === "Enter" || event.key === " ")) {
        event.preventDefault();
        void this.openChange(row, true);
      }
    }, { signal: this.signal });
    this.host.addEventListener("dblclick", (event) => {
      const row = (event.target as Element).closest<HTMLElement>("[data-git-change-index]");
      if (row && !(event.target as Element).closest("button")) void this.openChange(row, true);
    }, { signal: this.signal });
    this.host.addEventListener("click", (event) => void this.handleClick(event), { signal: this.signal });
  }

  private async handleClick(event: MouseEvent): Promise<void> {
    const target = event.target as Element;
    const global = target.closest<HTMLElement>("[data-git-global]");
    if (global) {
      await this.handleGlobal(global.dataset.gitGlobal || "", event);
      return;
    }
    const repositoryElement = target.closest<HTMLElement>("[data-git-repository]");
    const repositoryId = repositoryElement?.dataset.gitRepository || target.closest<HTMLElement>("[data-git-repository-toggle]")?.dataset.gitRepositoryToggle || "";
    const repository = this.repositories.find((candidate) => candidate.id === repositoryId);
    if (!repository) return;
    const repositoryToggle = target.closest<HTMLElement>("[data-git-repository-toggle]");
    if (repositoryToggle) {
      if (repositoryToggle.classList.contains("git-repository-selector")) {
        this.expandedRepositories.add(repository.id);
        this.render();
        requestAnimationFrame(() => this.host.querySelector<HTMLElement>(`[data-git-repository="${CSS.escape(repository.id)}"]`)?.scrollIntoView({ block: "start" }));
        return;
      }
      if (this.expandedRepositories.has(repository.id)) this.expandedRepositories.delete(repository.id);
      else this.expandedRepositories.add(repository.id);
      this.render();
      return;
    }
    const repoAction = target.closest<HTMLElement>("[data-git-repo-action]");
    if (repoAction) {
      const action = repoAction.dataset.gitRepoAction;
      if (action === "refresh") await this.refreshStatus(repository.id);
      if (action?.startsWith("commit_")) await this.commit(repository.id, action);
      if (action === "sync") await this.runSimple(repository, "sync");
      if (action === "menu") this.showRepositoryMenu(repository, event.clientX, event.clientY);
      return;
    }
    const groupToggle = target.closest<HTMLElement>("[data-git-group-toggle]");
    if (groupToggle) {
      const key = `${repository.id}:${groupToggle.dataset.gitGroupToggle}`;
      if (this.expandedGroups.has(key)) this.expandedGroups.delete(key); else this.expandedGroups.add(key);
      this.render();
      return;
    }
    const groupAction = target.closest<HTMLElement>("[data-git-group-action]");
    if (groupAction) {
      const action = groupAction.dataset.gitGroupAction || "";
      if (action === "track_group") {
        const scope = groupAction.closest<HTMLElement>("[data-git-group]")?.dataset.gitGroup || "";
        const status = this.statuses.get(repository.id);
        const paths = status ? this.changesForScope(status, scope).map((change) => change.path) : [];
        if (paths.length) await this.run(repository, { requestId: randomUUID(), action: "track", paths });
      } else {
        await this.run(repository, { requestId: randomUUID(), action });
      }
      return;
    }
    if (target.closest("[data-git-history-toggle]")) {
      if (this.historyExpanded.has(repository.id)) this.historyExpanded.delete(repository.id);
      else {
        this.historyExpanded.add(repository.id);
        if (!this.history.has(repository.id)) void this.loadHistory(repository.id);
      }
      this.render();
      return;
    }
    if (target.closest("[data-git-history-more]")) { await this.loadMoreHistory(repository.id); return; }
    const commit = target.closest<HTMLElement>("[data-git-commit]");
    if (commit) { await this.loadReview(repository.id, commit.dataset.gitCommit || "", "commit"); return; }
    if (target.closest("[data-git-review-close]")) { this.reviews.delete(repository.id); this.render(); return; }
    const reviewFile = target.closest<HTMLElement>("[data-git-review-file]");
    if (reviewFile) {
      const review = this.reviews.get(repository.id);
      const file = review?.detail.files[Number(reviewFile.dataset.gitReviewFile)];
      if (review && file) await this.callbacks.openDiff(repository, {
        kind: review.kind === "commit" ? "revision" : "stash",
        path: file.path, oldPath: file.oldPath, ref: review.ref,
      }, true);
      return;
    }
    const row = target.closest<HTMLElement>("[data-git-change-index]");
    if (!row) return;
    const context = this.changeContext(row);
    if (!context) return;
    const fileAction = target.closest<HTMLElement>("[data-git-file-action]");
    if (fileAction) {
      await this.handleFileAction(repository, context.scope, context.changes, context.index, fileAction.dataset.gitFileAction || "");
      return;
    }
    const ordered = context.changes.map((change) => this.changeKey(change));
    const next = updateSelection(this.selection(repository.id, context.scope), ordered, this.changeKey(context.change), {
      toggle: event.ctrlKey || event.metaKey, range: event.shiftKey,
    });
    this.selections.set(this.selectionKey(repository.id, context.scope), next);
    if (!event.ctrlKey && !event.metaKey && !event.shiftKey) await this.callbacks.openDiff(repository, {
      kind: "change", groupId: context.change.groupId, path: context.change.path,
      oldPath: context.change.oldPath, fileRef: context.change.ref,
    }, false);
    this.render();
  }

  private changeContext(row: HTMLElement): { scope: string; changes: SourceControlChange[]; change: SourceControlChange; index: number } | null {
    const list = row.closest<HTMLElement>("[data-git-list-key]");
    const repositoryId = list?.dataset.gitListRepository || "";
    const scope = list?.dataset.gitListScope || "";
    const status = this.statuses.get(repositoryId);
    const changes = status ? this.changesForScope(status, scope) : undefined;
    const index = Number(row.dataset.gitChangeIndex);
    const change = changes?.[index];
    return changes && change ? { scope, changes, change, index } : null;
  }

  private async openChange(row: HTMLElement, pin: boolean): Promise<void> {
    const context = this.changeContext(row);
    const repositoryId = row.closest<HTMLElement>("[data-git-repository]")?.dataset.gitRepository;
    const repository = this.repositories.find((candidate) => candidate.id === repositoryId);
    if (context && repository) await this.callbacks.openDiff(repository, {
      kind: "change", groupId: context.change.groupId, path: context.change.path,
      oldPath: context.change.oldPath, fileRef: context.change.ref,
    }, pin);
  }

  private async handleFileAction(repository: SourceControlRepository, scope: string, changes: SourceControlChange[], index: number, action: string): Promise<void> {
    const change = changes[index];
    if (!change) return;
    if (action === "open") {
      if (change.ref) await this.callbacks.openFile(change.ref, true);
      return;
    }
    const selectedKeys = actionKeys(this.selection(repository.id, scope), this.changeKey(change));
    const paths = changes.filter((candidate) => selectedKeys.includes(this.changeKey(candidate))).map((candidate) => candidate.path);
    if (action === "discard") {
      const answer = await choiceDialog({
        title: "Revert changes?", message: paths.length === 1 ? `Revert changes to ${paths[0]}?` : `Revert changes to ${paths.length} files?`,
        detail: "Tracked edits cannot be recovered. Untracked files will be moved to Echo Trash and can be restored.",
        choices: [{ id: "cancel", label: "Cancel" }, { id: "revert", label: "Revert", danger: true, primary: true }],
      });
      if (answer !== "revert") return;
      await this.run(repository, { requestId: randomUUID(), action: "discard", paths, confirmed: true });
      return;
    }
    await this.run(repository, { requestId: randomUUID(), action, paths });
  }

  private async commit(repositoryId: string, action: string): Promise<void> {
    const repository = this.repositories.find((candidate) => candidate.id === repositoryId);
    if (!repository) return;
    const message = (this.drafts.get(repositoryId) || "").trim();
    if (!message && !action.includes("amend")) { toast("Enter a commit message first."); return; }
    const status = this.statuses.get(repositoryId);
    const selected = status ? this.selectedChanges(repositoryId, status) : [];
    const paths = action === "commit_selected" ? selected.map((change) => change.path) : undefined;
    await this.run(repository, { requestId: randomUUID(), action, message, paths });
  }

  private async runPrimaryAction(repositoryId: string): Promise<void> {
    const repository = this.repositories.find((candidate) => candidate.id === repositoryId);
    if (!repository) return;
    const status = this.statuses.get(repositoryId);
    if (!status) return;
    if (shouldShowRepositorySync(repository, status)) {
      await this.runSimple(repository, "sync");
      return;
    }
    const commit = presentationFor(repository).commit(repository, status, this.selectedChanges(repositoryId, status));
    if (commit.enabled) await this.commit(repositoryId, commit.action);
  }

  private async run(repository: SourceControlRepository, request: SourceControlActionRequest): Promise<void> {
    const operation: SourceControlOperationEvent = { workspaceId: this.workspaceId, repositoryId: repository.id, providerId: repository.providerId, requestId: request.requestId, action: request.action, state: "running" };
    this.busyRepositories.set(repository.id, operation);
    this.render();
    try {
      const status = this.statuses.get(repository.id);
      const actionRequest = { ...request, expectedRevision: status?.revision } as SourceControlActionRequest;
      const result = await sourceControlAPI.runAction(this.workspaceId, repository.id, actionRequest);
      this.applyPrediction(repository.id, request, result);
      if (!["stage", "stage_all", "unstage", "unstage_all", "discard", "discard_all"].includes(request.action)) {
        this.metadata.delete(repository.id);
        this.history.delete(repository.id);
        if (this.historyExpanded.has(repository.id)) void this.loadHistory(repository.id);
      }
      if (request.action.startsWith("commit_")) this.drafts.delete(repository.id);
      if (result.trashIds?.length) {
        toast(`${result.trashIds.length} untracked file${result.trashIds.length === 1 ? "" : "s"} moved to Trash.`, {
          actionLabel: "Undo", action: async () => {
            await Promise.all(result.trashIds!.map((id) => api(`/api/workspaces/${encodeURIComponent(this.workspaceId)}/fs/trash/${encodeURIComponent(id)}/restore`, { method: "POST" })));
            await this.refreshStatus(repository.id);
          },
        });
      }
      this.render();
      void this.refreshStatus(repository.id);
    } catch (error) {
      toast(error instanceof Error ? error.message : String(error), { sticky: true });
      void this.refreshStatus(repository.id);
    } finally {
      this.busyRepositories.delete(repository.id);
      this.render();
    }
  }

  private applyPrediction(repositoryId: string, request: SourceControlActionRequest, result: SourceControlActionResult): void {
    const current = this.statuses.get(repositoryId);
    const repository = this.repositories.find((candidate) => candidate.id === repositoryId);
    const presentation = repository ? presentationFor(repository) : null;
    if (current && presentation?.predictStatus) {
      this.statuses.set(repositoryId, presentation.predictStatus(current, request, result));
    }
  }

  private canCommit(repository: SourceControlRepository, status: SourceControlStatus): boolean {
    return presentationFor(repository).commit(repository, status, this.selectedChanges(repository.id, status)).enabled;
  }

  private async handleGlobal(action: string, event: MouseEvent): Promise<void> {
    if (action === "refresh") { await this.reloadRepositories(); return; }
    if (action === "menu") { this.showGlobalMenu(event.clientX, event.clientY); return; }
    if (action === "toggle-repositories") { this.repositoryListExpanded = !this.repositoryListExpanded; this.render(); return; }
    if (action === "initialize") { await this.initialize(); return; }
    if (action === "clone") { await this.clone(); return; }
  }

  private showGlobalMenu(x: number, y: number): void {
    showContextMenu(x, y, [
      { label: "Refresh", icon: "refresh", run: () => this.reloadRepositories() },
      { label: "Initialize Git Repository…", icon: "repo-create", run: () => this.initialize() },
      { label: "Clone Git Repository…", icon: "repo-clone", run: () => this.clone() },
      { label: this.searchParents ? "Stop Searching Parent Repositories" : "Search Parent Repositories", icon: this.searchParents ? "check" : "blank", separatorBefore: true, run: () => this.toggleParentSearch() },
    ]);
  }

  private showRepositoryMenu(repository: SourceControlRepository, x: number, y: number): void {
    const presentation = presentationFor(repository);
    const actions: Parameters<typeof showContextMenu>[2] = [
      { label: "Refresh", icon: "refresh", run: () => this.refreshStatus(repository.id) },
    ];
    if (!repository.available) {
      showContextMenu(x, y, actions);
      return;
    }
    if (supports(repository, "commitAll") || supports(repository, "commitSelected")) {
      actions.push({ label: "Commit…", icon: "git-commit", separatorBefore: true, run: () => this.showCommitMenu(repository, x, y) });
    }
    if (supports(repository, "stage") || supports(repository, "track")) {
      actions.push({ label: "Changes…", icon: "list-selection", run: () => this.showChangesMenu(repository, x, y) });
    }
    if (supports(repository, "update") || supports(repository, "sync") || supports(repository, "pull") || supports(repository, "push")) {
      actions.push({ label: supports(repository, "update") ? "Update, Sync…" : "Pull, Push…", icon: "sync", run: () => this.showNetworkMenu(repository, x, y) });
    }
    if (supports(repository, "branches") || supports(repository, "merge")) {
      actions.push({ label: "Branch…", icon: "git-branch", run: () => void this.showBranchMenu(repository, x, y) });
    }
    if (presentation.supportsRemoteAdministration) actions.push({ label: "Remote…", icon: "remote", run: () => void this.showRemoteMenu(repository, x, y) });
    if (supports(repository, "stashes")) actions.push({ label: "Stash…", icon: "archive", run: () => void this.showStashMenu(repository, x, y) });
    if (presentation.supportsTagAdministration) actions.push({ label: "Tags…", icon: "tag", run: () => void this.showTagMenu(repository, x, y) });
    if (supports(repository, "history")) actions.push({ label: this.historyExpanded.has(repository.id) ? "Hide History" : "Show History", icon: "history", separatorBefore: true, run: () => {
        if (this.historyExpanded.has(repository.id)) this.historyExpanded.delete(repository.id);
        else { this.historyExpanded.add(repository.id); if (!this.history.has(repository.id)) void this.loadHistory(repository.id); }
        this.render();
      } });
    showContextMenu(x, y, actions);
  }

  private showCommitMenu(repository: SourceControlRepository, x: number, y: number): void {
    const status = this.statuses.get(repository.id);
    const disabled = this.busyRepositories.has(repository.id);
    const presentation = presentationFor(repository);
    if (presentation.workflow !== "git") {
      const selected = status ? this.selectedChanges(repository.id, status) : [];
      const allCommit = status ? presentation.commit(repository, status, []) : null;
      const selectedCommit = status ? presentation.commit(repository, status, selected) : null;
      const actions: Parameters<typeof showContextMenu>[2] = [];
      if (supports(repository, "commitAll")) actions.push({ label: "Commit All", icon: "git-commit", disabled: disabled || !allCommit?.enabled, run: () => this.commit(repository.id, "commit_all") });
      if (supports(repository, "commitSelected")) actions.push({ label: `Commit Selected${selected.length ? ` (${selected.length})` : ""}`, icon: "list-selection", disabled: disabled || !selectedCommit?.enabled || selected.length === 0, run: () => this.commit(repository.id, "commit_selected") });
      showContextMenu(x, y, actions);
      return;
    }
    showContextMenu(x, y, [
      { label: "Commit Staged", icon: "git-commit", disabled: disabled || !supports(repository, "commitSelected") || !status?.staged.length, run: () => this.commit(repository.id, "commit_staged") },
      { label: "Commit All", icon: "git-commit", disabled: disabled || !supports(repository, "commitAll") || !status?.unstaged.length, run: () => this.commit(repository.id, "commit_all") },
      { label: "Commit Staged (Amend)", icon: "git-commit", separatorBefore: true, disabled, run: () => this.commit(repository.id, "commit_staged_amend") },
      { label: "Commit All (Amend)", icon: "git-commit", disabled, run: () => this.commit(repository.id, "commit_all_amend") },
      { label: "Commit Staged (Signed Off)", icon: "verified", separatorBefore: true, disabled: disabled || !status?.staged.length, run: () => this.commit(repository.id, "commit_staged_signoff") },
      { label: "Commit All (Signed Off)", icon: "verified", disabled, run: () => this.commit(repository.id, "commit_all_signoff") },
      { label: "Abort Rebase", icon: "debug-restart", danger: true, separatorBefore: true, disabled: !status?.state.rebaseInProgress, run: () => this.runSimple(repository, "abort_rebase") },
    ]);
  }

  private showChangesMenu(repository: SourceControlRepository, x: number, y: number): void {
    const status = this.statuses.get(repository.id);
    if (presentationFor(repository).workflow !== "git") {
      const untracked = status?.unstaged.filter((change) => change.kind === "untracked") || [];
      const actions: Parameters<typeof showContextMenu>[2] = [];
      if (supports(repository, "track")) actions.push({ label: "Track All Files", icon: "add", disabled: untracked.length === 0, run: () => this.run(repository, { requestId: randomUUID(), action: "track", paths: untracked.map((change) => change.path) }) });
      if (status?.groups.some((group) => group.actions.includes("discard"))) actions.push({ label: "Discard All Changes", icon: "discard", danger: true, separatorBefore: actions.length > 0, disabled: !status.totalChangeCount, run: async () => {
        const choice = await choiceDialog({ title: "Discard all changes?", message: "Tracked changes will be reverted. Untracked files will be moved to Echo Trash.", choices: [{ id: "cancel", label: "Cancel" }, { id: "discard", label: "Discard All", danger: true, primary: true }] });
        if (choice === "discard") await this.run(repository, { requestId: randomUUID(), action: "discard_all", confirmed: true });
      } });
      showContextMenu(x, y, actions);
      return;
    }
    showContextMenu(x, y, [
      { label: "Stage All Changes", icon: "add", disabled: !status?.unstaged.length && !status?.conflicts.length, run: () => this.runSimple(repository, "stage_all") },
      { label: "Unstage All Changes", icon: "remove", disabled: !status?.staged.length, run: () => this.runSimple(repository, "unstage_all") },
      { label: "Discard All Changes", icon: "discard", danger: true, separatorBefore: true, disabled: !status?.unstaged.length, run: async () => {
        const choice = await choiceDialog({ title: "Discard all changes?", message: "Tracked changes cannot be recovered. Untracked files will be moved to Echo Trash.", choices: [{ id: "cancel", label: "Cancel" }, { id: "discard", label: "Discard All", danger: true, primary: true }] });
        if (choice === "discard") await this.run(repository, { requestId: randomUUID(), action: "discard_all", confirmed: true });
      } },
    ]);
  }

  private showNetworkMenu(repository: SourceControlRepository, x: number, y: number): void {
    if (presentationFor(repository).workflow !== "git") {
      const actions: Parameters<typeof showContextMenu>[2] = [];
      if (supports(repository, "sync")) actions.push({ label: "Sync", icon: "sync", run: () => this.runSimple(repository, "sync") });
      if (supports(repository, "update")) actions.push({ label: "Update Checkout", icon: "refresh", separatorBefore: actions.length > 0, run: () => this.runSimple(repository, "update") });
      if (supports(repository, "pull")) actions.push({ label: "Pull", icon: "cloud-download", run: () => this.runSimple(repository, "pull") });
      if (supports(repository, "push")) actions.push({ label: "Push", icon: "cloud-upload", run: () => this.runSimple(repository, "push") });
      showContextMenu(x, y, actions);
      return;
    }
    const actions: Parameters<typeof showContextMenu>[2] = [];
    if (supports(repository, "sync")) actions.push({ label: "Sync", icon: "sync", run: () => this.runSimple(repository, "sync") });
    if (supports(repository, "pull")) actions.push(
      { label: "Pull", icon: "cloud-download", separatorBefore: actions.length > 0, run: () => this.runSimple(repository, "pull") },
      { label: "Pull (Rebase)", icon: "git-pull-request", run: () => this.runSimple(repository, "pull_rebase") },
      { label: "Pull From…", icon: "cloud-download", run: () => this.runWithRemote(repository, "pull_from") },
    );
    if (supports(repository, "push")) actions.push(
      { label: "Push", icon: "cloud-upload", separatorBefore: actions.length > 0, run: () => this.runSimple(repository, "push") },
      { label: "Push To…", icon: "cloud-upload", run: () => this.runWithRemote(repository, "push_to") },
    );
    if (supports(repository, "pull")) actions.push(
      { label: "Fetch", icon: "remote", separatorBefore: true, run: () => this.runSimple(repository, "fetch") },
      { label: "Fetch (Prune)", icon: "remote", run: () => this.runSimple(repository, "fetch_prune") },
      { label: "Fetch All Remotes", icon: "remote", run: () => this.runSimple(repository, "fetch_all") },
    );
    showContextMenu(x, y, actions);
  }

  private async showBranchMenu(repository: SourceControlRepository, x: number, y: number): Promise<void> {
    const metadata = await this.ensureMetadata(repository.id);
    if (!metadata) return;
    if (presentationFor(repository).workflow !== "git") {
      const actions: Parameters<typeof showContextMenu>[2] = [];
      if (supports(repository, "branches")) actions.push(
        { label: "Switch To…", icon: "git-branch", run: () => this.chooseRefAction(repository, "checkout", metadata.branches.map((branch) => branch.name)) },
        { label: "Create Branch…", icon: "git-branch-create", separatorBefore: true, run: () => this.promptNameAction(repository, "create_branch", "Create Branch", "Branch name") },
        { label: "Create Branch From…", icon: "git-branch-create", run: () => this.createBranchFrom(repository, metadata) },
      );
      if (supports(repository, "merge")) actions.push({ label: "Merge Branch…", icon: "git-merge", separatorBefore: actions.length > 0, run: () => this.chooseRefAction(repository, "merge", metadata.branches.filter((branch) => !branch.current && !branch.closed).map((branch) => branch.name)) });
      showContextMenu(x, y, actions);
      return;
    }
    showContextMenu(x, y, [
      { label: "Checkout To…", icon: "git-branch", run: () => this.chooseRefAction(repository, "checkout", [...metadata.branches, ...metadata.remoteBranches].map((branch) => branch.name)) },
      { label: "Create Branch…", icon: "git-branch-create", separatorBefore: true, run: () => this.promptNameAction(repository, "create_branch", "Create Branch", "Branch name") },
      { label: "Create Branch From…", icon: "git-branch-create", run: () => this.createBranchFrom(repository, metadata) },
      { label: "Rename Current Branch…", icon: "edit", run: () => this.promptNameAction(repository, "rename_branch", "Rename Branch", "New branch name") },
      { label: "Merge Branch…", icon: "git-merge", separatorBefore: true, run: () => this.chooseRefAction(repository, "merge", metadata.branches.filter((branch) => !branch.current).map((branch) => branch.name)) },
      { label: "Rebase Current Branch…", icon: "git-pull-request", run: () => this.chooseRefAction(repository, "rebase", metadata.branches.filter((branch) => !branch.current).map((branch) => branch.name)) },
      { label: "Publish Branch", icon: "cloud-upload", run: () => this.run(repository, { requestId: randomUUID(), action: "publish_branch", remote: metadata.remotes[0]?.name || "origin" }) },
      { label: "Delete Local Branch…", icon: "trash", danger: true, separatorBefore: true, run: () => this.chooseRefAction(repository, "delete_branch", metadata.branches.filter((branch) => !branch.current).map((branch) => branch.name)) },
      { label: "Delete Remote Branch…", icon: "trash", danger: true, run: () => this.deleteRemoteBranch(repository, metadata) },
    ]);
  }

  private async showRemoteMenu(repository: SourceControlRepository, x: number, y: number): Promise<void> {
    const metadata = await this.ensureMetadata(repository.id);
    if (!metadata) return;
    showContextMenu(x, y, [
      { label: "Add Remote…", icon: "add", run: () => this.addRemote(repository) },
      { label: "Remove Remote…", icon: "remove", danger: true, disabled: metadata.remotes.length === 0, run: async () => {
        const name = await choose("Remove Remote", metadata.remotes.map((remote) => remote.name));
        if (name) await this.run(repository, { requestId: randomUUID(), action: "remove_remote", name });
      } },
    ]);
  }

  private async showStashMenu(repository: SourceControlRepository, x: number, y: number): Promise<void> {
    const metadata = await this.ensureMetadata(repository.id);
    if (!metadata) return;
    const hasStash = metadata.stashes.length > 0;
    const workflow = presentationFor(repository).workflow;
    if (workflow === "fossil") {
      showContextMenu(x, y, [
        { label: "Stash Changes", icon: "archive", run: () => this.runSimple(repository, "stash") },
        { label: "Snapshot Changes", icon: "archive", run: () => this.runSimple(repository, "stash_snapshot") },
        { label: "Apply Latest Stash", icon: "check", separatorBefore: true, disabled: !hasStash, run: () => this.runSimple(repository, "apply_latest_stash") },
        { label: "Apply Stash…", icon: "check", disabled: !hasStash, run: () => this.chooseStashAction(repository, metadata, "apply_stash") },
        { label: "Pop Latest Stash", icon: "move", disabled: !hasStash, run: () => this.runSimple(repository, "pop_latest_stash") },
        { label: "Pop Stash…", icon: "move", disabled: !hasStash, run: () => this.chooseStashAction(repository, metadata, "pop_stash") },
        { label: "View Stash…", icon: "eye", separatorBefore: true, disabled: !hasStash, run: async () => {
          const ref = await choose("View Stash", metadata.stashes.map((stash) => stash.ref), metadata.stashes.map((stash) => stash.message));
          if (ref) await this.loadReview(repository.id, ref, "stash");
        } },
        { label: "Drop Stash…", icon: "trash", danger: true, disabled: !hasStash, run: () => this.chooseStashAction(repository, metadata, "drop_stash") },
        { label: "Drop All Stashes", icon: "trash", danger: true, disabled: !hasStash, run: () => this.runSimple(repository, "drop_all_stashes") },
      ]);
      return;
    }
    if (workflow !== "git") {
      showContextMenu(x, y, [
        { label: "Stash Changes", icon: "archive", run: () => this.runSimple(repository, "stash") },
        { label: "Apply Latest Stash", icon: "check", separatorBefore: true, disabled: !hasStash, run: () => this.runSimple(repository, "apply_latest_stash") },
        { label: "Pop Latest Stash", icon: "move", disabled: !hasStash, run: () => this.runSimple(repository, "pop_latest_stash") },
        { label: "View Stash…", icon: "eye", separatorBefore: true, disabled: !hasStash, run: async () => {
          const ref = await choose("View Stash", metadata.stashes.map((stash) => stash.ref), metadata.stashes.map((stash) => stash.message));
          if (ref) await this.loadReview(repository.id, ref, "stash");
        } },
        { label: "Drop Stash…", icon: "trash", danger: true, disabled: !hasStash, run: () => this.chooseStashAction(repository, metadata, "drop_stash") },
      ]);
      return;
    }
    showContextMenu(x, y, [
      { label: "Stash Changes", icon: "archive", run: () => this.runSimple(repository, "stash") },
      { label: "Stash Changes (Include Untracked)", icon: "archive", run: () => this.runSimple(repository, "stash_untracked") },
      { label: "Stash Staged Changes", icon: "archive", run: () => this.runSimple(repository, "stash_staged") },
      { label: "Apply Latest Stash", icon: "check", separatorBefore: true, disabled: !hasStash, run: () => this.runSimple(repository, "apply_latest_stash") },
      { label: "Apply Stash…", icon: "check", disabled: !hasStash, run: () => this.chooseStashAction(repository, metadata, "apply_stash") },
      { label: "Pop Latest Stash", icon: "move", disabled: !hasStash, run: () => this.runSimple(repository, "pop_latest_stash") },
      { label: "Pop Stash…", icon: "move", disabled: !hasStash, run: () => this.chooseStashAction(repository, metadata, "pop_stash") },
      { label: "View Stash…", icon: "eye", separatorBefore: true, disabled: !hasStash, run: async () => {
        const ref = await choose("View Stash", metadata.stashes.map((stash) => stash.ref), metadata.stashes.map((stash) => stash.message));
        if (ref) await this.loadReview(repository.id, ref, "stash");
      } },
      { label: "Drop Stash…", icon: "trash", danger: true, disabled: !hasStash, run: () => this.chooseStashAction(repository, metadata, "drop_stash") },
      { label: "Drop All Stashes", icon: "trash", danger: true, disabled: !hasStash, run: () => this.runSimple(repository, "drop_all_stashes") },
    ]);
  }

  private async showTagMenu(repository: SourceControlRepository, x: number, y: number): Promise<void> {
    const metadata = await this.ensureMetadata(repository.id);
    if (!metadata) return;
    showContextMenu(x, y, [
      { label: "Create Tag…", icon: "tag-add", run: () => this.promptNameAction(repository, "create_tag", "Create Tag", "Tag name") },
      { label: "Delete Local Tag…", icon: "trash", danger: true, disabled: metadata.tags.length === 0, run: async () => {
        const name = await choose("Delete Tag", metadata.tags); if (name) await this.run(repository, { requestId: randomUUID(), action: "delete_tag", name });
      } },
      { label: "Delete Remote Tag…", icon: "trash", danger: true, disabled: metadata.tags.length === 0, run: async () => {
        const name = await choose("Delete Remote Tag", metadata.tags); if (name) await this.run(repository, { requestId: randomUUID(), action: "delete_remote_tag", name, remote: metadata.remotes[0]?.name || "origin" });
      } },
      { label: "Push Tags", icon: "cloud-upload", separatorBefore: true, disabled: metadata.tags.length === 0, run: () => this.run(repository, { requestId: randomUUID(), action: "push_tags", remote: metadata.remotes[0]?.name || "origin" }) },
    ]);
  }

  private async ensureMetadata(repositoryId: string): Promise<SourceControlMetadata | null> {
    const cached = this.metadata.get(repositoryId);
    if (cached) return cached;
    try {
      const metadata = await sourceControlAPI.loadMetadata(this.workspaceId, repositoryId);
      this.metadata.set(repositoryId, metadata);
      return metadata;
    } catch (error) {
      toast(error instanceof Error ? error.message : String(error));
      return null;
    }
  }

  private async loadHistory(repositoryId: string): Promise<void> {
    try { this.history.set(repositoryId, await sourceControlAPI.loadHistory(this.workspaceId, repositoryId)); this.render(); }
    catch (error) { toast(error instanceof Error ? error.message : String(error)); }
  }

  private async loadMoreHistory(repositoryId: string): Promise<void> {
    const current = this.history.get(repositoryId);
    if (!current?.hasMore) return;
    try {
      const next = await sourceControlAPI.loadHistory(this.workspaceId, repositoryId, current.nextOffset || current.commits.length);
      this.history.set(repositoryId, { commits: [...current.commits, ...next.commits], hasMore: next.hasMore, nextOffset: next.nextOffset });
      this.render();
    } catch (error) { toast(error instanceof Error ? error.message : String(error)); }
  }

  private async loadReview(repositoryId: string, ref: string, kind: "commit" | "stash"): Promise<void> {
    try {
      const detail = await sourceControlAPI.loadRevisionDetail(this.workspaceId, repositoryId, ref, kind);
      this.reviews.set(repositoryId, { ref, kind, detail });
      this.historyExpanded.add(repositoryId);
      this.render();
    } catch (error) { toast(error instanceof Error ? error.message : String(error)); }
  }

  private async runSimple(repository: SourceControlRepository, action: string): Promise<void> {
    await this.run(repository, { requestId: randomUUID(), action });
  }

  private async runWithRemote(repository: SourceControlRepository, action: string): Promise<void> {
    const metadata = await this.ensureMetadata(repository.id);
    if (!metadata) return;
    const remote = await choose("Choose Remote", metadata.remotes.map((item) => item.name));
    if (!remote) return;
    const ref = await promptDialog({ title: action === "pull_from" ? "Pull From" : "Push To", label: "Branch or ref (optional)", confirmLabel: action === "pull_from" ? "Pull" : "Push", required: false });
    if (ref === null) return;
    await this.run(repository, { requestId: randomUUID(), action, remote, ref });
  }

  private async chooseRefAction(repository: SourceControlRepository, action: string, refs: string[]): Promise<void> {
    const ref = await choose(actionLabel(action), refs);
    if (ref) await this.run(repository, { requestId: randomUUID(), action, ref });
  }

  private async promptNameAction(repository: SourceControlRepository, action: string, title: string, label: string): Promise<void> {
    const name = await promptDialog({ title, label, confirmLabel: title.split(" ")[0] });
    if (name) await this.run(repository, { requestId: randomUUID(), action, name });
  }

  private async createBranchFrom(repository: SourceControlRepository, metadata: SourceControlMetadata): Promise<void> {
    const startPoint = await choose("Create Branch From", [...metadata.branches, ...metadata.remoteBranches].map((branch) => branch.name));
    if (!startPoint) return;
    const name = await promptDialog({ title: "Create Branch", label: "Branch name", confirmLabel: "Create" });
    if (name) await this.run(repository, { requestId: randomUUID(), action: "create_branch_from", name, startPoint });
  }

  private async deleteRemoteBranch(repository: SourceControlRepository, metadata: SourceControlMetadata): Promise<void> {
    const selected = await choose("Delete Remote Branch", metadata.remoteBranches.map((branch) => branch.name));
    if (!selected) return;
    const slash = selected.indexOf("/");
    const remote = slash > 0 ? selected.slice(0, slash) : metadata.remotes[0]?.name || "origin";
    const ref = slash > 0 ? selected.slice(slash + 1) : selected;
    await this.run(repository, { requestId: randomUUID(), action: "delete_remote_branch", remote, ref });
  }

  private async addRemote(repository: SourceControlRepository): Promise<void> {
    const name = await promptDialog({ title: "Add Remote", label: "Remote name", initial: "origin", confirmLabel: "Next" });
    if (!name) return;
    const url = await promptDialog({ title: "Add Remote", label: "Remote URL", confirmLabel: "Add" });
    if (url) await this.run(repository, { requestId: randomUUID(), action: "add_remote", name, url });
  }

  private async chooseStashAction(repository: SourceControlRepository, metadata: SourceControlMetadata, action: string): Promise<void> {
    const ref = await choose(actionLabel(action), metadata.stashes.map((stash) => stash.ref), metadata.stashes.map((stash) => stash.message));
    if (ref) await this.run(repository, { requestId: randomUUID(), action, ref });
  }

  private async chooseRoot(title: string): Promise<WorkspaceRoot | null> {
    const roots = this.callbacks.roots();
    if (roots.length === 0) return null;
    if (roots.length === 1) return roots[0];
    const id = await choose(title, roots.map((root) => root.id), roots.map((root) => root.label));
    return roots.find((root) => root.id === id) || null;
  }

  private async initialize(): Promise<void> {
    const root = await this.chooseRoot("Initialize Repository");
    if (!root) return;
    try { await sourceControlAPI.initializeGitRepository(this.workspaceId, root.id); await this.reloadRepositories(); }
    catch (error) { toast(error instanceof Error ? error.message : String(error), { sticky: true }); }
  }

  private async clone(): Promise<void> {
    const root = await this.chooseRoot("Clone Into Workspace");
    if (!root) return;
    const url = await promptDialog({ title: "Clone Repository", label: "Repository URL", confirmLabel: "Next" });
    if (!url) return;
    const suggested = url.split(/[\\/]/).pop()?.replace(/\.git$/i, "") || "repository";
    const destination = await promptDialog({ title: "Clone Repository", label: `Folder inside ${root.label}`, initial: suggested, confirmLabel: "Clone" });
    if (!destination) return;
    try { await sourceControlAPI.cloneGitRepository(this.workspaceId, url, root.id, destination); await this.reloadRepositories(); }
    catch (error) { toast(error instanceof Error ? error.message : String(error), { sticky: true }); }
  }

  private async toggleParentSearch(): Promise<void> {
    try { await sourceControlAPI.setParentRepositorySearch(this.workspaceId, !this.searchParents); this.searchParents = !this.searchParents; await this.reloadRepositories(); }
    catch (error) { toast(error instanceof Error ? error.message : String(error)); }
  }
}

async function choose(title: string, values: string[], labels?: string[]): Promise<string | null> {
  if (values.length === 0) { toast(`No choices are available for ${title.toLowerCase()}.`); return null; }
  const choice = await choiceDialog({ title, message: `Choose ${title.toLowerCase()}.`, choices: [
    ...values.map((value, index) => ({ id: value, label: labels?.[index] ? `${labels[index]}${labels[index] === value ? "" : ` — ${value}`}` : value, primary: index === 0 })),
    { id: "__cancel__", label: "Cancel" },
  ] });
  return choice && choice !== "__cancel__" ? choice : null;
}

function actionLabel(action: string): string {
  return action.split("_").map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join(" ");
}

function shortHash(hash: string): string { return hash.length > 9 ? hash.slice(0, 9) : hash; }
function statusClass(status: string): string {
  if (status === "?") return "untracked";
  return /^[a-z]$/i.test(status) ? status.toLowerCase() : "modified";
}
function formatDate(value: string): string {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : new Intl.DateTimeFormat(undefined, { month: "short", day: "numeric", year: "numeric" }).format(date);
}

function sameStatusContent(left: SourceControlStatus, right: SourceControlStatus): boolean {
  const { revision: _leftRevision, ...leftContent } = left;
  const { revision: _rightRevision, ...rightContent } = right;
  return JSON.stringify(leftContent) === JSON.stringify(rightContent);
}

function shouldShowRepositorySync(repository: SourceControlRepository, status: SourceControlStatus | undefined): boolean {
  const presentation = presentationFor(repository);
  return presentation.promotesPendingSync && supports(repository, "sync") && presentation.showSync(status);
}
