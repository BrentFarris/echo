import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { InstanceClient, instances } from './instances';
import { aggregateUsage, fetchUsageMachines, mountGeneral, mountUsageReset, notifyUsageMachinesChanged } from './usage';
import type { UsageTotals } from './types';

const totals: UsageTotals = { inputTokens: 1315134, outputTokens: 1250000000, modelCalls: 3, inputReports: 3, outputReports: 3, revision: 3, trackingStartedAt: '2026-01-01T00:00:00Z', resetAt: '2026-01-02T00:00:00Z' };
const local = 'a'.repeat(32), remote = 'b'.repeat(32), offline = 'c'.repeat(32);
beforeEach(() => {
  instances.clear(); localStorage.clear();
  for (const [nodeId, name, online] of [[local, 'Desktop', true], [remote, 'Laptop', true], [offline, 'Offline', false]] as const) instances.set(nodeId, { node: { nodeId, name, epoch: '', version: 1 }, online });
});
afterEach(() => { document.body.replaceChildren(); vi.restoreAllMocks(); instances.clear(); localStorage.clear(); });
it('queries each online machine once and excludes offline and unsupported machines', async () => {
  const json = vi.spyOn(InstanceClient.prototype, 'json').mockImplementation(async function (this: InstanceClient) {
    if (this.nodeId === remote) throw Object.assign(new Error('not found'), { status: 404 });
    return totals;
  });
  const machines = await fetchUsageMachines();
  expect(json).toHaveBeenCalledTimes(2);
  expect(aggregateUsage(machines, 'input')).toEqual({ total: 1315134, partial: true, available: true });
  expect(machines.find(m => m.nodeId === remote)?.error).toContain('Update required');
  expect(machines.find(m => m.nodeId === offline)?.error).toContain('Offline');
  instances.get(local)!.online = false;
  expect(aggregateUsage(await fetchUsageMachines(), 'input')).toEqual({ total: 0, partial: true, available: false });
});
it('mounts stable labels with accessible exact counts and updates on machine changes', async () => {
  vi.spyOn(InstanceClient.prototype, 'json').mockResolvedValue(totals);
  const root = document.createElement('div'); document.body.append(root);
  const changed = vi.fn(); mountGeneral(root, changed);
  await vi.waitFor(() => expect(root.querySelector('[data-direction=input]')?.textContent).toBe('2.63M'));
  const button = root.querySelector<HTMLButtonElement>('[data-direction=input]')!, panel = root.querySelector<HTMLElement>('#input-usage-details')!;
  expect(panel.hidden).toBe(true); button.focus();
  expect(panel.hidden).toBe(false); expect(panel.textContent).toContain('2,630,268'); expect(panel.textContent).toContain('Desktop'); expect(panel.textContent).toContain('Offline');
  button.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })); expect(panel.hidden).toBe(true);
  const toggle = root.querySelector<HTMLInputElement>('input')!; toggle.click(); expect(changed).toHaveBeenCalledOnce(); expect(localStorage.getItem('echo.showTurnStatistics')).toBe('false');
  instances.get(remote)!.online = false; notifyUsageMachinesChanged();
  await vi.waitFor(() => expect(button.textContent).toBe('1.31M'));
  expect(root.querySelector('[data-direction=input]')).toBe(button);
});
it('confirms reachable resets, reports partial failure and reuses the ID only on failed machines', async () => {
  const requests: { node: string; requestId: string }[] = [];
  let failed = false;
  vi.spyOn(InstanceClient.prototype, 'json').mockImplementation(async function (this: InstanceClient, path, _method, body) {
    if (path === '/usage/reset') {
      requests.push({ node: this.nodeId, requestId: (body as { requestId: string }).requestId });
      if (this.nodeId === remote && !failed) { failed = true; throw new Error('response lost'); }
    }
    return totals;
  });
  const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
  const root = document.createElement('div'); document.body.append(root);
  const refresh = vi.fn(async () => {}); mountUsageReset(root, refresh);
  const button = root.querySelector<HTMLButtonElement>('button')!;
  button.click(); await vi.waitFor(() => expect(button.disabled).toBe(false)); expect(requests).toHaveLength(0);
  confirm.mockReturnValue(true); button.click();
  await vi.waitFor(() => expect(button.textContent).toBe('Retry unsuccessful resets…'));
  expect(requests).toHaveLength(2); expect(root.textContent).toContain('Offline: skipped');
  button.click(); await vi.waitFor(() => expect(button.textContent).toBe('Reset token totals…'));
  expect(requests).toHaveLength(3); expect(requests[2]).toEqual(requests[1]);
  expect(refresh).toHaveBeenCalledTimes(3);
});
