import { beforeEach, expect, test } from 'vitest';
import { ReferenceInput } from './referenceEditor';
import './referenceEditor';
import { activeSlash, applySlash, matchingCommands, planCommandText, slashEdit } from './slashCommands';
import type { SlashCommand } from './types';

const plan: SlashCommand = { id: 'plan', name: 'plan', description: 'Plan', builtin: true, enabled: true, action: { type: 'text', position: 'append', text: planCommandText } };
let input: ReferenceInput;
beforeEach(() => { document.body.innerHTML = '<reference-input contenteditable="true"></reference-input>'; input = document.querySelector('reference-input')!; input.focus(); });

test('matches slash tokens anywhere in prose but never paths, code, or chips', () => {
  for (const text of ['/', '/pl', 'Please /plan', 'first\n/pl']) expect(activeSlash({ text, references: [] }, text.length)).not.toBeNull();
  for (const text of ['/plan ', 'https://site/plan', '/tmp/file', 'C:/plan', '`/plan', '```ts\n/plan', '~~~\n/plan', '\\ /foo.bar', 'word/plan']) expect(activeSlash({ text, references: [] }, text.length)).toBeNull();
  expect(activeSlash({ text: '`code` /pl', references: [] }, 10)?.query).toBe('pl');
  expect(activeSlash({ text: '/plan/file', references: [] }, 5)).toBeNull();
  expect(activeSlash({ text: '/plan later', references: [] }, 3)).toEqual({ start: 0, end: 5, query: 'pl' });
  expect(activeSlash({ text: '/plan', references: [{ type: 'file', range: { start: 0, end: 5 } }] }, 5)).toBeNull();
  expect(activeSlash({ text: '/plan', references: [] }, 0, 5)).toBeNull();
});

test('filters enabled commands and ranks exact, prefix, then substring', () => {
  const commands = ['replan', 'planner', 'PLAN'].map((name, i) => ({ ...plan, id: String(i), name }));
  expect(matchingCommands([...commands, { ...plan, enabled: false }], 'plan').map(c => c.name)).toEqual(['PLAN', 'planner', 'replan']);
});

test.each(['/plan', '/plan tail', 'head /plan', '🙂 first\n/plan tail'])('plan preserves caret and exact text in %s with one undo/redo', text => {
  input.value = text;
  const start = text.indexOf('/plan'); input.setSelectionRange(start + 5);
  const match = activeSlash(input.snapshot(), input.selectionStart)!;
  applySlash(input, match, plan);
  expect(input.value).toBe(text.replace('/plan', '') + planCommandText);
  expect(input.selectionStart).toBe(start);
  input.dispatchEvent(new KeyboardEvent('keydown', { key: 'z', ctrlKey: true, bubbles: true }));
  expect(input.value).toBe(text); expect(input.selectionStart).toBe(start + 5);
  input.dispatchEvent(new KeyboardEvent('keydown', { key: 'z', ctrlKey: true, shiftKey: true, bubbles: true }));
  expect(input.value).toBe(text.replace('/plan', '') + planCommandText); expect(input.selectionStart).toBe(start);
});

test('reference ranges, inserted markup, and repeated invocation remain literal', () => {
  input.value = 'head /plan ';
  input.replace('#file', input.value.length, input.value.length, { type: 'file', path: '/file', text: 'file' });
  input.setSelectionRange(10); applySlash(input, activeSlash(input.snapshot(), 10)!, plan);
  expect(input.snapshot().references[0].range).toEqual({ start: 6, end: 11 });
  expect(input.querySelectorAll('[data-reference]')).toHaveLength(1);
  input.replace('/plan', 5, 5); applySlash(input, activeSlash(input.snapshot(), 10)!, plan);
  expect(input.value).toBe('head  #file' + planCommandText.repeat(2));
  const draft = { text: 'a /x b', references: [] };
  expect(slashEdit(draft, { start: 2, end: 4, query: 'x' }, { type: 'text', position: 'insert', text: '<script>/plan</script>' })).toEqual({ draft: { text: 'a <script>/plan</script> b', references: [] }, caret: 24 });
  expect(slashEdit(draft, { start: 2, end: 4, query: 'x' }, { type: 'text', position: 'prepend', text: 'pre ' }).caret).toBe(6);
});
