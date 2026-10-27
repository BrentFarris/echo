import type { FileEntry, Segment } from './types';
import { InstanceClient, entryNode, instances, ownedState, resourceID, splitID, localID } from './instances';
import { typedBotSegments } from './references';

export type ReferenceResults = { entries: FileEntry[]; indexing: boolean; incomplete: boolean };
export type ReferenceSearchState = ReferenceResults & { loading?: boolean; error?: string };

async function fetchReferences(content: string, query: string, signal: AbortSignal, segments: Segment[] = []): Promise<ReferenceResults> {
  const groups = referenceGroups(content, segments);
  const results = await Promise.allSettled([...groups].map(async ([node, bots]) => {
    const response = await new InstanceClient(node).fetch('/references', { method: 'POST', signal, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ content: '', query, segments: bots.map(botId => ({ type: 'bot', botId })) }) });
    const data = await response.json(); if (!response.ok) throw new Error(data.error || `File search failed (HTTP ${response.status})`);
    return { entries: (data as FileEntry[]).map(e => ({ ...e, nodeId: node, allowedBots: e.allowedBots.map(id => resourceID(node, id)) })), indexing: response.headers.get('X-Echo-Reference-Indexing') === 'true', incomplete: response.headers.get('X-Echo-Reference-Incomplete') === 'true' };
  }));
  if (results.every(r => r.status === 'rejected')) throw (results[0] as PromiseRejectedResult).reason;
  return results.reduce<ReferenceResults>((out, r) => r.status === 'fulfilled' ? { entries: [...out.entries, ...r.value.entries], indexing: out.indexing || r.value.indexing, incomplete: out.incomplete || r.value.incomplete } : { ...out, incomplete: true }, { entries: [], indexing: false, incomplete: false });
}

function referenceGroups(content: string, segments: Segment[]) {
  const bots = [...instances].flatMap(([node, i]) => i.state ? ownedState(node, i.state).bots : []);
  let masked = content.replace(/```[\s\S]*?```|`[^`]*`/g, value => ' '.repeat(value.length));
  for (const ref of segments) if (ref.range) masked = masked.slice(0, ref.range.start) + ' '.repeat(ref.range.end - ref.range.start) + masked.slice(ref.range.end);
  for (const match of masked.matchAll(/(?:^|[^A-Za-z0-9_@])@([A-Za-z][A-Za-z0-9_-]*(?:\.[A-Za-z][A-Za-z0-9_-]*)*)/g)) {
    if (!bots.some(bot => bot.handle.toLowerCase() === match[1].toLowerCase())) throw new Error(`unknown bot @${match[1].toLowerCase()}`);
  }
  const selected = [...segments.filter(s => s.type === 'bot'), ...typedBotSegments(masked, bots)];
  const groups = new Map<string, string[]>();
  for (const ref of selected) { const [node, id] = splitID(ref.botId); const key = ref.nodeId || node; groups.set(key, [...new Set([...(groups.get(key) || []), id])]); }
  if (!groups.size) groups.set(entryNode, ['echo']);
  return groups;
}
export async function validateInstanceReferences(content: string, segments: Segment[]): Promise<Segment[]> {
  const groups = referenceGroups(content, segments);
  // Mentions are already resolved; the target receives only its own recipients.
  const masked = content.replace(/(?:^|[^A-Za-z0-9_@])@([A-Za-z][A-Za-z0-9_-]*(?:\.[A-Za-z][A-Za-z0-9_-]*)*)/g, value => ' '.repeat(value.length));
  const files = segments.filter(s => s.type === 'file');
  const results = await Promise.all(files.map(async file => {
    const node = file.nodeId || entryNode, bots = groups.get(node) || [];
    if (!bots.length) return { ...file, allowedBots: [] };
    // Keep filenames containing @ intact so their checked ranges still match.
    let text = masked; for (const ref of files) if (ref.range) text = text.slice(0, ref.range.start) + content.slice(ref.range.start, ref.range.end) + text.slice(ref.range.end);
    const values = await new InstanceClient(node).json<Segment[]>('/references/validate', 'POST', { content: text, segments: [{ ...file, nodeId: node }, ...bots.map(botId => ({ type: 'bot', botId }))] });
    return { ...values[0], nodeId: node, allowedBots: (values[0]?.allowedBots || []).map(id => resourceID(node, id)) };
  }));
  return results;
}

// Each edit or dismissal invalidates both the debounce and any in-flight
// response. Poll only while the server is building its initial filename index.
export class ReferenceSearch {
  private version = 0;
  private timer?: ReturnType<typeof setTimeout>;
  private controller?: AbortController;

  constructor(private readonly request = fetchReferences) {}

  close() {
    this.version++;
    clearTimeout(this.timer);
    this.controller?.abort();
  }

  search(content: string, query: string, notify: (state: ReferenceSearchState) => void, segments: Segment[] = []) {
    this.close();
    const version = this.version;
    const controller = this.controller = new AbortController();
    notify({ entries: [], indexing: false, incomplete: false, loading: true });
    const run = async () => {
      try {
        const result = await this.request(content, query, controller.signal, segments);
        if (version !== this.version) return;
        notify(result);
        if (result.indexing) this.timer = setTimeout(() => { void run(); }, 300);
      } catch (error) {
        if (version !== this.version) return;
        notify({ entries: [], indexing: false, incomplete: false, error: error instanceof Error ? error.message : 'File search failed. Try again.' });
      }
    };
    this.timer = setTimeout(() => { void run(); }, 100);
  }
}
