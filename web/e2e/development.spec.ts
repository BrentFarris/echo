import { test, expect, type Page } from '@playwright/test';

// These tests route API responses; service workers can bypass page routing.
test.use({ serviceWorkers: 'block' });
import { resolve } from 'node:path';

async function openDevelopment(page: Page) {
  await page.goto('/');
  await page.getByRole('button', { name: '⚙ Settings', exact: true }).click();
  await page.locator('.settings-nav').getByRole('button', { name: 'Development', exact: true }).click();
  await expect(page.getByLabel('Echo Git repository path', { exact: true })).toBeVisible();
}

test('Development saves a server repository and keeps source-free controls available', async ({ page }) => {
  await page.goto('/');
  await page.request.post('/api/v1/session', { data: {} });
  await page.request.put('/api/v1/development', { data: { repositoryPath: '' } });
  await openDevelopment(page);
  const sections = await page.locator('.settings-nav button').allTextContents();
  expect(sections.at(-1)).toBe('Development');
  await expect(page.locator('.settings-nav > hr + button')).toHaveText('Development');
  await expect(page.getByRole('button', { name: 'Relaunch', exact: true })).toBeEnabled();
  await expect(page.getByRole('button', { name: 'Terminate Echo', exact: true })).toBeEnabled();
  await expect(page.getByRole('button', { name: 'Build & Relaunch', exact: true })).toBeDisabled();
  const input = page.getByLabel('Echo Git repository path', { exact: true });
  await input.fill('relative');
  await page.getByRole('button', { name: 'Save repository path' }).click();
  await expect(page.locator('[data-development-status]')).toContainText('absolute');
  await input.fill(resolve('..'));
  await page.getByRole('button', { name: 'Save repository path' }).click();
  await expect(page.locator('[data-development-status]')).toHaveText('Repository path saved.');
  await expect(page.getByRole('button', { name: 'Build & Relaunch', exact: true })).toBeEnabled();
  await page.locator('.settings-nav [data-section="development"]').scrollIntoViewIfNeeded();
  await page.screenshot({ path: test.info().outputPath('development-desktop.png') });
  await page.setViewportSize({ width: 390, height: 844 });
  await expect(page.getByRole('button', { name: 'Terminate Echo', exact: true })).toBeVisible();
  await page.screenshot({ path: test.info().outputPath('development-mobile.png') });
  await page.setViewportSize({ width: 1280, height: 720 });
  await page.reload();
  await page.getByRole('button', { name: '⚙ Settings', exact: true }).click();
  await page.locator('.settings-nav').getByRole('button', { name: 'Development', exact: true }).click();
  await expect(input).toHaveValue(resolve('..'));
  await page.request.put('/api/v1/development', { data: { repositoryPath: '' } });
});

async function mockDevelopment(page: Page, transientOutage = false) {
  const state: any = { repositoryPath: 'C:\\Echo', suggestedPath: '', repositoryValid: true, branch: 'echo2', upstream: 'origin/echo2', instanceId: 'old' };
  const calls: string[] = [];
  let polls = 0;
  let rollout: any;
  await page.route('**/api/v1/development**', async route => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith('/rollouts/check')) {
      calls.push('rollouts/check');
      await new Promise(resolve => setTimeout(resolve, 500));
      rollout = { id: 'check', sourceNodeId: route.request().postDataJSON().sourceNodeId, scope: 'swarm', target: 'a'.repeat(40), state: 'checked', members: [{ nodeId: 'local', name: 'desktop', status: 'queued', check: { current: false, revision: 'old' } }] };
      await route.fulfill({ json: rollout }); return;
    }
    if (path.endsWith('/rollouts')) { await route.fulfill({ json: rollout ? [rollout] : [] }); return; }
    if (route.request().method() === 'POST') {
      const action = path.split('/').pop()!; calls.push(action); polls = 0;
      state.operation = { id: 'operation', action, status: 'running', stage: 'Preparing', logPath: 'C:\\profile\\development\\operation.log', logTail: '', checkoutUpdated: false };
      await route.fulfill({ json: { operation: state.operation, instanceId: 'old' }, status: 202 });
    } else if (path.includes('/operations/')) {
      if (transientOutage) { transientOutage = false; await route.abort(); return; }
      polls++;
      if (polls >= 2) {
        const action = state.operation.action;
        if (action === 'check-updates') {
          state.operation.status = 'succeeded'; state.operation.stage = 'Update check complete';
          state.comparison = { state: 'behind', ahead: 0, behind: 2 };
        } else if (action === 'build-relaunch') {
          state.operation.status = 'failed'; state.operation.error = 'Frontend build failed'; state.operation.logTail = '<script>not executable</script>\nBuild output';
        } else state.operation.status = action === 'terminate' ? 'stopping' : 'restarting';
      }
      await route.fulfill({ json: state.operation });
    } else { if (route.request().method() === 'PUT') Object.assign(state, route.request().postDataJSON()); await route.fulfill({ json: state }); }
  });
  return { state, calls };
}

test('updates, confirmations, busy controls, and failure logs', async ({ page }) => {
  const { calls } = await mockDevelopment(page);
  await openDevelopment(page);
  await expect(page.locator('[data-tracking]')).toContainText('origin/echo2');
  await page.getByRole('button', { name: 'Check for Updates', exact: true }).click();
  await expect(page.getByLabel('Echo Git repository path', { exact: true })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Relaunch', exact: true })).toBeDisabled();
  await expect(page.getByRole('button', { name: 'Update & Relaunch', exact: true })).toBeVisible();
  await expect(page.locator('[data-rollout-status]')).toContainText('Update available');
  page.once('dialog', dialog => dialog.dismiss());
  await page.getByRole('button', { name: 'Build & Relaunch', exact: true }).click();
  expect(calls).toEqual(['rollouts/check']);
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Build & Relaunch', exact: true }).click();
  await expect(page.locator('[data-development-status]')).toHaveText('Frontend build failed');
  await page.getByText('Operation log', { exact: true }).click();
  await expect(page.locator('[data-log-tail]')).toHaveText('<script>not executable</script>\nBuild output');
  await expect(page.locator('[data-log-tail] script')).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Relaunch', exact: true })).toBeEnabled();
});

test('relaunch reloads the same tab only after a replacement is healthy', async ({ page, context }) => {
  await mockDevelopment(page);
  let healthChecks = 0;
  await page.route('**/api/v1/health', route => route.fulfill({ json: { instanceId: ++healthChecks < 3 ? 'old' : 'new' } }));
  await openDevelopment(page);
  page.once('dialog', dialog => dialog.accept());
  const reloaded = page.waitForEvent('load');
  await page.getByRole('button', { name: 'Relaunch', exact: true }).click();
  await reloaded;
  expect(healthChecks).toBe(3);
  expect(context.pages()).toHaveLength(1);
});

test('termination leaves a stopped message', async ({ page }) => {
  await mockDevelopment(page);
  await openDevelopment(page);
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Terminate Echo', exact: true }).click();
  await expect(page.locator('[data-development-status]')).toHaveText('Echo is shutting down. You can close this browser tab.');
  await expect(page.getByRole('button', { name: 'Terminate Echo', exact: true })).toBeDisabled();
});

test('build monitoring recovers from a transient connection failure', async ({ page }) => {
  await mockDevelopment(page, true);
  await page.route('**/api/v1/health', route => route.fulfill({ json: { instanceId: 'old' } }));
  await openDevelopment(page);
  page.once('dialog', dialog => dialog.accept());
  await page.getByRole('button', { name: 'Build & Relaunch', exact: true }).click();
  await expect(page.locator('[data-development-status]')).toHaveText('Frontend build failed');
  await expect(page.getByRole('button', { name: 'Relaunch', exact: true })).toBeEnabled();
});

test('automatic update preference persists and dots guide to Update & Relaunch', async ({ page }) => {
  const { state } = await mockDevelopment(page);
  state.autoCheckUpdates = false;
  state.updateAvailable = true;
  await openDevelopment(page);
  const preference = page.getByLabel('Automatically search for updates', { exact: true });
  await expect(preference).not.toBeChecked();
  await expect(page.locator('#settings-button .update-dot')).toBeVisible();
  await expect(page.locator('[data-section=development] .update-dot')).toBeVisible();
  await expect(page.locator('[data-rollout-check] .update-dot')).toBeVisible();
  await preference.check();
  await expect(page.locator('[data-development-status]')).toHaveText('Automatic update checks enabled.');
  expect(state.autoCheckUpdates).toBe(true);
  await page.reload();
  await page.getByRole('button', { name: '⚙ Settings', exact: true }).click();
  await page.locator('.settings-nav [data-section=development]').click();
  await expect(preference).toBeChecked();
  await page.getByRole('button', { name: 'Check for Updates', exact: true }).click();
  await expect(page.locator('[data-rollout-start] .update-dot')).toBeVisible();
  await expect(page.locator('[data-rollout-check] .update-dot')).toHaveCount(0);
  await page.screenshot({ path: test.info().outputPath('automatic-update-guidance.png') });
  await preference.uncheck();
  await expect(page.locator('[data-development-status]')).toHaveText('Automatic update checks disabled.');
  expect(state.autoCheckUpdates).toBe(false);
});
