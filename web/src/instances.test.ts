import { beforeEach, describe, expect, it, vi } from 'vitest';
import { InstanceClient, chatMachineIds, combineState, ownedEvent, ownedMessage, ownedState, resourceAPI, resourceID, route, attachmentURL } from './instances';
import type { Message, State } from './types';

const a = 'a'.repeat(32), b = 'b'.repeat(32);
const empty = (): State => ({ bots: [], chats: [], endpoints: [], plugins: [], appDock: [], schedules: [], occurrences: [], runs: [] });
beforeEach(() => { vi.restoreAllMocks(); combineState(empty(), { node: { nodeId: a, name: 'desktop', epoch: '1', version: 1 }, enabled: true, peers: [], instances: [{ node: { nodeId: b, name: 'linux', epoch: '2', version: 1 }, online: true, state: empty() }] }); });
describe('instance ownership', () => {
  it('counts participants and runs, including a chat-home Echo omitted from the bot list', () => {
    const state = empty();
    const chat = { id: 'mixed', bots: { [`${a}:bot`]: 'desktop.bot' }, title: '', updatedAt: '', generation: { endpointId: '', model: '', thinking: '', temperature: 0 } };
    state.chats.push(chat, { ...chat, id: 'empty', bots: {} }, { ...chat, id: 'pending', bots: {}, waitingForGroup: true });
    state.runs.push({ id: 'one', chatId: 'mixed', botId: 'echo', status: 'completed' },
      { id: 'two', chatId: 'mixed', botId: `${a}:bot`, status: 'completed' });
    const owned = ownedState(b, state);
    const machines = chatMachineIds(owned.chats, owned.runs);
    expect(machines.get(`${b}:mixed`)).toEqual(new Set([a, b]));
    expect(machines.get(`${b}:empty`)).toEqual(new Set([b]));
    expect(machines.get(`${b}:pending`)).toEqual(new Set());
  });
  it('owns bot override connections and localizes them when saving to their machine', async () => {
    const bot = { id: 'bot', handle: 'worker', plugins: {}, modelOverride: { endpointId: 'cli', model: 'fixed' }, thinkingOverride: 'high' } as State['bots'][number];
    const owned = ownedState(b, { ...empty(), bots: [bot] }).bots[0];
    expect(owned.modelOverride?.endpointId).toBe(`${b}:cli`);
    const fetcher = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('{}'));
    await resourceAPI('/bots', 'POST', owned, b);
    expect(fetcher.mock.calls[0][0]).toBe(`/api/v1/swarm/instances/${b}/bots`);
    expect(JSON.parse(fetcher.mock.calls[0][1]!.body as string).modelOverride).toEqual(bot.modelOverride);
    await expect(resourceAPI('/bots', 'POST', { modelOverride: { endpointId: `${b}:cli` } }, a)).rejects.toThrow('different machine');
  });
  it('combines pending questions with machine-qualified IDs and accepts older state', () => {
    const question = { chatId: 'chat', messageId: 'question', botId: 'echo' };
    const local = { ...empty(), questions: [question] }, remote = { ...empty(), questions: [question] };
    const combined = combineState(local, { node: { nodeId: a, name: 'desktop', epoch: '1', version: 1 }, enabled: true, peers: [], instances: [{ node: { nodeId: b, name: 'linux', epoch: '2', version: 1 }, online: true, state: remote }] });
    expect(combined.questions).toEqual([question, { chatId: `${b}:chat`, messageId: `${b}:question`, botId: `${b}:echo` }]);
    expect(ownedState(b, empty()).questions).toEqual([]);
    expect(ownedState(b, { ...empty(), questions: [{ ...question, botId: `${a}:echo` }] }).questions![0].botId).toBe('echo');
  });
  it('keeps run errors linked to their prompt in both remote snapshots and live events', () => {
    const run = { id: 'run', chatId: 'chat', botId: 'echo', userMessageId: 'prompt', status: 'cancelled', error: 'context canceled' };
    const expected = { id: `${b}:run`, chatId: `${b}:chat`, botId: `${b}:echo`, userMessageId: `${b}:prompt` };
    expect(ownedState(b, { ...empty(), runs: [run] }).runs[0]).toMatchObject(expected);
    expect(ownedEvent(b, { sequence: 1, type: 'run', payload: run }).payload).toMatchObject(expected);
  });
  it('retains the exact stored message ID when routing mutations to the chat home', () => {
    const message = ownedMessage(a, { id: `${b}:reply`, chatId: 'home', nodeId: b } as Message);
    expect(message.storedId).toBe(`${b}:reply`);
    expect(route(`/chats/${message.chatId}/messages/${encodeURIComponent(message.storedId!)}`)).toMatchObject({ path: `/chats/home/messages/${b}%3Areply` });
    expect(ownedMessage(b, { id: 'reply', chatId: 'remote' } as Message).storedId).toBe('reply');
  });
  it('preserves local IDs and isolates clients across machine selection', () => {
    const linux = new InstanceClient(b), local = new InstanceClient(a);
    expect(resourceID(a, 'echo')).toBe('echo'); expect(resourceID(b, 'echo')).toBe(`${b}:echo`);
    expect(linux.url('/bots')).toBe(`/api/v1/swarm/instances/${b}/bots`); expect(local.url('/bots')).toBe('/api/v1/bots');
    expect(route(`/bots/${b}:echo/editor/files?path=../file`)).toMatchObject({ path: '/bots/echo/editor/files?path=..%2Ffile' });
    expect(() => linux.url('/../session')).toThrow();
  });
  it('keeps stable references to third machines when owning remote state', () => {
    const state = empty(); state.chats.push({ id: 'chat', title: 'shared', bots: { [`${a}:echo`]: 'desktop.echo', echo: 'linux.echo' }, updatedAt: '', generation: { endpointId: 'model', model: 'test', thinking: '', temperature: .6 } });
    const owned = ownedState(b, state).chats[0];
    expect(owned.id).toBe(`${b}:chat`); expect(Object.keys(owned.bots)).toEqual(['echo', `${b}:echo`]);
    expect(owned.generation.endpointId).toBe(`${b}:model`);
  });
  it('routes native app requests and uploads to their permanent owner', async () => {
    const fetcher = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('{}', { headers: { 'Content-Type': 'application/json' } }));
    await resourceAPI(`/bots/${b}:echo/editor/settings`, 'PUT', { botId: `${b}:echo`, generation: { endpointId: `${b}:model` } });
    expect(fetcher.mock.calls[0][0]).toBe(`/api/v1/swarm/instances/${b}/bots/echo/editor/settings`);
    expect(JSON.parse(fetcher.mock.calls[0][1]!.body as string)).toEqual({ botId: 'echo', generation: { endpointId: 'model' } });
  });
  it('keeps generated attachments on the producing machine', () => {
    const message = ownedMessage(a, { id: 'm', chatId: 'home', botId: `${b}:echo`, attachments: [{ id: 'image', chatId: 'home', originChatId: 'worker-chat', nodeId: b, name: 'image', mediaType: 'image/png', size: 1 }] } as Message);
    expect(attachmentURL(message, message.attachments![0])).toBe(`/api/v1/swarm/instances/${b}/chats/worker-chat/attachments/image`);
  });
});
