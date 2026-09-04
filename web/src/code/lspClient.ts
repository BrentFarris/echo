import type * as Monaco from "monaco-editor";
import { monaco } from "./language";
import { profileMatchesDocument } from "./languageMap";
import type {
  LSPCodeAction, LSPCommand, LSPProfile, LSPRange, LSPStatus, LSPWorkspaceEdit, WorkspaceLSPConfig, WorkspaceLSPResponse,
} from "./lspTypes";
import { registerLSPProviders } from "./lspProviders";

type PendingRequest = {
  resolve(value: unknown): void;
  reject(error: Error): void;
  timer?: number;
  cancellation?: Monaco.IDisposable;
};

type TrackedModel = {
  model: Monaco.editor.ITextModel;
  profileId: string;
  leased: boolean;
  denied: boolean;
  change: Monaco.IDisposable;
  dispose: Monaco.IDisposable;
};

export type LSPDocumentState = "none" | "connecting" | "starting" | "owned" | "denied" | "failed";
export type LSPDiagnosticSeverity = "error" | "warning";

type TrackedDiagnostic = {
  uri: string;
  severity: LSPDiagnosticSeverity;
};

export type LSPClientOptions = {
  workspaceId: string;
  initial: WorkspaceLSPResponse;
  prepareWorkspaceEdit(edit: LSPWorkspaceEdit): Promise<Monaco.languages.WorkspaceEdit>;
  applyWorkspaceEdit(edit: LSPWorkspaceEdit): Promise<boolean>;
  isURIAllowed(uri: string): boolean;
  diagnosticKey(uri: string): string;
  prepareURI(uri: string): Promise<boolean>;
  onDocumentState(state: LSPDocumentState, status?: LSPStatus): void;
  onDiagnosticsChange(uri: string, severity: LSPDiagnosticSeverity | null): void;
  onMessage(message: string, sticky?: boolean): void;
};

export class EchoLSPClient {
  private readonly options: LSPClientOptions;
  private socket: WebSocket | null = null;
  private disposed = false;
  private reconnectTimer = 0;
  private reconnectDelay = 500;
  private requestSequence = 0;
  private pending = new Map<string, PendingRequest>();
  private tracked = new Map<string, TrackedModel>();
  private activeURI = "";
  private profiles: LSPProfile[];
  private config: WorkspaceLSPConfig;
  private statuses = new Map<string, LSPStatus>();
  private diagnostics = new Map<string, Map<string, TrackedDiagnostic>>();
  private providerDisposables: Monaco.IDisposable[] = [];
  private commandDisposable: Monaco.IDisposable | null = null;
  private codeLensListeners = new Set<(provider: Monaco.languages.CodeLensProvider) => unknown>();

  constructor(options: LSPClientOptions) {
    this.options = options;
    this.profiles = options.initial.profiles || [];
    this.config = options.initial.config || {};
    for (const status of options.initial.statuses || []) this.statuses.set(status.profileId, status);
    this.registerProviders();
    this.connect();
  }

  dispose(): void {
    if (this.disposed) return;
    this.disposed = true;
    window.clearTimeout(this.reconnectTimer);
    this.socket?.close();
    this.socket = null;
    for (const request of this.pending.values()) {
      request.timer && window.clearTimeout(request.timer);
      request.cancellation?.dispose();
      request.reject(new Error("Language server connection closed"));
    }
    this.pending.clear();
    for (const item of this.tracked.values()) {
      item.change.dispose();
      item.dispose.dispose();
    }
    this.tracked.clear();
    this.providerDisposables.forEach((item) => item.dispose());
    this.providerDisposables = [];
    this.commandDisposable?.dispose();
    this.commandDisposable = null;
    this.codeLensListeners.clear();
    this.clearAllDiagnostics();
  }

  effectiveProfiles(): LSPProfile[] {
    return this.profiles.map((profile) => ({ ...profile }));
  }

  workspaceConfig(): WorkspaceLSPConfig {
    return { ...this.config };
  }

  trackModel(model: Monaco.editor.ITextModel): void {
    const uri = model.uri.toString();
    if (model.uri.scheme !== "file" || this.tracked.has(uri)) return;
    const profile = this.profileForDocument(model);
    if (!profile) return;
    const tracked: TrackedModel = {
      model, profileId: profile.id, leased: false, denied: false,
      change: { dispose() {} }, dispose: { dispose() {} },
    };
    tracked.change = model.onDidChangeContent((event) => this.didChange(tracked, event));
    tracked.dispose = model.onWillDispose(() => {
      this.send({ type: "lsp_close", profileId: tracked.profileId, uri });
      tracked.change.dispose();
      this.tracked.delete(uri);
      if (this.activeURI === uri) {
        this.activeURI = "";
        this.options.onDocumentState("none");
      }
    });
    this.tracked.set(uri, tracked);
  }

  activateModel(model: Monaco.editor.ITextModel | null): void {
    this.activeURI = model?.uri.scheme === "file" ? model.uri.toString() : "";
    if (!model || !this.activeURI) {
      this.options.onDocumentState("none");
      return;
    }
    this.trackModel(model);
    const tracked = this.tracked.get(this.activeURI);
    if (!tracked) {
      this.options.onDocumentState("none");
      return;
    }
    if (tracked.leased) {
      this.options.onDocumentState("owned", this.statuses.get(tracked.profileId));
      return;
    }
    this.claim(tracked, false);
  }

  takeOverActiveDocument(): void {
    const tracked = this.tracked.get(this.activeURI);
    if (tracked) this.claim(tracked, true);
  }

  owns(model: Monaco.editor.ITextModel): boolean {
    return this.tracked.get(model.uri.toString())?.leased === true;
  }

  profileForModel(model: Monaco.editor.ITextModel): LSPProfile | undefined {
    const tracked = this.tracked.get(model.uri.toString());
    return tracked ? this.profiles.find((profile) => profile.id === tracked.profileId) : this.profileForDocument(model);
  }

  statusForModel(model: Monaco.editor.ITextModel): LSPStatus | undefined {
    const profile = this.profileForModel(model);
    return profile ? this.statuses.get(profile.id) : undefined;
  }

  supports(profileId: string, capability: string): boolean {
    const status = this.statuses.get(profileId);
    if (status?.state !== "running") return false;
    let current: unknown = status.capabilities || {};
    for (const part of capability.split(".")) {
      if (!current || typeof current !== "object") return false;
      current = (current as Record<string, unknown>)[part];
    }
    return current !== undefined && current !== null && current !== false;
  }

  capability<T>(profileId: string, capability: string): T | undefined {
    const status = this.statuses.get(profileId);
    let current: unknown = status?.capabilities || {};
    for (const part of capability.split(".")) {
      if (!current || typeof current !== "object") return undefined;
      current = (current as Record<string, unknown>)[part];
    }
    return current as T | undefined;
  }

  isURIAllowed(uri: string): boolean {
    return this.options.isURIAllowed(uri);
  }

  prepareURI(uri: string): Promise<boolean> {
    return this.options.prepareURI(uri);
  }

  async requestForModel<T>(model: Monaco.editor.ITextModel, method: string, params: unknown, token?: Monaco.CancellationToken, timeoutMS = 15000): Promise<T> {
    const profile = this.profileForModel(model);
    if (!profile) throw new Error("No language server handles this document");
    if (!this.owns(model)) throw new Error("This browser does not own LSP synchronization for the document");
    return this.request<T>(profile.id, method, params, token, timeoutMS);
  }

  async request<T>(profileId: string, method: string, params: unknown, token?: Monaco.CancellationToken, timeoutMS = 15000): Promise<T> {
    if (this.socket?.readyState !== WebSocket.OPEN) throw new Error("Language server connection is not open");
    const id = `browser-${++this.requestSequence}`;
    return new Promise<T>((resolve, reject) => {
      const pending: PendingRequest = { resolve: (value) => resolve(value as T), reject };
      if (timeoutMS > 0) {
        pending.timer = window.setTimeout(() => {
          this.pending.delete(id);
          this.send({ type: "lsp_cancel", id });
          reject(new Error(`Language server request timed out after ${timeoutMS} ms`));
        }, timeoutMS);
      }
      if (token) {
        pending.cancellation = token.onCancellationRequested(() => {
          this.pending.delete(id);
          pending.timer && window.clearTimeout(pending.timer);
          this.send({ type: "lsp_cancel", id });
          reject(new Error("Language server request was cancelled"));
        });
      }
      this.pending.set(id, pending);
      this.send({ type: "lsp_request", id, profileId, method, params });
    });
  }

  didSave(model: Monaco.editor.ITextModel): void {
    const tracked = this.tracked.get(model.uri.toString());
    if (!tracked?.leased) return;
    this.send({ type: "lsp_save", profileId: tracked.profileId, uri: model.uri.toString(), text: model.getValue() });
  }

  async format(model: Monaco.editor.ITextModel, range?: Monaco.Range, timeoutMS = 15000): Promise<boolean> {
    const profile = this.profileForModel(model);
    if (!profile || !this.owns(model)) throw new Error("Language server formatting is unavailable for this document");
    const method = range ? "textDocument/rangeFormatting" : "textDocument/formatting";
    const params: Record<string, unknown> = {
      textDocument: { uri: model.uri.toString() },
      options: {
        tabSize: model.getOptions().tabSize,
        insertSpaces: model.getOptions().insertSpaces,
        trimTrailingWhitespace: true,
        insertFinalNewline: false,
        trimFinalNewlines: false,
      },
    };
    if (range) params.range = toLSPRange(range);
    const edits = await this.requestForModel<Array<{ range: LSPRange; newText: string }> | null>(model, method, params, undefined, timeoutMS);
    if (!edits?.length) return false;
    model.pushEditOperations([], edits.map((edit) => ({ range: fromLSPRange(edit.range), text: edit.newText, forceMoveMarkers: true })), () => null);
    return true;
  }

  async organizeImports(model: Monaco.editor.ITextModel, timeoutMS = 15000): Promise<boolean> {
    const profile = this.profileForModel(model);
    if (!profile || !this.owns(model)) throw new Error("Language server import organization is unavailable for this document");
    if (!this.supports(profile.id, "codeActionProvider")) return false;
    const actions = await this.requestForModel<Array<LSPCodeAction | LSPCommand> | null>(model, "textDocument/codeAction", {
      textDocument: { uri: model.uri.toString() },
      range: toLSPRange(model.getFullModelRange()),
      context: { diagnostics: [], only: ["source.organizeImports"], triggerKind: 2 },
    }, undefined, timeoutMS);
    for (const candidate of actions || []) {
      if (isLSPCommand(candidate)) {
        await this.request(profile.id, "workspace/executeCommand", {
          command: candidate.command, arguments: candidate.arguments || [],
        }, undefined, timeoutMS);
        return true;
      }
      let action = candidate;
      if (action.disabled) continue;
      if (!action.edit && !action.command && action.data !== undefined && this.supports(profile.id, "codeActionProvider.resolveProvider")) {
        action = await this.request<LSPCodeAction>(profile.id, "codeAction/resolve", action, undefined, timeoutMS);
        if (action.disabled) continue;
      }
      let applied = false;
      if (action.edit) {
        if (!(await this.options.applyWorkspaceEdit(action.edit))) throw new Error("Language server import edit could not be applied");
        applied = true;
      }
      if (action.command) {
        await this.request(profile.id, "workspace/executeCommand", {
          command: action.command.command, arguments: action.command.arguments || [],
        }, undefined, timeoutMS);
        applied = true;
      }
      if (applied) return true;
    }
    return false;
  }

  async formatBeforeSave(model: Monaco.editor.ITextModel): Promise<void> {
    if (this.config.formatOnSave !== true) return;
    const timeout = this.config.formatOnSaveTimeoutMs || 3000;
    const deadline = Date.now() + timeout;
    const remaining = () => Math.max(1, deadline - Date.now());
    const failures: string[] = [];
    try {
      await this.organizeImports(model, remaining());
    } catch (error) {
      failures.push(`import organization: ${error instanceof Error ? error.message : String(error)}`);
    }
    try {
      await this.format(model, undefined, remaining());
    } catch (error) {
      failures.push(`formatting: ${error instanceof Error ? error.message : String(error)}`);
    }
    if (failures.length) this.options.onMessage(`Saved without all LSP save actions: ${failures.join("; ")}`);
  }

  prepareWorkspaceEdit(edit: LSPWorkspaceEdit): Promise<Monaco.languages.WorkspaceEdit> {
    return this.options.prepareWorkspaceEdit(edit);
  }

  executeCommand(profileId: string, command: string, args?: unknown[]): Promise<unknown> {
    return this.request(profileId, "workspace/executeCommand", { command, arguments: args || [] });
  }

  onCodeLensRefresh(listener: (provider: Monaco.languages.CodeLensProvider) => unknown): Monaco.IDisposable {
    this.codeLensListeners.add(listener);
    return { dispose: () => this.codeLensListeners.delete(listener) };
  }

  refreshCodeLenses(): void {
    for (const listener of [...this.codeLensListeners]) listener(undefined as unknown as Monaco.languages.CodeLensProvider);
  }

  async workspaceSymbols(query: string): Promise<Array<{ profileId: string; symbols: any[] }>> {
    const profiles = this.profiles.filter((profile) => this.supports(profile.id, "workspaceSymbolProvider"));
    return Promise.all(profiles.map(async (profile) => ({
      profileId: profile.id,
      symbols: await this.request<any[]>(profile.id, "workspace/symbol", { query }) || [],
    })));
  }

  private connect(): void {
    if (this.disposed) return;
    const protocol = location.protocol === "https:" ? "wss" : "ws";
    this.socket = new WebSocket(`${protocol}://${location.host}/api/workspaces/${encodeURIComponent(this.options.workspaceId)}/lsp/ws`);
    this.socket.onopen = () => {
      this.reconnectDelay = 500;
      if (this.activeURI) this.options.onDocumentState("connecting");
    };
    this.socket.onmessage = (event) => this.receive(event.data);
    this.socket.onerror = () => {};
    this.socket.onclose = () => {
      if (this.disposed) return;
      for (const tracked of this.tracked.values()) {
        tracked.leased = false;
        tracked.denied = false;
      }
      for (const request of this.pending.values()) request.reject(new Error("Language server connection closed"));
      this.pending.clear();
      if (this.activeURI) this.options.onDocumentState("connecting");
      this.reconnectTimer = window.setTimeout(() => this.connect(), this.reconnectDelay);
      this.reconnectDelay = Math.min(this.reconnectDelay * 2, 15000);
    };
  }

  private receive(data: string): void {
    let message: Record<string, any>;
    try {
      message = JSON.parse(data);
    } catch {
      return;
    }
    switch (message.type) {
      case "lsp_ready":
      case "lsp_configuration":
        this.clearAllDiagnostics();
        this.config = message.config || {};
        this.profiles = message.profiles || [];
        this.statuses.clear();
        for (const status of message.statuses || []) this.statuses.set(status.profileId, status);
        this.rebindTrackedModels();
        this.registerProviders();
        if (this.activeURI) {
          const tracked = this.tracked.get(this.activeURI);
          if (tracked) this.claim(tracked, false);
        }
        break;
      case "lsp_response":
        this.finishRequest(message.id, message.result, null);
        break;
      case "lsp_error":
        if (message.id) this.finishRequest(message.id, undefined, new Error(message.error || "Language server request failed"));
        else if (message.error && !String(message.error).includes("another browser owns")) this.options.onMessage(message.error);
        break;
      case "lsp_status":
        this.handleStatus(message.status as LSPStatus);
        break;
      case "lsp_lease_granted": {
        const tracked = this.tracked.get(message.uri);
        if (tracked && tracked.profileId === message.profileId) {
          tracked.leased = true;
          tracked.denied = false;
          if (this.activeURI === message.uri) this.options.onDocumentState("owned", this.statuses.get(tracked.profileId));
        }
        break;
      }
      case "lsp_lease_denied": {
        const tracked = this.tracked.get(message.uri);
        if (tracked && tracked.profileId === message.profileId) {
          tracked.leased = false;
          tracked.denied = true;
          this.clearDocumentDiagnostics(tracked.profileId, message.uri);
          if (this.activeURI === message.uri) this.options.onDocumentState("denied", this.statuses.get(tracked.profileId));
        }
        break;
      }
      case "lsp_lease_revoked": {
        const tracked = this.tracked.get(message.uri);
        if (tracked && tracked.profileId === message.profileId) {
          tracked.leased = false;
          tracked.denied = true;
          this.clearDocumentDiagnostics(tracked.profileId, message.uri);
          if (this.activeURI === message.uri) {
            this.options.onDocumentState("denied", this.statuses.get(tracked.profileId));
            this.options.onMessage("Another browser took over language-server synchronization for this file.");
          }
        }
        break;
      }
      case "lsp_notification":
        this.handleNotification(message.profileId, message.method, message.params);
        break;
      case "lsp_server_request":
        void this.handleServerRequest(message);
        break;
    }
  }

  private finishRequest(id: string, result: unknown, error: Error | null): void {
    const request = this.pending.get(id);
    if (!request) return;
    this.pending.delete(id);
    request.timer && window.clearTimeout(request.timer);
    request.cancellation?.dispose();
    if (error) request.reject(error);
    else request.resolve(result);
  }

  private claim(tracked: TrackedModel, takeOver: boolean): void {
    const status = this.statuses.get(tracked.profileId);
    if (status?.state !== "running") {
      this.options.onDocumentState(status?.state === "failed" ? "failed" : "starting", status);
      return;
    }
    if (this.socket?.readyState !== WebSocket.OPEN) {
      this.options.onDocumentState("connecting", status);
      return;
    }
    this.options.onDocumentState("starting", status);
    this.send({
      type: "lsp_claim", profileId: tracked.profileId, takeOver,
      document: {
        uri: tracked.model.uri.toString(), languageId: tracked.model.getLanguageId(),
        version: tracked.model.getVersionId(), text: tracked.model.getValue(),
      },
    });
  }

  private didChange(tracked: TrackedModel, event: Monaco.editor.IModelContentChangedEvent): void {
    if (!tracked.leased) return;
    const status = this.statuses.get(tracked.profileId);
    const sync = status?.capabilities?.textDocumentSync;
    const changeKind = typeof sync === "number" ? sync : (sync as Record<string, unknown> | undefined)?.change;
    const changes = changeKind === 2
      ? event.changes.map((change) => ({ range: toLSPRange(change.range), rangeLength: change.rangeLength, text: change.text }))
      : [{ text: tracked.model.getValue() }];
    this.send({
      type: "lsp_change", profileId: tracked.profileId, uri: tracked.model.uri.toString(),
      version: tracked.model.getVersionId(), contentChanges: changes,
    });
  }

  private handleStatus(status: LSPStatus): void {
    this.statuses.set(status.profileId, status);
    if (status.state === "running") {
      // Trigger characters and other provider metadata arrive with the
      // initialize result. Re-register adapters once that metadata is live.
      this.registerProviders();
      this.refreshCodeLenses();
    }
    if (status.state !== "running") {
      for (const tracked of this.tracked.values()) {
        if (tracked.profileId === status.profileId) tracked.leased = false;
      }
      this.clearDiagnostics(status.profileId);
    }
    const active = this.tracked.get(this.activeURI);
    if (active?.profileId === status.profileId) {
      if (status.state === "running" && !active.leased && !active.denied) this.claim(active, false);
      else if (status.state === "failed") this.options.onDocumentState("failed", status);
      else if (!active.leased) this.options.onDocumentState("starting", status);
    }
  }

  private handleNotification(profileId: string, method: string, params: any): void {
    if (method === "window/showMessage") {
      this.options.onMessage(String(params?.message || "Language server message"), Number(params?.type || 3) <= 2);
      return;
    }
    if (method === "window/logMessage") {
      if (Number(params?.type || 4) <= 2) this.options.onMessage(String(params?.message || "Language server error"));
      return;
    }
    if (method !== "textDocument/publishDiagnostics") return;
    const uri = String(params?.uri || "");
    if (!uri) return;
    const diagnostics = Array.isArray(params?.diagnostics) ? params.diagnostics : [];
    this.setDiagnosticSeverity(profileId, uri, diagnosticDecorationSeverity(diagnostics));
    const model = monaco.editor.getModel(monaco.Uri.parse(uri));
    if (!model) return;
    const markers: Monaco.editor.IMarkerData[] = diagnostics.map((diagnostic: any) => ({
      ...markerRange(diagnostic.range),
      severity: diagnosticSeverity(diagnostic.severity),
      message: String(diagnostic.message || "Language server diagnostic"),
      source: diagnostic.source || this.profiles.find((profile) => profile.id === profileId)?.name || "LSP",
      code: typeof diagnostic.code === "string" || typeof diagnostic.code === "number" ? String(diagnostic.code) : undefined,
      relatedInformation: (diagnostic.relatedInformation || []).filter((related: any) => this.isURIAllowed(related.location?.uri || "")).map((related: any) => ({
        resource: monaco.Uri.parse(related.location.uri),
        ...markerRange(related.location.range),
        message: String(related.message || ""),
      })),
    }));
    monaco.editor.setModelMarkers(model, markerOwner(profileId), markers);
  }

  private async handleServerRequest(message: Record<string, any>): Promise<void> {
    if (message.method === "workspace/codeLens/refresh") {
      this.refreshCodeLenses();
      this.send({ type: "lsp_server_response", id: message.id, profileId: message.profileId, result: null });
      return;
    }
    if (message.method !== "workspace/applyEdit") {
      this.send({ type: "lsp_server_response", id: message.id, profileId: message.profileId, error: { code: -32601, message: "Unsupported server request" } });
      return;
    }
    try {
      const applied = await this.options.applyWorkspaceEdit(message.params?.edit || {});
      this.send({ type: "lsp_server_response", id: message.id, profileId: message.profileId, result: { applied } });
    } catch (error) {
      this.send({
        type: "lsp_server_response", id: message.id, profileId: message.profileId,
        result: { applied: false, failureReason: error instanceof Error ? error.message : String(error) },
      });
    }
  }

  private profileForDocument(model: Monaco.editor.ITextModel): LSPProfile | undefined {
    return this.profiles.find((profile) => profileMatchesDocument(profile, model.getLanguageId(), model.uri.path));
  }

  private rebindTrackedModels(): void {
    for (const [uri, tracked] of [...this.tracked]) {
      const profile = this.profileForDocument(tracked.model);
      if (!profile) {
        this.send({ type: "lsp_close", profileId: tracked.profileId, uri });
        this.clearDocumentDiagnostics(tracked.profileId, uri);
        tracked.change.dispose();
        tracked.dispose.dispose();
        this.tracked.delete(uri);
      } else {
        if (profile.id !== tracked.profileId) {
          this.send({ type: "lsp_close", profileId: tracked.profileId, uri });
          this.clearDocumentDiagnostics(tracked.profileId, uri);
        }
        tracked.profileId = profile.id;
        tracked.leased = false;
        tracked.denied = false;
      }
    }
  }

  private registerProviders(): void {
    this.providerDisposables.forEach((item) => item.dispose());
    this.providerDisposables = registerLSPProviders(this);
    this.commandDisposable?.dispose();
    this.commandDisposable = monaco.editor.registerCommand("echo.lsp.executeCommand", (_accessor, profileId: string, command: string, args?: unknown[]) => {
      void this.executeCommand(profileId, command, args).catch((error) => this.options.onMessage(error instanceof Error ? error.message : String(error), true));
    });
  }

  private clearDiagnostics(profileId: string): void {
    for (const diagnostic of [...(this.diagnostics.get(profileId)?.values() || [])]) {
      this.setDiagnosticSeverity(profileId, diagnostic.uri, null);
    }
    for (const model of monaco.editor.getModels()) monaco.editor.setModelMarkers(model, markerOwner(profileId), []);
  }

  private clearAllDiagnostics(): void {
    const profileIDs = new Set([...this.profiles.map((profile) => profile.id), ...this.diagnostics.keys()]);
    for (const profileID of profileIDs) this.clearDiagnostics(profileID);
  }

  private clearDocumentDiagnostics(profileId: string, uri: string): void {
    this.setDiagnosticSeverity(profileId, uri, null);
    let model: Monaco.editor.ITextModel | null = null;
    try {
      model = monaco.editor.getModel(monaco.Uri.parse(uri));
    } catch {
      return;
    }
    if (model) monaco.editor.setModelMarkers(model, markerOwner(profileId), []);
  }

  private setDiagnosticSeverity(profileId: string, uri: string, severity: LSPDiagnosticSeverity | null): void {
    const key = this.options.diagnosticKey(uri) || uri;
    const previous = this.diagnosticSeverityForKey(key);
    let profileDiagnostics = this.diagnostics.get(profileId);
    if (severity) {
      if (!profileDiagnostics) {
        profileDiagnostics = new Map();
        this.diagnostics.set(profileId, profileDiagnostics);
      }
      profileDiagnostics.set(key, { uri, severity });
    } else if (profileDiagnostics) {
      profileDiagnostics.delete(key);
      if (!profileDiagnostics.size) this.diagnostics.delete(profileId);
    }
    const next = this.diagnosticSeverityForKey(key);
    if (next !== previous) this.options.onDiagnosticsChange(uri, next);
  }

  private diagnosticSeverityForKey(key: string): LSPDiagnosticSeverity | null {
    let warning = false;
    for (const diagnostics of this.diagnostics.values()) {
      const severity = diagnostics.get(key)?.severity;
      if (severity === "error") return "error";
      if (severity === "warning") warning = true;
    }
    return warning ? "warning" : null;
  }

  private send(message: unknown): boolean {
    if (this.socket?.readyState !== WebSocket.OPEN) return false;
    this.socket.send(JSON.stringify(message));
    return true;
  }
}

export function toLSPPosition(position: Monaco.Position): { line: number; character: number } {
  return { line: position.lineNumber - 1, character: position.column - 1 };
}

export function toLSPRange(range: Monaco.IRange): LSPRange {
  return {
    start: { line: range.startLineNumber - 1, character: range.startColumn - 1 },
    end: { line: range.endLineNumber - 1, character: range.endColumn - 1 },
  };
}

export function fromLSPRange(range: LSPRange): Monaco.Range {
  return new monaco.Range(range.start.line + 1, range.start.character + 1, range.end.line + 1, range.end.character + 1);
}

function isLSPCommand(value: LSPCodeAction | LSPCommand): value is LSPCommand {
  return typeof value.command === "string";
}

function markerRange(range: LSPRange): Pick<Monaco.editor.IMarkerData, "startLineNumber" | "startColumn" | "endLineNumber" | "endColumn"> {
  return {
    startLineNumber: range?.start?.line + 1 || 1,
    startColumn: range?.start?.character + 1 || 1,
    endLineNumber: range?.end?.line + 1 || 1,
    endColumn: range?.end?.character + 1 || 1,
  };
}

function diagnosticSeverity(value: number | undefined): Monaco.MarkerSeverity {
  if (value === 1) return monaco.MarkerSeverity.Error;
  if (value === 2) return monaco.MarkerSeverity.Warning;
  if (value === 3) return monaco.MarkerSeverity.Info;
  return monaco.MarkerSeverity.Hint;
}

function diagnosticDecorationSeverity(diagnostics: Array<{ severity?: number }>): LSPDiagnosticSeverity | null {
  let warning = false;
  for (const diagnostic of diagnostics) {
    if (diagnostic?.severity === 1) return "error";
    if (diagnostic?.severity === 2) warning = true;
  }
  return warning ? "warning" : null;
}

function markerOwner(profileId: string): string {
  return `echo-lsp:${profileId}`;
}
