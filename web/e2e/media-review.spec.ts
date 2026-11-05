import { test, expect } from '@playwright/test';
import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';
import { mkdir, mkdtemp, writeFile, rm } from 'node:fs/promises';
import { resolve, join } from 'node:path';
import { deflateSync } from 'node:zlib';

// Construct deterministic valid PNGs without another media fixture dependency.
function png(width: number, height: number, seed: number) {
  const chunk = (kind: string, data: Buffer) => {
    const body = Buffer.concat([Buffer.from(kind), data]); let crc = 0xffffffff;
    for (const byte of body) { crc ^= byte; for (let i = 0; i < 8; i++) crc = crc & 1 ? (crc >>> 1) ^ 0xedb88320 : crc >>> 1; }
    const result = Buffer.alloc(body.length + 8); result.writeUInt32BE(data.length); body.copy(result, 4); result.writeUInt32BE((crc ^ 0xffffffff) >>> 0, result.length - 4); return result;
  };
  const header = Buffer.alloc(13); header.writeUInt32BE(width); header.writeUInt32BE(height, 4); header[8] = 8; header[9] = 6;
  const pixels = Buffer.alloc(height * (width * 4 + 1));
  for (let y = 0; y < height; y++) for (let x = 0; x < width; x++) { const i = y * (width * 4 + 1) + 1 + x * 4; pixels[i] = (x * 17 + seed) % 256; pixels[i + 1] = y * 31 % 256; pixels[i + 2] = x * y % 256; pixels[i + 3] = 255; }
  return Buffer.concat([Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]), chunk('IHDR', header), chunk('IDAT', deflateSync(pixels)), chunk('IEND', Buffer.alloc(0))]);
}

test('turn media combines uploads and file snapshots through streaming, lightbox, tabs and reload', async ({ page }) => {
  await mkdir(resolve('../.test-data'), { recursive: true });
  const directory = await mkdtemp(resolve('../.test-data/media-review-'));
  const filename = join(directory, 'screen.png'), first = png(800, 400, 0), second = png(800, 400, 100);
  await writeFile(filename, first);
  let nextRound!: () => void, finish!: () => void;
  const roundGate = new Promise<void>(r => { nextRound = r; }), finalGate = new Promise<void>(r => { finish = r; });
  const remote = createServer(async (request, response) => {
    if (request.url === '/v1/models') { response.setHeader('Content-Type', 'application/json'); response.end(JSON.stringify({ data: [{ id: 'test' }] })); return; }
    if (request.url !== '/v1/chat/completions') { response.statusCode = 404; response.end(); return; }
    const chunks: Buffer[] = []; for await (const chunk of request) chunks.push(chunk);
    const body = JSON.parse(Buffer.concat(chunks).toString());
    const count = body.messages.filter((m: any) => m.role === 'tool').length;
    let delta: any;
    if (body.messages[0].content.includes('You are @viewer.')) delta = { content: 'Upload reviewed by Viewer.' };
    else if (count >= 2) { await finalGate; delta = { content: 'Reviewed both file snapshots.' }; }
    else {
      if (count === 1) { await roundGate; await writeFile(filename, second); }
      delta = { tool_calls: [{ index: 0, id: `read-${count}`, type: 'function', function: { name: 'file_read_image', arguments: JSON.stringify({ path: filename }) } }] };
    }
    response.setHeader('Content-Type', 'text/event-stream');
    response.end(`data: ${JSON.stringify({ choices: [{ delta, finish_reason: 'stop' }] })}\n\ndata: [DONE]\n\n`);
  });
  await new Promise<void>(r => remote.listen(0, '127.0.0.1', r));
  const base = `http://127.0.0.1:${(remote.address() as AddressInfo).port}`;
  let chatId = '', viewerId = '', originalEcho: any;
  try {
    await page.goto('/'); await page.request.post('/api/v1/session', { data: {} });
    originalEcho = (await (await page.request.get('/api/v1/state')).json()).bots.find((bot: any) => bot.id === 'echo');
    const state = await (await page.request.get('/api/v1/state')).json();
    for (const chat of state.chats) { await page.request.delete(`/api/v1/chats/${chat.id}`); await page.request.delete(`/api/v1/chats/${chat.id}/permanent`); }
    for (const bot of state.bots) if (bot.handle === 'viewer') await page.request.delete(`/api/v1/bots/${bot.id}`);
    await page.request.post('/api/v1/bots', { data: { ...originalEcho, roots: [directory] } });
    await page.request.post('/api/v1/endpoints', { data: { endpoint: { id: 'media-review', name: 'Media review', url: base + '/v1', model: 'test', contextTokens: 32768 } } });
    const viewer = await (await page.request.post('/api/v1/bots', { data: { handle: 'viewer', name: 'Viewer', roots: [directory] } })).json(); viewerId = viewer.id;
    const chat = await (await page.request.post('/api/v1/chats', { data: { title: 'Media review' } })).json(); chatId = chat.id;
    const uploaded = await (await page.request.post(`/api/v1/chats/${chatId}/attachments`, { multipart: { file: { name: 'pasted.png', mimeType: 'image/png', buffer: first } } })).json();
    await page.goto('/#' + chatId);
    const sent = await page.request.post(`/api/v1/chats/${chatId}/messages`, { data: { content: '@echo @viewer inspect the images', attachments: [uploaded.id], generation: { endpointId: 'media-review', model: 'test', temperature: 0.6 } } });
    expect(sent.ok()).toBe(true);
    await page.getByRole('tab', { name: /^echo/i }).click();
    const gallery = page.locator('.media-disclosure');
    await expect(gallery).toContainText('2 images');
    await expect(gallery).not.toHaveAttribute('open', '');
    await expect(gallery.locator('img')).toHaveCount(0);
    const fetched: string[] = [];
    page.on('request', request => { if (request.url().includes('/attachments/')) fetched.push(request.url()); });
    await gallery.locator('summary').click();
    await expect(gallery.locator('img')).toHaveCount(2);
    await expect.poll(() => gallery.locator('img').last().evaluate((image: HTMLImageElement) => image.naturalWidth)).toBe(384);
    expect(fetched.every(url => url.includes('?width=384'))).toBe(true);
    await gallery.locator('img').last().evaluate(image => { (window as any).retainedMediaImage = image; });
    const preview = gallery.getByRole('button', { name: 'Preview screen.png', exact: true }).first();
    await preview.click();
    const lightbox = page.locator('dialog.image-lightbox');
    await expect(lightbox).toBeVisible();
    await expect(lightbox.locator('img')).toHaveAttribute('src', /attachments\/[^?]+$/);
    const bounds = await lightbox.boundingBox(); expect(bounds!.width).toBe(page.viewportSize()!.width); expect(bounds!.height).toBe(page.viewportSize()!.height);
    await page.screenshot({ path: test.info().outputPath('media-lightbox.png') });
    await expect(page.getByRole('button', { name: 'Close preview' })).toBeFocused();
    await page.keyboard.press('Shift+Tab'); await expect(lightbox.getByRole('link')).toBeFocused();
    await page.keyboard.press('Tab'); await expect(page.getByRole('button', { name: 'Close preview' })).toBeFocused();
    await lightbox.locator('img').click(); await expect(lightbox).toBeVisible();
    await page.keyboard.press('Escape'); await expect(lightbox).toHaveCount(0); await expect(preview).toBeFocused();
    await preview.click(); await lightbox.click({ position: { x: 4, y: 4 } }); await expect(lightbox).toHaveCount(0);
    nextRound();
    await expect(gallery).toContainText('3 images');
    expect(await gallery.locator('img').nth(1).evaluate(image => image === (window as any).retainedMediaImage)).toBe(true);
    finish();
    await expect(page.locator('.response-group')).toContainText('Reviewed both file snapshots.');
    await expect(gallery).toHaveAttribute('open', '');
    await expect(page.locator('.work-disclosure')).not.toHaveAttribute('open', '');
    await page.screenshot({ path: test.info().outputPath('media-gallery.png') });
    await page.getByRole('tab', { name: /^Viewer/ }).click(); await expect(page.locator('.media-disclosure')).not.toHaveAttribute('open', '');
    await page.getByRole('tab', { name: /^echo/i }).click(); await expect(gallery).toHaveAttribute('open', '');
    const messages = await (await page.request.get(`/api/v1/chats/${chatId}/messages`)).json();
    const tools = messages.filter((m: any) => m.role === 'tool'); expect(tools).toHaveLength(2);
    const originals = await Promise.all(tools.map(async (m: any) => (await page.request.get(`/api/v1/chats/${chatId}/attachments/${m.attachments[0].id}`)).body()));
    expect(originals).toEqual([first, second]);
    await page.reload();
    await expect(gallery).toContainText('3 images'); await expect(gallery).not.toHaveAttribute('open', '');
    await gallery.locator('summary').click();
    await page.setViewportSize({ width: 390, height: 844 });
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(390);
    await expect(gallery.locator('img')).toHaveCount(3);
    await page.screenshot({ path: test.info().outputPath('media-mobile.png') });
  } finally {
    nextRound(); finish();
    if (chatId) await page.request.delete(`/api/v1/chats/${chatId}`);
    if (viewerId) await page.request.delete(`/api/v1/bots/${viewerId}`);
    if (originalEcho) await page.request.post('/api/v1/bots', { data: originalEcho });
    await page.request.delete('/api/v1/endpoints/media-review');
    remote.closeAllConnections(); await new Promise<void>(r => remote.close(() => r()));
    await rm(directory, { recursive: true, force: true });
  }
});
