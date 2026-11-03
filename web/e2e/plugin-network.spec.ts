import { test, expect } from '@playwright/test';
import { readFileSync } from 'node:fs';

const base = 'http://127.0.0.1:3742';
const ws = 'ws://127.0.0.1:3742';
const template = readFileSync('../internal/plugins/templates/ui.html', 'utf8');

test('asset-only plugin can review and approve origins without broker capabilities', async ({ page }) => {
  await page.goto('/'); await expect(page.locator('#apps-button')).toBeVisible();
  const id = `assets-${Date.now().toString(36)}`;
  const response = await page.request.post('/__fixtures/app', { data: {
    api: 1, id, name: 'Asset-only acceptance', network: { origins: [base] }, app: { viewId: 'main' },
    views: [{ id: 'main', title: 'Asset-only acceptance', html: `<body><script src="${base}/__fixtures/network/asset.js"></script></body>` }],
  } });
  expect(response.ok()).toBe(true); expect((await response.json()).pending).toBe(true);
  await page.reload(); await page.locator('#apps-button').click();
  const card = page.locator(`.app-card[data-app-id="${id}"]`);
  await card.getByRole('button', { name: 'Settings for Asset-only acceptance' }).click();
  await expect(page.getByRole('region', { name: 'Network access' })).toContainText(base);
  await page.getByRole('button', { name: 'Approve permissions' }).click();
  await page.locator('#settings').getByRole('button', { name: 'Close', exact: true }).click();
  await card.getByRole('button', { name: 'Open Asset-only acceptance' }).click();
  await expect(page.frameLocator('iframe[title="Asset-only acceptance"]').locator('body')).toHaveAttribute('data-network-asset', 'loaded');
});

test('network approval, host HTTP/WebSockets, asset CSP, identity checks and revocation', async ({ page }) => {
  let relayed = 0;
  page.on('websocket', socket => { if (socket.url().includes('/network?')) socket.on('framesent', () => relayed++); });
  await page.goto('/'); await expect(page.locator('#apps-button')).toBeVisible();
  const id = `network-${Date.now().toString(36)}`;
  const html = template + `
    <script src="${base}/__fixtures/network/asset.js"></script>
    <script src="http://localhost:3742/__fixtures/network/asset.js" onerror="document.body.dataset.deniedAsset='blocked'"></script>
    <button id="network-test">Test network</button><output id="network-result"></output>
    <script>
    document.querySelector('#network-test').onclick=async()=>{try{
      const response=await echoCall('web.request',{url:'${base}/__fixtures/network/http',method:'POST',body:'hello'});
      const body=JSON.parse(response.content);
      const {handle}=await echoCall('websocket.open',{url:'${ws}/__fixtures/network/socket'});
      await echoCall('websocket.send',{handle,data:'network echo'});
      const message=await echoCall('websocket.receive',{handle});
      let isolated=false;try{parent.document.body.dataset.compromised='yes'}catch{isolated=true}
      document.querySelector('#network-result').textContent=JSON.stringify({method:body.method,cookie:body.cookie,message:message.data,isolated});
      await echoCall('websocket.close',{handle});
    }catch(error){document.querySelector('#network-result').textContent=String(error)}};
    </script>`;
  const manifest = { api: 1, id, name: 'Network acceptance', version: '1', scope: 'bot', capabilities: ['web.request', 'websocket.open', 'websocket.send', 'websocket.receive', 'websocket.close'], network: { origins: [base, ws] }, app: { viewId: 'main' }, views: [{ id: 'main', kind: 'floating', title: 'Network acceptance', html }] };
  const installed = await page.request.post('/__fixtures/app', { data: manifest }); expect(installed.ok()).toBe(true);
  const plugin = await installed.json(); expect(plugin.pending).toBe(true);
  expect((await page.request.get(`/api/v1/plugins/${id}/views/main`)).ok()).toBe(false);
  await page.reload(); await page.locator('#apps-button').click();
  const card = page.locator(`.app-card[data-app-id="${id}"]`);
  await card.getByRole('button', { name: 'Settings for Network acceptance' }).click();
  const notice = page.getByRole('region', { name: 'Network access' });
  await expect(notice).toContainText(base); await expect(notice).toContainText(ws);
  await expect(notice).toContainText('Local/private network'); await expect(notice).toContainText('New permission requested');
  await page.getByRole('button', { name: 'Approve permissions' }).click();
  await expect(notice).toContainText('Approved destinations');
  await page.locator('#settings').getByRole('button', { name: 'Close', exact: true }).click();
  await card.getByRole('button', { name: 'Open Network acceptance' }).click();
  const frame = page.frameLocator('iframe[title="Network acceptance"]');
  await expect(frame.locator('body')).toHaveAttribute('data-network-asset', 'loaded');
  await expect(frame.locator('body')).toHaveAttribute('data-denied-asset', 'blocked');
  await expect(page.locator('iframe[title="Network acceptance"]')).toHaveAttribute('sandbox', 'allow-scripts');
  await frame.locator('#network-test').click();
  await expect(frame.locator('#network-result')).toHaveText(JSON.stringify({ method: 'POST', cookie: '', message: 'network echo', isolated: true }));
  // Messages from a different source or nonce must never reach the relay.
  const child = page.frames().find(f => f !== page.mainFrame() && f.url() === 'about:srcdoc')!;
  const sentBefore = relayed;
  await page.evaluate(id => window.postMessage({ type: 'echo-plugin-request', nonce: 'forged', pluginId: id, viewId: 'main', id: 'bad', method: 'web.request', params: { url: 'http://not-approved.invalid' } }, '*'), id);
  await child.evaluate(id => parent.postMessage({ type: 'echo-plugin-request', nonce: 'forged', pluginId: id, viewId: 'main', id: 'bad-child', method: 'web.request', params: { url: 'http://not-approved.invalid' } }, '*'), id);
  await child.evaluate(async base => (window as any).echoCall('web.request', { url: base + '/__fixtures/network/http' }), base);
  expect(relayed).toBe(sentBefore + 1);
  expect(await child.evaluate(async () => { try { await fetch('http://127.0.0.1:3742/__fixtures/network/http'); return 'unexpected'; } catch { return 'blocked'; } })).toBe('blocked');
  await page.request.patch(`/api/v1/plugins/${id}`, { data: { enabled: false, config: {} } });
  await expect(page.locator('iframe[title="Network acceptance"]')).toHaveCount(0);
});
