import { Terminal, type ITheme } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import "@xterm/xterm/css/xterm.css";

import {
  ResizeTerminalSession,
  RestartTerminalSession,
  StartTerminalSession,
  StopTerminalSession,
  SyncTerminalSession,
  WriteTerminalSession,
} from "../../backend/services";
import { services } from "../../../wailsjs/go/models";
import { getAppCallbacks } from "../callbacks";
import { appRoot } from "../dom";
import { icons } from "../icons";
import { activeWorkspace, state } from "../state";
import type { TerminalEvent } from "../types";
import { escapeAttribute, escapeHtml } from "../utils";

const terminalPreferencesKey = "echo.terminalDock.v1";
const defaultTerminalHeight = 280;
const minimumTerminalHeight = 160;
const terminalInputChunkSize = 48 * 1024;
const terminalTextEncoder = new TextEncoder();

type TerminalSessionMeta = {
  id: string;
  shell: string;
  workingDirectory: string;
  status: string;
  exitCode?: number;
  message?: string;
  lastSequence: number;
};

type TerminalPreference = {
  open?: boolean;
  maximized?: boolean;
  height?: number;
};

const terminalMeta = new Map<string, TerminalSessionMeta>();
const terminalControllers = new Map<string, TerminalController>();

class TerminalController {
  readonly workspaceID: string;
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
  private sessionID = "";
  private lastSequence = 0;

  constructor(workspaceID: string) {
    this.workspaceID = workspaceID;
    this.host = document.createElement("div");
    this.host.className = "terminal-xterm-instance";
    this.host.dataset.terminalXtermWorkspace = workspaceID;
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
      const meta = terminalMeta.get(this.workspaceID);
      if (meta?.status === "exited") {
        if (data.includes("\r") || data.includes("\n")) {
          void restartTerminal(this.workspaceID);
        }
        return;
      }
      this.queueInput(data);
    });
    this.terminal.onResize(({ cols, rows }) => {
      this.queueResize(cols, rows);
    });
  }

  mount(viewport: HTMLElement) {
    if (!this.host.isConnected || this.host.parentElement !== viewport) {
      viewport.appendChild(this.host);
    }
    if (!this.terminal.element) {
      this.terminal.open(this.host);
    }
    this.viewport = viewport;
    this.resizeObserver?.disconnect();
    this.resizeObserver = new ResizeObserver(() => this.fit());
    this.resizeObserver.observe(viewport);
    this.refreshTheme();
    this.fit();
  }

  async ensureStarted() {
    if (this.sessionID) {
      return;
    }
    if (this.startPromise) {
      return this.startPromise;
    }
    this.startPromise = this.start().finally(() => {
      this.startPromise = null;
    });
    return this.startPromise;
  }

  async resync() {
    if (!this.sessionID) {
      return this.ensureStarted();
    }
    if (this.syncPromise) {
      return this.syncPromise;
    }
    this.syncPromise = SyncTerminalSession(
      this.workspaceID,
      this.sessionID,
      this.lastSequence,
    ).then((snapshot) => {
      this.applySnapshot(snapshot);
    }).catch(async () => {
      const snapshot = await StartTerminalSession(
        this.workspaceID,
        this.terminal.cols,
        this.terminal.rows,
      );
      this.applySnapshot(snapshot, true);
    }).finally(() => {
      this.syncPromise = null;
    });
    return this.syncPromise;
  }

  applyEvent(event: TerminalEvent) {
    if (event.type === "started" && event.id !== this.sessionID) {
      if (this.startPromise) {
        return;
      }
      void this.startFromCurrentSession();
      return;
    }
    if (!this.sessionID || event.id !== this.sessionID) {
      return;
    }
    if (event.type === "data") {
      const sequence = Number(event.sequence ?? 0);
      if (sequence <= this.lastSequence) {
        return;
      }
      if (sequence !== this.lastSequence + 1) {
        void this.resync();
        return;
      }
      this.terminal.write(decodeBase64(event.data ?? ""));
      this.lastSequence = sequence;
      updateTerminalMeta(this.workspaceID, {
        lastSequence: sequence,
      }, false);
      return;
    }
    if (event.type === "exited") {
      updateTerminalMeta(this.workspaceID, {
        status: "exited",
        exitCode: event.exitCode,
        message: event.message,
        lastSequence: Number(event.sequence ?? this.lastSequence),
      });
    }
  }

  async stop() {
    if (!this.sessionID) {
      return;
    }
    updateTerminalMeta(this.workspaceID, { status: "stopping" });
    try {
      await StopTerminalSession(this.workspaceID, this.sessionID);
    } catch (error) {
      void this.resync();
      throw error;
    }
  }

  async restart() {
    if (!this.sessionID) {
      return this.ensureStarted();
    }
    const snapshot = await RestartTerminalSession(
      this.workspaceID,
      this.sessionID,
      this.terminal.cols,
      this.terminal.rows,
    );
    this.applySnapshot(snapshot, true);
    this.terminal.focus();
  }

  sendCommand(command: string) {
    if (!this.sessionID) {
      return false;
    }
    this.queueInput(command + "\r");
    this.terminal.focus();
    return true;
  }

  fit() {
    if (!this.viewport || !state.terminalOpen.has(this.workspaceID)) {
      return;
    }
    window.requestAnimationFrame(() => {
      if (!this.viewport || this.viewport.clientWidth <= 0 || this.viewport.clientHeight <= 0) {
        return;
      }
      try {
        this.fitAddon.fit();
      } catch {
        // The dock can be between render fragments for one frame.
      }
    });
  }

  refreshTheme() {
    this.terminal.options.theme = readTerminalTheme();
  }

  dispose() {
    window.cancelAnimationFrame(this.inputFrame);
    window.clearTimeout(this.resizeTimer);
    this.resizeObserver?.disconnect();
    this.terminal.dispose();
    this.host.remove();
  }

  private async start() {
    try {
      const snapshot = await StartTerminalSession(
        this.workspaceID,
        this.terminal.cols,
        this.terminal.rows,
      );
      this.applySnapshot(snapshot, true);
      this.fit();
      this.terminal.focus();
    } catch (error) {
      this.inputBuffer = "";
      updateTerminalMeta(this.workspaceID, {
        id: "",
        shell: "Terminal",
        workingDirectory: "",
        status: "error",
        message: getAppCallbacks().errorMessage(error),
        lastSequence: 0,
      });
    }
  }

  private async startFromCurrentSession() {
    try {
      const snapshot = await StartTerminalSession(
        this.workspaceID,
        this.terminal.cols,
        this.terminal.rows,
      );
      this.applySnapshot(snapshot, snapshot.id !== this.sessionID);
    } catch (error) {
      getAppCallbacks().pushToast(getAppCallbacks().errorMessage(error), "error");
    }
  }

  private applySnapshot(snapshot: services.TerminalSessionSnapshot, forceReset = false) {
    const changedSession = Boolean(this.sessionID) && this.sessionID !== snapshot.id;
    if (forceReset || changedSession || snapshot.reset) {
      this.terminal.reset();
      this.lastSequence = 0;
    }
    this.sessionID = snapshot.id;
    const chunks = [...(snapshot.output ?? [])].sort((a, b) => a.sequence - b.sequence);
    for (const chunk of chunks) {
      if (chunk.sequence <= this.lastSequence) {
        continue;
      }
      this.terminal.write(decodeBase64(chunk.data));
      this.lastSequence = chunk.sequence;
    }
    this.lastSequence = Math.max(this.lastSequence, snapshot.lastSequence ?? 0);
    terminalMeta.set(this.workspaceID, {
      id: snapshot.id,
      shell: snapshot.shell || "Terminal",
      workingDirectory: snapshot.workingDirectory || "",
      status: snapshot.status || "running",
      exitCode: snapshot.exitCode,
      message: snapshot.message,
      lastSequence: this.lastSequence,
    });
    this.flushInput();
    renderTerminalRegion();
  }

  private queueInput(data: string) {
    this.inputBuffer += data;
    if (this.inputFrame) {
      return;
    }
    this.inputFrame = window.requestAnimationFrame(() => {
      this.inputFrame = 0;
      this.flushInput();
    });
  }

  private flushInput() {
    if (!this.inputBuffer || !this.sessionID) {
      return;
    }
    const sessionID = this.sessionID;
    const chunks = splitTerminalInput(this.inputBuffer);
    this.inputBuffer = "";
    for (const chunk of chunks) {
      this.writeChain = this.writeChain
        .then(() => WriteTerminalSession(this.workspaceID, sessionID, chunk))
        .catch((error) => {
          getAppCallbacks().pushToast(getAppCallbacks().errorMessage(error), "error");
          return this.resync();
        });
    }
  }

  private queueResize(cols: number, rows: number) {
    if (!this.sessionID) {
      return;
    }
    window.clearTimeout(this.resizeTimer);
    this.resizeTimer = window.setTimeout(() => {
      void ResizeTerminalSession(this.workspaceID, this.sessionID, cols, rows).catch(() => {
        void this.resync();
      });
    }, 100);
  }
}

export function renderTerminalDock(workspace: services.Workspace | null): string {
  if (!workspace) {
    return "";
  }
  const workspaceID = workspace.id;
  const open = state.terminalOpen.has(workspaceID);
  const maximized = state.terminalMaximized.has(workspaceID);
  const savedOpen = state.terminalSavedMenuOpen.has(workspaceID);
  const height = terminalHeight(workspaceID);
  const meta = terminalMeta.get(workspaceID);
  const status = terminalStatus(meta);
  const title = meta?.shell || "Terminal";
  const folderLabel = terminalWorkspaceLabel(workspace, meta);
  const ws = escapeAttribute(workspaceID);
  return `
    <section
      class="terminal-dock${open ? " is-open" : ""}${maximized ? " is-maximized" : ""}"
      data-terminal-dock
      data-workspace-id="${ws}"
      style="--terminal-dock-height: ${height}px"
      aria-label="Integrated terminal"
    >
      ${open ? `<div class="terminal-resize-handle" role="separator" aria-label="Resize terminal" aria-orientation="horizontal" tabindex="0" data-terminal-resize-handle></div>` : ""}
      <header class="terminal-toolbar">
        <button class="terminal-title-button" type="button" data-action="toggle-terminal" data-workspace-id="${ws}" aria-expanded="${open}">
          ${icons.terminal}
          <span class="terminal-title">Terminal</span>
          <span class="terminal-session-label">${escapeHtml(title)}</span>
          ${folderLabel ? `<span class="terminal-workspace-label" title="${escapeAttribute(meta?.workingDirectory ?? "")}">${escapeHtml(folderLabel)}</span>` : ""}
          <span class="terminal-status-indicator is-${escapeAttribute(status.tone)}" title="${escapeAttribute(status.detail)}"></span>
          <span class="terminal-status-text">${escapeHtml(status.label)}</span>
        </button>
        <div class="terminal-toolbar-actions">
          <div class="terminal-saved-menu-wrap">
            <button class="terminal-toolbar-button" type="button" title="Saved commands" aria-label="Saved commands" aria-expanded="${savedOpen}" data-action="toggle-saved-commands" data-workspace-id="${ws}">
              ${icons.star}
              <span>Saved</span>
            </button>
            ${savedOpen ? renderTerminalSavedMenu(workspace) : ""}
          </div>
          <button class="terminal-toolbar-button icon-only" type="button" title="Restart terminal" aria-label="Restart terminal" data-action="restart-terminal" data-workspace-id="${ws}">
            ${icons.refresh}
          </button>
          <button class="terminal-toolbar-button icon-only danger" type="button" title="Kill terminal" aria-label="Kill terminal" data-action="stop-terminal" data-workspace-id="${ws}" ${meta?.status === "running" || meta?.status === "stopping" ? "" : "disabled"}>
            ${icons.trash}
          </button>
          <button class="terminal-toolbar-button icon-only terminal-maximize-button" type="button" title="${maximized ? "Restore terminal size" : "Maximize terminal"}" aria-label="${maximized ? "Restore terminal size" : "Maximize terminal"}" data-action="maximize-terminal" data-workspace-id="${ws}" ${open ? "" : "disabled"}>
            ${maximized ? icons.collapse : icons.expand}
          </button>
          <button class="terminal-toolbar-button icon-only" type="button" title="${open ? "Close terminal" : "Open terminal"}" aria-label="${open ? "Close terminal" : "Open terminal"}" data-action="toggle-terminal" data-workspace-id="${ws}">
            ${open ? icons.x : icons.arrowUp}
          </button>
        </div>
      </header>
      ${open ? `
        <div class="terminal-viewport" data-terminal-viewport>
          ${meta?.status === "error" ? renderTerminalMessage(meta.message || "Terminal could not start.", "error", workspaceID) : ""}
          ${meta?.status === "exited" ? renderTerminalMessage(`Process exited with code ${meta.exitCode ?? "?"}. Press Enter or restart to launch a new shell.`, "exited", workspaceID) : ""}
        </div>
      ` : ""}
    </section>
  `;
}

export function mountTerminalDock(region: HTMLElement, workspace: services.Workspace | null) {
  if (!workspace) {
    return;
  }
  const workspaceID = workspace.id;
  bindTerminalResizeHandle(region, workspaceID);
  if (!state.terminalOpen.has(workspaceID)) {
    return;
  }
  const viewport = region.querySelector<HTMLElement>("[data-terminal-viewport]");
  if (!viewport) {
    return;
  }
  const controller = terminalController(workspaceID);
  const wasAlreadyMounted = controller.host.isConnected;
  controller.mount(viewport);
  if (!wasAlreadyMounted) {
    controller.terminal.focus();
  }
  void controller.ensureStarted();
}

export function applyTerminalEvent(event: TerminalEvent) {
  const controller = terminalControllers.get(event.workspaceId);
  if (controller) {
    controller.applyEvent(event);
    return;
  }
  const current = terminalMeta.get(event.workspaceId);
  if (event.type === "started") {
    if (!current || current.id !== event.id) {
      terminalMeta.set(event.workspaceId, {
        id: event.id,
        shell: current?.shell || "Terminal",
        workingDirectory: current?.workingDirectory || "",
        status: "running",
        lastSequence: 0,
      });
      renderTerminalRegion();
    }
  } else if (event.type === "exited" && current?.id === event.id) {
    updateTerminalMeta(event.workspaceId, {
      status: "exited",
      exitCode: event.exitCode,
      message: event.message,
      lastSequence: Number(event.sequence ?? current.lastSequence),
    });
  }
}

export function loadTerminalPreferences(workspaces: services.Workspace[]) {
  let preferences: Record<string, TerminalPreference> = {};
  try {
    preferences = JSON.parse(window.localStorage.getItem(terminalPreferencesKey) || "{}");
  } catch {
    preferences = {};
  }
  const valid = new Set(workspaces.map((workspace) => workspace.id));
  for (const [workspaceID, preference] of Object.entries(preferences)) {
    if (!valid.has(workspaceID)) {
      continue;
    }
    if (preference.open) {
      state.terminalOpen.add(workspaceID);
    }
    if (preference.maximized) {
      state.terminalMaximized.add(workspaceID);
    }
    if (Number.isFinite(preference.height)) {
      state.terminalHeights.set(workspaceID, clampTerminalHeight(Number(preference.height)));
    }
  }
}

export function toggleTerminal(workspaceID: string) {
  if (state.terminalOpen.has(workspaceID)) {
    state.terminalOpen.delete(workspaceID);
    state.terminalMaximized.delete(workspaceID);
  } else {
    state.terminalOpen.add(workspaceID);
  }
  persistTerminalPreferences();
  getAppCallbacks().render();
}

export function toggleTerminalMaximized(workspaceID: string) {
  if (state.terminalMaximized.has(workspaceID)) {
    state.terminalMaximized.delete(workspaceID);
  } else {
    state.terminalMaximized.add(workspaceID);
    state.terminalOpen.add(workspaceID);
  }
  persistTerminalPreferences();
  getAppCallbacks().render();
}

export function toggleTerminalSavedCommands(workspaceID: string) {
  if (state.terminalSavedMenuOpen.has(workspaceID)) {
    state.terminalSavedMenuOpen.delete(workspaceID);
  } else {
    state.terminalSavedMenuOpen.clear();
    state.terminalSavedMenuOpen.add(workspaceID);
  }
  getAppCallbacks().render();
}

export async function stopTerminal(workspaceID: string) {
  try {
    await terminalController(workspaceID).stop();
  } catch (error) {
    getAppCallbacks().pushToast(getAppCallbacks().errorMessage(error), "error");
  }
}

export async function restartTerminal(workspaceID: string) {
  state.terminalOpen.add(workspaceID);
  persistTerminalPreferences();
  getAppCallbacks().render();
  try {
    await terminalController(workspaceID).restart();
  } catch (error) {
    updateTerminalMeta(workspaceID, {
      status: "error",
      message: getAppCallbacks().errorMessage(error),
    });
  }
}

export function runSavedTerminalCommand(workspaceID: string, command: string) {
  state.terminalOpen.add(workspaceID);
  state.terminalSavedMenuOpen.delete(workspaceID);
  persistTerminalPreferences();
  getAppCallbacks().render();
  window.requestAnimationFrame(() => {
    const controller = terminalController(workspaceID);
    void controller.ensureStarted().then(() => {
      if (!controller.sendCommand(command)) {
        getAppCallbacks().pushToast("Terminal is not available.", "error");
      }
    });
  });
}

export function syncActiveTerminal() {
  const workspace = activeWorkspace();
  if (!workspace) {
    return Promise.resolve();
  }
  const controller = terminalControllers.get(workspace.id);
  if (!controller) {
    return Promise.resolve();
  }
  return controller.resync();
}

export function disposeWorkspaceTerminal(workspaceID: string) {
  terminalControllers.get(workspaceID)?.dispose();
  terminalControllers.delete(workspaceID);
  terminalMeta.delete(workspaceID);
  state.terminalOpen.delete(workspaceID);
  state.terminalMaximized.delete(workspaceID);
  state.terminalHeights.delete(workspaceID);
  state.terminalSavedMenuOpen.delete(workspaceID);
  persistTerminalPreferences();
}

export function refreshTerminalThemes() {
  terminalControllers.forEach((controller) => controller.refreshTheme());
}

function terminalController(workspaceID: string): TerminalController {
  let controller = terminalControllers.get(workspaceID);
  if (!controller) {
    controller = new TerminalController(workspaceID);
    terminalControllers.set(workspaceID, controller);
  }
  return controller;
}

function renderTerminalSavedMenu(workspace: services.Workspace): string {
  const workspaceID = escapeAttribute(workspace.id);
  const commands = state.savedCommands.get(workspace.id) ?? [];
  return `
    <div class="terminal-saved-popover" role="menu" aria-label="Saved commands">
      <div class="terminal-saved-popover-header">
        <span>Saved Commands</span>
        <button class="terminal-saved-add" type="button" data-action="add-saved-command" data-workspace-id="${workspaceID}">
          ${icons.plus} Add
        </button>
      </div>
      <div class="terminal-saved-popover-list">
        ${commands.length > 0
          ? commands.map((command) => `
              <div class="terminal-saved-row" role="group">
                <button class="terminal-saved-run" type="button" role="menuitem" title="${escapeAttribute(command.command)}" data-action="run-saved-command" data-workspace-id="${workspaceID}" data-saved-id="${escapeAttribute(command.id)}">
                  <span class="terminal-saved-row-name">${escapeHtml(command.name)}</span>
                  <code>${escapeHtml(command.command)}</code>
                </button>
                <button class="terminal-saved-row-action" type="button" title="Edit ${escapeAttribute(command.name)}" aria-label="Edit ${escapeAttribute(command.name)}" data-action="edit-saved-command" data-workspace-id="${workspaceID}" data-saved-id="${escapeAttribute(command.id)}">${icons.edit}</button>
                <button class="terminal-saved-row-action danger" type="button" title="Delete ${escapeAttribute(command.name)}" aria-label="Delete ${escapeAttribute(command.name)}" data-action="delete-saved-command" data-workspace-id="${workspaceID}" data-saved-id="${escapeAttribute(command.id)}">${icons.trash}</button>
              </div>
            `).join("")
          : `<p class="terminal-saved-empty">No saved commands yet.</p>`}
      </div>
    </div>
  `;
}

function renderTerminalMessage(message: string, tone: "error" | "exited", workspaceID: string): string {
  return `
    <div class="terminal-process-message is-${tone}" data-terminal-process-message>
      <span>${escapeHtml(message)}</span>
      <button type="button" data-action="restart-terminal" data-workspace-id="${escapeAttribute(workspaceID)}">${icons.refresh} Restart</button>
    </div>
  `;
}

function bindTerminalResizeHandle(region: HTMLElement, workspaceID: string) {
  const handle = region.querySelector<HTMLElement>("[data-terminal-resize-handle]");
  if (!handle) {
    return;
  }
  handle.addEventListener("pointerdown", (event) => {
    if (window.matchMedia("(max-width: 720px)").matches) {
      return;
    }
    event.preventDefault();
    handle.setPointerCapture(event.pointerId);
    const startY = event.clientY;
    const startHeight = terminalHeight(workspaceID);
    const onMove = (moveEvent: PointerEvent) => {
      const next = clampTerminalHeight(startHeight + startY - moveEvent.clientY);
      state.terminalHeights.set(workspaceID, next);
      const dock = region.querySelector<HTMLElement>("[data-terminal-dock]");
      dock?.style.setProperty("--terminal-dock-height", `${next}px`);
      terminalControllers.get(workspaceID)?.fit();
    };
    const onEnd = () => {
      handle.removeEventListener("pointermove", onMove);
      handle.removeEventListener("pointerup", onEnd);
      handle.removeEventListener("pointercancel", onEnd);
      persistTerminalPreferences();
    };
    handle.addEventListener("pointermove", onMove);
    handle.addEventListener("pointerup", onEnd);
    handle.addEventListener("pointercancel", onEnd);
  });
  handle.addEventListener("keydown", (event) => {
    let height = terminalHeight(workspaceID);
    if (event.key === "ArrowUp") {
      height += 20;
    } else if (event.key === "ArrowDown") {
      height -= 20;
    } else if (event.key === "Home") {
      height = minimumTerminalHeight;
    } else if (event.key === "End") {
      height = maximumTerminalHeight();
    } else {
      return;
    }
    event.preventDefault();
    state.terminalHeights.set(workspaceID, clampTerminalHeight(height));
    persistTerminalPreferences();
    getAppCallbacks().render();
  });
}

function updateTerminalMeta(workspaceID: string, patch: Partial<TerminalSessionMeta>, rerender = true) {
  const current = terminalMeta.get(workspaceID) ?? {
    id: "",
    shell: "Terminal",
    workingDirectory: "",
    status: "idle",
    lastSequence: 0,
  };
  terminalMeta.set(workspaceID, { ...current, ...patch });
  if (rerender) {
    renderTerminalRegion();
  }
}

function renderTerminalRegion() {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  getAppCallbacks().render();
}

function terminalStatus(meta: TerminalSessionMeta | undefined): { label: string; detail: string; tone: string } {
  if (!meta) {
    return { label: "Ready", detail: "Open to start a shell", tone: "idle" };
  }
  if (meta.status === "running") {
    return { label: "Running", detail: "Interactive shell is running", tone: "running" };
  }
  if (meta.status === "stopping") {
    return { label: "Stopping", detail: "Stopping terminal process", tone: "busy" };
  }
  if (meta.status === "exited") {
    return {
      label: `Exit ${meta.exitCode ?? "?"}`,
      detail: meta.message || `Process exited with code ${meta.exitCode ?? "unknown"}`,
      tone: meta.exitCode === 0 ? "idle" : "error",
    };
  }
  if (meta.status === "error") {
    return { label: "Unavailable", detail: meta.message || "Terminal failed to start", tone: "error" };
  }
  return { label: "Ready", detail: "Open to start a shell", tone: "idle" };
}

function terminalWorkspaceLabel(workspace: services.Workspace, meta: TerminalSessionMeta | undefined): string {
  if (workspace.displayName) {
    return workspace.displayName;
  }
  if (meta?.workingDirectory) {
    return meta.workingDirectory.split(/[\\/]/).filter(Boolean).pop() ?? "";
  }
  return "";
}

function terminalHeight(workspaceID: string): number {
  return state.terminalHeights.get(workspaceID) ?? defaultTerminalHeight;
}

function maximumTerminalHeight(): number {
  return Math.max(minimumTerminalHeight, Math.floor(window.innerHeight * 0.7));
}

function clampTerminalHeight(value: number): number {
  return Math.min(maximumTerminalHeight(), Math.max(minimumTerminalHeight, Math.round(value)));
}

function persistTerminalPreferences() {
  const workspaceIDs = new Set<string>([
    ...state.terminalOpen,
    ...state.terminalMaximized,
    ...state.terminalHeights.keys(),
  ]);
  const preferences: Record<string, TerminalPreference> = {};
  workspaceIDs.forEach((workspaceID) => {
    preferences[workspaceID] = {
      open: state.terminalOpen.has(workspaceID),
      maximized: state.terminalMaximized.has(workspaceID),
      height: terminalHeight(workspaceID),
    };
  });
  try {
    window.localStorage.setItem(terminalPreferencesKey, JSON.stringify(preferences));
  } catch {
    // Client-local dock preferences are optional.
  }
}

function decodeBase64(value: string): Uint8Array {
  if (!value) {
    return new Uint8Array();
  }
  const binary = window.atob(value);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) {
    bytes[i] = binary.charCodeAt(i);
  }
  return bytes;
}

function splitTerminalInput(value: string): string[] {
  const chunks: string[] = [];
  let chunk = "";
  let bytes = 0;
  for (const character of value) {
    const characterBytes = terminalTextEncoder.encode(character).byteLength;
    if (chunk && bytes+characterBytes > terminalInputChunkSize) {
      chunks.push(chunk);
      chunk = "";
      bytes = 0;
    }
    chunk += character;
    bytes += characterBytes;
  }
  if (chunk) {
    chunks.push(chunk);
  }
  return chunks;
}

function readTerminalTheme(): ITheme {
  const styles = window.getComputedStyle(document.documentElement);
  const value = (name: string, fallback: string) => styles.getPropertyValue(name).trim() || fallback;
  return {
    background: value("--code-editor-bg", "#0d1117"),
    foreground: value("--code-editor-text", "#e6edf3"),
    cursor: value("--code-editor-caret", "#e6edf3"),
    cursorAccent: value("--code-editor-bg", "#0d1117"),
    selectionBackground: value("--code-editor-selection", "#264f78"),
    black: "#000000",
    red: value("--color-danger", "#ff6677"),
    green: value("--color-success", "#3fb950"),
    yellow: value("--color-warning", "#d29922"),
    blue: value("--color-info", "#58a6ff"),
    magenta: "#bc8cff",
    cyan: "#39c5cf",
    white: value("--code-editor-text", "#e6edf3"),
    brightBlack: value("--code-editor-gutter-text", "#7d8590"),
    brightRed: "#ff7b72",
    brightGreen: "#56d364",
    brightYellow: "#e3b341",
    brightBlue: "#79c0ff",
    brightMagenta: "#d2a8ff",
    brightCyan: "#56d4dd",
    brightWhite: "#ffffff",
  };
}

function updateMobileTerminalViewport() {
  const viewport = window.visualViewport;
  document.documentElement.style.setProperty(
    "--terminal-mobile-height",
    `${Math.round(viewport?.height ?? window.innerHeight)}px`,
  );
  document.documentElement.style.setProperty(
    "--terminal-mobile-top",
    `${Math.round(viewport?.offsetTop ?? 0)}px`,
  );
  const workspace = activeWorkspace();
  if (workspace) {
    terminalControllers.get(workspace.id)?.fit();
  }
}

updateMobileTerminalViewport();
window.addEventListener("resize", updateMobileTerminalViewport);
window.visualViewport?.addEventListener("resize", updateMobileTerminalViewport);
window.visualViewport?.addEventListener("scroll", updateMobileTerminalViewport);

window.matchMedia("(prefers-color-scheme: dark)").addEventListener("change", refreshTerminalThemes);
