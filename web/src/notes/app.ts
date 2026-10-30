import type { NativeApp, NativeAppContext } from '../pluginViews';
import { entryNode, instances, InstanceClient, localID, machineName, resourceID } from '../instances';
import { escapeHTML as esc } from '../helpers';
import { NotesCache } from './cache';
import { bindMentions, botKey, hashtags, markers, mentionHandle, noteKey, renderNote, sameContent, type Draft, type Note, type OwnedNote } from './model';
import './notes.css';

const base = '/plugins/notes/notes';
type Machine = { id: string; status: string; ready: boolean; initialized: boolean; epoch: number; loading?: Promise<void> };
const message = (error: unknown) => error instanceof Error ? error.message : String(error);
const statusCode = (error: unknown) => (error as { status?: number })?.status;

export async function mount(root: HTMLElement, context: NativeAppContext): Promise<NativeApp> {
  return new NotesApp(root, context);
}

export class NotesApp implements NativeApp {
  private notes = new Map<string, OwnedNote>();
  private drafts = new Map<string, Draft>();
  private machines = new Map<string, Machine>();
  private saves = new Map<string, Promise<boolean>>();
  private timers = new Map<string, number>();
  private errors = new Map<string, string>();
  private cache = new NotesCache(() => entryNode, () => this.context.plugin.nodeId || entryNode);
  private cacheWarning = '';
  private notice = '';
  private selected = '';
  private editing = false;
  private shown = true;
  private disposed = false;
  private machineFilter = '';
  private tagFilter = '';
  private botFilter = '';
  private creating = false;
  private suggestionIndex = 0;
  private suggestions: string[] = [];
  private suggestionsId = `notes-suggestions-${crypto.randomUUID()}`;
  private completion?: { start: number; end: number };
  private interval: number;
  private focus = () => { if (this.shown) this.refresh(); };
  private unload = (event: BeforeUnloadEvent) => {
    if ([...this.drafts.values()].some(d => d.dirty)) { event.preventDefault(); event.returnValue = ''; }
  };
  private q<T extends HTMLElement>(selector: string): T { return this.root.querySelector<T>(selector)!; }

  constructor(private root: HTMLElement, private context: NativeAppContext) {
    root.classList.add('notes-host');
    root.innerHTML = `<main class="notes-app" aria-label="Notes">
      <aside class="notes-sidebar">
        <div class="notes-heading"><h2>Notes</h2><button type="button" data-new>New note</button></div>
        <label>New note on<select aria-label="New note machine" data-destination></select></label>
        <label class="notes-search">Search notes<input type="search" data-search placeholder="Search titles and Markdown"></label>
        <label>Machine<select data-machines aria-label="Filter notes by machine"></select></label>
        <label>Hashtag<select data-tags aria-label="Filter notes by hashtag"></select></label>
        <label>Bot<select data-bots aria-label="Filter notes by bot"></select></label>
        <div class="notes-list" role="list" aria-label="Notes list"></div>
        <p class="notes-machines" data-machine-status></p>
      </aside>
      <section class="notes-workspace">
        <p data-empty class="notes-empty">Create a note or select one from the list.</p>
        <div data-editor class="notes-editor" hidden>
          <header class="notes-toolbar"><input data-title aria-label="Note title" maxlength="120"><button type="button" data-mode>Edit</button><button type="button" data-note-delete>Delete note</button></header>
          <div class="notes-meta"><span data-owner></span><span data-status role="status" aria-live="polite"></span></div>
          <div data-chips class="notes-chips"></div>
          <div data-conflict class="notes-conflict" hidden><p>The saved note changed or was deleted. Your draft is retained.</p><button type="button" data-latest>Load latest</button><button type="button" data-copy>Save draft as new note</button></div>
          <button type="button" data-retry hidden>Retry save</button>
          <div data-source class="notes-source" hidden><textarea data-body aria-label="Markdown source" spellcheck="true" aria-autocomplete="list" aria-expanded="false"></textarea><div data-suggestions class="notes-suggestions" role="listbox" aria-label="Note suggestions" hidden></div></div>
          <article data-preview class="notes-markdown" aria-label="Rendered note"></article>
        </div>
        <p data-warning role="alert" class="notes-warning"></p>
      </section>
    </main>`;
    this.q('[data-new]').onclick = () => { void this.create(); };
    this.q<HTMLInputElement>('[data-search]').oninput = () => this.renderList();
    this.q<HTMLSelectElement>('[data-machines]').onchange = event => {
      this.machineFilter = (event.target as HTMLSelectElement).value;
      this.q<HTMLSelectElement>('[data-destination]').value = this.machineFilter || entryNode;
      this.renderList();
    };
    this.q<HTMLSelectElement>('[data-tags]').onchange = event => { this.tagFilter = (event.target as HTMLSelectElement).value; this.renderList(); };
    this.q<HTMLSelectElement>('[data-bots]').onchange = event => { this.botFilter = (event.target as HTMLSelectElement).value; this.renderList(); };
    this.q('[data-mode]').onclick = () => { void this.toggleMode(); };
    this.q('[data-note-delete]').onclick = () => { void this.remove(); };
    this.q('[data-latest]').onclick = () => { void this.loadLatest(); };
    this.q('[data-copy]').onclick = () => { const draft = this.drafts.get(this.selected); if (draft) void this.create(draft); };
    this.q('[data-retry]').onclick = () => { void this.flush(this.selected); };
    this.q<HTMLInputElement>('[data-title]').oninput = () => this.edited();
    const body = this.q<HTMLTextAreaElement>('[data-body]');
    this.q('[data-suggestions]').id = this.suggestionsId;
    body.setAttribute('aria-controls', this.suggestionsId);
    body.oninput = () => { this.edited(); this.complete(); };
    body.onkeydown = event => this.completionKey(event);
    body.onclick = () => this.hideSuggestions();
    body.onblur = () => { window.setTimeout(() => { if (!this.disposed) this.hideSuggestions(); }, 120); };
    root.addEventListener('keydown', this.shortcut);
    window.addEventListener('focus', this.focus);
    window.addEventListener('beforeunload', this.unload);
    this.interval = window.setInterval(() => { if (this.shown) this.refresh(); }, 5000);
    this.refresh();
  }

  private inventory() { return this.context.inventory?.() || { bots: this.context.bots, plugins: [this.context.plugin] }; }
  private current() { return this.drafts.get(this.selected)?.note || this.notes.get(this.selected); }
  private writable(node: string) { return !!this.machines.get(node)?.ready && instances.get(node)?.online === true; }
  private client(node: string) { return new InstanceClient(node); }
  private authorized(node: string) { return !this.disposed && instances.has(node) && this.machines.has(node); }
  private shortcut = (event: KeyboardEvent) => {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === 's') { event.preventDefault(); void this.flush(this.selected); }
  };

  update() { this.refresh(); }
  visible(value: boolean) { this.shown = value; if (value) this.refresh(); }
  async beforeClose() {
    await Promise.all([...this.drafts.keys()].map(key => this.flush(key)));
    await Promise.all([...this.machines.keys()].map(node => this.persist(node)));
    return ![...this.drafts.values()].some(d => d.dirty) || window.confirm('Some edits are not saved to their machine. Close Notes and keep recovery drafts in this browser?');
  }
  dispose() {
    this.disposed = true;
    clearInterval(this.interval);
    for (const timer of this.timers.values()) clearTimeout(timer);
    window.removeEventListener('focus', this.focus);
    window.removeEventListener('beforeunload', this.unload);
    this.root.removeEventListener('keydown', this.shortcut);
    void Promise.all([...this.machines.keys()].map(node => this.persist(node))).finally(() => this.cache.close());
  }

  private async persist(node: string) {
    try {
      await this.cache.write(node, this.machines.has(node) ? {
        notes: [...this.notes.values()].filter(n => n.nodeId === node),
        drafts: [...this.drafts.values()].filter(d => d.note.nodeId === node && d.dirty),
      } : undefined);
    } catch { this.cacheWarning = 'Browser recovery storage is unavailable. Keep Notes open until edits are saved to their machine.'; if (!this.disposed) this.renderStatus(); }
  }

  private refresh() {
    if (this.disposed) return;
    const inventory = this.inventory();
    for (const [node, machine] of this.machines) {
      if (!instances.has(node)) {
        machine.epoch++;
        this.machines.delete(node);
        for (const [key, note] of this.notes) if (note.nodeId === node) this.notes.delete(key);
        for (const [key, draft] of this.drafts) if (draft.note.nodeId === node) { clearTimeout(this.timers.get(key)); this.drafts.delete(key); }
        if (!this.current()) this.selected = '';
        void this.persist(node);
      }
    }
    void this.cache.prune(new Set(instances.keys())).catch(() => {});
    for (const [node, instance] of instances) {
      let machine = this.machines.get(node);
      if (!machine) { machine = { id: node, status: 'Loading…', ready: false, initialized: false, epoch: 0 }; this.machines.set(node, machine); }
      const plugin = inventory.plugins.find(p => localID(p.id) === 'notes' && (p.nodeId || entryNode) === node);
      const reason = !instance.node.capabilities?.includes('native-notes/1') ? 'Update required' : !plugin?.enabled || plugin.pending ? 'Notes disabled' : !instance.online ? 'Offline' : '';
      if (reason) { if (machine.status !== reason) machine.epoch++; machine.ready = false; machine.status = reason; }
      if (reason && reason !== 'Offline') {
        if (this.current()?.nodeId === node) this.selected = '';
        continue;
      }
      if (!machine.loading) {
        const target = machine;
        target.loading = this.loadMachine(target, !reason).finally(() => { target.loading = undefined; });
      }
    }
    this.render();
  }

  private async loadMachine(machine: Machine, online: boolean) {
    const node = machine.id;
    if (!machine.initialized) {
      machine.initialized = true;
      try {
        const cached = await this.cache.read(node);
        if (!this.authorized(node)) return;
        for (const note of cached?.notes || []) if (note.nodeId === node) this.notes.set(noteKey(note), note);
        for (const draft of cached?.drafts || []) if (draft.note.nodeId === node && draft.dirty) this.drafts.set(noteKey(draft.note), draft);
      } catch { this.cacheWarning = 'Browser recovery storage is unavailable.'; }
      if (!this.disposed) this.render();
    }
    if (!online || !this.authorized(node) || [...this.saves.keys()].some(key => key.startsWith(node + ':'))) return;
    const epoch = machine.epoch;
    try {
      const notes = await this.client(node).json<Note[]>(base, 'GET', undefined, AbortSignal.timeout(8000));
      if (!this.authorized(node) || machine.epoch !== epoch) return;
      machine.ready = true; machine.status = '';
      const keys = new Set(notes.map(note => `${node}:${note.id}`));
      for (const [key, note] of this.notes) if (note.nodeId === node && !keys.has(key)) {
        this.notes.delete(key);
        const draft = this.drafts.get(key); if (draft?.dirty) draft.conflict = true;
        else { this.drafts.delete(key); if (this.selected === key) this.selected = ''; }
      }
      for (const note of notes) {
        const owned = { ...note, nodeId: node }, key = noteKey(owned), draft = this.drafts.get(key);
        this.notes.set(key, owned);
        // A reload/network loss may hide the acknowledgement of our own save.
        // Identical content is already durable, not a conflicting edit.
        if (draft?.dirty && sameContent(draft.note, note)) { this.drafts.delete(key); this.errors.delete(key); }
        else if (draft?.dirty && draft.baseRevision !== note.revision) draft.conflict = true;
        else if (!draft?.dirty) this.drafts.delete(key);
      }
      // A recovered draft can refer to a note deleted before this browser loaded.
      for (const [key, draft] of this.drafts) if (draft.note.nodeId === node && !keys.has(key)) draft.conflict = true;
      await this.persist(node);
      if (!this.authorized(node)) return;
      this.render();
      for (const [key, draft] of this.drafts) if (draft.note.nodeId === node && draft.dirty && !draft.conflict && !this.timers.has(key) && !this.errors.has(key)) void this.flush(key);
    } catch (error) {
      if (!this.authorized(node) || machine.epoch !== epoch) return;
      machine.ready = false; machine.status = [401, 403].includes(statusCode(error) || 0) ? 'Access unavailable' : `Unavailable: ${message(error)}`;
      if (statusCode(error) === 403 || statusCode(error) === 401) {
        for (const [key, note] of this.notes) if (note.nodeId === node) this.notes.delete(key);
        // Keep recovery drafts on disk but never display them without access.
        if (this.current()?.nodeId === node) this.selected = '';
      }
      this.render();
    }
  }

  private render() {
    if (this.disposed) return;
    this.renderFilters(); this.renderList(); this.renderEditor(); this.renderStatus();
  }
  private options(select: HTMLSelectElement, items: [string, string, boolean?][], value: string) {
    const html = items.map(([id, name, disabled]) => `<option value="${esc(id)}"${disabled ? ' disabled' : ''}>${esc(name)}</option>`).join('');
    if (select.innerHTML !== html) select.innerHTML = html;
    select.value = value;
  }
  private allNotes() {
    const notes = new Map(this.notes);
    for (const [key, draft] of this.drafts) notes.set(key, draft.note);
    return [...notes.values()].filter(n => this.machines.has(n.nodeId) && !['Notes disabled', 'Access unavailable', 'Update required'].includes(this.machines.get(n.nodeId)!.status));
  }
  private renderFilters() {
    const machines: [string, string, boolean?][] = [...this.machines.values()].map(m => [m.id, machineName(m.id) + (m.status ? ` · ${m.status}` : '')]);
    this.options(this.q('[data-machines]'), [['', 'All machines'], ...machines], this.machineFilter);
    const destination = this.q<HTMLSelectElement>('[data-destination]'), selected = destination.value || this.machineFilter || entryNode;
    this.options(destination, machines.map(([id, name]) => [id, name, !this.writable(id)]), selected);
    this.q<HTMLButtonElement>('[data-new]').disabled = this.creating || ![...this.machines.keys()].some(id => this.writable(id));
    const tags = [...new Set(this.allNotes().flatMap(n => hashtags(n.markdown)))].sort();
    if (this.tagFilter && !tags.includes(this.tagFilter)) tags.push(this.tagFilter);
    this.options(this.q('[data-tags]'), [['', 'All hashtags'], ...tags.map(tag => [tag, '#' + tag] as [string, string])], this.tagFilter);
    const bots = new Map(this.allNotes().flatMap(n => n.mentions.map(m => [botKey(m), m.label] as [string, string])));
    if (this.botFilter && !bots.has(this.botFilter)) bots.set(this.botFilter, this.botFilter);
    this.options(this.q('[data-bots]'), [['', 'All bots'], ...[...bots].sort((a, b) => a[1].localeCompare(b[1]))], this.botFilter);
    this.q('[data-machine-status]').textContent = [...this.machines.values()].filter(m => m.status).map(m => `${machineName(m.id)}: ${m.status}`).join('\n');
  }
  private renderList() {
    const query = this.q<HTMLInputElement>('[data-search]').value.trim().toLocaleLowerCase();
    const notes = this.allNotes().filter(n => (!this.machineFilter || n.nodeId === this.machineFilter) && (!this.tagFilter || hashtags(n.markdown).includes(this.tagFilter)) && (!this.botFilter || n.mentions.some(m => botKey(m) === this.botFilter)) && (!query || `${n.title}\n${n.markdown}`.toLocaleLowerCase().includes(query)))
      .sort((a, b) => b.updatedAt.localeCompare(a.updatedAt) || noteKey(a).localeCompare(noteKey(b)));
    this.q('.notes-list').innerHTML = notes.map(n => `<div role="listitem"><button type="button" data-note="${esc(noteKey(n))}"${noteKey(n) === this.selected ? ' aria-current="true"' : ''}><strong>${esc(n.title || 'Untitled note')}</strong><small>${esc(machineName(n.nodeId))}${this.writable(n.nodeId) ? '' : ' · Offline / read only'}${this.drafts.get(noteKey(n))?.dirty ? ' · Unsaved' : ''}</small></button></div>`).join('') || '<p class="notes-empty">No matching notes.</p>';
    for (const button of this.root.querySelectorAll<HTMLButtonElement>('[data-note]')) button.onclick = () => {
      if (this.selected) void this.flush(this.selected);
      this.selected = button.dataset.note!; this.editing = false; this.hideSuggestions(); this.render();
    };
  }
  private renderEditor() {
    const note = this.current(), editing = this.editing;
    this.q('[data-empty]').hidden = !!note;
    this.q('[data-editor]').hidden = !note;
    if (!note) return;
    const writable = this.writable(note.nodeId), conflict = !!this.drafts.get(this.selected)?.conflict;
    const title = this.q<HTMLInputElement>('[data-title]'), body = this.q<HTMLTextAreaElement>('[data-body]');
    if (title.value !== note.title) title.value = note.title;
    if (body.value !== note.markdown) body.value = note.markdown;
    title.readOnly = body.readOnly = !editing || !writable || conflict;
    this.q('[data-source]').hidden = !editing;
    this.q('[data-preview]').hidden = editing;
    if (!editing) this.q('[data-preview]').replaceChildren(renderNote(note.markdown));
    const mode = this.q<HTMLButtonElement>('[data-mode]'); mode.textContent = editing ? 'Done' : 'Edit'; mode.setAttribute('aria-pressed', String(editing)); mode.disabled = !editing && (!writable || conflict);
    this.q<HTMLButtonElement>('[data-note-delete]').disabled = !writable || conflict || this.saves.has(this.selected);
    this.q('[data-owner]').textContent = machineName(note.nodeId);
    this.q('[data-conflict]').hidden = !conflict;
    this.q<HTMLButtonElement>('[data-latest]').disabled = !writable;
    this.q<HTMLButtonElement>('[data-copy]').disabled = !writable || this.creating;
    this.q('[data-chips]').innerHTML = hashtags(note.markdown).map(tag => `<button type="button" data-tag="${esc(tag)}">#${esc(tag)}</button>`).join('') + [...new Map(note.mentions.map(m => [botKey(m), m])).values()].map(m => {
      const bot = this.inventory().bots.find(b => b.id === resourceID(m.nodeId, m.botId));
      const label = bot ? mentionHandle(bot, note.nodeId) : `${m.label} (unavailable)`;
      return `<button type="button" data-bot="${esc(botKey(m))}">${esc(label)}</button>`;
    }).join('');
    for (const button of this.root.querySelectorAll<HTMLButtonElement>('[data-tag]')) button.onclick = () => { this.tagFilter = button.dataset.tag!; this.renderFilters(); this.renderList(); };
    for (const button of this.root.querySelectorAll<HTMLButtonElement>('[data-bot]')) button.onclick = () => { this.botFilter = button.dataset.bot!; this.renderFilters(); this.renderList(); };
  }
  private renderStatus() {
    const note = this.current(), draft = this.drafts.get(this.selected), error = this.errors.get(this.selected);
    this.q('[data-status]').textContent = draft?.conflict ? 'Conflict · draft retained' : this.saves.has(this.selected) ? 'Saving…' : error || (note && !this.writable(note.nodeId) ? (draft?.dirty ? 'Offline · draft retained' : 'Cached · read only') : draft?.dirty ? 'Unsaved' : 'Saved');
    this.q('[data-retry]').hidden = !error || !!draft?.conflict || !note || !this.writable(note.nodeId);
    this.q('[data-warning]').textContent = this.cacheWarning || this.notice;
  }

  private edited() {
    const previous = this.current();
    if (!previous || !this.editing || !this.writable(previous.nodeId)) return;
    const body = this.q<HTMLTextAreaElement>('[data-body]').value;
    const note = { ...previous, title: this.q<HTMLInputElement>('[data-title]').value, markdown: body,
      mentions: bindMentions(body, previous.nodeId, this.inventory().bots, previous.markdown, previous.mentions) };
    const draft = this.drafts.get(this.selected);
    this.drafts.set(this.selected, { note, dirty: true, baseRevision: draft?.baseRevision || note.revision });
    this.errors.delete(this.selected);
    const key = this.selected;
    clearTimeout(this.timers.get(key));
    this.timers.set(key, window.setTimeout(() => { this.timers.delete(key); void this.flush(key); }, 750));
    void this.persist(note.nodeId);
    this.renderFilters(); this.renderList(); this.renderStatus();
  }

  private async flush(key: string): Promise<boolean> {
    clearTimeout(this.timers.get(key)); this.timers.delete(key);
    const pending = this.saves.get(key);
    if (pending) { if (!await pending) return false; return this.flush(key); }
    const draft = this.drafts.get(key);
    if (!draft?.dirty) return true;
    if (draft.conflict || !this.writable(draft.note.nodeId) || this.disposed) return false;
    const snapshot = structuredClone(draft), machine = this.machines.get(draft.note.nodeId)!;
    machine.epoch++; // Invalidate any list fetched before this save.
    const task = (async () => {
      try {
        const saved = await this.client(snapshot.note.nodeId).json<Note>(`${base}/${encodeURIComponent(snapshot.note.id)}`, 'PUT', { title: snapshot.note.title, markdown: snapshot.note.markdown, mentions: snapshot.note.mentions, revision: snapshot.baseRevision }, AbortSignal.timeout(8000));
        if (!this.authorized(snapshot.note.nodeId)) return false;
        const note = { ...saved, nodeId: snapshot.note.nodeId };
        this.notes.set(key, note); this.errors.delete(key);
        const current = this.drafts.get(key);
        if (current === draft) this.drafts.delete(key);
        else if (current) { current.baseRevision = saved.revision; current.note.revision = saved.revision; current.note.updatedAt = saved.updatedAt; }
        await this.persist(note.nodeId);
        return true;
      } catch (error) {
        const current = this.drafts.get(key);
        if (!this.authorized(snapshot.note.nodeId) || !current) return false;
        if ([404, 409].includes(statusCode(error) || 0)) current.conflict = true;
        else if (!statusCode(error) || (statusCode(error) || 0) >= 500) { machine.ready = false; machine.status = 'Unavailable · draft retained'; }
        this.errors.set(key, message(error)); await this.persist(snapshot.note.nodeId);
        return false;
      } finally { this.saves.delete(key); if (!this.disposed) this.render(); }
    })();
    this.saves.set(key, task); this.renderStatus();
    const saved = await task;
    return saved && this.drafts.get(key)?.dirty ? this.flush(key) : saved;
  }

  private async toggleMode() {
    if (this.editing) {
      // An offline/conflicted draft can still be viewed safely without saving.
      const note = this.current();
      if (note && this.writable(note.nodeId) && !this.drafts.get(this.selected)?.conflict && !await this.flush(this.selected)) return;
      this.editing = false;
    } else this.editing = true;
    this.hideSuggestions(); this.renderEditor(); this.renderStatus();
    if (this.editing) this.q('[data-body]').focus();
  }

  private async create(copy?: Draft) {
    if (this.creating) return;
    const node = copy?.note.nodeId || this.q<HTMLSelectElement>('[data-destination]').value;
    if (!this.writable(node)) { this.notice = 'Choose an available machine for the new note.'; this.renderStatus(); return; }
    this.notice = '';
    this.creating = true; this.renderFilters();
    try {
      const note = await this.client(node).json<Note>(base, 'POST', { title: copy ? `${Array.from(copy.note.title).slice(0, 113).join('')} (copy)` : 'Untitled note', markdown: copy?.note.markdown || '', mentions: copy?.note.mentions || [] }, AbortSignal.timeout(8000));
      if (!this.authorized(node)) return;
      this.machines.get(node)!.epoch++;
      if (copy) { this.drafts.delete(noteKey(copy.note)); this.errors.delete(noteKey(copy.note)); }
      const owned = { ...note, nodeId: node }; this.notes.set(noteKey(owned), owned);
      this.selected = noteKey(owned); this.editing = true; this.machineFilter = this.tagFilter = this.botFilter = '';
      this.q<HTMLInputElement>('[data-search]').value = '';
      await this.persist(node); this.render(); this.q('[data-title]').focus(); this.q<HTMLInputElement>('[data-title]').select();
    } catch (error) { this.notice = `Could not create note: ${message(error)}`; }
    finally { this.creating = false; if (!this.disposed) this.render(); }
  }

  private async remove() {
    const note = this.current(), key = this.selected;
    if (!note || !window.confirm(`Delete “${note.title}”? This permanently removes the note.`)) return;
    if (!await this.flush(key)) return;
    const saved = this.notes.get(key); if (!saved) return;
    try {
      this.machines.get(note.nodeId)!.epoch++;
      await this.client(note.nodeId).json(`${base}/${encodeURIComponent(note.id)}?revision=${saved.revision}`, 'DELETE', undefined, AbortSignal.timeout(8000));
      if (!this.authorized(note.nodeId)) return;
      this.machines.get(note.nodeId)!.epoch++;
      this.notes.delete(key); this.drafts.delete(key); this.errors.delete(key); if (this.selected === key) this.selected = '';
      await this.persist(note.nodeId); this.render();
    } catch (error) {
      if (statusCode(error) === 409) this.drafts.set(key, { note: saved, baseRevision: saved.revision, dirty: true, conflict: true });
      this.errors.set(key, message(error)); this.render();
    }
  }

  private async loadLatest() {
    const draft = this.drafts.get(this.selected), key = this.selected;
    if (!draft || !window.confirm('Discard this local draft and load the latest saved note?')) return;
    try {
      const note = await this.client(draft.note.nodeId).json<Note>(`${base}/${encodeURIComponent(draft.note.id)}`, 'GET', undefined, AbortSignal.timeout(8000));
      if (!this.authorized(draft.note.nodeId)) return;
      this.machines.get(draft.note.nodeId)!.epoch++;
      this.notes.set(key, { ...note, nodeId: draft.note.nodeId }); this.drafts.delete(key); this.errors.delete(key); this.editing = false;
    } catch (error) {
      if (statusCode(error) === 404) { this.notes.delete(key); this.drafts.delete(key); this.selected = ''; }
      else this.errors.set(key, message(error));
    }
    await this.persist(draft.note.nodeId); this.render();
  }

  private hideSuggestions() { this.suggestions = []; this.completion = undefined; this.q('[data-suggestions]').hidden = true; this.q('[data-body]').setAttribute('aria-expanded', 'false'); this.q('[data-body]').removeAttribute('aria-activedescendant'); }
  private complete() {
    const body = this.q<HTMLTextAreaElement>('[data-body]'), note = this.current();
    const match = body.value.slice(0, body.selectionStart).match(/(?:^|[^\p{L}\p{N}_@#\\])([@#][\p{L}\p{N}_.-]*)$/u);
    if (!match || !note) { this.hideSuggestions(); return; }
    const query = match[1], start = body.selectionStart - query.length;
    // A temporary suffix lets a bare @/# pass the Markdown prose token check.
    const probe = body.value.slice(0, start) + query + 'x' + body.value.slice(body.selectionStart);
    if (!markers(probe).some(m => m.start === start)) { this.hideSuggestions(); return; }
    const choices = query.startsWith('@') ? this.inventory().bots.map(b => mentionHandle(b, note.nodeId)) : this.allNotes().flatMap(n => hashtags(n.markdown).map(t => '#' + t));
    this.suggestions = [...new Set(choices)].filter(value => value.toLocaleLowerCase().startsWith(query.toLocaleLowerCase()) && value !== query).sort().slice(0, 12);
    this.completion = { start, end: body.selectionStart }; this.suggestionIndex = 0; this.renderSuggestions();
  }
  private renderSuggestions() {
    const picker = this.q('[data-suggestions]'); picker.hidden = !this.suggestions.length;
    this.q('[data-body]').setAttribute('aria-expanded', String(!!this.suggestions.length));
    picker.innerHTML = this.suggestions.map((s, i) => `<button type="button" id="${this.suggestionsId}-${i}" role="option" aria-selected="${i === this.suggestionIndex}" data-suggestion="${i}">${esc(s)}</button>`).join('');
    if (this.suggestions.length) this.q('[data-body]').setAttribute('aria-activedescendant', `${this.suggestionsId}-${this.suggestionIndex}`);
    else this.q('[data-body]').removeAttribute('aria-activedescendant');
    for (const button of picker.querySelectorAll<HTMLButtonElement>('button')) {
      button.onmousedown = event => event.preventDefault();
      button.onclick = () => this.insertSuggestion(Number(button.dataset.suggestion));
    }
  }
  private completionKey(event: KeyboardEvent) {
    if (!this.suggestions.length || event.isComposing) return;
    if (event.key === 'Escape') { event.preventDefault(); this.hideSuggestions(); }
    else if (event.key === 'ArrowDown' || event.key === 'ArrowUp') { event.preventDefault(); this.suggestionIndex = (this.suggestionIndex + (event.key === 'ArrowDown' ? 1 : -1) + this.suggestions.length) % this.suggestions.length; this.renderSuggestions(); }
    else if (event.key === 'Enter' || event.key === 'Tab') { event.preventDefault(); this.insertSuggestion(this.suggestionIndex); }
  }
  private insertSuggestion(index: number) {
    if (!this.completion) return;
    const body = this.q<HTMLTextAreaElement>('[data-body]');
    body.setRangeText(this.suggestions[index] + ' ', this.completion.start, this.completion.end, 'end');
    this.hideSuggestions(); this.edited(); body.focus();
  }
}
