import { afterEach, describe, expect, it, vi } from 'vitest';
import { SourceControlHistoryView, operationChangesHistory } from './sourceControlHistory';
import * as api from './sourceControlApi';
import type { SourceControlCommit, SourceControlHistory, SourceControlRepository, SourceControlRevisionDetail } from './sourceControlTypes';

vi.mock('./sourceControlApi', () => ({ loadHistory: vi.fn(), loadRevisionDetail: vi.fn() }));
const controllers: AbortController[] = [];
afterEach(() => { controllers.splice(0).forEach(c => c.abort()); document.body.replaceChildren(); vi.resetAllMocks(); });
const commit = (hash: string): SourceControlCommit => ({ hash, subject: `Commit ${hash}`, parents: [], author: 'A <B>', authoredAt: '2026-11-05T10:00:00Z', refs: ['main'] });
const page = (...hashes: string[]): SourceControlHistory => ({ commits: hashes.map(commit), hasMore: false });
const detail = (ref: string): SourceControlRevisionDetail => ({ ref, files: [{ path: 'new name.txt', oldPath: 'old name.txt', status: 'R' }] });
function deferred<T>() { let resolve!: (value: T) => void; let reject!: (error: Error) => void; const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; }
function setup(provider = 'git', capable = true) {
  let repositories: SourceControlRepository[] = [{ id: 'repo', providerId: provider, providerLabel: provider, label: 'Repo', scopes: [], parent: false, revision: 1, available: true, capabilities: capable ? ['history'] : ['status'] }];
  const host = document.createElement('div'); document.body.append(host);
  const controller = new AbortController(); controllers.push(controller);
  const openDiff = vi.fn().mockResolvedValue(undefined);
  const render = () => { history.remember(host); host.innerHTML = repositories.map(repo => history.render(repo)).join(''); history.restore(host); };
  const history = new SourceControlHistoryView('workspace', controller.signal, { repositories: () => repositories, render, openDiff });
  host.addEventListener('click', event => history.click(event));
  host.addEventListener('dblclick', event => history.doubleClick(event));
  host.addEventListener('keydown', event => history.keydown(event));
  render();
  const click = (selector: string) => { const element = host.querySelector<HTMLButtonElement>(selector); expect(element).not.toBeNull(); element!.click(); };
  return { host, history, render, openDiff, click, controller, remove: () => { repositories = []; history.reconcile(); render(); }, changeScope: () => { repositories[0].scopes = [{rootId:'restricted', rootLabel:'Restricted',repoPrefix:'limited'}]; history.reconcile(); render(); } };
}

describe('commit history', () => {
  it.each(['git', 'fossil'])('loads %s history lazily and opens immutable renamed-file diffs', async provider => {
    vi.mocked(api.loadHistory).mockResolvedValue(page('abcdef012345'));
    vi.mocked(api.loadRevisionDetail).mockResolvedValue(detail('abcdef012345'));
    const view = setup(provider);
    expect(api.loadHistory).not.toHaveBeenCalled();
    view.click('[data-history-action="toggle"]');
    expect(view.host.textContent).toContain('Loading history');
    await vi.waitFor(() => expect(view.host.textContent).toContain('Commit abcdef012345'));
    expect(api.loadHistory).toHaveBeenCalledWith('workspace', 'repo', 0, expect.any(AbortSignal));
    expect(view.host.textContent).toContain('A <B>');
    expect(view.host.querySelector('b:not(.scm-history-file b)')).toBeNull();
    view.click('[data-history-commit]');
    await vi.waitFor(() => expect(view.host.textContent).toContain('old name.txt → new name.txt'));
    view.click('[data-history-file]');
    expect(view.openDiff).toHaveBeenLastCalledWith(expect.objectContaining({ id: 'repo' }), { kind: 'revision', scope: 'commit', ref: 'abcdef012345', path: 'new name.txt', oldPath: 'old name.txt' }, false);
    view.host.querySelector('[data-history-file]')!.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }));
    expect(view.openDiff).toHaveBeenLastCalledWith(expect.anything(), expect.anything(), true);
    view.host.querySelector('[data-history-file]')!.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }));
    expect(view.openDiff).toHaveBeenCalledTimes(3);
    view.click('[data-history-commit]');
    expect(view.host.querySelector('[data-history-file]')).toBeNull();
  });

  it('hides history for unsupported providers', () => {
    expect(setup('p4', false).host.textContent).toBe('');
    expect(api.loadHistory).not.toHaveBeenCalled();
  });

  it('appends pages once, deduplicates hashes, and shows provider truncation', async () => {
    vi.mocked(api.loadHistory).mockResolvedValueOnce({ ...page('a'), hasMore: true, nextOffset: 100 });
    const more = deferred<SourceControlHistory>();
    vi.mocked(api.loadHistory).mockReturnValueOnce(more.promise);
    const view = setup(); view.click('[data-history-action="toggle"]');
    await vi.waitFor(() => expect(view.host.querySelector('[data-history-action="more"]')).not.toBeNull());
    view.click('[data-history-action="more"]'); view.click('[data-history-action="more"]');
    expect(api.loadHistory).toHaveBeenCalledTimes(2);
    expect(vi.mocked(api.loadHistory).mock.calls[1][2]).toBe(100);
    more.resolve({ ...page('a', 'b'), truncated: true, hasMore: true, nextOffset: 100 });
    await vi.waitFor(() => expect(view.host.querySelectorAll('[data-history-commit]')).toHaveLength(2));
    expect(view.host.textContent).toContain('limited by the provider');
    expect(view.host.querySelector('[data-history-action="more"]')).toBeNull();
  });

  it('offers retry and an empty state after a failed load', async () => {
    vi.mocked(api.loadHistory).mockRejectedValueOnce(new Error('Provider offline')).mockResolvedValueOnce(page());
    const view = setup(); view.click('[data-history-action="toggle"]');
    await vi.waitFor(() => expect(view.host.textContent).toContain('Provider offline'));
    view.click('[data-history-action="retry"]');
    await vi.waitFor(() => expect(view.host.textContent).toContain('No commits yet'));
  });

  it('ignores obsolete detail responses when a different commit is selected', async () => {
    vi.mocked(api.loadHistory).mockResolvedValue(page('a', 'b'));
    const first = deferred<SourceControlRevisionDetail>();
    vi.mocked(api.loadRevisionDetail).mockReturnValueOnce(first.promise).mockResolvedValueOnce({ ref: 'b', files: [{ path: 'b.txt', status: 'A' }] });
    const view = setup(); view.click('[data-history-action="toggle"]');
    await vi.waitFor(() => expect(view.host.querySelectorAll('[data-history-commit]')).toHaveLength(2));
    view.click('[data-history-commit="a"]'); view.click('[data-history-commit="b"]');
    expect(vi.mocked(api.loadRevisionDetail).mock.calls[0][4]!.aborted).toBe(true);
    await vi.waitFor(() => expect(view.host.textContent).toContain('b.txt'));
    first.resolve(detail('a'));
    await new Promise(resolve => setTimeout(resolve, 0));
    expect(view.host.textContent).not.toContain('old name.txt');
    expect(view.host.querySelectorAll('[data-history-commit][aria-expanded="true"]')).toHaveLength(1);
  });

  it('retries failed detail without reloading the history list', async () => {
    vi.mocked(api.loadHistory).mockResolvedValue(page('a'));
    vi.mocked(api.loadRevisionDetail).mockRejectedValueOnce(new Error('Missing revision')).mockResolvedValueOnce({ ref: 'a', files: [] });
    const view = setup(); view.click('[data-history-action="toggle"]');
    await vi.waitFor(() => expect(view.host.querySelector('[data-history-commit]')).not.toBeNull());
    view.click('[data-history-commit]');
    await vi.waitFor(() => expect(view.host.textContent).toContain('Missing revision'));
    view.click('[data-history-action="retry-detail"]');
    await vi.waitFor(() => expect(view.host.textContent).toContain('No changed files in this workspace'));
    expect(api.loadHistory).toHaveBeenCalledTimes(1);
  });

  it('refreshes from the first page and retains immutable selected detail', async () => {
    vi.mocked(api.loadHistory).mockResolvedValueOnce(page('a')).mockResolvedValueOnce(page('new', 'a'));
    vi.mocked(api.loadRevisionDetail).mockResolvedValue(detail('a'));
    const view = setup(); view.click('[data-history-action="toggle"]');
    await vi.waitFor(() => expect(view.host.querySelector('[data-history-commit]')).not.toBeNull());
    view.click('[data-history-commit]');
    await vi.waitFor(() => expect(view.host.querySelector('[data-history-file]')).not.toBeNull());
    view.history.refresh('repo');
    await vi.waitFor(() => expect(view.host.querySelectorAll('[data-history-commit]')).toHaveLength(2));
    expect(view.host.querySelector('[data-history-commit="a"]')?.getAttribute('aria-expanded')).toBe('true');
    expect(view.host.querySelector('[data-history-file]')).not.toBeNull();
    expect(api.loadRevisionDetail).toHaveBeenCalledTimes(1);
    expect(operationChangesHistory('commit_all')).toBe(true);
    expect(operationChangesHistory('fetch')).toBe(true);
    expect(operationChangesHistory('stage_hunk')).toBe(false);
  });

  it.each(['collapse', 'remove', 'dispose'])('cancels pending history on %s', async action => {
    const pending = deferred<SourceControlHistory>(); vi.mocked(api.loadHistory).mockReturnValue(pending.promise);
    const view = setup(); view.click('[data-history-action="toggle"]');
    if (action === 'collapse') view.click('[data-history-action="toggle"]');
    else if (action === 'remove') view.remove(); else view.controller.abort();
    expect(vi.mocked(api.loadHistory).mock.calls[0][3]!.aborted).toBe(true);
    pending.resolve(page('obsolete'));
    await new Promise(resolve => setTimeout(resolve, 0));
    expect(view.host.textContent).not.toContain('obsolete');
  });

  it('preserves scroll/focus through renders and supports keyboard navigation', async () => {
    vi.mocked(api.loadHistory).mockResolvedValue(page('a', 'b'));
    const view = setup(); view.click('[data-history-action="toggle"]');
    await vi.waitFor(() => expect(view.host.querySelectorAll('[data-history-commit]')).toHaveLength(2));
    const first = view.host.querySelector<HTMLButtonElement>('[data-history-commit="a"]')!;
    first.focus(); view.host.querySelector('[data-history-scroll]')!.scrollTop = 123;
    view.render();
    expect((document.activeElement as HTMLElement).dataset.historyCommit).toBe('a');
    expect(view.host.querySelector('[data-history-scroll]')!.scrollTop).toBe(123);
    document.activeElement!.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowDown', bubbles: true }));
    expect((document.activeElement as HTMLElement).dataset.historyCommit).toBe('b');
  });

  it('discards cached detail when repository permissions change', async () => {
    vi.mocked(api.loadHistory).mockResolvedValue(page('a'));
    vi.mocked(api.loadRevisionDetail).mockResolvedValue(detail('a'));
    const view = setup(); view.click('[data-history-action="toggle"]');
    await vi.waitFor(() => expect(view.host.querySelector('[data-history-commit]')).not.toBeNull());
    view.click('[data-history-commit]');
    await vi.waitFor(() => expect(view.host.querySelector('[data-history-file]')).not.toBeNull());
    view.changeScope();
    expect(view.host.querySelector('[data-history-file]')).toBeNull();
    expect(view.host.querySelector('[data-history-action="toggle"]')?.getAttribute('aria-expanded')).toBe('false');
  });
});
