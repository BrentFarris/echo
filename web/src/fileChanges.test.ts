import { expect, test, vi } from 'vitest';
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

test('file actions describe the editor destination when enabled and the native fallback otherwise', () => {
  const group = { key: 'group', author: 'Kaiju', status: 'completed', messages: [message('tool', [change('created.txt', undefined, 'a'), change('deleted.txt', 'a', undefined)])] };
  for (const enabled of [true, false]) {
    const buttons = fileChangesFooter(group, async () => {}, enabled)!.querySelectorAll('button');
    expect(buttons[0].getAttribute('aria-label')).toContain(enabled ? 'Open in code editor:' : 'Show in File Explorer:');
    expect(buttons[1].getAttribute('aria-label')).toContain(enabled ? 'Show containing folder in code editor:' : 'Show containing folder in File Explorer:');
  }
});

test('refreshes retain the file list, scroll position, focus and current open action', async () => {
  const group = { key: 'group', author: 'Kaiju', status: 'completed', messages: [message('tool', [change('edited.txt', 'a', 'b')])] };
  const firstOpen = vi.fn(async () => {}), latestOpen = vi.fn(async () => {});
  const footer = fileChangesFooter(group, firstOpen)!;
  document.body.append(footer);
  try {
    const list = footer.querySelector('ul')!, button = footer.querySelector('button')!;
    list.scrollTop = 150; button.focus();
    const refreshed = fileChangesFooter(structuredClone(group), latestOpen, false, footer)!;
    expect(refreshed).toBe(footer);
    expect(refreshed.querySelector('ul')).toBe(list);
    expect(refreshed.querySelector('button')).toBe(button);
    expect(list.scrollTop).toBe(150);
    expect(document.activeElement).toBe(button);
    button.click(); await Promise.resolve();
    expect(firstOpen).not.toHaveBeenCalled();
    expect(latestOpen).toHaveBeenCalledWith(expect.objectContaining({ path: 'edited.txt', messageId: 'tool' }));
  } finally { footer.remove(); }
});

test('retained footers update changed files, notices and editor actions without replacing the list', async () => {
  const group = { key: 'group', author: 'Kaiju', status: 'completed', messages: [message('first-tool', [change('edited.txt', 'a', 'b')])] };
  const open = vi.fn(async () => {}), footer = fileChangesFooter(group, open)!;
  const list = footer.querySelector('ul')!;
  list.scrollTop = 120;
  group.messages.push({ ...message('latest-tool', [change('created.txt', undefined, 'c'), change('edited.txt', 'b', 'c')]), fileChangesNotice: 'Some changes are unavailable.' });
  expect(fileChangesFooter(group, open, true, footer)).toBe(footer);
  expect(footer.querySelector('ul')).toBe(list);
  expect(list.scrollTop).toBe(120);
  expect(footer.textContent).toContain('2 files');
  expect(footer.textContent).toContain('Some changes are unavailable.');
  const button = footer.querySelectorAll('button')[1];
  expect(button.getAttribute('aria-label')).toContain('Open in code editor:');
  button.click(); await Promise.resolve();
  expect(open).toHaveBeenCalledWith(expect.objectContaining({ path: 'edited.txt', after: 'c', messageId: 'latest-tool' }));
  group.messages[1].fileChangesNotice = undefined;
  fileChangesFooter(group, open, false, footer);
  expect(footer.querySelector('.file-changes-note')).toBeNull();
  expect(footer.querySelector('button')!.getAttribute('aria-label')).toContain('Show in File Explorer:');
  expect(fileChangesFooter({ ...group, status: 'running' }, open, false, footer)).toBeNull();
});
