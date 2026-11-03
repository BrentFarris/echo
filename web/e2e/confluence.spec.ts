import { test, expect } from '@playwright/test';
import { createServer } from 'node:http';
import type { AddressInfo } from 'node:net';

test('Confluence settings protect tokens and a bot reads the complete paginated page', async ({ page }) => {
  const fullText = '界🙂 onboarding '.repeat(2500) + 'END OF FULL PAGE';
  let readCalls = 0, receivedBodies: string[] = [], modelCalls = 0, userLookupCalls = 0, currentUserCalls = 0, authorCQL = '';
  const upstream = createServer(async (request, response) => {
    response.setHeader('Content-Type', 'application/json');
    if (request.url === '/v1/chat/completions') {
      const chunks: Buffer[] = []; for await (const chunk of request) chunks.push(chunk);
      const input = JSON.parse(Buffer.concat(chunks).toString());
      const results = input.messages.filter((m: any) => m.role === 'tool').map((m: any) => { try { return JSON.parse(m.content); } catch { return {}; } });
      const last = results.at(-1);
      const currentUser = results.find((r: any) => r.username === 'service.account');
      const users = results.find((r: any) => r.results?.some((u: any) => u.userKey));
      const pages = results.find((r: any) => r.results?.some((item: any) => item.type === 'page'));
      const reads = results.filter((r: any) => r.id === '42' && typeof r.body === 'string');
      let call: any;
      if (!currentUser) call = { name: 'confluence_get_current_user', arguments: '{}' };
      else if (!users) call = { name: 'confluence_search_users', arguments: JSON.stringify({ query: 'Brent Farris' }) };
      else if (!pages) call = { name: 'confluence_search', arguments: JSON.stringify({ cql: `type = page AND creator = "${users.results[0].username}" ORDER BY created DESC`, limit: 3 }) };
      else if (!reads.length) call = { name: 'confluence_read', arguments: JSON.stringify({ id: '42' }) };
      else if (!last.complete && last.nextContinuation) call = { name: 'confluence_read', arguments: JSON.stringify({ id: '42', continuation: last.nextContinuation }) };
      else receivedBodies = reads.map((r: any) => r.body);
      const delta = call ? { tool_calls: [{ index: 0, id: `confluence-${++modelCalls}`, type: 'function', function: call }] } : { content: 'Read the complete Confluence onboarding page, including END OF FULL PAGE.' };
      response.setHeader('Content-Type', 'text/event-stream'); response.end(`data: ${JSON.stringify({ choices: [{ delta, finish_reason: call ? 'tool_calls' : 'stop' }] })}\n\ndata: [DONE]\n\n`);
    } else if (request.headers.authorization !== 'Bearer browser-confluence-token') {
      response.statusCode = 401; response.end('{}');
    } else if (request.url === '/confluence/rest/api/user/current') {
      currentUserCalls++; response.end(JSON.stringify({ type: 'known', username: 'service.account', userKey: 'service-key', displayName: 'Service Account' }));
    } else if (request.url?.startsWith('/confluence/rest/api/search?')) {
      const cql = new URL(request.url, 'http://fixture').searchParams.get('cql') || '';
      if (cql.startsWith('user.fullname')) {
        userLookupCalls++; response.end(JSON.stringify({ results: [{ user: { username: 'brent.farris', userKey: 'brent-hash', displayName: 'Brent Farris' } }] }));
      } else {
        if (cql.includes('creator')) authorCQL = cql;
        response.end(JSON.stringify({ results: ['42', '43', '44'].map(id => ({ content: { id, type: 'page', title: id === '42' ? 'Onboarding' : `Page ${id}`, space: { key: 'ENG' }, version: { number: 2 } }, excerpt: 'An onboarding page' })) }));
      }
    } else if (request.url?.startsWith('/confluence/rest/api/content/42?')) {
      readCalls++; response.end(JSON.stringify({ id: '42', type: 'page', title: 'Onboarding', version: { number: 2 }, body: { storage: { value: `<p>${fullText}</p>` } } }));
    } else { response.statusCode = 404; response.end('{}'); }
  });
  await new Promise<void>(resolve => upstream.listen(0, '127.0.0.1', resolve));
  const base = `http://127.0.0.1:${(upstream.address() as AddressInfo).port}`;
  let chatId = '', original: any;
  try {
    await page.goto('/'); await expect(page.locator('#settings-button')).toBeVisible();
    original = (await (await page.request.get('/api/v1/state')).json()).plugins.find((p: any) => p.id === 'confluence');
    await page.locator('#settings-button').click(); await page.locator('.settings-nav [data-section="confluence"]').click();
    await page.getByLabel('Deployment type').selectOption('datacenter'); await page.getByLabel('Confluence site/base URL').fill(base + '/confluence');
    await page.getByRole('checkbox', { name: 'Enable Confluence', exact: true }).check(); await page.getByRole('button', { name: 'Save settings', exact: true }).click();
    await expect.poll(async () => (await (await page.request.get('/api/v1/state')).json()).plugins.find((p: any) => p.id === 'confluence').config.url).toBe(base + '/confluence');
    await page.getByLabel('API token / personal access token').fill('browser-confluence-token'); await page.getByRole('button', { name: 'Save token', exact: true }).click();
    await expect(page.locator('[data-confluence-status]')).toHaveText('Token is saved'); await expect(page.getByLabel('API token / personal access token')).toHaveValue('');
    await page.getByRole('button', { name: 'Test connection', exact: true }).click(); await expect(page.locator('[data-confluence-result]')).toContainText('Connected to Confluence');
    expect(await (await page.request.get('/api/v1/state')).text()).not.toContain('browser-confluence-token');
    await page.reload(); await page.locator('#settings-button').click(); await page.locator('.settings-nav [data-section="confluence"]').click();
    await expect(page.locator('[data-confluence-status]')).toHaveText('Token is saved'); await expect(page.getByLabel('Confluence site/base URL')).toHaveValue(base + '/confluence');
    await page.locator('dialog[open]').getByRole('button', { name: 'Close', exact: true }).click();
    const endpoint = await page.request.post('/api/v1/endpoints', { data: { endpoint: { id: 'confluence-test', name: 'Confluence model', url: base + '/v1', model: 'test', contextTokens: 131072 } } }); expect(endpoint.ok()).toBe(true);
    const chat = await (await page.request.post('/api/v1/chats', { data: { title: 'Confluence full content test' } })).json(); chatId = chat.id;
    const sent = await page.request.post(`/api/v1/chats/${chatId}/messages`, { data: { content: 'Identify the configured Confluence account, find the last three pages Brent Farris authored, and read the whole onboarding page.', generation: { endpointId: 'confluence-test', model: 'test', temperature: 0.6 } } }); expect(sent.ok(), await sent.text()).toBe(true);
    await page.goto('/#' + chatId); await expect(page.locator('.response-group')).toContainText('including END OF FULL PAGE');
    expect(readCalls).toBeGreaterThan(1); expect(receivedBodies.join('')).toBe(fullText); expect(modelCalls).toBe(3 + readCalls);
    expect(currentUserCalls).toBe(1); expect(userLookupCalls).toBe(1); expect(authorCQL).toBe('type = page AND creator = "brent.farris" ORDER BY created DESC');
    const transcript = await (await page.request.get(`/api/v1/chats/${chatId}`)).text(); expect(transcript).not.toContain('browser-confluence-token');
    await page.locator('#settings-button').click(); await page.locator('.settings-nav [data-section="confluence"]').click(); await page.getByRole('button', { name: 'Clear token', exact: true }).click(); await expect(page.locator('[data-confluence-status]')).toHaveText('No token saved');
  } finally {
    if (chatId) await page.request.delete(`/api/v1/chats/${chatId}`);
    await page.request.delete('/api/v1/endpoints/confluence-test'); await page.request.post('/api/v1/plugins/confluence/credential', { data: { secret: '' } });
    if (original) await page.request.patch('/api/v1/plugins/confluence', { data: { enabled: original.enabled, config: original.config } });
    upstream.closeAllConnections(); await new Promise<void>(resolve => upstream.close(() => resolve()));
  }
});
