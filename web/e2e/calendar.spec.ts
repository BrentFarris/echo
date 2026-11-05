import { test, expect, type Page } from '@playwright/test';

async function json(page: Page, path: string, method = 'GET', data?: unknown) {
  const response = await page.request.fetch('/api/v1' + path, { method, data });
  expect(response.ok(), await response.text()).toBe(true); return response.json();
}
async function schedule(page: Page, recurrence: string, prompt = 'Calendar manual task', paused = false) {
  return json(page, '/schedules', 'POST', { botId: 'echo', prompt, recurrence, timezone: 'America/Chicago', paused,
    generation: { endpointId: 'test', model: 'echo-test', temperature: .3, thinking: '' } });
}
async function openCalendar(page: Page) {
  await page.reload(); await page.locator('#settings-button').click();
  await page.locator('.settings-nav [data-section="calendar"]').click();
}

test.beforeEach(async ({ page }) => {
  await page.goto('/'); await expect(page.locator('#new-chat')).toBeVisible();
  const state = await json(page, '/state');
  for (const chat of state.chats) { await json(page, `/chats/${chat.id}`, 'DELETE'); await json(page, `/chats/${chat.id}/permanent`, 'DELETE'); }
  for (const saved of state.schedules) await json(page, `/schedules/${saved.id}`, 'DELETE');
  const echo = state.bots.find((b: any) => b.id === 'echo');
  await json(page, '/bots', 'POST', { ...echo, modelOverride: null, plugins: { ...echo.plugins, calendar: true } });
  await json(page, '/plugins/calendar', 'PATCH', { enabled: true, config: {} });
  await json(page, '/endpoints', 'POST', { endpoint: { id: 'test', name: 'Test model', url: 'http://127.0.0.1:3743/v1', model: 'echo-test', contextTokens: 32768 } });
});
test.afterEach(async ({ page }) => {
  for (const chat of (await json(page, '/state')).chats) await page.request.post(`/api/v1/chats/${chat.id}/stop`, { data: {} });
});

test('Run Now consumes a paused one-time event, updates active state, and preserves settings edits', async ({ page }) => {
  const saved = await schedule(page, new Date(Date.now() + 3600_000).toISOString(), 'slow stream calendar task', true);
  await openCalendar(page);
  const card = page.locator(`[data-schedule-card="${saved.id}"]`), run = card.getByRole('button', { name: 'Run Now', exact: true });
  // This is an unsaved form change; live card updates must not replace it.
  const enabled = page.getByRole('checkbox', { name: 'Enable Calendar', exact: true });
  await enabled.uncheck();
  await run.click();
  await expect(page.getByRole('status').filter({ hasText: 'Run queued' })).toBeVisible();
  await expect(page.locator('#settings')).toBeVisible();
  await expect(card).toContainText('No future automatic run');
  await expect(card).toContainText('Paused');
  await expect(run).toBeDisabled();
  await expect(enabled).not.toBeChecked();
  await expect.poll(async () => (await json(page, '/state')).occurrences.filter((o: any) => o.scheduleId === saved.id && o.status === 'completed').length).toBe(1);
  await expect(run).toBeEnabled();
  await expect(enabled).not.toBeChecked();
  const current = (await json(page, '/state')).schedules.find((s: any) => s.id === saved.id);
  expect(current).toMatchObject({ paused: true, oneShotConsumed: true });
  expect(current.next).toMatch(/^0001-/);
  await run.click();
  await expect.poll(async () => (await json(page, '/state')).occurrences.filter((o: any) => o.scheduleId === saved.id && o.status === 'completed').length).toBe(2);
  expect((await json(page, '/state')).chats).toHaveLength(2);
});

for (const recurrence of ['every 5 minutes', 'daily 17:00', 'weekly WED 17:00']) {
  test(`Run Now applies the correct timing for ${recurrence}`, async ({ page }) => {
    const saved = await schedule(page, recurrence);
    await openCalendar(page);
    const response = page.waitForResponse(r => r.url().endsWith(`/schedules/${saved.id}/run`) && r.request().method() === 'POST');
    await page.locator(`[data-run-schedule="${saved.id}"]`).click();
    const accepted = await response;
    expect(accepted.status()).toBe(202);
    const result = await accepted.json();
    if (recurrence.startsWith('every')) expect(Date.parse(result.schedule.next) - Date.parse(result.occurrence.intendedAt)).toBe(300_000);
    else expect(result.schedule.next).toBe(saved.next);
    expect(result.occurrence).toMatchObject({ manual: true, botId: 'echo', prompt: saved.prompt, generation: saved.generation });
    await expect.poll(async () => (await json(page, '/state')).occurrences.find((o: any) => o.id === result.occurrence.id)?.status).toBe('completed');
    await expect(page.locator('#settings')).toBeVisible();
  });
}

test('Run Now reports conflicts and follows saved Calendar availability', async ({ page }) => {
  const saved = await schedule(page, 'every 5 minutes');
  await openCalendar(page);
  const run = page.locator(`[data-run-schedule="${saved.id}"]`);
  await page.route(`**/schedules/${saved.id}/run`, route => route.fulfill({ status: 409, json: { error: 'This schedule already has running work' } }));
  await run.click();
  await expect(page.getByRole('alert')).toHaveText('This schedule already has running work');
  expect((await json(page, '/state')).schedules.find((s: any) => s.id === saved.id).next).toBe(saved.next);
  await json(page, '/plugins/calendar', 'PATCH', { enabled: false, config: {} });
  await expect(run).toBeDisabled();
  await expect(run).toHaveAttribute('title', 'Calendar is disabled for this bot');
  await json(page, '/plugins/calendar', 'PATCH', { enabled: true, config: {} });
  await expect(run).toBeEnabled();
});
