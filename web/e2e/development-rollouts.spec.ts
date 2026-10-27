import { test, expect, type Page } from '@playwright/test';

async function fixture(page: Page) {
  await page.goto('/');
  await page.request.post('/api/v1/session', { data: {} });
  const directory = await (await page.request.get('/api/v1/swarm/peers')).json();
  await page.route('**/api/v1/swarm/peers', route => route.fulfill({ json: { ...directory, enabled: true } }));
  const source = directory.node.nodeId, target = 'a'.repeat(40);
  const state = { repositoryPath: '/echo', repositoryValid: true, suggestedPath: '', branch: 'main', upstream: 'origin/main', instanceId: 'old', revision: target };
  let rollout: any;
  const calls: { path: string; body: any }[] = [];
  let blocked = false;
  await page.route('**/api/v1/development**', async route => {
    const path = new URL(route.request().url()).pathname;
    if (path === '/api/v1/development') { await route.fulfill({ json: state }); return; }
    if (route.request().method() === 'POST') {
      const body = route.request().postDataJSON(); calls.push({ path, body });
      if (path.endsWith('/check')) rollout = { id: 'rollout-1', sourceNodeId: source, scope: body.scope, target, state: 'checked', cancelRequested: false, members: [
        { nodeId: source, name: 'desktop', status: 'succeeded', check: { revision: target, current: true } },
        ...(body.scope === 'swarm' ? [{ nodeId: 'linux', name: 'linux', status: 'queued', check: { revision: 'previous', current: false } }, { nodeId: 'mac', name: 'mac', status: 'skipped', error: 'Machine offline', check: { revision: '', current: false } }] : []),
      ] };
      else if (path.endsWith('/cancel')) { rollout.state = 'cancelled'; rollout.cancelRequested = true; }
      else {
        rollout.state = 'running'; rollout.cancelRequested = false;
        const linux = rollout.members.find((m: any) => m.nodeId === 'linux');
        linux.status = 'draining'; linux.update = { status: 'draining', stage: 'Waiting for active work to finish', revision: 'previous', logPath: '/linux/operation.log', logTail: '<script>unsafe</script>\nNative build complete', checkoutUpdated: true };
      }
      await route.fulfill({ json: rollout }); return;
    }
    if (path === '/api/v1/development/rollouts') { await route.fulfill({ json: rollout ? [rollout] : [] }); return; }
    if (blocked) { await route.abort(); return; }
    await route.fulfill({ json: rollout });
  });
  const open = async () => {
    await page.getByRole('button', { name: '⚙ Settings', exact: true }).click();
    await page.locator('.settings-nav [data-section="development"]').click();
    await expect(page.getByLabel('Update scope')).toBeVisible();
  };
  await open();
  return { calls, source, target, open, disconnect: (value: boolean) => { blocked = value; } };
}

test('swarm update defaults, peer-only lag, confirmation, persistent progress and cancellation', async ({ page }) => {
  const f = await fixture(page);
  await expect(page.getByLabel('Update scope')).toHaveValue('swarm');
  await page.getByRole('button', { name: 'Check for Updates', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Update & Relaunch', exact: true })).toBeVisible();
  await expect(page.locator('[data-rollout-members]')).toContainText('Machine offline');
  page.once('dialog', async dialog => { expect(dialog.message()).toContain(f.target); expect(dialog.message()).toContain('linux: update and relaunch'); expect(dialog.message()).toContain('mac: Machine offline'); await dialog.accept(); });
  await page.getByRole('button', { name: 'Update & Relaunch', exact: true }).click();
  await expect(page.locator('[data-rollout-members]')).toContainText('Waiting for active work');
  await expect(page.getByRole('button', { name: 'Relaunch', exact: true })).toBeDisabled();
  await page.getByText('Update log — linux', { exact: true }).click();
  await expect(page.locator('[data-rollout-members] pre')).toContainText('<script>unsafe</script>');
  await expect(page.locator('[data-rollout-members] script')).toHaveCount(0);
  f.disconnect(true);
  await expect(page.locator('[data-rollout-status]')).toContainText('server continues');
  f.disconnect(false);
  await page.reload(); await f.open();
  await expect(page.locator('[data-rollout-members]')).toContainText('Waiting for active work');
  await page.getByRole('button', { name: 'Cancel pending updates', exact: true }).click();
  await expect(page.getByRole('button', { name: 'Retry unsuccessful machines', exact: true })).toBeVisible();
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Retry unsuccessful machines', exact: true }).click();
  expect(f.calls.map(c => c.path)).toEqual(['/api/v1/development/rollouts/check', '/api/v1/development/rollouts', '/api/v1/development/rollouts/rollout-1/cancel', '/api/v1/development/rollouts/rollout-1/retry']);
  await page.screenshot({ path: test.info().outputPath('swarm-update-progress.png') });
});

test('machine-only scope excludes peers and reports an already-current selected machine', async ({ page }) => {
  const f = await fixture(page);
  await page.getByLabel('Update scope').selectOption('machine');
  await page.getByRole('button', { name: 'Check for Updates', exact: true }).click();
  await expect(page.locator('[data-rollout-status]')).toContainText('Check complete');
  await expect(page.getByRole('button', { name: 'Update & Relaunch', exact: true })).toBeHidden();
  await expect(page.locator('[data-rollout-members]')).not.toContainText('linux');
  expect(f.calls[0].body).toEqual({ sourceNodeId: f.source, scope: 'machine' });
});
