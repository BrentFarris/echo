import { test, expect, type Page } from '@playwright/test';

async function setup(page: Page) {
  await page.goto('/');
  await page.request.post('/api/v1/session', { data: {} });
  await page.request.post('/api/v1/endpoints', { data: { endpoint: { id: 'actions-model', name: 'Actions model', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', contextTokens: 32768 } } });
  const response = await page.request.post('/api/v1/chats'); const chat = await response.json();
  await page.request.put(`/api/v1/chats/${chat.id}/generation`, { data: { endpointId: 'actions-model', model: 'echo-test', temperature: .6 } });
  await page.goto('/#' + chat.id); await page.reload();
  await page.getByRole('textbox', { name: 'Message', exact: true }).fill('Original **message**');
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(page.locator('.response-group > .message-actions')).toHaveCount(1);
  await expect(page.locator('.chat-log > .message.user [data-action="edit"]')).toHaveAttribute('aria-disabled', 'false');
  return chat.id as string;
}

test('hover, final-only copy, assistant edit, user resubmit and deletion persist', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  const chat = await setup(page);
  const observer = await context.newPage(); await observer.goto('/#' + chat);
  await expect(observer.locator('.response-group')).toHaveCount(1);
  const user = page.locator('.chat-log > .message.user').first(), response = page.locator('.response-group').first();
  const actions = user.locator(':scope > .message-actions');
  await page.getByRole('textbox', { name: 'Message', exact: true }).focus(); await page.mouse.move(0, 0);
  await expect(actions).toHaveCSS('opacity', '0'); await user.hover(); await expect(actions).toHaveCSS('opacity', '1');
  await user.getByRole('button', { name: 'Copy message', exact: true }).click();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe('Original **message**');
  await user.getByRole('button', { name: 'Fork chat from this message' }).click();
  await expect(page.locator('.message-edit-form')).toHaveCount(0);
  const before = await (await page.request.get(`/api/v1/chats/${chat}/messages`)).json();
  const final = before.filter((m: any) => m.role === 'assistant').at(-1);
  await response.hover(); await response.getByRole('button', { name: 'Copy final response' }).click();
  await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(final.content);
  await expect(page.locator('.work-content .message-actions')).toHaveCount(0);
  await response.getByRole('button', { name: 'Edit response', exact: true }).click();
  await page.getByRole('textbox', { name: 'Edit response text' }).fill('Edited **response**');
  await page.getByRole('button', { name: 'Save', exact: true }).click();
  await expect(page.locator('.message-edit-form')).toHaveCount(0);
  await expect(response).toContainText('Edited response');
  await expect(observer.locator('.response-group')).toContainText('Edited response');
  const edited = await (await page.request.get(`/api/v1/chats/${chat}/messages`)).json();
  expect(edited).toHaveLength(before.length); expect(edited.at(-1).content).toBe('Edited **response**');
  await page.reload(); await expect(response).toContainText('Edited response');
  await user.hover(); await user.getByRole('button', { name: 'Edit message', exact: true }).click();
  await page.getByRole('textbox', { name: 'Edit message text' }).fill('Replacement message');
  await page.getByRole('button', { name: 'Save & Resubmit', exact: true }).click();
  await expect(user).toContainText('Replacement message');
  await expect(user.locator('[data-action="trash"]')).toHaveAttribute('aria-disabled', 'false');
  await expect(page.locator('.chat-log')).not.toContainText('Edited response');
  const resubmitted = await (await page.request.get(`/api/v1/chats/${chat}/messages`)).json();
  expect(resubmitted.some((m: any) => m.id === final.id)).toBe(false);
  page.once('dialog', dialog => dialog.dismiss()); await response.hover(); await response.getByRole('button', { name: 'Delete response' }).click();
  await expect(response).toHaveCount(1);
  page.once('dialog', dialog => dialog.accept()); await response.getByRole('button', { name: 'Delete response' }).click();
  await expect(page.locator('.response-group > .message-actions')).toHaveCount(0);
  await expect(user).toContainText('Replacement message');
  page.once('dialog', dialog => dialog.accept()); await user.hover(); await user.getByRole('button', { name: 'Delete message', exact: true }).click();
  await expect(page.locator('.chat-log > .message.user')).toHaveCount(0);
  await expect(observer.locator('.chat-log > .message.user')).toHaveCount(0);
  await observer.close();
});

test.describe('touch', () => {
  test.use({ hasTouch: true, isMobile: true, viewport: { width: 390, height: 844 } });
  test('tap toggles the top-right toolbar and an outside tap closes it', async ({ page }) => {
    await setup(page);
    const user = page.locator('.chat-log > .message.user').first(), actions = user.locator(':scope > .message-actions');
    await expect(actions).toHaveCSS('opacity', '0');
    await user.locator('.message-content').tap(); await expect(actions).toHaveCSS('opacity', '1');
    const card = await user.boundingBox(), toolbar = await actions.boundingBox();
    expect(Math.abs(card!.x + card!.width - toolbar!.x - toolbar!.width)).toBeLessThan(12);
    expect(toolbar!.y - card!.y).toBeLessThan(12);
    await user.locator('.message-content').tap(); await expect(actions).toHaveCSS('opacity', '0');
    await user.locator('.message-content').tap(); await page.getByRole('textbox', { name: 'Message', exact: true }).tap(); await expect(actions).toHaveCSS('opacity', '0');
  });
});
