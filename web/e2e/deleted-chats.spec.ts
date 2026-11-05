import { test, expect, type Page } from '@playwright/test';

async function setup(page: Page) {
  await page.goto('/'); await expect(page.locator('#new-chat')).toBeVisible();
  const state = await (await page.request.get('/api/v1/state')).json();
  await page.request.delete('/api/v1/endpoints/retention-test');
  for (const chat of state.chats) {
    if (!chat.deletedAt) await page.request.delete(`/api/v1/chats/${chat.id}`);
    await page.request.delete(`/api/v1/chats/${chat.id}/permanent`);
  }
  await page.request.post('/api/v1/endpoints', { data: { endpoint: { id: 'test', name: 'Test local model', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', contextTokens: 32768 } } });
  await page.reload();
}

async function conversation(page: Page, text: string) {
  const created = page.waitForResponse(response => response.url().endsWith('/api/v1/chats') && response.request().method() === 'POST');
  await page.locator('#new-chat').click();
  const id = (await (await created).json()).id;
  await expect(page).toHaveURL(new RegExp('#' + id + '$'));
  await page.request.put(`/api/v1/chats/${id}/generation`, { data: { endpointId: 'test', model: 'echo-test', temperature: .6 } });
  await page.reload();
  await page.getByRole('textbox', { name: 'Message', exact: true }).fill(text);
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(page.locator('.message.assistant')).toContainText('Completed the requested work.');
  await expect(page.getByRole('button', { name: 'Stop response', exact: true })).toHaveCount(0);
  return id;
}

test('Deleted stays collapsed, groups chats, permits viewing and restores the same chat', async ({ page }) => {
  await setup(page);
  const id = await conversation(page, 'Retain this conversation');
  await page.locator(`[data-delete="${id}"]`).click();
  const root = page.locator('[data-deleted-root]');
  await expect(root).toHaveText('Deleted (1)');
  await expect(root).toHaveAttribute('aria-expanded', 'false');
  await expect(page.locator('.chat-row:not(.deleted-chat-row)')).toHaveCount(0);
  expect(new URL(page.url()).hash).not.toBe('#' + id);
  await root.click();
  const subgroup = page.locator('[data-deleted-group]');
  await expect(subgroup).toHaveCount(1); await expect(subgroup).toHaveAttribute('aria-expanded', 'false');
  await subgroup.press('Enter');
  const row = page.locator('.deleted-chat-row');
  await expect(row).toHaveCount(1); await expect(row.locator('.chat-dot')).toHaveCount(0);
  await row.locator('.chat-link').click();
  await expect(page.locator('.chat-log')).toContainText('Retain this conversation');
  await expect(page.locator('.deleted-chat-banner')).toContainText('Permanently removed');
  await expect(page.locator('.composer')).toBeHidden();
  await expect(page.locator('.message-action.edit:visible,.message-action.trash:visible,.message-action.fork:visible,.message-action.rerun:visible')).toHaveCount(0);
  await expect(page.locator('.message-action.copy').first()).toBeAttached();
  // Background state changes preserve both expansion levels.
  await page.request.post('/api/v1/chats', { data: {} });
  await expect(page.locator('.chat-row:not(.deleted-chat-row)')).toHaveCount(1);
  await expect(root).toHaveAttribute('aria-expanded', 'true');
  await expect(subgroup).toHaveAttribute('aria-expanded', 'true');
  await page.screenshot({ path: 'test-results/deleted-chat-desktop.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.locator('.deleted-chat-banner')).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await page.screenshot({ path: 'test-results/deleted-chat-mobile.png', fullPage: true });
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.reload();
  await expect(root).toHaveAttribute('aria-expanded', 'false');
  await expect(subgroup).toHaveAttribute('aria-expanded', 'false');
  await expect(page.locator('.deleted-chat-banner')).toBeVisible();
  await page.getByRole('button', { name: 'Restore', exact: true }).click();
  await expect(page).toHaveURL(new RegExp('#' + id + '$'));
  await expect(page.locator('.deleted-chat-banner')).toBeHidden();
  await expect(page.locator('.composer')).toBeVisible();
  await expect(page.locator(`[data-chat="${id}"]`)).toBeVisible();
  await expect(root).toHaveCount(0);
  await page.getByRole('textbox', { name: 'Message', exact: true }).fill('Continue restored conversation');
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(page.locator('.message.assistant')).toHaveCount(2);
});

test('permanent deletion requires confirmation and removes saved history', async ({ page }) => {
  await setup(page);
  const id = await conversation(page, 'Remove permanently');
  await page.locator(`[data-delete="${id}"]`).click();
  await page.locator('[data-deleted-root]').click();
  await page.locator('[data-deleted-group]').click();
  await page.locator(`[data-chat="${id}"]`).click();
  page.once('dialog', dialog => dialog.dismiss());
  await page.getByRole('button', { name: 'Delete permanently', exact: true }).click();
  await expect(page.locator('.deleted-chat-banner')).toBeVisible();
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Delete permanently', exact: true }).click();
  await expect(page.locator('[data-deleted-root]')).toHaveCount(0);
  await expect(page.locator('.deleted-chat-banner')).toBeHidden();
  expect((await page.request.get(`/api/v1/chats/${id}/messages`)).ok()).toBe(false);
});

test('an expired startup transcript does not prevent live sidebar updates', async ({ page }) => {
  await setup(page);
  const id = await conversation(page, 'Expired cached destination');
  await page.route(`**/api/v1/chats/${id}/messages`, route => route.fulfill({ status: 410, contentType: 'application/json', body: JSON.stringify({ error: 'This deleted chat has expired' }) }));
  await page.reload();
  await expect(page.locator('.notice[role="alert"]')).toContainText('expired');
  await expect(page.locator('#connection')).toHaveText('');
  await page.request.delete(`/api/v1/chats/${id}`);
  await page.request.delete(`/api/v1/chats/${id}/permanent`);
  const next = await (await page.request.post('/api/v1/chats', { data: {} })).json();
  await expect(page.locator(`[data-chat="${next.id}"]`)).toBeVisible();
  await expect(page.locator(`[data-chat="${id}"]`)).toHaveCount(0);
});
