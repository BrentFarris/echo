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
  await page.locator('.settings-nav [data-section="bots"]').click();
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

test('chat shortcuts preserve drafts, close read chats, and respect dialogs and repeated keys', async ({ page }) => {
  const first = new URL(page.url()).hash.slice(1), composer = page.getByRole('textbox', { name: 'Message', exact: true });
  await composer.fill('Keep this draft');
  const created = page.waitForResponse(response => response.url().endsWith('/api/v1/chats') && response.request().method() === 'POST');
  await composer.press('Control+n');
  const second = (await (await created).json()).id;
  await expect(page).toHaveURL(new RegExp('#' + second + '$'));
  await expect(composer).toBeFocused();
  await expect.poll(() => composer.evaluate((el: any) => el.value)).toBe('');
  await page.locator(`[data-chat="${first}"]`).click();
  await expect.poll(() => composer.evaluate((el: any) => el.value)).toBe('Keep this draft');

  // Holding a shortcut must not create or delete multiple chats.
  for (const key of ['n', 'w']) await composer.dispatchEvent('keydown', { key, ctrlKey: true, repeat: true });
  expect((await state(page)).chats).toHaveLength(2);
  await page.getByRole('button', { name: '⚙ Settings', exact: true }).click();
  await page.keyboard.press('Control+n'); await page.keyboard.press('Control+w');
  await expect(page.locator('#settings')).toBeVisible();
  expect((await state(page)).chats).toHaveLength(2);
  await page.locator('#settings').getByRole('button', { name: 'Close', exact: true }).click();

  const prompts: string[] = [];
  page.on('dialog', async dialog => { prompts.push(dialog.message()); await dialog.dismiss(); });
  await composer.press('Control+w');
  await expect(page.locator(`[data-chat="${first}"]`)).toHaveCount(0);
  await expect(page).toHaveURL(new RegExp('#' + second + '$'));
  await page.locator('#apps-button').click();
  await page.keyboard.press('Control+w');
  await expect(page.locator(`[data-chat="${second}"]`)).toBeVisible();
  await page.locator(`[data-delete="${second}"]`).click();
  await expect(page.locator('.chat-row')).toHaveCount(0);
  await expect(page).toHaveURL('http://127.0.0.1:3742/#apps');
  const fromApps = page.waitForResponse(response => response.url().endsWith('/api/v1/chats') && response.request().method() === 'POST');
  await page.keyboard.press('Control+n');
  const third = (await (await fromApps).json()).id;
  await expect(page).toHaveURL(new RegExp('#' + third + '$'));
  await composer.press('Control+w');
  await expect(page.locator('.chat-row')).toHaveCount(0);
  await expect(page).toHaveURL('http://127.0.0.1:3742/');
  await composer.press('Control+w');
  expect(prompts).toEqual([]);
  expect((await state(page)).chats).toHaveLength(0);
});

for (const trigger of ['shortcut', 'X'] as const) for (const queued of [false, true]) {
  test(`${trigger} confirms before closing a ${queued ? 'queued' : 'running'} chat`, async ({ page }) => {
    const first = new URL(page.url()).hash.slice(1);
    await send(page, 'multitasking hold close first');
    await expect(page.locator('.message.assistant')).toContainText('Holding this request.');
    let target = first;
    if (queued) {
      target = await newChat(page);
      await send(page, 'multitasking hold close queued');
      await expect(page.locator('.run-queued')).toBeVisible();
    }
    const composer = page.getByRole('textbox', { name: 'Message', exact: true });
    await composer.fill('Unsent follow-up');
    // The X must also protect background chats without changing the selection.
    const selected = trigger === 'X' ? await newChat(page) : target;
    const prompts: string[] = []; let accept = false, deletes = 0;
    page.on('request', request => { if (request.method() === 'DELETE' && request.url().endsWith('/chats/' + target)) ++deletes; });
    page.on('dialog', async dialog => { prompts.push(dialog.message()); if (accept) await dialog.accept(); else await dialog.dismiss(); });
    const close = () => trigger === 'X' ? page.locator(`[data-delete="${target}"]`).click() : composer.press('Control+w');
    await close();
    expect(prompts).toHaveLength(1); expect(prompts[0]).toContain('still busy');
    expect(deletes).toBe(0);
    expect((await state(page)).chats.some((chat: any) => chat.id === target)).toBe(true);
    expect((await state(page)).runs.find((run: any) => run.chatId === target).status).toBe(queued ? 'queued' : 'running');
    await expect(page).toHaveURL(new RegExp('#' + selected + '$'));
    if (trigger === 'shortcut') await expect.poll(() => composer.evaluate((el: any) => el.value)).toBe('Unsent follow-up');
    accept = true;
    await close();
    await expect(page.locator(`[data-chat="${target}"]`)).toHaveCount(0);
    expect(deletes).toBe(1); expect(prompts).toHaveLength(2);
    expect((await state(page)).chats.some((chat: any) => chat.id === target)).toBe(false);
    if (queued) expect((await state(page)).runs.find((run: any) => run.chatId === first).status).toBe('running');
  });
}

test('unread results require confirmation, and opening the chat clears the close warning', async ({ page }) => {
  const unread = new URL(page.url()).hash.slice(1);
  await send(page, 'multitasking hold unread close');
  await expect(page.locator('.message.assistant')).toContainText('Holding this request.');
  const selected = await newChat(page);
  await page.request.post(`/api/v1/chats/${unread}/stop`, { data: {} });
  const row = page.locator('.chat-row').filter({ has: page.locator(`[data-chat="${unread}"]`) });
  await expect(row.locator('.chat-dot.done')).toBeVisible();
  const prompts: string[] = [];
  page.on('dialog', async dialog => { prompts.push(dialog.message()); await dialog.dismiss(); });
  await page.locator(`[data-delete="${unread}"]`).click();
  expect(prompts).toHaveLength(1); expect(prompts[0]).toContain('not been read');
  await expect(page).toHaveURL(new RegExp('#' + selected + '$'));
  expect((await state(page)).chats.some((chat: any) => chat.id === unread)).toBe(true);
  await page.locator(`[data-chat="${unread}"]`).click();
  await expect(row.locator('.chat-dot')).toHaveCount(0);
  await page.getByRole('textbox', { name: 'Message', exact: true }).press('Control+w');
  await expect(page.locator(`[data-chat="${unread}"]`)).toHaveCount(0);
  expect(prompts).toHaveLength(1);
});

test('a never-opened chat requires confirmation before the X deletes it', async ({ page }) => {
  const selected = new URL(page.url()).hash.slice(1);
  const unread = await (await page.request.post('/api/v1/chats', { data: {} })).json();
  await expect(page.locator(`[data-chat="${unread.id}"]`)).toBeVisible();
  const prompt = page.waitForEvent('dialog');
  const closing = page.locator(`[data-delete="${unread.id}"]`).click();
  const dialog = await prompt;
  expect(dialog.message()).toContain('not been read');
  expect((await state(page)).chats.some((chat: any) => chat.id === unread.id)).toBe(true);
  await dialog.accept(); await closing;
  await expect(page.locator(`[data-chat="${unread.id}"]`)).toHaveCount(0);
  await expect(page).toHaveURL(new RegExp('#' + selected + '$'));
});

test('checkbox defaults off, persists, and chat creation accepts multitasking', async ({ page }) => {
  await page.getByRole('button', { name: '⚙ Settings', exact: true }).click();
  await page.locator('.settings-nav [data-section="bots"]').click();
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

test('group tabs isolate queued and live bot threads and remember the selection across chats', async ({ page }) => {
  await expect(page.getByRole('tablist', { name: 'Bot threads' })).toBeHidden();
  for (const [handle, name] of [['partner', 'Partner'], ['third', 'Third'], ['fourth', 'Fourth']]) await page.request.post('/api/v1/bots', { data: { handle, name, roots: [], plugins: {} } });
  await send(page, 'multitasking hold first');
  await expect(page.locator('.message.assistant')).toContainText('Holding this request.');
  const group = await newChat(page);
  await page.reload();
  await send(page, '@echo @partner @third @fourth multitasking hold group');
  const echoTab = page.getByRole('tab', { name: 'Echo Queued', exact: true });
  const partnerTab = page.getByRole('tab', { name: 'Partner Working', exact: true });
  await expect(echoTab).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('.run-queued')).toHaveText('Echo Queued');
  await expect(partnerTab).toBeVisible();
  await partnerTab.click();
  await expect(page.locator('.run-queued')).toHaveCount(0);
  await expect(page.locator('.message.assistant')).toContainText('Holding this request.');
  await expect(page.locator('.response-group')).toHaveCount(1);
  await expect(partnerTab.locator('.bot-tab-busy-dot')).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole('button', { name: 'Scroll tabs right', exact: true })).toBeVisible();
  expect((await page.locator('.bot-tabs-shell').boundingBox())!.height).toBe(32);
  await page.getByRole('button', { name: 'Scroll tabs right', exact: true }).click();
  await expect.poll(() => page.locator('.bot-tabs').evaluate(el => el.scrollLeft)).toBeGreaterThan(0);
  await page.emulateMedia({ reducedMotion: 'reduce', colorScheme: 'dark' });
  await expect(partnerTab.locator('.bot-tab-busy-dot')).toHaveCSS('animation-name', 'none');
  await partnerTab.click();
  await page.screenshot({ path: 'test-results/bot-threads-active-mobile.png', fullPage: true });
  await page.setViewportSize({ width: 1280, height: 720 });
  await newChat(page);
  await expect(page.getByRole('tablist', { name: 'Bot threads' })).toBeHidden();
  await page.locator(`[data-chat="${group}"]`).click();
  await expect(partnerTab).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('.message.assistant')).toContainText('Holding this request.');
  await page.getByRole('button', { name: 'Stop response' }).click();
  await expect(page.getByRole('tab', { name: 'Partner Stopped', exact: true })).toHaveAttribute('aria-selected', 'true');
  await page.getByRole('tab', { name: 'Echo Stopped', exact: true }).click();
  await expect(page.locator('.message.assistant')).toHaveCount(0);
});

test('thread identity follows bots across chats, tabs, renames and reloads', async ({ page }) => {
  const identity = page.locator('.brand-identity'), name = identity.locator('.brand-name');
  const color = (id: string) => identity.locator(`[data-bot-avatar="${id}"]`).evaluate(el => getComputedStyle(el).backgroundColor);
  await expect(name).toHaveText('Echo');
  const echoColor = await color('echo');
  const bots = [];
  for (const [handle, name] of [['partner', 'Partner'], ['third', 'Third'], ['fourth', 'Fourth']]) {
    const response = await page.request.post('/api/v1/bots', { data: { handle, name, roots: [], plugins: {} } });
    expect(response.ok()).toBe(true);
    bots.push(await response.json());
  }
  const partner = bots[0];
  await page.reload();
  const single = new URL(page.url()).hash.slice(1);
  await send(page, '@partner hello');
  await expect(page.getByRole('button', { name: 'Stop response' })).toBeHidden();
  await expect(name).toHaveText('Partner');
  const partnerColor = await color(partner.id);
  expect(partnerColor).not.toBe(echoColor);
  await newChat(page);
  await expect(name).toHaveText('Echo');
  await send(page, '@echo @partner @third @fourth hello');
  await expect(page.getByRole('button', { name: 'Stop response' })).toBeHidden();
  await expect(name).toHaveText('Echo');
  await expect(identity.locator('.bot-avatar-overflow')).toHaveText('+1');
  await page.getByRole('tab', { name: 'Partner', exact: true }).click();
  await expect(name).toHaveText('Partner');
  await expect(identity.locator('.brand-peers')).toHaveText('with Echo, Third, Fourth');
  expect(await color(partner.id)).toBe(partnerColor);
  expect(await page.getByRole('tab', { name: 'Partner', exact: true }).locator('.bot-avatar').evaluate(el => getComputedStyle(el).backgroundColor)).toBe(partnerColor);
  await page.reload();
  await expect(name).toHaveText('Partner');
  expect(await color(partner.id)).toBe(partnerColor);
  const renamed = await page.request.post('/api/v1/bots', { data: { ...partner, name: 'Co Builder' } });
  expect(renamed.ok()).toBe(true);
  await expect(name).toHaveText('Co Builder');
  expect(await color(partner.id)).toBe(partnerColor);
  await page.screenshot({ path: 'test-results/bot-identity-desktop.png', fullPage: true });
  await page.emulateMedia({ colorScheme: 'dark' });
  await page.screenshot({ path: 'test-results/bot-identity-dark.png', fullPage: true });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole('button', { name: 'Toggle sidebar', exact: true }).click();
  await expect(name).toBeVisible();
  await page.screenshot({ path: 'test-results/bot-identity-mobile.png', fullPage: true });
  await page.getByRole('button', { name: 'Close sidebar', exact: true }).click();
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.getByRole('button', { name: '▦ Apps', exact: true }).click();
  await expect(name).toHaveText('Echo');
  await page.locator(`[data-chat="${single}"]`).click();
  await expect(name).toHaveText('Co Builder');
  expect(await color(partner.id)).toBe(partnerColor);
  await expect(identity.locator('.brand-peers')).toHaveCount(0);
  await page.request.delete(`/api/v1/bots/${partner.id}`);
  await page.reload();
  await expect(name).toHaveText('Partner');
  expect(await color(partner.id)).toBe(partnerColor);
});

test('chat navigation and bot tabs address follow-ups to the selected bot', async ({ page }) => {
  const composer = page.getByRole('textbox', { name: 'Message', exact: true });
  const value = () => composer.evaluate((el: any) => el.value as string);
  await expect.poll(value).toBe('');
  await page.request.post('/api/v1/bots', { data: { handle: 'partner', name: 'Partner', roots: [], plugins: {} } });
  await page.reload();
  const single = new URL(page.url()).hash.slice(1);
  await send(page, '@partner hello');
  await expect(page.getByRole('button', { name: 'Stop response' })).toBeHidden();
  await expect.poll(value).toBe('@partner ');
  await newChat(page);
  await expect.poll(value).toBe('');
  await page.locator(`[data-chat="${single}"]`).click();
  await expect.poll(value).toBe('@partner ');
  await expect(composer.locator('.bot-reference')).toHaveText('@partner');
  await expect(composer).toBeFocused();
  await composer.pressSequentially('follow up');
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Stop response' })).toBeHidden();
  const transcript = await (await page.request.get(`/api/v1/chats/${single}/messages`)).json();
  const lastUser = transcript.filter((message: any) => message.role === 'user').at(-1);
  expect(lastUser.content).toBe('@partner follow up');
  expect(lastUser.recipients).toHaveLength(1);
  expect(lastUser.segments.find((segment: any) => segment.type === 'bot')?.text).toBe('@partner');

  const group = await newChat(page);
  await send(page, '@echo @partner hello');
  await expect(page.getByRole('button', { name: 'Stop response' })).toBeHidden();
  await page.getByRole('tab', { name: 'Partner', exact: true }).click();
  await expect.poll(value).toBe('@partner ');
  await composer.click(); await composer.press('Control+End'); await composer.pressSequentially('Keep this draft');
  await page.getByRole('tab', { name: 'Echo', exact: true }).click();
  await expect.poll(value).toBe('@echo Keep this draft');
  await page.getByRole('tab', { name: 'Echo', exact: true }).press('ArrowRight');
  await expect.poll(value).toBe('@partner Keep this draft');
  await newChat(page);
  await page.locator(`[data-chat="${group}"]`).click();
  await expect.poll(value).toBe('@partner Keep this draft');
  await page.reload();
  await expect.poll(value).toBe('@partner ');
  await expect(page.getByRole('tab', { name: 'Partner', exact: true })).toHaveAttribute('aria-selected', 'true');
});
