import { test, expect, type Page } from '@playwright/test';
import { mkdir } from 'node:fs/promises';
import path from 'node:path';

const base = '/api/v1/plugins/terminal/bots/echo';
async function json(page: Page, url: string, method = 'GET', data?: unknown) {
  const response = await page.request.fetch(url, { method, data });
  expect(response.ok(), await response.text()).toBe(true); return response.json();
}
async function start(page: Page) {
  await page.goto('/'); await expect(page.locator('#apps-button')).toBeVisible();
  await json(page, '/api/v1/plugins/terminal', 'PATCH', { enabled: true, config: {} });
  const saved = await json(page, `${base}/saved-commands`);
  for (const item of saved.commands) if (item.name === 'Terminal saved check') await json(page, `${base}/saved-commands/${item.id}`, 'DELETE');
  await page.evaluate(() => localStorage.clear()); await page.reload();
  await expect(page.getByLabel('Terminal bot')).toBeVisible();
  await page.getByRole('button', { name: 'Open terminal', exact: true }).click();
  await expect(page.locator('.terminal-status-text')).toHaveText('Running');
  const sessions = await json(page, `${base}/sessions`);
  return sessions.sessions[0];
}
async function command(page: Page, text: string) {
  await page.locator('.terminal-region .xterm-helper-textarea').focus();
  await page.keyboard.type(text); await page.keyboard.press('Enter');
}

test('persistent dock, folder shortcuts, saved commands, bot isolation and lifecycle', async ({ page }) => {
  const root = path.resolve('../.test-data/terminal-folders');
  const folders = [path.join(root, 'first'), path.join(root, "second ' 日本")];
  for (const folder of folders) await mkdir(folder, { recursive: true });
  await page.goto('/');
  const state = await json(page, '/api/v1/state');
  const echo = state.bots.find((b: any) => b.id === 'echo');
  await json(page, '/api/v1/bots', 'POST', { ...echo, roots: folders });
  const other = await json(page, '/api/v1/bots', 'POST', { handle: `term-${Date.now()}`, name: 'Terminal Other', roots: [folders[0]] });
  try {
    const session = await start(page);
    await page.goto('/#app/terminal/main/echo');
    await expect(page.locator('.chat-view')).toBeVisible();
    await expect(page.locator('.terminal-status-text')).toHaveText('Running');
    await expect(page.getByLabel('Change folder')).toBeVisible();
    const changing = page.waitForRequest(r => r.url().endsWith('/folder'));
    await page.getByLabel('Change folder').selectOption(folders[1]);
    expect((await changing).postDataJSON().folder).toBe(folders[1]);
    await command(page, process.platform === 'win32' ? '(Get-Location).Path' : 'pwd');
    await expect(page.locator('.xterm-rows')).toContainText(folders[1]);
    await page.getByRole('button', { name: 'Saved commands', exact: true }).click();
    await page.getByRole('button', { name: 'Add', exact: true }).click();
    const dialog = page.getByRole('dialog', { name: 'New Command' });
    await dialog.getByLabel('Command name').fill('Terminal saved check');
    await dialog.getByLabel('Command text').fill('echo SAVED_TERMINAL_OK');
    await dialog.getByRole('button', { name: 'Save', exact: true }).click();
    await page.getByRole('button', { name: 'Saved commands', exact: true }).click();
    await page.getByRole('menuitem').filter({ hasText: 'Terminal saved check' }).click();
    await expect(page.locator('.xterm-rows')).toContainText('SAVED_TERMINAL_OK');
    await page.getByRole('button', { name: 'Close terminal', exact: true }).click();
    await page.locator('#apps-button').click();
    await page.getByRole('button', { name: 'Open Terminal', exact: true }).click();
    expect((await json(page, `${base}/sessions`)).sessions[0].id).toBe(session.id);
    await expect(page.locator('.terminal-dock.is-open')).toBeVisible();
    await page.getByRole('button', { name: 'Open Monaco Editor', exact: true }).click();
    await expect(page.locator('.embedded-plugin')).toBeVisible();
    await expect(page.locator('.terminal-dock')).toBeVisible();
    const terminalBox = await page.locator('.terminal-dock').boundingBox();
    const editorBox = await page.locator('.embedded-plugin').boundingBox();
    expect(editorBox!.y + editorBox!.height).toBeLessThanOrEqual(terminalBox!.y + 2);
    const handle = page.getByRole('separator', { name: 'Resize terminal' });
    await handle.focus(); await page.keyboard.press('ArrowUp');
    await page.getByRole('button', { name: 'Maximize terminal', exact: true }).click();
    await expect(page.locator('.terminal-dock')).toHaveClass(/is-maximized/);
    await page.getByLabel('Terminal bot').selectOption(other.id);
    await expect(page.locator('.terminal-dock')).toHaveClass(/is-open.*is-maximized/);
    await expect(page.locator('.terminal-status-text')).toHaveText('Running');
    await page.getByRole('button', { name: 'Restore terminal size', exact: true }).click();
    await expect(page.getByLabel('Change folder')).toHaveCount(0);
    expect((await json(page, `/api/v1/plugins/terminal/bots/${other.id}/saved-commands`)).commands).toHaveLength(0);
    await page.getByLabel('Terminal bot').selectOption('echo');
    await expect(page.locator('.terminal-dock')).toHaveClass(/is-open/);
    await page.reload();
    await expect(page.locator('.terminal-status-text')).toHaveText('Running');
    expect((await json(page, `${base}/sessions`)).sessions[0].id).toBe(session.id);
    await page.getByRole('button', { name: 'Saved commands', exact: true }).click();
    await page.getByRole('button', { name: 'Edit Terminal saved check', exact: true }).click();
    await page.getByRole('dialog', { name: 'Edit Command' }).getByLabel('Command text').fill('echo EDITED_TERMINAL_OK');
    await page.getByRole('dialog', { name: 'Edit Command' }).getByRole('button', { name: 'Save', exact: true }).click();
    await page.getByRole('button', { name: 'Saved commands', exact: true }).click();
    await page.getByRole('menuitem').filter({ hasText: 'Terminal saved check' }).click();
    await expect(page.locator('.xterm-rows')).toContainText('EDITED_TERMINAL_OK');
    await page.getByRole('button', { name: 'Saved commands', exact: true }).click();
    await page.getByRole('button', { name: 'Delete Terminal saved check', exact: true }).click();
    await expect(page.getByRole('menuitem').filter({ hasText: 'Terminal saved check' })).toHaveCount(0);
    await page.getByRole('button', { name: 'Kill terminal', exact: true }).click();
    await expect(page.locator('.terminal-status-text')).toHaveText(/^Exit/);
    await page.locator('.xterm-helper-textarea').focus(); await page.keyboard.press('Enter');
    await expect(page.locator('.terminal-status-text')).toHaveText('Running');
    expect((await json(page, `${base}/sessions`)).sessions[0].id).not.toBe(session.id);
    await command(page, 'echo TERMINAL_READY');
    await expect(page.locator('.xterm-rows')).toContainText('TERMINAL_READY');
    const dismiss = page.getByRole('button', { name: 'Dismiss terminal message', exact: true });
    while (await dismiss.count()) await dismiss.first().click();
    await page.screenshot({ path: 'test-results/terminal-desktop.png' });
    await page.setViewportSize({ width: 390, height: 844 });
    await expect(page.getByLabel('Terminal bot')).toBeVisible();
    await page.screenshot({ path: 'test-results/terminal-mobile.png' });
    expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  } finally {
    await page.request.delete(`/api/v1/bots/${other.id}`).catch(() => {});
    await json(page, '/api/v1/bots', 'POST', echo).catch(() => {});
  }
});

test('selection copy, fallback, interrupt and paste', async ({ page, context }) => {
  await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  await start(page);
  const input = page.locator('.terminal-region .xterm-helper-textarea');
  const selection = page.locator('.terminal-region .xterm-selection div');
  const sent: string[] = [];
  page.on('request', r => { if (/\/sessions\/[^/]+\/input$/.test(r.url())) sent.push(r.postDataJSON().data); });
  await command(page, 'echo ECHO_TERMINAL_COPY_OK');
  const output = page.locator('.xterm-rows').getByText('ECHO_TERMINAL_COPY_OK', { exact: true }).last();
  await expect(output).toBeVisible();
  for (const fallback of [false, true]) {
    await page.evaluate(() => navigator.clipboard.writeText('before'));
    if (fallback) await page.evaluate(() => Object.defineProperty(navigator, 'clipboard', { configurable: true, value: undefined }));
    const bounds = await output.boundingBox(); expect(bounds).not.toBeNull();
    await page.mouse.dblclick(bounds!.x + bounds!.width / 2, bounds!.y + bounds!.height / 2);
    await expect(selection).not.toHaveCount(0); sent.length = 0;
    await page.keyboard.press('Control+c');
    await expect(selection).toHaveCount(0); await expect(input).toBeFocused();
    if (fallback) await page.evaluate(() => Reflect.deleteProperty(navigator, 'clipboard'));
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe('ECHO_TERMINAL_COPY_OK');
    expect(sent.join('').replace(/\u001b\[[IO]/g, '')).toBe('');
    await page.keyboard.press('Control+c');
    await expect.poll(() => sent.join('')).toContain('\u0003');
  }
  await page.evaluate(() => navigator.clipboard.writeText('echo PASTED_TERMINAL_OK'));
  await input.focus(); await page.keyboard.press('Control+v'); await page.keyboard.press('Enter');
  await expect(page.locator('.xterm-rows').getByText('PASTED_TERMINAL_OK', { exact: true }).last()).toBeVisible();
});

test('remote terminal remains on its host and reconnects without replaying input', async ({ page }) => {
  await start(page);
  const node = await json(page, '/api/v1/swarm/peers', 'POST', { url: 'http://127.0.0.1:3744', password: 'echo-test-password' });
  const remote = `/api/v1/swarm/instances/${node.nodeId}`;
  await json(page, `${remote}/plugins/terminal`, 'PATCH', { enabled: true, config: {} });
  await expect.poll(async () => (await json(page, '/api/v1/swarm/state')).instances.some((i: any) => i.node.nodeId === node.nodeId && i.state?.plugins.some((p: any) => p.id === 'terminal' && p.enabled))).toBe(true);
  await page.reload();
  await page.getByLabel('Terminal bot').selectOption(`${node.nodeId}:echo`);
  await expect(page.locator('.terminal-dock')).toHaveClass(/is-open/);
  await expect(page.locator('.terminal-status-text')).toHaveText('Running');
  const session = (await json(page, `${remote}/plugins/terminal/bots/echo/sessions`)).sessions[0];
  await command(page, 'echo REMOTE_TERMINAL_OK');
  await expect(page.locator('.xterm-rows')).toContainText('REMOTE_TERMINAL_OK');
  try {
    await json(page, `/api/v1/swarm/peers/${node.nodeId}`, 'PATCH', { disconnected: true });
    await expect(page.locator('.terminal-status-text')).toHaveText('Offline');
    await page.locator('.xterm-helper-textarea').focus(); await page.keyboard.type('NEVER_REPLAY_THIS');
    await json(page, `/api/v1/swarm/peers/${node.nodeId}`, 'PATCH', { disconnected: false });
    await expect(page.locator('.terminal-status-text')).toHaveText('Running', { timeout: 20000 });
    expect((await json(page, `${remote}/plugins/terminal/bots/echo/sessions`)).sessions[0].id).toBe(session.id);
    await expect(page.locator('.xterm-rows')).not.toContainText('NEVER_REPLAY_THIS');
  } finally { await page.request.delete(`/api/v1/swarm/peers/${node.nodeId}`); }
});
