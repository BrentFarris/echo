import { test, expect, type Page } from '@playwright/test';

async function send(page: Page, prompt: string) {
  await page.getByRole('textbox', { name: 'Message', exact: true }).fill(prompt);
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
}

test('stopping and continuing keeps the cancellation inline with its original response', async ({ page }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: '＋ New chat', exact: true })).toBeVisible();
  await page.request.post('/api/v1/session', { data: {} });
  await page.request.post('/api/v1/endpoints', { data: { endpoint: { id: 'test', name: 'Test local model', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', contextTokens: 32768 } } });
  await page.reload();
  const created = page.waitForResponse(response => response.url().endsWith('/api/v1/chats') && response.request().method() === 'POST');
  await page.getByRole('button', { name: '＋ New chat', exact: true }).click();
  const { id: chatId } = await (await created).json();
  await expect.poll(() => new URL(page.url()).hash).toBe('#' + chatId);
  try {
    await send(page, 'multitasking hold original');
    await expect(page.locator('.message.assistant')).toContainText('Holding this request.');
    await page.getByRole('button', { name: 'Stop response', exact: true }).click();
    await expect(page.getByRole('button', { name: 'Stop response', exact: true })).toBeHidden();
    await expect(page.locator('.run-error')).toContainText('context canceled');
    await send(page, 'multitasking hold continuation');
    await expect(page.locator('.response-group')).toHaveCount(2);
    await expect(page.locator('.response-group').last()).toContainText('Holding this request.');
    const checkOrder = async () => {
      await expect(page.locator('.run-error')).toHaveCount(1);
      await expect(page.locator('.run-error + .message.user')).toContainText('multitasking hold continuation');
      await expect(page.locator('.response-group').last().locator('.badge.status-running')).toHaveText('Working');
      await expect(page.locator('.chat-log > :last-child')).toHaveClass('response-group');
    };
    await checkOrder();
    await page.reload();
    await checkOrder();
    await page.request.post(`/api/v1/chats/${chatId}/stop`, { data: {} });
    await expect(page.getByRole('button', { name: 'Stop response', exact: true })).toBeHidden();
    await send(page, 'Continue and finish');
    await expect(page.locator('.response-group').last()).toContainText('Completed the requested work.');
    await expect(page.locator('.chat-log > :last-child')).toHaveClass('response-group');
  } finally {
    await page.request.post(`/api/v1/chats/${chatId}/stop`, { data: {} });
    await page.request.delete(`/api/v1/chats/${chatId}`);
  }
});
