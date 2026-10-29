import { expect, test } from 'vitest';
import { botColor, threadIdentityMarkup } from './botIdentity';
import { combineState } from './instances';
import type { State } from './types';

test('bot colors survive local/remote ownership changes and distinguish machines', () => {
  const a = 'a'.repeat(32), b = 'b'.repeat(32);
  const state: State = { bots: [], chats: [], endpoints: [], plugins: [], schedules: [], occurrences: [], runs: [], appDock: [] };
  const viewFrom = (nodeId: string) => combineState(state, { node: { nodeId, name: 'machine', epoch: '1', version: 1 }, enabled: true, peers: [], instances: [] });
  viewFrom(a);
  const color = botColor('worker');
  expect(botColor(`${a}:worker`)).toBe(color);
  expect(botColor('another-worker')).not.toBe(color);
  expect(botColor(`${b}:worker`)).not.toBe(color);
  viewFrom(b);
  expect(botColor(`${a}:worker`)).toBe(color);
});

test('shared thread identity leads with the selected bot, bounds avatars and escapes historical names', () => {
  const root = document.createElement('div');
  const bots = [{ id: 'one', name: 'One' }, { id: 'two', name: 'Two' }, { id: 'three', name: '<img src=x onerror=alert(1)>' }, { id: 'four', name: 'Build Bot' }];
  root.innerHTML = threadIdentityMarkup(bots, 'four');
  expect(root.querySelector('.brand-name')?.textContent).toBe('Build Bot');
  expect(root.querySelector('[data-bot-avatar]')?.getAttribute('data-bot-avatar')).toBe('four');
  expect(root.querySelector('[data-bot-avatar]')?.textContent).toBe('BB');
  expect(root.querySelectorAll('[data-bot-avatar]')).toHaveLength(3);
  expect(root.querySelector('.bot-avatar-overflow')?.textContent).toBe('+1');
  expect(root.querySelector('.brand-peers')?.textContent).toBe('with One, Two, <img src=x onerror=alert(1)>');
  expect(root.querySelector('img')).toBeNull();
  const color = root.querySelector('[data-bot-avatar=four]')?.getAttribute('style');
  root.innerHTML = threadIdentityMarkup([{ id: 'four', name: 'Renamed' }]);
  expect(root.querySelector('[data-bot-avatar=four]')?.getAttribute('style')).toBe(color);
  expect(root.querySelector('.brand-peers')).toBeNull();
});
