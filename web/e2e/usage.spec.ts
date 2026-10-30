import { test, expect, type Page } from '@playwright/test';

async function api(page: Page, path: string, method = 'GET', data?: unknown) {
  const response = await page.request.fetch('/api/v1' + path, { method, data });
  expect(response.ok(), await response.text()).toBe(true); return response.json();
}
async function detach(page: Page) {
  const directory = await api(page, '/swarm/peers');
  for (const peer of directory.peers) if (peer.authorized) await api(page, `/swarm/peers/${peer.node.nodeId}`, 'DELETE');
}
async function prepare(page: Page, prefix = '') {
  const state = await api(page, prefix + '/state');
  for (const chat of state.chats) await api(page, prefix + '/chats/' + chat.id, 'DELETE');
  for (const schedule of state.schedules) await api(page, prefix + '/schedules/' + schedule.id, 'DELETE');
  await api(page, prefix + '/endpoints', 'POST', { endpoint: { id: 'usage-test', name: 'Usage test', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', contextTokens: 32768 } });
  await api(page, prefix + '/usage/reset', 'POST', { requestId: crypto.randomUUID() });
}
async function generate(page: Page, prefix = '', recipient = prefix ? '@linux.echo ' : '') {
  const generation = { endpointId: 'usage-test', model: 'echo-test', temperature: .6, thinking: '' };
  const chat = await api(page, prefix + '/chats', 'POST', { generation });
  await api(page, prefix + `/chats/${chat.id}/messages`, 'POST', { content: recipient + 'usage statistics fixture', generation, requestId: crypto.randomUUID() });
  await expect.poll(async () => (await api(page, prefix + '/state')).runs.find((r: any) => r.chatId === chat.id)?.status).toBe('completed');
  return chat.id as string;
}
test.beforeEach(async ({ page }) => {
  await page.goto('/'); await expect(page.locator('#settings-button')).toBeVisible();
  await detach(page); await prepare(page);
});
test.afterEach(async ({ page }) => { await detach(page); });

test('completed turn footer, General preference, stable details and confirmed reset', async ({ page }) => {
  const chat = await generate(page);
  await page.goto('/#' + chat);
  const footer = page.locator('.turn-statistics');
  await expect(footer).toContainText('2,468 in · 112 out · 1 tool call · 2 model calls');
  await expect(page.locator('.response-turn > .response-group + .turn-statistics')).toHaveCount(1);
  await page.locator('#settings-button').click();
  await expect(page.locator('.settings-nav button').first()).toHaveText('General');
  const toggle = page.getByLabel('Show turn statistics');
  await expect(toggle).toBeChecked();
  await expect(page.locator('[data-direction=input]')).toHaveText('2,468');
  const before = await page.locator('.usage-totals').boundingBox();
  await page.getByRole('button', { name: 'Input token details' }).hover();
  await expect(page.locator('#input-usage-details')).toBeVisible();
  await expect(page.locator('#input-usage-details')).toContainText('2,468 input tokens');
  expect(await page.locator('.usage-totals').boundingBox()).toEqual(before);
  await toggle.uncheck(); await expect(footer).toHaveCount(0);
  await page.reload(); await expect(footer).toHaveCount(0);
  await page.locator('#settings-button').click(); await expect(toggle).not.toBeChecked();
  await toggle.check(); await expect(footer).toHaveCount(1);
  await page.screenshot({ path: test.info().outputPath('general-usage.png') });
  await page.locator('[data-section=development]').click();
  const reset = page.getByRole('button', { name: 'Reset token totals…', exact: true });
  await expect(reset).toHaveClass('danger');
  page.once('dialog', dialog => dialog.dismiss()); await reset.click();
  await expect(reset).toBeEnabled(); expect((await api(page, '/usage')).inputTokens).toBe(2468);
  page.once('dialog', dialog => dialog.accept()); await reset.click();
  await expect(page.locator('[data-reset-status]')).toContainText(': reset');
  expect((await api(page, '/usage')).inputTokens).toBe(0);
  await page.locator('[data-section=general]').click(); await expect(page.locator('[data-direction=input]')).toHaveText('0');
  await page.locator('#settings .dialog-heading button').click(); await expect(footer).toContainText('2,468 in');
  await page.screenshot({ path: test.info().outputPath('turn-statistics.png') });
  await page.locator('#settings-button').click();
  await page.setViewportSize({ width: 390, height: 844 });
  const input = page.getByRole('button', { name: 'Input token details' });
  const mobileBefore = await page.locator('.usage-totals').boundingBox();
  await input.click();
  const panel = page.locator('#input-usage-details'); await expect(panel).toBeVisible();
  const box = (await panel.boundingBox())!;
  expect(box.x).toBeGreaterThanOrEqual(0); expect(box.x + box.width).toBeLessThanOrEqual(390);
  expect(box.y + box.height).toBeLessThanOrEqual(844);
  expect(await page.locator('.usage-totals').boundingBox()).toEqual(mobileBefore);
  await input.press('Escape'); await expect(panel).toBeHidden();
  await expect(page.locator('#settings')).toBeVisible();
  await page.screenshot({ path: test.info().outputPath('general-usage-mobile.png') });
});

test('online machine totals exclude disconnected peers and reset only reachable machines', async ({ page }) => {
  const peer = await api(page, '/swarm/peers', 'POST', { url: 'http://127.0.0.1:3744', password: 'echo-test-password' });
  const prefix = `/swarm/instances/${peer.nodeId}`;
  // The remote worker's answer is replicated to a local chat home; that must
  // not count its usage on the home machine a second time.
  await prepare(page, prefix); await generate(page); await generate(page, '', '@linux.echo ');
  expect((await api(page, '/usage')).inputTokens).toBe(2468);
  expect((await api(page, prefix + '/usage')).inputTokens).toBe(2468);
  await page.reload(); await page.locator('#settings-button').click();
  await expect(page.locator('[data-direction=input]')).toHaveText('4,936');
  await page.getByRole('button', { name: 'Input token details' }).focus();
  await expect(page.locator('#input-usage-details')).toContainText('linux');
  await api(page, `/swarm/peers/${peer.nodeId}`, 'PATCH', { disconnected: true });
  await expect(page.locator('[data-direction=input]')).toHaveText('2,468');
  await expect(page.locator('[data-usage-status]')).toContainText('excluded');
  await page.locator('[data-section=development]').click();
  page.once('dialog', dialog => { expect(dialog.message()).toContain('Skipped'); void dialog.accept(); });
  await page.getByRole('button', { name: 'Reset token totals…', exact: true }).click();
  await expect(page.locator('[data-reset-status]')).toContainText('linux: skipped');
  await api(page, `/swarm/peers/${peer.nodeId}`, 'PATCH', { disconnected: false });
  await expect.poll(async () => (await api(page, '/swarm/state')).instances.find((i: any) => i.node.nodeId === peer.nodeId)?.online).toBe(true);
  await page.locator('[data-section=general]').click();
  await expect(page.locator('[data-direction=input]')).toHaveText('2,468');
  expect((await api(page, prefix + '/usage')).inputTokens).toBe(2468);
});
