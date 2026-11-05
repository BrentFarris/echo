import { loadHistory, loadRevisionDetail } from './sourceControlApi';
import type { SourceControlCommit, SourceControlDiffRequest, SourceControlRepository, SourceControlRevisionDetail } from './sourceControlTypes';
import { escapeHTML } from './ui';

type HistoryState = {
  scope: string;
  expanded: boolean; loaded: boolean; commits: SourceControlCommit[]; nextOffset: number;
  hasMore: boolean; truncated: boolean; error?: string; failedOffset: number;
  request?: AbortController; selected?: string; detail?: SourceControlRevisionDetail;
  detailRequest?: AbortController; detailError?: string; selectedFile?: number; scroll: number;
};
type Callbacks = {
  repositories(): SourceControlRepository[];
  render(): void;
  openDiff(repository: SourceControlRepository, target: SourceControlDiffRequest, keep: boolean): Promise<void>;
};

// Immutable revision reviews have their own selection and request lifecycle;
// they never participate in working-tree multi-selection or stash review.
export class SourceControlHistoryView {
  private states = new Map<string, HistoryState>();
  private focusKey?: string;
  constructor(private workspaceId: string, private signal: AbortSignal, private callbacks: Callbacks) {
    signal.addEventListener('abort', () => {
      for (const state of this.states.values()) { state.request?.abort(); state.detailRequest?.abort(); }
      this.states.clear();
    }, { once: true });
  }

  private state(id: string): HistoryState {
    let state = this.states.get(id);
    if (!state) {
      state = { scope: this.scope(id), expanded: false, loaded: false, commits: [], nextOffset: 0, hasMore: false, truncated: false, failedOffset: 0, scroll: 0 };
      this.states.set(id, state);
    }
    return state;
  }

  private scope(id: string): string {
    const repo = this.callbacks.repositories().find(repo => repo.id === id);
    return JSON.stringify([repo?.providerId, repo?.rootRef, repo?.scopes]);
  }

  reconcile(): void {
    for (const [id, state] of this.states) {
      if (state.scope === this.scope(id) && this.callbacks.repositories().some(repo => repo.id === id && repo.available && repo.capabilities.includes('history'))) continue;
      state.request?.abort(); state.detailRequest?.abort(); this.states.delete(id);
    }
  }

  refresh(id: string): void {
    const state = this.states.get(id);
    if (!state || (!state.loaded && !state.request && !state.error)) return;
    state.request?.abort(); state.request = undefined;
    state.loaded = false; state.commits = []; state.nextOffset = 0; state.hasMore = false; state.error = undefined;
    if (state.expanded) void this.load(id, 0);
  }

  remember(host: HTMLElement): void {
    this.focusKey = document.activeElement instanceof HTMLElement && host.contains(document.activeElement)
      ? document.activeElement.dataset.historyFocus : undefined;
    host.querySelectorAll<HTMLElement>('[data-history-scroll]').forEach(el => {
      const state = this.states.get(el.dataset.historyScroll!);
      if (state) state.scroll = el.scrollTop;
    });
  }

  restore(host: HTMLElement): void {
    host.querySelectorAll<HTMLElement>('[data-history-scroll]').forEach(el => { el.scrollTop = this.states.get(el.dataset.historyScroll!)?.scroll || 0; });
    if (this.focusKey) [...host.querySelectorAll<HTMLElement>('[data-history-focus]')]
      .find(el => el.dataset.historyFocus === this.focusKey)?.focus({ preventScroll: true });
  }

  render(repo: SourceControlRepository): string {
    if (!repo.available || !repo.capabilities.includes('history')) return '';
    const state = this.state(repo.id);
    const key = (value: string) => `data-history-focus="${escapeHTML(repo.id + ':' + value)}"`;
    return `<section class="scm-history" data-history-repository="${escapeHTML(repo.id)}" aria-label="Commit history">
      <header><button type="button" data-history-action="toggle" ${key('toggle')} aria-expanded="${state.expanded}"><span class="codicon codicon-chevron-${state.expanded ? 'down' : 'right'}"></span><strong>History</strong></button>
      <button type="button" data-history-action="refresh" ${key('refresh')} aria-label="Refresh History" title="Refresh History"><span class="codicon codicon-refresh"></span></button></header>
      ${state.expanded ? `<div class="scm-history-list" data-history-scroll="${escapeHTML(repo.id)}" aria-busy="${!!state.request}">
        ${state.commits.map(commit => {
          const selected = state.selected === commit.hash;
          const date = new Date(commit.authoredAt);
          const dateLabel = Number.isNaN(date.valueOf()) ? commit.authoredAt : date.toLocaleString();
          return `<div class="scm-history-entry"><button type="button" class="scm-history-commit" data-history-commit="${escapeHTML(commit.hash)}" ${key(commit.hash)} aria-expanded="${selected}" title="${escapeHTML(commit.subject + '\n' + commit.author + ' · ' + dateLabel + '\n' + commit.hash)}">
            <span class="codicon codicon-chevron-${selected ? 'down' : 'right'}"></span><span class="scm-history-summary"><strong>${escapeHTML(commit.subject || '(no commit message)')}</strong><small>${escapeHTML(commit.author)} · ${escapeHTML(dateLabel)}</small><small>${escapeHTML(commit.hash.slice(0, 10))}${(commit.refs || []).map(ref => ` <span class="scm-history-ref">${escapeHTML(ref)}</span>`).join('')}</small></span></button>
            ${selected ? `<div class="scm-history-files" aria-label="Changed files">
              ${state.detailRequest ? '<div class="git-empty compact" role="status">Loading changed files…</div>' : ''}
              ${state.detailError ? `<div class="git-warning" role="status">${escapeHTML(state.detailError)} <button type="button" data-history-action="retry-detail" ${key('retry-detail')}>Retry</button></div>` : ''}
              ${state.detail ? state.detail.files.length ? state.detail.files.map((file, index) => `<button type="button" class="scm-history-file" data-history-file="${index}" ${key(commit.hash + ':' + index)} aria-pressed="${state.selectedFile === index}" title="${escapeHTML(file.oldPath ? file.oldPath + ' → ' + file.path : file.path)}"><span class="codicon codicon-file-code"></span><span>${escapeHTML(file.oldPath ? file.oldPath + ' → ' + file.path : file.path)}</span><b>${escapeHTML(file.status)}</b></button>`).join('') : '<div class="git-empty compact">No changed files in this workspace</div>' : ''}
            </div>` : ''}</div>`;
        }).join('')}
        ${state.request ? '<div class="git-empty compact" role="status">Loading history…</div>' : ''}
        ${state.error ? `<div class="git-warning" role="status">${escapeHTML(state.error)} <button type="button" data-history-action="retry" ${key('retry')}>Retry</button></div>` : ''}
        ${state.loaded && !state.commits.length && !state.request && !state.error ? '<div class="git-empty compact">No commits yet</div>' : ''}
        ${state.truncated ? '<div class="git-warning" role="status">History results were limited by the provider.</div>' : ''}
        ${state.hasMore && !state.error ? `<button type="button" class="scm-history-more" data-history-action="more" ${key('more')} ${state.request ? 'disabled' : ''}>Show more</button>` : ''}
      </div>` : ''}</section>`;
  }

  private context(target: EventTarget | null) {
    if (!(target instanceof Element)) return;
    const section = target.closest<HTMLElement>('[data-history-repository]');
    const repo = this.callbacks.repositories().find(repo => repo.id === section?.dataset.historyRepository);
    if (repo && section) return { repo, section, target, state: this.state(repo.id) };
  }

  click(event: MouseEvent): boolean {
    const context = this.context(event.target);
    if (!context) return false;
    const { repo, target, state } = context;
    const action = target.closest<HTMLElement>('[data-history-action]')?.dataset.historyAction;
    if (action === 'toggle') {
      state.expanded = !state.expanded;
      if (!state.expanded) {
        state.request?.abort(); state.request = undefined;
        state.detailRequest?.abort(); state.detailRequest = undefined;
      } else {
        if (!state.loaded) void this.load(repo.id, 0);
        if (state.selected && !state.detail) void this.select(repo.id, state.selected);
      }
      this.callbacks.render();
    } else if (action === 'refresh') {
      if (state.expanded) void this.load(repo.id, 0); else this.refresh(repo.id);
    } else if (action === 'more' || action === 'retry') {
      void this.load(repo.id, action === 'more' ? state.nextOffset : state.failedOffset);
    } else if (action === 'retry-detail' && state.selected) {
      void this.select(repo.id, state.selected);
    } else if (target.closest('[data-history-file]')) {
      context.section.querySelectorAll('[data-history-file]').forEach(button => button.setAttribute('aria-pressed', String(button === target.closest('[data-history-file]'))));
      void this.openFile(repo, Number(target.closest<HTMLElement>('[data-history-file]')!.dataset.historyFile), false);
    } else {
      const hash = target.closest<HTMLElement>('[data-history-commit]')?.dataset.historyCommit;
      if (hash) {
        if (state.selected === hash) {
          state.detailRequest?.abort(); state.detailRequest = undefined;
          state.selected = undefined; state.detail = undefined; this.callbacks.render();
        } else void this.select(repo.id, hash);
      }
    }
    return true;
  }

  doubleClick(event: MouseEvent): boolean {
    const context = this.context(event.target);
    const file = context?.target.closest<HTMLElement>('[data-history-file]');
    if (!context || !file) return false;
    void this.openFile(context.repo, Number(file.dataset.historyFile), true);
    return true;
  }

  keydown(event: KeyboardEvent): boolean {
    const context = this.context(event.target);
    if (!context) return false;
    const { target, section, repo } = context;
    if (event.key === 'Enter' && target.matches('[data-history-file]')) {
      section.querySelectorAll('[data-history-file]').forEach(button => button.setAttribute('aria-pressed', String(button === target)));
      event.preventDefault(); void this.openFile(repo, Number((target as HTMLElement).dataset.historyFile), true); return true;
    }
    if (['ArrowUp', 'ArrowDown', 'Home', 'End'].includes(event.key)) {
      event.preventDefault();
      const buttons = [...section.querySelectorAll<HTMLButtonElement>('button:not(:disabled)')];
      const index = buttons.indexOf(target as HTMLButtonElement);
      const next = event.key === 'Home' ? 0 : event.key === 'End' ? buttons.length - 1 : Math.max(0, Math.min(buttons.length - 1, index + (event.key === 'ArrowUp' ? -1 : 1)));
      buttons[next]?.focus(); return true;
    }
    return false;
  }

  private async load(id: string, offset: number): Promise<void> {
    const state = this.state(id);
    if (offset && state.request) return;
    state.request?.abort();
    const request = state.request = new AbortController();
    state.error = undefined; state.failedOffset = offset;
    if (!offset) { state.commits = []; state.loaded = false; state.hasMore = false; state.truncated = false; }
    this.callbacks.render();
    try {
      const page = await loadHistory(this.workspaceId, id, offset, AbortSignal.any([this.signal, request.signal]));
      if (request.signal.aborted || this.signal.aborted || state.request !== request) return;
      const known = new Set(state.commits.map(commit => commit.hash));
      for (const commit of page.commits) if (!known.has(commit.hash)) { state.commits.push(commit); known.add(commit.hash); }
      state.nextOffset = page.nextOffset ?? offset + page.commits.length;
      state.hasMore = page.hasMore && state.nextOffset > offset;
      state.truncated = !!page.truncated; state.loaded = true;
    } catch (error) {
      if (!request.signal.aborted && !this.signal.aborted) state.error = error instanceof Error ? error.message : String(error);
    } finally {
      if (state.request === request && !this.signal.aborted) { state.request = undefined; this.callbacks.render(); }
    }
  }

  private async select(id: string, hash: string): Promise<void> {
    const state = this.state(id);
    state.detailRequest?.abort();
    const request = state.detailRequest = new AbortController();
    state.selected = hash; state.detail = undefined; state.detailError = undefined; state.selectedFile = undefined;
    this.callbacks.render();
    try {
      const detail = await loadRevisionDetail(this.workspaceId, id, hash, 'commit', AbortSignal.any([this.signal, request.signal]));
      if (!request.signal.aborted && !this.signal.aborted && state.detailRequest === request) state.detail = detail;
    } catch (error) {
      if (!request.signal.aborted && !this.signal.aborted) state.detailError = error instanceof Error ? error.message : String(error);
    } finally {
      if (state.detailRequest === request && !this.signal.aborted) { state.detailRequest = undefined; this.callbacks.render(); }
    }
  }

  private async openFile(repo: SourceControlRepository, index: number, keep: boolean): Promise<void> {
    const state = this.state(repo.id), file = state.detail?.files[index];
    if (!file || !state.detail) return;
    state.selectedFile = index;
    // Preserve the clicked DOM node until dblclick has been dispatched.
    try {
      await this.callbacks.openDiff(repo, { kind: 'revision', scope: 'commit', ref: state.detail.ref, path: file.path, oldPath: file.oldPath }, keep);
    } catch (error) {
      if (!this.signal.aborted) { state.detailError = error instanceof Error ? error.message : String(error); this.callbacks.render(); }
    }
  }
}

export function operationChangesHistory(action: string): boolean {
  return !['stage', 'stage_all', 'stage_hunk', 'unstage', 'unstage_all', 'unstage_hunk', 'discard', 'discard_all', 'protect', 'protect_all', 'protect_hunk', 'unprotect', 'unprotect_all', 'unprotect_hunk', 'revert_hunk', 'track', 'open_ui'].includes(action);
}
