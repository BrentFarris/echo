import type { FileEntry, Segment } from './types';
import { InstanceClient, entryNode, instances, ownedState, resourceID, splitID, localID } from './instances';
import { draftRecipients } from './recipientMentions';

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
  const inventory = [...instances].flatMap(([node, i]) => i.state ? [ownedState(node, i.state)] : []);
  const { selected } = draftRecipients(content, segments, inventory.flatMap(s => s.bots), inventory.flatMap(s => s.botGroups || []), true);
  const groups = new Map<string, string[]>();
  for (const ref of selected) { const [node, id] = splitID(ref.botId); const key = ref.nodeId || node; groups.set(key, [...new Set([...(groups.get(key) || []), id])]); }
  if (!groups.size) groups.set(entryNode, ['echo']);
  return groups;
}
export function instanceReferenceDraft(content: string, segments: Segment[], node = entryNode) {
  const bots = referenceGroups(content, segments).get(node) || [];
  if (!bots.length) throw new Error('This reference is no longer available to any recipient.');
  const files = segments.filter(s => s.type === 'file' && (s.nodeId || entryNode) === node);
  // Mentions are already resolved; the target receives only its own recipients.
  let text = content.replace(/(?:^|[^A-Za-z0-9_@])@([A-Za-z][A-Za-z0-9_-]*(?:\.[A-Za-z][A-Za-z0-9_-]*)*)/g, value => ' '.repeat(value.length));
  // Keep filenames containing @ intact so their checked ranges still match.
  for (const ref of files) if (ref.range) text = text.slice(0, ref.range.start) + content.slice(ref.range.start, ref.range.end) + text.slice(ref.range.end);
  return { content: text, segments: [...files, ...bots.map(botId => ({ type: 'bot', botId }))] };
}
export async function validateInstanceReferences(content: string, segments: Segment[]): Promise<Segment[]> {
  const groups = referenceGroups(content, segments);
  const files = segments.filter(s => s.type === 'file');
  const results = await Promise.all(files.map(async file => {
    const node = file.nodeId || entryNode, bots = groups.get(node) || [];
    if (!bots.length) return { ...file, allowedBots: [] };
    const draft = instanceReferenceDraft(content, segments, node);
    const values = await new InstanceClient(node).json<Segment[]>('/references/validate', 'POST', { ...draft, segments: [{ ...file, nodeId: node }, ...draft.segments.filter(s => s.type === 'bot')] });
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
