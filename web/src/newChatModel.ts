import { endpointGeneration } from './generationSettings';
import { escapeHTML as esc } from './helpers';
import { isExternalAgent, externalAgentNotice } from './externalAgents';
import { localID, sameMachine } from './instances';
import type { Endpoint, Generation } from './types';
import './newChatModel.css';

export type NewChatModel = { endpointId: string; model?: string };
const preferenceKey = (node: string) => `echo2:new-chat-model:${node}`;

export function readNewChatModel(node: string): NewChatModel | undefined {
  try {
    const value = JSON.parse(localStorage.getItem(preferenceKey(node)) || 'null');
    if (value && typeof value.endpointId === 'string' && value.endpointId.trim() && (value.model === undefined || typeof value.model === 'string')) {
      return { endpointId: value.endpointId, ...(value.model?.trim() ? { model: value.model.trim() } : {}) };
    }
  } catch { /* Missing or corrupt browser storage uses automatic selection. */ }
}

export function saveNewChatModel(node: string, value?: NewChatModel): void {
  try {
    if (!value?.endpointId) localStorage.removeItem(preferenceKey(node));
    else localStorage.setItem(preferenceKey(node), JSON.stringify({ endpointId: localID(value.endpointId), ...(value.model?.trim() ? { model: value.model.trim() } : {}) }));
  } catch { /* Browser storage is optional. */ }
}

export function newChatGeneration(node: string, endpoints: Endpoint[]): Generation {
  const choices = endpoints.filter(endpoint => sameMachine(endpoint, node));
  const saved = readNewChatModel(node), selected = choices.find(endpoint => localID(endpoint.id) === saved?.endpointId);
  const generation = endpointGeneration(selected || choices[0]);
  if (selected && saved?.model) generation.model = saved.model;
  return generation;
}

export function newChatGenerations(nodes: Iterable<string>, endpoints: Endpoint[]): Record<string, Generation> {
  return Object.fromEntries([...nodes].map(node => [node, newChatGeneration(node, endpoints)]));
}

export function mountNewChatModel(root: HTMLElement, node: string, endpoints: Endpoint[], discover: (endpoint: Endpoint) => Promise<string[]>): void {
  const choices = endpoints.filter(endpoint => sameMachine(endpoint, node)), saved = readNewChatModel(node);
  const selected = choices.find(endpoint => localID(endpoint.id) === saved?.endpointId);
  root.className = 'new-chat-model-settings';
  root.innerHTML = `<h4>Default model for new chats</h4><div class="form">
    <label>Default model connection<select name="newChatModelEndpoint"><option value="">Automatic (first connection)</option>${choices.map(endpoint => `<option value="${esc(endpoint.id)}">${esc(endpoint.name)}${isExternalAgent(endpoint) ? ' · CLI' : ''}</option>`).join('')}${saved && !selected ? `<option value="${esc(saved.endpointId)}">Unavailable connection (${esc(saved.endpointId)})</option>` : ''}</select></label>
    <label>Default model identifier<input name="newChatModelIdentifier" list="new-chat-model-options" placeholder="Connection default"></label><datalist id="new-chat-model-options"></datalist>
  </div><p class="subtle">Applies to this browser on the selected machine. Sets the initial model for new chats; bot model overrides take precedence. Leave the identifier blank to use the connection’s configured model.</p><p class="subtle" data-default-model-unavailable hidden>The saved connection is unavailable. New chats use the first available connection until it returns.</p><p class="subtle" data-default-model-external hidden>${externalAgentNotice}</p>`;
  const connection = root.querySelector<HTMLSelectElement>('select')!;
  const identifier = root.querySelector<HTMLInputElement>('input')!;
  const models = root.querySelector<HTMLDataListElement>('datalist')!;
  connection.value = selected?.id || saved?.endpointId || '';
  identifier.value = saved?.model || '';
  let discovery = 0;
  const render = () => {
    const endpoint = choices.find(endpoint => endpoint.id === connection.value), version = ++discovery;
    identifier.disabled = !connection.value;
    identifier.placeholder = isExternalAgent(endpoint) ? 'CLI default' : 'Connection default';
    root.querySelector<HTMLElement>('[data-default-model-unavailable]')!.hidden = !connection.value || !!endpoint;
    root.querySelector<HTMLElement>('[data-default-model-external]')!.hidden = !isExternalAgent(endpoint);
    models.innerHTML = '';
    if (endpoint && !isExternalAgent(endpoint)) void discover(endpoint).then(values => {
      if (version === discovery) models.innerHTML = values.map(value => `<option value="${esc(value)}"></option>`).join('');
    }).catch(() => { /* Manual identifiers remain available. */ });
  };
  const save = () => saveNewChatModel(node, connection.value ? { endpointId: connection.value, model: identifier.value } : undefined);
  connection.onchange = () => { identifier.value = ''; save(); render(); };
  identifier.oninput = save;
  render();
}
