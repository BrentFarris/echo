import { test, expect } from '@playwright/test';

test('serves an installable manifest, artwork and a controlling service worker', async ({ page, context }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: '＋ New chat', exact: true })).toBeVisible();
  const manifestURL = await page.locator('link[rel="manifest"]').getAttribute('href');
  const response = await page.request.get(manifestURL!);
  expect(response.ok()).toBe(true);
  expect(response.headers()['content-type']).toContain('application/manifest+json');
  expect(response.headers()['cache-control']).toBe('no-cache');
  const manifest = await response.json();
  expect(manifest).toMatchObject({ id: '/', name: 'Echo', start_url: '/', scope: '/', display: 'standalone' });
  expect(manifest.icons.map((icon: { sizes: string }) => icon.sizes)).toEqual(['192x192', '512x512']);
  const favicon = await page.locator('link[rel="icon"]').getAttribute('href');
  for (const [src, size] of [[favicon!, 32], ['/icons/echo-180.png', 180], ...manifest.icons.map((icon: { src: string; sizes: string }) => [icon.src, Number(icon.sizes.split('x')[0])])] as [string, number][]) {
    const dimensions = await page.evaluate(async src => {
      const image = new Image(); image.src = src; await image.decode();
      return [image.naturalWidth, image.naturalHeight];
    }, src);
    expect(dimensions).toEqual([size, size]);
  }
  await page.evaluate(async () => { await navigator.serviceWorker.ready; });
  await expect.poll(() => page.evaluate(() => navigator.serviceWorker.controller?.scriptURL)).toContain('/sw.js');
  const cdp = await context.newCDPSession(page);
  const browserManifest = await cdp.send('Page.getAppManifest');
  expect(browserManifest.errors).toEqual([]);
  const installability = await cdp.send('Page.getInstallabilityErrors');
  expect(installability.installabilityErrors).toEqual([]);
});

test('install button prompts once, handles dismissal and hides after installation', async ({ page }) => {
  await page.goto('/');
  const install = page.getByRole('button', { name: 'Install Echo', exact: true });
  await expect(page.getByRole('button', { name: '＋ New chat', exact: true })).toBeVisible();
  const offerInstall = async (outcome: 'dismissed' | 'accepted') => page.evaluate(outcome => {
    const event = new Event('beforeinstallprompt', { cancelable: true });
    Object.assign(event, {
      prompt: async () => { document.body.dataset.installPrompts = String(Number(document.body.dataset.installPrompts || 0) + 1); },
      userChoice: Promise.resolve({ outcome })
    });
    window.dispatchEvent(event);
    return event.defaultPrevented;
  }, outcome);
  expect(await offerInstall('dismissed')).toBe(true);
  await expect(install).toBeVisible();
  await install.click();
  await expect(install).toBeHidden();
  await expect(page.locator('body')).toHaveAttribute('data-install-prompts', '1');
  await offerInstall('accepted');
  await install.click();
  await expect(page.locator('body')).toHaveAttribute('data-install-prompts', '2');
  await page.evaluate(() => window.dispatchEvent(new Event('appinstalled')));
  await offerInstall('accepted');
  await expect(install).toBeHidden();
});

test('offline launch explains reconnection, keeps private data uncached and recovers', async ({ page, context }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: '＋ New chat', exact: true })).toBeVisible();
  await page.evaluate(async () => { await navigator.serviceWorker.ready; });
  await expect.poll(() => page.evaluate(() => !!navigator.serviceWorker.controller)).toBe(true);
  await page.evaluate(async () => { await fetch('/api/v1/state'); });
  const cachedPaths = await page.evaluate(async () => {
    const paths: string[] = [];
    for (const key of await caches.keys()) {
      for (const request of await (await caches.open(key)).keys()) paths.push(new URL(request.url).pathname);
    }
    return paths;
  });
  expect(cachedPaths).toEqual(['/offline.html']);
  await context.setOffline(true);
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Reconnect to Echo' })).toBeVisible();
  await expect(page.getByText('echo.exe', { exact: true })).toBeVisible();
  const apiOffline = await page.evaluate(async () => {
    try { await fetch('/api/v1/state'); return false; } catch { return true; }
  });
  expect(apiOffline).toBe(true);
  await page.screenshot({ path: 'test-results/echo-pwa-offline.png' });
  await context.setOffline(false);
  await page.getByRole('link', { name: 'Try again' }).click();
  await expect(page.getByRole('button', { name: '＋ New chat', exact: true })).toBeVisible();
});
