import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import { NotesApp } from './app';
import { combineState, instances, InstanceClient } from '../instances';
import type { NativeAppContext } from '../pluginViews';
import type { Bot, Plugin, State } from '../types';
import { type Note, type Draft, noteKey } from './model';

const cache = vi.hoisted(() => new Map<string, any>());
vi.mock('./cache', () => ({ NotesCache: class {
  async read(node: string) { return structuredClone(cache.get(node)); }
  async write(node: string, data: unknown) { if (data) cache.set(node, structuredClone(data)); else cache.delete(node); }
  async prune(nodes: Set<string>) { for (const node of cache.keys()) if (!nodes.has(node)) cache.delete(node); }
  close() {}
} }));

const home = 'a'.repeat(32), remote = 'b'.repeat(32);
const plugin = { id: 'notes', name: 'Notes', builtin: true, enabled: true, scope: 'app', views: [{ id: 'main', kind: 'embedded' }] } as Plugin;
const bot = { id: 'echo', handle: 'echo', nodeId: home } as Bot;
let root: HTMLElement, app: NotesApp, records: Map<string, Note[]>, api: ReturnType<typeof vi.spyOn>, plugins: Plugin[];
const note = (id = 'one'): Note => ({ id, title: 'First note', markdown: '# Welcome\n\n#plan @echo', mentions: [], revision: 1, createdAt: '2026-01-01', updatedAt: '2026-01-01' });
const blank: State = { bots: [bot], plugins: [plugin], chats: [], endpoints: [], schedules: [], occurrences: [], runs: [], appDock: [] };
const q = <T extends HTMLElement>(selector: string) => root.querySelector<T>(selector)!;
const tick = async () => { await vi.advanceTimersByTimeAsync(0); };
const open = async (key = `${home}:one`) => { q<HTMLButtonElement>(`[data-note="${key}"]`).click(); await tick(); };
const edit = async (text: string) => {
  if (q('[data-mode]').textContent === 'Edit') q<HTMLButtonElement>('[data-mode]').click();
  q<HTMLTextAreaElement>('[data-body]').value = text; q('[data-body]').dispatchEvent(new Event('input')); await tick();
};
function mount() {
  app = new NotesApp(root, { bots: [bot], plugin, inventory: () => ({ bots: [bot], plugins }), api: vi.fn() } as unknown as NativeAppContext);
}
beforeEach(() => {
  vi.useFakeTimers(); cache.clear(); plugins = [plugin, { ...plugin, id: `${remote}:notes`, nodeId: remote }];
  combineState(blank, { node: { nodeId: home, name: 'desktop', epoch: '', version: 1, capabilities: ['native-notes/1'] }, enabled: true, peers: [], instances: [{ node: { nodeId: remote, name: 'linux', epoch: '', version: 1, capabilities: ['native-notes/1'] }, online: true, state: blank }] });
  records = new Map([[home, [note()]], [remote, [{ ...note('remote'), title: 'Remote note' }]]]);
  api = vi.spyOn(InstanceClient.prototype, 'json').mockImplementation(async function(this: InstanceClient, path: string, method = 'GET', body?: any) {
    const list = records.get(this.nodeId)!;
    if (method === 'GET') return structuredClone(path.endsWith('/notes') ? list : list.find(n => path.endsWith('/' + n.id)));
    if (method === 'PUT') {
      const index = list.findIndex(n => path.endsWith('/' + n.id)), previous = list[index];
      if (!previous) throw Object.assign(new Error('Deleted'), { status: 404 });
      if (previous.revision !== body.revision) throw Object.assign(new Error('Conflict'), { status: 409 });
      list[index] = { ...previous, ...body, revision: previous.revision + 1, updatedAt: '2026-02-01' }; return structuredClone(list[index]);
    }
    if (method === 'POST') { const created = { ...note('new'), ...body }; list.push(created); return structuredClone(created); }
    if (method === 'DELETE') { const index = list.findIndex(n => path.includes('/' + n.id + '?')); list.splice(index, 1); return {}; }
  });
  vi.spyOn(window, 'confirm').mockReturnValue(true);
  root = document.createElement('div'); document.body.append(root);
});
afterEach(async () => { app?.dispose(); await tick(); root.remove(); vi.restoreAllMocks(); vi.useRealTimers(); });

it('starts read-only, autosaves Markdown and toggles back to noneditable rendering', async () => {
  mount(); await tick(); await open();
  expect(q<HTMLTextAreaElement>('[data-body]').readOnly).toBe(true);
  expect(q('[data-preview] h1').textContent).toBe('Welcome');
  await edit('# Changed\n\n- [ ] task #project @echo');
  expect(q('[data-status]').textContent).toBe('Unsaved');
  await vi.advanceTimersByTimeAsync(750);
  expect(records.get(home)![0].markdown).toContain('Changed');
  expect(records.get(home)![0].mentions[0].botId).toBe('echo');
  q<HTMLButtonElement>('[data-mode]').click(); await tick();
  expect(q('[data-preview]').hidden).toBe(false);
  expect(q<HTMLTextAreaElement>('[data-body]').readOnly).toBe(true);
  expect(q<HTMLInputElement>('[data-preview] input').disabled).toBe(true);
  expect(q('[data-status]').textContent).toBe('Saved');
});

it('combines filters and creates a note on the chosen machine', async () => {
  mount(); await tick();
  expect(root.querySelectorAll('[data-note]')).toHaveLength(2);
  q<HTMLSelectElement>('[data-machines]').value = remote; q('[data-machines]').dispatchEvent(new Event('change'));
  expect(root.querySelectorAll('[data-note]')).toHaveLength(1);
  expect(q<HTMLSelectElement>('[data-destination]').value).toBe(remote);
  q<HTMLButtonElement>('[data-new]').click(); await tick();
  expect(records.get(remote)).toHaveLength(2);
  expect(q('[data-owner]').textContent).toBe('linux');
  expect(q<HTMLTextAreaElement>('[data-body]').readOnly).toBe(false);
});

it('preserves offline drafts across reopen and detects conflicting remote revisions', async () => {
  mount(); await tick(); await open(); await edit('my unsaved draft');
  instances.get(home)!.online = false; app.update(); await tick();
  expect(q<HTMLTextAreaElement>('[data-body]').readOnly).toBe(true);
  await vi.advanceTimersByTimeAsync(750);
  expect(records.get(home)![0].markdown).not.toBe('my unsaved draft');
  app.dispose(); await tick();
  records.get(home)![0] = { ...note(), markdown: 'another device', revision: 2 };
  instances.get(home)!.online = true; mount(); await tick(); await open();
  expect(q('[data-conflict]').hidden).toBe(false);
  expect(q('[data-preview]').textContent?.trim()).toBe('my unsaved draft');
  q<HTMLButtonElement>('[data-copy]').click(); await tick();
  expect(records.get(home)!.map(n => n.markdown)).toEqual(['another device', 'my unsaved draft']);
});

it('serializes saves and flushes a newer edit after an in-flight save', async () => {
  mount(); await tick(); await open();
  const original = api.getMockImplementation()!;
  let release!: () => void;
  api.mockImplementationOnce(async function(this: InstanceClient, ...args: any[]) { await new Promise<void>(resolve => { release = resolve; }); return original.apply(this, args as any); });
  await edit('first'); await vi.advanceTimersByTimeAsync(750);
  await edit('second');
  q<HTMLButtonElement>('[data-mode]').click(); await tick();
  expect(api.mock.calls.filter((c: any[]) => c[1] === 'PUT')).toHaveLength(1);
  release(); await tick();
  expect(api.mock.calls.filter((c: any[]) => c[1] === 'PUT').map((c: any[]) => c[2].revision)).toEqual([1, 2]);
  expect(records.get(home)![0].markdown).toBe('second');
  expect(q('[data-mode]').textContent).toBe('Edit');
});

it('clears a recovered draft when its save committed before the acknowledgement was lost', async () => {
  const saved = { ...note(), markdown: 'already saved', revision: 2 };
  records.set(home, [saved]);
  cache.set(home, { notes: [{ ...note(), nodeId: home }], drafts: [{ note: { ...saved, nodeId: home, revision: 1 }, baseRevision: 1, dirty: true }] });
  mount(); await tick(); await open();
  expect(q('[data-conflict]').hidden).toBe(true);
  expect(q('[data-status]').textContent).toBe('Saved');
  expect(q<HTMLButtonElement>('[data-note-delete]').disabled).toBe(false);
  expect(cache.get(home).drafts).toEqual([]);
  expect(api.mock.calls.some((c: any[]) => c[1] === 'PUT')).toBe(false);
});

it('offers keyboard autocomplete without contacting bots', async () => {
  mount(); await tick(); await open(); await edit('@e');
  const body = q<HTMLTextAreaElement>('[data-body]'); body.selectionStart = body.selectionEnd = 2; body.dispatchEvent(new Event('input')); await tick();
  expect(q('[data-suggestions]').hidden).toBe(false);
  body.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true })); await tick();
  expect(body.value).toBe('@echo ');
  expect(api.mock.calls.every((c: any[]) => c[0].startsWith('/plugins/notes/notes'))).toBe(true);
});

it('removes revoked machine caches and hides disabled-machine notes', async () => {
  mount(); await tick(); await open(`${remote}:remote`);
  instances.delete(remote); app.update(); await tick();
  expect(root.querySelector('[data-note*="remote"]')).toBeNull();
  expect(q('[data-editor]').hidden).toBe(true);
  expect(cache.has(remote)).toBe(false);
  await open(); plugins = plugins.map(p => p.id === 'notes' ? { ...p, enabled: false } : p); app.update(); await tick();
  expect(root.querySelectorAll('[data-note]')).toHaveLength(0);
  expect(q('[data-editor]').hidden).toBe(true);
});

it('does not let a slow peer block local notes and retains a deleted note draft', async () => {
  const original = api.getMockImplementation()!;
  api.mockImplementation(function(this: InstanceClient, ...args: any[]) { return this.nodeId === remote ? new Promise(() => {}) : original.apply(this, args as any); });
  const draft: Draft = { note: { ...note('deleted'), nodeId: home, markdown: 'keep me' }, baseRevision: 1, dirty: true };
  cache.set(home, { notes: [], drafts: [draft] });
  mount(); await tick(); await open(noteKey(draft.note));
  expect(q('[data-conflict]').hidden).toBe(false);
  expect(q('[data-preview]').textContent?.trim()).toBe('keep me');
  expect(root.querySelectorAll('[data-note]')).toHaveLength(2);
});
