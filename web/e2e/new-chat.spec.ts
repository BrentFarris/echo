import { test, expect, type Page } from '@playwright/test';

async function json(page: Page, path: string, method = 'GET', data?: unknown) {
  const response = await page.request.fetch('/api/v1' + path, { method, data });
  expect(response.ok(), await response.text()).toBe(true);
  return response.json();
}
async function setup(page: Page) {
  await page.goto('/');
  await expect(page.locator('#new-chat')).toBeVisible();
  const state = await json(page, '/state');
  for (const chat of state.chats) await json(page, `/chats/${chat.id}`, 'DELETE');
  for (const bot of state.bots) if (bot.id !== 'echo') await json(page, `/bots/${bot.id}`, 'DELETE');
  await json(page, '/endpoints', 'POST', { endpoint: { id: 'test', name: 'Test model', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', contextTokens: 32768 } });
  const bot = await json(page, '/bots', 'POST', { handle: 'partner', name: 'Partner', roots: [], plugins: {} });
  await page.reload();
  await expect(page.getByRole('combobox', { name: 'Bot for new chat' }).locator(`option[value="${bot.id}"]`)).toBeAttached();
  return bot;
}
async function createChat(page: Page, shortcut = false) {
  const created = page.waitForResponse(response => response.url().endsWith('/api/v1/chats') && response.request().method() === 'POST');
  if (shortcut) await page.keyboard.press('Control+n');
  else await page.locator('#new-chat').click();
  const chat = await (await created).json();
  await expect(page).toHaveURL(new RegExp('#' + chat.id + '$'));
  await expect(page.locator('#composer')).toBeEnabled();
  return chat.id as string;
}

test('a bot can create a group chat with a visible origin and a working link', async ({ page }) => {
  const bot = await setup(page);
  for (const group of await json(page, '/bot-groups')) await json(page, `/bot-groups/${group.id}`, 'DELETE');
  await json(page, '/bot-groups', 'POST', { handle: 'delegationmods', members: [{ botId: bot.id }] });
  const parent = await createChat(page);
  await page.locator('#composer').fill('Chat delegation fixture');
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(page.locator('.chat-log')).toContainText('Delegated the fixture task.');
  const messages = await json(page, `/chats/${parent}/messages`);
  const receipt = JSON.parse(messages.find((message: any) => message.role === 'tool' && message.author.endsWith(' · chat_start')).content);
  expect(receipt.chatId).not.toBe(parent);
  await page.getByText('Show work', { exact: true }).click();
  await page.getByRole('link', { name: 'Open delegated chat' }).click();
  await expect(page).toHaveURL(new RegExp('#' + receipt.chatId + '$'));
  await expect(page.locator('.handoff-label')).toHaveText('Delegated by @echo');
  await expect(page.locator('.chat-log')).toContainText('Implement DELEGATE-1 reported by @unknown.');
  const child = await json(page, `/chats/${receipt.chatId}/messages`);
  expect(child[0].recipients).toEqual([bot.id]);
  expect(child[0].segments[0].type).toBe('group');
  await page.reload();
  await expect(page.locator('.handoff-label')).toHaveText('Delegated by @echo');
});

test('new chat selection preserves drafts, applies to shortcuts, and resets on reload', async ({ page }) => {
  const bot = await setup(page), select = page.getByRole('combobox', { name: 'Bot for new chat' }), composer = page.locator('#composer');
  const value = () => composer.evaluate((el: any) => el.value as string);
  await expect(select).toHaveValue('echo');
  await createChat(page);
  await expect.poll(value).toBe('');
  await composer.fill('Previous draft');
  const previous = new URL(page.url()).hash.slice(1), count = (await json(page, '/state')).chats.length;
  await select.selectOption(bot.id);
  await expect.poll(value).toBe('Previous draft');
  expect((await json(page, '/state')).chats).toHaveLength(count);
  const echoOption = await select.locator('option[value="echo"]').elementHandle();
  const first = await createChat(page);
  await expect.poll(value).toBe('@partner ');
  await expect(composer.locator('.bot-reference')).toHaveCount(1);
  await expect(composer).toBeFocused();
  expect(await composer.evaluate((el: any) => [el.selectionStart, el.selectionEnd])).toEqual([9, 9]);
  expect(await echoOption!.evaluate(el => el.isConnected)).toBe(true);
  await composer.pressSequentially('Keep this draft');
  await createChat(page, true);
  await expect(select).toHaveValue(bot.id);
  await expect.poll(value).toBe('@partner ');
  await page.locator(`[data-chat="${first}"]`).click();
  await expect.poll(value).toBe('@partner Keep this draft');
  await select.selectOption('echo');
  await expect.poll(value).toBe('@partner Keep this draft');
  await createChat(page);
  await expect.poll(value).toBe('');
  await page.locator(`[data-chat="${previous}"]`).click();
  await expect.poll(value).toBe('Previous draft');
  await expect(select).toHaveValue('echo');
  await select.selectOption(bot.id);
  await page.reload();
  await expect(select).toHaveValue('echo');
});

test('the selector fits the sidebar and keyboard creation sends only to the chosen bot', async ({ page }) => {
  const bot = await setup(page), select = page.getByRole('combobox', { name: 'Bot for new chat' }), composer = page.locator('#composer');
  const checkBounds = async () => {
    const sidebar = (await page.locator('.sidebar').boundingBox())!, button = (await page.locator('#new-chat').boundingBox())!, picker = (await select.boundingBox())!;
    expect(picker.x).toBeGreaterThanOrEqual(button.x + button.width);
    expect(Math.abs(picker.y - button.y)).toBeLessThan(2);
    expect(picker.width).toBeGreaterThan(40);
    expect(picker.x + picker.width).toBeLessThanOrEqual(sidebar.x + sidebar.width);
  };
  await checkBounds();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole('button', { name: 'Toggle sidebar' }).click();
  await checkBounds();
  await select.focus(); await select.press('ArrowDown');
  await expect(select).toHaveValue(bot.id);
  await select.press('Shift+Tab');
  await expect(page.locator('#new-chat')).toBeFocused();
  await page.keyboard.press('Enter');
  await expect(composer).toBeEnabled();
  await expect.poll(() => composer.evaluate((el: any) => el.value)).toBe('@partner ');
  await expect(composer).toBeFocused();
  await composer.pressSequentially('hello');
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  const chatId = new URL(page.url()).hash.slice(1);
  await expect.poll(async () => (await json(page, `/chats/${chatId}/messages`)).filter((message: any) => message.role === 'user').length).toBe(1);
  const message = (await json(page, `/chats/${chatId}/messages`)).find((message: any) => message.role === 'user');
  expect(message.content).toBe('@partner hello');
  expect(message.recipients).toEqual([bot.id]);
  expect(message.segments).toContainEqual(expect.objectContaining({ type: 'bot', botId: bot.id, text: '@partner', range: { start: 0, end: 8 } }));
  await expect(page.getByRole('button', { name: 'Stop response' })).toBeHidden();
});

test('inventory updates preserve selected IDs, sort handles, and reset deleted bots to Echo', async ({ page }) => {
  const bot = await setup(page), select = page.getByRole('combobox', { name: 'Bot for new chat' }), composer = page.locator('#composer');
  await select.selectOption(bot.id);
  await composer.fill('Keep this draft');
  const alpha = await json(page, '/bots', 'POST', { handle: 'alpha', name: 'A <bot> & friend', roots: [], plugins: {} });
  await expect(select.locator('option')).toHaveText(['Echo', 'A <bot> & friend (@alpha)', 'Partner (@partner)']);
  await json(page, '/bots', 'POST', { ...bot, handle: 'renamed', name: 'Renamed bot' });
  await expect(select.locator(`option[value="${bot.id}"]`)).toHaveText('Renamed bot (@renamed)');
  await expect(select).toHaveValue(bot.id);
  await expect(composer).toHaveText('Keep this draft');
  await createChat(page);
  await expect.poll(() => composer.evaluate((el: any) => el.value)).toBe('@renamed ');
  await json(page, `/bots/${bot.id}`, 'DELETE');
  await expect(select).toHaveValue('echo');
  await expect(select.locator(`option[value="${bot.id}"]`)).toHaveCount(0);
  await expect(select.locator(`option[value="${alpha.id}"]`)).toBeAttached();
  await createChat(page);
  await expect.poll(() => composer.evaluate((el: any) => el.value)).toBe('');
});

test('delayed creation captures the choice and keeps its mention out of another chat', async ({ page }) => {
  const bot = await setup(page), select = page.getByRole('combobox', { name: 'Bot for new chat' }), composer = page.locator('#composer');
  const previous = await createChat(page);
  await composer.fill('Previous draft');
  await select.selectOption(bot.id);
  let releaseCreate!: () => void, releaseHistory!: () => void;
  const createGate = new Promise<void>(resolve => { releaseCreate = resolve; }), historyGate = new Promise<void>(resolve => { releaseHistory = resolve; });
  await page.route('**/api/v1/chats', async route => { if (route.request().method() === 'POST') await createGate; await route.continue(); });
  await page.route('**/api/v1/chats/*/messages', async route => { if (route.request().method() === 'GET' && !route.request().url().endsWith(`/chats/${previous}/messages`)) await historyGate; await route.continue(); });
  try {
    const creating = page.waitForRequest(request => request.url().endsWith('/api/v1/chats') && request.method() === 'POST');
    const created = page.waitForResponse(response => response.url().endsWith('/api/v1/chats') && response.request().method() === 'POST');
    await page.locator('#new-chat').click(); await creating;
    await select.selectOption('echo');
    releaseCreate();
    const chatId = (await (await created).json()).id;
    await expect(page).toHaveURL(new RegExp('#' + chatId + '$'));
    await expect.poll(() => composer.evaluate((el: any) => el.value)).toBe('@partner ');
    await page.locator(`[data-chat="${previous}"]`).click();
    await expect.poll(() => composer.evaluate((el: any) => el.value)).toBe('Previous draft');
    const loaded = page.waitForResponse(response => response.url().endsWith(`/chats/${chatId}/messages`));
    releaseHistory(); await (await loaded).finished();
    await expect(composer).toBeEnabled();
    await expect.poll(() => composer.evaluate((el: any) => el.value)).toBe('Previous draft');
    await page.locator(`[data-chat="${chatId}"]`).click();
    await expect.poll(() => composer.evaluate((el: any) => el.value)).toBe('@partner ');
    await expect(composer.locator('.bot-reference')).toHaveCount(1);
    await expect(select).toHaveValue('echo');
  } finally { releaseCreate(); releaseHistory(); }
});
