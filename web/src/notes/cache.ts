import type { Draft, OwnedNote } from './model';

export type CachedNotes = { notes: OwnedNote[]; drafts: Draft[] };
type StoredNotes = { notes: OwnedNote[]; draftsByView: Record<string, Draft[]> };

// One record per entry profile + owning machine; no workspace/bot storage.
export class NotesCache {
  private db: Promise<IDBDatabase>;
  private tab: string;
  constructor(private entry: () => string, private view: () => string) {
    try {
      this.tab = sessionStorage.getItem('echo:notes:tab') || crypto.randomUUID();
      sessionStorage.setItem('echo:notes:tab', this.tab);
    } catch { this.tab = ''; }
    this.db = new Promise((resolve, reject) => {
      const request = indexedDB.open('echo-notes-v1', 1);
      request.onupgradeneeded = () => request.result.createObjectStore('machines');
      request.onsuccess = () => resolve(request.result);
      request.onerror = () => reject(request.error);
    });
    // A disabled IndexedDB must not create an unhandled rejection at startup.
    void this.db.catch(() => {});
  }
  private prefix() {
    const entry = this.entry();
    if (!entry) throw new Error('Echo profile identity is not ready');
    return `${entry}:`;
  }
  private draftSlot() {
    if (!this.tab) throw new Error('Browser draft identity storage is unavailable');
    return `${this.tab}:${this.view()}`;
  }
  async read(node: string): Promise<CachedNotes | undefined> {
    const db = await this.db;
    const key = this.prefix() + node, slot = this.draftSlot();
    return new Promise((resolve, reject) => {
      const request = db.transaction('machines').objectStore('machines').get(key);
      request.onsuccess = () => {
        const stored = request.result as StoredNotes | undefined;
        resolve(stored ? { notes: stored.notes, drafts: stored.draftsByView?.[slot] || [] } : undefined);
      };
      request.onerror = () => reject(request.error);
    });
  }
  async write(node: string, data?: CachedNotes): Promise<void> {
    const db = await this.db;
    const key = this.prefix() + node, slot = this.draftSlot();
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction('machines', 'readwrite');
      const store = tx.objectStore('machines');
      if (data) {
        // Another Notes window/tab may have unsaved work on this machine.
        // Only replace our own recovery slot, in the same read/write transaction.
        const request = store.get(key);
        request.onsuccess = () => {
          const draftsByView = (request.result as StoredNotes | undefined)?.draftsByView || {};
          if (data.drafts.length) draftsByView[slot] = data.drafts; else delete draftsByView[slot];
          store.put({ notes: data.notes, draftsByView } satisfies StoredNotes, key);
        };
      } else store.delete(key);
      tx.oncomplete = () => resolve();
      tx.onerror = () => reject(tx.error);
      tx.onabort = () => reject(tx.error);
    });
  }
  async prune(nodes: Set<string>): Promise<void> {
    const db = await this.db;
    const prefix = this.prefix();
    await new Promise<void>((resolve, reject) => {
      const tx = db.transaction('machines', 'readwrite'), store = tx.objectStore('machines');
      const request = store.openCursor();
      request.onsuccess = () => {
        const cursor = request.result;
        if (!cursor) return;
        const key = String(cursor.key);
        if (key.startsWith(prefix) && !nodes.has(key.slice(prefix.length))) cursor.delete();
        cursor.continue();
      };
      tx.oncomplete = () => resolve(); tx.onerror = () => reject(tx.error);
    });
  }
  close() { void this.db.then(db => db.close()).catch(() => {}); }
}
