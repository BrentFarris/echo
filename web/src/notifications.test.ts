import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { ChatNotifications, defaultNotificationPreferences, notificationPermission, popupEnabled, soundEnabled, soundFiles, type ChatAlert } from './notifications';
import { notificationRoute, parseChatRoute } from './notificationRoutes';

const alert: ChatAlert = { id: 'event', kind: 'completion', homeNodeId: 'a'.repeat(32), chatId: 'chat', botId: 'bot', messageId: 'message', occurredAt: new Date().toISOString(), title: 'Bot finished', preview: 'Review the result', active: true };
let claims: Set<string>, calls: string[], plays: string[], fetchMock: ReturnType<typeof vi.fn>;
beforeEach(() => {
  vi.useFakeTimers(); localStorage.clear(); document.body.innerHTML = '';
  vi.spyOn(document, 'hasFocus').mockReturnValue(true);
  vi.spyOn(document, 'hidden', 'get').mockReturnValue(false);
  vi.stubGlobal('isSecureContext', true);
  claims = new Set(); calls = []; plays = [];
  vi.stubGlobal('Audio', class { currentTime = 0; muted = false; preload = ''; constructor(public src: string) {} play() { plays.push(this.src); return Promise.resolve(); } pause() {} });
  fetchMock = vi.fn(async (url: string, init?: RequestInit) => {
    calls.push(url); const body = init?.body ? JSON.parse(String(init.body)) : {};
    let value: unknown = {};
    if (url.endsWith('/devices')) value = { id: 'device', push: false, preferences: defaultNotificationPreferences };
    if (url.endsWith('/pending')) value = [];
    if (url.endsWith('/claim')) { const claimed = !claims.has(body.alert); claims.add(body.alert); value = { claimed }; }
    return { ok: true, text: async () => JSON.stringify(value) };
  });
  vi.stubGlobal('fetch', fetchMock);
});
afterEach(() => { vi.clearAllTimers(); vi.useRealTimers(); vi.restoreAllMocks(); vi.unstubAllGlobals(); });
async function flush() { for (let i = 0; i < 15; i++) await Promise.resolve(); }

test('routes preserve chat, bot and message including qualified Swarm identities', () => {
  const route = notificationRoute(alert.homeNodeId, alert);
  expect(parseChatRoute(route)).toEqual({ chatId: `${alert.homeNodeId}:chat`, botId: `${alert.homeNodeId}:bot`, messageId: `${alert.homeNodeId}:message` });
  expect(parseChatRoute('#existing-chat')).toEqual({ chatId: 'existing-chat', botId: undefined, messageId: undefined });
  const worker = 'b'.repeat(32) + ':reply';
  expect(parseChatRoute(notificationRoute(alert.homeNodeId, { chatId: 'chat', messageId: worker })).messageId).toBe(worker);
});
test('completion and question controls and sounds are independent', () => {
  const p = { ...defaultNotificationPreferences, completion: false, questionSound: false };
  expect(popupEnabled(p, 'completion')).toBe(false); expect(popupEnabled(p, 'question')).toBe(true);
  expect(soundEnabled(p, 'completion')).toBe(true); expect(soundEnabled(p, 'question')).toBe(false);
  expect(soundFiles.completion).not.toBe(soundFiles.question);
});
test('viewing the conversation suppresses popup but keeps original sound', async () => {
  const service = new ChatNotifications({ viewing: () => true, navigate: vi.fn(), notice: vi.fn() });
  await service.start(); service.receive(alert); await flush();
  expect(plays).toEqual(['/sounds/completion.wav']); expect(document.querySelector('.chat-notification')).toBeNull();
});
test('other conversation gets clickable toast and question sound, without duplicates across tabs', async () => {
  const navigate = vi.fn(); const options = { viewing: () => false, navigate, notice: vi.fn() };
  const first = new ChatNotifications(options), second = new ChatNotifications(options);
  await first.start(); await second.start();
  const question = { ...alert, kind: 'question' as const };
  first.receive(question); second.receive(question); await flush();
  expect(plays).toEqual(['/sounds/question.wav']); expect(document.querySelectorAll('.chat-notification')).toHaveLength(1);
  (document.querySelector('.chat-notification-open') as HTMLButtonElement).click();
  expect(navigate).toHaveBeenCalledWith(notificationRoute(alert.homeNodeId, alert));
  first.receive(question); await flush(); expect(plays).toHaveLength(1);
});
test('push winner rejects late live delivery', async () => {
  const service = new ChatNotifications({ viewing: () => false, navigate: vi.fn(), notice: vi.fn() });
  await service.start(); claims.add(alert.id); service.receive(alert); await flush();
  expect(plays).toEqual([]); expect(document.querySelector('.chat-notification')).toBeNull();
});
test('denied permission retains in-app alerts and independent settings', async () => {
  vi.stubGlobal('Notification', { permission: 'denied' });
  const service = new ChatNotifications({ viewing: () => false, navigate: vi.fn(), notice: vi.fn() });
  await service.start(); const root = document.createElement('div'); document.body.append(root); await service.mount(root);
  expect(notificationPermission()).toBe('denied'); expect(root.textContent).toContain('Notifications are blocked');
  service.receive(alert); await flush(); expect(document.querySelector('.chat-notification')).not.toBeNull();
  const input = root.querySelector('input')!; input.click(); await flush();
  expect(JSON.parse(localStorage.getItem('echo-notification-preferences')!).completion).toBe(false);
});
test('question resolution removes its outstanding toast', async () => {
  const service = new ChatNotifications({ viewing: () => false, navigate: vi.fn(), notice: vi.fn() });
  await service.start(); service.receive({ ...alert, kind: 'question' }); await flush();
  service.receive({ ...alert, kind: 'question', active: false }); expect(document.querySelector('.chat-notification')).toBeNull();
});
