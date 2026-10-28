import { test, expect, type Page } from '@playwright/test';

async function setup(page: Page) {
  await page.goto('/');
  await expect(page.locator('#new-chat')).toBeAttached();
  await expect.poll(() => page.evaluate(() => localStorage.getItem('echo-notification-device'))).toBeTruthy();
  await page.request.post('/api/v1/endpoints', { data: { endpoint: { id: 'notification-model', name: 'Notification test', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', contextTokens: 32768 } } });
}

test('completion toast opens its chat and final reply, including after reload', async ({ page }) => {
  await setup(page);
  const response = await page.request.post('/api/v1/chats', { data: {} });
  const chat = await response.json();
  await page.getByRole('button', { name: '▦ Apps', exact: true }).click();
  const sent = await page.request.post(`/api/v1/chats/${chat.id}/messages`, { data: { content: 'Notification navigation test', generation: { endpointId: 'notification-model', model: 'echo-test', temperature: .6 } } });
  expect(sent.ok()).toBe(true);
  const toast = page.locator('.chat-notification').filter({ hasText: 'Echo finished' });
  await expect(toast).toBeVisible();
  await page.screenshot({ path: 'test-results/notification-completion-toast.png' });
  await toast.locator('.chat-notification-open').click();
  await expect(page).toHaveURL(new RegExp(`#${chat.id}\\?bot=echo&message=`));
  const hash = new URL(page.url()).hash;
  const message = new URLSearchParams(hash.split('?')[1]).get('message')!;
  await expect(page.locator(`[id="message-${message}"]`)).toBeVisible();
  await page.reload();
  await expect(page.locator(`[id="message-${message}"]`)).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`#${chat.id}\\?bot=echo&message=`));
  await page.request.delete(`/api/v1/chats/${chat.id}`);
});

test('notification settings persist separately for this device on a narrow screen', async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await setup(page);
  await page.getByRole('button', { name: 'Toggle sidebar', exact: true }).click();
  await page.getByRole('button', { name: '⚙ Settings', exact: true }).click();
  await page.getByRole('button', { name: 'Notifications', exact: true }).click();
  await expect(page.getByRole('heading', { name: 'Notifications on this device' })).toBeVisible();
  await page.getByLabel('Completion sound', { exact: true }).uncheck();
  await expect.poll(async () => JSON.parse(await page.evaluate(() => localStorage.getItem('echo-notification-preferences')) || '{}').completionSound).toBe(false);
  await expect(page.getByLabel('Question sound', { exact: true })).toBeChecked();
  await expect(page.getByText('Mobile push requires HTTPS', { exact: false })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: 'test-results/notification-settings-mobile.png' });
});

test('deleted notification target is explained instead of opening an unrelated chat', async ({ page }) => {
  await setup(page);
  await page.goto('/#deleted-chat?bot=echo&message=deleted-message');
  await expect(page.getByText('This chat is no longer available.', { exact: true })).toBeVisible();
});

test('question toast reveals the pending card and the chosen bot tab', async ({ page }) => {
  await setup(page);
  const handle = 'notify' + Date.now().toString(36);
  const botResponse = await page.request.post('/api/v1/bots', { data: { handle, name: 'Notification bot', prompt: 'Help with the request.', roots: [], plugins: {} } });
  expect(botResponse.ok()).toBe(true); const bot = await botResponse.json();
  const response = await page.request.post('/api/v1/chats', { data: {} }); const chat = await response.json();
  await page.getByRole('button', { name: '▦ Apps', exact: true }).click();
  await page.request.post(`/api/v1/chats/${chat.id}/messages`, { data: { content: `@echo @${handle} notification question fixture`, generation: { endpointId: 'notification-model', model: 'echo-test' } } });
  const toast = page.locator('.chat-notification').filter({ hasText: 'Notification bot has a question' });
  await expect(toast).toBeVisible(); await toast.locator('.chat-notification-open').click();
  await expect(page.getByRole('tab', { name: /^Notification bot/ })).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByText('Which notification approach?', { exact: true })).toBeVisible();
  await expect(page.locator('.question-card')).toHaveAttribute('open', '');
  await page.getByRole('button', { name: 'Skip', exact: true }).click();
  await expect(page.getByText('Questions skipped.', { exact: true })).toBeAttached();
  await page.request.delete(`/api/v1/chats/${chat.id}`);
  await page.request.delete(`/api/v1/bots/${bot.id}`);
});
