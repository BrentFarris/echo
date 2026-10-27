import { test, expect, type Page } from '@playwright/test';
import { mkdir, writeFile, readFile } from 'node:fs/promises';
import path from 'node:path';

const password = 'echo-test-password';
async function json(page: Page, url: string, method = 'GET', data?: unknown) {
  const response = await page.request.fetch(url, { method, data });
  expect(response.ok(), await response.text()).toBe(true); return response.json();
}
async function connect(page: Page) {
  await page.goto('/'); await expect(page.locator('#apps-button')).toBeVisible();
  const directory = await json(page, '/api/v1/swarm/peers');
  for (const peer of directory.peers) if (peer.authorized) await json(page, `/api/v1/swarm/peers/${peer.node.nodeId}`, 'DELETE');
  const nodes: string[] = [];
  for (const port of [3744, 3745]) { const node = await json(page, '/api/v1/swarm/peers', 'POST', { url: `http://127.0.0.1:${port}`, password }); nodes.push(node.nodeId); }
  for (const node of ['', ...nodes]) {
    const api = node ? `/api/v1/swarm/instances/${node}` : '/api/v1';
    const state = await json(page, `${api}/state`);
    for (const c of state.chats) await json(page, `${api}/chats/${c.id}`, 'DELETE');
    for (const b of state.bots) if (b.id !== 'echo') await json(page, `${api}/bots/${b.id}`, 'DELETE');
    for (const s of state.schedules) await json(page, `${api}/schedules/${s.id}`, 'DELETE');
    await json(page, `${api}/bots`, 'POST', { handle: 'kaiju', name: 'Kaiju', prompt: 'Complete the requested work.' });
    await json(page, `${api}/endpoints`, 'POST', { endpoint: { id: 'test', name: 'Test model', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', contextTokens: 32768 } });
  }
  await page.reload(); return nodes;
}
test.afterEach(async ({ page }) => {
  const result = await page.request.get('/api/v1/swarm/peers'); if (!result.ok()) return;
  for (const peer of (await result.json()).peers) if (peer.authorized) await page.request.delete(`/api/v1/swarm/peers/${peer.node.nodeId}`);
  await page.request.patch('/api/v1/plugins/swarm', { data: { enabled: false, config: {} } });
});

test('remote plugin bridge, schedules and development stay bound to the selected host', async ({ page }) => {
  const [linux] = await connect(page), remote = `/api/v1/swarm/instances/${linux}`;
  const html = `<button id="write" disabled>Write host value</button><output id="result"></output><script>
    let identity; addEventListener('message', e => { if(e.source!==parent)return;
      if(e.data.type==='echo-plugin-init'){identity=e.data;document.querySelector('#write').disabled=false;}
      if(e.data.type==='echo-plugin-response')document.querySelector('#result').textContent=e.data.error||'Saved';
    }); document.querySelector('#write').onclick=()=>parent.postMessage({...identity,type:'echo-plugin-request',id:'write',method:'storage.set',params:{key:'owner',value:'linux'}},'*');
    parent.postMessage({type:'echo-plugin-ready'},'*');</script>`;
  const manifest = { api: 1, id: 'swarm-storage', name: 'Swarm storage', version: '1', scope: 'bot', capabilities: ['storage.get', 'storage.set'], app: { viewId: 'main' }, views: [{ id: 'main', title: 'Swarm storage', kind: 'floating', html }] };
  const installed = await json(page, 'http://127.0.0.1:3744/__fixtures/app', 'POST', manifest);
  if (installed.pending) await json(page, `${remote}/plugins/swarm-storage/approve`, 'POST', { digest: installed.digest });
  await json(page, `${remote}/plugins/swarm-storage`, 'PATCH', { enabled: true, config: {} });
  await page.reload(); await page.locator('#apps-button').click();
  await page.locator(`[data-app-id="${linux}:swarm-storage"]`).getByRole('button', { name: 'Open Swarm storage', exact: true }).click();
  const frame = page.frameLocator('iframe[title="Swarm storage"]');
  await frame.getByRole('button').click(); await expect(frame.locator('#result')).toHaveText('Saved');
  expect(await json(page, `${remote}/plugins/swarm-storage/bridge`, 'POST', { botId: 'echo', method: 'storage.get', params: { key: 'owner' } })).toBe('linux');
  expect((await page.request.post('/api/v1/plugins/swarm-storage/bridge', { data: { botId: 'echo', method: 'storage.get', params: { key: 'owner' } } })).ok()).toBe(false);
  await expect(page.locator('iframe[title="Swarm storage"]')).toHaveAttribute('sandbox', 'allow-scripts');
  await page.getByRole('button', { name: 'Close plugin', exact: true }).click();
  await page.locator('#settings-button').click(); await page.getByLabel('Settings machine').selectOption(linux);
  await page.locator('.settings-nav [data-section="calendar"]').click(); await page.locator('#new-schedule').click();
  await page.getByLabel('Prompt', { exact: true }).fill('Remote scheduled build');
  await page.getByRole('button', { name: 'Save schedule', exact: true }).click();
  await expect.poll(async () => (await json(page, `${remote}/state`)).schedules.length).toBe(1);
  const schedule = (await json(page, `${remote}/state`)).schedules[0]; expect(schedule.generation.endpointId).toBe('test');
  expect((await json(page, '/api/v1/state')).schedules).toHaveLength(0);
  let reloads = 0, health = 0; const actions: string[] = [];
  page.on('load', () => { reloads++; });
  await page.route(`**${remote}/development**`, async route => {
    const request = route.request(), suffix = new URL(request.url()).pathname.slice(remote.length);
    const operation = { id: 'remote-op', action: 'relaunch', status: 'restarting', stage: 'Restarting', logPath: '/remote/log', logTail: '' };
    if (request.method() === 'POST') { actions.push(suffix); await route.fulfill({ status: 202, json: { instanceId: 'old', operation } }); }
    else if (suffix.includes('/operations/')) await route.fulfill({ json: operation });
    else await route.fulfill({ json: { repositoryPath: '', repositoryValid: false, instanceId: health ? 'new' : 'old' } });
  });
  await page.route(`**${remote}/health`, route => { health++; return route.fulfill({ json: { instanceId: 'new' } }); });
  await page.route('**/api/v1/development/relaunch', route => { actions.push('wrong-host'); return route.fulfill({ status: 500 }); });
  await page.locator('.settings-nav [data-section="development"]').click();
  page.once('dialog', dialog => dialog.accept()); await page.getByRole('button', { name: 'Relaunch', exact: true }).click();
  await expect(page.locator('[data-development-status]')).toHaveText('Remote Echo reconnected.');
  expect(actions).toEqual(['/development/relaunch']); expect(health).toBeGreaterThan(0); expect(reloads).toBe(0);
});

test('three machines, existing remote chats, namespace chips and remote-only dispatch', async ({ page }) => {
  const [linux] = await connect(page), remote = `/api/v1/swarm/instances/${linux}`;
  const old = await json(page, `${remote}/chats`, 'POST', {});
  await json(page, `${remote}/chats/${old.id}/messages`, 'POST', { content: '@linux.kaiju existing remote conversation', requestId: 'existing-remote' });
  await page.reload();
  const remoteRow = page.locator(`[data-chat="${linux}:${old.id}"]`);
  await expect(remoteRow).toContainText('linux'); await remoteRow.click();
  await expect(page.locator('.message.assistant')).toContainText('Completed the requested work.');
  await page.locator('#new-chat').click();
  await page.getByRole('textbox', { name: 'Message', exact: true }).fill('@kaiju @linux.kaiju @mac.kaiju build');
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(page.getByRole('tab', { name: 'Kaiju', exact: true })).toHaveCount(3);
  for (const tab of await page.getByRole('tab', { name: 'Kaiju', exact: true }).all()) { await tab.click(); await expect(page.locator('.message.assistant')).toContainText('Completed the requested work.'); }
  await expect(page.locator('.chat-row.active')).toContainText('linux'); await expect(page.locator('.chat-row.active')).toContainText('mac');
  await expect(page.getByRole('button', { name: 'Stop response' })).toHaveCount(0);
  const local = await json(page, '/api/v1/state');
  for (const e of local.endpoints) await json(page, `/api/v1/endpoints/${e.id}`, 'DELETE');
  await page.locator('#new-chat').click();
  await page.getByRole('textbox', { name: 'Message', exact: true }).fill('@linux.kaiju remote only');
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(page.locator('.message.assistant')).toContainText('Completed the requested work.');
  const localChat = (await json(page, '/api/v1/state')).chats.find((c: any) => c.id !== old.id);
  const localUser = (await json(page, `/api/v1/chats/${localChat.id}/messages`)).find((m: any) => m.role === 'user').content;
  let releaseHistory!: () => void;
  const pendingHistory = new Promise<void>(resolve => { releaseHistory = resolve; });
  await page.route(`**${remote}/chats/${old.id}/messages`, async route => { await pendingHistory; await route.continue(); });
  const slowRequest = page.waitForRequest(request => request.url().endsWith(`${remote}/chats/${old.id}/messages`));
  await remoteRow.click(); await slowRequest;
  await page.locator(`[data-chat="${localChat.id}"]`).click();
  const lateResponse = page.waitForResponse(response => response.url().endsWith(`${remote}/chats/${old.id}/messages`));
  releaseHistory();
  await (await lateResponse).finished(); await page.waitForTimeout(150);
  await expect(page.locator('.chat-row.active [data-chat]')).toHaveAttribute('data-chat', localChat.id);
  await expect(page.locator('.message.user')).toContainText(localUser);
  await page.screenshot({ path: test.info().outputPath('swarm-chat.png') });
});

test('simultaneous Monaco windows keep owners, recovery drafts and settings isolated', async ({ page }, info) => {
  await page.setViewportSize({ width: 1920, height: 1080 });
  const [linux] = await connect(page), remote = `/api/v1/swarm/instances/${linux}`;
  const roots = [path.resolve('../.test-data', `swarm-editor-local-${info.testId}`), path.resolve('../.test-data', `swarm-editor-remote-${info.testId}`)];
  for (const [index, api] of ['/api/v1', remote].entries()) {
    await mkdir(roots[index], { recursive: true }); await writeFile(path.join(roots[index], 'same.txt'), index ? 'remote original' : 'local original');
    const state = await json(page, `${api}/state`), echo = state.bots.find((b: any) => b.id === 'echo');
    await json(page, `${api}/bots`, 'POST', { ...echo, roots: [roots[index]], plugins: {} });
  }
  await page.reload(); await page.locator('#apps-button').click();
  await page.locator('[data-app-id="monaco-editor"]').getByRole('button', { name: 'Open Monaco Editor', exact: true }).click();
  await page.getByRole('button', { name: 'Open in Window', exact: true }).click();
  const localWindow = page.locator('.floating-plugin').first();
  await localWindow.locator('.code-tree-row').filter({ hasText: 'same.txt' }).dblclick();
  await expect(localWindow.locator('.view-lines').first()).toContainText('local original');
  const header = await localWindow.locator(':scope > header').boundingBox();
  await page.mouse.move(header!.x + 60, header!.y + 20); await page.mouse.down(); await page.mouse.move(1250, 120); await page.mouse.up();
  await page.locator('#apps-button').click();
  await page.locator(`[data-app-id="${linux}:monaco-editor"]`).getByRole('button', { name: 'Open Monaco Editor', exact: true }).click();
  const remoteWindow = page.locator('.embedded-plugin');
  await remoteWindow.locator('.code-tree-row').filter({ hasText: 'same.txt' }).dblclick();
  const editor = remoteWindow.locator('.monaco-editor').first();
  await editor.locator('.view-lines').click({ position: { x: 40, y: 10 } }); await page.keyboard.press('Control+End'); await page.keyboard.type(' remote draft');
  await expect(remoteWindow.locator('.view-lines').first()).toContainText('remote draft');
  await json(page, `/api/v1/swarm/peers/${linux}`, 'PATCH', { disconnected: true });
  await expect(remoteWindow.locator('.view-lines').first()).toContainText('remote draft');
  await json(page, `/api/v1/swarm/peers/${linux}`, 'PATCH', { disconnected: false });
  await expect.poll(async () => (await json(page, '/api/v1/swarm/state')).instances.find((i: any) => i.node.nodeId === linux)?.online).toBe(true);
  await editor.locator('.view-lines').click({ position: { x: 40, y: 10 } }); await page.keyboard.press('Control+s');
  await expect.poll(() => readFile(path.join(roots[1], 'same.txt'), 'utf8')).toContain('remote draft');
  expect(await readFile(path.join(roots[0], 'same.txt'), 'utf8')).toBe('local original');
  await page.locator('#settings-button').click();
  await page.getByLabel('Settings machine').selectOption(linux);
  await expect(page.locator('.settings-content')).toContainText('linux.echo');
  await page.screenshot({ path: test.info().outputPath('swarm-monaco.png') });
});
