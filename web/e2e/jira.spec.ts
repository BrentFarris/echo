import { test, expect } from '@playwright/test';
import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';

test('Jira settings store credentials and test the selected machine connection', async ({ page }) => {
  const jira = createServer((request, response) => {
    if (request.url === '/jira/rest/api/2/myself' && request.headers.authorization === 'Bearer browser-jira-token') {
      response.setHeader('Content-Type', 'application/json'); response.end(JSON.stringify({ displayName: 'Browser Jira reader', name: 'reader' }));
    } else { response.statusCode = 401; response.end('{}'); }
  });
  await new Promise<void>(resolve => jira.listen(0, '127.0.0.1', resolve));
  let node = '';
  try {
    await page.goto('/'); await expect(page.locator('#settings-button')).toBeVisible();
    const before = await (await page.request.get('/api/v1/state')).json();
    const localJira = before.plugins.find((p: any) => p.id === 'jira');
    const paired = await page.request.post('/api/v1/swarm/peers', { data: { url: 'http://127.0.0.1:3744', password: 'echo-test-password' } });
    expect(paired.ok(), await paired.text()).toBe(true); node = (await paired.json()).nodeId;
    await page.reload(); await page.locator('#settings-button').click();
    await page.getByLabel('Settings machine').selectOption(node);
    await page.locator('.settings-nav [data-section="jira"]').click();
    await page.getByLabel('Deployment type').selectOption('datacenter');
    await page.getByLabel('Jira site URL').fill(`http://127.0.0.1:${(jira.address() as AddressInfo).port}/jira`);
    await page.getByRole('checkbox', { name: 'Enable Jira', exact: true }).check();
    await page.getByRole('button', { name: 'Save settings', exact: true }).click();
    await expect(page.locator('[data-jira-status]')).toHaveText('No credential saved');
    await page.getByLabel('API token / personal access token').fill('browser-jira-token');
    await page.getByRole('button', { name: 'Save token', exact: true }).click();
    await expect(page.locator('[data-jira-status]')).toHaveText('Credential is saved');
    await expect(page.getByLabel('API token / personal access token')).toHaveValue('');
    await page.getByRole('button', { name: 'Test connection', exact: true }).click();
    await expect(page.locator('[data-jira-identity]')).toHaveText('Connected as Browser Jira reader');
    const local = await (await page.request.get('/api/v1/state')).json();
    expect(local.plugins.find((p: any) => p.id === 'jira')).toEqual(localJira);
    const remoteState = await (await page.request.get(`/api/v1/swarm/instances/${node}/state`)).text();
    expect(remoteState).not.toContain('browser-jira-token');
    await page.reload(); await page.locator('#settings-button').click(); await page.getByLabel('Settings machine').selectOption(node); await page.locator('.settings-nav [data-section="jira"]').click();
    await expect(page.locator('[data-jira-status]')).toHaveText('Credential is saved');
    await page.getByRole('button', { name: 'Clear credential', exact: true }).click();
    await expect(page.locator('[data-jira-status]')).toHaveText('No credential saved');
  } finally {
    if (node) {
      await page.request.post(`/api/v1/swarm/instances/${node}/plugins/jira/credential`, { data: { secret: '' } });
      await page.request.patch(`/api/v1/swarm/instances/${node}/plugins/jira`, { data: { enabled: false, config: { url: '', email: '', cloudId: '', deployment: 'cloud' } } });
      await page.request.delete(`/api/v1/swarm/peers/${node}`);
    }
    await new Promise<void>((resolve, reject) => jira.close(error => error ? reject(error) : resolve()));
  }
});
