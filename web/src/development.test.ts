import { describe, it, expect, vi, afterEach } from 'vitest';
import { waitForReplacementServer } from './development';

afterEach(() => vi.useRealTimers());

describe('replacement server detection', () => {
  it('waits for a new process identity through outages and stale responses', async () => {
    const fetcher = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ instanceId: 'old' })))
      .mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce(new Response('sign in', { status: 401 }))
      .mockResolvedValueOnce(new Response(JSON.stringify({ instanceId: 'new' })));
    const delay = vi.fn(async () => {});
    await waitForReplacementServer('old', { fetcher, delay });
    expect(fetcher).toHaveBeenCalledTimes(4);
    expect(delay).toHaveBeenCalledTimes(3);
    expect(fetcher.mock.calls[0][0]).toBe('/api/v1/health');
    expect(fetcher.mock.calls[0][1].cache).toBe('no-store');
  });
  it('accepts a fast replacement without requiring an observed outage', async () => {
    const fetcher = vi.fn(async () => new Response(JSON.stringify({ instanceId: 'new' })));
    await waitForReplacementServer('old', { fetcher });
    expect(fetcher).toHaveBeenCalledTimes(1);
  });
  it('times out if the old process stays healthy', async () => {
    vi.useFakeTimers();
    const fetcher = vi.fn(async () => new Response(JSON.stringify({ instanceId: 'old' })));
    const result = waitForReplacementServer('old', { fetcher, timeoutMs: 1000 });
    const assertion = expect(result).rejects.toThrow('Check the operation and launcher logs');
    await vi.advanceTimersByTimeAsync(1000);
    await assertion;
  });
});
