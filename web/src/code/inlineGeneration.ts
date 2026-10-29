import { endpointGeneration, thinkingLevels } from '../generationSettings';
import { InstanceClient, localID, resourceID, splitID } from '../instances';
import type { Endpoint, Generation } from '../types';

export type InlineModelContext = { generationFor?(node: string): Generation; openModelSettings?(node: string): void };

/** Preferences belong to a bot on a machine, never to the visible main chat. */
export class InlineGeneration {
  endpoints: Endpoint[] = [];
  value: Generation;
  readonly node: string;
  private readonly key: string;
  private loading?: Promise<void>;
  constructor(readonly botId: string, readonly context: InlineModelContext = {}) {
    this.node = splitID(botId)[0];
    this.key = `echo2:inline-generation:${this.node}:${localID(botId)}`;
    this.value = structuredClone(context.generationFor?.(this.node) || endpointGeneration());
    try {
      const saved = JSON.parse(localStorage.getItem(this.key) || 'null');
      if (saved && typeof saved.endpointId === 'string' && typeof saved.model === 'string' && thinkingLevels.includes(saved.thinking) && typeof saved.temperature === 'number' && saved.temperature >= 0 && saved.temperature <= 2) this.value = saved;
    } catch { /* Corrupt or unavailable preferences do not block the editor. */ }
  }
  save(value: Generation): void {
    this.value = { ...value };
    try { localStorage.setItem(this.key, JSON.stringify(this.value)); } catch { /* Optional storage. */ }
  }
  refresh(): Promise<void> {
    return this.loading ||= new InstanceClient(this.node).json<{ endpoints: Endpoint[] }>('/state').then(state => {
      this.endpoints = state.endpoints.map(endpoint => ({ ...endpoint, nodeId: this.node, id: resourceID(this.node, endpoint.id) }));
      this.save(this.value.endpointId ? this.value : endpointGeneration(this.endpoints[0]));
    }).finally(() => { this.loading = undefined; });
  }
}
