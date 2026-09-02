import type { editor as MonacoEditor, IDisposable } from "monaco-editor";
import { on as onSocket, onState as onSocketState, send as sendSocket } from "../../js/ws.js";
import type { FileRef } from "../code/types";
import { refKey } from "../code/types";
import { monaco } from "../code/language";
import { copyText, escapeHTML, promptDialog, showContextMenu, toast } from "../code/ui";
import { openWorkbenchPanel, registerWorkbenchPanel } from "../terminal";
import {
  addAdapterProfile, addAdapterTemplate, dapRequest, deleteAdapterProfile, diagnoseAdapter, disconnectDebug, listDebugProcesses, loadDebugConfig,
  loadDebugSnapshot, previewVSCodeImport, restartDebug, restartDebugGroup, saveDebugConfig, saveDebugState, setDebugTrace,
  startDebug, stopDebug, stopDebugGroup, terminateDebug, updateAdapterProfile,
} from "./api";
import {
  activeDebugSession, applyDebugEvent, breakpointDecorationClass, capability,
  commandSupported, debugKeyAction,
} from "./state";
import {
  debugStopNotificationPermission, debugStopNotificationsEnabled,
  requestDebugStopNotificationPermission, setDebugStopNotificationsEnabled,
} from "./debugNotifications";
import type {
  AdapterProfile, DAPScope, DAPStackFrame, DAPThread, DAPVariable, DataBreakpoint, DebugConfiguration,
  DebugEvent, DebugInput, DebugOutput, DebugPersistentState, DebugSession, DebugSnapshot,
  DebugSource, SourceBreakpoint, WorkspaceDebugConfig,
} from "./types";

export type DebugViewOptions = {
  workspaceId: string;
  host: HTMLElement;
  toolbarHost: HTMLElement;
  editor: MonacoEditor.IStandaloneCodeEditor;
  signal: AbortSignal;
  activeFile(): FileRef | null;
  selectedText(): string;
  saveAll(requireActiveFile?: boolean): Promise<boolean>;
  showSidebar(): void;
  openSource(source: DebugSource, line: number, column: number): Promise<void>;
  openVirtualSource(title: string, content: string, mimeType?: string): Promise<void>;
};

type FrameSelection = { sessionId: string; threadId: number; frame: DAPStackFrame };
type WatchResult = { value?: string; type?: string; variablesReference?: number; error?: string };
type PanelKind = "debug-console" | "debug-output";
type DebugBrowserPreferences = {
  selectedSessionId?: string;
  collapsed?: string[];
  consoleHistory?: string[];
  frame?: { sessionId: string; threadId: number; frameId: number };
};

const defaultSnapshot = (workspaceId: string): DebugSnapshot => ({
  workspaceId, sequence: 0, sessions: [], groups: [], state: { revision: 0 },
});

export class DebugView {
  private readonly options: DebugViewOptions;
  private config: WorkspaceDebugConfig = { version: 1 };
  private profiles: AdapterProfile[] = [];
  private templates: Array<{ id: string; description: string; installGuide: string; profile: AdapterProfile }> = [];
  private snapshot: DebugSnapshot;
  private selectedSessionId = "";
  private selectedLaunchId = "";
  private frameSelection: FrameSelection | null = null;
  private threads = new Map<string, DAPThread[]>();
  private frames = new Map<string, DAPStackFrame[]>();
  private frameTotals = new Map<string, number>();
  private completeFrameStacks = new Set<string>();
  private scopes: DAPScope[] = [];
  private variables = new Map<number, DAPVariable[]>();
  private variableTotals = new Map<number, number>();
  private completeVariableReferences = new Set<number>();
  private expandedVariables = new Set<number>();
  private watchResults = new Map<string, WatchResult>();
  private exceptionDetails = new Map<string, { exceptionId?: string; description?: string; breakMode?: string; details?: { message?: string; typeName?: string; stackTrace?: string } }>();
  private modules: Array<Record<string, unknown>> = [];
  private loadedSources: DebugSource[] = [];
  private consoleHistory: string[] = [];
  private consoleHistoryIndex = 0;
  private consoleEntries: Array<{ expression?: string; value: string; category: string }> = [];
  private consoleFilter = "";
  private outputFilter = "";
  private consoleClearedAt = 0;
  private outputClearedAt = 0;
  private progress = new Map<string, { sessionId: string; progressId: string; title: string; message?: string; percentage?: number; cancellable?: boolean }>();
  private inspectionGeneration = 0;
  private breakpointDecorations: string[] = [];
  private inlineValueDecorations: string[] = [];
  private breakpointDecorationIDs = new Map<string, string>();
  private breakpointPersistTimer = 0;
  private disposables: IDisposable[] = [];
  private unregisterPanels: Array<() => void> = [];
  private unsubscribeSocket: Array<() => void> = [];
  private resyncPromise: Promise<void> | null = null;
  private collapsed = new Set<string>();
  private sessionDataBreakpoints = new Map<string, DataBreakpoint[]>();
  private savedFrame: DebugBrowserPreferences["frame"];

  constructor(options: DebugViewOptions) {
    this.options = options;
    this.snapshot = defaultSnapshot(options.workspaceId);
    const preferences = readDebugBrowserPreferences(options.workspaceId);
    this.selectedSessionId = preferences.selectedSessionId || "";
    this.collapsed = new Set(preferences.collapsed || []);
    this.consoleHistory = (preferences.consoleHistory || []).slice(-100);
    this.consoleHistoryIndex = this.consoleHistory.length;
    this.savedFrame = preferences.frame;
    this.installEditorIntegration();
    this.installEvents();
    this.registerPanels();
  }

  async start(): Promise<void> {
    this.options.host.innerHTML = `<div class="debug-loading"><span class="codicon codicon-loading codicon-modifier-spin"></span> Loading debugger…</div>`;
    try {
      const [configResult, snapshot] = await Promise.all([
        loadDebugConfig(this.options.workspaceId), loadDebugSnapshot(this.options.workspaceId),
      ]);
      if (this.options.signal.aborted) return;
      this.config = configResult.config || { version: 1 };
      this.profiles = configResult.profiles || [];
      this.templates = configResult.templates || [];
      this.applySnapshot(snapshot);
      this.selectedLaunchId = snapshot.state.selectedConfigurationId
        || this.config.configurations?.[0]?.id
        || this.config.compounds?.[0]?.id || "";
      this.subscribe();
      this.render();
      const stopped = activeDebugSession(this.snapshot, this.selectedSessionId);
      if (stopped?.status === "stopped") void this.inspectStopped(stopped);
    } catch (error) {
      this.options.host.innerHTML = `<div class="debug-empty"><span class="codicon codicon-error"></span><p>${escapeHTML(errorMessage(error))}</p><button type="button" data-debug-action="retry">Retry</button></div>`;
    }
  }

  dispose(): void {
    window.clearTimeout(this.breakpointPersistTimer);
    this.inspectionGeneration++;
    this.clearInlineValues();
    for (const dispose of this.disposables) dispose.dispose();
    for (const unregister of this.unregisterPanels) unregister();
    for (const unsubscribe of this.unsubscribeSocket) unsubscribe();
    sendSocket({ type: "debug_unsubscribe", workspaceId: this.options.workspaceId });
    this.options.toolbarHost.innerHTML = "";
    this.persistBrowserPreferences();
  }

  onEditorContextChanged(): void {
    this.refreshEditorDecorations();
    this.clearInlineValues();
    const session = this.activeSession();
    if (session?.status === "stopped" && this.frameSelection) void this.loadInlineValues(session, this.frameSelection.frame, this.inspectionGeneration);
  }

  handleKeydown(event: KeyboardEvent): boolean {
    const target = event.target as HTMLElement | null;
    const action = debugKeyAction(event, {
      codeActive: true,
      modalOpen: Boolean(document.querySelector(".code-modal-overlay, .code-picker-overlay, .debug-settings-overlay")),
      inputFocused: Boolean(target?.closest("input, textarea, select, [contenteditable=true]")),
    });
    if (!action) return false;
    event.preventDefault();
    event.stopPropagation();
    if (action === "view") this.options.showSidebar();
    else if (action === "toggle") void this.toggleLifecycle();
    else if (action === "stop") void this.stopActive();
    else if (action === "restart") void this.restartActive();
    else if (action === "breakpoint") void this.toggleBreakpointAtCursor();
    else void this.control(action);
    return true;
  }

  async toggleLifecycle(): Promise<void> {
    const session = this.activeSession();
    if (!session || session.status === "terminated" || session.status === "failed") {
      await this.launch(false);
    } else if (session.status === "running") {
      await this.control("pause");
    } else if (session.status === "stopped") {
      await this.control("continue");
    }
  }

  openSettings(): void { this.showSettings(); }

  async launch(noDebug: boolean): Promise<void> {
    if (!this.selectedLaunchId) {
      this.showSettings();
      return;
    }
    if (!(await this.options.saveAll(this.launchRequiresActiveFile()))) return;
    const configuration = this.config.configurations?.find((item) => item.id === this.selectedLaunchId);
    const compound = this.config.compounds?.find((item) => item.id === this.selectedLaunchId);
    if (!configuration && !compound) {
      toast("Select a debug configuration or compound.");
      return;
    }
    const inputs = await this.collectInputs(this.config.inputs || []);
    if (inputs === null) return;
    const request = {
      ...(configuration ? { configurationId: configuration.id } : { compoundId: compound!.id }),
      currentFile: this.options.activeFile() || undefined,
      selectedText: this.options.selectedText(), inputs, noDebug,
    };
    try {
      this.applySnapshot(await startDebug(this.options.workspaceId, request));
      this.render();
    } catch (error) {
      toast(errorMessage(error), { sticky: true });
    }
  }

  async control(command: string): Promise<void> {
    const session = this.activeSession();
    if (!session || !commandSupported(session, command)) return;
    try {
      await dapRequest(this.options.workspaceId, session.id, command, session.revision, session.stopGeneration, {});
      if (["continue", "next", "stepIn", "stepOut", "stepBack", "reverseContinue"].includes(command)) {
        this.invalidateInspection();
      }
    } catch (error) {
      await this.handleRequestError(error);
    }
  }

  async controlAll(command: "pause" | "continue"): Promise<void> {
    const targets = this.snapshot.sessions.filter((session) => command === "pause" ? session.status === "running" : session.status === "stopped");
    await Promise.all(targets.map(async (session) => {
      try { await dapRequest(this.options.workspaceId, session.id, command, session.revision, session.stopGeneration, {}); }
      catch (error) { await this.handleRequestError(error); }
    }));
    if (command === "continue") this.invalidateInspection();
  }

  async restartActiveCompound(): Promise<void> {
    const groupId = this.activeSession()?.groupId;
    if (!groupId) return;
    try { this.applySnapshot(await restartDebugGroup(this.options.workspaceId, groupId, this.groupRevisions(groupId))); this.render(); }
    catch (error) { await this.handleRequestError(error); }
  }

  async stopActiveCompound(): Promise<void> {
    const groupId = this.activeSession()?.groupId;
    if (groupId) await this.stopGroup(groupId);
  }

  async stopActive(terminateDebuggee?: boolean): Promise<void> {
    const session = this.activeSession();
    if (!session) return;
    try {
      const snapshot = terminateDebuggee === true
        ? await terminateDebug(this.options.workspaceId, session.id, session.revision)
        : session.request === "attach"
          ? await disconnectDebug(this.options.workspaceId, session.id, session.revision)
          : await stopDebug(this.options.workspaceId, session.id, session.revision, terminateDebuggee);
      this.applySnapshot(snapshot);
      this.render();
    } catch (error) { await this.handleRequestError(error); }
  }

  async restartActive(): Promise<void> {
    const session = this.activeSession();
    if (!session) return;
    try {
      this.applySnapshot(await restartDebug(this.options.workspaceId, session.id, session.revision));
      this.render();
    } catch (error) { await this.handleRequestError(error); }
  }

  async toggleBreakpointAtCursor(): Promise<void> {
    const ref = this.options.activeFile();
    const position = this.options.editor.getPosition();
    if (!ref || !position) return;
    await this.toggleSourceBreakpoint(ref, position.lineNumber);
  }

  async runToCursorAtEditor(): Promise<void> {
    const ref = this.options.activeFile();
    const position = this.options.editor.getPosition();
    if (ref && position) await this.runToCursor(ref, position.lineNumber, position.column);
  }

  openPanel(kind: "debug-console" | "debug-output"): void {
    openWorkbenchPanel(this.options.workspaceId, kind, true);
  }

  private subscribe(): void {
    this.unsubscribeSocket.push(onSocket("debug_event", (raw: object) => this.receiveEvent(raw as DebugEvent)));
    this.unsubscribeSocket.push(onSocket("debug_subscribed", (raw: object) => {
      if ((raw as { workspaceId?: string }).workspaceId === this.options.workspaceId) void this.resync();
    }));
    this.unsubscribeSocket.push(onSocketState((state) => {
      if (state === "open") sendSocket({ type: "debug_subscribe", workspaceId: this.options.workspaceId });
    }));
    sendSocket({ type: "debug_subscribe", workspaceId: this.options.workspaceId });
  }

  private receiveEvent(event: DebugEvent): void {
    if (event.workspaceId !== this.options.workspaceId) return;
    const next = applyDebugEvent(this.snapshot, event);
    if (!next) {
      void this.resync();
      return;
    }
    this.snapshot = next;
    if (event.session && !this.selectedSessionId) this.selectedSessionId = event.session.id;
    if (event.output) {
      if (["adapter", "lifecycle", "echo", "dap"].includes(event.output.category)) openWorkbenchPanel(this.options.workspaceId, "debug-output");
      else if (event.output.category !== "telemetry") openWorkbenchPanel(this.options.workspaceId, "debug-console");
      this.renderPanel("debug-console");
      this.renderPanel("debug-output");
    }
    if (event.sessionId && ["progressStart", "progressUpdate", "progressEnd"].includes(event.event)) {
      const body = (event.body || {}) as { progressId?: string; title?: string; message?: string; percentage?: number; cancellable?: boolean };
      const key = `${event.sessionId}:${body.progressId || ""}`;
      if (event.event === "progressEnd") this.progress.delete(key);
      else {
        const previous = this.progress.get(key);
        this.progress.set(key, {
          sessionId: event.sessionId, progressId: body.progressId || previous?.progressId || "",
          title: body.title || previous?.title || "Debugger progress", message: body.message ?? previous?.message,
          percentage: body.percentage ?? previous?.percentage, cancellable: body.cancellable ?? previous?.cancellable,
        });
      }
      this.renderPanel("debug-output");
    }
    if (event.event === "stopped" && event.session) {
      this.selectedSessionId = event.session.id;
      if (event.session.stoppedReason !== "exception") this.exceptionDetails.delete(event.session.id);
      void this.inspectStopped(event.session);
    } else if (["continued", "session_running", "terminated", "failed", "invalidated"].includes(event.event)) {
      this.invalidateInspection();
      if (event.event === "failed") openWorkbenchPanel(this.options.workspaceId, "debug-output");
      if ((event.event === "terminated" || event.event === "failed") && event.sessionId) this.sessionDataBreakpoints.delete(event.sessionId);
      if ((event.event === "terminated" || event.event === "failed") && event.sessionId) {
        for (const [key, value] of this.progress) if (value.sessionId === event.sessionId) this.progress.delete(key);
      }
    }
    this.render();
  }

  private async resync(): Promise<void> {
    if (this.resyncPromise) return this.resyncPromise;
    this.resyncPromise = loadDebugSnapshot(this.options.workspaceId)
      .then((snapshot) => {
        this.applySnapshot(snapshot);
        this.render();
        const session = this.activeSession();
        if (session?.status === "stopped") void this.inspectStopped(session);
      })
      .catch((error) => toast(errorMessage(error)))
      .finally(() => { this.resyncPromise = null; });
    return this.resyncPromise;
  }

  private applySnapshot(snapshot: DebugSnapshot): void {
    this.snapshot = snapshot ? {
      ...snapshot,
      sessions: snapshot.sessions || [],
      groups: snapshot.groups || [],
      state: snapshot.state || { revision: 0 },
    } : defaultSnapshot(this.options.workspaceId);
    const selected = activeDebugSession(this.snapshot, this.selectedSessionId);
    this.selectedSessionId = selected?.id || "";
    this.refreshEditorDecorations();
    this.renderToolbar();
    this.renderPanel("debug-console");
    this.renderPanel("debug-output");
  }

  private activeSession(): DebugSession | undefined {
    return activeDebugSession(this.snapshot, this.selectedSessionId);
  }

  private render(): void {
    const session = this.activeSession();
    const launches = [
      ...(this.config.configurations || []).map((item) => ({ id: item.id, name: item.name, kind: item.request })),
      ...(this.config.compounds || []).map((item) => ({ id: item.id, name: item.name, kind: "compound" })),
    ];
    this.options.host.innerHTML = `
      <header class="debug-sidebar-header">
        <span>RUN AND DEBUG</span>
        <button type="button" title="Debug Settings" aria-label="Debug Settings" data-debug-action="settings"><span class="codicon codicon-settings-gear"></span></button>
      </header>
      <div class="debug-launch-row">
        <select aria-label="Debug configuration" data-debug-launch ${launches.length ? "" : "disabled"}>
          ${launches.length ? launches.map((item) => `<option value="${escapeHTML(item.id)}" ${item.id === this.selectedLaunchId ? "selected" : ""}>${escapeHTML(item.name)} · ${escapeHTML(item.kind)}</option>`).join("") : `<option>Configure a debugger…</option>`}
        </select>
        <button type="button" class="debug-start" title="Start Debugging (F8)" aria-label="Start Debugging" data-debug-action="start" ${launches.length ? "" : "disabled"}><span class="codicon codicon-debug-start"></span></button>
        <button type="button" title="Start Without Debugging" aria-label="Start Without Debugging" data-debug-action="start-without" ${launches.length ? "" : "disabled"}><span class="codicon codicon-run"></span></button>
      </div>
      ${launches.length ? "" : `<div class="debug-empty"><span class="codicon codicon-debug-alt"></span><p>Create or import a launch configuration for this workspace.</p><button type="button" data-debug-action="settings">Configure Debugging</button></div>`}
      ${session ? this.renderSessionPicker(session) : ""}
      <div class="debug-sidebar-scroll">
        ${this.renderSection("variables", "VARIABLES", this.renderVariables(), session?.status === "stopped")}
        ${this.renderSection("watch", "WATCH", this.renderWatches(), true, `<button type="button" aria-label="Add Watch Expression" title="Add Watch Expression" data-debug-action="add-watch"><span class="codicon codicon-add"></span></button>`)}
        ${this.renderSection("callstack", "CALL STACK", this.renderCallStack(), true)}
        ${this.renderSection("breakpoints", "BREAKPOINTS", this.renderBreakpoints(), true, `<button type="button" aria-label="Add Function Breakpoint" title="Add Function Breakpoint" data-debug-action="add-function-breakpoint"><span class="codicon codicon-add"></span></button><button type="button" aria-label="Remove All Breakpoints" title="Remove All Breakpoints" data-debug-action="remove-all-breakpoints"><span class="codicon codicon-clear-all"></span></button>`)}
        ${session && capability(session, "supportsModulesRequest") ? this.renderSection("modules", "MODULES", this.renderModules(), true) : ""}
        ${session && capability(session, "supportsLoadedSourcesRequest") ? this.renderSection("sources", "LOADED SOURCES", this.renderLoadedSources(), true) : ""}
      </div>`;
    this.renderToolbar();
    this.refreshEditorDecorations();
  }

  private renderSection(id: string, label: string, content: string, visible: boolean, actions = ""): string {
    if (!visible) return "";
    const collapsed = this.collapsed.has(id);
    return `<section class="debug-section" data-debug-section="${id}">
      <header><button type="button" data-debug-action="toggle-section" data-section-id="${id}" aria-expanded="${!collapsed}"><span class="codicon codicon-chevron-${collapsed ? "right" : "down"}"></span>${label}</button><span>${actions}</span></header>
      <div class="debug-section-content" ${collapsed ? "hidden" : ""}>${content}</div>
    </section>`;
  }

  private renderSessionPicker(active: DebugSession): string {
    const live = this.snapshot.sessions.filter((session) => !["terminated", "failed"].includes(session.status));
    return `<div class="debug-session-row"><span class="debug-status is-${active.status}"></span><select aria-label="Active debug session" data-debug-session>${live.map((session) => `<option value="${session.id}" ${session.id === active.id ? "selected" : ""}>${escapeHTML(session.configuration)} · ${escapeHTML(session.status)}</option>`).join("")}</select></div>`;
  }

  private renderToolbar(): void {
    const session = this.activeSession();
    const live = this.snapshot.sessions.filter((item) => !["terminated", "failed"].includes(item.status));
    if (!session || live.length === 0) {
      this.options.toolbarHost.innerHTML = "";
      return;
    }
    const stopped = session.status === "stopped";
    const running = session.status === "running";
    const controllable = stopped || running;
    const canTerminateDebuggee = capability(session, "supportsTerminateRequest") || capability(session, "supportTerminateDebuggee");
    this.options.toolbarHost.innerHTML = `<div class="debug-floating-toolbar" role="toolbar" aria-label="Debug controls">
      ${live.length > 1 ? `<select aria-label="Active session" data-debug-toolbar-session>${live.map((item) => `<option value="${item.id}" ${item.id === session.id ? "selected" : ""}>${escapeHTML(item.configuration)}</option>`).join("")}</select>` : `<span class="debug-toolbar-label">${escapeHTML(session.configuration)}</span>`}
      <button type="button" title="${stopped ? "Continue (F8)" : "Pause (F8)"}" aria-label="${stopped ? "Continue" : "Pause"}" data-debug-control="${stopped ? "continue" : "pause"}" ${controllable ? "" : "disabled"}><span class="codicon codicon-debug-${stopped ? "continue" : "pause"}"></span></button>
      <button type="button" title="Step Over (F10)" aria-label="Step Over" data-debug-control="next" ${stopped ? "" : "disabled"}><span class="codicon codicon-debug-step-over"></span></button>
      <button type="button" title="Step Into (F11)" aria-label="Step Into" data-debug-control="stepIn" ${stopped ? "" : "disabled"}><span class="codicon codicon-debug-step-into"></span></button>
      <button type="button" title="Step Out (Shift+F11)" aria-label="Step Out" data-debug-control="stepOut" ${stopped ? "" : "disabled"}><span class="codicon codicon-debug-step-out"></span></button>
      ${commandSupported(session, "stepBack") ? `<button type="button" title="Step Back" aria-label="Step Back" data-debug-control="stepBack" ${stopped ? "" : "disabled"}><span class="codicon codicon-debug-step-back"></span></button>` : ""}
      ${commandSupported(session, "reverseContinue") ? `<button type="button" title="Reverse Continue" aria-label="Reverse Continue" data-debug-control="reverseContinue" ${stopped ? "" : "disabled"}><span class="codicon codicon-debug-continue-small"></span></button>` : ""}
      <button type="button" title="Restart (Ctrl+Shift+F8)" aria-label="Restart" data-debug-action="restart" ${controllable ? "" : "disabled"}><span class="codicon codicon-debug-restart"></span></button>
      <button type="button" title="${session.request === "attach" ? "Disconnect" : "Stop"} (Shift+F8)" aria-label="${session.request === "attach" ? "Disconnect" : "Stop"}" data-debug-action="stop"><span class="codicon codicon-debug-${session.request === "attach" ? "disconnect" : "stop"}"></span></button>
      ${session.request === "attach" && canTerminateDebuggee ? `<button type="button" title="Terminate Process" aria-label="Terminate Process" data-debug-action="terminate-debuggee"><span class="codicon codicon-debug-stop"></span></button>` : ""}
      ${session.groupId ? `<button type="button" title="Stop Compound" aria-label="Stop Compound" data-debug-action="stop-group" data-group-id="${session.groupId}"><span class="codicon codicon-circle-slash"></span></button>` : ""}
      ${session.groupId ? `<button type="button" title="Restart Compound" aria-label="Restart Compound" data-debug-action="restart-group" data-group-id="${session.groupId}"><span class="codicon codicon-debug-restart"></span></button>` : ""}
    </div>`;
  }

  private renderCallStack(): string {
    if (!this.snapshot.sessions.length) return `<p class="debug-section-empty">No debug sessions.</p>`;
    return this.snapshot.sessions.map((session) => {
      const threads = session.status === "stopped" ? this.threads.get(session.id) || [] : [];
      const threadRows = threads.map((thread) => {
        const key = `${session.id}:${thread.id}`;
        const frames = session.status === "stopped" ? this.frames.get(key) || [] : [];
        const total = this.frameTotals.get(key) || 0;
        const more = frames.length > 0 && !this.completeFrameStacks.has(key) && (!total || frames.length < total);
        return `<div class="debug-thread"><button type="button" class="debug-thread-title" data-debug-thread data-session-id="${session.id}" data-thread-id="${thread.id}"><span class="codicon codicon-server-process"></span>${escapeHTML(thread.name)}</button>${frames.map((frame) => `<button type="button" class="debug-frame ${this.frameSelection?.frame.id === frame.id && this.frameSelection.sessionId === session.id ? "is-selected" : ""}" data-debug-action="select-frame" data-session-id="${session.id}" data-thread-id="${thread.id}" data-frame-id="${frame.id}"><strong>${escapeHTML(frame.name)}</strong><span>${escapeHTML(frame.source?.name || frame.source?.path || "")}:${frame.line}</span></button>`).join("")}${more ? `<button type="button" class="debug-load-row" data-debug-action="load-more-frames" data-session-id="${session.id}" data-thread-id="${thread.id}">Load more frames…${total ? ` (${frames.length}/${total})` : ""}</button>` : ""}</div>`;
      }).join("");
      const exception = this.exceptionDetails.get(session.id);
      const exceptionText = exception ? `${exception.exceptionId || exception.details?.typeName || "Exception"}${exception.description || exception.details?.message ? `: ${exception.description || exception.details?.message}` : ""}` : "";
      return `<div class="debug-stack-session"><div class="debug-stack-session-title"><span class="debug-status is-${session.status}"></span><strong>${escapeHTML(session.configuration)}</strong><span>${escapeHTML(session.status)}</span></div>${exceptionText ? `<div class="debug-exception-detail" title="${escapeHTML(exception?.details?.stackTrace || "")}">${escapeHTML(exceptionText)}</div>` : ""}${threadRows || `<p class="debug-section-empty">${session.status === "stopped" ? "Loading stack…" : escapeHTML(session.stoppedText || session.error || session.status)}</p>`}</div>`;
    }).join("");
  }

  private renderVariables(): string {
    if (!this.frameSelection) return `<p class="debug-section-empty">Variables appear while paused.</p>`;
    if (!this.scopes.length) return `<p class="debug-section-empty">Loading variables…</p>`;
    return this.scopes.map((scope) => `<div class="debug-scope"><div class="debug-scope-title">${escapeHTML(scope.name)}</div>${this.renderVariableList(scope.variablesReference)}</div>`).join("");
  }

  private renderVariableList(reference: number): string {
    const values = this.variables.get(reference);
    if (!values) return `<button type="button" class="debug-load-row" data-debug-action="expand-variable" data-variable-reference="${reference}">Load values…</button>`;
    if (!values.length) return `<p class="debug-section-empty">No values.</p>`;
    const total = this.variableTotals.get(reference) || 0;
    const more = !this.completeVariableReferences.has(reference) && (!total || values.length < total);
    return `<div role="tree">${values.map((variable) => {
      const expandable = variable.variablesReference > 0;
      const expanded = expandable && this.expandedVariables.has(variable.variablesReference);
      return `<div class="debug-variable" role="treeitem" aria-expanded="${expandable ? expanded : undefined}">
        <div><button type="button" class="debug-tree-toggle" aria-label="${expanded ? "Collapse" : "Expand"} ${escapeHTML(variable.name)}" data-debug-action="expand-variable" data-variable-reference="${variable.variablesReference}" ${expandable ? "" : "disabled"}><span class="codicon codicon-${expandable ? `chevron-${expanded ? "down" : "right"}` : "blank"}"></span></button><span class="debug-variable-name">${escapeHTML(variable.name)}</span><span class="debug-variable-value" title="${escapeHTML(variable.value)}">${escapeHTML(variable.value)}</span><span class="debug-variable-type">${escapeHTML(variable.type || "")}</span><button type="button" class="debug-row-action" title="More Actions" aria-label="Variable Actions" data-debug-action="variable-menu" data-parent-reference="${reference}" data-variable-name="${escapeHTML(variable.name)}"><span class="codicon codicon-ellipsis"></span></button></div>
        ${expanded ? this.renderVariableList(variable.variablesReference) : ""}
      </div>`;
    }).join("")}${more ? `<button type="button" class="debug-load-row" data-debug-action="load-more-variables" data-variable-reference="${reference}">Load more…${total ? ` (${values.length}/${total})` : ""}</button>` : ""}</div>`;
  }

  private renderWatches(): string {
    const watches = this.snapshot.state.watches || [];
    if (!watches.length) return `<p class="debug-section-empty">Add an expression to watch.</p>`;
    return watches.map((watch) => {
      const result = this.watchResults.get(watch.id);
      return `<div class="debug-watch-row" data-debug-watch-id="${watch.id}"><input type="checkbox" aria-label="Enable ${escapeHTML(watch.expression)}" data-debug-action="toggle-watch" data-watch-id="${watch.id}" ${watch.enabled ? "checked" : ""}><button type="button" class="debug-watch-expression" data-debug-action="edit-watch" data-watch-id="${watch.id}">${escapeHTML(watch.expression)}</button><span class="${result?.error ? "is-error" : ""}" title="${escapeHTML(result?.error || result?.value || "")}">${escapeHTML(result?.error || result?.value || (watch.enabled ? "" : "disabled"))}</span><button type="button" aria-label="Remove Watch" title="Remove Watch" data-debug-action="remove-watch" data-watch-id="${watch.id}"><span class="codicon codicon-close"></span></button></div>`;
    }).join("");
  }

  private renderBreakpoints(): string {
    const source = this.snapshot.state.sourceBreakpoints || [];
    const functions = this.snapshot.state.functionBreakpoints || [];
    const instructions = this.snapshot.state.instructionBreakpoints || [];
    const data = [
      ...(this.snapshot.state.dataBreakpoints || []),
      ...(this.activeSession() ? this.sessionDataBreakpoints.get(this.activeSession()!.id) || [] : []),
    ];
    const configuredExceptions = this.snapshot.state.exceptionBreakpoints || [];
    const advertisedExceptions = (this.activeSession()?.capabilities?.exceptionBreakpointFilters || []) as Array<{ filter: string; label?: string; description?: string }>;
    const exceptions = [...configuredExceptions];
    for (const advertised of advertisedExceptions) if (!exceptions.some((item) => item.filter === advertised.filter)) exceptions.push({ filter: advertised.filter, enabled: false });
    if (!source.length && !functions.length && !instructions.length && !data.length && !exceptions.length) return `<p class="debug-section-empty">No breakpoints.</p>`;
    const sourceRows = source.map((item) => `<div class="debug-breakpoint-row"><input type="checkbox" data-debug-action="toggle-breakpoint-enabled" data-breakpoint-id="${item.id}" ${item.enabled ? "checked" : ""}><button type="button" data-debug-action="open-breakpoint" data-breakpoint-id="${item.id}"><span class="debug-breakpoint-dot ${breakpointDecorationClass(item, this.snapshot.sessions)}"></span><strong>${escapeHTML(item.source.path.split("/").pop() || item.source.path)}</strong><span>:${item.line}</span>${item.condition ? `<small>if ${escapeHTML(item.condition)}</small>` : ""}${item.hitCondition ? `<small>hit ${escapeHTML(item.hitCondition)}</small>` : ""}${item.logMessage ? `<small>${escapeHTML(item.logMessage)}</small>` : ""}</button><button type="button" aria-label="Edit Breakpoint" data-debug-action="edit-breakpoint" data-breakpoint-id="${item.id}"><span class="codicon codicon-edit"></span></button><button type="button" aria-label="Remove Breakpoint" data-debug-action="remove-breakpoint" data-breakpoint-id="${item.id}"><span class="codicon codicon-close"></span></button></div>`).join("");
    const functionRows = functions.map((item) => `<div class="debug-breakpoint-row"><input type="checkbox" data-debug-action="toggle-function-breakpoint" data-breakpoint-id="${item.id}" ${item.enabled ? "checked" : ""}><span class="codicon codicon-symbol-function"></span><strong>${escapeHTML(item.name)}</strong><button type="button" aria-label="Edit Function Breakpoint" data-debug-action="edit-function-breakpoint" data-breakpoint-id="${item.id}"><span class="codicon codicon-edit"></span></button><button type="button" aria-label="Remove Function Breakpoint" data-debug-action="remove-function-breakpoint" data-breakpoint-id="${item.id}"><span class="codicon codicon-close"></span></button></div>`).join("");
    const exceptionRows = exceptions.map((item) => { const advertised = advertisedExceptions.find((candidate) => candidate.filter === item.filter); return `<div class="debug-exception-row" title="${escapeHTML(advertised?.description || "")}"><label><input type="checkbox" data-debug-action="toggle-exception-breakpoint" data-filter-id="${escapeHTML(item.filter)}" ${item.enabled ? "checked" : ""}><span>${escapeHTML(advertised?.label || item.filter)}</span></label><button type="button" aria-label="Edit Exception Breakpoint" data-debug-action="edit-exception-breakpoint" data-filter-id="${escapeHTML(item.filter)}"><span class="codicon codicon-edit"></span></button></div>`; }).join("");
    const dataRows = data.length ? `<p class="debug-subheading">Data breakpoints</p>${data.map((item) => `<div class="debug-breakpoint-row"><span class="codicon codicon-debug-breakpoint-data"></span><strong>${escapeHTML(item.name || item.dataId)}</strong><small>${escapeHTML(item.accessType || "write")}${item.sessionOnly ? " · session only" : ""}</small><button type="button" aria-label="Edit Data Breakpoint" data-debug-action="edit-data-breakpoint" data-breakpoint-id="${item.id}"><span class="codicon codicon-edit"></span></button><button type="button" aria-label="Remove Data Breakpoint" data-debug-action="remove-data-breakpoint" data-breakpoint-id="${item.id}"><span class="codicon codicon-close"></span></button></div>`).join("")}` : "";
    return `${sourceRows}${functionRows}${exceptionRows}${dataRows}${instructions.length ? `<p class="debug-subheading">Instruction breakpoints</p>${instructions.map((item) => `<div class="debug-breakpoint-row"><span class="codicon codicon-debug-breakpoint-data"></span><strong>${escapeHTML(item.instructionReference)}</strong><button type="button" aria-label="Edit Instruction Breakpoint" data-debug-action="edit-instruction-breakpoint" data-breakpoint-id="${item.id}"><span class="codicon codicon-edit"></span></button><button type="button" aria-label="Remove Instruction Breakpoint" data-debug-action="remove-instruction-breakpoint" data-breakpoint-id="${item.id}"><span class="codicon codicon-close"></span></button></div>`).join("")}` : ""}`;
  }

  private renderModules(): string {
    if (!this.modules.length) return `<button type="button" class="debug-load-row" data-debug-action="load-modules">Load modules…</button>`;
    return this.modules.map((module) => `<div class="debug-module-row"><strong>${escapeHTML(module.name || module.id || "module")}</strong><span>${escapeHTML(module.path || module.version || "")}</span></div>`).join("");
  }

  private renderLoadedSources(): string {
    if (!this.loadedSources.length) return `<button type="button" class="debug-load-row" data-debug-action="load-sources">Load sources…</button>`;
    return this.loadedSources.map((source, index) => `<button type="button" class="debug-source-row" data-debug-action="open-loaded-source" data-source-index="${index}"><span class="codicon codicon-file-code"></span><span>${escapeHTML(source.name || source.path || `Source ${source.sourceReference || index + 1}`)}</span></button>`).join("");
  }

  private installEvents(): void {
    const host = this.options.host;
    host.addEventListener("change", (event) => {
      const element = event.target as HTMLInputElement | HTMLSelectElement;
      if (element.matches("[data-debug-launch]")) {
        this.selectedLaunchId = element.value;
        void this.persistState({ ...this.snapshot.state, selectedConfigurationId: element.value });
      } else if (element.matches("[data-debug-session]")) {
        this.selectSession(element.value);
      } else if (element.matches("[data-debug-action=toggle-watch]")) {
        void this.updateWatch(element.dataset.watchId || "", { enabled: (element as HTMLInputElement).checked });
      } else if (element.matches("[data-debug-action=toggle-breakpoint-enabled]")) {
        void this.updateBreakpoint(element.dataset.breakpointId || "", { enabled: (element as HTMLInputElement).checked });
      } else if (element.matches("[data-debug-action=toggle-function-breakpoint]")) {
        void this.persistState({ ...this.snapshot.state, functionBreakpoints: (this.snapshot.state.functionBreakpoints || []).map((item) => item.id === element.dataset.breakpointId ? { ...item, enabled: (element as HTMLInputElement).checked } : item) });
      } else if (element.matches("[data-debug-action=toggle-exception-breakpoint]")) {
        void this.toggleExceptionBreakpoint(element.dataset.filterId || "", (element as HTMLInputElement).checked);
      }
    }, { signal: this.options.signal });
    host.addEventListener("click", (event) => {
      const button = (event.target as Element).closest<HTMLElement>("[data-debug-action], [data-debug-control]");
      if (!button) return;
      if (button.dataset.debugControl) { void this.control(button.dataset.debugControl); return; }
      const action = button.dataset.debugAction;
      if (action === "retry") void this.start();
      else if (action === "settings") this.showSettings();
      else if (action === "start") void this.launch(false);
      else if (action === "start-without") void this.launch(true);
      else if (action === "stop") void this.stopActive();
      else if (action === "terminate-debuggee") void this.stopActive(true);
      else if (action === "restart") void this.restartActive();
      else if (action === "stop-group") void this.stopGroup(button.dataset.groupId || "");
      else if (action === "restart-group") void this.restartGroup(button.dataset.groupId || "");
      else if (action === "toggle-section") { const id = button.dataset.sectionId || ""; if (this.collapsed.has(id)) this.collapsed.delete(id); else this.collapsed.add(id); this.persistBrowserPreferences(); this.render(); }
      else if (action === "add-watch") void this.addWatch();
      else if (action === "add-function-breakpoint") void this.addFunctionBreakpoint();
      else if (action === "edit-watch") void this.editWatch(button.dataset.watchId || "");
      else if (action === "remove-watch") void this.removeWatch(button.dataset.watchId || "");
      else if (action === "remove-all-breakpoints") void this.removeAllBreakpoints();
      else if (action === "remove-breakpoint") void this.removeBreakpoint(button.dataset.breakpointId || "");
      else if (action === "remove-function-breakpoint") void this.persistState({ ...this.snapshot.state, functionBreakpoints: (this.snapshot.state.functionBreakpoints || []).filter((item) => item.id !== button.dataset.breakpointId) });
      else if (action === "edit-function-breakpoint") void this.editAdvancedBreakpoint("function", button.dataset.breakpointId || "");
      else if (action === "remove-instruction-breakpoint") void this.persistState({ ...this.snapshot.state, instructionBreakpoints: (this.snapshot.state.instructionBreakpoints || []).filter((item) => item.id !== button.dataset.breakpointId) });
      else if (action === "edit-instruction-breakpoint") void this.editAdvancedBreakpoint("instruction", button.dataset.breakpointId || "");
      else if (action === "remove-data-breakpoint") void this.removeDataBreakpoint(button.dataset.breakpointId || "");
      else if (action === "edit-data-breakpoint") void this.editAdvancedBreakpoint("data", button.dataset.breakpointId || "");
      else if (action === "edit-exception-breakpoint") void this.editExceptionBreakpoint(button.dataset.filterId || "");
      else if (action === "edit-breakpoint") void this.editBreakpoint(button.dataset.breakpointId || "");
      else if (action === "open-breakpoint") void this.openBreakpoint(button.dataset.breakpointId || "");
      else if (action === "select-frame") void this.selectFrameByID(button.dataset.sessionId || "", Number(button.dataset.threadId), Number(button.dataset.frameId));
      else if (action === "load-more-frames") void this.loadMoreFrames(button.dataset.sessionId || "", Number(button.dataset.threadId));
      else if (action === "expand-variable") void this.toggleVariable(Number(button.dataset.variableReference));
      else if (action === "load-more-variables") void this.loadMoreVariables(Number(button.dataset.variableReference));
      else if (action === "variable-menu") this.showVariableMenu(event as MouseEvent, Number(button.dataset.parentReference), button.dataset.variableName || "");
      else if (action === "load-modules") void this.loadModules();
      else if (action === "load-sources") void this.loadSources();
      else if (action === "open-loaded-source") void this.openDebugSource(this.loadedSources[Number(button.dataset.sourceIndex)]);
    }, { signal: this.options.signal });
    host.addEventListener("contextmenu", (event) => {
      const watchRow = (event.target as Element).closest<HTMLElement>("[data-debug-watch-id]");
      if (watchRow) {
        const watch = (this.snapshot.state.watches || []).find((item) => item.id === watchRow.dataset.debugWatchId);
        const result = watch ? this.watchResults.get(watch.id) : undefined;
        const session = this.activeSession();
        if (!watch || !session) return;
        event.preventDefault();
        showContextMenu(event.clientX, event.clientY, [
          { label: "Copy Value", icon: "copy", disabled: result?.value === undefined, run: () => result?.value !== undefined && copyText(result.value) },
          { label: "Set Expression Value…", icon: "edit", disabled: session.status !== "stopped" || !commandSupported(session, "setExpression"), run: () => this.setWatchExpression(session, watch.id) },
        ]);
        return;
      }
      const threadRow = (event.target as Element).closest<HTMLElement>("[data-debug-thread]");
      if (threadRow) {
        const session = this.snapshot.sessions.find((item) => item.id === threadRow.dataset.sessionId);
        const threadId = Number(threadRow.dataset.threadId);
        if (!session || !threadId) return;
        event.preventDefault();
        showContextMenu(event.clientX, event.clientY, [
          { label: "Terminate Thread", icon: "debug-stop", disabled: !commandSupported(session, "terminateThreads"), run: () => this.terminateThread(session, threadId) },
        ]);
        return;
      }
      const row = (event.target as Element).closest<HTMLElement>("[data-debug-action=select-frame]");
      if (!row) return;
      event.preventDefault();
      const session = this.snapshot.sessions.find((item) => item.id === row.dataset.sessionId);
      const frame = this.frames.get(`${row.dataset.sessionId}:${Number(row.dataset.threadId)}`)?.find((item) => item.id === Number(row.dataset.frameId));
      if (!session || !frame) return;
      showContextMenu(event.clientX, event.clientY, [
        { label: "Open Source", icon: "go-to-file", disabled: !frame.source, run: () => this.openDebugSource(frame.source, frame.line, frame.column) },
        { label: "Restart Frame", icon: "debug-restart-frame", disabled: !commandSupported(session, "restartFrame"), run: () => this.restartFrame(session, frame.id) },
        { label: "Copy Stack Frame", icon: "copy", separatorBefore: true, run: () => copyText(`${frame.name} (${frame.source?.path || frame.source?.name || "unknown"}:${frame.line})`) },
      ]);
    }, { signal: this.options.signal });
    this.options.toolbarHost.addEventListener("click", (event) => {
      const button = (event.target as Element).closest<HTMLElement>("[data-debug-action], [data-debug-control]");
      if (!button || button.hasAttribute("disabled")) return;
      if (button.dataset.debugControl) void this.control(button.dataset.debugControl);
      else if (button.dataset.debugAction === "restart") void this.restartActive();
      else if (button.dataset.debugAction === "stop") void this.stopActive();
      else if (button.dataset.debugAction === "terminate-debuggee") void this.stopActive(true);
      else if (button.dataset.debugAction === "stop-group") void this.stopGroup(button.dataset.groupId || "");
      else if (button.dataset.debugAction === "restart-group") void this.restartGroup(button.dataset.groupId || "");
    }, { signal: this.options.signal });
    this.options.toolbarHost.addEventListener("change", (event) => {
      const select = (event.target as Element).closest<HTMLSelectElement>("[data-debug-toolbar-session]");
      if (select) this.selectSession(select.value);
    }, { signal: this.options.signal });
  }

  private installEditorIntegration(): void {
    this.disposables.push(this.options.editor.onMouseDown((event) => {
      if (event.event.leftButton && event.target.type === monaco.editor.MouseTargetType.GUTTER_GLYPH_MARGIN && event.target.position) {
        void this.toggleSourceBreakpoint(this.options.activeFile(), event.target.position.lineNumber);
      }
    }));
    this.disposables.push(this.options.editor.onContextMenu((event) => {
      if (event.target.type !== monaco.editor.MouseTargetType.GUTTER_GLYPH_MARGIN || !event.target.position) return;
      const ref = this.options.activeFile();
      if (!ref) return;
      const line = event.target.position.lineNumber;
      const breakpoint = (this.snapshot.state.sourceBreakpoints || []).find((item) => refKey(item.source) === refKey(ref) && item.line === line);
      showContextMenu(event.event.posx, event.event.posy, [
        { label: breakpoint ? "Remove Breakpoint" : "Add Breakpoint", icon: "debug-breakpoint", run: () => this.toggleSourceBreakpoint(ref, line) },
        { label: "Add Conditional Breakpoint…", icon: "debug-breakpoint-conditional", run: () => this.editBreakpointAt(ref, line, "condition") },
        { label: "Add Hit Count Breakpoint…", icon: "debug-breakpoint-conditional", run: () => this.editBreakpointAt(ref, line, "hitCondition") },
        { label: "Add Logpoint…", icon: "debug-breakpoint-log", run: () => this.editBreakpointAt(ref, line, "logMessage") },
        ...(breakpoint ? [{ label: breakpoint.enabled ? "Disable Breakpoint" : "Enable Breakpoint", icon: "circle-slash", run: () => this.updateBreakpoint(breakpoint.id, { enabled: !breakpoint.enabled }) }] : []),
        ...(this.activeSession()?.status === "stopped" && commandSupported(this.activeSession(), "goto") ? [{ label: "Run to Cursor", icon: "debug-run-to-cursor", separatorBefore: true, run: () => this.runToCursor(ref, line, event.target.position?.column || 1) }] : []),
      ]);
    }));
    this.disposables.push(this.options.editor.onDidChangeModel(() => {
      this.refreshEditorDecorations();
      this.clearInlineValues();
      const session = this.activeSession();
      if (session?.status === "stopped" && this.frameSelection) void this.loadInlineValues(session, this.frameSelection.frame, this.inspectionGeneration);
    }));
    this.disposables.push(this.options.editor.onDidChangeModelContent(() => {
      window.clearTimeout(this.breakpointPersistTimer);
      this.breakpointPersistTimer = window.setTimeout(() => void this.persistMovedBreakpoints(), 450);
    }));
    const languages = [...new Set(monaco.languages.getLanguages().map((language) => language.id))];
    for (const language of languages) {
      this.disposables.push(monaco.languages.registerHoverProvider(language, {
        provideHover: async (model, position, token) => {
          const session = this.activeSession();
          const frame = this.frameSelection?.frame;
          if (!session || session.status !== "stopped" || !frame || model !== this.options.editor.getModel()) return null;
          const selection = this.options.editor.getSelection();
          const expression = selection && !selection.isEmpty() && selection.containsPosition(position)
            ? model.getValueInRange(selection)
            : model.getWordAtPosition(position)?.word || "";
          if (!expression) return null;
          const generation = this.inspectionGeneration;
          try {
            const result = await dapRequest<{ result?: string; type?: string }>(this.options.workspaceId, session.id, "evaluate", session.revision, session.stopGeneration, { expression, frameId: frame.id, context: "hover" });
            if (token.isCancellationRequested || generation !== this.inspectionGeneration || session.stopGeneration !== this.activeSession()?.stopGeneration) return null;
            return { range: selection && !selection.isEmpty() ? selection : undefined, contents: [{ value: `**${escapeMarkdown(expression)}**${result.body.type ? ` · \`${escapeMarkdown(result.body.type)}\`` : ""}\n\n\`${escapeMarkdown(result.body.result || "") }\`` }] };
          } catch { return null; }
        },
      }));
    }
  }

  private refreshEditorDecorations(): void {
    const model = this.options.editor.getModel();
    const ref = this.options.activeFile();
    const decorations: MonacoEditor.IModelDeltaDecoration[] = [];
    const ids: string[] = [];
    if (model && ref) {
      for (const breakpoint of this.snapshot.state.sourceBreakpoints || []) {
        if (refKey(breakpoint.source) !== refKey(ref)) continue;
        decorations.push({ range: new monaco.Range(breakpoint.line, 1, breakpoint.line, 1), options: { isWholeLine: false, glyphMarginClassName: breakpointDecorationClass(breakpoint, this.snapshot.sessions), glyphMarginHoverMessage: { value: breakpointTooltip(breakpoint, this.snapshot.sessions) }, stickiness: monaco.editor.TrackedRangeStickiness.NeverGrowsWhenTypingAtEdges } });
        ids.push(breakpoint.id);
      }
      const session = this.activeSession();
      const locationRef = session?.location?.ref || session?.location?.echoRef;
      if (session?.status === "stopped" && locationRef && refKey(locationRef) === refKey(ref) && session.location?.line) {
        decorations.push({ range: new monaco.Range(session.location.line, 1, session.location.line, 1), options: { isWholeLine: true, className: "echo-debug-current-line", glyphMarginClassName: "echo-debug-current-arrow" } });
        ids.push("__current__");
      }
      const selected = this.frameSelection?.frame;
      if (selected && selected.id !== undefined && selected.source?.echoRef && refKey(selected.source.echoRef) === refKey(ref) && selected.line && selected.line !== session?.location?.line) {
        decorations.push({ range: new monaco.Range(selected.line, 1, selected.line, 1), options: { isWholeLine: true, className: "echo-debug-selected-line", glyphMarginClassName: "echo-debug-selected-arrow" } });
        ids.push("__selected__");
      }
    }
    this.breakpointDecorations = this.options.editor.deltaDecorations(this.breakpointDecorations, decorations);
    this.breakpointDecorationIDs.clear();
    ids.forEach((id, index) => this.breakpointDecorationIDs.set(id, this.breakpointDecorations[index]));
  }

  private async persistMovedBreakpoints(): Promise<void> {
    const model = this.options.editor.getModel();
    const ref = this.options.activeFile();
    if (!model || !ref) return;
    let changed = false;
    const source = (this.snapshot.state.sourceBreakpoints || []).map((breakpoint) => {
      if (refKey(breakpoint.source) !== refKey(ref)) return breakpoint;
      const decoration = this.breakpointDecorationIDs.get(breakpoint.id);
      const range = decoration ? model.getDecorationRange(decoration) : null;
      if (!range || range.startLineNumber === breakpoint.line) return breakpoint;
      changed = true;
      return { ...breakpoint, line: range.startLineNumber };
    });
    if (changed) await this.persistState({ ...this.snapshot.state, sourceBreakpoints: source });
  }

  private async toggleSourceBreakpoint(ref: FileRef | null, line: number): Promise<void> {
    if (!ref || line < 1) return;
    const source = [...(this.snapshot.state.sourceBreakpoints || [])];
    const index = source.findIndex((item) => refKey(item.source) === refKey(ref) && item.line === line);
    if (index >= 0) source.splice(index, 1);
    else source.push({ id: crypto.randomUUID(), source: ref, line, enabled: true });
    await this.persistState({ ...this.snapshot.state, sourceBreakpoints: source });
  }

  private async updateBreakpoint(id: string, patch: Partial<SourceBreakpoint>): Promise<void> {
    const source = (this.snapshot.state.sourceBreakpoints || []).map((item) => item.id === id ? { ...item, ...patch } : item);
    await this.persistState({ ...this.snapshot.state, sourceBreakpoints: source });
  }

  private async removeBreakpoint(id: string): Promise<void> {
    await this.persistState({ ...this.snapshot.state, sourceBreakpoints: (this.snapshot.state.sourceBreakpoints || []).filter((item) => item.id !== id) });
  }

  private async addFunctionBreakpoint(): Promise<void> {
    const name = await promptDialog({ title: "Add Function Breakpoint", label: "Function name", required: true });
    if (!name) return;
    await this.persistState({
      ...this.snapshot.state,
      functionBreakpoints: [
        ...(this.snapshot.state.functionBreakpoints || []),
        { id: crypto.randomUUID(), name, enabled: true },
      ],
    });
  }

  private async toggleExceptionBreakpoint(filter: string, enabled: boolean): Promise<void> {
    if (!filter) return;
    const exceptions = [...(this.snapshot.state.exceptionBreakpoints || [])];
    const index = exceptions.findIndex((item) => item.filter === filter);
    if (index >= 0) exceptions[index] = { ...exceptions[index], enabled };
    else exceptions.push({ filter, enabled });
    await this.persistState({ ...this.snapshot.state, exceptionBreakpoints: exceptions });
  }

  private async editExceptionBreakpoint(filter: string): Promise<void> {
    const existing = (this.snapshot.state.exceptionBreakpoints || []).find((item) => item.filter === filter) || { filter, enabled: true };
    const raw = await promptLargeJSON("Edit Exception Breakpoint", existing);
    if (!raw) return;
    const updated = { ...existing, ...raw, filter };
    await this.persistState({
      ...this.snapshot.state,
      exceptionBreakpoints: [...(this.snapshot.state.exceptionBreakpoints || []).filter((item) => item.filter !== filter), updated],
    });
  }

  private async editAdvancedBreakpoint(kind: "function" | "instruction" | "data", id: string): Promise<void> {
    const session = this.activeSession();
    const persistent = kind === "function" ? this.snapshot.state.functionBreakpoints || []
      : kind === "instruction" ? this.snapshot.state.instructionBreakpoints || []
        : this.snapshot.state.dataBreakpoints || [];
    let current = persistent.find((item) => item.id === id) as unknown as Record<string, unknown> | undefined;
    const transientIndex = kind === "data" && session ? (this.sessionDataBreakpoints.get(session.id) || []).findIndex((item) => item.id === id) : -1;
    if (!current && transientIndex >= 0 && session) current = (this.sessionDataBreakpoints.get(session.id) || [])[transientIndex] as unknown as Record<string, unknown>;
    if (!current) return;
    const raw = await promptLargeJSON(`Edit ${kind[0].toUpperCase()}${kind.slice(1)} Breakpoint`, current);
    if (!raw) return;
    const updated = { ...current, ...raw, id };
    if (transientIndex >= 0 && session) {
      const transient = [...(this.sessionDataBreakpoints.get(session.id) || [])];
      transient[transientIndex] = updated as unknown as DataBreakpoint;
      this.sessionDataBreakpoints.set(session.id, transient);
      try { await this.syncDataBreakpoints(session); this.render(); } catch (error) { await this.handleRequestError(error); }
      return;
    }
    if (kind === "function") await this.persistState({ ...this.snapshot.state, functionBreakpoints: persistent.map((item) => item.id === id ? updated as never : item) as typeof this.snapshot.state.functionBreakpoints });
    else if (kind === "instruction") await this.persistState({ ...this.snapshot.state, instructionBreakpoints: persistent.map((item) => item.id === id ? updated as never : item) as typeof this.snapshot.state.instructionBreakpoints });
    else await this.persistState({ ...this.snapshot.state, dataBreakpoints: persistent.map((item) => item.id === id ? updated as never : item) as typeof this.snapshot.state.dataBreakpoints });
  }

  private async removeAllBreakpoints(): Promise<void> {
    const session = this.activeSession();
    if (session) this.sessionDataBreakpoints.delete(session.id);
    await this.persistState({
      ...this.snapshot.state,
      sourceBreakpoints: [],
      functionBreakpoints: [],
      instructionBreakpoints: [],
      dataBreakpoints: [],
    });
    if (session?.status === "stopped") await this.syncDataBreakpoints(session);
  }

  private async removeDataBreakpoint(id: string): Promise<void> {
    const persistent = this.snapshot.state.dataBreakpoints || [];
    if (persistent.some((item) => item.id === id)) {
      await this.persistState({ ...this.snapshot.state, dataBreakpoints: persistent.filter((item) => item.id !== id) });
      return;
    }
    const session = this.activeSession();
    if (!session) return;
    const transient = (this.sessionDataBreakpoints.get(session.id) || []).filter((item) => item.id !== id);
    this.sessionDataBreakpoints.set(session.id, transient);
    try {
      await this.syncDataBreakpoints(session);
      this.render();
    } catch (error) { await this.handleRequestError(error); }
  }

  private async syncDataBreakpoints(session: DebugSession): Promise<void> {
    const persistent = (this.snapshot.state.dataBreakpoints || []).filter((item) => item.enabled && (!item.adapterProfileId || item.adapterProfileId === session.adapterProfileId));
    const transient = (this.sessionDataBreakpoints.get(session.id) || []).filter((item) => item.enabled);
    await dapRequest(this.options.workspaceId, session.id, "setDataBreakpoints", session.revision, session.stopGeneration, {
      breakpoints: [...persistent, ...transient].map((item) => ({
        dataId: item.dataId,
        accessType: item.accessType,
        condition: item.condition,
        hitCondition: item.hitCondition,
      })),
    });
  }

  private async editBreakpoint(id: string): Promise<void> {
    const breakpoint = (this.snapshot.state.sourceBreakpoints || []).find((item) => item.id === id);
    if (!breakpoint) return;
    await this.showBreakpointEditor(breakpoint);
  }

  private async editBreakpointAt(ref: FileRef, line: number, field: "condition" | "hitCondition" | "logMessage"): Promise<void> {
    let breakpoint = (this.snapshot.state.sourceBreakpoints || []).find((item) => refKey(item.source) === refKey(ref) && item.line === line);
    if (!breakpoint) breakpoint = { id: crypto.randomUUID(), source: ref, line, enabled: true };
    const label = field === "condition" ? "Expression" : field === "hitCondition" ? "Hit count" : "Message (use {expression} for interpolation)";
    const value = await promptDialog({ title: field === "logMessage" ? "Logpoint" : "Conditional Breakpoint", label, initial: breakpoint[field] || "", required: false });
    if (value === null) return;
    const source = [...(this.snapshot.state.sourceBreakpoints || []).filter((item) => item.id !== breakpoint!.id), { ...breakpoint, [field]: value || undefined }];
    await this.persistState({ ...this.snapshot.state, sourceBreakpoints: source });
  }

  private async showBreakpointEditor(breakpoint: SourceBreakpoint): Promise<void> {
    const value = await promptDialog({ title: "Edit Breakpoint", label: "Condition (leave empty for unconditional)", initial: breakpoint.condition || "", required: false });
    if (value !== null) await this.updateBreakpoint(breakpoint.id, { condition: value || undefined });
  }

  private async openBreakpoint(id: string): Promise<void> {
    const breakpoint = (this.snapshot.state.sourceBreakpoints || []).find((item) => item.id === id);
    if (breakpoint) await this.options.openSource({ echoRef: breakpoint.source, name: breakpoint.source.path }, breakpoint.line, breakpoint.column || 1);
  }

  private async persistState(state: DebugPersistentState): Promise<void> {
    try {
      const saved = await saveDebugState(this.options.workspaceId, this.snapshot.state.revision || 0, state);
      this.snapshot = { ...this.snapshot, state: saved };
      this.render();
    } catch (error) {
      await this.handleRequestError(error);
    }
  }

  private async addWatch(): Promise<void> {
    const expression = await promptDialog({ title: "Add Watch Expression", label: "Expression", required: true });
    if (!expression) return;
    await this.persistState({ ...this.snapshot.state, watches: [...(this.snapshot.state.watches || []), { id: crypto.randomUUID(), expression, enabled: true }] });
    const session = this.activeSession();
    if (session?.status === "stopped") void this.evaluateWatches(session);
  }

  private async editWatch(id: string): Promise<void> {
    const watch = (this.snapshot.state.watches || []).find((item) => item.id === id);
    if (!watch) return;
    const expression = await promptDialog({ title: "Edit Watch Expression", label: "Expression", initial: watch.expression, required: true });
    if (expression) await this.updateWatch(id, { expression });
  }

  private async updateWatch(id: string, patch: Partial<{ expression: string; enabled: boolean }>): Promise<void> {
    await this.persistState({ ...this.snapshot.state, watches: (this.snapshot.state.watches || []).map((item) => item.id === id ? { ...item, ...patch } : item) });
  }

  private async removeWatch(id: string): Promise<void> {
    await this.persistState({ ...this.snapshot.state, watches: (this.snapshot.state.watches || []).filter((item) => item.id !== id) });
  }

  private async setWatchExpression(session: DebugSession, watchId: string): Promise<void> {
    const watch = (this.snapshot.state.watches || []).find((item) => item.id === watchId);
    if (!watch || session.status !== "stopped") return;
    const value = await promptDialog({ title: `Set ${watch.expression}`, label: "Value", initial: this.watchResults.get(watchId)?.value || "", required: true });
    if (value === null) return;
    try {
      await dapRequest(this.options.workspaceId, session.id, "setExpression", session.revision, session.stopGeneration, { expression: watch.expression, value, frameId: this.frameSelection?.frame.id });
      await this.evaluateWatches(session, this.inspectionGeneration);
      this.render();
    } catch (error) { await this.handleRequestError(error); }
  }

  private async terminateThread(session: DebugSession, threadId: number): Promise<void> {
    try {
      await dapRequest(this.options.workspaceId, session.id, "terminateThreads", session.revision, session.stopGeneration, { threadIds: [threadId] });
      await this.inspectStopped(session);
    } catch (error) { await this.handleRequestError(error); }
  }

  private async inspectStopped(session: DebugSession): Promise<void> {
    const generation = ++this.inspectionGeneration;
    try {
      const threadResponse = await dapRequest<{ threads?: DAPThread[] }>(this.options.workspaceId, session.id, "threads", session.revision, session.stopGeneration);
      if (!this.inspectionCurrent(session, generation)) return;
      const threads = threadResponse.body.threads || [];
      this.threads.set(session.id, threads);
      await Promise.all(threads.map(async (thread) => {
        const key = `${session.id}:${thread.id}`;
        const stack = await dapRequest<{ stackFrames?: DAPStackFrame[]; totalFrames?: number }>(this.options.workspaceId, session.id, "stackTrace", session.revision, session.stopGeneration, { threadId: thread.id, startFrame: 0, levels: 50 });
        if (this.inspectionCurrent(session, generation)) {
          const frames = stack.body.stackFrames || [];
          this.frames.set(key, frames);
          if (stack.body.totalFrames) this.frameTotals.set(key, stack.body.totalFrames);
          if (frames.length !== 50 || (stack.body.totalFrames !== undefined && frames.length >= stack.body.totalFrames)) this.completeFrameStacks.add(key);
          else this.completeFrameStacks.delete(key);
        }
      }));
      if (!this.inspectionCurrent(session, generation)) return;
      const targetThread = (this.savedFrame?.sessionId === session.id ? threads.find((thread) => thread.id === this.savedFrame?.threadId) : undefined)
        || threads.find((thread) => thread.id === session.threadId) || threads[0];
      const threadFrames = targetThread ? this.frames.get(`${session.id}:${targetThread.id}`) || [] : [];
      const firstFrame = (this.savedFrame?.sessionId === session.id ? threadFrames.find((frame) => frame.id === this.savedFrame?.frameId) : undefined) || threadFrames[0];
      if (session.stoppedReason === "exception" && session.threadId && commandSupported(session, "exceptionInfo")) void this.loadExceptionDetails(session);
      if (targetThread && firstFrame) await this.selectFrame(session, targetThread.id, firstFrame, generation);
      if (capability(session, "supportsModulesRequest")) void this.loadModules();
      if (capability(session, "supportsLoadedSourcesRequest")) void this.loadSources();
      this.render();
    } catch (error) { await this.handleRequestError(error); }
  }

  private async loadMoreFrames(sessionId: string, threadId: number): Promise<void> {
    const session = this.snapshot.sessions.find((item) => item.id === sessionId);
    const key = `${sessionId}:${threadId}`;
    if (!session || session.status !== "stopped" || this.completeFrameStacks.has(key)) return;
    const current = this.frames.get(key) || [];
    try {
      const response = await dapRequest<{ stackFrames?: DAPStackFrame[]; totalFrames?: number }>(this.options.workspaceId, session.id, "stackTrace", session.revision, session.stopGeneration, { threadId, startFrame: current.length, levels: 50 });
      const page = response.body.stackFrames || [];
      this.frames.set(key, [...current, ...page]);
      if (response.body.totalFrames) this.frameTotals.set(key, response.body.totalFrames);
      const total = this.frameTotals.get(key) || 0;
      if (page.length !== 50 || (total > 0 && current.length + page.length >= total)) this.completeFrameStacks.add(key);
      this.render();
    } catch (error) { await this.handleRequestError(error); }
  }

  private async loadExceptionDetails(session: DebugSession): Promise<void> {
    try {
      const response = await dapRequest<{ exceptionId?: string; description?: string; breakMode?: string; details?: { message?: string; typeName?: string; stackTrace?: string } }>(this.options.workspaceId, session.id, "exceptionInfo", session.revision, session.stopGeneration, { threadId: session.threadId });
      if (this.activeSession()?.id !== session.id || this.activeSession()?.stopGeneration !== session.stopGeneration) return;
      this.exceptionDetails.set(session.id, response.body);
      this.render();
    } catch { /* Some adapters report an exception stop without implementing exceptionInfo. */ }
  }

  private async selectFrameByID(sessionId: string, threadId: number, frameId: number): Promise<void> {
    const session = this.snapshot.sessions.find((item) => item.id === sessionId);
    const frame = this.frames.get(`${sessionId}:${threadId}`)?.find((item) => item.id === frameId);
    if (!session || !frame) return;
    this.selectedSessionId = session.id;
    await this.selectFrame(session, threadId, frame, this.inspectionGeneration);
  }

  private async selectFrame(session: DebugSession, threadId: number, frame: DAPStackFrame, generation: number): Promise<void> {
    this.frameSelection = { sessionId: session.id, threadId, frame };
    this.savedFrame = { sessionId: session.id, threadId, frameId: frame.id };
    this.persistBrowserPreferences();
    this.scopes = [];
    this.variables.clear();
    this.variableTotals.clear();
    this.completeVariableReferences.clear();
    this.expandedVariables.clear();
    this.render();
    if (frame.source) await this.openDebugSource(frame.source, frame.line, frame.column || 1);
    const response = await dapRequest<{ scopes?: DAPScope[] }>(this.options.workspaceId, session.id, "scopes", session.revision, session.stopGeneration, { frameId: frame.id });
    if (!this.inspectionCurrent(session, generation) || this.frameSelection?.frame.id !== frame.id) return;
    this.scopes = response.body.scopes || [];
    for (const scope of this.scopes) {
      const total = (scope.namedVariables || 0) + (scope.indexedVariables || 0);
      if (total > 0) this.variableTotals.set(scope.variablesReference, total);
    }
    await Promise.all(this.scopes.filter((scope) => !scope.expensive).map((scope) => this.loadVariables(scope.variablesReference, session, generation)));
    await this.evaluateWatches(session, generation);
    await this.loadInlineValues(session, frame, generation);
    this.render();
  }

  private async loadInlineValues(session: DebugSession, frame: DAPStackFrame, generation: number): Promise<void> {
    this.clearInlineValues();
    if (!commandSupported(session, "inlineValues") || !this.inspectionCurrent(session, generation)) return;
    const model = this.options.editor.getModel();
    if (!model) return;
    const activeRef = this.options.activeFile();
    if (frame.source?.echoRef && (!activeRef || refKey(frame.source.echoRef) !== refKey(activeRef))) return;
    const visible = this.options.editor.getVisibleRanges()[0];
    const startLine = Math.max(1, visible?.startLineNumber || 1);
    const endLine = Math.min(model.getLineCount(), visible?.endLineNumber || model.getLineCount());
    try {
      const response = await dapRequest<{ areas?: Array<{ range: { startLine: number; startColumn: number; endLine: number; endColumn: number }; text?: string; variableName?: string; expression?: string }> }>(this.options.workspaceId, session.id, "inlineValues", session.revision, session.stopGeneration, {
        frameId: frame.id,
        range: { startLine, startColumn: 1, endLine, endColumn: model.getLineMaxColumn(endLine) },
        context: { frameId: frame.id, stoppedLocation: { startLine: frame.line, startColumn: frame.column || 1, endLine: frame.endLine || frame.line, endColumn: frame.endColumn || frame.column || 1 } },
      });
      if (!this.inspectionCurrent(session, generation) || this.frameSelection?.frame.id !== frame.id || model !== this.options.editor.getModel()) return;
      const areas = (response.body.areas || []).slice(0, 40);
      const resolved = await Promise.all(areas.map(async (area) => {
        if (area.text !== undefined) return area.text;
        const expression = area.expression || area.variableName;
        if (!expression) return "";
        const local = [...this.variables.values()].flat().find((variable) => variable.name === expression);
        if (local) return `${expression} = ${local.value}`;
        try {
          const evaluation = await dapRequest<{ result?: string }>(this.options.workspaceId, session.id, "evaluate", session.revision, session.stopGeneration, { expression, frameId: frame.id, context: "variables" });
          return `${expression} = ${evaluation.body.result || ""}`;
        } catch { return ""; }
      }));
      if (!this.inspectionCurrent(session, generation) || model !== this.options.editor.getModel()) return;
      const decorations: MonacoEditor.IModelDeltaDecoration[] = [];
      areas.forEach((area, index) => {
        const text = resolved[index];
        if (!text) return;
        const line = Math.max(1, Math.min(model.getLineCount(), area.range.endLine));
        const column = Math.max(1, Math.min(model.getLineMaxColumn(line), area.range.endColumn));
        decorations.push({
          range: new monaco.Range(line, column, line, column),
          options: { after: { content: `  ${text}`, inlineClassName: "echo-debug-inline-value" }, hoverMessage: { value: `\`${escapeMarkdown(text)}\`` } },
        });
      });
      this.inlineValueDecorations = this.options.editor.deltaDecorations(this.inlineValueDecorations, decorations);
    } catch { /* Inline values are supplemental and never block stack inspection. */ }
  }

  private clearInlineValues(): void {
    if (this.inlineValueDecorations.length) this.inlineValueDecorations = this.options.editor.deltaDecorations(this.inlineValueDecorations, []);
  }

  private async loadVariables(reference: number, session = this.activeSession(), generation = this.inspectionGeneration, append = false): Promise<void> {
    if (!session || reference <= 0 || (!append && this.variables.has(reference)) || (append && this.completeVariableReferences.has(reference))) return;
    const existing = append ? this.variables.get(reference) || [] : [];
    const pageSize = 100;
    const response = await dapRequest<{ variables?: DAPVariable[] }>(this.options.workspaceId, session.id, "variables", session.revision, session.stopGeneration, { variablesReference: reference, start: existing.length, count: pageSize });
    if (!this.inspectionCurrent(session, generation)) return;
    const page = response.body.variables || [];
    this.variables.set(reference, [...existing, ...page]);
    for (const variable of page) {
      const total = (variable.namedVariables || 0) + (variable.indexedVariables || 0);
      if (variable.variablesReference > 0 && total > 0) this.variableTotals.set(variable.variablesReference, total);
    }
    const total = this.variableTotals.get(reference) || 0;
    if (page.length !== pageSize || (total > 0 && existing.length + page.length >= total)) this.completeVariableReferences.add(reference);
  }

  private async loadMoreVariables(reference: number): Promise<void> {
    try {
      await this.loadVariables(reference, this.activeSession(), this.inspectionGeneration, true);
      this.render();
    } catch (error) { await this.handleRequestError(error); }
  }

  private async toggleVariable(reference: number): Promise<void> {
    if (reference <= 0) return;
    if (this.expandedVariables.has(reference)) this.expandedVariables.delete(reference);
    else {
      this.expandedVariables.add(reference);
      try { await this.loadVariables(reference); } catch (error) { await this.handleRequestError(error); }
    }
    this.render();
  }

  private showVariableMenu(event: MouseEvent, parentReference: number, name: string): void {
    const session = this.activeSession();
    const variable = this.variables.get(parentReference)?.find((item) => item.name === name);
    if (!session || !variable) return;
    showContextMenu(event.clientX, event.clientY, [
      { label: "Copy Value", icon: "copy", run: () => copyText(variable.value) },
      { label: "Copy as Expression", icon: "symbol-variable", disabled: !variable.evaluateName, run: () => variable.evaluateName && copyText(variable.evaluateName) },
      { label: "Set Value…", icon: "edit", disabled: !capability(session, "supportsSetVariable"), run: () => this.setVariable(parentReference, variable) },
      { label: "Add Data Breakpoint", icon: "debug-breakpoint-data", disabled: !capability(session, "supportsDataBreakpoints"), run: () => this.discoverDataBreakpoint(parentReference, variable) },
      { label: "View Memory", icon: "database", disabled: !variable.memoryReference || !capability(session, "supportsReadMemoryRequest"), run: () => this.viewMemory(variable.memoryReference || "") },
      { label: "Disassemble", icon: "symbol-numeric", separatorBefore: true, disabled: !variable.memoryReference || !capability(session, "supportsDisassembleRequest"), run: () => this.viewDisassembly(variable.memoryReference || "") },
    ]);
  }

  private async setVariable(parentReference: number, variable: DAPVariable): Promise<void> {
    const session = this.activeSession();
    if (!session) return;
    const value = await promptDialog({ title: `Set ${variable.name}`, label: "Value", initial: variable.value, required: true });
    if (value === null) return;
    try {
      await dapRequest(this.options.workspaceId, session.id, "setVariable", session.revision, session.stopGeneration, { variablesReference: parentReference, name: variable.name, value });
      this.variables.delete(parentReference);
      await this.loadVariables(parentReference);
      this.render();
    } catch (error) { await this.handleRequestError(error); }
  }

  private async runToCursor(ref: FileRef, line: number, column: number): Promise<void> {
    const session = this.activeSession();
    if (!session || session.status !== "stopped") return;
    try {
      const targets = await dapRequest<{ targets?: Array<{ id: number; label: string; line: number; column?: number }> }>(this.options.workspaceId, session.id, "gotoTargets", session.revision, session.stopGeneration, { source: { name: ref.path.split("/").pop(), echoRef: ref }, line, column });
      const target = targets.body.targets?.[0];
      if (!target) { toast("The adapter did not find a runnable target at the cursor."); return; }
      await dapRequest(this.options.workspaceId, session.id, "goto", session.revision, session.stopGeneration, { threadId: this.frameSelection?.threadId || session.threadId, targetId: target.id });
      this.invalidateInspection();
    } catch (error) { await this.handleRequestError(error); }
  }

  private async restartFrame(session: DebugSession, frameId: number): Promise<void> {
    try {
      await dapRequest(this.options.workspaceId, session.id, "restartFrame", session.revision, session.stopGeneration, { frameId });
      this.invalidateInspection();
      await this.resync();
    } catch (error) { await this.handleRequestError(error); }
  }

  private async discoverDataBreakpoint(parentReference: number, variable: DAPVariable): Promise<void> {
    const session = this.activeSession();
    if (!session) return;
    try {
      const response = await dapRequest<{ dataId?: string; description?: string; accessTypes?: Array<"read" | "write" | "readWrite">; canPersist?: boolean }>(this.options.workspaceId, session.id, "dataBreakpointInfo", session.revision, session.stopGeneration, { name: variable.name, variablesReference: parentReference });
      if (!response.body.dataId) { toast("The adapter cannot create a data breakpoint for this value."); return; }
      let accessType = response.body.accessTypes?.[0];
      if ((response.body.accessTypes?.length || 0) > 1) {
        const chosen = await choiceInput({ id: "access", type: "pickString", description: "Break when the value is accessed", options: response.body.accessTypes });
        if (chosen === null) return;
        accessType = chosen as DataBreakpoint["accessType"];
      }
      const breakpoint: DataBreakpoint = {
        id: crypto.randomUUID(), dataId: response.body.dataId,
        name: response.body.description || variable.name, adapterProfileId: session.adapterProfileId,
        accessType, enabled: true,
      };
      if (response.body.canPersist) {
        await this.persistState({ ...this.snapshot.state, dataBreakpoints: [...(this.snapshot.state.dataBreakpoints || []), breakpoint] });
        toast(`Reusable data breakpoint added for ${breakpoint.name}.`);
      } else {
        breakpoint.sessionOnly = true;
        const transient = [...(this.sessionDataBreakpoints.get(session.id) || []), breakpoint];
        this.sessionDataBreakpoints.set(session.id, transient);
        await this.syncDataBreakpoints(session);
        this.render();
        toast(`Session-only data breakpoint added for ${breakpoint.name}.`);
      }
    } catch (error) { await this.handleRequestError(error); }
  }

  private async viewMemory(memoryReference: string): Promise<void> {
    const session = this.activeSession();
    if (!session || !memoryReference) return;
    try {
      const response = await dapRequest<{ address?: string; data?: string; unreadableBytes?: number }>(this.options.workspaceId, session.id, "readMemory", session.revision, session.stopGeneration, { memoryReference, offset: 0, count: 256 });
      const bytes = response.body.data ? Uint8Array.from(atob(response.body.data), (character) => character.charCodeAt(0)) : new Uint8Array();
      if (capability(session, "supportsWriteMemoryRequest")) this.showMemoryViewer(session, memoryReference, response.body.address || memoryReference, bytes, response.body.unreadableBytes || 0);
      else {
        const content = [...bytes].map((byte, index) => `${index % 16 === 0 ? `${(index).toString(16).padStart(8, "0")}: ` : ""}${byte.toString(16).padStart(2, "0")}${index % 16 === 15 ? "\n" : " "}`).join("");
        await this.options.openVirtualSource(`Memory ${response.body.address || memoryReference}`, content, "text/plain");
      }
    } catch (error) { await this.handleRequestError(error); }
  }

  private async viewDisassembly(memoryReference: string): Promise<void> {
    const session = this.activeSession();
    if (!session || !memoryReference) return;
    try {
      const response = await dapRequest<{ instructions?: Array<{ address: string; instruction: string; symbol?: string; location?: DebugSource; line?: number }> }>(this.options.workspaceId, session.id, "disassemble", session.revision, session.stopGeneration, { memoryReference, instructionOffset: -32, instructionCount: 96, resolveSymbols: true });
      const instructions = response.body.instructions || [];
      if (capability(session, "supportsInstructionBreakpoints")) this.showDisassemblyViewer(instructions);
      else await this.options.openVirtualSource(`Disassembly ${memoryReference}`, instructions.map((item) => `${item.address.padEnd(18)} ${item.instruction}${item.symbol ? ` ; ${item.symbol}` : ""}`).join("\n"), "text/x-asm");
    } catch (error) { await this.handleRequestError(error); }
  }

  private showMemoryViewer(session: DebugSession, memoryReference: string, address: string, bytes: Uint8Array, unreadableBytes: number): void {
    const overlay = document.createElement("div");
    overlay.className = "debug-settings-overlay";
    overlay.innerHTML = `<form class="debug-memory-viewer" role="dialog" aria-modal="true"><header><div><h2>Memory ${escapeHTML(address)}</h2><p>Edit hexadecimal bytes. ${unreadableBytes ? `${unreadableBytes} trailing bytes were unreadable.` : ""}</p></div><button type="button" data-cancel aria-label="Close"><span class="codicon codicon-close"></span></button></header><textarea name="bytes" spellcheck="false" aria-label="Memory bytes">${[...bytes].map((byte) => byte.toString(16).padStart(2, "0")).join(" ")}</textarea><p data-error></p><footer><button type="button" data-cancel>Cancel</button><button type="submit" class="is-primary">Write Memory</button></footer></form>`;
    const close = () => overlay.remove();
    overlay.querySelectorAll("[data-cancel]").forEach((button) => button.addEventListener("click", close));
    overlay.addEventListener("keydown", (event) => { if (event.key === "Escape") close(); });
    overlay.querySelector("form")?.addEventListener("submit", async (event) => {
      event.preventDefault();
      const errorHost = overlay.querySelector<HTMLElement>("[data-error]")!;
      const text = (new FormData(event.currentTarget as HTMLFormElement).get("bytes") || "").toString().trim();
      if (text && !/^(?:[0-9a-fA-F]{2})(?:\s+[0-9a-fA-F]{2})*$/.test(text)) { errorHost.textContent = "Use two hexadecimal digits per byte."; return; }
      const data = text ? Uint8Array.from(text.split(/\s+/), (value) => Number.parseInt(value, 16)) : new Uint8Array();
      let binary = ""; for (const byte of data) binary += String.fromCharCode(byte);
      try {
        await dapRequest(this.options.workspaceId, session.id, "writeMemory", session.revision, session.stopGeneration, { memoryReference, offset: 0, data: btoa(binary), allowPartial: false });
        close(); toast(`${data.length} memory bytes written.`);
      } catch (error) { errorHost.textContent = errorMessage(error); }
    });
    document.body.appendChild(overlay);
  }

  private showDisassemblyViewer(instructions: Array<{ address: string; instruction: string; symbol?: string }>): void {
    const overlay = document.createElement("div");
    overlay.className = "debug-settings-overlay";
    overlay.innerHTML = `<section class="debug-disassembly-viewer" role="dialog" aria-modal="true"><header><div><h2>Disassembly</h2><p>Select an address to add an instruction breakpoint.</p></div><button type="button" data-cancel aria-label="Close"><span class="codicon codicon-close"></span></button></header><div role="list">${instructions.map((item) => `<button type="button" role="listitem" data-instruction-address="${escapeHTML(item.address)}"><code>${escapeHTML(item.address)}</code><span>${escapeHTML(item.instruction)}</span><small>${escapeHTML(item.symbol || "")}</small></button>`).join("") || `<p>No instructions returned.</p>`}</div></section>`;
    const close = () => overlay.remove();
    overlay.querySelector("[data-cancel]")?.addEventListener("click", close);
    overlay.addEventListener("keydown", (event) => { if (event.key === "Escape") close(); });
    overlay.addEventListener("click", async (event) => {
      const row = (event.target as Element).closest<HTMLElement>("[data-instruction-address]");
      if (!row) return;
      const instructionReference = row.dataset.instructionAddress || "";
      if (!instructionReference) return;
      await this.persistState({ ...this.snapshot.state, instructionBreakpoints: [...(this.snapshot.state.instructionBreakpoints || []), { id: crypto.randomUUID(), instructionReference, enabled: true }] });
      row.classList.add("is-selected");
      toast(`Instruction breakpoint added at ${instructionReference}.`);
    });
    document.body.appendChild(overlay);
  }

  private async evaluateWatches(session: DebugSession, generation = this.inspectionGeneration): Promise<void> {
    const frame = this.frameSelection?.frame;
    const watches = (this.snapshot.state.watches || []).filter((watch) => watch.enabled);
    await Promise.all(watches.map(async (watch) => {
      try {
        const response = await dapRequest<{ result?: string; type?: string; variablesReference?: number }>(this.options.workspaceId, session.id, "evaluate", session.revision, session.stopGeneration, { expression: watch.expression, frameId: frame?.id, context: "watch" });
        if (this.inspectionCurrent(session, generation)) this.watchResults.set(watch.id, { value: response.body.result, type: response.body.type, variablesReference: response.body.variablesReference });
      } catch (error) {
        if (this.inspectionCurrent(session, generation)) this.watchResults.set(watch.id, { error: errorMessage(error) });
      }
    }));
  }

  private async loadModules(): Promise<void> {
    const session = this.activeSession();
    if (!session) return;
    try {
      const response = await dapRequest<{ modules?: Array<Record<string, unknown>> }>(this.options.workspaceId, session.id, "modules", session.revision, session.stopGeneration, { startModule: 0, moduleCount: 1000 });
      this.modules = response.body.modules || [];
      this.render();
    } catch (error) { await this.handleRequestError(error); }
  }

  private async loadSources(): Promise<void> {
    const session = this.activeSession();
    if (!session) return;
    try {
      const response = await dapRequest<{ sources?: DebugSource[] }>(this.options.workspaceId, session.id, "loadedSources", session.revision, session.stopGeneration);
      this.loadedSources = response.body.sources || [];
      this.render();
    } catch (error) { await this.handleRequestError(error); }
  }

  private async openDebugSource(source?: DebugSource, line = 1, column = 1): Promise<void> {
    if (!source) return;
    if (source.echoRef) {
      await this.options.openSource(source, line, column);
      return;
    }
    const session = this.activeSession();
    if (!session) return;
    try {
      const response = await dapRequest<{ content?: string; mimeType?: string }>(this.options.workspaceId, session.id, "source", session.revision, session.stopGeneration, { source, sourceReference: source.sourceReference || 0 });
      await this.options.openVirtualSource(source.name || source.path || `Source ${source.sourceReference || "adapter"}`, response.body.content || "", response.body.mimeType);
    } catch (error) {
      toast(`The adapter could not provide this out-of-workspace source: ${errorMessage(error)}`, { sticky: true });
    }
  }

  private registerPanels(): void {
    this.unregisterPanels.push(registerWorkbenchPanel(this.options.workspaceId, { id: "debug-console", label: "Debug Console", icon: "debug-console", mount: (host) => this.mountConsole(host) }));
    this.unregisterPanels.push(registerWorkbenchPanel(this.options.workspaceId, { id: "debug-output", label: "Output", icon: "output", mount: (host) => this.mountOutput(host) }));
  }

  private mountConsole(host: HTMLElement): void {
    host.dataset.debugPanel = "debug-console";
    this.renderConsoleHost(host);
    host.onclick = (event) => {
      const source = (event.target as Element).closest<HTMLElement>("[data-debug-output-source]");
      if (source) { void this.openEncodedOutputSource(source.dataset.debugOutputSource || ""); return; }
      const action = (event.target as Element).closest<HTMLElement>("[data-console-action]")?.dataset.consoleAction;
      if (action === "clear") { this.consoleEntries = []; this.consoleClearedAt = Date.now(); this.renderConsoleHost(host); }
    };
    host.oninput = (event) => {
      const filter = (event.target as Element).closest<HTMLInputElement>("[data-debug-console-filter]");
      if (!filter) return;
      this.consoleFilter = filter.value;
      this.applyPanelFilter(host, this.consoleFilter);
    };
    host.onkeydown = (event) => {
      const input = (event.target as Element).closest<HTMLTextAreaElement>("[data-debug-repl]");
      if (!input) return;
      if (event.key === "Enter" && !event.shiftKey) { event.preventDefault(); void this.evaluateConsole(input.value, host); }
      else if (event.key === "ArrowUp" && !input.value.includes("\n")) { event.preventDefault(); this.consoleHistoryIndex = Math.max(0, this.consoleHistoryIndex - 1); input.value = this.consoleHistory[this.consoleHistoryIndex] || ""; }
      else if (event.key === "ArrowDown" && !input.value.includes("\n")) { event.preventDefault(); this.consoleHistoryIndex = Math.min(this.consoleHistory.length, this.consoleHistoryIndex + 1); input.value = this.consoleHistory[this.consoleHistoryIndex] || ""; }
      else if ((event.ctrlKey && event.code === "Space") || (event.key === "Tab" && !input.value.includes("\n"))) { event.preventDefault(); void this.completeConsole(input); }
    };
  }

  private renderConsoleHost(host: HTMLElement): void {
    const output = this.allOutput().filter((entry) => !["telemetry", "adapter", "lifecycle", "echo", "dap"].includes(entry.category) && Date.parse(entry.timestamp) >= this.consoleClearedAt);
    host.innerHTML = `<section class="debug-panel-view"><header><span>Frame-aware expressions use the selected stack frame.</span><input type="search" class="debug-panel-filter" aria-label="Filter Debug Console" placeholder="Filter" value="${escapeHTML(this.consoleFilter)}" data-debug-console-filter><button type="button" data-console-action="clear"><span class="codicon codicon-clear-all"></span> Clear</button></header><div class="debug-console-output" aria-live="polite">${output.map(renderOutputEntry).join("")}${this.consoleEntries.map((entry) => `<div class="debug-console-entry is-${escapeHTML(entry.category)}">${entry.expression ? `<span class="debug-console-prompt">› ${escapeHTML(entry.expression)}</span>` : ""}<pre>${escapeHTML(entry.value)}</pre></div>`).join("")}</div><div class="debug-repl-row"><span>›</span><textarea rows="1" aria-label="Debug Console expression" placeholder="Evaluate expression (Shift+Enter for a new line)" data-debug-repl></textarea></div></section>`;
    this.applyPanelFilter(host, this.consoleFilter);
    const outputHost = host.querySelector<HTMLElement>(".debug-console-output");
    if (outputHost) outputHost.scrollTop = outputHost.scrollHeight;
  }

  private mountOutput(host: HTMLElement): void {
    host.dataset.debugPanel = "debug-output";
    this.renderOutputHost(host);
    host.onclick = (event) => {
      const source = (event.target as Element).closest<HTMLElement>("[data-debug-output-source]");
      if (source) { void this.openEncodedOutputSource(source.dataset.debugOutputSource || ""); return; }
      const cancel = (event.target as Element).closest<HTMLElement>("[data-debug-cancel-progress]");
      if (cancel) { void this.cancelProgress(cancel.dataset.sessionId || "", cancel.dataset.progressId || ""); return; }
      if ((event.target as Element).closest("[data-output-action=clear]")) { this.outputClearedAt = Date.now(); this.renderOutputHost(host); }
    };
    host.onchange = (event) => {
      const toggle = (event.target as Element).closest<HTMLInputElement>("[data-debug-trace]");
      if (toggle) void this.setDAPTrace(toggle.checked);
    };
    host.oninput = (event) => {
      const filter = (event.target as Element).closest<HTMLInputElement>("[data-debug-output-filter]");
      if (!filter) return;
      this.outputFilter = filter.value;
      this.applyPanelFilter(host, this.outputFilter);
    };
  }

  private renderOutputHost(host: HTMLElement): void {
    const session = this.activeSession();
    const entries = this.allOutput().filter((entry) => ["adapter", "lifecycle", "echo", "telemetry", "dap"].includes(entry.category) && Date.parse(entry.timestamp) >= this.outputClearedAt);
    const progress = [...this.progress.values()].map((item) => `<div class="debug-progress-row"><span class="codicon codicon-loading codicon-modifier-spin"></span><strong>${escapeHTML(item.title)}</strong><span>${escapeHTML(item.message || "")}${item.percentage !== undefined ? ` ${Math.max(0, Math.min(100, item.percentage))}%` : ""}</span>${item.cancellable ? `<button type="button" data-debug-cancel-progress data-session-id="${item.sessionId}" data-progress-id="${escapeHTML(item.progressId)}">Cancel</button>` : ""}</div>`).join("");
    const content = progress + entries.map(renderOutputEntry).join("");
    host.innerHTML = `<section class="debug-panel-view"><header><span>Debugger lifecycle, adapter stderr, hooks, telemetry, and optional redacted protocol tracing</span><input type="search" class="debug-panel-filter" aria-label="Filter Debug Output" placeholder="Filter" value="${escapeHTML(this.outputFilter)}" data-debug-output-filter><label class="debug-trace-toggle"><input type="checkbox" data-debug-trace ${session?.traceDAP ? "checked" : ""} ${session && !["terminated", "failed"].includes(session.status) ? "" : "disabled"}> Trace Protocol</label><button type="button" data-output-action="clear"><span class="codicon codicon-clear-all"></span> Clear</button></header><div class="debug-console-output">${content || `<p class="debug-section-empty">No debugger diagnostics.</p>`}</div></section>`;
    this.applyPanelFilter(host, this.outputFilter);
  }

  private applyPanelFilter(host: HTMLElement, filter: string): void {
    const query = filter.trim().toLowerCase();
    host.querySelectorAll<HTMLElement>(".debug-output-entry, .debug-console-entry, .debug-progress-row").forEach((row) => {
      row.hidden = Boolean(query) && !String(row.textContent || "").toLowerCase().includes(query);
    });
  }

  private async cancelProgress(sessionId: string, progressId: string): Promise<void> {
    const session = this.snapshot.sessions.find((item) => item.id === sessionId);
    if (!session || !progressId || !commandSupported(session, "cancel")) return;
    try { await dapRequest(this.options.workspaceId, session.id, "cancel", session.revision, session.stopGeneration, { progressId }); }
    catch (error) { await this.handleRequestError(error); }
  }

  private async setDAPTrace(enabled: boolean): Promise<void> {
    const session = this.activeSession();
    if (!session) return;
    try {
      const updated = await setDebugTrace(this.options.workspaceId, session.id, session.revision, enabled);
      this.snapshot = { ...this.snapshot, sessions: this.snapshot.sessions.map((item) => item.id === updated.id ? updated : item) };
      this.renderPanel("debug-output");
    } catch (error) { await this.handleRequestError(error); }
  }

  private async openEncodedOutputSource(encoded: string): Promise<void> {
    try {
      const value = JSON.parse(decodeURIComponent(encoded)) as { source?: DebugSource; line?: number; column?: number };
      await this.openDebugSource(value.source, value.line || 1, value.column || 1);
    } catch { toast("The debugger output source reference is invalid."); }
  }

  private renderPanel(kind: PanelKind): void {
    const host = document.querySelector<HTMLElement>(`[data-debug-panel=${kind}]`);
    if (!host) return;
    if (kind === "debug-console") this.renderConsoleHost(host);
    else this.renderOutputHost(host);
  }

  private async evaluateConsole(expression: string, host: HTMLElement): Promise<void> {
    expression = expression.trim();
    const session = this.activeSession();
    if (!expression || !session || session.status !== "stopped") return;
    this.consoleHistory.push(expression);
    this.consoleHistory = this.consoleHistory.slice(-100);
    this.consoleHistoryIndex = this.consoleHistory.length;
    this.persistBrowserPreferences();
    const input = host.querySelector<HTMLTextAreaElement>("[data-debug-repl]");
    if (input) input.value = "";
    try {
      const response = await dapRequest<{ result?: string; type?: string }>(this.options.workspaceId, session.id, "evaluate", session.revision, session.stopGeneration, { expression, frameId: this.frameSelection?.frame.id, context: "repl" });
      this.consoleEntries.push({ expression, value: response.body.result || "", category: "result" });
    } catch (error) {
      this.consoleEntries.push({ expression, value: errorMessage(error), category: "error" });
    }
    this.renderConsoleHost(host);
  }

  private async completeConsole(input: HTMLTextAreaElement): Promise<void> {
    const session = this.activeSession();
    if (!session || session.status !== "stopped" || !capability(session, "supportsCompletionsRequest")) return;
    const cursor = input.selectionStart ?? input.value.length;
    try {
      const response = await dapRequest<{ targets?: Array<{ label: string; text?: string; start?: number; length?: number; detail?: string }> }>(this.options.workspaceId, session.id, "completions", session.revision, session.stopGeneration, {
        text: input.value, column: cursor + 1, frameId: this.frameSelection?.frame.id,
      });
      const targets = response.body.targets || [];
      if (!targets.length) return;
      let target = targets[0];
      if (targets.length > 1) {
        const labels = targets.map((item, index) => `${item.label}${item.detail ? ` — ${item.detail}` : ""}${targets.filter((candidate) => candidate.label === item.label).length > 1 ? ` (${index + 1})` : ""}`);
        const chosen = await choiceInput({ id: "completion", type: "pickString", description: "Debug Console Completions", options: labels });
        if (chosen === null) return;
        target = targets[labels.indexOf(chosen)] || target;
      }
      const start = Math.max(0, Math.min(cursor, target.start ?? cursor));
      const length = Math.max(0, target.length ?? 0);
      const text = target.text || target.label;
      input.setRangeText(text, start, Math.min(input.value.length, start + length), "end");
      input.focus();
    } catch (error) { await this.handleRequestError(error); }
  }

  private allOutput(): DebugOutput[] {
    return this.snapshot.sessions.flatMap((session) => session.output || []).sort((left, right) => Date.parse(left.timestamp) - Date.parse(right.timestamp));
  }

  private async stopGroup(groupId: string): Promise<void> {
    if (!groupId) return;
    try { this.applySnapshot(await stopDebugGroup(this.options.workspaceId, groupId, this.groupRevisions(groupId))); this.render(); }
    catch (error) { toast(errorMessage(error)); }
  }

  private async restartGroup(groupId: string): Promise<void> {
    if (!groupId) return;
    try { this.applySnapshot(await restartDebugGroup(this.options.workspaceId, groupId, this.groupRevisions(groupId))); this.render(); }
    catch (error) { await this.handleRequestError(error); }
  }

  private selectSession(id: string): void {
    this.selectedSessionId = id;
    this.persistBrowserPreferences();
    this.invalidateInspection();
    const session = this.activeSession();
    if (session?.status === "stopped") void this.inspectStopped(session);
    this.render();
  }

  private groupRevisions(groupId: string): Record<string, number> {
    return Object.fromEntries(this.snapshot.sessions.filter((session) => session.groupId === groupId).map((session) => [session.id, session.revision]));
  }

  private invalidateInspection(): void {
    this.inspectionGeneration++;
    this.frameSelection = null;
    this.scopes = [];
    this.variables.clear();
    this.variableTotals.clear();
    this.completeVariableReferences.clear();
    this.watchResults.clear();
    this.clearInlineValues();
    this.refreshEditorDecorations();
  }

  private inspectionCurrent(session: DebugSession, generation: number): boolean {
    const current = this.snapshot.sessions.find((item) => item.id === session.id);
    return generation === this.inspectionGeneration && current?.status === "stopped" && current.stopGeneration === session.stopGeneration;
  }

  private persistBrowserPreferences(): void {
    writeDebugBrowserPreferences(this.options.workspaceId, {
      selectedSessionId: this.selectedSessionId || undefined,
      collapsed: [...this.collapsed], consoleHistory: this.consoleHistory.slice(-100), frame: this.savedFrame,
    });
  }

  private async handleRequestError(error: unknown): Promise<void> {
    const status = (error as { status?: number })?.status;
    if (status === 409) await this.resync();
    else toast(errorMessage(error), { sticky: true });
  }

  private async collectInputs(inputs: DebugInput[]): Promise<Record<string, string> | null> {
    const values: Record<string, string> = {};
    for (const input of inputs) {
      if (input.type === "pickString") {
        const value = await choiceInput(input);
        if (value === null) return null;
        values[input.id] = value;
      } else if (input.type === "pickProcess") {
        try {
          const value = await processInput(input, await listDebugProcesses(this.options.workspaceId));
          if (value === null) return null;
          values[input.id] = value;
        } catch (error) { toast(errorMessage(error), { sticky: true }); return null; }
      } else {
        const value = await debugPrompt(input);
        if (value === null) return null;
        values[input.id] = value;
      }
    }
    return values;
  }

  private launchRequiresActiveFile(): boolean {
    const fileVariable = /\$\{(?:file|fileDirname|fileBasename|fileBasenameNoExtension|fileExtname|relativeFile)\}/;
    const configurationIds = this.config.compounds?.find((item) => item.id === this.selectedLaunchId)?.configurationIds
      || (this.config.configurations?.some((item) => item.id === this.selectedLaunchId) ? [this.selectedLaunchId] : []);
    const values: unknown[] = [];
    for (const id of configurationIds) {
      const configuration = this.config.configurations?.find((item) => item.id === id);
      if (!configuration) continue;
      values.push(configuration);
      const profile = this.profiles.find((item) => item.id === configuration.adapterProfileId);
      if (profile) values.push(profile);
      if (this.config.overrides?.[configuration.adapterProfileId]) values.push(this.config.overrides[configuration.adapterProfileId]);
    }
    return values.some((value) => fileVariable.test(JSON.stringify(value)));
  }

  private showSettings(draft: WorkspaceDebugConfig = this.config): void {
    document.querySelector(".debug-settings-overlay")?.remove();
    const overlay = document.createElement("div");
    overlay.className = "debug-settings-overlay";
    const configurations = draft.configurations || [];
    const notificationPermission = debugStopNotificationPermission();
    const notificationStatus = notificationPermission === "granted"
      ? "Browser notifications are allowed."
      : notificationPermission === "denied"
        ? "Browser notifications are blocked in browser or operating-system settings."
        : notificationPermission === "unsupported"
          ? "Browser notifications are unavailable in this environment."
          : `Browser permission is required. <button type="button" data-settings-action="allow-stop-notifications">Allow notifications</button>`;
    overlay.innerHTML = `<section class="debug-settings-dialog" role="dialog" aria-modal="true" aria-labelledby="debug-settings-title"><header><div><h2 id="debug-settings-title">Workspace Debugging</h2><p>Adapter executables stay user-installed. Workspace launches are saved in <code>.echo/workspace.json</code>; breakpoints and watches stay machine-local.</p></div><button type="button" aria-label="Close" data-settings-action="close"><span class="codicon codicon-close"></span></button></header><div class="debug-settings-body"><section><h3>Adapter profiles</h3><div class="debug-profile-list">${this.profiles.map((profile) => `<div><span class="codicon codicon-extensions"></span><div><strong>${escapeHTML(profile.name)}</strong><small>${escapeHTML(profile.adapterId)} · ${escapeHTML(profile.transport.kind)} · ${escapeHTML(profile.command || `${profile.transport.host}:${profile.transport.port}`)}</small></div><button type="button" data-settings-action="test-profile" data-profile-id="${profile.id}">Test</button><button type="button" data-settings-action="edit-profile" data-profile-id="${profile.id}">Edit</button><button type="button" data-settings-action="delete-profile" data-profile-id="${profile.id}" aria-label="Delete ${escapeHTML(profile.name)}"><span class="codicon codicon-trash"></span></button></div>`).join("") || `<p>No machine adapter profiles yet.</p>`}</div><div class="debug-template-list">${this.templates.filter((template) => !this.profiles.some((profile) => profile.id === template.profile.id)).map((template) => `<button type="button" data-settings-action="add-template" data-template-id="${template.id}" title="${escapeHTML(template.installGuide)}"><span class="codicon codicon-add"></span>${escapeHTML(template.profile.name)}</button>`).join("")}</div><div class="debug-stop-notification-setting"><h4>Debugger attention</h4><label><input type="checkbox" data-debug-stop-notifications ${debugStopNotificationsEnabled() ? "checked" : ""}> Notify when the debugger stops while Echo is in the background</label><small>${notificationStatus}</small></div></section><section class="debug-config-editor"><div><h3>Workspace configuration</h3><button type="button" data-settings-action="import">Preview VS Code Import</button></div><textarea aria-label="Workspace debug configuration" spellcheck="false" data-debug-config-json>${escapeHTML(JSON.stringify(this.config, null, 2))}</textarea><p class="debug-settings-error" data-settings-error></p></section></div><footer><button type="button" data-settings-action="close">Cancel</button><button type="button" class="is-primary" data-settings-action="save">Save Workspace Configuration</button></footer></section>`;
    overlay.querySelector<HTMLTextAreaElement>("[data-debug-config-json]")!.value = JSON.stringify(draft, null, 2);
    const profileHeading = overlay.querySelector<HTMLElement>(".debug-settings-body > section:first-child h3")!;
    const profileHeadingRow = document.createElement("div");
    profileHeadingRow.className = "debug-settings-section-title";
    profileHeading.parentElement!.insertBefore(profileHeadingRow, profileHeading);
    profileHeadingRow.append(profileHeading);
    profileHeadingRow.insertAdjacentHTML("beforeend", `<button type="button" data-settings-action="new-profile"><span class="codicon codicon-add"></span>New Profile</button>`);
    const configList = document.createElement("div");
    configList.className = "debug-configuration-list";
    configList.innerHTML = `<header><strong>Launch and attach configurations</strong><button type="button" data-settings-action="add-configuration"><span class="codicon codicon-add"></span>Add</button></header>${configurations.map((entry) => `<div><span class="codicon codicon-debug-alt"></span><div><strong>${escapeHTML(entry.name)}</strong><small>${escapeHTML(entry.request)} · ${escapeHTML(entry.adapterProfileId)}</small></div><button type="button" data-settings-action="edit-configuration" data-configuration-id="${escapeHTML(entry.id)}">Edit</button><button type="button" aria-label="Remove ${escapeHTML(entry.name)}" data-settings-action="remove-configuration" data-configuration-id="${escapeHTML(entry.id)}"><span class="codicon codicon-trash"></span></button></div>`).join("") || `<p>No configurations yet. Add one with the common-fields editor, or edit the complete JSON below.</p>`}`;
    const configTextarea = overlay.querySelector<HTMLTextAreaElement>("[data-debug-config-json]")!;
    configTextarea.parentElement!.insertBefore(configList, configTextarea);
    const close = () => overlay.remove();
    overlay.addEventListener("click", (event) => {
      if (event.target === overlay) close();
      const button = (event.target as Element).closest<HTMLElement>("[data-settings-action]");
      if (!button) return;
      const action = button.dataset.settingsAction;
      if (action === "close") close();
      else if (action === "save") void this.saveSettingsOverlay(overlay, close);
      else if (action === "new-profile") void this.createProfile(overlay);
      else if (action === "add-template") void this.addTemplate(button.dataset.templateId || "", overlay);
      else if (action === "test-profile") void this.testProfile(button.dataset.profileId || "", button);
      else if (action === "edit-profile") void this.editProfile(button.dataset.profileId || "", overlay);
      else if (action === "delete-profile") void this.deleteProfile(button.dataset.profileId || "", overlay);
      else if (action === "add-configuration") void this.editConfiguration("", overlay);
      else if (action === "edit-configuration") void this.editConfiguration(button.dataset.configurationId || "", overlay);
      else if (action === "remove-configuration") this.removeConfiguration(button.dataset.configurationId || "", overlay);
      else if (action === "import") void this.importVSCode(overlay);
      else if (action === "allow-stop-notifications") void requestDebugStopNotificationPermission().then(() => { if (overlay.isConnected) { const latest = this.settingsDraft(overlay); overlay.remove(); this.showSettings(latest); } });
    });
    overlay.querySelector<HTMLInputElement>("[data-debug-stop-notifications]")?.addEventListener("change", (event) => {
      const enabled = (event.currentTarget as HTMLInputElement).checked;
      setDebugStopNotificationsEnabled(enabled);
      if (enabled && debugStopNotificationPermission() === "default") {
        void requestDebugStopNotificationPermission().then(() => { if (overlay.isConnected) { const latest = this.settingsDraft(overlay); overlay.remove(); this.showSettings(latest); } });
      }
    });
    overlay.addEventListener("keydown", (event) => { if (event.key === "Escape") close(); });
    document.body.appendChild(overlay);
    requestAnimationFrame(() => overlay.querySelector<HTMLTextAreaElement>("textarea")?.focus());
  }

  private async saveSettingsOverlay(overlay: HTMLElement, close: () => void): Promise<void> {
    const errorHost = overlay.querySelector<HTMLElement>("[data-settings-error]")!;
    try {
      const config = JSON.parse(overlay.querySelector<HTMLTextAreaElement>("[data-debug-config-json]")!.value) as WorkspaceDebugConfig;
      this.config = await saveDebugConfig(this.options.workspaceId, config);
      close();
      this.selectedLaunchId = this.config.configurations?.[0]?.id || this.config.compounds?.[0]?.id || "";
      this.render();
      toast("Workspace debug configuration saved.");
    } catch (error) { errorHost.textContent = errorMessage(error); }
  }

  private settingsDraft(overlay: HTMLElement): WorkspaceDebugConfig {
    return JSON.parse(overlay.querySelector<HTMLTextAreaElement>("[data-debug-config-json]")!.value) as WorkspaceDebugConfig;
  }

  private async createProfile(overlay: HTMLElement): Promise<void> {
    let id = "custom-adapter";
    for (let suffix = 2; this.profiles.some((profile) => profile.id === id); suffix++) id = `custom-adapter-${suffix}`;
    const raw = await promptLargeJSON("Create Adapter Profile", {
      id, name: "Custom Adapter", adapterId: "custom", command: "debug-adapter",
      args: [], environment: {}, selectors: [], transport: { kind: "stdio", startupTimeoutMs: 15000 },
    });
    if (!raw) return;
    try {
      const draft = this.settingsDraft(overlay);
      this.profiles.push(await addAdapterProfile(raw as unknown as AdapterProfile));
      overlay.remove();
      this.showSettings(draft);
    } catch (error) { toast(errorMessage(error), { sticky: true }); }
  }

  private async editConfiguration(id: string, overlay: HTMLElement): Promise<void> {
    const errorHost = overlay.querySelector<HTMLElement>("[data-settings-error]")!;
    try {
      const draft = this.settingsDraft(overlay);
      const existing = (draft.configurations || []).find((entry) => entry.id === id);
      let generatedID = "debug";
      for (let suffix = 2; (draft.configurations || []).some((entry) => entry.id === generatedID); suffix++) generatedID = `debug-${suffix}`;
      const edited = await promptDebugConfiguration(existing || {
        id: generatedID, name: "New Debug Configuration", adapterProfileId: this.profiles[0]?.id || "", request: "launch", arguments: {},
      }, this.profiles, Boolean(existing));
      if (!edited) return;
      draft.configurations = existing
        ? (draft.configurations || []).map((entry) => entry.id === id ? edited : entry)
        : [...(draft.configurations || []), edited];
      if (edited.adapterProfileId && !(draft.enabledAdapterProfileIds || []).includes(edited.adapterProfileId)) {
        draft.enabledAdapterProfileIds = [...(draft.enabledAdapterProfileIds || []), edited.adapterProfileId];
      }
      overlay.remove();
      this.showSettings(draft);
    } catch (error) { errorHost.textContent = errorMessage(error); }
  }

  private removeConfiguration(id: string, overlay: HTMLElement): void {
    const errorHost = overlay.querySelector<HTMLElement>("[data-settings-error]")!;
    try {
      const draft = this.settingsDraft(overlay);
      const existing = (draft.configurations || []).find((entry) => entry.id === id);
      if (!existing || !window.confirm(`Remove ${existing.name}? Compound references to it will also be removed.`)) return;
      draft.configurations = (draft.configurations || []).filter((entry) => entry.id !== id);
      draft.compounds = (draft.compounds || [])
        .map((compound) => ({ ...compound, configurationIds: compound.configurationIds.filter((configurationID) => configurationID !== id) }))
        .filter((compound) => compound.configurationIds.length > 0);
      overlay.remove();
      this.showSettings(draft);
    } catch (error) { errorHost.textContent = errorMessage(error); }
  }

  private async addTemplate(id: string, overlay: HTMLElement): Promise<void> {
    try {
      const draft = this.settingsDraft(overlay);
      this.profiles.push(await addAdapterTemplate(id));
      overlay.remove();
      this.showSettings(draft);
    } catch (error) { toast(errorMessage(error)); }
  }

  private async testProfile(id: string, button: HTMLElement): Promise<void> {
    button.textContent = "Testing…";
    try {
      const diagnostic = await diagnoseAdapter(this.options.workspaceId, id);
      toast(`${diagnostic.available ? "Ready" : "Unavailable"} in ${diagnostic.execution}: ${diagnostic.message}`, { sticky: !diagnostic.available });
    } catch (error) { toast(errorMessage(error), { sticky: true }); }
    button.textContent = "Test";
  }

  private async editProfile(id: string, overlay: HTMLElement): Promise<void> {
    const profile = this.profiles.find((item) => item.id === id);
    if (!profile) return;
    const raw = await promptLargeJSON("Edit Adapter Profile", profile);
    if (!raw) return;
    try {
      const draft = this.settingsDraft(overlay);
      const updated = await updateAdapterProfile(raw as AdapterProfile);
      this.profiles = this.profiles.map((item) => item.id === updated.id ? updated : item);
      overlay.remove(); this.showSettings(draft);
    } catch (error) { toast(errorMessage(error), { sticky: true }); }
  }

  private async deleteProfile(id: string, overlay: HTMLElement): Promise<void> {
    try {
      const draft = this.settingsDraft(overlay);
      await deleteAdapterProfile(id);
      this.profiles = this.profiles.filter((item) => item.id !== id);
      overlay.remove(); this.showSettings(draft);
    }
    catch (error) { toast(errorMessage(error), { sticky: true }); }
  }

  private async importVSCode(overlay: HTMLElement): Promise<void> {
    const errorHost = overlay.querySelector<HTMLElement>("[data-settings-error]")!;
    try {
      const preview = await previewVSCodeImport(this.options.workspaceId);
      const warnings = preview.warnings || [];
      const accepted = window.confirm(`${warnings.length ? warnings.map((warning) => `• ${warning.message}`).join("\n") : "No import warnings."}\n\nLoad this preview into the editor? Nothing is saved until you choose Save.`);
      if (accepted) { overlay.remove(); this.showSettings(preview.config); }
    } catch (error) { errorHost.textContent = errorMessage(error); }
  }
}

function renderOutputEntry(entry: DebugOutput): string {
  const source = entry.data?.source as DebugSource | undefined;
  const location = source ? encodeURIComponent(JSON.stringify({ source, line: entry.data?.line, column: entry.data?.column })) : "";
  return `<div class="debug-output-entry is-${escapeHTML(entry.category || "console")}"><span>${escapeHTML(entry.category || "console")}</span><pre>${escapeHTML(entry.output)}</pre>${source ? `<button type="button" class="debug-output-source" data-debug-output-source="${escapeHTML(location)}"><span class="codicon codicon-go-to-file"></span>${escapeHTML(source.name || source.path || "Open source")}:${escapeHTML(entry.data?.line || "")}</button>` : ""}</div>`;
}

function breakpointTooltip(breakpoint: SourceBreakpoint, sessions: DebugSession[]): string {
  if (!breakpoint.enabled) return "Disabled breakpoint";
  const details = breakpoint.logMessage ? `Logpoint: ${breakpoint.logMessage}`
    : breakpoint.condition ? `Breakpoint when ${breakpoint.condition}`
      : breakpoint.hitCondition ? `Breakpoint hit count ${breakpoint.hitCondition}` : "Breakpoint";
  const statuses = sessions.flatMap((session) => session.breakpoints || []).filter((status) => status.stateId === breakpoint.id);
  const adapter = statuses.map((status) => status.message || (status.verified ? status.line && status.line !== breakpoint.line ? `Verified and relocated to line ${status.line}` : "Verified" : "Unverified")).filter(Boolean);
  return [details, ...new Set(adapter)].map(escapeMarkdown).join("\n\n");
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function escapeMarkdown(value: string): string {
  return value.replace(/[\\`*_{}[\]()#+.!|>-]/g, "\\$&");
}

function readDebugBrowserPreferences(workspaceId: string): DebugBrowserPreferences {
  try {
    const value = JSON.parse(window.localStorage.getItem(`echo.debug.ui.v1:${workspaceId}`) || "{}");
    if (!value || typeof value !== "object" || Array.isArray(value)) return {};
    const candidate = value as DebugBrowserPreferences;
    return {
      selectedSessionId: typeof candidate.selectedSessionId === "string" ? candidate.selectedSessionId : undefined,
      collapsed: Array.isArray(candidate.collapsed) ? candidate.collapsed.filter((item): item is string => typeof item === "string") : [],
      consoleHistory: Array.isArray(candidate.consoleHistory) ? candidate.consoleHistory.filter((item): item is string => typeof item === "string").slice(-100) : [],
      frame: candidate.frame && typeof candidate.frame.sessionId === "string" && Number.isFinite(candidate.frame.threadId) && Number.isFinite(candidate.frame.frameId) ? candidate.frame : undefined,
    };
  } catch { return {}; }
}

function writeDebugBrowserPreferences(workspaceId: string, preferences: DebugBrowserPreferences): void {
  try { window.localStorage.setItem(`echo.debug.ui.v1:${workspaceId}`, JSON.stringify(preferences)); } catch { /* Browser storage is optional. */ }
}

function debugPrompt(input: DebugInput): Promise<string | null> {
  return new Promise((resolve) => {
    const overlay = document.createElement("div");
    overlay.className = "code-modal-overlay";
    overlay.innerHTML = `<form class="code-modal code-prompt-modal" role="dialog" aria-modal="true"><h2>${escapeHTML(input.description || input.id)}</h2><label>${input.type === "pickProcess" ? "Process ID" : "Value"}<input name="value" type="${input.type === "secret" ? "password" : "text"}" value="${escapeHTML(input.default || "")}" required autocomplete="${input.type === "secret" ? "off" : "on"}"></label>${input.type === "pickProcess" ? `<p class="code-modal-detail">Enter a PID from the workspace's ${"host or sandbox"} process list.</p>` : ""}<div class="code-modal-actions"><button type="button" data-cancel>Cancel</button><button type="submit" class="is-primary">Continue</button></div></form>`;
    const finish = (value: string | null) => { overlay.remove(); resolve(value); };
    overlay.querySelector("[data-cancel]")?.addEventListener("click", () => finish(null));
    overlay.querySelector("form")?.addEventListener("submit", (event) => { event.preventDefault(); finish((new FormData(event.currentTarget as HTMLFormElement).get("value") || "").toString()); });
    overlay.addEventListener("keydown", (event) => { if (event.key === "Escape") finish(null); });
    document.body.appendChild(overlay);
    requestAnimationFrame(() => overlay.querySelector<HTMLInputElement>("input")?.focus());
  });
}

function choiceInput(input: DebugInput): Promise<string | null> {
  return new Promise((resolve) => {
    const overlay = document.createElement("div");
    overlay.className = "code-modal-overlay";
    overlay.innerHTML = `<section class="code-modal" role="dialog" aria-modal="true"><h2>${escapeHTML(input.description || input.id)}</h2><div class="debug-input-choices">${(input.options || []).map((option) => `<button type="button" data-value="${escapeHTML(option)}">${escapeHTML(option)}</button>`).join("")}</div><div class="code-modal-actions"><button type="button" data-cancel>Cancel</button></div></section>`;
    const finish = (value: string | null) => { overlay.remove(); resolve(value); };
    overlay.addEventListener("click", (event) => { const button = (event.target as Element).closest<HTMLElement>("[data-value]"); if (button) finish(button.dataset.value || ""); else if ((event.target as Element).closest("[data-cancel]") || event.target === overlay) finish(null); });
    overlay.addEventListener("keydown", (event) => { if (event.key === "Escape") finish(null); });
    document.body.appendChild(overlay);
  });
}

function processInput(input: DebugInput, processes: Array<{ pid: number; name: string; commandLine?: string; execution: string }>): Promise<string | null> {
  return new Promise((resolve) => {
    const overlay = document.createElement("div");
    overlay.className = "code-modal-overlay";
    const rows = () => processes.map((process) => `<button type="button" data-process-pid="${process.pid}" data-process-search="${escapeHTML(`${process.name} ${process.commandLine || ""} ${process.pid}`.toLowerCase())}"><strong>${escapeHTML(process.name)}</strong><span>PID ${process.pid} · ${escapeHTML(process.execution)}</span><small>${escapeHTML(process.commandLine || "")}</small></button>`).join("");
    overlay.innerHTML = `<section class="code-modal debug-process-picker" role="dialog" aria-modal="true" aria-label="${escapeHTML(input.description || "Select process")}"><h2>${escapeHTML(input.description || "Select Process")}</h2><input type="search" aria-label="Filter processes" placeholder="Filter by name, command, or PID"><div class="debug-process-list" role="listbox">${rows() || `<p>No processes are available.</p>`}</div><div class="code-modal-actions"><button type="button" data-manual>Enter PID…</button><button type="button" data-cancel>Cancel</button></div></section>`;
    const finish = (value: string | null) => { overlay.remove(); resolve(value); };
    overlay.addEventListener("click", async (event) => {
      const process = (event.target as Element).closest<HTMLElement>("[data-process-pid]");
      if (process) finish(process.dataset.processPid || "");
      else if ((event.target as Element).closest("[data-cancel]") || event.target === overlay) finish(null);
      else if ((event.target as Element).closest("[data-manual]")) {
        const value = await debugPrompt(input);
        if (value !== null) finish(value);
      }
    });
    overlay.querySelector<HTMLInputElement>("[type=search]")?.addEventListener("input", (event) => {
      const query = (event.currentTarget as HTMLInputElement).value.trim().toLowerCase();
      overlay.querySelectorAll<HTMLElement>("[data-process-search]").forEach((row) => { row.hidden = Boolean(query) && !String(row.dataset.processSearch || "").includes(query); });
    });
    overlay.addEventListener("keydown", (event) => { if (event.key === "Escape") finish(null); });
    document.body.appendChild(overlay);
    requestAnimationFrame(() => overlay.querySelector<HTMLInputElement>("[type=search]")?.focus());
  });
}

function promptDebugConfiguration(value: DebugConfiguration, profiles: AdapterProfile[], lockID: boolean): Promise<DebugConfiguration | null> {
  return new Promise((resolve) => {
    const overlay = document.createElement("div");
    overlay.className = "debug-settings-overlay";
    overlay.innerHTML = `<form class="debug-configuration-dialog" role="dialog" aria-modal="true" aria-labelledby="debug-configuration-title">
      <header><div><h2 id="debug-configuration-title">${lockID ? "Edit" : "Add"} Debug Configuration</h2><p>Common launch fields are structured here. Adapter-specific arguments and lifecycle hooks remain validated JSON.</p></div><button type="button" aria-label="Close" data-cancel><span class="codicon codicon-close"></span></button></header>
      <div class="debug-configuration-fields">
        <label>ID<input name="id" required pattern="[a-z0-9][a-z0-9._-]{0,63}" value="${escapeHTML(value.id)}" ${lockID ? "readonly" : ""}></label>
        <label>Name<input name="name" required value="${escapeHTML(value.name)}"></label>
        <label>Adapter profile<select name="profile" required>${profiles.map((profile) => `<option value="${escapeHTML(profile.id)}" ${profile.id === value.adapterProfileId ? "selected" : ""}>${escapeHTML(profile.name)} · ${escapeHTML(profile.adapterId)}</option>`).join("")}</select></label>
        <label>Request<select name="request"><option value="launch" ${value.request === "launch" ? "selected" : ""}>Launch</option><option value="attach" ${value.request === "attach" ? "selected" : ""}>Attach</option></select></label>
        <label class="is-wide">Adapter arguments (JSON object)<textarea name="arguments" spellcheck="false">${escapeHTML(JSON.stringify(value.arguments || {}, null, 2))}</textarea></label>
        <label class="is-wide">Pre-launch hook (JSON object or blank)<textarea name="preLaunch" spellcheck="false" placeholder='{"command":"go","args":["build","./..."]}'>${escapeHTML(value.preLaunch ? JSON.stringify(value.preLaunch, null, 2) : "")}</textarea></label>
        <label class="is-wide">Post-debug hook (JSON object or blank)<textarea name="postDebug" spellcheck="false" placeholder='{"command":"cleanup","args":[]}'>${escapeHTML(value.postDebug ? JSON.stringify(value.postDebug, null, 2) : "")}</textarea></label>
      </div>
      <p class="debug-settings-error" data-error></p>
      <footer><button type="button" data-cancel>Cancel</button><button type="submit" class="is-primary">Apply to Draft</button></footer>
    </form>`;
    const form = overlay.querySelector<HTMLFormElement>("form")!;
    const finish = (result: DebugConfiguration | null) => { overlay.remove(); resolve(result); };
    overlay.addEventListener("click", (event) => {
      if (event.target === overlay || (event.target as Element).closest("[data-cancel]")) finish(null);
    });
    overlay.addEventListener("keydown", (event) => { if (event.key === "Escape") finish(null); });
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      const data = new FormData(form);
      const parseObject = (field: string, label: string, optional = false): Record<string, unknown> | undefined => {
        const raw = String(data.get(field) || "").trim();
        if (!raw && optional) return undefined;
        const parsed = JSON.parse(raw || "{}");
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) throw new Error(`${label} must be a JSON object.`);
        return parsed as Record<string, unknown>;
      };
      try {
        const id = String(data.get("id") || "").trim().toLowerCase();
        const name = String(data.get("name") || "").trim();
        if (!/^[a-z0-9][a-z0-9._-]{0,63}$/.test(id)) throw new Error("ID must start with a letter or number and use at most 64 lowercase letters, numbers, dots, underscores, or dashes.");
        if (!name) throw new Error("Name is required.");
        const preLaunch = parseObject("preLaunch", "Pre-launch hook", true) as DebugConfiguration["preLaunch"];
        const postDebug = parseObject("postDebug", "Post-debug hook", true) as DebugConfiguration["postDebug"];
        finish({
          id, name, adapterProfileId: String(data.get("profile") || ""), request: String(data.get("request")) as "launch" | "attach",
          arguments: parseObject("arguments", "Adapter arguments") || {},
          ...(preLaunch ? { preLaunch } : {}), ...(postDebug ? { postDebug } : {}),
        });
      } catch (error) { overlay.querySelector<HTMLElement>("[data-error]")!.textContent = errorMessage(error); }
    });
    document.body.appendChild(overlay);
    requestAnimationFrame(() => form.querySelector<HTMLInputElement>(lockID ? "[name=name]" : "[name=id]")?.focus());
  });
}

function promptLargeJSON(title: string, value: unknown): Promise<Record<string, unknown> | null> {
  return new Promise((resolve) => {
    const overlay = document.createElement("div");
    overlay.className = "debug-settings-overlay";
    overlay.innerHTML = `<form class="debug-json-dialog" role="dialog" aria-modal="true"><h2>${escapeHTML(title)}</h2><textarea name="json" spellcheck="false">${escapeHTML(JSON.stringify(value, null, 2))}</textarea><p data-error></p><div><button type="button" data-cancel>Cancel</button><button type="submit" class="is-primary">Save</button></div></form>`;
    const finish = (result: Record<string, unknown> | null) => { overlay.remove(); resolve(result); };
    overlay.querySelector("[data-cancel]")?.addEventListener("click", () => finish(null));
    overlay.querySelector("form")?.addEventListener("submit", (event) => { event.preventDefault(); try { finish(JSON.parse((new FormData(event.currentTarget as HTMLFormElement).get("json") || "{}").toString())); } catch (error) { overlay.querySelector<HTMLElement>("[data-error]")!.textContent = errorMessage(error); } });
    document.body.appendChild(overlay);
  });
}
