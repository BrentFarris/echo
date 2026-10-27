import { test, expect, type Page } from '@playwright/test';

async function state(page: Page) { return (await page.request.get('/api/v1/state')).json(); }
async function send(page: Page, prompt: string) {
  await page.getByRole('textbox', { name: 'Message', exact: true }).fill(prompt);
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
}
async function newChat(page: Page) {
  const created = page.waitForResponse(response => response.url().endsWith('/api/v1/chats') && response.request().method() === 'POST');
  await page.getByRole('button', { name: '＋ New chat', exact: true }).click();
  const chat = await (await created).json();
  await expect.poll(() => new URL(page.url()).hash).toBe('#' + chat.id);
  await expect(page.getByRole('textbox', { name: 'Message', exact: true })).toBeEnabled();
  return chat.id;
}
async function editBot(page: Page, name: string) {
  await page.getByRole('button', { name: '⚙ Settings', exact: true }).click();
  await page.locator('.list-card').filter({ has: page.locator('strong', { hasText: new RegExp(`^${name}$`) }) }).getByRole('button', { name: 'Edit', exact: true }).click();
  return page.getByRole('dialog').last();
}

test.beforeEach(async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: '＋ New chat', exact: true })).toBeVisible();
  await page.request.post('/api/v1/session', { data: {} });
  const initial = await state(page);
  for (const chat of initial.chats) await page.request.delete('/api/v1/chats/' + chat.id);
  for (const bot of initial.bots) if (bot.id !== 'echo') await page.request.delete('/api/v1/bots/' + bot.id);
  for (const schedule of initial.schedules) await page.request.delete('/api/v1/schedules/' + schedule.id);
  for (const plugin of initial.plugins) if (!plugin.builtin) await page.request.patch('/api/v1/plugins/' + plugin.id, { data: { enabled: false, config: plugin.config } });
  await page.request.post('/api/v1/bots', { data: { ...initial.bots.find((b: any) => b.id === 'echo'), enableMultitasking: false } });
  await page.request.post('/api/v1/endpoints', { data: { endpoint: { id: 'test', name: 'Test local model', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', contextTokens: 32768 } } });
  await page.reload();
  await newChat(page);
});

test.afterEach(async ({ page }) => {
  for (const chat of (await state(page)).chats) await page.request.post(`/api/v1/chats/${chat.id}/stop`, { data: {} });
});

test('checkbox defaults off, persists, and chat creation accepts multitasking', async ({ page }) => {
  await page.getByRole('button', { name: '⚙ Settings', exact: true }).click();
  await page.getByRole('button', { name: '＋ Add bot', exact: true }).click();
  let editor = page.getByRole('dialog').last();
  await expect(editor.getByRole('checkbox', { name: 'Enable Multitasking', exact: true })).not.toBeChecked();
  await editor.getByLabel('Handle', { exact: true }).fill('builder');
  await editor.getByLabel('Name', { exact: true }).fill('Builder');
  await editor.getByRole('checkbox', { name: 'Enable Multitasking', exact: true }).check();
  await editor.getByRole('button', { name: 'Save bot', exact: true }).click();
  await expect(page.getByRole('dialog')).toHaveCount(1);
  await page.locator('#settings').getByRole('button', { name: 'Close', exact: true }).click();
  await page.reload();
  editor = await editBot(page, 'Builder');
  await expect(editor.getByRole('checkbox', { name: 'Enable Multitasking', exact: true })).toBeChecked();
  await editor.getByRole('checkbox', { name: 'Enable Multitasking', exact: true }).uncheck();
  await editor.getByRole('button', { name: 'Save bot', exact: true }).click();
  await expect(page.getByRole('dialog')).toHaveCount(1);
  await page.locator('#settings').getByRole('button', { name: 'Close', exact: true }).click();
  expect((await state(page)).bots.find((b: any) => b.handle === 'builder').enableMultitasking).toBe(false);
  await send(page, 'Create a bot named parallel with multitasking enabled');
  await expect.poll(async () => (await state(page)).bots.find((b: any) => b.handle === 'parallel')?.enableMultitasking).toBe(true);
  await expect(page.getByRole('button', { name: 'Stop response' })).toBeHidden();
  await send(page, 'Create a bot named serial with multitasking disabled');
  await expect.poll(async () => (await state(page)).bots.find((b: any) => b.handle === 'serial')?.enableMultitasking).toBe(false);
});

test('queued chats display a status, survive reload, can be stopped, and advance', async ({ page }) => {
  const first = new URL(page.url()).hash.slice(1);
  await send(page, 'multitasking hold first');
  await expect(page.locator('.message.assistant')).toContainText('Holding this request.');
  const second = await newChat(page);
  await send(page, 'multitasking hold second');
  await expect(page.locator('.run-queued')).toHaveText('Echo Queued');
  await expect(page.locator('.message.assistant')).toHaveCount(0);
  await page.reload();
  await expect(page.locator('.run-queued')).toHaveText('Echo Queued');
  await page.getByRole('button', { name: 'Stop response' }).click();
  await expect(page.locator('.run-queued')).toHaveCount(0);
  expect((await state(page)).runs.find((r: any) => r.chatId === second).status).toBe('cancelled');
  await newChat(page);
  await send(page, 'multitasking hold third');
  await expect(page.locator('.run-queued')).toBeVisible();
  await page.request.post(`/api/v1/chats/${first}/stop`, { data: {} });
  await expect(page.locator('.run-queued')).toHaveCount(0);
  await expect(page.locator('.message.assistant')).toContainText('Holding this request.');
});

test('saving only the checkbox admits queued work without stopping active chats', async ({ page }) => {
  const first = new URL(page.url()).hash.slice(1);
  await send(page, 'multitasking hold first');
  await expect(page.locator('.message.assistant')).toContainText('Holding this request.');
  const second = await newChat(page);
  await send(page, 'multitasking hold second');
  await expect(page.locator('.run-queued')).toBeVisible();
  const revision = (await state(page)).bots.find((b: any) => b.id === 'echo').revision;
  const editor = await editBot(page, 'Echo');
  await editor.getByRole('checkbox', { name: 'Enable Multitasking', exact: true }).check();
  await editor.getByRole('button', { name: 'Save bot', exact: true }).click();
  await expect(page.getByRole('dialog')).toHaveCount(1);
  await page.locator('#settings').getByRole('button', { name: 'Close', exact: true }).click();
  await expect(page.locator('.message.assistant')).toContainText('Holding this request.');
  await expect(page.locator('.run-queued')).toHaveCount(0);
  const after = await state(page);
  expect(after.bots.find((b: any) => b.id === 'echo').revision).toBe(revision);
  for (const id of [first, second]) expect(after.runs.find((r: any) => r.chatId === id).status).toBe('running');
});
