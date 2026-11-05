import { test, expect } from '@playwright/test';
import { mkdir, writeFile, readFile } from 'node:fs/promises';
import { execFileSync } from 'node:child_process';
import path from 'node:path';

for (const provider of ['git', 'fossil']) {
  test(`${provider} commit history opens immutable diffs and restores retained tabs`, async ({ page }) => {
    test.setTimeout(120_000);
    const errors: string[] = []; page.on('pageerror', error => { errors.push(error.message); console.error(error.message); });
    const parent = path.resolve('../.test-data/history-fixtures', `${provider}-${Date.now()}`);
    const root = path.join(parent, 'checkout'); await mkdir(root, { recursive: true });
    const run = (...args: string[]) => execFileSync(provider, args, { cwd: root, timeout: 30_000, env: { ...process.env, GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: process.platform === 'win32' ? 'NUL' : '/dev/null', GIT_AUTHOR_NAME: 'Echo test', GIT_AUTHOR_EMAIL: 'test@example.invalid', GIT_COMMITTER_NAME: 'Echo test', GIT_COMMITTER_EMAIL: 'test@example.invalid' } }).toString();
    if (provider === 'git') { run('init', '-b', 'main'); run('config', 'core.autocrlf', 'false'); }
    else { run('init', '--admin-user', 'echo-test', path.join(parent, 'repo.fossil')); run('open', path.join(parent, 'repo.fossil')); run('user', 'default', 'echo-test'); }
    const commit = (message: string) => {
      if (provider === 'git') { run('commit', '-m', message); return run('rev-parse', 'HEAD').trim(); }
      run('commit', '--nosync', '-m', message); return run('timeline', 'ancestors', 'current', '-n', '1', '--format', '%H').trim().split(/\s/)[0];
    };
    await writeFile(path.join(root, 'history.txt'), 'before history\n');
    run('add', '.'); const first = commit('history first');
    await writeFile(path.join(root, 'history.txt'), 'after history\n');
    await writeFile(path.join(root, 'added.txt'), 'new file\n');
    run('add', '.'); const second = commit('history second');
    await writeFile(path.join(root, 'history.txt'), 'working copy\n');

    await page.goto('/'); await page.request.post('/api/v1/session', { data: {} });
    const state = await (await page.request.get('/api/v1/state')).json();
    const created = await page.request.post('/api/v1/bots', { data: { ...state.bots.find((bot: any) => bot.handle === 'echoapp'), handle: 'echoapp', name: `History ${provider}`, roots: [root] } });
    expect(created.ok()).toBeTruthy();
    await page.request.patch('/api/v1/plugins/monaco-editor', { data: { enabled: true, config: {} } });
    await page.reload(); await page.locator('#apps-button').click();
    await page.locator('[data-app-id="monaco-editor"]').getByRole('button', { name: 'Open Monaco Editor', exact: true }).click();
    const botId = await page.getByLabel('Editor bot').locator('option').filter({ hasText: `History ${provider}` }).getAttribute('value');
    await page.getByLabel('Editor bot').selectOption(botId!);
    const editor = page.locator('.editor-bot-session:not([hidden])');
    await expect(editor.locator('.code-app-shell')).toHaveAttribute('aria-busy', 'false');
    await editor.getByRole('button', { name: 'Source Control', exact: true }).click();
    const history = editor.locator('.scm-history');
    await expect(history.locator('[data-history-action="toggle"]')).toBeVisible({ timeout: 30_000 });
    await expect(history.locator('[data-history-commit]')).toHaveCount(0);
    await history.locator('[data-history-action="toggle"]').click();
    await history.locator(`[data-history-commit="${second}"]`).click();
    const file = history.locator('[data-history-file]').filter({ hasText: 'history.txt' });
    await file.click();
    const diff = editor.locator('[data-monaco-diff-host]');
    await expect(diff.locator('.original .view-lines')).toContainText('before history');
    await expect(diff.locator('.modified .view-lines')).toContainText('after history');
    const secondTab = editor.getByRole('tab', { name: new RegExp(`history.txt \\(${second.slice(0, 10)}\\)`) });
    await expect(secondTab).toHaveClass(/is-preview/);
    await expect(editor.locator('[data-hunk-action]')).toHaveCount(0);
    await diff.locator('.modified').getByRole('textbox').focus(); await page.keyboard.type('should not be inserted');
    await expect(diff.locator('.modified .view-lines')).not.toContainText('should not');
    expect(await readFile(path.join(root, 'history.txt'), 'utf8')).toBe('working copy\n');
    await file.dblclick(); await expect(secondTab).not.toHaveClass(/is-preview/);
    await history.locator(`[data-history-commit="${first}"]`).click();
    await history.locator('[data-history-file]').filter({ hasText: 'history.txt' }).press('Enter');
    await expect(editor.getByRole('tab', { name: /history.txt \(/ })).toHaveCount(2);
    await expect(diff.locator('.modified .view-lines')).toContainText('before history');
    await secondTab.click();
    await editor.screenshot({ path: test.info().outputPath(`${provider}-history.png`) });
    // Session writes are deliberately debounced. Verify durable state before
    // navigating away instead of depending on screenshot timing.
    await expect.poll(() => page.evaluate(() => new Promise<unknown[]>((resolve, reject) => {
      const open = indexedDB.open('echo2-monaco-editor', 2);
      open.onerror = () => reject(open.error);
      open.onsuccess = () => {
        const database = open.result;
        const request = database.transaction('bot-sessions').objectStore('bot-sessions').getAll();
        request.onerror = () => { database.close(); reject(request.error); };
        request.onsuccess = () => {
          const saved = request.result.map(session => ({ active: session.activeTabId, tabs: session.tabs.map((tab: any) => ({ ref: tab.diff?.target.ref, kept: tab.pinned })) }));
          database.close(); resolve(saved);
        };
      };
    }))).toEqual(expect.arrayContaining([expect.objectContaining({ active: expect.stringContaining(second), tabs: expect.arrayContaining([{ ref: first, kept: true }, { ref: second, kept: true }]) })]));
    await page.reload(); await page.getByLabel('Editor bot').selectOption(botId!);
    await expect(editor.getByRole('tab', { name: /history.txt \(/ })).toHaveCount(2, { timeout: 30_000 });
    await expect(diff.locator('.modified .view-lines')).toContainText('after history');
    expect(errors).toEqual([]);
  });
}
