import { Terminal, type ITheme } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";

import { icons } from "../../js/icons.js";
import { on as onSocket, onState as onSocketState, send as sendSocket } from "../../js/ws.js";
import { toast } from "../code/ui";
import {
  createSavedCommand, deleteSavedCommand, listSavedCommands, listTerminalSessions, resizeTerminal,
  restartTerminal as restartTerminalAPI, startTerminal, stopTerminal as stopTerminalAPI,
  syncTerminal, updateSavedCommand, writeTerminal,
  type SavedCommand, type TerminalEvent, type TerminalSnapshot,
} from "./terminalApi";
import {
  clampTerminalHeight, decodeTerminalBase64, parseTerminalPreferences,
  splitTerminalInput, terminalSequenceAction,
} from "./terminalUtils";

export type TerminalWorkspace = {
  id: string;
  name: string;
  mainPath: string;
  folders?: string[];
};

type TerminalMeta = {
  id: string;
  shell: string;
  workingDirectory: string;
  status: string;
  exitCode?: number;
  message?: string;
  lastSequence: number;
};
type TerminalPreference = { open?: boolean; maximized?: boolean; height?: number; activePanel?: string };
export type WorkbenchPanel = {
  id: string;
  label: string;
  icon?: string;
  mount(host: HTMLElement): void;
  unmount?(): void;
};

const preferenceKey = "echo.terminalDock.v1";
const defaultHeight = 280;
const terminalColorScheme = typeof window.matchMedia === "function"
  ? window.matchMedia("(prefers-color-scheme: dark)")
  : { matches: false, addEventListener() {}, removeEventListener() {} } as unknown as MediaQueryList;
const controllers = new Map<string, TerminalController>();
const meta = new Map<string, TerminalMeta>();
const commands = new Map<string, SavedCommand[]>();
const commandsLoaded = new Set<string>();
const openWorkspaces = new Set<string>();
const maximizedWorkspaces = new Set<string>();
const heights = new Map<string, number>();
const savedMenus = new Set<string>();
const subscribed = new Set<string>();
const workbenchPanels = new Map<string, Map<string, WorkbenchPanel>>();
const activePanels = new Map<string, string>();
const debugPanelUnregister = new Map<string, () => void>();
const subscriptionWaits = new Map<string, { promise: Promise<void>; resolve(): void }>();
const terminalRestorePromises = new Map<string, Promise<void>>();
let preferencesLoaded = false;
let mountedRegion: HTMLElement | null = null;
let mountedWorkspace: TerminalWorkspace | null = null;

class TerminalController {
  readonly workspaceId: string;
  readonly key: string;
  readonly fixedSession: boolean;
  readonly host: HTMLDivElement;
  readonly terminal: Terminal;
  readonly fitAddon: FitAddon;
  private viewport: HTMLElement | null = null;
  private resizeObserver: ResizeObserver | null = null;
  private startPromise: Promise<void> | null = null;
  private syncPromise: Promise<void> | null = null;
  private writeChain: Promise<void> = Promise.resolve();
  private inputBuffer = "";
  private inputFrame = 0;
  private resizeTimer = 0;
  private sessionId = "";
  private lastSequence = 0;

  constructor(workspaceId: string, sessionId = "") {
    this.workspaceId = workspaceId;
    this.fixedSession = Boolean(sessionId);
    this.sessionId = sessionId;
    this.key = this.fixedSession ? debugTerminalKey(workspaceId, sessionId) : workspaceId;
    this.host = document.createElement("div");
    this.host.className = "terminal-xterm-instance";
    this.host.dataset.terminalXtermWorkspace = workspaceId;
    this.terminal = new Terminal({
      allowProposedApi: false,
      convertEol: false,
      cursorBlink: true,
      cursorStyle: "bar",
      fontFamily: '"Cascadia Mono", "SFMono-Regular", Consolas, "Liberation Mono", monospace',
      fontSize: 13,
      lineHeight: 1.2,
      scrollback: 5000,
      theme: readTerminalTheme(),
    });
    this.fitAddon = new FitAddon();
    this.terminal.loadAddon(this.fitAddon);
    this.terminal.loadAddon(new WebLinksAddon());
    this.terminal.onData((data) => {
      const current = meta.get(this.key);
      if (current?.status === "exited") {
        if (!this.fixedSession && (data.includes("\r") || data.includes("\n"))) void this.restart();
        return;
      }
      this.queueInput(data);
    });
    this.terminal.onResize(({ cols, rows }) => this.queueResize(cols, rows));
  }

  mount(viewport: HTMLElement): void {
    if (this.host.parentElement !== viewport) viewport.appendChild(this.host);
    if (!this.terminal.element) this.terminal.open(this.host);
    this.viewport = viewport;
    this.resizeObserver?.disconnect();
    this.resizeObserver = new ResizeObserver(() => this.fit());
    this.resizeObserver.observe(viewport);
    this.refreshTheme();
    this.fit();
  }

  unmount(): void {
    this.resizeObserver?.disconnect();
    this.viewport = null;
  }

  async ensureStarted(): Promise<void> {
    if (this.fixedSession) {
      if (meta.has(this.key)) return;
      if (this.startPromise) return this.startPromise;
      this.startPromise = this.resync().finally(() => { this.startPromise = null; });
      return this.startPromise;
    }
    if (this.sessionId) return;
    if (this.startPromise) return this.startPromise;
    this.startPromise = this.start().finally(() => { this.startPromise = null; });
    return this.startPromise;
  }

  async resync(): Promise<void> {
    if (!this.sessionId) return this.ensureStarted();
    if (this.syncPromise) return this.syncPromise;
    this.syncPromise = subscribeWorkspace(this.workspaceId)
      .then(() => syncTerminal(this.workspaceId, this.sessionId, this.lastSequence))
      .then((snapshot) => this.applySnapshot(snapshot))
      .catch(async (error) => {
        if (this.fixedSession) {
          this.setMeta({ status: "error", message: errorMessage(error) });
          return;
        }
        const snapshot = await startTerminal(this.workspaceId, this.terminal.cols, this.terminal.rows);
        this.applySnapshot(snapshot, snapshot.id !== this.sessionId);
      })
      .finally(() => { this.syncPromise = null; });
    return this.syncPromise;
  }

  applyEvent(event: TerminalEvent): void {
    if (event.event === "started" && event.sessionId !== this.sessionId) {
      if (!this.fixedSession && event.kind !== "debug" && !this.startPromise) void this.startFromCurrentSession();
      return;
    }
    if (!this.sessionId || event.sessionId !== this.sessionId) return;
    if (event.event === "data") {
      const sequence = Number(event.sequence || 0);
      const action = terminalSequenceAction(this.lastSequence, sequence);
      if (action === "ignore") return;
      if (action === "resync") {
        void this.resync();
        return;
      }
      this.terminal.write(decodeTerminalBase64(event.data || ""));
      this.lastSequence = sequence;
      this.setMeta({ lastSequence: sequence }, false);
      return;
    }
    if (event.event === "exited") {
      this.setMeta({
        status: "exited",
        exitCode: event.exitCode,
        message: event.message,
        lastSequence: Number(event.sequence ?? this.lastSequence),
      });
      if (this.fixedSession) this.terminal.writeln(`\r\n[Process exited with code ${event.exitCode ?? "?"}]`);
    }
  }

  async stop(): Promise<void> {
    if (!this.sessionId) return;
    this.setMeta({ status: "stopping" });
    try {
      await stopTerminalAPI(this.workspaceId, this.sessionId);
    } catch (error) {
      void this.resync();
      throw error;
    }
  }

  async restart(): Promise<void> {
    if (!this.sessionId) return this.ensureStarted();
    const snapshot = await restartTerminalAPI(
      this.workspaceId, this.sessionId, this.terminal.cols, this.terminal.rows,
    );
    this.applySnapshot(snapshot, true);
    this.terminal.focus();
  }

  sendCommand(command: string): boolean {
    if (!this.sessionId) return false;
    this.queueInput(command + "\r");
    this.terminal.focus();
    return true;
  }

  fit(): void {
    if (!this.viewport || !openWorkspaces.has(this.workspaceId)) return;
    window.requestAnimationFrame(() => {
      if (!this.viewport || this.viewport.clientWidth <= 0 || this.viewport.clientHeight <= 0) return;
      try { this.fitAddon.fit(); } catch { /* The dock may be between route fragments. */ }
    });
  }

  refreshTheme(): void { this.terminal.options.theme = readTerminalTheme(); }

  private async start(): Promise<void> {
    try {
      await subscribeWorkspace(this.workspaceId);
      const snapshot = await startTerminal(this.workspaceId, this.terminal.cols, this.terminal.rows);
      this.applySnapshot(snapshot, true);
      this.fit();
      this.terminal.focus();
    } catch (error) {
      this.inputBuffer = "";
      this.setMeta({
        id: "", shell: "Terminal", workingDirectory: "", status: "error",
        message: errorMessage(error), lastSequence: 0,
      });
    }
  }

  private async startFromCurrentSession(): Promise<void> {
    try {
      const snapshot = await startTerminal(this.workspaceId, this.terminal.cols, this.terminal.rows);
      this.applySnapshot(snapshot, snapshot.id !== this.sessionId);
    } catch (error) {
      toast(errorMessage(error));
    }
  }

  private applySnapshot(snapshot: TerminalSnapshot, forceReset = false): void {
    const changedSession = Boolean(this.sessionId) && this.sessionId !== snapshot.id;
    if (forceReset || changedSession || snapshot.reset) {
      this.terminal.reset();
      this.lastSequence = 0;
    }
    this.sessionId = snapshot.id;
    const output = [...(snapshot.output || [])].sort((a, b) => a.sequence - b.sequence);
    for (const chunk of output) {
      if (chunk.sequence <= this.lastSequence) continue;
      this.terminal.write(decodeTerminalBase64(chunk.data));
      this.lastSequence = chunk.sequence;
    }
    this.lastSequence = Math.max(this.lastSequence, snapshot.lastSequence || 0);
    meta.set(this.key, {
      id: snapshot.id,
      shell: snapshot.shell || "Terminal",
      workingDirectory: snapshot.workingDirectory || "",
      status: snapshot.status || "running",
      exitCode: snapshot.exitCode,
      message: snapshot.message,
      lastSequence: this.lastSequence,
    });
    this.flushInput();
    renderMountedDock();
  }

  restore(snapshot: TerminalSnapshot): void {
    this.applySnapshot(snapshot, this.sessionId !== snapshot.id);
  }

  private setMeta(patch: Partial<TerminalMeta>, rerender = true): void {
    const current = meta.get(this.key) || {
      id: this.sessionId, shell: this.fixedSession ? "Debug Terminal" : "Terminal",
      workingDirectory: "", status: "idle", lastSequence: 0,
    };
    meta.set(this.key, { ...current, ...patch });
    if (rerender && mountedWorkspace?.id === this.workspaceId) renderMountedDock();
  }

  private queueInput(data: string): void {
    this.inputBuffer += data;
    if (this.inputFrame) return;
    this.inputFrame = window.requestAnimationFrame(() => {
      this.inputFrame = 0;
      this.flushInput();
    });
  }

  private flushInput(): void {
    if (!this.inputBuffer || !this.sessionId) return;
    const sessionId = this.sessionId;
    const chunks = splitTerminalInput(this.inputBuffer);
    this.inputBuffer = "";
    for (const chunk of chunks) {
      this.writeChain = this.writeChain
        .then(() => writeTerminal(this.workspaceId, sessionId, chunk))
        .catch((error) => {
          // Restart may replace the session while already-queued keystrokes
          // are still completing. The old write is then intentionally stale.
          if (this.sessionId !== sessionId) return;
          toast(errorMessage(error));
          return this.resync();
        });
    }
  }

  private queueResize(cols: number, rows: number): void {
    if (!this.sessionId) return;
    window.clearTimeout(this.resizeTimer);
    this.resizeTimer = window.setTimeout(() => {
      void resizeTerminal(this.workspaceId, this.sessionId, cols, rows).catch(() => this.resync());
    }, 100);
  }
}

export function mountTerminalDock(region: HTMLElement | null, workspace: TerminalWorkspace | null): void {
  loadPreferences();
  if (mountedWorkspace && (mountedWorkspace.id !== workspace?.id || mountedRegion !== region)) {
    unmountWorkspaceTerminals(mountedWorkspace.id);
  }
  mountedRegion = region;
  mountedWorkspace = workspace;
  if (!region || !workspace) {
    if (region) region.innerHTML = "";
    return;
  }
  void loadCommands(workspace.id);
  renderMountedDock();
}

export function detachTerminalDock(region?: HTMLElement | null): void {
  if (region && mountedRegion !== region) return;
  if (mountedWorkspace) unmountWorkspaceTerminals(mountedWorkspace.id);
  mountedRegion = null;
  mountedWorkspace = null;
}

export function registerWorkbenchPanel(workspaceId: string, panel: WorkbenchPanel): () => void {
  let panels = workbenchPanels.get(workspaceId);
  if (!panels) {
    panels = new Map();
    workbenchPanels.set(workspaceId, panels);
  }
  panels.set(panel.id, panel);
  if (mountedWorkspace?.id === workspaceId) renderMountedDock();
  return () => {
    panel.unmount?.();
    const current = workbenchPanels.get(workspaceId);
    current?.delete(panel.id);
    if (!current?.size) workbenchPanels.delete(workspaceId);
    if (activePanels.get(workspaceId) === panel.id) activePanels.set(workspaceId, "terminal");
    if (mountedWorkspace?.id === workspaceId) renderMountedDock();
  };
}

export function openWorkbenchPanel(workspaceId: string, panelId: string, focus = false): void {
  if (panelId !== "terminal" && !workbenchPanels.get(workspaceId)?.has(panelId)) return;
  activePanels.set(workspaceId, panelId);
  openWorkspaces.add(workspaceId);
  persistPreferences();
  renderMountedDock();
  if (focus) requestAnimationFrame(() => mountedRegion?.querySelector<HTMLElement>("[data-workbench-panel-host]")?.focus());
}

function renderMountedDock(): void {
  const region = mountedRegion;
  const workspace = mountedWorkspace;
  if (!region || !workspace) return;
  const workspaceId = workspace.id;
  const open = openWorkspaces.has(workspaceId);
  const maximized = maximizedWorkspaces.has(workspaceId);
  const savedOpen = savedMenus.has(workspaceId);
  const current = meta.get(workspaceId);
  const status = terminalStatus(current);
  const panels = [...(workbenchPanels.get(workspaceId)?.values() || [])];
  let activePanel = activePanels.get(workspaceId) || "terminal";
  if (activePanel !== "terminal" && !panels.some((panel) => panel.id === activePanel)) activePanel = "terminal";
  activePanels.set(workspaceId, activePanel);
  const activeRegistration = panels.find((panel) => panel.id === activePanel);
  unmountWorkspaceTerminals(workspaceId);
  region.innerHTML = `
    <section class="terminal-dock${open ? " is-open" : ""}${maximized ? " is-maximized" : ""}" data-terminal-dock data-workspace-id="${escapeAttribute(workspaceId)}" style="--terminal-dock-height:${terminalHeight(workspaceId)}px" aria-label="Integrated terminal">
      ${open ? `<div class="terminal-resize-handle" role="separator" aria-label="Resize terminal" aria-orientation="horizontal" tabindex="0" data-terminal-resize-handle></div>` : ""}
      <header class="terminal-toolbar">
        <button class="terminal-title-button" type="button" data-terminal-action="toggle" aria-expanded="${open}">
          ${activeRegistration?.icon ? `<span class="codicon codicon-${escapeAttribute(activeRegistration.icon)}"></span>` : icons.terminal}<span class="terminal-title">${escapeHTML(activeRegistration?.label || "Terminal")}</span>
          ${activePanel === "terminal" ? `
          <span class="terminal-session-label">${escapeHTML(current?.shell || "Terminal")}</span>
          <span class="terminal-workspace-label" title="${escapeAttribute(current?.workingDirectory || workspace.mainPath)}">${escapeHTML(workspace.name)}</span>
          <span class="terminal-status-indicator is-${escapeAttribute(status.tone)}" title="${escapeAttribute(status.detail)}"></span>
          <span class="terminal-status-text">${escapeHTML(status.label)}</span>` : ""}
        </button>
        <div class="terminal-panel-tabs" role="tablist" aria-label="Panel views">
          ${[{ id: "terminal", label: "Terminal", icon: "terminal" }, ...panels].map((panel) => `<button type="button" role="tab" aria-selected="${activePanel === panel.id}" class="${activePanel === panel.id ? "is-active" : ""}" data-terminal-action="panel" data-panel-id="${escapeAttribute(panel.id)}"><span class="codicon codicon-${escapeAttribute(panel.icon || "output")}"></span>${escapeHTML(panel.label)}</button>`).join("")}
        </div>
        <div class="terminal-toolbar-actions">
          ${activePanel === "terminal" ? `
          <div class="terminal-saved-menu-wrap">
            <button class="terminal-toolbar-button" type="button" title="Saved commands" aria-label="Saved commands" aria-expanded="${savedOpen}" data-terminal-action="saved">${icons.star}<span>Saved</span></button>
            ${savedOpen ? renderSavedMenu(workspaceId) : ""}
          </div>
          <button class="terminal-toolbar-button icon-only" type="button" title="Restart terminal" aria-label="Restart terminal" data-terminal-action="restart">${icons.refresh}</button>
          <button class="terminal-toolbar-button icon-only danger" type="button" title="Kill terminal" aria-label="Kill terminal" data-terminal-action="stop" ${current?.status === "running" || current?.status === "stopping" ? "" : "disabled"}>${icons.trash}</button>
          ` : ""}
          <button class="terminal-toolbar-button icon-only terminal-maximize-button" type="button" title="${maximized ? "Restore terminal size" : "Maximize terminal"}" aria-label="${maximized ? "Restore terminal size" : "Maximize terminal"}" data-terminal-action="maximize" ${open ? "" : "disabled"}>${maximized ? icons.collapse : icons.expand}</button>
          <button class="terminal-toolbar-button icon-only" type="button" title="${open ? "Close terminal" : "Open terminal"}" aria-label="${open ? "Close terminal" : "Open terminal"}" data-terminal-action="toggle">${open ? icons.x : icons.arrowUp}</button>
        </div>
      </header>
      ${open && activePanel === "terminal" ? `<div class="terminal-viewport" data-terminal-viewport>
        ${current?.status === "error" ? renderProcessMessage(current.message || "Terminal could not start.", "error") : ""}
        ${current?.status === "exited" ? renderProcessMessage(`Process exited with code ${current.exitCode ?? "?"}. Press Enter or restart to launch a new shell.`, "exited") : ""}
      </div>` : open ? `<div class="terminal-custom-panel" data-workbench-panel-host tabindex="-1"></div>` : ""}
    </section>`;
  bindDock(region, workspace);
  if (open && activePanel === "terminal") {
    const viewport = region.querySelector<HTMLElement>("[data-terminal-viewport]");
    if (viewport) {
      const controller = terminalController(workspaceId);
      const alreadyMounted = controller.host.isConnected;
      controller.mount(viewport);
      if (!alreadyMounted) controller.terminal.focus();
      void controller.ensureStarted();
    }
  }
  if (open && activeRegistration) {
    const host = region.querySelector<HTMLElement>("[data-workbench-panel-host]");
    if (host) activeRegistration.mount(host);
  }
}

function bindDock(region: HTMLElement, workspace: TerminalWorkspace): void {
  region.onclick = (event) => {
    const button = (event.target as Element).closest<HTMLElement>("[data-terminal-action]");
    if (!button || button.hasAttribute("disabled")) return;
    const action = button.dataset.terminalAction;
    const workspaceId = workspace.id;
    if (action === "toggle") toggleTerminal(workspaceId);
    else if (action === "maximize") toggleMaximized(workspaceId);
    else if (action === "restart") void restartWorkspaceTerminal(workspaceId);
    else if (action === "stop") void stopWorkspaceTerminal(workspaceId);
    else if (action === "saved") toggleSavedMenu(workspaceId);
    else if (action === "panel") openWorkbenchPanel(workspaceId, button.dataset.panelId || "terminal", true);
    else if (action === "run") runSaved(workspaceId, button.dataset.commandId || "");
    else if (action === "add") openSavedCommandDialog(workspaceId, null);
    else if (action === "edit") openSavedCommandDialog(workspaceId, button.dataset.commandId || "");
    else if (action === "delete") void removeSavedCommand(workspaceId, button.dataset.commandId || "");
  };
  bindResizeHandle(region, workspace.id);
}

function renderSavedMenu(workspaceId: string): string {
  const list = commands.get(workspaceId) || [];
  return `<div class="terminal-saved-popover" role="menu" aria-label="Saved commands">
    <div class="terminal-saved-popover-header"><span>Saved Commands</span><button class="terminal-saved-add" type="button" data-terminal-action="add">${icons.plus} Add</button></div>
    <div class="terminal-saved-popover-list">
      ${list.length ? list.map((command) => `<div class="terminal-saved-row" role="group">
        <button class="terminal-saved-run" type="button" role="menuitem" title="${escapeAttribute(command.command)}" data-terminal-action="run" data-command-id="${escapeAttribute(command.id)}"><span class="terminal-saved-row-name">${escapeHTML(command.name)}</span><code>${escapeHTML(command.command)}</code></button>
        <button class="terminal-saved-row-action" type="button" title="Edit ${escapeAttribute(command.name)}" aria-label="Edit ${escapeAttribute(command.name)}" data-terminal-action="edit" data-command-id="${escapeAttribute(command.id)}">${icons.edit || icons.settings}</button>
        <button class="terminal-saved-row-action danger" type="button" title="Delete ${escapeAttribute(command.name)}" aria-label="Delete ${escapeAttribute(command.name)}" data-terminal-action="delete" data-command-id="${escapeAttribute(command.id)}">${icons.trash}</button>
      </div>`).join("") : `<p class="terminal-saved-empty">No saved commands yet.</p>`}
    </div></div>`;
}

function renderProcessMessage(message: string, tone: "error" | "exited"): string {
  return `<div class="terminal-process-message is-${tone}"><span>${escapeHTML(message)}</span><button type="button" data-terminal-action="restart">${icons.refresh} Restart</button></div>`;
}

function terminalController(workspaceId: string): TerminalController {
  let controller = controllers.get(workspaceId);
  if (!controller) {
    controller = new TerminalController(workspaceId);
    controllers.set(workspaceId, controller);
  }
  void subscribeWorkspace(workspaceId);
  return controller;
}

function unmountWorkspaceTerminals(workspaceId: string): void {
  for (const controller of controllers.values()) if (controller.workspaceId === workspaceId) controller.unmount();
}

function debugTerminalKey(workspaceId: string, sessionId: string): string { return `${workspaceId}:debug:${sessionId}`; }
function debugTerminalPanelID(sessionId: string): string { return `debug-terminal-${sessionId}`; }
function debugTerminalController(workspaceId: string, sessionId: string, name: string): TerminalController {
  const key = debugTerminalKey(workspaceId, sessionId);
  let controller = controllers.get(key);
  if (!controller) {
    controller = new TerminalController(workspaceId, sessionId);
    controllers.set(key, controller);
  }
  if (!debugPanelUnregister.has(key)) {
    const registered = controller;
    debugPanelUnregister.set(key, registerWorkbenchPanel(workspaceId, {
      id: debugTerminalPanelID(sessionId), label: name, icon: "terminal",
      mount(host) { registered.mount(host); void registered.ensureStarted(); },
      unmount() { registered.unmount(); },
    }));
  }
  void subscribeWorkspace(workspaceId);
  return controller;
}

function subscribeWorkspace(workspaceId: string): Promise<void> {
  if (subscribed.has(workspaceId)) return Promise.resolve();
  let wait = subscriptionWaits.get(workspaceId);
  if (!wait) {
    let resolve!: () => void;
    const promise = new Promise<void>((done) => { resolve = done; });
    wait = { promise, resolve };
    subscriptionWaits.set(workspaceId, wait);
  }
  sendSocket({ type: "terminal_subscribe", workspaceId });
  return wait.promise;
}

onSocketState((state) => {
  if (state !== "open") {
    subscribed.clear();
    return;
  }
  for (const workspaceId of new Set([...controllers.values()].map((controller) => controller.workspaceId))) {
    sendSocket({ type: "terminal_subscribe", workspaceId });
  }
});
onSocket("terminal_subscribed", (message: object) => {
  const workspaceId = String((message as { workspaceId?: string }).workspaceId || "");
  if (!workspaceId) return;
  subscribed.add(workspaceId);
  const wait = subscriptionWaits.get(workspaceId);
  wait?.resolve();
  subscriptionWaits.delete(workspaceId);
  for (const controller of controllers.values()) {
    if (controller.workspaceId === workspaceId) void controller.resync();
  }
  void restoreDebugTerminals(workspaceId);
});
onSocket("terminal_event", (message: object) => {
  const event = message as TerminalEvent;
  if (event.kind === "debug" || controllers.has(debugTerminalKey(event.workspaceId, event.sessionId))) {
    const controller = debugTerminalController(event.workspaceId, event.sessionId, event.name || "Debug Terminal");
    controller.applyEvent(event);
    if (event.event === "started") openWorkbenchPanel(event.workspaceId, debugTerminalPanelID(event.sessionId), true);
    return;
  }
  controllers.get(event.workspaceId)?.applyEvent(event);
});

function restoreDebugTerminals(workspaceId: string): Promise<void> {
  const existing = terminalRestorePromises.get(workspaceId);
  if (existing) return existing;
  const restore = listTerminalSessions(workspaceId)
    .then((sessions) => {
      for (const snapshot of sessions) {
        if (snapshot.kind !== "debug") continue;
        debugTerminalController(workspaceId, snapshot.id, snapshot.name || "Debug Terminal").restore(snapshot);
      }
    })
    .catch((error) => toast(`Could not restore debug terminals: ${errorMessage(error)}`))
    .finally(() => terminalRestorePromises.delete(workspaceId));
  terminalRestorePromises.set(workspaceId, restore);
  return restore;
}

function toggleTerminal(workspaceId: string): void {
  if (openWorkspaces.has(workspaceId)) {
    openWorkspaces.delete(workspaceId);
    maximizedWorkspaces.delete(workspaceId);
  } else openWorkspaces.add(workspaceId);
  persistPreferences();
  renderMountedDock();
}
function toggleMaximized(workspaceId: string): void {
  if (maximizedWorkspaces.has(workspaceId)) maximizedWorkspaces.delete(workspaceId);
  else { maximizedWorkspaces.add(workspaceId); openWorkspaces.add(workspaceId); }
  persistPreferences();
  renderMountedDock();
}
function toggleSavedMenu(workspaceId: string): void {
  if (savedMenus.has(workspaceId)) savedMenus.delete(workspaceId);
  else { savedMenus.clear(); savedMenus.add(workspaceId); }
  renderMountedDock();
}
async function restartWorkspaceTerminal(workspaceId: string): Promise<void> {
  openWorkspaces.add(workspaceId);
  persistPreferences();
  renderMountedDock();
  try { await terminalController(workspaceId).restart(); }
  catch (error) { updateMeta(workspaceId, { status: "error", message: errorMessage(error) }); }
}
async function stopWorkspaceTerminal(workspaceId: string): Promise<void> {
  try { await terminalController(workspaceId).stop(); }
  catch (error) { toast(errorMessage(error)); }
}

async function loadCommands(workspaceId: string): Promise<void> {
  if (commandsLoaded.has(workspaceId)) return;
  commandsLoaded.add(workspaceId);
  try {
    commands.set(workspaceId, await listSavedCommands(workspaceId));
    if (mountedWorkspace?.id === workspaceId && savedMenus.has(workspaceId)) renderMountedDock();
  } catch (error) {
    commandsLoaded.delete(workspaceId);
    toast(errorMessage(error));
  }
}
function runSaved(workspaceId: string, commandId: string): void {
  const command = (commands.get(workspaceId) || []).find((candidate) => candidate.id === commandId);
  if (!command) return;
  openWorkspaces.add(workspaceId);
  savedMenus.delete(workspaceId);
  persistPreferences();
  renderMountedDock();
  window.requestAnimationFrame(() => {
    const controller = terminalController(workspaceId);
    void controller.ensureStarted().then(() => {
      if (!controller.sendCommand(command.command)) toast("Terminal is not available.");
    });
  });
}
async function removeSavedCommand(workspaceId: string, commandId: string): Promise<void> {
  if (!commandId) return;
  try {
    await deleteSavedCommand(workspaceId, commandId);
    commands.set(workspaceId, (commands.get(workspaceId) || []).filter((command) => command.id !== commandId));
    toast("Command deleted.");
  } catch (error) { toast(errorMessage(error)); }
  renderMountedDock();
}

function openSavedCommandDialog(workspaceId: string, commandId: string | null): void {
  document.querySelector("[data-saved-command-overlay]")?.remove();
  const existing = commandId
    ? (commands.get(workspaceId) || []).find((command) => command.id === commandId)
    : undefined;
  const overlay = document.createElement("div");
  overlay.className = "saved-command-dialog-overlay";
  overlay.dataset.savedCommandOverlay = "";
  overlay.innerHTML = `<form class="saved-command-dialog" role="dialog" aria-modal="true" aria-label="${existing ? "Edit Command" : "New Command"}">
    <h3>${existing ? "Edit Command" : "New Command"}</h3>
    <input name="name" type="text" placeholder="Name" value="${escapeAttribute(existing?.name || "")}" aria-label="Command name" required>
    <input name="command" class="saved-command-text" type="text" placeholder="Command" value="${escapeAttribute(existing?.command || "")}" aria-label="Command text" required>
    <p class="saved-command-error" data-saved-command-error></p>
    <div class="dialog-actions"><button type="button" class="secondary-button" data-cancel>Cancel</button><button type="submit" class="primary-button">Save</button></div>
  </form>`;
  const form = overlay.querySelector<HTMLFormElement>("form")!;
  const name = form.elements.namedItem("name") as HTMLInputElement;
  const command = form.elements.namedItem("command") as HTMLInputElement;
  const error = overlay.querySelector<HTMLElement>("[data-saved-command-error]")!;
  const close = () => overlay.remove();
  overlay.addEventListener("click", (event) => { if (event.target === overlay) close(); });
  overlay.addEventListener("keydown", (event) => { if (event.key === "Escape") close(); });
  overlay.querySelector("[data-cancel]")?.addEventListener("click", close);
  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const nextName = name.value.trim();
    const nextCommand = command.value.trim();
    if (!nextName || !nextCommand) { error.textContent = "Name and command are required."; return; }
    const submit = form.querySelector<HTMLButtonElement>("[type=submit]")!;
    submit.disabled = true;
    try {
      const saved = existing
        ? await updateSavedCommand(workspaceId, existing.id, nextName, nextCommand)
        : await createSavedCommand(workspaceId, nextName, nextCommand);
      const list = [...(commands.get(workspaceId) || [])];
      const index = list.findIndex((candidate) => candidate.id === saved.id);
      if (index >= 0) list[index] = saved; else list.push(saved);
      list.sort((a, b) => a.order - b.order);
      commands.set(workspaceId, list);
      close();
      toast("Command saved.");
      renderMountedDock();
    } catch (cause) {
      error.textContent = errorMessage(cause);
      submit.disabled = false;
    }
  });
  document.body.appendChild(overlay);
  requestAnimationFrame(() => { name.focus(); name.select(); });
}

function bindResizeHandle(region: HTMLElement, workspaceId: string): void {
  const handle = region.querySelector<HTMLElement>("[data-terminal-resize-handle]");
  if (!handle) return;
  handle.addEventListener("pointerdown", (event) => {
    if (window.matchMedia("(max-width: 720px)").matches) return;
    event.preventDefault();
    handle.setPointerCapture(event.pointerId);
    const startY = event.clientY;
    const startHeight = terminalHeight(workspaceId);
    const move = (next: PointerEvent) => {
      const value = clampTerminalHeight(startHeight + startY - next.clientY);
      heights.set(workspaceId, value);
      region.querySelector<HTMLElement>("[data-terminal-dock]")?.style.setProperty("--terminal-dock-height", `${value}px`);
      controllers.get(workspaceId)?.fit();
    };
    const end = () => {
      handle.removeEventListener("pointermove", move);
      handle.removeEventListener("pointerup", end);
      handle.removeEventListener("pointercancel", end);
      persistPreferences();
    };
    handle.addEventListener("pointermove", move);
    handle.addEventListener("pointerup", end);
    handle.addEventListener("pointercancel", end);
  });
  handle.addEventListener("keydown", (event) => {
    let value = terminalHeight(workspaceId);
    if (event.key === "ArrowUp") value += 20;
    else if (event.key === "ArrowDown") value -= 20;
    else if (event.key === "Home") value = 160;
    else if (event.key === "End") value = Math.floor(window.innerHeight * 0.7);
    else return;
    event.preventDefault();
    heights.set(workspaceId, clampTerminalHeight(value));
    persistPreferences();
    renderMountedDock();
  });
}

function updateMeta(workspaceId: string, patch: Partial<TerminalMeta>, rerender = true): void {
  const current = meta.get(workspaceId) || {
    id: "", shell: "Terminal", workingDirectory: "", status: "idle", lastSequence: 0,
  };
  meta.set(workspaceId, { ...current, ...patch });
  if (rerender && mountedWorkspace?.id === workspaceId) renderMountedDock();
}
function terminalStatus(current?: TerminalMeta): { label: string; detail: string; tone: string } {
  if (!current) return { label: "Ready", detail: "Open to start a shell", tone: "idle" };
  if (current.status === "running") return { label: "Running", detail: "Interactive shell is running", tone: "running" };
  if (current.status === "stopping") return { label: "Stopping", detail: "Stopping terminal process", tone: "busy" };
  if (current.status === "exited") return {
    label: `Exit ${current.exitCode ?? "?"}`,
    detail: current.message || `Process exited with code ${current.exitCode ?? "unknown"}`,
    tone: current.exitCode === 0 ? "idle" : "error",
  };
  if (current.status === "error") return { label: "Unavailable", detail: current.message || "Terminal failed to start", tone: "error" };
  return { label: "Ready", detail: "Open to start a shell", tone: "idle" };
}

function loadPreferences(): void {
  if (preferencesLoaded) return;
  preferencesLoaded = true;
  const stored = parseTerminalPreferences(window.localStorage.getItem(preferenceKey));
  for (const [workspaceId, preference] of Object.entries(stored)) {
    if (preference.open) openWorkspaces.add(workspaceId);
    if (preference.maximized) maximizedWorkspaces.add(workspaceId);
    if (Number.isFinite(preference.height)) heights.set(workspaceId, clampTerminalHeight(Number(preference.height)));
    if (typeof preference.activePanel === "string" && preference.activePanel) activePanels.set(workspaceId, preference.activePanel);
  }
}
function persistPreferences(): void {
  const workspaceIds = new Set([...openWorkspaces, ...maximizedWorkspaces, ...heights.keys(), ...activePanels.keys()]);
  const preferences: Record<string, TerminalPreference> = {};
  for (const workspaceId of workspaceIds) preferences[workspaceId] = {
    open: openWorkspaces.has(workspaceId),
    maximized: maximizedWorkspaces.has(workspaceId),
    height: terminalHeight(workspaceId),
    activePanel: activePanels.get(workspaceId) || "terminal",
  };
  try { window.localStorage.setItem(preferenceKey, JSON.stringify(preferences)); } catch { /* Optional. */ }
}
function terminalHeight(workspaceId: string): number { return heights.get(workspaceId) || defaultHeight; }

function readTerminalTheme(): ITheme {
  const styles = window.getComputedStyle(document.documentElement);
  const value = (name: string, fallback: string) => styles.getPropertyValue(name).trim() || fallback;
  if (!terminalColorScheme.matches) {
    const background = value("--color-bg", "#f7f3f1");
    const foreground = value("--color-text", "#241f1f");
    return {
      background, foreground, cursor: foreground, cursorAccent: background,
      selectionBackground: value("--code-editor-selection", "rgba(9,105,218,.25)"),
      black: foreground, red: value("--color-danger", "#b42332"), green: value("--color-success", "#1a7f37"),
      yellow: value("--color-warning", "#9a6700"), blue: value("--color-accent-strong", "#1d4ed8"),
      magenta: "#7557a8", cyan: "#277a80", white: foreground,
      brightBlack: value("--color-text-muted", "#6f6360"), brightRed: value("--color-danger", "#b42332"),
      brightGreen: value("--color-success", "#1a7f37"), brightYellow: value("--color-warning", "#9a6700"),
      brightBlue: value("--color-accent", "#2563eb"), brightMagenta: "#6f42c1", brightCyan: "#0f766e", brightWhite: foreground,
    };
  }
  return {
    background: value("--code-editor-bg", "#0d1117"), foreground: value("--code-editor-text", "#e6edf3"),
    cursor: value("--code-editor-caret", "#e6edf3"), cursorAccent: value("--code-editor-bg", "#0d1117"),
    selectionBackground: value("--code-editor-selection", "#264f78"), black: "#000000",
    red: value("--color-danger", "#ff6677"), green: value("--color-success", "#3fb950"),
    yellow: value("--color-warning", "#d29922"), blue: value("--color-info", "#58a6ff"),
    magenta: "#bc8cff", cyan: "#39c5cf", white: value("--code-editor-text", "#e6edf3"),
    brightBlack: value("--code-editor-gutter-text", "#7d8590"), brightRed: "#ff7b72", brightGreen: "#56d364",
    brightYellow: "#e3b341", brightBlue: "#79c0ff", brightMagenta: "#d2a8ff", brightCyan: "#56d4dd", brightWhite: "#ffffff",
  };
}

function updateMobileViewport(): void {
  const viewport = window.visualViewport;
  document.documentElement.style.setProperty("--terminal-mobile-height", `${Math.round(viewport?.height || window.innerHeight)}px`);
  document.documentElement.style.setProperty("--terminal-mobile-top", `${Math.round(viewport?.offsetTop || 0)}px`);
  if (mountedWorkspace) controllers.get(mountedWorkspace.id)?.fit();
}
function escapeHTML(value: unknown): string {
  return String(value ?? "").replace(/[&<>"']/g, (character) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[character] || character));
}
function escapeAttribute(value: unknown): string { return escapeHTML(value); }
function errorMessage(error: unknown): string { return error instanceof Error ? error.message : String(error); }

document.addEventListener("keydown", (event) => {
  if (!mountedWorkspace || document.querySelector("[data-saved-command-overlay]")) return;
  if ((event.ctrlKey || event.metaKey) && !event.altKey && !event.shiftKey && event.code === "Backquote") {
    event.preventDefault();
    event.stopPropagation();
    toggleTerminal(mountedWorkspace.id);
  }
}, true);
document.addEventListener("pointerdown", (event) => {
  if (!mountedWorkspace || !savedMenus.has(mountedWorkspace.id)) return;
  const target = event.target as Node;
  if (mountedRegion?.querySelector(".terminal-saved-menu-wrap")?.contains(target)) return;
  savedMenus.delete(mountedWorkspace.id);
  renderMountedDock();
}, true);
window.addEventListener("resize", updateMobileViewport);
window.visualViewport?.addEventListener("resize", updateMobileViewport);
window.visualViewport?.addEventListener("scroll", updateMobileViewport);
terminalColorScheme.addEventListener("change", () => controllers.forEach((controller) => controller.refreshTheme()));
updateMobileViewport();
