import { groupChats, activeMention } from './helpers';
import { test, expect } from 'vitest';
import type { Chat } from './types';
test('every chat remains listed once in its bot combination', () => {
  const chats = Array.from({ length: 1000 }, (_, i) => ({ id: String(i), bots: i % 2 ? { a: 'kaiju', b: 'backend' } : {}, title: 'Chat' } as Chat));
  const grouped = groupChats(chats);
  expect(grouped.flatMap(g => g.chats)).toHaveLength(1000);
  expect(grouped.find(g => g.key === 'a|b')?.label).toBe('kaiju + backend');
});
test('mentions work in the middle of a message', () => {
  expect(activeMention('Please ask @kai', 15)?.query).toBe('kai');
  expect(activeMention('Open #map_editor', 16)?.type).toBe('#');
  expect(activeMention('a@example.com', 13)).toBeNull();
});
