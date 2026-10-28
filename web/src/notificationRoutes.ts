import { resourceID } from './instances';

export type NotificationTarget = { chatId: string; botId?: string; messageId?: string };
export function parseChatRoute(hash: string): NotificationTarget {
  const raw = hash.replace(/^#/, '');
  const index = raw.indexOf('?');
  const params = new URLSearchParams(index < 0 ? '' : raw.slice(index + 1));
  return { chatId: index < 0 ? raw : raw.slice(0, index), botId: params.get('bot') || undefined, messageId: params.get('message') || undefined };
}
export function notificationRoute(home: string, target: NotificationTarget): string {
  const params = new URLSearchParams();
  if (target.botId) params.set('bot', resourceID(home, target.botId));
  if (target.messageId) params.set('message', resourceID(home, target.messageId));
  return '#' + resourceID(home, target.chatId) + (params.size ? '?' + params : '');
}
