import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { InlineGeneration } from './inlineGeneration';
import { combineState } from '../instances';
import { streamInlineChat } from './inlineChatSession';
import type { State } from '../types';

const a = 'a'.repeat(32), b = 'b'.repeat(32);
const state = (): State => ({ bots: [], chats: [], endpoints: [], plugins: [], appDock: [], schedules: [], occurrences: [], runs: [] });
beforeEach(() => {
  localStorage.clear();
  combineState(state(), { node: { nodeId: a, name: 'desktop', epoch: '1', version: 1 }, enabled: true, peers: [], instances: [{ node: { nodeId: b, name: 'remote', epoch: '1', version: 1 }, online: true, state: state() }] });
});
afterEach(() => vi.restoreAllMocks());

it('seeds once and persists preferences separately for each bot and machine', async () => {
  const chat = { endpointId: 'model', model: 'initial', thinking: '', temperature: .6 };
  vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response(JSON.stringify({ endpoints: [{ id: 'model', name: 'Model', model: 'default', url: 'http://localhost' }] })));
  const first = new InlineGeneration('bot', { generationFor: () => chat }); await first.refresh();
  chat.model = 'main chat changed';
  expect(new InlineGeneration('bot', { generationFor: () => chat }).value.model).toBe('initial');
  first.save({ ...first.value, model: 'editor model', thinking: 'high', temperature: .2 });
  expect(chat.model).toBe('main chat changed');
  expect(new InlineGeneration('bot').value).toEqual(first.value);
  expect(new InlineGeneration('second', { generationFor: () => chat }).value.model).toBe('main chat changed');
  const remote = new InlineGeneration(`${b}:bot`); await remote.refresh();
  expect(remote.value.endpointId).toBe(`${b}:model`);
  expect(remote.value.model).toBe('default');
});

it('routes streamed requests to their bot owner and strips only that endpoint namespace', async () => {
  const fetcher = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => new Response('{"type":"status","text":"Working"}\n{"type":"done"}\n'));
  const request = { message: 'edit', file: { title: 'new', content: '', selections: [] }, references: [], history: [], generation: { endpointId: `${b}:model`, model: 'chosen', thinking: 'max', temperature: 0 } };
  const receive = vi.fn(); await streamInlineChat(`${b}:bot`, request, new AbortController().signal, receive);
  expect(fetcher.mock.calls[0][0]).toBe(`/api/v1/swarm/instances/${b}/plugins/monaco-editor/bots/bot/inline-chat`);
  expect(JSON.parse(fetcher.mock.calls[0][1]!.body as string).generation).toEqual({ ...request.generation, endpointId: 'model' });
  expect(receive).toHaveBeenCalledWith({ type: 'status', text: 'Working' });
  await expect(streamInlineChat('local-bot', request, new AbortController().signal, receive)).rejects.toThrow('different machine');
  expect(fetcher).toHaveBeenCalledTimes(1);
});
