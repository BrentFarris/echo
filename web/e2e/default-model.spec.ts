import { test, expect, type Page } from '@playwright/test';

const preferred = 'new-chat-default-fixture', other = 'new-chat-other-fixture';
let remoteNode = '';
async function json(page: Page, path: string, method = 'GET', data?: unknown) {
  const response = await page.request.fetch('/api/v1' + path, { method, data });
  expect(response.ok(), await response.text()).toBe(true);
  return response.json();
}
async function setup(page: Page) {
  await page.goto('/'); await expect(page.locator('#new-chat')).toBeVisible();
  const state = await json(page, '/state');
  for (const chat of state.chats) { await json(page, `/chats/${chat.id}`, 'DELETE'); await json(page, `/chats/${chat.id}/permanent`, 'DELETE'); }
  for (const id of [preferred, other]) await json(page, '/endpoints', 'POST', { endpoint: { id, name: id === preferred ? 'Preferred fixture' : 'Other fixture', url: 'http://127.0.0.1:3743/v1', model: 'connection-model', temperature: .27, reasoningEffort: 'high' } });
  await page.reload(); await expect(page.locator('#new-chat')).toBeVisible();
}
async function settings(page: Page, node?: string) {
  await page.locator('#settings-button').click();
  if (node) await page.getByLabel('Settings machine').selectOption(node);
  await expect(page.getByLabel('Default model connection')).toBeVisible();
}
async function closeSettings(page: Page) { await page.locator('#settings .dialog-heading button').click(); }
async function setDefault(page: Page, model: string, node?: string) {
  await settings(page, node);
  await page.getByLabel('Default model connection').selectOption(node ? `${node}:${preferred}` : preferred);
  await page.getByLabel('Default model identifier').fill(model);
  await closeSettings(page);
}
async function create(page: Page, shortcut = false) {
  const response = page.waitForResponse(response => response.url().endsWith('/api/v1/chats') && response.request().method() === 'POST');
  if (shortcut) await page.keyboard.press('Control+n'); else await page.locator('#new-chat').click();
  const chat = await (await response).json();
  await expect(page).toHaveURL(new RegExp('#' + chat.id + '$'));
  await expect(page.locator('#composer')).toBeEnabled();
  return chat.id as string;
}
async function modelSettings(page: Page) { await page.locator('.model-settings summary').click(); await expect(page.locator('#model')).toBeVisible(); }
async function chooseOther(page: Page) {
  await modelSettings(page); await page.locator(`#endpoint-options [data-endpoint="${other}"]`).click(); await page.keyboard.press('Escape');
}
const draftText = (page: Page) => page.locator('#composer').evaluate((element: any) => element.value as string);

test.afterEach(async ({ page }) => {
  for (const base of ['', ...(remoteNode ? [`/swarm/instances/${remoteNode}`] : [])]) {
    const response = await page.request.get('/api/v1' + base + '/state');
    if (!response.ok()) continue;
    const state = await response.json();
    for (const bot of state.bots) if (bot.handle === 'newchatpinnedfixture') await json(page, `${base}/bots/${bot.id}`, 'DELETE');
    for (const chat of state.chats) { await json(page, `${base}/chats/${chat.id}`, 'DELETE'); await json(page, `${base}/chats/${chat.id}/permanent`, 'DELETE'); }
    for (const endpoint of state.endpoints) if ([preferred, other].includes(endpoint.id)) await json(page, `${base}/endpoints/${endpoint.id}`, 'DELETE');
  }
  if (remoteNode) await json(page, `/swarm/peers/${remoteNode}`, 'DELETE');
  remoteNode = '';
});

test('General saves a default for button and shortcut creation while existing drafts retain their model', async ({ page }) => {
  await setup(page); await settings(page);
  await expect(page.getByLabel('Default model connection')).toHaveValue('');
  await expect(page.getByLabel('Default model identifier')).toBeDisabled();
  await page.getByLabel('Default model connection').selectOption(preferred);
  await page.getByLabel('Default model identifier').fill('custom-default');
  await page.screenshot({ path: test.info().outputPath('general-default-model-desktop.png') });
  await page.setViewportSize({ width: 390, height: 844 });
  const bounds = await page.getByLabel('Default model connection').boundingBox();
  expect(bounds!.x).toBeGreaterThanOrEqual(0); expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(390);
  await page.screenshot({ path: test.info().outputPath('general-default-model-mobile.png') });
  await page.setViewportSize({ width: 1280, height: 900 }); await closeSettings(page);
  const first = await create(page);
  expect((await json(page, '/state')).chats.find((chat: any) => chat.id === first).generation).toEqual({ endpointId: preferred, model: 'custom-default', thinking: '', temperature: .27 });
  await expect(page.locator('#model-name')).toHaveText('Preferred fixture');
  await chooseOther(page); await page.locator('#composer').fill('Keep this draft');
  await setDefault(page, 'next-default');
  await expect(page.locator('#model-name')).toHaveText('Other fixture');
  await expect.poll(() => draftText(page)).toBe('Keep this draft');
  const next = await create(page, true);
  await expect(page.locator('#model-name')).toHaveText('Preferred fixture');
  expect((await json(page, '/state')).chats.find((chat: any) => chat.id === next).generation.model).toBe('next-default');
  await page.locator(`[data-chat="${first}"]`).click();
  await expect(page.locator('#model-name')).toHaveText('Other fixture');
  await expect.poll(() => draftText(page)).toBe('Keep this draft');
  await page.reload(); await expect(page.locator('#model-name')).toHaveText('Other fixture');
  await settings(page);
  await expect(page.getByLabel('Default model identifier')).toHaveValue('next-default');
  await closeSettings(page);
});

test('welcome selection follows the stored default once and direct sending preserves a manual choice', async ({ page }) => {
  await setup(page); await setDefault(page, 'welcome-default');
  await page.reload(); await expect(page.locator('#model-name')).toHaveText('Preferred fixture');
  await modelSettings(page); await expect(page.locator('#model')).toHaveValue('welcome-default'); await page.keyboard.press('Escape');
  await chooseOther(page);
  // A background inventory refresh must not restore the preference over a manual choice.
  await json(page, '/bots', 'POST', { id: 'echo', name: 'Echo' });
  await expect(page.locator('#model-name')).toHaveText('Other fixture');
  await page.locator('#composer').fill('Default model welcome fixture');
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(page.locator('.chat-log')).toContainText('Completed the requested work.');
  const chat = (await json(page, '/state')).chats[0];
  expect(chat.generation.endpointId).toBe(other);
  await settings(page); await expect(page.getByLabel('Default model identifier')).toHaveValue('welcome-default');
  await page.getByLabel('Default model connection').selectOption(''); await closeSettings(page);
  await create(page);
  const state = await json(page, '/state');
  await expect(page.locator('#model-name')).toHaveText(state.endpoints[0].name);
});

test('bot overrides take precedence over the initial selector without changing it', async ({ page }) => {
  await setup(page); await setDefault(page, 'chat-default');
  const bot = await json(page, '/bots', 'POST', { handle: 'newchatpinnedfixture', modelOverride: { endpointId: other, model: 'pinned-model' } });
  await page.reload();
  const inherited = await create(page);
  await page.locator('#composer').fill('Inherited model fixture');
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(page.locator('.chat-log')).toContainText('Completed the requested work.');
  let state = await json(page, '/state');
  expect(state.runs.find((run: any) => run.chatId === inherited).generation).toMatchObject({ endpointId: preferred, model: 'chat-default' });
  await page.getByLabel('Bot for new chat').selectOption(bot.id);
  const pinned = await create(page);
  await page.locator('#composer').pressSequentially('Pinned model fixture');
  await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(page.locator('.chat-log')).toContainText('Completed the requested work.');
  state = await json(page, '/state');
  expect(state.runs.find((run: any) => run.chatId === pinned).generation).toMatchObject({ endpointId: other, model: 'pinned-model' });
  await expect(page.locator('#model-name')).toHaveText('Preferred fixture');
});

test('General keeps independent defaults for Swarm machines and new chats seed both choices', async ({ page }) => {
  await setup(page);
  const paired = await json(page, '/swarm/peers', 'POST', { url: 'http://127.0.0.1:3744', password: 'echo-test-password' });
  remoteNode = paired.nodeId;
  const base = `/swarm/instances/${remoteNode}`;
  await json(page, base + '/endpoints', 'POST', { endpoint: { id: preferred, name: 'Remote preferred fixture', url: 'http://127.0.0.1:3743/v1', model: 'remote-configured' } });
  await expect.poll(async () => (await json(page, '/swarm/state')).instances.find((instance: any) => instance.node.nodeId === remoteNode)?.state?.endpoints.some((endpoint: any) => endpoint.id === preferred)).toBe(true);
  await page.reload(); await setDefault(page, 'local-default'); await setDefault(page, 'remote-default', remoteNode);
  await page.reload();
  await settings(page, remoteNode); await expect(page.getByLabel('Default model identifier')).toHaveValue('remote-default');
  const localNode = (await json(page, '/swarm/state')).node.nodeId;
  await page.getByLabel('Settings machine').selectOption(localNode);
  await expect(page.getByLabel('Default model identifier')).toHaveValue('local-default'); await closeSettings(page);
  await create(page); await modelSettings(page);
  await expect(page.getByLabel('Model machine', { exact: true })).toHaveValue(localNode);
  await expect(page.locator('#model')).toHaveValue('local-default');
  await page.getByLabel('Model machine', { exact: true }).selectOption(remoteNode);
  await expect(page.locator('#model')).toHaveValue('remote-default');
  await expect(page.locator('#model-name')).toHaveText('Remote preferred fixture');
  await page.getByLabel('Model machine', { exact: true }).selectOption(localNode);
  await expect(page.locator('#model')).toHaveValue('local-default');
});
