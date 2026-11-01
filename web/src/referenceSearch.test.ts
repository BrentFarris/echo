import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { ReferenceSearch, instanceReferenceDraft, type ReferenceResults, type ReferenceSearchState } from './referenceSearch';
import { combineState } from './instances';
import type { Segment, State } from './types';

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());
const result = (name: string, indexing = false): ReferenceResults => ({ entries: [{ name, path: name, directory: true, allowedBots: ['kaiju'] }], indexing, incomplete: false });

test('typing is debounced and obsolete successes and failures cannot replace the current query', async () => {
  const pending: { resolve: (value: ReferenceResults) => void; reject: (error: Error) => void; signal: AbortSignal }[] = [];
  const request = vi.fn((_content: string, _query: string, signal: AbortSignal) => new Promise<ReferenceResults>((resolve, reject) => pending.push({ resolve, reject, signal })));
  const search = new ReferenceSearch(request), updates = vi.fn<(state: ReferenceSearchState) => void>();
  search.search('@kaiju #t', 't', updates); search.search('@kaiju #tools', 'tools', updates);
  expect(request).not.toHaveBeenCalled();
  await vi.advanceTimersByTimeAsync(100); expect(request).toHaveBeenCalledTimes(1);
  search.search('@kaiju #src', 'src', updates); await vi.advanceTimersByTimeAsync(100);
  expect(pending[0].signal.aborted).toBe(true);
  search.search('@backend #src', 'src', updates); await vi.advanceTimersByTimeAsync(100);
  pending[2].resolve(result('backend-src')); await vi.advanceTimersByTimeAsync(0);
  pending[0].resolve(result('obsolete-tools')); pending[1].reject(new Error('obsolete error')); await vi.advanceTimersByTimeAsync(0);
  expect(updates).toHaveBeenLastCalledWith(result('backend-src'));
  search.close();
});

test('indexing refreshes suggestions and dismissal prevents reopening or further polling', async () => {
  const request = vi.fn().mockResolvedValueOnce(result('partial', true)).mockResolvedValueOnce(result('complete'));
  const search = new ReferenceSearch(request), updates = vi.fn();
  search.search('@kaiju #folder', 'folder', updates);
  await vi.advanceTimersByTimeAsync(100); expect(updates).toHaveBeenLastCalledWith(result('partial', true));
  await vi.advanceTimersByTimeAsync(300); expect(updates).toHaveBeenLastCalledWith(result('complete'));
  await vi.advanceTimersByTimeAsync(1000); expect(request).toHaveBeenCalledTimes(2);
  search.search('@kaiju #again', 'again', updates); search.close();
  await vi.advanceTimersByTimeAsync(1000); expect(request).toHaveBeenCalledTimes(2);

  let finish!: (value: ReferenceResults) => void;
  const inflight = new ReferenceSearch(() => new Promise(resolve => { finish = resolve; }));
  inflight.search('content', 'query', updates); await vi.advanceTimersByTimeAsync(100);
  inflight.close(); const count = updates.mock.calls.length;
  finish(result('late', true)); await vi.advanceTimersByTimeAsync(1000);
  expect(updates).toHaveBeenCalledTimes(count);
});

test('empty results, incomplete searches and actionable errors are preserved', async () => {
  const empty = { entries: [], indexing: false, incomplete: true };
  const search = new ReferenceSearch(vi.fn().mockResolvedValueOnce(empty).mockRejectedValueOnce(new Error('unknown bot @missing'))), updates = vi.fn();
  search.search('content', 'absent', updates); await vi.advanceTimersByTimeAsync(100);
  expect(updates).toHaveBeenLastCalledWith(empty);
  search.search('@missing #folder', 'folder', updates); await vi.advanceTimersByTimeAsync(100);
  expect(updates.mock.lastCall?.[0].error).toBe('unknown bot @missing'); search.close();
});

test('reference activation keeps files and resolved recipients on their owning machine', () => {
  const local = 'a'.repeat(32), remote = 'b'.repeat(32);
  const state = (): State => ({ bots: [{ id: 'echo', handle: 'echo' }, { id: 'kaiju', handle: 'kaiju' }].map(bot => ({ ...bot, name: bot.handle, roots: [], plugins: {}, enableMultitasking: false, prompt: '', revision: 0 })), chats: [], endpoints: [], plugins: [], appDock: [], schedules: [], occurrences: [], runs: [] });
  combineState(state(), { node: { nodeId: local, name: 'desktop', epoch: '', version: 1 }, enabled: true, peers: [], instances: [{ node: { nodeId: remote, name: 'linux', epoch: '', version: 1 }, state: state(), online: true }] });
  const content = '@kaiju @linux.kaiju inspect #local.txt and #@missing.txt';
  const file = (name: string, nodeId: string): Segment => ({ type: 'file', nodeId, text: name, path: `/${name}`, range: { start: content.indexOf('#' + name), end: content.indexOf('#' + name) + name.length + 1 } });
  const localFile = file('local.txt', local), remoteFile = file('@missing.txt', remote);
  const draft = instanceReferenceDraft(content, [localFile, remoteFile], remote);
  expect(draft.content).toHaveLength(content.length);
  expect(draft.content).not.toContain('@kaiju'); expect(draft.content).not.toContain('@linux.kaiju');
  expect(draft.content.slice(remoteFile.range!.start, remoteFile.range!.end)).toBe('#@missing.txt');
  expect(draft.segments).toEqual([remoteFile, { type: 'bot', botId: 'kaiju' }]);
  const localDraft = instanceReferenceDraft(content, [localFile, remoteFile], local);
  expect(localDraft.segments).toEqual([localFile, { type: 'bot', botId: 'kaiju' }]);
  expect(localDraft.content).not.toContain('@missing');
  expect(instanceReferenceDraft('#file', [], local).segments).toEqual([{ type: 'bot', botId: 'echo' }]);
  expect(() => instanceReferenceDraft('No recipients on Linux', [remoteFile], remote)).toThrow('no longer available');
});
