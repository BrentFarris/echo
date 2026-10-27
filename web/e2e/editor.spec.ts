import { test, expect, type Page } from '@playwright/test';
import { mkdir, writeFile, readFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const editor = (page: Page) => page.locator('.editor-bot-session:not([hidden])');
const row = (page: Page, name: string) => editor(page).locator('.code-tree-row').filter({ hasText: name }).first();
const tab = (page: Page, name: string) => editor(page).getByRole('tab', { name: new RegExp(name.replace('.', '\\.')) });
const text = (page: Page) => editor(page).locator('.monaco-editor .view-lines').first();
async function launch(page: Page) {
  await page.locator('#apps-button').click();
  await page.locator('[data-app-id="monaco-editor"]').getByRole('button', { name: 'Open Monaco Editor', exact: true }).click();
  await expect(page.locator('.embedded-plugin')).toBeVisible();
  await expect(editor(page).locator('.code-app-shell')).toHaveAttribute('aria-busy', 'false');
}

test.beforeEach(async ({ page }, info) => {
  await page.goto('/');
  await expect(page.locator('#apps-button')).toBeVisible();
  await page.request.post('/api/v1/session', { data: {} });
  const state = await (await page.request.get('/api/v1/state')).json();
  for (const bot of state.bots) if (bot.id !== 'echo') await page.request.delete(`/api/v1/bots/${bot.id}`);
  const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../.test-data/editor-fixtures', String(info.workerIndex), String(Date.now()));
  await mkdir(root, { recursive: true });
  await Promise.all([
    writeFile(path.join(root, 'alpha.ts'), 'const alpha = 1;\n'),
    writeFile(path.join(root, 'beta.txt'), 'second file\n'),
    writeFile(path.join(root, 'readme.md'), '# Editor preview\n\n**Markdown works**\n'),
    writeFile(path.join(root, 'image.svg'), '<svg xmlns="http://www.w3.org/2000/svg" width="40" height="40"><rect width="40" height="40" fill="blue"/></svg>'),
  ]);
  await page.request.post('/api/v1/bots', { data: { ...state.bots.find((b: any) => b.id === 'echo'), roots: [root] } });
  await page.request.post('/api/v1/bots', { data: { handle: 'editor-second', name: 'Second bot', roots: [root] } });
  await page.request.post('/api/v1/bots', { data: { handle: 'editor-empty', name: 'Empty bot', roots: [] } });
  await page.request.patch('/api/v1/plugins/monaco-editor', { data: { enabled: true, config: {} } });
  const bookmarks = await (await page.request.get('/api/v1/plugins/monaco-editor/bots/echo/bookmarks')).json();
  for (const mark of bookmarks.bookmarks) await page.request.post('/api/v1/plugins/monaco-editor/bots/echo/bookmarks', { data: { action: 'delete', id: mark.id } });
  await page.reload();
});

test('preview, keep-open, pin, save, bot isolation and live presentation switching', async ({ page }) => {
  const errors: string[] = []; page.on('pageerror', error => errors.push(error.message));
  await launch(page);
  await row(page, 'alpha.ts').click(); await expect(tab(page, 'alpha.ts')).toHaveClass(/is-preview/);
  await row(page, 'beta.txt').click(); await expect(tab(page, 'alpha.ts')).toHaveCount(0);
  await row(page, 'alpha.ts').dblclick(); await expect(tab(page, 'alpha.ts')).not.toHaveClass(/is-preview/);
  await tab(page, 'alpha.ts').click({ button: 'right' }); await page.getByRole('menuitem', { name: /^Pin/ }).click();
  await expect(tab(page, 'alpha.ts')).toHaveClass(/is-pinned/);
  await tab(page, 'alpha.ts').click({ button: 'middle' }); await expect(tab(page, 'alpha.ts')).toBeVisible();
  await editor(page).locator('[data-monaco-host] .view-lines').click({ position: { x: 100, y: 10 } });
  await page.keyboard.press('Control+End'); await page.keyboard.type('// retained draft');
  await expect(tab(page, 'alpha.ts').locator('.code-tab-dirty')).toHaveClass(/is-visible/);
  const second = await page.getByLabel('Editor bot').locator('option').filter({ hasText: 'Second bot' }).getAttribute('value');
  await page.getByLabel('Editor bot').selectOption(second!);
  await row(page, 'alpha.ts').click(); await expect(text(page)).not.toContainText('retained draft');
  await page.getByLabel('Editor bot').selectOption('echo'); await expect(text(page)).toContainText('retained draft');
  await page.getByRole('button', { name: 'Open in Window', exact: true }).click();
  await expect(page.locator('.floating-plugin')).toBeVisible(); await expect(text(page)).toContainText('retained draft');
  await page.getByRole('button', { name: 'Open Embedded', exact: true }).click();
  await editor(page).locator('[data-monaco-host] .view-lines').click({ position: { x: 100, y: 10 } });
  await page.keyboard.press('Control+z'); await expect(text(page)).not.toContainText('retained draft');
  await page.keyboard.press('Control+y'); await expect(text(page)).toContainText('retained draft');
  await page.keyboard.press('Control+s'); await expect(tab(page, 'alpha.ts').locator('.code-tab-dirty')).not.toHaveClass(/is-visible/);
  const current = await (await page.request.get('/api/v1/state')).json();
  const root = current.bots.find((b: any) => b.id === 'echo').roots[0];
  expect(await readFile(path.join(root, 'alpha.ts'), 'utf8')).toContain('retained draft');
  await page.screenshot({ path: test.info().outputPath('editor-desktop.png') });
  expect(errors).toEqual([]);
});

test('empty roots, untitled recovery, scoped shortcuts and close cancellation', async ({ page }) => {
  await launch(page);
  const empty = await page.getByLabel('Editor bot').locator('option').filter({ hasText: 'Empty bot' }).getAttribute('value');
  await page.getByLabel('Editor bot').selectOption(empty!);
  await expect(editor(page).locator('.code-tree-row')).toHaveCount(0);
  await page.locator('.embedded-plugin > header').focus(); await page.keyboard.press('Control+n');
  await page.keyboard.type('recover this draft'); await expect(text(page)).toContainText('recover this draft');
  await page.getByRole('button', { name: 'Close plugin' }).click();
  await page.getByRole('button', { name: 'Cancel', exact: true }).click(); await expect(text(page)).toContainText('recover this draft');
  await page.reload(); await expect(text(page)).toContainText('recover this draft');
  await page.locator('#new-chat').click();
  await expect(page.locator('.embedded-plugin')).toBeHidden();
  const chatId = new URL(page.url()).hash.slice(1);
  const message = page.getByRole('textbox', { name: 'Message', exact: true }); await message.fill('chat draft');
  await page.keyboard.press('Control+p'); await expect(page.locator('.code-picker-overlay')).toHaveCount(0);
  await launch(page); await page.locator(`[data-chat="${chatId}"]`).click(); await expect(message).toHaveText('chat draft');
});

test('quick open, duplicate selection, external changes, previews and mobile layout', async ({ page }) => {
  await launch(page); await page.locator('.embedded-plugin > header').focus(); await page.keyboard.press('Control+p');
  await page.locator('.code-picker input').fill('beta.txt'); await expect(page.locator('.code-picker [role=option]').first()).toContainText('beta.txt'); await page.keyboard.press('Enter');
  await expect(text(page)).toContainText('second file');
  await page.keyboard.press('Control+a'); await page.keyboard.press('Control+d');
  await expect(text(page)).toContainText(/second\s+file.*second\s+file/s);
  await page.keyboard.press('Control+z'); await page.keyboard.press('Control+s'); await expect(tab(page, 'beta.txt').locator('.code-tab-dirty')).not.toHaveClass(/is-visible/);
  const state = await (await page.request.get('/api/v1/state')).json(); const root = state.bots.find((b: any) => b.id === 'echo').roots[0];
  await writeFile(path.join(root, 'beta.txt'), 'external update\n'); await expect(text(page)).toContainText('external update');
  await row(page, 'readme.md').click(); await expect(editor(page).locator('[data-markdown-mode]')).not.toHaveCount(0);
  await row(page, 'image.svg').click(); await expect(editor(page).locator('.code-media-preview img')).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 });
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await expect.poll(() => editor(page).locator('.code-sidebar').evaluate(el => el.getBoundingClientRect().right)).toBeLessThanOrEqual(36);
  await editor(page).getByRole('button', { name: 'Toggle Sidebar', exact: true }).click();
  await expect.poll(() => editor(page).locator('.code-sidebar').evaluate(el => Math.round(el.getBoundingClientRect().left))).toBe(36);
  await row(page, 'image.svg').click();
  await expect.poll(() => editor(page).locator('.code-sidebar').evaluate(el => el.getBoundingClientRect().right)).toBeLessThanOrEqual(36);
  await expect(editor(page).locator('.code-media-preview img')).toBeVisible();
  await page.screenshot({ path: test.info().outputPath('editor-mobile.png') });
});

test('explorer creation, rename, trash restoration and revision conflicts', async ({ page }) => {
  await launch(page);
  await editor(page).getByRole('button', { name: 'New File', exact: true }).click();
  await page.getByRole('dialog').getByLabel('Name', { exact: true }).fill('created.txt');
  await page.getByRole('button', { name: 'Create', exact: true }).click();
  await expect(row(page, 'created.txt')).toBeVisible();
  await row(page, 'created.txt').click(); await page.keyboard.press('F2');
  await editor(page).locator('.code-tree input').fill('renamed.txt'); await page.keyboard.press('Enter');
  await expect(row(page, 'renamed.txt')).toBeVisible();
  await row(page, 'renamed.txt').click(); await page.keyboard.press('Delete');
  await page.getByRole('button', { name: 'Move to Trash', exact: true }).click();
  await expect(row(page, 'renamed.txt')).toHaveCount(0);
  await editor(page).getByRole('button', { name: 'Trash', exact: true }).click();
  await page.locator('.code-trash-item').filter({ hasText: 'renamed.txt' }).getByRole('button', { name: 'Restore', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: 'Close', exact: true }).click();
  await expect(row(page, 'renamed.txt')).toBeVisible();
  await row(page, 'beta.txt').dblclick();
  await editor(page).locator('[data-monaco-host] .view-lines').click({ position: { x: 80, y: 10 } });
  await page.keyboard.press('Control+a'); await page.keyboard.type('local unsaved');
  const state = await (await page.request.get('/api/v1/state')).json(); const root = state.bots.find((b: any) => b.id === 'echo').roots[0];
  await writeFile(path.join(root, 'beta.txt'), 'external conflict\n');
  await expect(tab(page, 'beta.txt').locator('[title="Changed on disk"]')).toBeVisible();
  await expect(text(page)).toContainText('local unsaved'); await page.keyboard.press('Control+s');
  await page.getByRole('button', { name: 'Compare', exact: true }).click();
  await expect(page.locator('.code-diff-dialog .monaco-diff-editor')).toBeVisible();
  await page.locator('.code-diff-dialog').getByRole('button', { name: 'Close', exact: true }).click();
  await page.getByRole('button', { name: 'Overwrite', exact: true }).click();
  await expect(tab(page, 'beta.txt').locator('.code-tab-dirty')).not.toHaveClass(/is-visible/);
  expect(await readFile(path.join(root, 'beta.txt'), 'utf8')).toBe('local unsaved');
});

test('bookmarks, bundled outline, search replacement, commands and editor preferences', async ({ page }) => {
  await launch(page); await row(page, 'alpha.ts').dblclick();
  await editor(page).locator('[data-monaco-host] .view-lines').click({ position: { x: 80, y: 10 } });
  await page.keyboard.press('Control+k'); await page.keyboard.press('k');
  await expect.poll(async () => (await (await page.request.get('/api/v1/plugins/monaco-editor/bots/echo/bookmarks')).json()).bookmarks.length).toBe(1);
  await editor(page).getByRole('button', { name: 'OUTLINE', exact: true }).click();
  await expect(editor(page).getByRole('tree', { name: 'Outline symbols' })).toContainText('alpha');
  await page.locator('.embedded-plugin > header').focus(); await page.keyboard.press('Control+Shift+h');
  await editor(page).getByLabel('Search workspace', { exact: true }).fill('second');
  await editor(page).getByLabel('Replace in workspace', { exact: true }).fill('changed');
  await expect(editor(page).locator('[data-search-summary]')).toContainText('1 result');
  await editor(page).getByRole('button', { name: 'Replace All', exact: true }).click();
  await page.getByRole('dialog').getByRole('button', { name: /Replace/, exact: false }).click();
  const state = await (await page.request.get('/api/v1/state')).json(); const root = state.bots.find((b: any) => b.id === 'echo').roots[0];
  await expect.poll(() => readFile(path.join(root, 'beta.txt'), 'utf8')).toContain('changed file');
  await page.getByRole('button', { name: 'Editor Settings', exact: true }).click();
  await page.getByRole('dialog').getByLabel('Font size').fill('16');
  await page.getByRole('dialog').getByRole('button', { name: 'Save', exact: true }).click();
  await expect.poll(async () => (await (await page.request.get('/api/v1/state')).json()).plugins.find((p: any) => p.id === 'monaco-editor').config.editorFontSize).toBe(16);
  await expect(page.locator('.embedded-plugin')).toBeVisible();
  await page.locator('.embedded-plugin > header').focus(); await page.keyboard.press('Control+Shift+p');
  await page.locator('.code-picker input').fill('line endings to CRLF');
  await page.keyboard.press('Enter');
  await expect(editor(page).locator('[data-status=eol]')).toHaveText('CRLF');
});

test('edits made during a save remain dirty and survive access revocation and disablement', async ({ page }) => {
  await launch(page); await row(page, 'beta.txt').dblclick();
  await editor(page).locator('[data-monaco-host] .view-lines').click({ position: { x: 80, y: 10 } });
  await page.keyboard.press('Control+a'); await page.keyboard.type('first save');
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  let arrived!: () => void;
  const saving = new Promise<void>(resolve => { arrived = resolve; });
  await page.route('**/monaco-editor/bots/echo/fs/file', async route => {
    if (route.request().method() !== 'PUT') return route.continue();
    const response = await route.fetch(); arrived(); await gate; await route.fulfill({ response });
  });
  await page.keyboard.press('Control+s'); await saving;
  await page.keyboard.press('Control+End'); await page.keyboard.type(' and later edits');
  release();
  await expect(tab(page, 'beta.txt').locator('.code-tab-dirty')).toHaveClass(/is-visible/);
  await page.unroute('**/monaco-editor/bots/echo/fs/file');
  const state = await (await page.request.get('/api/v1/state')).json();
  const bot = state.bots.find((b: any) => b.id === 'echo');
  await expect.poll(() => readFile(path.join(bot.roots[0], 'beta.txt'), 'utf8')).toBe('first save');
  await page.request.post('/api/v1/bots', { data: { ...bot, roots: [] } });
  await expect(editor(page).locator('.code-tree-row')).toHaveCount(0);
  await page.locator('.embedded-plugin > header').focus(); await page.keyboard.press('Control+s');
  await expect(tab(page, 'beta.txt').locator('.code-tab-dirty')).toHaveClass(/is-visible/);
  expect(await readFile(path.join(bot.roots[0], 'beta.txt'), 'utf8')).toBe('first save');
  await page.request.patch('/api/v1/plugins/monaco-editor', { data: { enabled: false, config: {} } });
  await expect(page.locator('[data-plugin-id="monaco-editor"]')).toHaveCount(0);
  await page.request.patch('/api/v1/plugins/monaco-editor', { data: { enabled: true, config: {} } });
  await launch(page);
  await expect(text(page)).toContainText('first save and later edits');
  await expect(tab(page, 'beta.txt').locator('.code-tab-dirty')).toHaveClass(/is-visible/);
});

test('explicit closure checks every dirty tab across bot sessions', async ({ page }) => {
  await launch(page);
  for (const content of ['first draft', 'second draft']) {
    await page.locator('.embedded-plugin > header').focus(); await page.keyboard.press('Control+n');
    await page.keyboard.type(content);
  }
  const second = await page.getByLabel('Editor bot').locator('option').filter({ hasText: 'Second bot' }).getAttribute('value');
  await page.getByLabel('Editor bot').selectOption(second!);
  await expect(editor(page).locator('.code-app-shell')).toHaveAttribute('aria-busy', 'false');
  await page.locator('.embedded-plugin > header').focus(); await page.keyboard.press('Control+n'); await page.keyboard.type('other bot draft');
  await expect(text(page)).toContainText('other bot draft');
  await page.getByRole('button', { name: 'Close plugin' }).click();
  for (const title of ['Echo: Untitled-1', 'Echo: Untitled-2', 'Second bot: Untitled-1']) {
    await expect(page.getByRole('dialog')).toContainText(title);
    await page.getByRole('button', { name: 'Discard', exact: true }).click();
  }
  await expect(page.locator('[data-plugin-id="monaco-editor"]')).toHaveCount(0);
});
