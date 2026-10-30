import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { PluginWindows } from './pluginWindows';
import type { Bot, Plugin } from './types';

const echo: Bot = { id: 'echo', name: 'Echo', handle: 'echo', prompt: '', roots: [], plugins: {}, enableMultitasking: false, revision: 1 };
const plugin: Plugin = { id: 'calculator', name: 'Calculator', enabled: true, pending: false, scope: 'bot', builtin: false, capabilities: [], config: {}, settings: { properties: {} }, digest: 'one', app: { viewId: 'main' }, views: [{ id: 'main', title: 'Calculator', kind: 'floating', html: '<input>' }] };
const view = { ...plugin.views![0], digest: 'one' };
let windows: PluginWindows;
beforeEach(() => {
  vi.stubGlobal('ResizeObserver', class { observe() {} disconnect() {} });
  // jsdom does not implement iframe.sandbox's DOMTokenList.
  Object.defineProperty(HTMLIFrameElement.prototype, 'sandbox', { configurable: true, get() { return { add: (value: string) => this.setAttribute('sandbox', value) }; } });
});
afterEach(() => { windows?.sync([], []); document.body.replaceChildren(); localStorage.clear(); vi.unstubAllGlobals(); });

test('simultaneous launches share a request and later clicks focus the same live iframe', async () => {
  let resolve!: (value: typeof view) => void;
  const api = vi.fn(() => new Promise<typeof view>(yes => { resolve = yes; }));
  windows = new PluginWindows(api as any); windows.sync([plugin], [echo]);
  const first = windows.open('calculator', 'main'), second = windows.open('calculator', 'main');
  expect(first).toBe(second); expect(api).toHaveBeenCalledTimes(1);
  resolve(view); await first;
  const frame = document.querySelector('iframe')!; frame.dataset.retained = 'live';
  const third = windows.open('calculator', 'main'); resolve(view); await third;
  expect(document.querySelectorAll('.floating-plugin')).toHaveLength(1);
  expect(document.querySelector('iframe')).toBe(frame); expect(frame.dataset.retained).toBe('live');
  expect(document.activeElement).toBe(document.querySelector('.floating-plugin header'));
  expect(frame.getAttribute('sandbox')).toBe('allow-scripts');
  expect(api).toHaveBeenCalledWith('/plugins/calculator/views/main?botId=echo');
});

test('updates and bot disablement close stale windows; failed openings can be retried', async () => {
  const api = vi.fn().mockResolvedValue(view);
  windows = new PluginWindows(api as any); windows.sync([plugin], [echo]);
  await windows.open('calculator', 'main');
  windows.sync([{ ...plugin, digest: 'two' }], [echo]);
  expect(document.querySelector('.floating-plugin')).toBeNull();
  windows.sync([plugin], [echo]); api.mockRejectedValueOnce(new Error('Unavailable'));
  await expect(windows.open('calculator', 'main')).rejects.toThrow('Unavailable');
  await windows.open('calculator', 'main');
  windows.sync([plugin], [{ ...echo, plugins: { calculator: false } }]);
  expect(document.querySelector('.floating-plugin')).toBeNull();
});

test('disablement during an in-flight fetch cannot create a stale window', async () => {
  let resolve!: (value: typeof view) => void;
  windows = new PluginWindows((() => new Promise<typeof view>(yes => { resolve = yes; })) as any);
  windows.sync([plugin], [echo]);
  const opening = windows.open('calculator', 'main');
  windows.sync([{ ...plugin, pending: true }], [echo]); resolve(view);
  await expect(opening).rejects.toThrow('changed');
  expect(document.querySelector('.floating-plugin')).toBeNull();
});

test('each bot gets its own window and only that bot loses it on disablement', async () => {
  windows = new PluginWindows(vi.fn().mockResolvedValue(view));
  const other = { ...echo, id: 'other', plugins: { calculator: true } }; windows.sync([plugin], [echo, other]);
  await windows.open('calculator', 'main'); await windows.open('calculator', 'main', 'other');
  expect(document.querySelectorAll('.floating-plugin')).toHaveLength(2);
  windows.sync([plugin], [echo, { ...other, plugins: { calculator: false } }]);
  expect(document.querySelectorAll('.floating-plugin')).toHaveLength(1);
  expect(document.querySelector<HTMLElement>('.floating-plugin')!.dataset.botId).toBe('echo');
});


test('Terminal launches use the persistent dock without creating a window or changing the main view', async () => {
  const terminal: Plugin = { ...plugin, id:'terminal', name:'Terminal', scope:'app', builtin:true };
  const openDock = vi.fn(async () => {}), navigate = vi.fn();
  windows = new PluginWindows(vi.fn().mockResolvedValue({...view,kind:'embedded'}), {openDock,navigate});
  windows.sync([terminal],[echo]);
  await windows.open('terminal','main'); await windows.open('terminal','main');
  expect(openDock).toHaveBeenCalledTimes(2);
  expect(document.querySelector('.floating-plugin,.embedded-plugin,iframe')).toBeNull();
  expect(navigate).not.toHaveBeenCalled();
});

test('disabling Terminal during its view fetch cannot reopen the dock', async () => {
  const terminal: Plugin = { ...plugin, id:'terminal', name:'Terminal', scope:'app', builtin:true };
  const openDock = vi.fn(async () => {});
  let resolve!: (value: typeof view) => void;
  windows = new PluginWindows((() => new Promise<typeof view>(done => {resolve=done;})) as any,{openDock});
  windows.sync([terminal],[echo]);
  const pending = windows.open('terminal','main');
  windows.sync([{...terminal,enabled:false}],[echo]); resolve(view);
  await expect(pending).rejects.toThrow('changed');
  expect(openDock).not.toHaveBeenCalled();
});
