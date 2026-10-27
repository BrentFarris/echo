import { expect, test } from 'vitest';
import { conversationItems } from './conversation';
import type { Message, Run } from './types';

const message = (id: string, extra: Partial<Message> = {}): Message => ({ id, chatId: 'chat', runId: 'run', botId: 'bot', author: 'Kaiju', role: 'assistant', status: 'completed', content: '', createdAt: '', ...extra });
const run = (status: string, id = 'run'): Run => ({ id, chatId: 'chat', botId: 'bot', status });
const response = (messages: Message[], runs: Run[]) => conversationItems(messages, runs).flatMap(item => item.kind === 'response' ? [item.group] : []);

test('folds reasoning, progress and tools behind the final answer without changing the transcript', () => {
  const messages = [message('user', { runId: undefined, role: 'user', content: 'Build it' }), message('progress', { reasoning: 'Inspect the folder', content: 'Working', toolCalls: [{}] }), message('tool', { role: 'tool', content: 'Result' }), message('final', { content: 'Done', reasoning: 'Check the result' })];
  const before = JSON.stringify(messages), items = conversationItems(messages, [run('completed')]);
  expect(items).toHaveLength(2); expect(items[0].kind).toBe('message');
  const group = response(messages, [run('completed')])[0];
  expect(group.final?.id).toBe('final'); expect(group.messages.map(m => m.id)).toEqual(['progress', 'tool', 'final']);
  expect(JSON.stringify(messages)).toBe(before);
});

test('keeps independent bots attributed and only folds the finished bot', () => {
  const messages = [message('a-work', { content: 'Progress', toolCalls: [{}] }), message('b-work', { runId: 'b', botId: 'b', author: 'Backend', content: 'Still working', status: 'running' }), message('a-final', { content: 'Kaiju finished' })];
  const groups = response(messages, [run('completed'), run('running', 'b')]);
  expect(groups.map(g => g.author)).toEqual(['Kaiju', 'Backend']);
  expect(groups[0].final?.content).toBe('Kaiju finished'); expect(groups[1].final).toBeUndefined();
  expect(groups[0].messages.map(m => m.id)).toEqual(['a-work', 'a-final']);
});

test.each(['running', 'waiting', 'queued', 'failed', 'cancelled', 'interrupted'])('%s runs do not hide unfinished work', status => {
  expect(response([message('result', { content: 'Partial result' })], [run(status)])[0].final).toBeUndefined();
});

test('a resumed handoff unfolds until the replacement final reply completes', () => {
  const messages = [message('first-final', { content: 'Initial result' }), message('handoff', { role: 'user', author: 'Bot handoff', content: 'Please revise' }), message('new-work', { status: 'running', content: 'Revising' })];
  let groups = response(messages, [run('running')]);
  expect(groups).toHaveLength(1); expect(groups[0].final).toBeUndefined();
  messages[2].status = 'completed'; messages.push(message('new-final', { content: 'Revised result' }));
  groups = response(messages, [run('completed')]);
  expect(groups[0].final?.id).toBe('new-final'); expect(groups[0].messages).toHaveLength(4);
});

test('never treats a completed tool call or empty answer as the final reply', () => {
  for (const last of [message('call', { content: 'Calling a tool', toolCalls: [{}] }), message('result', { role: 'tool', content: 'Tool result' }), message('empty')]) {
    expect(response([last], [run('completed')])[0].final).toBeUndefined();
  }
});

test('reconstructs completed history without a surviving run record and separates user turns', () => {
  const messages = [message('first', { content: 'Old result' }), message('user', { role: 'user', runId: undefined, content: 'Another question' }), message('second', { content: 'New result' })];
  const groups = response(messages, []);
  expect(groups).toHaveLength(2); expect(groups[0].key).not.toBe(groups[1].key);
  expect(groups.map(g => g.final?.id)).toEqual(['first', 'second']);
});
