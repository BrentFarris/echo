import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { ReferenceSearch, type ReferenceResults, type ReferenceSearchState } from './referenceSearch';

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
