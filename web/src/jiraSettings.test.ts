import { afterEach, expect, it, vi } from 'vitest';
import { mountJiraSettings } from './jiraSettings';
import type { Plugin } from './types';
import { InstanceClient } from './instances';

afterEach(() => { document.body.replaceChildren(); vi.restoreAllMocks(); });

function mount(config: Record<string, unknown> = {}, api = vi.fn().mockResolvedValue({ displayName: '<script>Jira reader</script>' })) {
  const root = document.createElement('div'); document.body.append(root);
  const changed = vi.fn().mockResolvedValue(undefined), notice = vi.fn();
  mountJiraSettings(root, { id: 'jira', enabled: false, config } as Plugin, api, changed, notice);
  return { root, api, changed, notice };
}
function input(root: HTMLElement, name: string) { return root.querySelector<HTMLInputElement>(`[name="${name}"]`)!; }
function submit(root: HTMLElement, selector: string) { root.querySelector(selector)!.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })); }

it('saves Cloud connection settings without putting a token into plugin config', async () => {
  const { root, api, changed } = mount();
  input(root, 'url').value = 'https://example.atlassian.net'; input(root, 'email').value = 'reader@example.com'; input(root, 'cloudId').value = 'cloud-1'; input(root, 'enabled').checked = true;
  submit(root, '[data-jira-settings]');
  await vi.waitFor(() => expect(changed).toHaveBeenCalledOnce());
  expect(api).toHaveBeenCalledWith('/plugins/jira', 'PATCH', { enabled: true, config: { deployment: 'cloud', url: 'https://example.atlassian.net', email: 'reader@example.com', cloudId: 'cloud-1' } });
  expect(input(root, 'secret').type).toBe('password');
});

it('switches to Data Center and removes Cloud-only settings from the saved connection', async () => {
  const { root, api } = mount({ email: 'old@example.com', cloudId: 'old' });
  input(root, 'url').value = 'http://jira.internal/jira';
  input(root, 'deployment').value = 'datacenter'; input(root, 'deployment').dispatchEvent(new Event('change'));
  expect(root.querySelector<HTMLElement>('[data-cloud]')!.hidden).toBe(true);
  submit(root, '[data-jira-settings]');
  await vi.waitFor(() => expect(api).toHaveBeenCalledWith('/plugins/jira', 'PATCH', { enabled: false, config: { deployment: 'datacenter', url: 'http://jira.internal/jira', email: '', cloudId: '' } }));
});

it('writes and clears tokens through the protected endpoint and renders identity as text', async () => {
  const { root, api, changed } = mount({ credential: 'vault-reference' });
  expect(root.textContent).toContain('Credential is saved'); expect(root.innerHTML).not.toContain('vault-reference');
  input(root, 'secret').value = 'private-token'; submit(root, '[data-jira-credential]');
  expect(input(root, 'secret').value).toBe('');
  await vi.waitFor(() => expect(changed).toHaveBeenCalledTimes(1));
  expect(api).toHaveBeenCalledWith('/plugins/jira/credential', 'POST', { secret: 'private-token' });
  root.querySelector<HTMLButtonElement>('[data-jira-test]')!.click();
  await vi.waitFor(() => expect(root.querySelector('[data-jira-identity]')!.textContent).toBe('Connected as <script>Jira reader</script>'));
  expect(root.querySelector('script')).toBeNull();
  root.querySelector<HTMLButtonElement>('[data-jira-clear]')!.click();
  await vi.waitFor(() => expect(changed).toHaveBeenCalledTimes(2));
  expect(api).toHaveBeenCalledWith('/plugins/jira/credential', 'POST', { secret: '' });
  expect(root.textContent).toContain('No credential saved');
});

it('reports connection failure and clears the password field even if saving fails', async () => {
  const api = vi.fn().mockRejectedValue(new Error('Jira unavailable'));
  const { root, notice } = mount({}, api);
  input(root, 'secret').value = 'private-token'; submit(root, '[data-jira-credential]');
  await vi.waitFor(() => expect(notice).toHaveBeenCalled()); expect(input(root, 'secret').value).toBe('');
  root.querySelector<HTMLButtonElement>('[data-jira-test]')!.click();
  await vi.waitFor(() => expect(root.textContent).toContain('Connection test failed'));
  expect(root.querySelector<HTMLButtonElement>('[data-jira-test]')!.disabled).toBe(false);
});

it('keeps credential and connection requests bound to the selected machine', async () => {
  const fetcher = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response(JSON.stringify({ displayName: 'Remote reader' })));
  const client = new InstanceClient('a'.repeat(32));
  const api = vi.fn((path: string, method?: string, body?: unknown) => client.json(path, method, body));
  const { root, changed } = mount({}, api);
  input(root, 'secret').value = 'private-token'; submit(root, '[data-jira-credential]');
  await vi.waitFor(() => expect(changed).toHaveBeenCalled());
  root.querySelector<HTMLButtonElement>('[data-jira-test]')!.click();
  await vi.waitFor(() => expect(fetcher).toHaveBeenCalledTimes(2));
  expect(fetcher.mock.calls.map(call => call[0])).toEqual([`/api/v1/swarm/instances/${'a'.repeat(32)}/plugins/jira/credential`, `/api/v1/swarm/instances/${'a'.repeat(32)}/plugins/jira/test`]);
});
