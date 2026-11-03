import { test, expect } from '@playwright/test';

test('remote sign-in survives browser relaunch and devices can be disconnected', async ({ page, browser }) => {
  await page.goto('/');
  await expect(page.getByRole('button', { name: '⚙ Settings', exact: true })).toBeVisible();
  const password = 'browser-remote-password';
  expect((await page.request.post('/api/v1/hosting/password', { data: { password } })).ok()).toBe(true);
  // Chromium resolves *.localhost to loopback, while Echo requires a password
  // for this hostname. This exercises the actual remote login path.
  const remoteURL = 'http://remote.localhost:3742';
  const first = await browser.newContext();
  const remotePage = await first.newPage();
  await remotePage.goto(remoteURL);
  await remotePage.getByLabel('Password', { exact: true }).fill(password);
  await remotePage.getByRole('button', { name: 'Sign in', exact: true }).click();
  await expect(remotePage.getByRole('button', { name: '⚙ Settings', exact: true })).toBeVisible();
  const saved = await first.storageState();
  const cookie = saved.cookies.find(cookie => cookie.name === 'echo2-session')!;
  expect(cookie.httpOnly).toBe(true);
  expect(cookie.expires).toBeGreaterThan(Date.now() / 1000 + 29 * 24 * 3600);
  expect(saved.origins.flatMap(origin => origin.localStorage).some(item => item.value.includes(password))).toBe(false);
  await first.close();

  const second = await browser.newContext({ storageState: saved });
  try {
    const remote = await second.newPage();
    await remote.goto(remoteURL);
    await expect(remote.getByRole('button', { name: '⚙ Settings', exact: true })).toBeVisible();
    await remote.reload();
    await expect(remote.getByRole('button', { name: '⚙ Settings', exact: true })).toBeVisible();
    expect((await second.cookies()).find(cookie => cookie.name === 'echo2-session')!.value).toBe(cookie.value);
    await expect.poll(async () => {
      const sessions = await (await remote.request.get(remoteURL + '/api/v1/hosting/sessions')).json();
      return sessions.find((session: { current?: boolean }) => session.current)?.connected;
    }).toBe(true);
    await page.reload();
    await page.getByRole('button', { name: '⚙ Settings', exact: true }).click();
    await page.locator('.settings-nav').getByRole('button', { name: 'Hosted Server', exact: true }).click();
    const devices = page.locator('[data-sessions]');
    await expect(devices).toContainText('This device');
    const sessions = await (await remote.request.get(remoteURL + '/api/v1/hosting/sessions')).json();
    const current = sessions.find((session: { current?: boolean }) => session.current);
    const card = devices.locator('.list-card').filter({ has: page.locator(`[data-close-session="${current.id}"]`) });
    await expect(card).toContainText('Connected now');
    await card.scrollIntoViewIfNeeded();
    await page.screenshot({ path: 'test-results/hosted-server-devices.png', fullPage: true });
    await card.getByRole('button', { name: 'Close session', exact: true }).click();
    await expect(card).toHaveCount(0);
    await expect(remote.getByRole('button', { name: 'Sign in', exact: true })).toBeVisible();
    expect((await remote.request.get(remoteURL + '/api/v1/state')).status()).toBe(401);
    await remote.reload();
    await expect(remote.getByRole('button', { name: 'Sign in', exact: true })).toBeVisible();
    await remote.getByLabel('Password', { exact: true }).fill(password);
    await remote.getByRole('button', { name: 'Sign in', exact: true }).click();
    await remote.getByRole('button', { name: '⚙ Settings', exact: true }).click();
    await remote.locator('.settings-nav').getByRole('button', { name: 'Hosted Server', exact: true }).click();
    await remote.getByLabel('Remote access password', { exact: true }).fill('replacement-browser-password');
    await remote.getByRole('button', { name: 'Set password', exact: true }).click();
    await expect(remote.getByRole('status')).toContainText('Password saved');
    await expect(remote.locator('[data-sessions]')).toContainText('This device');
    expect((await remote.request.get(remoteURL + '/api/v1/session')).status()).toBe(200);
  } finally { await second.close(); }
});
