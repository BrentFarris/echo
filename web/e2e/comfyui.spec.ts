import { test, expect } from '@playwright/test';
import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';

for (const kind of ['image', 'video', 'gif']) test(`ComfyUI ${kind} results survive reload outside Show work`, async ({ page }) => {
  const png = Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVQIHWP4z8DwHwAFgAI/ScLttAAAAABJRU5ErkJggg==', 'base64');
  const gif = Buffer.from('R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7', 'base64');
  const media = kind === 'image' ? png : kind === 'gif' ? gif : Buffer.from([0, 0, 0, 24, ...Buffer.from('ftypmp42'), 0, 0, 0, 0, ...Buffer.from('mp42isom')]);
  const filename = kind === 'image' ? 'result.png' : kind === 'gif' ? 'result.gif' : 'result.mp4';
  const workflow = JSON.stringify({ '1': { class_type: 'SaveMedia', inputs: { text: '{{PROMPT}}' } } });
  const remote = createServer(async (request, response) => {
    if (request.url === '/v1/chat/completions') {
      const chunks: Buffer[] = []; for await (const chunk of request) chunks.push(chunk);
      const body = JSON.parse(Buffer.concat(chunks).toString());
      const done = body.messages.some((m: any) => m.role === 'tool');
      const delta = done ? { content: 'Your generated media is ready.' } : { tool_calls: [{ index: 0, id: 'generate', type: 'function', function: { name: kind === 'image' ? 'comfyui_generate' : 'comfyui_generate_video', arguments: JSON.stringify({ prompt: 'A small test scene', workflowJSON: workflow }) } }] };
      response.setHeader('Content-Type', 'text/event-stream');
      response.end(`data: ${JSON.stringify({ choices: [{ delta, finish_reason: 'stop' }] })}\n\ndata: [DONE]\n\n`);
    } else if (request.url === '/prompt') response.end(JSON.stringify({ prompt_id: 'test' }));
    else if (request.url === '/history/test') response.end(JSON.stringify({ test: { outputs: { '1': { [kind === 'image' ? 'images' : 'videos']: [{ filename, subfolder: '', type: kind === 'gif' ? 'custom' : 'output' }] } } } }));
    else if (request.url?.startsWith('/view?')) response.end(media);
    else { response.statusCode = 404; response.end(); }
  });
  await new Promise<void>(resolve => remote.listen(0, '127.0.0.1', resolve));
  const base = `http://127.0.0.1:${(remote.address() as AddressInfo).port}`;
  let chatId = '', original: any;
  try {
    await page.goto('/'); await page.request.post('/api/v1/session', { data: {} });
    const state = await (await page.request.get('/api/v1/state')).json(); original = state.plugins.find((p: any) => p.id === 'comfyui');
    await page.request.patch('/api/v1/plugins/comfyui', { data: { enabled: true, config: { url: base, defaultCheckpoint: 'test.safetensors' } } });
    await page.request.post('/api/v1/endpoints', { data: { endpoint: { id: 'comfy-test', name: 'Comfy test model', url: base + '/v1', model: 'test', contextTokens: 32768 } } });
    const chat = await (await page.request.post('/api/v1/chats', { data: { title: `ComfyUI ${kind}` } })).json(); chatId = chat.id;
    const sent = await page.request.post(`/api/v1/chats/${chatId}/messages`, { data: { content: 'Generate media', generation: { endpointId: 'comfy-test', model: 'test', temperature: 0.6 } } });
    expect(sent.ok()).toBe(true);
    await page.goto('/#' + chatId); await page.reload();
    const result = page.locator('.generated-media');
    await expect(page.locator('.response-group')).toContainText('Your generated media is ready.');
    await expect(result).toBeVisible(); await expect(page.locator('.work-disclosure')).not.toHaveAttribute('open', '');
    await expect(result.getByRole('link', { name: `Download ${filename}` })).toBeVisible();
    if (kind === 'video') await expect(result.locator('video')).toHaveAttribute('controls', '');
    else {
      await expect(result.getByRole('img')).toBeVisible();
      await expect.poll(() => result.locator('img').evaluate((image: HTMLImageElement) => image.naturalWidth)).toBe(1);
      await result.getByRole('button', { name: `Preview ${filename}` }).click();
      await expect(page.locator('.attachment-viewer')).toBeVisible();
      await page.getByRole('button', { name: 'Close preview' }).click();
    }
    const link = await result.locator('a').getAttribute('href');
    expect(await (await page.request.get(link!)).body()).toEqual(media);
    await page.reload(); await expect(result).toBeVisible();
    await page.setViewportSize({ width: 390, height: 844 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
    await page.getByRole('button', { name: 'Toggle sidebar', exact: true }).click();
    await page.getByRole('button', { name: '⚙ Settings', exact: true }).click();
    await page.locator('.settings-nav').getByRole('button', { name: 'ComfyUI', exact: true }).click();
    await expect(page.getByRole('checkbox', { name: 'Enable ComfyUI' })).toBeChecked();
    await expect(page.locator('.settings-content input[name=url]')).toHaveValue(base);
    for (const name of ['defaultCheckpoint', 'txt2imgWorkflow', 'img2imgWorkflow', 'videoWorkflow']) await expect(page.locator(`.settings-content input[name=${name}]`)).toBeVisible();
    await page.locator('.settings-content input[name=txt2imgWorkflow]').fill('workflows/image.json');
    await page.getByRole('button', { name: 'Save settings', exact: true }).click();
    await expect.poll(async () => (await (await page.request.get('/api/v1/state')).json()).plugins.find((p: any) => p.id === 'comfyui').config.txt2imgWorkflow).toBe('workflows/image.json');
  } finally {
    if (chatId) await page.request.delete(`/api/v1/chats/${chatId}`);
    await page.request.delete('/api/v1/endpoints/comfy-test');
    if (original) await page.request.patch('/api/v1/plugins/comfyui', { data: { enabled: original.enabled, config: original.config } });
    remote.closeAllConnections(); await new Promise<void>(resolve => remote.close(() => resolve()));
  }
});
