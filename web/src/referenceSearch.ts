import type { FileEntry, Segment } from './types';

export type ReferenceResults = { entries: FileEntry[]; indexing: boolean; incomplete: boolean };
export type ReferenceSearchState = ReferenceResults & { loading?: boolean; error?: string };

async function fetchReferences(content: string, query: string, signal: AbortSignal, segments: Segment[] = []): Promise<ReferenceResults> {
  const response = await fetch('/api/v1/references', { method: 'POST', signal, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ content, query, segments }) });
  const data = await response.json();
  if (!response.ok) throw new Error(data.error || `File search failed (HTTP ${response.status})`);
  return { entries: data, indexing: response.headers.get('X-Echo-Reference-Indexing') === 'true', incomplete: response.headers.get('X-Echo-Reference-Incomplete') === 'true' };
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
