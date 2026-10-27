import { afterEach, expect, test, vi } from 'vitest';
import { Apps, appIcon, appStatus, dockedApps, pluginAvailable, registeredApps } from './apps';
import type { Bot, Plugin } from './types';

const echo: Bot = { id: 'echo', name: 'Echo', handle: 'echo', prompt: '', roots: [], plugins: {}, enableMultitasking: false, revision: 1 };
const plugin = (id: string, extra: Partial<Plugin> = {}): Plugin => ({ id, name: id, enabled: true, pending: false, scope: 'bot', builtin: false, capabilities: [], config: {}, settings: { properties: {} }, app: { viewId: 'main' }, ...extra });
afterEach(() => document.body.replaceChildren());

test('only explicit registrations appear, sorted by name, while unavailable pins remain stored', () => {
  const apps = [plugin('z'), plugin('b', { enabled: false }), plugin('pending', { pending: true }), plugin('a'), plugin('view-only', { app: undefined, views: [{ id: 'main', kind: 'floating', title: '', html: '' }] })];
  expect(registeredApps(apps).map(p => p.id)).toEqual(['a', 'b', 'pending', 'z']);
  const ids = ['z', 'b', 'missing', 'a', 'a', 'pending'];
  expect(dockedApps(apps, ids, echo).map(p => p.id)).toEqual(['z', 'a']);
  expect(ids).toEqual(['z', 'b', 'missing', 'a', 'a', 'pending']);
  apps[1].enabled = true;
  expect(dockedApps(apps, ids, echo).map(p => p.id)).toEqual(['z', 'b', 'a']);
});

test('availability follows server scopes, bot overrides, pending approval, and missing bots', () => {
  const disabledEcho = { ...echo, plugins: { calculator: false } };
  expect(appStatus(plugin('calculator'), disabledEcho)).toBe('Disabled for Echo');
  expect(appStatus(plugin('calculator', { scope: 'app' }), disabledEcho)).toBe('');
  expect(appStatus(plugin('calculator', { enabled: false }), echo)).toBe('Disabled');
  expect(appStatus(plugin('calculator', { enabled: false, pending: true }), echo)).toBe('Awaiting permission');
  expect(pluginAvailable(plugin('calculator', { scope: 'echo' }), { ...echo, id: 'other' })).toBe(false);
  expect(pluginAvailable(plugin('calculator'), undefined)).toBe(false);
});

test('icons render as images, reject remote markup, and fall back to initials on decode failure', () => {
  expect(appIcon(plugin('calculator', { name: 'Pocket Calculator' })).textContent).toBe('PC');
  for (const source of ['https://example.com/icon.png', '<svg onload=alert(1)>', 'data:text/html,bad', 'data:image/png,' + 'x'.repeat(256 * 1024)]) {
    expect(appIcon(plugin('calculator', { app: { viewId: 'main', icon: source } })).querySelector('img')).toBeNull();
  }
  const icon = appIcon(plugin('calculator', { app: { viewId: 'main', icon: 'data:image/svg+xml,%3Csvg/%3E' } }));
  expect(icon.querySelector('svg')).toBeNull();
  icon.querySelector('img')!.dispatchEvent(new Event('error'));
  expect(icon.textContent).toBe('C');
});

test('failed docking keeps the original state and restores the action for retry', async () => {
  const catalog = document.createElement('div'), dock = document.createElement('nav'); document.body.append(catalog, dock);
  let reject!: (error: Error) => void;
  const error = vi.fn(), change = vi.fn(() => new Promise<void>((_, no) => { reject = no; }));
  const apps = new Apps(catalog, dock, { open: vi.fn(), settings: vi.fn(), dock: change, error });
  apps.render([plugin('calculator')], [], echo);
  catalog.querySelector<HTMLButtonElement>('[data-app-control="dock:calculator"]')!.click();
  await Promise.resolve();
  expect(catalog.querySelector<HTMLButtonElement>('[data-app-control="dock:calculator"]')!.disabled).toBe(true);
  reject(new Error('Save failed'));
  await vi.waitFor(() => expect(error).toHaveBeenCalled());
  const retry = catalog.querySelector<HTMLButtonElement>('[data-app-control="dock:calculator"]')!;
  expect(retry.textContent).toBe('Dock'); expect(retry.disabled).toBe(false); expect(dock.hidden).toBe(true);
});

test('inactive cards allow undocking and app refresh preserves focused dock icons', async () => {
  const catalog = document.createElement('div'), dock = document.createElement('nav'); document.body.append(catalog, dock);
  const change = vi.fn(async () => {}), settings = vi.fn();
  const apps = new Apps(catalog, dock, { open: vi.fn(), settings, dock: change, error: vi.fn() });
  const entries = [plugin('a'), plugin('b', { enabled: false })];
  apps.render(entries, ['b', 'a'], echo);
  expect(catalog.querySelector<HTMLButtonElement>('[data-app-control="open:b"]')!.disabled).toBe(true);
  const unpin = catalog.querySelector<HTMLButtonElement>('[data-app-control="dock:b"]')!;
  expect(unpin.disabled).toBe(false); unpin.click(); await vi.waitFor(() => expect(change).toHaveBeenCalledWith('b', false));
  const button = dock.querySelector<HTMLButtonElement>('button')!; button.focus();
  apps.render([...entries, plugin('c')], ['b', 'a', 'c'], echo);
  expect((document.activeElement as HTMLElement).dataset.appControl).toBe('open:a');
});
