import type { Chat } from './types';
export function groupChats(chats: Chat[]): { key: string; label: string; chats: Chat[] }[] {
  const groups = new Map<string, { key: string; label: string; chats: Chat[] }>();
  for (const chat of chats) {
    const ids = Object.keys(chat.bots || {}).sort();
    const key = ids.join('|') || 'echo';
    if (!groups.has(key)) groups.set(key, { key, label: ids.map(id => chat.bots[id]).join(' + ') || 'Echo', chats: [] });
    groups.get(key)!.chats.push(chat);
  }
  return [...groups.values()];
}
export const escapeHTML = (value: unknown) => String(value ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]!);
export function activeMention(value: string, caret: number) {
  const prefix = value.slice(0, caret);
  const match = prefix.match(/(?:^|\s)([@#])([^\n@#]*)$/);
  if (!match || (match[1] === '@' && /\s/.test(match[2]))) return null;
  return { type: match[1], query: match[2], start: caret - match[2].length - 1, end: caret };
}
