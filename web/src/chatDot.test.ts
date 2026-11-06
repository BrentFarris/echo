import { expect, test } from 'vitest';
import { chatDotMarkup, chatDotState, chatUnread, hasPendingQuestion } from './chatDot';
import type { Chat, PendingQuestion, Run } from './types';

const chat = (overrides: Partial<Chat> = {}): Chat => ({
  id: 'chat1', title: 'Chat', bots: {}, generation: { endpointId: '', model: '', thinking: '', temperature: .6 }, updatedAt: '', ...overrides,
});
const run = (overrides: Partial<Run> = {}): Run => ({ id: 'run1', chatId: 'chat1', botId: 'bot1', status: 'running', ...overrides });
const question = (overrides: Partial<PendingQuestion> = {}): PendingQuestion => ({ chatId: 'chat1', messageId: 'message1', botId: 'bot1', ...overrides });
const read = chat({ lastReadAt: '2026-01-01T00:00:00Z' });
const finished = run({ status: 'completed', updatedAt: '2026-01-02T00:00:00Z' });

test('deleted chats never show unread, busy, or question indicators', () => {
  const deleted = chat({ deletedAt: '2026-01-03T00:00:00Z', waitingForGroup: true });
  expect(chatUnread(deleted, [finished], '', false)).toBe(false);
  expect(chatDotState(deleted, [run(), finished], [question()], '', false)).toBe('');
});

test('pending questions take priority over busy and unread states, even in the selected chat', () => {
  for (const runs of [[run()], [finished], [run(), finished], []]) {
    expect(chatDotState(read, runs, [question()], '', false)).toBe('question');
    expect(chatDotState(read, runs, [question()], 'chat1', true)).toBe('question');
  }
  expect(chatDotState(chat(), [], [question()], '', false)).toBe('question');
});

test.each(['running', 'waiting', 'queued'])('%s runs remain yellow without a pending question', status => {
  expect(chatDotState(read, [run({ status }), finished], [], 'chat1', true)).toBe('busy');
});

test('clearing the last question restores busy, unread, or no dot according to the chat state', () => {
  expect(chatDotState(read, [run()], [], '', false)).toBe('busy');
  expect(chatDotState(read, [finished], [], '', false)).toBe('done');
  expect(chatDotState(read, [finished], [], 'chat1', true)).toBe('');
  expect(chatDotState(chat({ lastReadAt: '2026-01-03T00:00:00Z' }), [finished], [], '', false)).toBe('');
  expect(chatDotState(chat(), [finished], [], '', false)).toBe('');
});

test('a remaining question keeps the chat blue without affecting other chats or bots', () => {
  const questions = [question(), question({ messageId: 'message2', botId: 'bot2' })];
  expect(hasPendingQuestion(questions, 'chat1', 'bot1')).toBe(true);
  expect(hasPendingQuestion(questions, 'chat1', 'bot2')).toBe(true);
  expect(hasPendingQuestion(questions, 'other', 'bot1')).toBe(false);
  expect(hasPendingQuestion(questions, 'chat1', 'other')).toBe(false);
  const remaining = questions.slice(1);
  expect(chatDotState(read, [], remaining, '', false)).toBe('question');
  expect(hasPendingQuestion(remaining, 'chat1', 'bot1')).toBe(false);
  expect(hasPendingQuestion(remaining, 'chat1', 'bot2')).toBe(true);
});

test('a manual unread mark applies even to the open chat and respects status priority', () => {
  const marked = chat({ markedUnread: true });
  expect(chatUnread(marked, [], marked.id, true)).toBe(true);
  expect(chatDotState(marked, [], [], marked.id, true)).toBe('done');
  expect(chatDotState(marked, [run()], [], marked.id, true)).toBe('busy');
  expect(chatDotState(marked, [run()], [question()], marked.id, true)).toBe('question');
  expect(chatDotState({ ...marked, deletedAt: '2026-01-03T00:00:00Z' }, [], [], '', false)).toBe('');
});

test('machine-qualified chat and bot IDs keep questions isolated', () => {
  const node = 'a'.repeat(32), remoteChat = `${node}:chat1`, remoteBot = `${node}:bot1`;
  const questions = [question({ chatId: remoteChat, botId: remoteBot })];
  expect(chatDotState(chat({ id: remoteChat }), [], questions, '', false)).toBe('question');
  expect(chatDotState(read, [], questions, '', false)).toBe('');
  expect(hasPendingQuestion(questions, remoteChat, remoteBot)).toBe(true);
  expect(hasPendingQuestion(questions, remoteChat, 'bot1')).toBe(false);
});

test('older state without questions preserves the existing unread behavior', () => {
  expect(chatDotState(read, [finished], undefined, '', false)).toBe('done');
  expect(hasPendingQuestion(undefined, 'chat1', 'bot1')).toBe(false);
  expect(chatUnread(read, [finished], 'chat1', true)).toBe(false);
  expect(chatUnread(read, [finished], 'chat1', false)).toBe(true);
  expect(chatUnread(chat(), [], '', false)).toBe(true);
});

test('dots expose their meaning to assistive technology and tooltips', () => {
  const element = document.createElement('div');
  element.innerHTML = chatDotMarkup('question');
  const dot = element.querySelector('.chat-dot.question')!;
  expect(dot.getAttribute('role')).toBe('img');
  expect(dot.getAttribute('aria-label')).toBe('Waiting for your answer');
  expect(dot.getAttribute('title')).toBe('Waiting for your answer');
  expect(chatDotMarkup('busy')).toContain('chat-dot busy');
  expect(chatDotMarkup('done')).toContain('chat-dot done');
  expect(chatDotMarkup('')).toBe('');
});
