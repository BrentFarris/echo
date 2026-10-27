import { expect, test } from 'vitest';
import { changedFiles, fileChangesFooter } from './fileChanges';
import { conversationItems } from './conversation';
import type { FileChange, Message } from './types';

const change = (path: string, before?: string, after?: string): FileChange => ({ path, displayPath: path, before, after, operation: !before ? 'created' : !after ? 'deleted' : 'edited' });
const message = (id: string, fileChanges: FileChange[]): Message => ({ id, chatId: 'chat', runId: 'run', botId: 'kaiju', role: 'tool', author: 'Kaiju', content: '', createdAt: '', status: 'completed', fileChanges });

test('summarizes repeated edits and net creates, deletes, replacements and reversions', () => {
  const messages = [message('a', [change('created', undefined, 'a'), change('edited', 'a', 'b'), change('deleted', 'a', 'b'), change('temporary', undefined, 'a'), change('restored', 'a', undefined), change('reverted', 'a', 'b')]), message('b', [change('created', 'a', 'b'), change('edited', 'b', 'c'), change('deleted', 'b', undefined), change('temporary', 'a', undefined), change('restored', undefined, 'b'), change('reverted', 'b', 'a')])];
  expect(changedFiles(messages).map(f => [f.path, f.operation, f.messageId])).toEqual([['created', 'created', 'b'], ['deleted', 'deleted', 'b'], ['edited', 'edited', 'b'], ['restored', 'edited', 'b']]);
  expect(changedFiles(messages)[0].before).toBeUndefined();
});

test('each bot keeps its own changes and assistant text cannot invent files', () => {
  const a = message('a', [change('a/file.go', undefined, 'a')]);
  const b = { ...message('b', [change('b/file.go', undefined, 'a')]), runId: 'other', botId: 'backend' };
  const prose = { ...message('prose', [change('forged', undefined, 'a')]), role: 'assistant', content: 'I edited a file.' };
  const groups = conversationItems([a, b, prose], []).flatMap(item => item.kind === 'response' ? [item.group] : []);
  expect(groups.map(g => changedFiles(g.messages).map(f => f.path))).toEqual([['a/file.go'], ['b/file.go']]);
});

test('footer waits for completion, also shows stopped effects, and safely renders clickable filenames', async () => {
  const group = { key: 'group', author: 'Kaiju', status: 'running', messages: [message('tool', [change('folder/<script>.txt', undefined, 'a')])] };
  const revealed: string[] = [];
  const reveal = async (file: { path: string }) => { revealed.push(file.path); };
  expect(fileChangesFooter(group, reveal)).toBeNull();
  for (const status of ['completed', 'cancelled', 'failed', 'interrupted']) {
    const footer = fileChangesFooter({ ...group, status }, reveal)!;
    expect(footer.querySelector('script')).toBeNull();
    expect(footer.textContent).toContain('folder/<script>.txt');
    expect(footer.textContent).toContain('Created');
    footer.querySelector('button')!.click(); await Promise.resolve();
  }
  expect(revealed).toHaveLength(4);
});
