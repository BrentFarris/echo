import { test, expect, type Page } from '@playwright/test';
import { mkdir } from 'node:fs/promises';
import { resolve } from 'node:path';

const planText = '\n\n You are to plan this request out, do not make any changes until the plan is approved';
const input = (page: Page) => page.getByRole('textbox', { name: 'Message', exact: true });
const picker = (page: Page) => page.getByRole('listbox', { name: 'Slash commands', exact: true });
const value = (page: Page) => input(page).evaluate((el: any) => el.value as string);
async function json(page: Page, path: string, method = 'GET', data?: unknown) {
  const response = await page.request.fetch('/api/v1' + path, { method, data });
  expect(response.ok(), await response.text()).toBe(true); return response.json();
}
async function save(page: Page, name: string, action: unknown) { return json(page, '/slash-commands', 'POST', { name, description: 'Test ' + name, action }); }
async function settings(page: Page) {
  await page.getByRole('button', { name: '⚙ Settings', exact: true }).click();
  await expect(page.locator('.slash-command-settings')).toContainText('/plan');
}

test.beforeEach(async ({ page }) => {
  await page.goto('/'); await expect(page.locator('#new-chat')).toBeVisible();
  for (const command of await json(page, '/slash-commands')) {
    if (!command.builtin) await json(page, '/slash-commands/' + command.id, 'DELETE');
    else if (!command.enabled) await json(page, '/slash-commands/' + command.id + '/restore', 'POST', {});
  }
  const state = await json(page, '/state');
  for (const chat of state.chats) { await json(page, '/chats/' + chat.id, 'DELETE'); await json(page, '/chats/' + chat.id + '/permanent', 'DELETE'); }
  for (const bot of state.bots) if (bot.id !== 'echo') await json(page, '/bots/' + bot.id, 'DELETE');
  const echo = state.bots.find((b: any) => b.id === 'echo');
  await json(page, '/bots', 'POST', { ...echo, plugins: { ...echo.plugins, calendar: true } });
  const calendar = state.plugins.find((p: any) => p.id === 'calendar');
  await json(page, '/plugins/calendar', 'PATCH', { enabled: true, config: calendar.config });
  await json(page, '/endpoints', 'POST', { endpoint: { id: 'test', name: 'Test', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', contextTokens: 32768 } });
  await page.reload(); await page.locator('#new-chat').click(); await input(page).focus();
});

for (const key of ['Enter', 'Tab']) test(`${key} selects plan as a single undoable edit that preserves the invocation caret`, async ({ page }) => {
  await input(page).fill('before /pl after');
  await input(page).evaluate((el: any) => { el.setSelectionRange(10); el.dispatchEvent(new MouseEvent('click', { bubbles: true })); });
  await expect(picker(page).getByRole('option')).toContainText('/plan');
  await input(page).press(key);
  await expect.poll(() => value(page)).toBe('before  after' + planText);
  await expect(input(page)).toBeFocused();
  await expect(picker(page)).toBeHidden();
  expect(await input(page).evaluate((el: any) => el.selectionStart)).toBe(7);
  await expect(page.locator('.message.user')).toHaveCount(0);
  await input(page).press('Control+Z'); await expect.poll(() => value(page)).toBe('before /pl after');
  await input(page).press('Control+Shift+Z'); await expect.poll(() => value(page)).toBe('before  after' + planText);
  expect(await input(page).evaluate((el: any) => el.selectionStart)).toBe(7);
  await input(page).pressSequentially('more');
  await expect.poll(() => value(page)).toBe('before more after' + planText);
  await input(page).fill('/plan'); await picker(page).getByRole('option').click();
  await expect.poll(() => value(page)).toBe(planText);
  expect(await input(page).evaluate((el: any) => el.selectionStart)).toBe(0);
});

test('Space Shift+Tab Escape paste and submission leave slash text literal', async ({ page }) => {
  for (const key of ['Space', 'Shift+Tab', 'Escape']) {
    await input(page).fill('/plan'); await expect(picker(page)).toBeVisible();
    await input(page).press(key); await expect(picker(page)).toBeHidden();
    expect(await value(page)).toBe('/plan' + (key === 'Space' ? ' ' : ''));
  }
  await input(page).fill('literal /plan text'); await expect(picker(page)).toBeHidden();
  await input(page).fill('/unknown-command'); await input(page).press('Tab');
  await expect(picker(page)).toBeHidden(); expect(await value(page)).toBe('/unknown-command');
  await input(page).press('Enter');
  await expect(page.locator('.message.user').last()).toContainText('/unknown-command');
  await expect(page.locator('#send')).toHaveAttribute('aria-label', 'Send message');
  await input(page).fill('/plan'); await input(page).press('Escape'); await input(page).press('Enter');
  await expect(page.locator('.message.user').last()).toContainText('/plan');
  await expect(page.locator('.message.user').last()).not.toContainText('You are to plan');
});

test('General creates edits removes and restores commands across browsers', async ({ page, browser }, info) => {
  const otherContext = await browser.newContext(), other = await otherContext.newPage();
  try {
    await other.goto('/'); await expect(input(other)).toBeVisible();
    await settings(page); await page.getByRole('button', { name: '＋ Add command' }).click();
    await page.getByLabel('Command name', { exact: true }).fill('bad/name');
    expect(await page.getByLabel('Command name', { exact: true }).evaluate((el: HTMLInputElement) => el.checkValidity())).toBe(false);
    await page.getByLabel('Command name', { exact: true }).fill('review');
    await page.getByLabel('Description', { exact: true }).fill('Review the request');
    await page.getByLabel('Text', { exact: true }).fill('Please review carefully.');
    await page.getByRole('button', { name: 'Save command', exact: true }).click();
    const card = page.locator('.slash-command-card').filter({ has: page.locator('strong', { hasText: '/review' }) });
    await expect(card).toBeVisible();
    await expect.poll(async () => (await json(other, '/state')).slashCommands.some((c: any) => c.name === 'review')).toBe(true);
    await other.reload(); await input(other).fill('/rev'); await picker(other).getByRole('option').click();
    await expect.poll(() => value(other)).toBe('Please review carefully.');
    await card.getByRole('button', { name: 'Edit', exact: true }).click();
    await page.getByRole('combobox', { name: 'Position', exact: true }).selectOption('append');
    await page.getByLabel('Text', { exact: true }).fill('APPENDED');
    await page.getByRole('button', { name: 'Save command', exact: true }).click();
    await expect(page.locator('[data-command-editor]')).toBeEmpty();
    const builtin = page.locator('.slash-command-card').filter({ has: page.locator('strong', { hasText: '/plan' }) });
    await builtin.getByRole('button', { name: 'Remove', exact: true }).click();
    await expect(builtin.getByRole('button', { name: 'Restore', exact: true })).toBeVisible();
    await other.reload(); await input(other).fill('/plan'); await expect(picker(other).getByRole('option')).toHaveCount(0);
    await builtin.getByRole('button', { name: 'Restore', exact: true }).click();
    await expect(builtin.getByRole('button', { name: 'Remove', exact: true })).toBeVisible();
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: info.outputPath('slash-settings-mobile.png') });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
    await card.getByRole('button', { name: 'Delete', exact: true }).click(); await expect(card).toHaveCount(0);
  } finally { await otherContext.close(); }
});

test('General configures plugin actions and native app features with fixed bot context', async ({ page }) => {
  const root = resolve('../.test-data/slash-native'); await mkdir(root, { recursive: true });
  const bot = await json(page, '/bots', 'POST', { handle: 'slash-worker', name: 'Slash worker', roots: [root] });
  await json(page, '/plugins/monaco-editor', 'PATCH', { enabled: true, config: {} });
  await page.reload(); await settings(page);
  await page.getByRole('button', { name: '＋ Add command' }).click();
  await page.getByLabel('Command name', { exact: true }).fill('schedules');
  await page.getByRole('combobox', { name: 'Action', exact: true }).selectOption('plugin');
  await page.getByRole('combobox', { name: 'Plugin', exact: true }).selectOption('calendar');
  await page.getByRole('combobox', { name: 'Plugin action', exact: true }).selectOption('schedule_list');
  await page.getByRole('button', { name: 'Save command', exact: true }).click();
  await expect(page.locator('[data-command-editor]')).toBeEmpty();
  await page.getByRole('button', { name: '＋ Add command' }).click();
  await page.getByLabel('Command name', { exact: true }).fill('scm');
  await page.getByRole('combobox', { name: 'Action', exact: true }).selectOption('app');
  await page.getByRole('combobox', { name: 'App', exact: true }).selectOption('monaco-editor');
  await page.getByRole('combobox', { name: 'Feature', exact: true }).selectOption('source-control');
  await page.getByRole('combobox', { name: 'Bot', exact: true }).selectOption(bot.id);
  await page.getByRole('button', { name: 'Save command', exact: true }).click();
  await expect(page.locator('[data-command-editor]')).toBeEmpty();
  await page.locator('#settings').getByRole('button', { name: 'Close', exact: true }).click();
  await input(page).fill('/schedules'); await input(page).press('Enter');
  await expect(page.getByRole('region', { name: 'Command results' })).toContainText('Completed');
  await input(page).fill('draft /scm'); await input(page).press('Enter');
  await expect(page.getByLabel('Editor bot')).toHaveValue(bot.id);
  await expect(page.locator('.editor-bot-session:not([hidden]) [data-sidebar-view="source-control"]')).toBeVisible();
  expect(new URL(page.url()).hash).toContain('feature=source-control');
  const chat = (await json(page, '/state')).chats[0];
  await page.locator(`[data-chat="${chat.id}"]`).click(); await expect.poll(() => value(page)).toBe('draft ');
  await input(page).fill('/scm'); await input(page).press('Enter');
  await expect(page.locator('.monaco-app')).toHaveCount(1); await expect(page.getByLabel('Editor bot')).toHaveValue(bot.id);
});

test('autocomplete follows the selected bot machine and General edits its selected machine', async ({ page }) => {
  const peer = await json(page, '/swarm/peers', 'POST', { url: 'http://127.0.0.1:3744', password: 'echo-test-password' });
  const remote = `/swarm/instances/${peer.nodeId}`;
  try {
    for (const command of await json(page, remote + '/slash-commands')) if (!command.builtin) await json(page, remote + '/slash-commands/' + command.id, 'DELETE');
    await save(page, 'place', { type: 'text', position: 'insert', text: 'LOCAL' });
    await json(page, remote + '/slash-commands', 'POST', { name: 'place', description: 'Remote command', action: { type: 'text', position: 'insert', text: 'REMOTE' } });
    await json(page, remote + '/endpoints', 'POST', { endpoint: { id: 'test', name: 'Test', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', contextTokens: 32768 } });
    await expect.poll(async () => (await json(page, '/swarm/state')).instances.find((item: any) => item.node.nodeId === peer.nodeId)?.state?.slashCommands?.some((c: any) => c.name === 'place')).toBe(true);
    await page.reload(); await page.getByRole('combobox', { name: 'Bot for new chat' }).selectOption(peer.nodeId + ':echo');
    await page.locator('#new-chat').click(); await input(page).fill('/place'); await input(page).press('Enter');
    await expect.poll(() => value(page)).toBe('REMOTE');
    await settings(page); await page.getByRole('combobox', { name: 'Settings machine' }).selectOption(peer.nodeId);
    const card = page.locator('.slash-command-card').filter({ has: page.locator('strong', { hasText: '/place' }) });
    await expect(card).toContainText('Remote command'); await card.getByRole('button', { name: 'Delete', exact: true }).click(); await expect(card).toHaveCount(0);
    expect((await json(page, '/slash-commands')).some((c: any) => c.name === 'place')).toBe(true);
    await page.locator('#settings').getByRole('button', { name: 'Close', exact: true }).click();
    await page.getByRole('combobox', { name: 'Bot for new chat' }).selectOption('echo'); await page.locator('#new-chat').click();
    await input(page).fill('/place'); await input(page).press('Enter'); await expect.poll(() => value(page)).toBe('LOCAL');
  } finally {
    await json(page, `/swarm/peers/${peer.nodeId}`, 'DELETE');
    await json(page, '/plugins/swarm', 'PATCH', { enabled: false, config: {} });
  }
});

test('plugin results stay separate and late results belong to the originating chat', async ({ page }) => {
  await save(page, 'schedules', { type: 'plugin', pluginId: 'calendar', tool: 'schedule_list', arguments: {} });
  await page.reload(); await input(page).fill('draft /schedules');
  let release!: () => void, calls = 0;
  const pending = new Promise<void>(resolve => { release = resolve; });
  await page.route('**/slash-commands/*/invoke', async route => { calls++; await pending; await route.fulfill({ json: { result: 'finished on original chat' } }); });
  await input(page).press('Enter'); await expect(page.getByRole('region', { name: 'Command results' })).toContainText('Running');
  expect(await value(page)).toBe('draft '); expect(calls).toBe(1);
  const original = new URL(page.url()).hash.slice(1);
  await input(page).pressSequentially('/schedules'); await input(page).press('Enter'); expect(calls).toBe(1);
  await page.locator('#new-chat').click(); release();
  await expect(page.getByRole('region', { name: 'Command results' })).toBeHidden();
  await page.locator(`[data-chat="${original}"]`).click();
  const results = page.getByRole('region', { name: 'Command results' }); await expect(results).toContainText('Completed');
  await results.getByText('Result', { exact: true }).click(); await expect(results).toContainText('finished on original chat');
  expect(await value(page)).not.toContain('finished on original chat'); await expect(page.locator('.message.user')).toHaveCount(0);
});

test('app commands navigate declared features in a retained iframe', async ({ page }) => {
  const manifest = { api: 1, id: 'slash-app', name: 'Slash App', version: '1', scope: 'app', app: { viewId: 'main' }, views: [{ id: 'main', title: 'Slash App', kind: 'floating', features: [{ id: 'history', title: 'History' }, { id: 'settings', title: 'Settings' }], html: `<input value="keep me"><output id="feature"></output><script>let nonce;addEventListener('message',e=>{if(e.source!==parent)return;const d=e.data;if(d.type==='echo-plugin-init'){nonce=d.nonce;document.querySelector('output').textContent=d.feature}if(d.type==='echo-plugin-navigate'&&d.nonce===nonce)document.querySelector('output').textContent=d.feature});parent.postMessage({type:'echo-plugin-ready'},'*')</script>` }] };
  const installed = await page.request.post('/__fixtures/app', { data: manifest }); expect(installed.ok()).toBe(true);
  await save(page, 'history', { type: 'app', pluginId: 'slash-app', viewId: 'main', feature: 'history' });
  await save(page, 'appsettings', { type: 'app', pluginId: 'slash-app', viewId: 'main', feature: 'settings' });
  await page.reload(); await input(page).fill('/history'); await input(page).press('Enter');
  const frame = page.frameLocator('iframe[title="Slash App"]'); await expect(frame.locator('#feature')).toHaveText('history');
  await frame.locator('input').fill('retained state');
  await input(page).fill('/appsettings'); await input(page).press('Enter'); await expect(frame.locator('#feature')).toHaveText('settings');
  await expect(frame.locator('input')).toHaveValue('retained state'); await expect(page.locator('iframe[title="Slash App"]')).toHaveCount(1);
});
