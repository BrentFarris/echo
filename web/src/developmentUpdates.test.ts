import { afterEach, describe, expect, it, vi } from 'vitest';
import { instances } from './instances';
import { developmentUpdateNode, refreshDevelopmentUpdates, renderDevelopmentUpdates, setDevelopmentUpdate, startDevelopmentUpdates } from './developmentUpdates';

function machine(nodeId: string) { instances.set(nodeId, { node: { nodeId, name: nodeId, epoch: 'one', version: 1 }, online: true }); }

afterEach(async () => {
  instances.clear();
  await refreshDevelopmentUpdates();
  document.body.innerHTML = '';
  vi.clearAllTimers(); vi.useRealTimers(); vi.unstubAllGlobals();
});

describe('development update guidance', () => {
  it('guides through settings and Development to the update button while preserving names', () => {
    machine('local');
    document.body.innerHTML = `<button data-update-notice="*">Settings</button><button data-update-notice="*">Development</button><button data-update-notice="local">Check for Updates</button><button data-update-notice="other">Other machine</button>`;
    setDevelopmentUpdate('local', true);
    expect(document.querySelectorAll('.update-dot')).toHaveLength(3);
    expect([...document.querySelectorAll('button')].map(b => b.textContent)).toEqual(['Settings', 'Development', 'Check for Updates', 'Other machine']);
    expect(document.querySelector('button')?.getAttribute('aria-description')).toBe('Update available');
    setDevelopmentUpdate('local', false);
    expect(document.querySelectorAll('.update-dot')).toHaveLength(0);
    expect(document.querySelector('button')?.hasAttribute('aria-description')).toBe(false);
  });

  it('moves the dot onto the actionable update button after a coordinated check', () => {
    machine('local');
    document.body.innerHTML = `<button data-update-notice="local" data-update-suppressed="true">Check</button><button data-update-notice="local" data-update-fallback="true">Update</button>`;
    renderDevelopmentUpdates();
    expect(document.querySelectorAll('.update-dot')).toHaveLength(1);
    expect(document.querySelector('button:last-child .update-dot')).not.toBeNull();
  });

  it('identifies the machine needing an update and labels the machine picker', () => {
    machine('local'); machine('remote');
    document.body.innerHTML = '<select aria-label="Settings machine"><option value="local">local</option><option value="remote">remote</option></select>';
    setDevelopmentUpdate('remote', true);
    expect(developmentUpdateNode('local')).toBe('remote');
    expect(document.querySelector('option[value=remote]')?.textContent).toBe('remote · Update available');
    setDevelopmentUpdate('local', true);
    expect(developmentUpdateNode('local')).toBe('local');
  });

  it('refreshes only cached server status, retains notices during outages, and removes revoked machines', async () => {
    machine('local');
    const fetcher = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ updateAvailable: true })))
      .mockRejectedValueOnce(new Error('offline')).mockResolvedValueOnce(new Response(JSON.stringify({ updateAvailable: false })));
    vi.stubGlobal('fetch', fetcher);
    document.body.innerHTML = '<button data-update-notice="*">Settings</button>';
    await refreshDevelopmentUpdates();
    expect(document.querySelector('.update-dot')).not.toBeNull();
    await refreshDevelopmentUpdates();
    expect(document.querySelector('.update-dot')).not.toBeNull();
    await refreshDevelopmentUpdates();
    expect(document.querySelector('.update-dot')).toBeNull();
    for (const [path, init] of fetcher.mock.calls) { expect(path).toMatch(/\/development\/update-status$/); expect(init.method).toBe('GET'); }
    setDevelopmentUpdate('local', true);
    instances.clear(); await refreshDevelopmentUpdates();
    expect(document.querySelector('.update-dot')).toBeNull();
  });

  it('keeps indicators current outside the settings panel', async () => {
    vi.useFakeTimers(); machine('local');
    const fetcher = vi.fn(async () => new Response(JSON.stringify({ updateAvailable: true })));
    vi.stubGlobal('fetch', fetcher);
    startDevelopmentUpdates();
    await refreshDevelopmentUpdates();
    await vi.advanceTimersByTimeAsync(15_000);
    expect(fetcher).toHaveBeenCalledTimes(2);
  });
});
