// @vitest-environment jsdom
import { beforeEach, expect, test } from 'vitest';
import { prefixBotMention } from './composer';
import { ReferenceInput } from './referenceEditor';
import type { Bot } from './types';

const bots = [{ id: 'a', handle: 'alpha' }, { id: 'b', handle: 'linux.beta' }] as Bot[];
let input: ReferenceInput;
beforeEach(() => {
  input = new ReferenceInput();
  input.setAttribute('contenteditable', 'true');
  document.body.replaceChildren(input);
  input.bots = () => bots;
  input.focus();
});

test('prefixes an empty composer with a bot chip and places the caret after it', () => {
  prefixBotMention(input, bots[1]);
  expect(input.value).toBe('@linux.beta ');
  expect(input.selectionStart).toBe(12);
  expect(input.snapshot().references).toEqual([{ type: 'bot', botId: 'b', text: '@linux.beta', range: { start: 0, end: 11 } }]);
});

test('switching bots replaces the automatic prefix and preserves text, file ranges and selection', () => {
  input.value = '🙂 inspect ';
  input.replace('#file ', input.value.length, input.value.length, { type: 'file', text: 'file', path: '/file' });
  input.setSelectionRange(4, 8);
  const automatic = prefixBotMention(input, bots[0]);
  prefixBotMention(input, bots[1], automatic);
  expect(input.value).toBe('@linux.beta 🙂 inspect #file ');
  expect(input.snapshot().references[1]).toMatchObject({ type: 'file', path: '/file', range: { start: 23, end: 28 } });
  expect([input.selectionStart, input.selectionEnd]).toEqual([16, 20]);
});

test('reselecting a thread does not duplicate its mention', () => {
  const automatic = prefixBotMention(input, bots[0]);
  expect(prefixBotMention(input, bots[0], automatic)).toEqual(automatic);
  expect(input.value).toBe('@alpha ');
});

test('preserves user-authored recipients when changing the automatic prefix', () => {
  input.replace('@alpha compare', 0, 0, { type: 'bot', botId: 'a', text: '@alpha' });
  expect(prefixBotMention(input, bots[0])).toBeUndefined();
  const automatic = prefixBotMention(input, bots[1]);
  expect(input.value).toBe('@linux.beta @alpha compare');
  prefixBotMention(input, bots[0], automatic);
  expect(input.value).toBe('@alpha compare');
});

test('does not replace draft text after the automatic prefix has been deleted', () => {
  const automatic = prefixBotMention(input, bots[0]);
  input.value = 'Keep this draft';
  prefixBotMention(input, bots[1], automatic);
  expect(input.value).toBe('@linux.beta Keep this draft');
});
