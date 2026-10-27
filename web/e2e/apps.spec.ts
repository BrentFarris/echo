import { test, expect, type Page } from '@playwright/test';

const state = async (page: Page) => (await page.request.get('/api/v1/state')).json();
const card = (page: Page, id: string) => page.locator(`.app-card[data-app-id="${id}"]`);
const dock = (page: Page) => page.getByRole('navigation', { name: 'Docked apps' });
const manifest = (id: string, name = id) => ({ api: 1, id, name, version: '1', scope: 'bot', app: { viewId: 'main', icon: 'data:image/svg+xml,' + encodeURIComponent('<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 48 48"><rect width="48" height="48" rx="8" fill="#2563eb"/><path d="M14 24h20M24 14v20" stroke="white" stroke-width="4"/></svg>') }, views: [{ id: 'main', title: name, kind: 'floating', html: '<label>Value <input value="initial"></label>' }] });
async function install(page: Page, value: object) {
  const response = await page.request.post('/__fixtures/app', { data: value }); expect(response.ok()).toBe(true);
  const plugin = await response.json();
  // Announce the fixture installation through a normal settings change.
  expect((await page.request.patch(`/api/v1/plugins/${plugin.id}`, { data: { enabled: !plugin.pending, config: plugin.config } })).ok()).toBe(true);
  return plugin;
}

test.beforeEach(async ({ page }) => {
  await page.goto('/'); await expect(page.locator('#apps-button')).toBeVisible();
  const current = await state(page);
  for (const chat of current.chats) await page.request.delete(`/api/v1/chats/${chat.id}`);
  for (const bot of current.bots) if (bot.id !== 'echo') await page.request.delete(`/api/v1/bots/${bot.id}`);
  for (const id of current.appDock) await page.request.put(`/api/v1/plugins/${id}/app/dock`, { data: { docked: false } });
  for (const plugin of current.plugins) if (!plugin.builtin) await page.request.patch(`/api/v1/plugins/${plugin.id}`, { data: { enabled: false, config: plugin.config } });
  const echo = current.bots.find((bot: any) => bot.id === 'echo');
  await page.request.post('/api/v1/bots', { data: { ...echo, plugins: {} } });
  await page.request.post('/api/v1/endpoints', { data: { endpoint: { id: 'test', name: 'Test local model', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', contextTokens: 32768 } } });
  await page.reload();
  const created = page.waitForResponse(response => response.request().method() === 'POST' && response.url().endsWith('/api/v1/chats'));
  await page.locator('#new-chat').click();
  const chat = await (await created).json(); await expect(page).toHaveURL(new RegExp(`#${chat.id}$`));
});

test('generated Calculator launches from Apps and shares its dock across browsers without sharing windows', async ({ page, browser }) => {
  await page.getByRole('textbox', { name: 'Message', exact: true }).fill('Create a draggable calculator plugin');
  await page.getByRole('button', { name: 'Send message' }).click();
  await expect(page.locator('.floating-plugin')).toBeVisible();
  await page.getByRole('button', { name: 'Close plugin' }).click();
  await page.locator('#apps-button').click();
  await expect(page).toHaveURL(/#apps$/); await expect(page.locator('.composer')).toBeHidden();
  await card(page, 'calculator').getByRole('button', { name: 'Dock Calculator', exact: true }).click();
  await expect(dock(page).getByRole('button', { name: 'Open Calculator' })).toBeVisible();
  const context = await browser.newContext(); const other = await context.newPage();
  try {
    await other.goto('/#apps');
    await expect(dock(other).getByRole('button', { name: 'Open Calculator' })).toBeVisible();
    await card(page, 'calculator').getByRole('button', { name: 'Open Calculator' }).click();
    const calculator = page.frameLocator('iframe[title="Calculator"]');
    await calculator.locator('#a').fill('7'); await calculator.getByRole('button', { name: 'Multiply' }).click();
    await expect(calculator.locator('#result')).toHaveText('21');
    await dock(page).getByRole('button', { name: 'Open Calculator' }).dblclick();
    await expect(page.locator('.floating-plugin')).toHaveCount(1); await expect(calculator.locator('#result')).toHaveText('21');
    await expect(other.locator('.floating-plugin')).toHaveCount(0);
    await page.getByRole('button', { name: 'Close plugin' }).click();
    await card(page, 'calculator').getByRole('button', { name: 'Undock Calculator' }).click();
    await expect(dock(other)).toBeHidden();
    await card(page, 'calculator').getByRole('button', { name: 'Dock Calculator', exact: true }).click();
    await page.reload(); await expect(page).toHaveURL(/#apps$/);
    await expect(dock(page).getByRole('button', { name: 'Open Calculator' })).toBeVisible();
    await page.screenshot({ path: 'test-results/apps-desktop.png', fullPage: true });
  } finally { await context.close(); }
});

test('disabled and pending apps keep their pins and settings restore availability', async ({ page }) => {
  await install(page, manifest('availability', 'Availability'));
  await page.locator('#apps-button').click();
  await card(page, 'availability').getByRole('button', { name: 'Dock Availability', exact: true }).click();
  await dock(page).getByRole('button', { name: 'Open Availability' }).click();
  await expect(page.locator('.floating-plugin')).toBeVisible();
  await page.request.patch('/api/v1/plugins/availability', { data: { enabled: false, config: {} } });
  await expect(page.locator('.floating-plugin')).toHaveCount(0); await expect(card(page, 'availability')).toContainText('Disabled');
  await expect(dock(page)).toBeHidden(); expect((await state(page)).appDock).toContain('availability');
  await expect(card(page, 'availability').getByRole('button', { name: 'Open Availability' })).toBeDisabled();
  await expect(card(page, 'availability').getByRole('button', { name: 'Undock Availability' })).toBeEnabled();
  await page.request.patch('/api/v1/plugins/availability', { data: { enabled: true, config: {} } });
  await expect(dock(page).getByRole('button', { name: 'Open Availability' })).toBeVisible();
  await install(page, { ...manifest('availability', 'Availability'), capabilities: ['web.get'] });
  await expect(card(page, 'availability')).toContainText('Awaiting permission'); await expect(dock(page)).toBeHidden();
  await card(page, 'availability').getByRole('button', { name: 'Settings for Availability' }).click();
  await page.getByRole('button', { name: 'Approve permissions' }).click();
  await page.locator('#settings').getByRole('button', { name: 'Close', exact: true }).click();
  await expect(dock(page).getByRole('button', { name: 'Open Availability' })).toBeVisible();
  const echo = (await state(page)).bots.find((bot: any) => bot.id === 'echo');
  await page.request.post('/api/v1/bots', { data: { ...echo, plugins: { availability: false } } });
  await expect(card(page, 'availability')).toContainText('Disabled for Echo'); await expect(dock(page)).toBeHidden();
  await card(page, 'availability').getByRole('button', { name: 'Settings for Availability' }).click();
  await expect(page.getByRole('heading', { name: 'Edit bot' })).toBeVisible();
  await page.getByRole('checkbox', { name: 'Availability', exact: true }).check();
  await page.getByRole('button', { name: 'Save bot' }).click();
  await page.locator('#settings').getByRole('button', { name: 'Close', exact: true }).click();
  await expect(dock(page).getByRole('button', { name: 'Open Availability' })).toBeVisible();
});

test('package changes close windows and registration removal and rollback retain the dock', async ({ page }) => {
  await install(page, manifest('replaceable', 'Replaceable'));
  await page.locator('#apps-button').click();
  await card(page, 'replaceable').getByRole('button', { name: 'Dock Replaceable', exact: true }).click();
  await dock(page).getByRole('button', { name: 'Open Replaceable' }).click();
  await expect(page.locator('.floating-plugin')).toBeVisible();
  await install(page, { ...manifest('replaceable', 'Replaceable'), version: '2' });
  await expect(page.locator('.floating-plugin')).toHaveCount(0);
  await install(page, { ...manifest('replaceable', 'Replaceable'), version: '3', app: undefined });
  await expect(card(page, 'replaceable')).toHaveCount(0); await expect(dock(page)).toBeHidden();
  expect((await state(page)).appDock).toContain('replaceable');
  await page.request.post('/api/v1/plugins/replaceable/rollback', { data: {} });
  await expect(card(page, 'replaceable')).toBeVisible(); await expect(dock(page)).toBeVisible();
  await card(page, 'replaceable').getByRole('button', { name: 'Open Replaceable' }).click();
  await expect(page.frameLocator('iframe[title="Replaceable"]').getByLabel('Value')).toHaveValue('initial');
});

test('Apps navigation preserves drafts, attachments, selected thread, and an ongoing response', async ({ page }) => {
  await install(page, { ...manifest('embedded-draft', 'Embedded draft'), views: [{ id: 'main', kind: 'embedded', title: 'Embedded draft', html: '<p>Embedded app</p>' }] });
  const chatId = new URL(page.url()).hash.slice(1);
  for (const [handle, name] of [['apps-alpha', 'Apps Alpha'], ['apps-beta', 'Apps Beta']]) {
    await page.request.post('/api/v1/bots', { data: { handle, name, prompt: '', plugins: {}, roots: [] } });
  }
  await page.reload();
  const input = page.getByRole('textbox', { name: 'Message', exact: true });
  await input.fill('@apps-alpha @apps-beta slow stream'); await page.getByRole('button', { name: 'Send message' }).click();
  await page.getByRole('tab', { name: /Apps Beta/ }).click();
  await expect(page.locator('.message.assistant')).toContainText('A');
  await input.fill('Keep this draft');
  await input.evaluate(element => {
    const data = new DataTransfer(); data.items.add(new File(['notes'], 'notes.txt', { type: 'text/plain' }));
    element.dispatchEvent(new ClipboardEvent('paste', { bubbles: true, clipboardData: data }));
  });
  const selectedThread = await page.locator('.bot-tabs [aria-selected="true"]').getAttribute('data-bot-thread');
  await page.locator('#apps-button').click();
  await card(page, 'embedded-draft').getByRole('button', { name: 'Open Embedded draft', exact: true }).click();
  await expect(page.locator('.embedded-plugin')).toBeVisible();
  await expect(page.locator('.composer')).toBeHidden();
  await expect.poll(async () => (await state(page)).runs.some((run: any) => run.chatId === chatId && run.status === 'completed')).toBe(true);
  await expect(page).toHaveURL(/#app\/embedded-draft\/main\/echo$/);
  await page.locator(`[data-chat="${chatId}"]`).click();
  await expect(input).toHaveText('Keep this draft'); await expect(page.locator('.attachment-drafts')).toContainText('notes.txt');
  await expect(page.locator('.message.assistant')).toContainText('A stream that survives a browser reload.');
  await expect(page.locator('.bot-tabs [aria-selected="true"]')).toHaveAttribute('data-bot-thread', selectedThread!);
  await page.locator('#apps-button').click(); await page.locator('#new-chat').click(); await expect(input).toBeVisible();
});

test('square dock icons scroll with wheel and keyboard and remain usable on mobile', async ({ page }) => {
  for (let i = 0; i < 9; i++) {
    const id = `dock-${i}`; await install(page, manifest(id, `Dock app ${i}`));
    expect((await page.request.put(`/api/v1/plugins/${id}/app/dock`, { data: { docked: true } })).ok()).toBe(true);
  }
  await page.locator('#apps-button').click();
  await expect(dock(page).locator('button')).toHaveCount(9);
  const size = await dock(page).locator('button').first().boundingBox(); expect(size!.width).toBe(size!.height);
  await expect.poll(() => dock(page).evaluate(el => el.scrollWidth > el.clientWidth)).toBe(true);
  await dock(page).hover(); await page.mouse.wheel(0, 180);
  await expect.poll(() => dock(page).evaluate(el => el.scrollLeft)).toBeGreaterThan(0);
  await dock(page).locator('button').first().focus(); await page.keyboard.press('End');
  await expect(dock(page).locator('button').last()).toBeFocused();
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole('button', { name: 'Toggle sidebar' }).click();
  await expect(dock(page)).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: 'test-results/apps-mobile-dock.png', fullPage: true });
  await dock(page).locator('button').last().focus(); await page.keyboard.press('Enter');
  await expect(page.locator('.sidebar')).not.toHaveClass(/open/);
  await expect(page.locator('.floating-plugin')).toBeVisible();
});


test('HTML app preserves its browsing context through presentation switches', async ({ page }) => {
  await install(page, manifest('stateful-mode', 'Stateful mode'));
  await page.locator('#apps-button').click();
  await card(page, 'stateful-mode').getByRole('button', { name: 'Open Stateful mode', exact: true }).click();
  const iframe = page.frameLocator('iframe[title="Stateful mode"]');
  await iframe.getByLabel('Value').fill('live iframe state');
  const original = await page.locator('iframe[title="Stateful mode"]').elementHandle();
  await page.getByRole('button', { name: 'Open Embedded', exact: true }).click();
  await expect(page.locator('.embedded-plugin')).toBeVisible();
  await expect(iframe.getByLabel('Value')).toHaveValue('live iframe state');
  await page.locator('#apps-button').click();
  await card(page, 'stateful-mode').getByRole('button', { name: 'Open Stateful mode', exact: true }).click();
  await expect(iframe.getByLabel('Value')).toHaveValue('live iframe state');
  await page.getByRole('button', { name: 'Open in Window', exact: true }).click();
  await expect(iframe.getByLabel('Value')).toHaveValue('live iframe state');
  expect(await original!.evaluate(el => el === document.querySelector('iframe[title="Stateful mode"]'))).toBe(true);
});
