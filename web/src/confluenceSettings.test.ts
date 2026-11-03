import { afterEach, expect, it, vi } from 'vitest';
import { mountConfluenceSettings } from './confluenceSettings';
import { InstanceClient } from './instances';
import type { Plugin } from './types';

afterEach(() => { document.body.replaceChildren(); vi.restoreAllMocks(); });
function mount(config: Record<string, unknown> = {}, api = vi.fn().mockResolvedValue({ connected: true })) {
  const root = document.createElement('div'); document.body.append(root);
  const changed = vi.fn().mockResolvedValue(undefined), notice = vi.fn();
  mountConfluenceSettings(root, { id: 'confluence', enabled: true, config } as Plugin, api, changed, notice);
  return { root, api, changed, notice };
}
function field(root: HTMLElement, name: string) { return root.querySelector<HTMLInputElement>(`[name="${name}"]`)!; }
function submit(root: HTMLElement, name: string) { root.querySelector(`[data-confluence-${name}]`)!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); }

it('saves scoped Cloud settings and hides Cloud fields for Data Center', async () => {
  const { root, api, changed } = mount();
  field(root, 'url').value = 'https://site.atlassian.net'; field(root, 'email').value = 'reader@example.com'; field(root, 'cloudId').value = 'cloud-1'; field(root, 'tokenType').value = 'scoped'; field(root, 'tokenType').dispatchEvent(new Event('change'));
  expect(root.querySelector<HTMLElement>('[data-scoped]')!.hidden).toBe(false);
  submit(root, 'settings'); await vi.waitFor(() => expect(changed).toHaveBeenCalledOnce());
  expect(api).toHaveBeenCalledWith('/plugins/confluence', 'PATCH', { enabled: true, config: { deployment: 'cloud', url: 'https://site.atlassian.net', email: 'reader@example.com', tokenType: 'scoped', cloudId: 'cloud-1' } });
  field(root, 'deployment').value = 'datacenter'; field(root, 'deployment').dispatchEvent(new Event('change')); field(root, 'url').value = 'http://internal/confluence';
  expect(root.querySelector<HTMLElement>('[data-cloud]')!.hidden).toBe(true); expect(root.querySelector<HTMLElement>('[data-scoped]')!.hidden).toBe(true);
  submit(root, 'settings'); await vi.waitFor(() => expect(changed).toHaveBeenCalledTimes(2));
  expect(api).toHaveBeenLastCalledWith('/plugins/confluence', 'PATCH', { enabled: true, config: { deployment: 'datacenter', url: 'http://internal/confluence', email: '', tokenType: 'classic', cloudId: '' } });
});

it('saves and clears tokens separately, clears input immediately, and tests the saved connection', async () => {
  const { root, api, changed } = mount({ credential: 'vault-reference' });
  expect(root.innerHTML).not.toContain('vault-reference'); expect(field(root, 'secret').type).toBe('password');
  field(root, 'secret').value = 'private-token'; submit(root, 'credential'); expect(field(root, 'secret').value).toBe('');
  await vi.waitFor(() => expect(changed).toHaveBeenCalledOnce()); expect(api).toHaveBeenCalledWith('/plugins/confluence/credential', 'POST', { secret: 'private-token' });
  root.querySelector<HTMLButtonElement>('[data-confluence-test]')!.click(); await vi.waitFor(() => expect(root.textContent).toContain('Connected to Confluence'));
  root.querySelector<HTMLButtonElement>('[data-confluence-clear]')!.click(); await vi.waitFor(() => expect(changed).toHaveBeenCalledTimes(2));
  expect(root.textContent).toContain('No token saved'); expect(api).toHaveBeenCalledWith('/plugins/confluence/credential', 'POST', { secret: '' });
});

it('reports failures without retaining the password or disabled controls', async () => {
  const api = vi.fn().mockRejectedValue(new Error('Unavailable')); const { root, notice } = mount({}, api);
  field(root, 'secret').value = 'private-token'; submit(root, 'credential'); await vi.waitFor(() => expect(notice).toHaveBeenCalled()); expect(field(root, 'secret').value).toBe('');
  root.querySelector<HTMLButtonElement>('[data-confluence-test]')!.click(); await vi.waitFor(() => expect(root.textContent).toContain('Connection test failed'));
  expect(root.querySelector<HTMLButtonElement>('[data-confluence-test]')!.disabled).toBe(false);
});

it('routes credential and test requests to the selected machine', async () => {
  const fetcher = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response('{}'));
  const client = new InstanceClient('b'.repeat(32));
  const api = vi.fn((path: string, method?: string, body?: unknown) => client.json(path, method, body));
  const { root, changed } = mount({}, api);
  field(root, 'secret').value = 'private-token'; submit(root, 'credential'); await vi.waitFor(() => expect(changed).toHaveBeenCalled());
  root.querySelector<HTMLButtonElement>('[data-confluence-test]')!.click(); await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
  expect(fetcher.mock.calls.map(call => call[0])).toEqual([`/api/v1/swarm/instances/${'b'.repeat(32)}/plugins/confluence/credential`, `/api/v1/swarm/instances/${'b'.repeat(32)}/plugins/confluence/test`]);
});
