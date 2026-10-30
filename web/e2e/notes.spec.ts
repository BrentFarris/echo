import { test, expect, type Page } from '@playwright/test';

const base = '/api/v1/plugins/notes/notes';
async function json(page: Page, url: string, method = 'GET', data?: unknown) {
  const response = await page.request.fetch(url, { method, data });
  expect(response.ok(), await response.text()).toBe(true); return response.json();
}
async function openNotes(page: Page) {
  await page.locator('#apps-button').click();
  await page.locator('[data-app-id="notes"]').getByRole('button', { name: 'Open Notes', exact: true }).click();
  await expect(page.locator('.notes-app')).toBeVisible();
}
test.beforeEach(async ({ page }) => {
  await page.goto('/'); await expect(page.locator('#apps-button')).toBeVisible();
  await json(page, '/api/v1/plugins/notes', 'PATCH', { enabled: true, config: {} });
  const directory = await json(page, '/api/v1/swarm/peers');
  for (const peer of directory.peers) if (peer.authorized) await json(page, `/api/v1/swarm/peers/${peer.node.nodeId}`, 'DELETE');
  for (const note of await json(page, base)) await json(page, `${base}/${note.id}?revision=${note.revision}`, 'DELETE');
  await page.reload();
});
test.afterEach(async ({ page }) => {
  const response = await page.request.get('/api/v1/swarm/peers');
  if (response.ok()) for (const peer of (await response.json()).peers) if (peer.authorized) await page.request.delete(`/api/v1/swarm/peers/${peer.node.nodeId}`);
});

test('Markdown autosave, tags, bots, read-only preview and window presentation', async ({ page }) => {
  await openNotes(page);
  const app = page.locator('.notes-app');
  await app.getByRole('button', { name: 'New note', exact: true }).click();
  await app.getByRole('textbox', { name: 'Note title', exact: true }).fill('Launch checklist');
  const source = app.getByRole('textbox', { name: 'Markdown source', exact: true });
  await source.fill('# Launch\n\n- [ ] Review **release**\n\n#Launch #work @echo');
  await expect(app.locator('[data-status]')).toHaveText('Saved');
  await app.getByRole('button', { name: 'Done', exact: true }).click();
  await expect(app.getByRole('heading', { name: 'Launch', exact: true })).toBeVisible();
  await expect(app.locator('[data-preview] input[type="checkbox"]')).toBeDisabled();
  await expect(source).toBeHidden();
  await expect(app.getByRole('textbox', { name: 'Note title', exact: true })).toHaveAttribute('readonly', '');
  await app.getByRole('button', { name: '#launch', exact: true }).click();
  await expect(app.getByLabel('Filter notes by hashtag')).toHaveValue('launch');
  await app.getByRole('button', { name: '@echo', exact: true }).click();
  await expect(app.locator('[data-note]')).toHaveCount(1);
  await page.getByRole('button', { name: 'Open in Window', exact: true }).click();
  await expect(page.locator('.floating-plugin .notes-app')).toBeVisible();
  await app.getByRole('button', { name: 'Edit', exact: true }).click();
  await source.fill('# Window draft\n\n#launch @echo');
  await page.getByRole('button', { name: 'Open Embedded', exact: true }).click();
  await expect(source).toHaveValue('# Window draft\n\n#launch @echo');
  await app.getByRole('button', { name: 'Done', exact: true }).click();
  await page.reload();
  await expect(app).toBeVisible();
  await app.locator('[data-note]').click();
  await expect(app.getByRole('heading', { name: 'Window draft' })).toBeVisible();
  await expect(app.getByRole('button', { name: 'Edit', exact: true })).toBeVisible();
  await expect(app.locator('[data-conflict]')).toBeHidden();
  await page.screenshot({ path: test.info().outputPath('notes-preview.png') });
  page.once('dialog', dialog => dialog.accept());
  await app.getByRole('button', { name: 'Delete note', exact: true }).click();
  await expect(app.locator('[data-note]')).toHaveCount(0);
});

test('combined machines, cached offline reading and reconnect', async ({ page }) => {
  const node = await json(page, '/api/v1/swarm/peers', 'POST', { url: 'http://127.0.0.1:3744', password: 'echo-test-password' });
  const remote = `/api/v1/swarm/instances/${node.nodeId}/plugins/notes`;
  await json(page, remote, 'PATCH', { enabled: true, config: {} });
  for (const note of await json(page, `${remote}/notes`)) await json(page, `${remote}/notes/${note.id}?revision=${note.revision}`, 'DELETE');
  await json(page, `${remote}/notes`, 'POST', { title: 'Linux notebook', markdown: '# Remote\n\n#swarm @echo' });
  await json(page, base, 'POST', { title: 'Desktop notebook', markdown: '# Local\n\n#swarm' });
  await expect.poll(async () => (await json(page, '/api/v1/swarm/state')).instances.find((i: any) => i.node.nodeId === node.nodeId)?.state?.plugins.some((p: any) => p.id === 'notes' && p.enabled)).toBe(true);
  await page.reload(); await openNotes(page);
  const app = page.locator('.notes-app');
  await expect(app.locator('[data-note]')).toHaveCount(2);
  await app.getByLabel('Filter notes by machine').selectOption(node.nodeId);
  await expect(app.locator('[data-note]')).toHaveCount(1);
  await app.locator('[data-note]').click();
  await expect(app.getByRole('heading', { name: 'Remote', exact: true })).toBeVisible();
  await json(page, `/api/v1/swarm/peers/${node.nodeId}`, 'PATCH', { disconnected: true });
  await expect(app.getByRole('button', { name: 'Edit', exact: true })).toBeDisabled();
  await expect(app.getByRole('heading', { name: 'Remote', exact: true })).toBeVisible();
  await app.getByLabel('Filter notes by machine').selectOption('');
  await app.getByRole('button', { name: /Desktop notebook/ }).click();
  await expect(app.getByRole('button', { name: 'Edit', exact: true })).toBeEnabled();
  await json(page, `/api/v1/swarm/peers/${node.nodeId}`, 'PATCH', { disconnected: false });
  await app.getByRole('button', { name: /Linux notebook/ }).click();
  await expect(app.getByRole('button', { name: 'Edit', exact: true })).toBeEnabled({ timeout: 15000 });
});

test('a failed autosave retains its draft through reload and resolves conflicts by copying', async ({ page }) => {
  const note = await json(page, base, 'POST', { title: 'Conflicted note', markdown: 'original' });
  await openNotes(page);
  const app = page.locator('.notes-app');
  await app.locator('[data-note]').click();
  await app.getByRole('button', { name: 'Edit', exact: true }).click();
  await page.route(`**${base}/${note.id}`, async route => {
    if (route.request().method() === 'PUT') await route.abort(); else await route.continue();
  });
  await app.getByRole('textbox', { name: 'Markdown source' }).fill('my retained draft');
  await expect(app.locator('[data-status]')).toContainText(/fetch|Failed|Network|network/i);
  await json(page, `${base}/${note.id}`, 'PUT', { title: 'Changed elsewhere', markdown: 'remote changes', revision: 1 });
  // Reading in another tab must not erase the first tab's recovery draft.
  const reader = await page.context().newPage();
  await reader.goto('/'); await openNotes(reader);
  await expect(reader.locator('.notes-app [data-note]')).toHaveCount(1);
  await reader.close();
  page.on('dialog', dialog => dialog.accept());
  await page.reload();
  await expect(app).toBeVisible();
  await app.locator('[data-note]').click();
  await expect(app.locator('[data-conflict]')).toBeVisible();
  await expect(app.locator('[data-preview]')).toContainText('my retained draft');
  await app.getByRole('button', { name: 'Save draft as new note' }).click();
  await expect(app.locator('[data-note]')).toHaveCount(2);
  const saved = await json(page, base);
  expect(saved.map((n: any) => n.markdown).sort()).toEqual(['my retained draft', 'remote changes']);
});
