import { test, expect, type Page } from '@playwright/test';
import { mkdtemp, mkdir, writeFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

async function setup(page: Page, root: string) {
  await page.goto('/');
  await expect(page.getByRole('button', { name: '＋ New chat', exact: true })).toBeVisible();
  await page.request.post('/api/v1/session', { data: {} });
  const state = await (await page.request.get('/api/v1/state')).json();
  for (const chat of state.chats) await page.request.delete(`/api/v1/chats/${chat.id}`);
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
    await expect(picker.getByRole('option').first().locator('span')).toHaveText('▣ tools');
    await expect(picker.getByRole('option').first().locator('small')).toContainText(join(root, 'z', 'game', 'tools'));
    await composer.press('Tab'); await expect(page.locator('.refs')).toContainText('#tools · kaiju');
    await expect(picker).toBeHidden();

    await composer.fill('@kaiju #z\\game\\to');
    await expect(picker.getByRole('option').first().locator('span')).toHaveText('▣ tools');
    await composer.press('Enter'); await expect(composer).toHaveValue('@kaiju #tools ');
    await expect(page.locator('.message')).toHaveCount(0);

    await page.setViewportSize({ width: 390, height: 844 });
    await composer.fill('@kaiju #kaiju'); await expect(picker.getByRole('option').first().locator('span')).toHaveText('▣ kaiju');
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
    await expect(picker).toBeHidden(); await expect(composer).toHaveValue('');
  } finally { release(); await rm(root, { recursive: true, force: true }); }
});
