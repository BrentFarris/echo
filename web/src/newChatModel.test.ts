import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { combineState } from './instances';
import { mountNewChatModel, newChatGeneration, newChatGenerations, readNewChatModel, saveNewChatModel } from './newChatModel';
import type { Endpoint, State } from './types';

const local = 'a'.repeat(32), remote = 'b'.repeat(32);
const endpoint = (id: string, nodeId = local, extra: Partial<Endpoint> = {}): Endpoint => ({ id, nodeId, name: id, model: 'configured-model', url: 'http://localhost/v1', cloud: false, contextTokens: 32768, compact: true, ...extra });
const endpoints = [endpoint('first'), endpoint('preferred', local, { temperature: .3, reasoningEffort: 'high' }), endpoint(`${remote}:preferred`, remote)];
beforeEach(() => {
  localStorage.clear();
  combineState({ bots: [], chats: [], endpoints: [], plugins: [], appDock: [], schedules: [], occurrences: [], runs: [] } as State, { node: { nodeId: local, name: 'Local', epoch: '', version: 1 }, enabled: false, peers: [], instances: [] });
});
afterEach(() => { document.body.replaceChildren(); vi.restoreAllMocks(); localStorage.clear(); });

it('stores local connection IDs and keeps custom model choices isolated by machine', () => {
  saveNewChatModel(local, { endpointId: 'preferred', model: ' custom-local ' });
  saveNewChatModel(remote, { endpointId: `${remote}:preferred`, model: 'custom-remote' });
  expect(readNewChatModel(local)).toEqual({ endpointId: 'preferred', model: 'custom-local' });
  expect(readNewChatModel(remote)).toEqual({ endpointId: 'preferred', model: 'custom-remote' });
  expect(newChatGenerations([local, remote], endpoints)).toEqual({
    [local]: { endpointId: 'preferred', model: 'custom-local', temperature: .3, thinking: '' },
    [remote]: { endpointId: `${remote}:preferred`, model: 'custom-remote', temperature: .6, thinking: '' },
  });
});

it('follows current connection defaults when the identifier is blank and supports clearing', () => {
  saveNewChatModel(local, { endpointId: 'preferred', model: '  ' });
  expect(readNewChatModel(local)).toEqual({ endpointId: 'preferred' });
  expect(newChatGeneration(local, endpoints).model).toBe('configured-model');
  expect(newChatGeneration(local, endpoints.map(e => ({ ...e, model: 'updated-model' }))).model).toBe('updated-model');
  saveNewChatModel(local);
  expect(readNewChatModel(local)).toBeUndefined();
  expect(newChatGeneration(local, endpoints).endpointId).toBe('first');
});

it('falls back for missing connections without deleting the saved choice', () => {
  saveNewChatModel(local, { endpointId: 'missing', model: 'saved-model' });
  expect(newChatGeneration(local, endpoints)).toEqual({ endpointId: 'first', model: 'configured-model', thinking: '', temperature: .6 });
  expect(readNewChatModel(local)?.endpointId).toBe('missing');
  expect(newChatGeneration(local, [...endpoints, endpoint('missing')]).model).toBe('saved-model');
  expect(newChatGeneration(local, [])).toEqual({ endpointId: '', model: '', thinking: '', temperature: .6 });
});

it.each(['{', 'null', '[]', '"text"', '{"endpointId":1}', '{"endpointId":""}', '{"endpointId":"first","model":false}'])('ignores malformed preferences: %s', value => {
  localStorage.setItem(`echo2:new-chat-model:${local}`, value);
  expect(readNewChatModel(local)).toBeUndefined();
  expect(newChatGeneration(local, endpoints).endpointId).toBe('first');
});

it('keeps creation usable when browser storage is unavailable', () => {
  vi.spyOn(Storage.prototype, 'getItem').mockImplementation(() => { throw new Error('blocked'); });
  vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('blocked'); });
  vi.spyOn(Storage.prototype, 'removeItem').mockImplementation(() => { throw new Error('blocked'); });
  expect(() => { saveNewChatModel(local, { endpointId: 'preferred' }); saveNewChatModel(local); }).not.toThrow();
  expect(newChatGeneration(local, endpoints).endpointId).toBe('first');
});

function mount(choices = endpoints, discover = vi.fn<(endpoint: Endpoint) => Promise<string[]>>().mockResolvedValue(['discovered-model'])) {
  const root = document.createElement('section'); document.body.append(root);
  mountNewChatModel(root, local, choices, discover);
  return { root, connection: root.querySelector('select')!, identifier: root.querySelector('input')!, discover };
}

it('autosaves controls and leaves existing chat selections independent', async () => {
  const existing = newChatGeneration(local, endpoints);
  const { root, connection, identifier } = mount();
  expect(identifier.disabled).toBe(true);
  connection.value = 'preferred'; connection.dispatchEvent(new Event('change'));
  expect(identifier.disabled).toBe(false);
  await vi.waitFor(() => expect(root.querySelector('datalist option')?.getAttribute('value')).toBe('discovered-model'));
  identifier.value = 'manual-model'; identifier.dispatchEvent(new Event('input'));
  expect(newChatGeneration(local, endpoints).model).toBe('manual-model');
  expect(existing.endpointId).toBe('first');
  connection.value = ''; connection.dispatchEvent(new Event('change'));
  expect(identifier.value).toBe(''); expect(identifier.disabled).toBe(true);
  expect(readNewChatModel(local)).toBeUndefined();
});

it('displays unavailable choices and external-agent choices without discovery', () => {
  saveNewChatModel(local, { endpointId: 'gone', model: 'saved' });
  const { root, connection, identifier, discover } = mount([...endpoints, endpoint('cli', local, { kind: 'codex-cli', model: '' }), endpoint('cloud', local, { cloud: true })]);
  expect(connection.selectedOptions[0].textContent).toContain('Unavailable connection');
  expect(identifier.value).toBe('saved');
  expect(root.querySelector<HTMLElement>('[data-default-model-unavailable]')!.hidden).toBe(false);
  expect([...connection.options].some(option => option.value === 'cloud')).toBe(true);
  connection.value = 'cli'; connection.dispatchEvent(new Event('change'));
  expect(identifier.placeholder).toBe('CLI default');
  expect(discover).not.toHaveBeenCalled();
  expect(newChatGeneration(local, [...endpoints, endpoint('cli', local, { kind: 'codex-cli', model: '' })]).model).toBe('');
  expect(root.querySelector<HTMLElement>('[data-default-model-external]')!.hidden).toBe(false);
});

it('ignores stale discovery results and tolerates discovery failures', async () => {
  let resolveFirst!: (models: string[]) => void;
  const discover = vi.fn<(endpoint: Endpoint) => Promise<string[]>>()
    .mockImplementationOnce(() => new Promise(resolve => { resolveFirst = resolve; }))
    .mockRejectedValueOnce(new Error('offline'));
  const { root, connection, identifier } = mount(endpoints, discover);
  connection.value = 'first'; connection.dispatchEvent(new Event('change'));
  connection.value = 'preferred'; connection.dispatchEvent(new Event('change'));
  resolveFirst(['stale']); await Promise.resolve(); await Promise.resolve();
  expect(root.querySelector('datalist')!.children).toHaveLength(0);
  identifier.value = 'manual'; identifier.dispatchEvent(new Event('input'));
  expect(readNewChatModel(local)?.model).toBe('manual');
});
