import { readFileSync } from 'node:fs';
import vm from 'node:vm';
import { expect, test, vi } from 'vitest';

function worker(clients: unknown[] = []) {
  const handlers: Record<string, (event: any) => void> = {};
  const showNotification = vi.fn().mockResolvedValue(undefined), openWindow = vi.fn().mockResolvedValue(undefined);
  const context = vm.createContext({ URL, Promise, setTimeout, clearTimeout, MessageChannel,
    indexedDB: { open: () => { const request: any = {}; queueMicrotask(() => request.onerror()); return request; } },
    self: { location: { origin: 'https://echo.example' }, addEventListener: (name: string, fn: any) => handlers[name] = fn,
      registration: { showNotification }, clients: { matchAll: async () => clients, openWindow } } });
  vm.runInContext(readFileSync('public/sw.js', 'utf8'), context);
  return { handlers, showNotification, openWindow };
}
test('background push displays a notification with a safe deep link without an open client', async () => {
  const { handlers, showNotification } = worker(); let pending: Promise<void> | undefined;
  handlers.push({ data: { json: () => ({ alert: { id: 'one', title: 'Bot finished', preview: 'Ready', kind: 'completion' }, url: '/#chat?bot=b&message=m' }) }, waitUntil: (promise: Promise<void>) => pending = promise });
  await pending;
  expect(showNotification).toHaveBeenCalledWith('Bot finished', expect.objectContaining({ tag: 'one', silent: false, data: { url: 'https://echo.example/#chat?bot=b&message=m' } }));
});
test('notification click cold-launches Echo and rejects external destinations', async () => {
  const { handlers, openWindow } = worker(); let pending: Promise<void> | undefined;
  const close = vi.fn(); handlers.notificationclick({ notification: { close, data: { url: 'https://evil.example/' } }, waitUntil: (p: Promise<void>) => pending = p }); await pending;
  expect(close).toHaveBeenCalled(); expect(openWindow).toHaveBeenCalledWith('https://echo.example/');
});
