import { test, expect, type Page } from '@playwright/test';
import { mkdtemp, mkdir, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

async function setup(page: Page, root: string) {
  await page.goto('/');
  await expect(page.getByRole('button', { name: '＋ New chat', exact: true })).toBeVisible();
  await page.request.post('/api/v1/session', { data: {} });
  const state = await (await page.request.get('/api/v1/state')).json();
  for (const chat of state.chats) { await page.request.delete(`/api/v1/chats/${chat.id}`); await page.request.delete(`/api/v1/chats/${chat.id}/permanent`); }
  for (const bot of state.bots) if (bot.id !== 'echo') await page.request.delete(`/api/v1/bots/${bot.id}`);
  const response = await page.request.post('/api/v1/bots', { data: { handle: 'kaiju', name: 'Kaiju', roots: [root], plugins: {} } });
  expect(response.ok()).toBe(true);
  await page.reload(); await page.getByRole('button', { name: '＋ New chat', exact: true }).click();
}

test('folder references rank exact matches, complete Windows paths and remain usable on mobile', async ({ page }) => {
  const temp = await mkdtemp(join(tmpdir(), 'echo-reference-')), root = join(temp, 'kaiju');
  try {
    await mkdir(join(root, 'a-bulk'), { recursive: true }); await mkdir(join(root, 'z', 'game', 'tools'), { recursive: true });
    await Promise.all(Array.from({ length: 230 }, (_, i) => writeFile(join(root, 'a-bulk', `tools-${i}.txt`), '')));
    await setup(page, root);
    const composer = page.getByRole('textbox', { name: 'Message', exact: true }), picker = page.getByRole('listbox', { name: 'File and bot suggestions' });
    await composer.fill('@kai'); await expect(picker.getByRole('option')).toContainText('@kaiju'); await composer.press('Tab');
    await composer.pressSequentially('#tools');
    await expect(picker.getByRole('option').first().locator('strong')).toHaveText('tools');
    await expect(picker.getByRole('option').first().locator('small')).toContainText(join(root, 'z', 'game', 'tools'));
    await composer.press('Tab'); await expect(composer.locator('.reference-chip').last()).toContainText('tools');
    await expect(picker).toBeHidden();

    await composer.fill('@kaiju #z\\game\\to');
    await expect(picker.getByRole('option').first().locator('strong')).toHaveText('tools');
    await composer.press('Enter'); await expect.poll(() => composer.evaluate((e: any) => e.value)).toBe('@kaiju #tools ');
    await expect(page.locator('.message')).toHaveCount(0);

    await page.setViewportSize({ width: 390, height: 844 });
    await composer.fill('@kaiju #kaiju'); await expect(picker.getByRole('option').first().locator('strong')).toHaveText('kaiju');
    const bounds = await picker.boundingBox(); expect(bounds!.x).toBeGreaterThanOrEqual(0); expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(390);
    await composer.press('ArrowDown'); await expect(picker.getByRole('option').nth(1)).toHaveAttribute('aria-selected', 'true');
    await composer.press('Escape'); await expect(picker).toBeHidden();
    await composer.fill('@kaiju #__does_not_exist__'); await expect(picker).toContainText('No matching files or folders.');
    await composer.press('Enter'); await expect(page.locator('.message')).toHaveCount(0);
    await composer.press('Escape'); await expect(picker).toBeHidden();
    await composer.fill('@missing #tools'); await expect(picker).toContainText('unknown bot @missing');
  } finally { await rm(temp, { recursive: true, force: true }); }
});

test('pending reference requests cannot send a message or reopen a dismissed picker', async ({ page }) => {
  const root = await mkdtemp(join(tmpdir(), 'echo-reference-pending-'));
  let release!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  let started!: () => void;
  const requestStarted = new Promise<void>(resolve => { started = resolve; });
  let finished!: () => void;
  const requestFinished = new Promise<void>(resolve => { finished = resolve; });
  try {
    await setup(page, root);
    await page.route('**/api/v1/references', async route => {
      started(); await gate;
      try { await route.fulfill({ json: [{ name: 'late-folder', path: join(root, 'late-folder'), directory: true, allowedBots: ['kaiju'] }] }); } catch { /* The dismissed request may have been aborted. */ }
      finally { finished(); }
    });
    const composer = page.getByRole('textbox', { name: 'Message', exact: true }), picker = page.getByRole('listbox', { name: 'File and bot suggestions' });
    await composer.fill('@kaiju #late'); await requestStarted;
    await expect(picker).toContainText('Searching files and folders');
    await composer.press('Enter'); await expect(page.locator('.message')).toHaveCount(0);
    await composer.press('Escape'); release(); await requestFinished;
    await expect(picker).toBeHidden(); await expect(composer).toHaveAttribute('aria-expanded', 'false');
    await page.getByRole('button', { name: '＋ New chat', exact: true }).click();
    await expect(picker).toBeHidden(); await expect(composer).toHaveText('');
  } finally { release(); await rm(root, { recursive: true, force: true }); }
});

test('a local group can reference files and send while the Swarm directory is unavailable', async ({ page }) => {
  const root = await mkdtemp(join(tmpdir(), 'echo-group-files-'));
  let groupId = '';
  try {
    await writeFile(join(root, 'group-review.txt'), 'review');
    await setup(page, root);
    const state = await (await page.request.get('/api/v1/state')).json();
    const bot = state.bots.find((b: any) => b.handle === 'kaiju');
    const saved = await page.request.post('/api/v1/bot-groups', { data: { handle: 'localreview', members: [{ botId: bot.id }] } });
    expect(saved.ok(), await saved.text()).toBe(true); groupId = (await saved.json()).id;
    await page.request.post('/api/v1/endpoints', { data: { endpoint: { id: 'test', name: 'Test model', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', contextTokens: 32768 } } });
    await page.route('**/api/v1/swarm/state', route => route.fulfill({ status: 503, json: { error: 'Directory unavailable' } }));
    await page.reload(); await page.locator('#new-chat').click();
    const composer = page.getByRole('textbox', { name: 'Message', exact: true });
    await composer.fill('@localreview inspect #group-review');
    await expect(page.getByRole('option').filter({ hasText: 'group-review.txt' })).toBeVisible();
    await composer.press('Tab'); await page.getByRole('button', { name: 'Send message', exact: true }).click();
    await expect(page.locator('.message.assistant')).toContainText('Completed the requested work.');
    const messages = await (await page.request.get('/api/v1/chats/' + new URL(page.url()).hash.slice(1) + '/messages')).json();
    const prompt = messages.find((m: any) => m.role === 'user');
    expect(prompt.recipients).toEqual([bot.id]);
    expect(prompt.segments.find((s: any) => s.type === 'file').allowedBots).toEqual([bot.id]);
  } finally {
    if (groupId) await page.request.delete('/api/v1/bot-groups/' + groupId);
    await rm(root, { recursive: true, force: true });
  }
});

test('inline references edit, restore, reveal and persist with distinct paths', async ({ page, context }, testInfo) => {
  const root = await mkdtemp(join(tmpdir(), 'echo-inline-'));
  try {
    await mkdir(join(root, 'map_editor')); await mkdir(join(root, 'other'));
    await writeFile(join(root, 'map_editor', 'camera.go'), 'package game'); await writeFile(join(root, 'other', 'camera.go'), 'package other');
    await setup(page, root);
    await page.request.patch('/api/v1/plugins/monaco-editor', { data: { enabled: false, config: {} } });
    await page.request.post('/api/v1/endpoints', { data: { endpoint: { id: 'test', name: 'Test local model', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', cloud: false, contextTokens: 32768, compact: true } } });
    await page.reload();
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    const composer = page.getByRole('textbox', { name: 'Message', exact: true });
    const picker = page.getByRole('listbox', { name: 'File and bot suggestions' });
    const value = () => composer.evaluate((el: any) => el.value as string);
    await composer.fill('@kai'); await composer.press('Tab');
    await expect(composer.locator('.bot-reference')).toHaveText('@kaiju');
    await composer.pressSequentially('Check out the #map_editor');
    await expect(picker.getByRole('option').first().locator('strong')).toHaveText('map_editor'); await composer.press('Tab');
    await composer.pressSequentially('and tell me if #map_editor/camera.go');
    await expect(picker.getByRole('option').first().locator('strong')).toHaveText('camera.go'); await composer.press('Tab');
    await composer.pressSequentially('is working as intended.');
    await expect(picker).toBeHidden(); await expect(composer.locator('.reference-chip')).toHaveCount(3);
    const draftText = await value();
    await composer.press('Control+End'); await composer.press('Shift+Enter'); await composer.pressSequentially('🙂 Compare #other/camera.go');
    await expect(picker.getByRole('option').first().locator('strong')).toHaveText('camera.go'); await composer.press('Tab');
    await expect(composer.locator('.reference-chip')).toHaveCount(4);
    await composer.press('Control+Z'); await expect(composer.locator('.reference-chip')).toHaveCount(3);
    await composer.press('Control+Shift+Z'); await expect(composer.locator('.reference-chip')).toHaveCount(4);
    const fullText = await value(); expect(fullText).toContain('\n🙂 Compare #camera.go');
    const chatID = new URL(page.url()).hash.slice(1);
    await page.getByRole('button', { name: '＋ New chat', exact: true }).click();
    await page.locator(`[data-chat="${chatID}"]`).click();
    await expect.poll(value).toBe(fullText); await expect(composer.locator('.reference-chip')).toHaveCount(4);
    await composer.press('Control+A'); await composer.press('Control+C');
    await expect.poll(() => page.evaluate(() => navigator.clipboard.readText())).toBe(fullText);
    await composer.press('Control+End');

    const reveals: any[] = [];
    await page.route('**/references/reveal', async route => { reveals.push(route.request().postDataJSON()); await route.fulfill({ json: { path: reveals.at(-1).path, select: true, fallback: false } }); });
    const file = composer.getByRole('button', { name: `Open file ${join(root, 'map_editor', 'camera.go')}`, exact: true });
    await file.click(); await expect.poll(() => reveals.length).toBe(1);
    await file.focus(); await file.press('Enter'); await expect.poll(() => reveals.length).toBe(2);
    const folder = composer.getByRole('button', { name: `Open folder ${join(root, 'map_editor')}`, exact: true });
    await folder.click(); await expect.poll(() => reveals.length).toBe(3);
    await composer.locator('.bot-reference').click();
    const details = page.getByRole('dialog', { name: 'Bot details' });
    await expect(details).toContainText('Kaiju'); await expect(details).toContainText(root);
    await details.press('Escape'); await expect(details).toBeHidden();
    await expect(composer.locator('.bot-reference')).toBeFocused();
    await composer.locator('.bot-reference').click(); await details.getByRole('button', { name: 'Bot settings' }).click();
    await expect(page.getByLabel('Handle', { exact: true })).toHaveValue('kaiju');
    await page.getByRole('dialog').getByRole('button', { name: 'Close', exact: true }).click();

    await page.setViewportSize({ width: 1440, height: 900 }); await page.emulateMedia({ colorScheme: 'dark' });
    await page.screenshot({ path: testInfo.outputPath('reference-desktop.png') });
    await page.setViewportSize({ width: 390, height: 844 });
    await page.screenshot({ path: testInfo.outputPath('reference-mobile.png') });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.getByRole('button', { name: 'Send message', exact: true }).click();
    await expect(page.locator('.message.user .reference-chip')).toHaveCount(4);
    await page.reload(); await expect(page.locator('.message.user .reference-chip')).toHaveCount(4);
    const stored = await (await page.request.get(`/api/v1/chats/${chatID}/messages`)).json();
    const user = stored.find((m: any) => m.role === 'user');
    expect(user.content).toBe(fullText.trim()); expect(user.content).toContain(draftText);
    const files = user.segments.filter((s: any) => s.type === 'file');
    expect(files.map((s: any) => s.path)).toEqual([join(root, 'map_editor'), join(root, 'map_editor', 'camera.go'), join(root, 'other', 'camera.go')]);
    for (const ref of files) expect(user.content.slice(ref.range.start, ref.range.end)).toBe('#' + ref.text);
    await page.locator('.message.user').getByRole('button', { name: `Open file ${join(root, 'other', 'camera.go')}`, exact: true }).click();
    await expect.poll(() => reveals.length).toBe(4); expect(reveals[3].messageId).toBe(user.id);

    const state = await (await page.request.get('/api/v1/state')).json();
    await page.request.delete('/api/v1/bots/' + state.bots.find((b: any) => b.handle === 'kaiju').id); await page.reload();
    await page.locator('.message.user .bot-reference').click();
    await expect(details).toContainText('no longer available'); await expect(details.getByRole('button')).toHaveCount(0);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test('file chips open draft and saved references in Monaco and reuse the editor', async ({ page }) => {
  const root = await mkdtemp(join(tmpdir(), 'echo-reference-editor-'));
  try {
    await mkdir(join(root, 'src'));
    await writeFile(join(root, 'src', 'camera.go'), 'package game');
    await setup(page, root);
    await page.request.patch('/api/v1/plugins/monaco-editor', { data: { enabled: true, config: {} } });
    await page.request.post('/api/v1/endpoints', { data: { endpoint: { id: 'test', name: 'Test local model', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', contextTokens: 32768 } } });
    await page.reload();
    const composer = page.getByRole('textbox', { name: 'Message', exact: true });
    const picker = page.getByRole('listbox', { name: 'File and bot suggestions' });
    await composer.fill('@kaiju inspect #src/camera.go');
    await expect(picker.getByRole('option').first().locator('strong')).toHaveText('camera.go'); await composer.press('Tab');
    await composer.pressSequentially('in #src');
    await expect(picker.getByRole('option').first().locator('strong')).toHaveText('src'); await composer.press('Tab');
    const value = await composer.evaluate((el: any) => el.value as string);
    const chatID = new URL(page.url()).hash.slice(1);
    const fileName = `Open file ${join(root, 'src', 'camera.go')}`;
    const file = composer.getByRole('button', { name: fileName, exact: true });
    const reveals: string[] = [];
    await page.route('**/references/reveal', async route => { reveals.push(route.request().url()); await route.fulfill({ json: { fallback: false } }); });
    await file.click();
    const editor = page.locator('.editor-bot-session:not([hidden])');
    const tab = editor.getByRole('tab', { name: /camera\.go/ });
    await expect(tab).toBeVisible();
    await expect(editor.locator('.monaco-editor .view-lines').first()).toContainText('package game');
    await expect(page.getByLabel('Editor bot').locator('option:checked')).toContainText('Kaiju');
    await editor.locator('[data-monaco-host] .view-lines').click({ position: { x: 100, y: 10 } });
    await page.keyboard.press('Control+End'); await page.keyboard.type(' // unsaved editor draft');
    await page.locator(`[data-chat="${chatID}"]`).click();
    await expect.poll(() => composer.evaluate((el: any) => el.value)).toBe(value);
    await file.focus(); await file.press('Enter');
    await expect(tab).toBeVisible(); await expect(page.locator('.monaco-app')).toHaveCount(1);
    await expect(editor.locator('.monaco-editor .view-lines').first()).toContainText('unsaved editor draft');
    await page.locator(`[data-chat="${chatID}"]`).click();
    await composer.getByRole('button', { name: `Open folder ${join(root, 'src')}`, exact: true }).click();
    await expect(editor.locator('.code-tree-row.is-selected')).toContainText('src');
    await page.locator(`[data-chat="${chatID}"]`).click();
    await page.getByRole('button', { name: 'Send message', exact: true }).click();
    await expect(page.locator('.message.user').getByRole('button', { name: fileName, exact: true })).toBeVisible();
    await page.locator('.message.user').getByRole('button', { name: fileName, exact: true }).click();
    await expect(tab).toBeVisible();
    await expect(editor.locator('.monaco-editor .view-lines').first()).toContainText('unsaved editor draft');
    expect(reveals).toEqual([]);
  } finally { await page.close(); await rm(root, { recursive: true, force: true, maxRetries: 10, retryDelay: 100 }); }
});

test('plain-text paste, atomic deletion and IME preserve reference editing', async ({ page }) => {
  const root = await mkdtemp(join(tmpdir(), 'echo-reference-input-'));
  try {
    await writeFile(join(root, 'My File.txt'), 'text'); await setup(page, root);
    const composer = page.getByRole('textbox', { name: 'Message', exact: true });
    const value = () => composer.evaluate((e: any) => e.value as string);
    await composer.fill('@kaiju #My File');
    await expect(page.getByRole('listbox', { name: 'File and bot suggestions' }).getByRole('option').first().locator('strong')).toHaveText('My File.txt'); await composer.press('Tab');
    await composer.press('Backspace'); await composer.press('Backspace');
    await expect(composer.locator('.reference-chip')).toHaveCount(1); await expect.poll(value).toBe('@kaiju ');
    await composer.press('Control+Z'); await expect(composer.locator('.reference-chip')).toHaveCount(2);
    await composer.press('Control+End');
    await composer.evaluate(el => {
      const data = new DataTransfer(); data.setData('text/plain', '\n<img src=x onerror=alert(1)>'); data.setData('text/html', '<img src=x onerror=alert(1)>');
      el.dispatchEvent(new ClipboardEvent('paste', { bubbles: true, cancelable: true, clipboardData: data }));
    });
    await expect(composer.locator('img')).toHaveCount(0); expect(await value()).toContain('\n<img src=x onerror=alert(1)>');
    await composer.fill('');
    await composer.evaluate(el => {
      el.dispatchEvent(new CompositionEvent('compositionstart', { bubbles: true })); el.textContent = '@kaiju ';
      el.dispatchEvent(new InputEvent('input', { bubbles: true, isComposing: true }));
    });
    await expect(composer.locator('.bot-reference')).toHaveCount(0); await expect(page.locator('.picker')).toBeHidden();
    await composer.evaluate(el => el.dispatchEvent(new CompositionEvent('compositionend', { bubbles: true })));
    await expect(composer.locator('.bot-reference')).toHaveCount(1);
    await composer.fill('`@kaiju` user@kaiju @kaiju '); await expect(composer.locator('.bot-reference')).toHaveCount(1);
  } finally { await rm(root, { recursive: true, force: true }); }
});

test('late validation cannot replace another chat draft', async ({ page }) => {
  const root = await mkdtemp(join(tmpdir(), 'echo-reference-validation-'));
  let release!: () => void, started!: () => void;
  const gate = new Promise<void>(resolve => { release = resolve; });
  const pending = new Promise<void>(resolve => { started = resolve; });
  try {
    await writeFile(join(root, 'camera.go'), 'package game'); await setup(page, root);
    await page.route('**/references/validate', async route => { const body = route.request().postDataJSON(); started(); await gate; await route.fulfill({ json: body.segments.filter((s: any) => s.type === 'file').map((s: any) => ({ ...s, allowedBots: [] })) }); });
    const composer = page.getByRole('textbox', { name: 'Message', exact: true });
    await composer.fill('@kaiju #camera'); await expect(page.getByRole('listbox', { name: 'File and bot suggestions' }).getByRole('option').first().locator('strong')).toHaveText('camera.go'); await composer.press('Tab'); await pending;
    await page.getByRole('button', { name: '＋ New chat', exact: true }).click();
    await composer.fill('A different draft'); release();
    await expect(composer).toHaveText('A different draft'); await expect(composer.locator('.reference-chip')).toHaveCount(0);
  } finally { release(); await rm(root, { recursive: true, force: true }); }
});
