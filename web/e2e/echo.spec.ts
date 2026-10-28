import { test, expect, type Page, type WebSocketRoute } from '@playwright/test';
import { mkdtemp, writeFile, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
async function setup(page: Page) {
  await page.goto('/');
  await expect(page.getByRole('button', { name: '＋ New chat', exact: true })).toBeVisible();
  await page.request.post('/api/v1/session', { data: {} });
  const response = await page.request.get('/api/v1/state'); const state = await response.json();
  for (const c of state.chats) await page.request.delete('/api/v1/chats/' + c.id);
  for (const b of state.bots) if (b.id !== 'echo') await page.request.delete('/api/v1/bots/' + b.id);
  for (const s of state.schedules) await page.request.delete('/api/v1/schedules/' + s.id);
  for (const p of state.plugins) if (!p.builtin) await page.request.patch('/api/v1/plugins/' + p.id, { data: { enabled: false, config: p.config } });
  await page.request.post('/api/v1/endpoints', { data: { endpoint: { id: 'test', name: 'Test local model', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', cloud: false, contextTokens: 32768, compact: true } } });
  await page.reload(); await page.getByRole('button', { name: '＋ New chat', exact: true }).click();
}
async function send(page: Page, value: string) { await page.getByRole('textbox', { name: 'Message', exact: true }).fill(value); await page.getByRole('button', { name: 'Send message', exact: true }).click(); }

test('chat releases sticky scrolling immediately and resumes only at the tail', async ({ page }) => {
  let socket: WebSocketRoute | undefined;
  await page.routeWebSocket('**/api/v1/events', ws => { socket = ws; ws.connectToServer(); });
  await setup(page);
  await expect(page.getByRole('textbox', { name: 'Message', exact: true })).toBeEnabled();
  await page.reload();
  await expect(page.locator('#connection')).toHaveText('');
  await expect.poll(() => !!socket).toBe(true);
  const chatId = new URL(page.url()).hash.slice(1);
  const log = page.locator('.chat-log');
  const bottomGap = () => log.evaluate(el => el.scrollHeight - el.clientHeight - el.scrollTop);
  const position = () => log.evaluate(el => el.scrollTop);
  let counter = 0;
  const message = (id: string, content: string) => socket!.send(JSON.stringify({ type: 'message', chatId, payload: {
    id, chatId, role: 'assistant', author: 'Echo', content, status: 'streaming', createdAt: new Date().toISOString(),
  } }));
  const grow = async (repaint = false) => {
    const marker = `Streaming paragraph ${++counter}`;
    if (repaint) message(`scroll-${counter}`, marker);
    else socket!.send(JSON.stringify({ type: 'delta', chatId, payload: { id: 'scroll-stream', content: `\n\n${marker}` } }));
    await expect(log).toContainText(marker);
    // Allow mutation/resize observers and the scheduled follow to run.
    await page.evaluate(() => new Promise<void>(resolve => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
  };
  message('scroll-stream', Array.from({ length: 80 }, (_, i) => `History paragraph ${i}`).join('\n\n'));
  await expect(log).toContainText('History paragraph 79');
  await expect.poll(bottomGap).toBeLessThanOrEqual(2);
  await grow();
  await expect.poll(bottomGap).toBeLessThanOrEqual(2);

  // A tiny wheel movement inside the former 120px snap zone must release it.
  await log.hover();
  const tail = await position();
  await page.mouse.wheel(0, -40);
  await expect.poll(position).toBeLessThan(tail);
  const reading = await position();
  await grow(); await grow(true);
  expect(Math.abs(await position() - reading)).toBeLessThanOrEqual(2);
  await page.mouse.wheel(0, 10000);
  await expect.poll(bottomGap).toBeLessThanOrEqual(2);
  await grow();
  await expect.poll(bottomGap).toBeLessThanOrEqual(2);

  for (const up of ['ArrowUp', 'PageUp', 'Home']) {
    await log.focus();
    const before = await position();
    await page.keyboard.press(up);
    // Deliver a delta immediately, while native key scrolling may still animate.
    await grow();
    await expect.poll(position).toBeLessThan(before);
    await expect.poll(async () => {
      const first = await position();
      await page.waitForTimeout(100);
      return Math.abs(await position() - first);
    }).toBeLessThanOrEqual(1);
    const stopped = await position();
    await grow(true);
    expect(Math.abs(await position() - stopped)).toBeLessThanOrEqual(2);
    await page.keyboard.press('End');
    await expect.poll(bottomGap).toBeLessThanOrEqual(2);
    await grow();
    await expect.poll(bottomGap).toBeLessThanOrEqual(2);
  }
});

test('streaming thinking keeps clicks and its reading position intact', async ({ page }) => {
  let socket: WebSocketRoute | undefined;
  await page.routeWebSocket('**/api/v1/events', ws => { socket = ws; ws.connectToServer(); });
  await setup(page);
  await page.reload();
  await expect(page.locator('#connection')).toHaveText('');
  await expect.poll(() => !!socket).toBe(true);
  const chatId = new URL(page.url()).hash.slice(1), id = 'thinking-stream';
  const emit = (type: string, payload: object) => socket!.send(JSON.stringify({ type, chatId, payload: { id, ...payload } }));
  emit('message', { chatId, role: 'assistant', author: 'Echo', content: '', reasoning: '', status: 'running', createdAt: new Date().toISOString() });
  await expect(page.locator('[id="message-thinking-stream"]')).toBeVisible();
  emit('delta', { reasoning: Array.from({ length: 100 }, (_, i) => `Thinking line ${i}`).join('\n') });
  const thinking = page.locator('.reasoning'), summary = thinking.locator('summary'), text = thinking.locator('pre');
  await expect(summary).toBeVisible();

  // A token arriving between pointer down and up must not replace the click target.
  await summary.hover();
  await page.mouse.down();
  emit('delta', { reasoning: '\nThinking during click' });
  await expect(text).toContainText('Thinking during click');
  await page.mouse.up();
  await expect(thinking).toHaveAttribute('open', '');
  await expect(summary).toBeFocused();
  const original = await text.elementHandle();
  await text.hover();
  await page.mouse.wheel(0, 200);
  await expect.poll(() => text.evaluate(el => el.scrollTop)).toBeGreaterThan(0);
  const top = await text.evaluate(el => el.scrollTop);
  for (let i = 0; i < 5; i++) {
    emit('delta', { reasoning: `\nThinking update ${i}`, content: i === 0 ? 'The answer starts here.' : '' });
    await expect(text).toContainText(`Thinking update ${i}`);
    expect(await text.evaluate((el, node) => el === node, original)).toBe(true);
    expect(await text.evaluate(el => el.scrollTop)).toBe(top);
    await expect(thinking).toHaveAttribute('open', '');
  }
  await expect(page.locator('.message-content')).toHaveText('The answer starts here.');
  await summary.press('Enter');
  emit('delta', { reasoning: '\nThinking while closed' });
  await expect(text).toContainText('Thinking while closed');
  await expect(thinking).not.toHaveAttribute('open', '');
  await summary.press('Enter');
  await expect(thinking).toHaveAttribute('open', '');
});

test('cloud connections can be added and selected without enable switches', async ({ page }) => {
  await setup(page);
  let endpointId = '';
  try {
    await page.getByRole('button', { name: '⚙ Settings', exact: true }).click();
    for (const button of ['Edit', '＋ Add bot']) {
      await page.getByRole('button', { name: button, exact: true }).click();
      const editor = page.getByRole('dialog').last();
      await expect(editor.getByLabel('Handle', { exact: true })).toBeVisible();
      await expect(editor).not.toContainText('Cloud Models');
      await expect(editor.getByLabel('Git', { exact: true })).toBeVisible();
      await editor.getByRole('button', { name: 'Close', exact: true }).click();
    }
    await page.locator('.settings-nav').getByRole('button', { name: 'Cloud Models', exact: true }).click();
    await expect(page.getByRole('checkbox', { name: 'Enable Cloud Models' })).toHaveCount(0);
    await page.getByRole('button', { name: '＋ Add connection', exact: true }).click();
    await page.getByLabel('Name', { exact: true }).fill('Test cloud model');
    await page.getByLabel('Model', { exact: true }).fill('echo-test');
    await page.getByLabel('Endpoint URL', { exact: true }).fill('http://127.0.0.1:3743/v1');
    await page.getByRole('button', { name: 'Save connection', exact: true }).click();
    await expect(page.locator('.settings-content')).toContainText('Test cloud model');
    const state = await (await page.request.get('/api/v1/state')).json();
    endpointId = state.endpoints.find((e: any) => e.name === 'Test cloud model').id;
    expect(state.plugins.some((p: any) => p.id === 'cloud-models')).toBe(false);
    const discovery = await page.request.get(`/api/v1/endpoints/${endpointId}/models`);
    expect(discovery.ok()).toBe(true);
    expect((await discovery.json()).data.some((m: any) => m.id === 'echo-test')).toBe(true);
    await page.reload();
    await page.locator('.model-settings summary').click();
    await expect(page.getByRole('option', { name: 'Test local model — echo-test', exact: true })).toBeVisible();
    await page.getByRole('option', { name: 'Test cloud model — echo-test', exact: true }).click();
    await expect(page.locator('#model-name')).toHaveText('Test cloud model');
    await page.locator('.model-settings summary').press('Escape');
    await send(page, 'Hello from the cloud model');
    await expect(page.locator('.message.assistant')).toContainText('Completed the requested work.');
    await page.reload();
    await expect(page.locator('#model-name')).toHaveText('Test cloud model');
  } finally {
    if (endpointId) await page.request.delete(`/api/v1/endpoints/${endpointId}`);
  }
});

test('finished work folds behind the final reply and stays compact on mobile', async ({ page }) => {
  await setup(page); await page.emulateMedia({ colorScheme: 'dark' });
  await send(page, 'Prepare the work disclosure report');
  const response = page.locator('.response-group'), work = response.locator('.work-disclosure');
  await expect(response.getByText('I will inspect the configuration before preparing the report.', { exact: true })).toBeVisible();
  await expect(work).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Stop response' })).toBeVisible();
  await expect(work).toBeVisible(); await expect(work).not.toHaveAttribute('open', '');
  await expect(response.locator(':scope > .message.assistant')).toContainText('The workspace report is ready.');
  await expect(response.getByText('I will inspect the configuration before preparing the report.', { exact: true })).toBeHidden();
  await expect(response.locator('.reasoning')).toHaveCount(2);
  await expect(response.locator('.work-disclosure .message.tool')).toHaveCount(1);
  await expect(response.locator('.badge')).toHaveCount(0);
  const currentChat = new URL(page.url()).hash.slice(1);
  const original = await (await page.request.get(`/api/v1/chats/${currentChat}/messages`)).json();
  expect(original.filter((m: any) => m.role === 'tool')).toHaveLength(1);
  expect(original.filter((m: any) => m.reasoning)).toHaveLength(2);
  await work.locator(':scope > summary').focus(); await page.keyboard.press('Enter');
  await expect(work).toHaveAttribute('open', '');
  await expect(response.getByText('I will inspect the configuration before preparing the report.', { exact: true })).toBeVisible();
  await send(page, 'Hello again');
  await expect(page.locator('.response-group').last()).toContainText('Completed the requested work.');
  await expect(page.locator('.work-disclosure').first()).toHaveAttribute('open', '');
  await page.reload(); await expect(page.locator('.work-disclosure')).not.toHaveAttribute('open', '');
  await expect(page.locator('.response-group').first().locator(':scope > .message.assistant')).toContainText('The workspace report is ready.');
  await page.setViewportSize({ width: 390, height: 844 });
  expect((await page.locator('.chat-log > .message.user').first().boundingBox())!.height).toBeLessThan(65);
  expect((await page.locator('.response-group').first().boundingBox())!.height).toBeLessThan(210);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
  await page.screenshot({ path: 'test-results/echo2-compact-chat-mobile.png', fullPage: true });
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.screenshot({ path: 'test-results/echo2-compact-chat-desktop.png', fullPage: true });
});

test('stopping before a final answer keeps the partial work visible', async ({ page }) => {
  await setup(page); await send(page, 'Prepare another work disclosure report');
  await expect(page.locator('.response-group > .message.assistant').last()).toContainText('Done.');
  await page.getByRole('button', { name: 'Stop response' }).click();
  await expect(page.getByRole('button', { name: 'Stop response' })).toBeHidden();
  await expect(page.locator('.work-disclosure')).toHaveCount(0);
  await expect(page.getByText('I will inspect the configuration before preparing the report.', { exact: true })).toBeVisible();
  await page.reload(); await expect(page.locator('.work-disclosure')).toHaveCount(0);
  await expect(page.getByText('I will inspect the configuration before preparing the report.', { exact: true })).toBeVisible();
});

test('composer pastes images, attaches files, preserves drafts and renders persisted media', async ({ page, context }) => {
  await setup(page); await context.grantPermissions(['clipboard-read', 'clipboard-write']);
  const input = page.getByRole('textbox', { name: 'Message', exact: true });
  await input.fill('Inspect this attachment');
  await page.evaluate(async () => {
    const canvas = document.createElement('canvas'); canvas.width = 8; canvas.height = 8;
    const paint = canvas.getContext('2d')!; paint.fillStyle = '#60a5fa'; paint.fillRect(0, 0, 8, 8);
    const blob = await new Promise<Blob>(resolve => canvas.toBlob(b => resolve(b!)));
    await navigator.clipboard.write([new ClipboardItem({ 'image/png': blob })]);
  });
  await input.press('Control+V');
  await expect(page.locator('.attachment-drafts .attachment-card')).toHaveCount(1);
  await expect(page.locator('.attachment-drafts img')).toBeVisible();
  await expect(input).toHaveText('Inspect this attachment');
  const oldChat = new URL(page.url()).hash.slice(1);
  await page.getByRole('button', { name: '＋ New chat', exact: true }).click();
  await expect(page.locator('.attachment-drafts')).toBeHidden();
  await page.locator(`[data-chat="${oldChat}"]`).click();
  await expect(page.locator('.attachment-drafts .attachment-card')).toHaveCount(1);
  await expect(input).toHaveText('Inspect this attachment');
  await page.locator('.attachment-drafts .attachment-preview').click();
  await expect(page.locator('.attachment-viewer img')).toBeVisible(); await page.getByRole('button', { name: 'Close preview' }).click();
  await page.getByLabel('Attach file', { exact: true }).click();
  await expect(page.getByRole('menuitem', { name: 'Video', exact: true })).toBeVisible();
  const chooser = page.waitForEvent('filechooser'); await page.getByRole('menuitem', { name: 'File', exact: true }).click();
  await (await chooser).setFiles({ name: 'notes.txt', mimeType: 'text/plain', buffer: Buffer.from('Important attached notes') });
  await expect(page.locator('.attachment-drafts .attachment-card')).toHaveCount(2);
  await page.getByRole('button', { name: 'Remove notes.txt' }).click(); await expect(page.locator('.attachment-drafts .attachment-card')).toHaveCount(1);
  await input.fill(''); await page.getByRole('button', { name: 'Send message', exact: true }).click();
  await expect(page.locator('.message.assistant')).toContainText('Received 1 attachment parts.');
  await expect(page.locator('.message.user .attachment-card')).toHaveCount(1);
  const imageURL = await page.locator('.message.user img').getAttribute('src');
  await page.reload(); await expect(page.locator('.message.user img')).toBeVisible();
  await expect.poll(() => page.locator('.message.user img').evaluate((img: HTMLImageElement) => img.naturalWidth)).toBe(8);
  await input.fill('This draft belongs only to the deleted chat');
  await input.press('Control+V'); await expect(page.locator('.attachment-drafts .attachment-card')).toHaveCount(1);
  await page.request.delete('/api/v1/chats/' + oldChat);
  expect((await page.request.get(imageURL!)).status()).toBe(404);
  await expect(input).toHaveText(''); await expect(page.locator('.attachment-drafts')).toBeHidden();
});

test('composer grows to half height, opens upward menus and uses one send/stop icon', async ({ page }) => {
  await setup(page); await page.setViewportSize({ width: 1440, height: 900 }); await page.emulateMedia({ colorScheme: 'dark' });
  const input = page.getByRole('textbox', { name: 'Message', exact: true });
  const original = (await input.boundingBox())!.height;
  await input.fill(Array.from({ length: 9 }, (_, i) => `Line ${i}`).join('\n'));
  expect((await input.boundingBox())!.height).toBeGreaterThan(original + 100);
  await input.fill('Long line\n'.repeat(120));
  expect((await page.locator('.composer').boundingBox())!.height).toBeLessThanOrEqual(451);
  expect(await input.evaluate(el => el.scrollHeight > el.clientHeight)).toBe(true);
  await input.fill(''); expect((await input.boundingBox())!.height).toBeLessThan(60);
  await page.locator('.model-settings summary').click();
  await expect(page.getByRole('option', { name: 'Test local model — echo-test' })).toHaveAttribute('aria-selected', 'true');
  const popup = (await page.locator('.model-popup').boundingBox())!, trigger = (await page.locator('.model-settings summary').boundingBox())!;
  expect(popup.y + popup.height).toBeLessThan(trigger.y);
  await page.screenshot({ path: 'test-results/echo2-composer-model-menu.png' });
  await page.locator('.model-settings summary').press('Escape'); await expect(page.locator('.model-popup')).toBeHidden();
  await page.getByLabel('Attach file', { exact: true }).click();
  await page.getByLabel('Attach file', { exact: true }).press('ArrowDown'); await expect(page.getByRole('menuitem', { name: 'Image' })).toBeFocused();
  await page.screenshot({ path: 'test-results/echo2-composer-attachment-menu.png' });
  await page.getByRole('menuitem', { name: 'Image' }).press('Escape'); await expect(page.locator('.attachment-popup')).toBeHidden();
  await send(page, 'slow stream'); await expect(page.getByRole('button', { name: 'Stop response' })).toBeVisible();
  await expect(page.locator('.composer .send-button')).toHaveCount(1); await expect(page.locator('.send-button svg')).toHaveCount(1);
  await expect(page.getByRole('button', { name: 'Send message', exact: true })).toHaveCount(0);
  await page.getByRole('button', { name: 'Stop response' }).click(); await expect(page.getByRole('button', { name: 'Send message', exact: true })).toBeVisible();
  await page.setViewportSize({ width: 390, height: 844 }); await input.fill('More lines\n'.repeat(90));
  expect((await page.locator('.composer').boundingBox())!.height).toBeLessThanOrEqual(423);
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
});

test('advanced endpoint settings persist and seed the chat controls', async ({ page }) => {
  await setup(page); await page.setViewportSize({ width: 1440, height: 1050 }); await page.emulateMedia({ colorScheme: 'dark' });
  await page.getByRole('button', { name: '⚙ Settings', exact: true }).click();
  await page.locator('.settings-nav').getByRole('button', { name: 'Local Models', exact: true }).click();
  await page.locator('[data-model="test"]').click();
  const form = page.locator('.endpoint-editor');
  const fields: Record<string, string> = { temperature: '.7', topK: '20', topP: '.95', minP: '0', contextTokens: '1048576', maxTokens: '32168', frequencyPenalty: '.2', presencePenalty: '.4', repetitionPenalty: '1.1', requestTimeoutSeconds: '600', streamIdleTimeoutSeconds: '-1', thinkingTokenBudget: '4096' };
  for (const [name, value] of Object.entries(fields)) await form.locator(`[name="${name}"]`).fill(value);
  await form.getByRole('combobox', { name: 'Reasoning Effort', exact: true }).selectOption('high');
  await expect(form.getByLabel('Thinking Token Budget', { exact: true })).toBeDisabled();
  await form.locator('[name=compressionThreshold]').fill('65');
  await expect(form.locator('.compression-setting output')).toHaveText('65%');
  await page.screenshot({ path: 'test-results/echo2-model-settings.png' });
  await form.getByRole('button', { name: 'Save connection', exact: true }).click();
  await expect(page.locator('.endpoint-editor')).toHaveCount(0);
  const state = await (await page.request.get('/api/v1/state')).json();
  expect(state.endpoints.find((e: any) => e.id === 'test')).toMatchObject({ temperature: .7, topK: 20, topP: .95, minP: 0, contextTokens: 1048576, maxTokens: 32168, frequencyPenalty: .2, presencePenalty: .4, repetitionPenalty: 1.1, reasoningEffort: 'high', thinkingTokenBudget: 4096, compressionThreshold: 65, streamIdleTimeoutSeconds: -1 });
  await page.locator('#settings').getByRole('button', { name: 'Close', exact: true }).click();
  await page.locator('.model-settings summary').click();
  await expect(page.locator('#temperature')).toHaveValue('0.7'); await expect(page.locator('#thinking-label')).toHaveText('Default · High');
  await page.locator('#temperature').fill('0.25'); await page.locator('#thinking').fill('3');
  const selectedChat = new URL(page.url()).hash.slice(1);
  await expect.poll(async () => (await (await page.request.get('/api/v1/state')).json()).chats.find((c: any) => c.id === selectedChat).generation.temperature).toBe(.25);
  await page.reload(); await page.locator('.model-settings summary').click();
  await expect(page.locator('#temperature')).toHaveValue('0.25'); await expect(page.locator('#thinking')).toHaveValue('3');
  await page.reload(); await page.getByRole('button', { name: '＋ New chat', exact: true }).click(); await page.locator('.model-settings summary').click(); await expect(page.locator('#temperature')).toHaveValue('0.7');
});
test('built-in source-control settings and per-bot toggles persist', async ({ page }) => {
  await setup(page); await page.getByRole('button', { name: '⚙ Settings', exact: true }).click();
  for (const name of ['Git', 'Fossil', 'P4 (Perforce)']) {
    await page.locator('.settings-nav').getByRole('button', { name, exact: true }).click();
    await expect(page.getByLabel('CLI executable path (blank discovers it on PATH)')).toBeVisible();
    await expect(page.getByLabel('Grant Internet access to this plugin\'s workspace-bound commands')).toBeVisible();
  }
  await page.getByLabel('P4 server, for example ssl:perforce.example.com:1666').fill('ssl:example.invalid:1666');
  await page.getByLabel('P4 user', { exact: true }).fill('artist'); await page.getByLabel('P4 client workspace', { exact: true }).fill('game');
  await page.getByRole('button', { name: 'Save settings', exact: true }).click();
  await expect(page.getByText('Settings saved', { exact: true })).toBeVisible();
  await expect(page.getByLabel('P4 ticket or password', { exact: true })).toHaveAttribute('type', 'password');
  const state = await (await page.request.get('/api/v1/state')).json();
  expect(state.plugins.find((p: any) => p.id === 'p4').config).toMatchObject({ port: 'ssl:example.invalid:1666', user: 'artist', client: 'game' });
  await page.locator('.settings-nav').getByRole('button', { name: 'Bots', exact: true }).click(); await page.getByRole('button', { name: '＋ Add bot', exact: true }).click();
  await page.getByLabel('Handle', { exact: true }).fill('scm');
  for (const name of ['Git', 'Fossil', 'P4 (Perforce)']) await expect(page.getByLabel(name, { exact: true })).toBeChecked();
  await page.getByLabel('Git', { exact: true }).uncheck(); await page.getByRole('button', { name: 'Save bot', exact: true }).click();
  const updated = await (await page.request.get('/api/v1/state')).json(); expect(updated.bots.find((b: any) => b.handle === 'scm').plugins.git).toBe(false);
});
test('chat routes through tools, renders markdown, creates bots, and groups every chat', async ({ page }) => {
  await setup(page);
  await send(page, 'List my bots');
  await expect(page.getByRole('heading', { name: 'Available bots' })).toBeVisible();
  await send(page, 'Create a bot named kaiju');
  await expect(page.locator('.message.assistant').filter({ hasText: 'Created the bot.' })).toBeVisible();
  await page.getByRole('button', { name: '＋ New chat', exact: true }).click();
  await send(page, '@kaiju summarize your workspace');
  await expect(page.locator('.group-label').filter({ hasText: /^kaiju$/ })).toBeVisible();
  await expect(page.locator('.message.assistant').filter({ hasText: 'Completed the requested work.' })).toBeVisible();
  await expect(page.getByText('show more', { exact: false })).toHaveCount(0);
  await page.screenshot({ path: 'test-results/echo2-chat.png', fullPage: true });
});

test('installed renderer stays isolated, sanitizes output, and respects bot toggles', async ({ page }) => {
  await setup(page); expect((await page.request.post('/__fixtures/renderer')).ok()).toBe(true); await page.reload();
  try {
    await send(page, 'List my bots');
    await expect(page.locator('.message.assistant').last().getByRole('heading', { name: 'Isolated renderer' })).toBeVisible();
    await expect(page.locator('.message.user .message-content')).toHaveText('List my bots');
    expect(await page.evaluate(() => (window as any).compromised || document.body.dataset.compromised)).toBeUndefined();
    await expect(page.locator('.message-content img, .message-content svg, .message-content a[href^="javascript:"]')).toHaveCount(0);
    await page.request.post('/api/v1/bots', { data: { handle: 'plain', name: 'Plain', roots: [], plugins: { 'test-renderer': false, markdown: false } } });
    await page.getByRole('button', { name: '＋ New chat', exact: true }).click(); await send(page, '@plain say hello');
    await expect(page.locator('.message.assistant').last()).toContainText('Completed the requested work.');
    await expect(page.locator('.message.assistant h2')).toHaveCount(0);
  } finally { await page.request.patch('/api/v1/plugins/test-renderer', { data: { enabled: false, config: {} } }); }
});

test('keyboard references edit an authorized file and deleting a bot keeps author labels', async ({ page }) => {
  const root = await mkdtemp(join(tmpdir(), 'echo2-browser-'));
  try {
    await writeFile(join(root, 'map_editor.go'), 'package game\n');
    await setup(page);
    await page.request.post('/api/v1/bots', { data: { handle: 'kaiju', name: 'Kaiju', prompt: '', roots: [root], plugins: {} } }); await page.reload();
    const composer = page.getByRole('textbox', { name: 'Message', exact: true });
    await composer.fill('@kai'); await expect(page.getByRole('option').filter({ hasText: '@kaiju' })).toBeVisible(); await composer.press('Tab');
    await composer.press('End'); await composer.pressSequentially('edit #map');
    await expect(page.getByRole('option').filter({ hasText: 'map_editor.go' })).toBeVisible(); await composer.press('Tab'); await composer.pressSequentially('to add a transform gizmo');
    await page.getByRole('button', { name: 'Send message', exact: true }).click();
    await expect(page.locator('.message.assistant').filter({ hasText: 'Added the transform gizmo.' })).toBeVisible();
    expect(await readFile(join(root, 'map_editor.go'), 'utf8')).toContain('transform gizmo');
    await send(page, 'Delete the kaiju bot'); await expect(page.getByText('Deleted kaiju.', { exact: true })).toBeVisible();
    expect((await (await page.request.get('/api/v1/state')).json()).bots).toHaveLength(1);
    await expect(page.locator('.group-label').filter({ hasText: /^Kaiju$/ })).toBeVisible();
  } finally { await rm(root, { recursive: true, force: true }); }
});

test('completed replies list net file changes outside Show work and reveal from the keyboard', async ({ page }) => {
  const root = await mkdtemp(join(tmpdir(), 'echo-changes-'));
  try {
    await writeFile(join(root, 'edited.txt'), 'before'); await writeFile(join(root, 'deleted.txt'), 'before');
    await setup(page);
    await page.request.post('/api/v1/bots', { data: { handle: 'kaiju', name: 'Kaiju', prompt: '', roots: [root], plugins: {} } });
    await page.reload();
    await send(page, `@kaiju prepare the file change report.\nChange report folder: ${root}`);
    const footer = page.getByRole('region', { name: 'Files changed' });
    await expect(footer).toBeVisible(); await expect(footer.locator('button')).toHaveCount(3);
    await expect(footer).toContainText('3 files');
    for (const label of ['Created', 'Edited', 'Deleted']) await expect(footer.getByText(label, { exact: true })).toBeVisible();
    await expect(page.locator('.work-disclosure')).not.toHaveAttribute('open', '');
    await expect(page.locator('.work-disclosure .files-changed')).toHaveCount(0);
    const response = footer.locator('..');
    expect(await response.locator('.message.assistant').last().evaluate(el => el.compareDocumentPosition(el.parentElement!.querySelector('.files-changed')!) & Node.DOCUMENT_POSITION_FOLLOWING)).toBeTruthy();
    const id = new URL(page.url()).hash.slice(1);
    const messages = await (await page.request.get(`/api/v1/chats/${id}/messages`)).json();
    expect(messages.flatMap((m: any) => m.fileChanges || [])).toHaveLength(4);
    await page.reload(); await expect(footer.locator('button')).toHaveCount(3);
    const requests: any[] = [];
    await page.route('**/api/v1/chats/*/files/reveal', async route => { requests.push(route.request().postDataJSON()); await route.fulfill({ json: { path: root, select: false } }); });
    const deleted = footer.getByRole('button', { name: /containing folder.*deleted.txt/ });
    await deleted.focus(); await page.keyboard.press('Enter');
    await expect.poll(() => requests.length).toBe(1);
    expect(requests[0].path.toLowerCase()).toBe(join(root, 'deleted.txt').toLowerCase());
    expect(messages.some((m: any) => m.id === requests[0].messageId && m.role === 'tool')).toBeTruthy();
    await expect(page.getByRole('status')).toContainText('Opened the containing folder');
    await page.route('**/api/v1/chats/*/files/reveal', route => route.fulfill({ status: 403, json: { error: 'Open Echo on this Windows computer to reveal files in File Explorer.' } }));
    await footer.getByRole('button', { name: /Show in File Explorer.*created.txt/ }).click();
    await expect(page.getByRole('alert')).toContainText('Open Echo on this Windows computer');
    await page.reload(); await expect(footer.locator('button')).toHaveCount(3);
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.setViewportSize({ width: 390, height: 844 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
    await expect(footer.locator('button')).toHaveCount(3);
    await page.screenshot({ path: 'test-results/echo-files-changed-mobile.png', fullPage: true });
    await page.setViewportSize({ width: 1440, height: 900 });
    await page.screenshot({ path: 'test-results/echo-files-changed-desktop.png', fullPage: true });
  } finally { await rm(root, { recursive: true, force: true }); }
});

test('stream reconnect, stop, and deletion leave no running dispatch or transcript', async ({ page }) => {
  await setup(page); await send(page, 'slow stream');
  await expect(page.locator('.message.assistant')).toContainText('A stream'); await page.reload();
  await expect(page.locator('.message.assistant')).toContainText('A stream that survives a browser reload.');
  await send(page, 'slow stream again'); await expect(page.getByRole('button', { name: 'Stop response' })).toBeVisible(); await page.getByRole('button', { name: 'Stop response' }).click();
  await expect(page.getByRole('button', { name: 'Stop response' })).toBeHidden();
  const state = await (await page.request.get('/api/v1/state')).json(); await page.request.delete('/api/v1/chats/' + state.chats[0].id);
  expect(await (await page.request.get('/api/v1/chats/' + state.chats[0].id + '/messages')).json()).toEqual([]);
});

test('missed jobs select exactly one, skip others, and support unlimited batches', async ({ page }) => {
  await setup(page); await page.request.post('/__fixtures/missed'); await page.reload();
  await page.getByRole('button', { name: 'Missed jobs (3)' }).click();
  await expect(page.getByRole('spinbutton', { name: 'Concurrent jobs' })).toHaveValue('1');
  await page.locator('.missed-job input').first().check(); await page.getByRole('button', { name: 'Run selected' }).click();
  await expect.poll(async () => (await (await page.request.get('/api/v1/state')).json()).occurrences.filter((o: any) => o.status === 'completed').length).toBeGreaterThanOrEqual(1);
  await page.request.post('/__fixtures/missed'); await page.reload(); await page.getByRole('button', { name: 'Missed jobs (3)' }).click();
  for (const box of await page.locator('.missed-job input').all()) await box.check(); await page.getByRole('spinbutton', { name: 'Concurrent jobs' }).fill('0'); await page.getByRole('button', { name: 'Run selected' }).click();
  await expect.poll(async () => (await (await page.request.get('/api/v1/state')).json()).occurrences.filter((o: any) => o.status === 'missed').length).toBe(0);
  await page.request.post('/__fixtures/missed'); await page.reload(); await page.getByRole('button', { name: 'Missed jobs (3)' }).click(); await page.getByRole('button', { name: 'Skip all' }).click();
  await expect(page.getByRole('button', { name: /Missed jobs/ })).toBeHidden();
});

test('hostile markdown is sanitized and every chat remains in the sidebar', async ({ page }) => {
  await setup(page); await send(page, 'hostile markdown'); await expect(page.getByRole('heading', { name: 'Safe heading' })).toBeVisible();
  expect(await page.evaluate(() => (window as any).compromised)).toBeUndefined(); await expect(page.locator('.message-content img, .message-content script, .message-content a[href^="javascript:"]')).toHaveCount(0);
  for (let i = 0; i < 120; i++) await page.request.post('/api/v1/chats', { data: {} }); await page.reload();
  await expect(page.locator('.chat-link')).toHaveCount(121); await page.locator('.chat-link').last().scrollIntoViewIfNeeded(); await expect(page.locator('.chat-link').last()).toBeVisible();
  await page.emulateMedia({ colorScheme: 'dark' }); expect(await page.locator('html').evaluate(el => getComputedStyle(el).getPropertyValue('--bg'))).toBe('#121214');
});
test('explicit handoff completes without a reply loop', async ({ page }) => {
  await setup(page);
  for (const handle of ['kaiju', 'backend']) await page.request.post('/api/v1/bots', { data: { id: '', handle, name: handle, prompt: 'Complete assigned work.', roots: [], plugins: {}, revision: 0 } });
  await page.reload();
  await send(page, '@kaiju work with @backend to wire up the shield REST API');
  const kaijuTab = page.getByRole('tab', { name: /^kaiju/ }), backendTab = page.getByRole('tab', { name: /^backend/ });
  await expect(page.getByRole('tablist', { name: 'Bot threads' })).toBeVisible();
  await expect(kaijuTab).toHaveAttribute('aria-selected', 'true');
  await expect(page.locator('.message.assistant').filter({ hasText: 'Wired up the shield REST API using the backend handoff.' })).toBeVisible({ timeout: 30_000 });
  await expect(page.getByRole('button', { name: 'Stop response' })).toBeHidden();
  const state = await (await page.request.get('/api/v1/state')).json();
  expect(state.runs.every((r: any) => r.status === 'completed')).toBeTruthy();
  expect(state.chats[0].bots && Object.keys(state.chats[0].bots).length).toBe(2);
  await expect(page.locator('.response-group')).toHaveCount(1);
  await expect(page.locator('.response-group[aria-label="kaiju response"] > .message.assistant')).toContainText('Wired up the shield REST API');
  await backendTab.click();
  await expect(page.locator('.response-group')).toHaveCount(1);
  await expect(page.locator('.response-group[aria-label="kaiju response"]')).toHaveCount(0);
  await expect(page.locator('.response-group[aria-label="backend response"] > .message.assistant')).toContainText('The backend shield endpoint is ready.');
  for (const disclosure of await page.locator('.work-disclosure').all()) await expect(disclosure).not.toHaveAttribute('open', '');
  await page.locator('.work-disclosure > summary').click();
  await backendTab.press('ArrowLeft');
  await expect(kaijuTab).toBeFocused();
  await expect(kaijuTab).toHaveAttribute('aria-selected', 'true');
  await kaijuTab.press('End');
  await expect(backendTab).toBeFocused();
  await expect(page.locator('.work-disclosure')).toHaveAttribute('open', '');
  await page.reload();
  await expect(backendTab).toHaveAttribute('aria-selected', 'true');
  await expect(page.getByRole('tabpanel', { name: 'backend', exact: true })).toBeVisible();
  await expect(page.locator('.response-group')).toHaveCount(1);
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(kaijuTab).toBeVisible();
  await kaijuTab.click();
  await expect(page.locator('.response-group')).toContainText('Wired up the shield REST API');
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth)).toBe(true);
  await page.screenshot({ path: 'test-results/bot-threads-mobile.png', fullPage: true });
});
test('generated calculator runs in an isolated draggable view', async ({ page }) => {
  await setup(page); await send(page, 'Create a draggable calculator plugin');
  const window = page.locator('.floating-plugin'); await expect(window).toBeVisible();
  const calculator = page.frameLocator('iframe[title="Calculator"]');
  await calculator.getByRole('button', { name: 'Add', exact: true }).click(); await expect(calculator.locator('#result')).toHaveText('5');
  await calculator.locator('#a').fill('7'); await calculator.getByRole('button', { name: 'Multiply' }).click(); await expect(calculator.locator('#result')).toHaveText('21');
  const before = await window.boundingBox(); const header = window.locator('header'); const h = await header.boundingBox();
  await page.mouse.move(h!.x + 30, h!.y + 15); await page.mouse.down(); await page.mouse.move(h!.x + 110, h!.y + 65); await page.mouse.up();
  expect((await window.boundingBox())!.x).toBeGreaterThan(before!.x + 50);
  const frame = page.frames().find(f => f !== page.mainFrame() && f.url() === 'about:srcdoc')!;
  expect(await frame.evaluate(() => { try { return !!parent.document.body; } catch { return false; } })).toBe(false);
  await page.screenshot({ path: 'test-results/echo2-calculator.png', fullPage: true });
});
test('schedules survive chat deletion and mobile layout remains usable', async ({ page }) => {
  await setup(page); await page.request.post('/api/v1/bots', { data: { handle: 'kaiju', name: 'kaiju', prompt: '', roots: [], plugins: {} } }); await page.reload();
  await send(page, 'Every day at 3pm have @kaiju create a test report');
  await expect(page.locator('.message.assistant').filter({ hasText: 'Scheduled the report.' })).toBeVisible();
  const state = await (await page.request.get('/api/v1/state')).json(); expect(state.schedules).toHaveLength(1);
  await page.request.delete('/api/v1/chats/' + state.chats[0].id);
  expect((await (await page.request.get('/api/v1/state')).json()).schedules).toHaveLength(1);
  await page.setViewportSize({ width: 390, height: 844 }); await page.reload();
  await expect(page.getByRole('textbox', { name: 'Message', exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
});
