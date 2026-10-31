import type { editor, languages, IDisposable, IRange, Uri } from 'monaco-editor';
import type { WorkspaceEdit, PublishDiagnosticsParams, Range } from 'vscode-languageserver-protocol';
import { instances, splitID } from '../instances';
import { api } from './api';
import * as editorAPI from './editorApi';
import { monaco } from './language';
import { LSPClient } from './lspClient';
import type { FileRef, FileSnapshot } from './types';
import { installLSPProviders } from './lspProviders';
import { validateTextEdits } from './lspEdits';

export const lspRange = (range: Range): IRange => ({ startLineNumber: range.start.line + 1, startColumn: range.start.character + 1, endLineNumber: range.end.line + 1, endColumn: range.end.character + 1 });
export type GoEditorHooks = {
  open(ref: FileRef): Promise<editor.ITextModel | null>;
  load(ref: FileRef): Promise<editor.ITextModel | null>;
  revision(uri: string): string | undefined;
  status(state: string, message: string): void;
};
type Document = { model: editor.ITextModel; snapshot: FileSnapshot; session?: GoSession; subscription: IDisposable; dispose: IDisposable };
type GoSession = { directory: string; client: LSPClient; opened: Set<string>; ready: Promise<void> };
const managers = new Map<string, GoLanguage>();
export const workspaceModelAuthority = (workspace: string): string => splitID(workspace).join('-');

export class GoLanguage {
  readonly documents = new Map<string, Document>();
  private sessions = new Map<string, GoSession>();
  private starting = new Map<string, Promise<GoSession>>();
  private hooks = new Set<GoEditorHooks>();
  private disposables: IDisposable[] = [];
  private stopped = false;
  private disposed = false;
  private epoch = 0;
  private sourceModels = new Set<editor.ITextModel>();
  private retry = 0;
  private retryTimer = 0;
  state = 'idle'; message = '';
  log = '';
  private constructor(readonly workspace: string) { this.disposables = installLSPProviders(this); }
  static acquire(workspace: string, hooks: GoEditorHooks): { service: GoLanguage; dispose(): void } {
    let service = managers.get(workspace); if (!service) { service = new GoLanguage(workspace); managers.set(workspace, service); }
    service.hooks.add(hooks); hooks.status(service.state, service.message);
    return { service, dispose: () => { service!.hooks.delete(hooks); if (!service!.hooks.size) { service!.dispose(); managers.delete(workspace); } } };
  }
  get base(): string { return `/api/v1/plugins/go/bots/${encodeURIComponent(this.workspace)}/language`; }
  owns(model: editor.ITextModel): boolean { return this.documents.has(model.uri.toString()); }
  wireURI(uri: string): string {
    const parsed = monaco.Uri.parse(uri);
    if (parsed.scheme === 'echo-go') return uri;
    if (parsed.scheme !== 'file' || parsed.authority !== workspaceModelAuthority(this.workspace)) throw new Error('Document belongs to another workspace');
    const [, root, ...parts] = parsed.path.split('/');
    return monaco.Uri.from({ scheme: 'echo-workspace', authority: root, path: '/' + parts.join('/') }).toString();
  }
  modelURI(uri: string): Uri {
    const parsed = monaco.Uri.parse(uri);
    return parsed.scheme === 'echo-workspace' ? monaco.Uri.from({ scheme: 'file', authority: workspaceModelAuthority(this.workspace), path: `/${parsed.authority}${parsed.path}` }) : parsed;
  }
  ref(uri: string): FileRef {
    const parsed = this.modelURI(uri); if (parsed.scheme !== 'file' || parsed.authority !== workspaceModelAuthority(this.workspace)) throw new Error('Dependency sources cannot be edited');
    const [, rootId, ...path] = parsed.path.split('/'); return { rootId, path: path.join('/') };
  }
  attach(model: editor.ITextModel, snapshot: FileSnapshot): void {
    if (!/\.go$|(^|\/)go\.(mod|work)$/.test(snapshot.ref.path) || this.disposed) return;
    const uri = model.uri.toString(); const existing = this.documents.get(uri); if (existing) { existing.snapshot = snapshot; return; }
    const doc: Document = { model, snapshot, subscription: model.onDidChangeContent(() => {
      if (doc.session?.opened.has(uri)) {
        try { doc.session.client.notify('textDocument/didChange', { textDocument: { uri: this.wireURI(uri), version: model.getVersionId() }, contentChanges: [{ text: model.getValue() }] }); }
        catch { doc.session?.client.close(); }
      }
    }), dispose: model.onWillDispose(() => this.detach(uri)) };
    this.documents.set(uri, doc);
    if (!this.stopped) void this.session(doc).catch(error => { if (this.state !== 'installation-required') this.status('failed', String(error.message || error)); });
  }
  saved(model: editor.ITextModel, snapshot: FileSnapshot): void {
    const doc = this.documents.get(model.uri.toString()); if (!doc) return; doc.snapshot = snapshot;
    if (doc.session?.opened.has(model.uri.toString())) {
      try { doc.session.client.notify('textDocument/didSave', { textDocument: { uri: this.wireURI(model.uri.toString()) } }); }
      catch { doc.session?.client.close(); }
    }
  }
  private detach(uri: string): void {
    const doc = this.documents.get(uri); if (!doc) return;
    if (doc.session?.opened.delete(uri)) { try { doc.session.client.notify('textDocument/didClose', { textDocument: { uri: this.wireURI(uri) } }); } catch {} }
    doc.subscription.dispose(); doc.dispose.dispose(); this.documents.delete(uri);
  }
  private status(state: string, message = ''): void { this.state = state; this.message = message; for (const hook of this.hooks) hook.status(state, message); }
  reportFailure(message: string): void { if (this.state !== 'installation-required') this.status('failed', message); }
  private async session(doc: Document): Promise<GoSession> {
    const epoch = this.epoch;
    if (doc.session) { await doc.session.ready; return doc.session; }
    const owner = instances.get(splitID(this.workspace)[0]);
    if (owner && !owner.node.capabilities?.includes('go-language/1')) throw new Error('This Echo host does not support Go language services');
    const directory = doc.snapshot.hostPath.replace(/[\\/][^\\/]+$/, '');
    const status = await api(`${this.base}/status`, { query: { directory } });
    if (status.state !== 'ready') { this.status(status.state, status.message); throw new Error(status.message); }
    if (this.stopped || doc.model.isDisposed() || epoch !== this.epoch) throw new Error('Editor session changed');
    let session = this.sessions.get(status.directory);
    if (!session) {
      let pending = this.starting.get(status.directory);
      if (!pending) { pending = this.connect(status.directory); this.starting.set(status.directory, pending); const started = pending; void pending.finally(() => { if (this.starting.get(status.directory) === started) this.starting.delete(status.directory); }).catch(() => {}); }
      session = await pending;
    }
    if (epoch !== this.epoch || this.stopped) throw new Error('Editor session changed');
    doc.session = session; await session.ready;
    this.open(doc, session); return session;
  }
  private async connect(directory: string): Promise<GoSession> {
    this.status('starting', 'Loading Go packages');
    const session = {} as GoSession; session.directory = directory; session.opened = new Set();
    session.client = new LSPClient(`${this.base}/session?${new URLSearchParams({ directory })}`, message => {
      if (message.method === 'echo/status') this.status(message.params.state, message.params.message);
      if (message.method === 'textDocument/publishDiagnostics') this.diagnostics(message.params);
      if (message.method === 'window/logMessage' || message.method === 'window/showMessage') this.log = (this.log + message.params.message + '\n').slice(-16384);
      if (message.method === 'window/showMessage') this.status(message.params.type === 1 ? 'failed' : 'ready', message.params.message);
      if (message.method === '$/progress' && message.params.value?.kind === 'begin') this.status('indexing', message.params.value.title || 'Loading Go packages');
      if (message.method === '$/progress' && message.params.value?.kind === 'end' && this.state !== 'failed') this.status('ready');
    }, () => {
      if (this.sessions.get(directory) === session) this.sessions.delete(directory);
      for (const doc of this.documents.values()) if (doc.session === session) { doc.session = undefined; monaco.editor.setModelMarkers(doc.model, 'go', []); }
      if (!this.stopped) { this.status('disconnected'); if (this.retry++ < 3) { clearTimeout(this.retryTimer); this.retryTimer = window.setTimeout(() => { for (const doc of this.documents.values()) void this.session(doc).catch(error => this.status('failed', error.message)); }, 2500); } }
    });
    session.ready = session.client.ready; this.sessions.set(directory, session);
    await session.ready; return session;
  }
  private open(doc: Document, session: GoSession): void {
    const uri = doc.model.uri.toString(); if (session.opened.has(uri) || doc.model.isDisposed()) return;
    session.client.notify('textDocument/didOpen', { textDocument: { uri: this.wireURI(uri), languageId: doc.snapshot.ref.path.endsWith('go.mod') ? 'go.mod' : doc.snapshot.ref.path.endsWith('go.work') ? 'go.work' : 'go', version: doc.model.getVersionId(), text: doc.model.getValue() } });
    session.opened.add(uri);
  }
  private diagnostics(params: PublishDiagnosticsParams): void {
    const model = monaco.editor.getModel(this.modelURI(params.uri)); if (!model || params.version !== undefined && params.version !== model.getVersionId()) return;
    monaco.editor.setModelMarkers(model, 'go', params.diagnostics.map(d => ({ ...lspRange(d.range), message: d.message, severity: d.severity === 1 ? monaco.MarkerSeverity.Error : d.severity === 2 ? monaco.MarkerSeverity.Warning : d.severity === 4 ? monaco.MarkerSeverity.Hint : monaco.MarkerSeverity.Info, source: d.source, code: d.code === undefined ? undefined : String(d.code) })));
  }
  async request(model: editor.ITextModel, method: string, params: any, signal?: AbortSignal): Promise<any> {
    const doc = this.documents.get(model.uri.toString()); if (!doc) return null;
    const version = model.getVersionId(); const session = await this.session(doc);
    const result = await session.client.call(method, params, signal);
    if (model.isDisposed() || model.getVersionId() !== version) return null;
    return result;
  }
  async install(model?: editor.ITextModel | null): Promise<void> {
    const doc = model && this.documents.get(model.uri.toString()) || this.documents.values().next().value;
    if (!doc) throw new Error('Open a saved Go file first');
    this.pause();
    this.status('installing', 'Installing the supported Go language server');
    try { await api(`${this.base}/install`, { method: 'POST', body: { directory: doc.snapshot.hostPath.replace(/[\\/][^\\/]+$/, '') } }); if (!this.disposed) this.restart(); }
    catch (error) { if (!this.disposed) { this.stopped = false; this.status('failed', (error as Error).message); } throw error; }
  }
  private pause(): void {
    this.epoch++; this.stopped = true; clearTimeout(this.retryTimer); for (const session of this.sessions.values()) session.client.close(); this.sessions.clear(); this.starting.clear();
    for (const doc of this.documents.values()) doc.session = undefined;
  }
  restart(): void {
    if (this.disposed) return;
    this.pause();
    this.stopped = false; this.retry = 0;
    for (const doc of this.documents.values()) void this.session(doc).catch(error => this.status('failed', error.message));
  }
  async ensureSource(uri: string): Promise<editor.ITextModel | null> {
    const resource = this.modelURI(uri); const current = monaco.editor.getModel(resource); if (current) return current;
    if (resource.scheme === 'echo-go') {
      for (const model of this.sourceModels) if (model.isDisposed()) this.sourceModels.delete(model);
      if (this.sourceModels.size >= 128) throw new Error('Go source preview limit reached; close source tabs or reopen the editor');
      const source = await api(`${this.base}/source`, { query: { uri } });
      const model = monaco.editor.getModel(resource) || monaco.editor.createModel(source.content, 'go', resource); this.sourceModels.add(model); return model;
    }
    const ref = this.ref(uri);
    for (const hook of this.hooks) { const model = await hook.load(ref); if (model) return model; }
    return null;
  }
  async prepareEdits(edit: WorkspaceEdit, baseline: Map<string, number>, revisions: Record<string, string>): Promise<languages.IWorkspaceTextEdit[]> {
    const groups = new Map<string, { edits: any[]; version?: number | null }>();
    for (const [uri, edits] of Object.entries(edit.changes || {})) groups.set(uri, { edits });
    for (const change of edit.documentChanges || []) {
      if (!('textDocument' in change)) throw new Error('File operations are not supported by Go rename');
      if (groups.has(change.textDocument.uri)) throw new Error('Duplicate language edit target');
      groups.set(change.textDocument.uri, { edits: change.edits, version: change.textDocument.version });
    }
    const prepared: Array<{ model: editor.ITextModel; version: number; edits: any[] }> = [];
    for (const [uri, group] of groups) {
      validateTextEdits(group.edits);
      const ref = this.ref(uri); let model: editor.ITextModel | null = null;
      for (const hook of this.hooks) { model = await hook.open(ref); if (model) break; }
      if (!model) throw new Error('Could not open rename target');
      const version = model.getVersionId();
      if (group.version != null && group.version !== version || baseline.has(model.uri.toString()) && baseline.get(model.uri.toString()) !== version) throw new Error('A file changed during rename; retry');
      const disk = await editorAPI.readFile(this.workspace, ref);
      const expected = [...this.hooks].map(h => h.revision(model.uri.toString())).find(Boolean);
      if (!expected || expected !== disk.revision || revisions[uri] !== disk.revision) throw new Error('An edit target changed on disk; resolve the conflict first');
      for (const edit of group.edits) {
        const range = lspRange(edit.range); const valid = model.validateRange(range);
        if (!monaco.Range.equalsRange(range, valid)) throw new Error('Invalid language edit range');
      }
      prepared.push({ model, version, edits: group.edits });
    }
    if (prepared.some(p => p.model.isDisposed() || p.model.getVersionId() !== p.version)) throw new Error('Files changed while preparing edits');
    return prepared.flatMap(p => p.edits.map(edit => ({ resource: p.model.uri, versionId: p.version, textEdit: { range: lspRange(edit.range), text: edit.newText } })));
  }
  versions(): Map<string, number> { return new Map([...this.documents].map(([uri, doc]) => [uri, doc.model.getVersionId()])); }
  dispose(): void {
    this.disposed = true; this.epoch++; this.stopped = true; clearTimeout(this.retryTimer);
    for (const session of this.sessions.values()) session.client.close();
    for (const uri of [...this.documents.keys()]) this.detach(uri);
    for (const item of this.disposables) item.dispose();
    for (const model of this.sourceModels) if (!model.isDisposed()) model.dispose(); this.sourceModels.clear();
  }
}
