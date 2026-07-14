import {
  ContinueDebugSession,
  EvaluateDebugExpression,
  LoadDebugState,
  LoadDebugScopes,
  LoadDebugStackTrace,
  LoadDebugThreads,
  LoadDebugVariables,
  LoadWorkspaceDebugSettings,
  PauseDebugSession,
  SetWorkspaceSelectedDebugConfiguration,
  SetDebugBreakpoints,
  StartDebugSession,
  StepIntoDebugSession,
  StepOutDebugSession,
  StepOverDebugSession,
  StopDebugSession,
} from "../backend/services";
import { StateEffect, StateField, RangeSetBuilder, type Extension } from "@codemirror/state";
import { Decoration, EditorView, GutterMarker, ViewPlugin, gutter, hoverTooltip, type DecorationSet, type ViewUpdate } from "@codemirror/view";
import { activeCodeTab } from "./state";
import type { CodeViewCallbacks } from "./types";
import type {
  DebugEvent,
  DebugBreakpoint,
  DebugScope,
  DebugStackFrame,
  DebugState,
  DebugThread,
  DebugVariable,
  WorkspaceDebugSettings,
} from "./debugTypes";
import { escapeAttribute, escapeHtml } from "./utils";

type VariablePage = {
  items: DebugVariable[];
  loading: boolean;
  complete: boolean;
  error: string;
};

type DebugPaneName = "call-stack" | "variables" | "output";

type DebugWorkspaceUI = {
  settings: WorkspaceDebugSettings | null;
  settingsLoading: boolean;
  settingsError: string;
  operation: string;
  dockOpen: boolean;
  collapsedPanes: Set<DebugPaneName>;
  threads: DebugThread[];
  frames: DebugStackFrame[];
  scopes: DebugScope[];
  selectedThreadId: number;
  selectedFrameId: number;
  variables: Map<number, VariablePage>;
  expandedVariables: Set<number>;
  inspectionLoading: boolean;
  inspectionError: string;
  callbacks: CodeViewCallbacks | null;
  breakpoints: DebugBreakpoint[];
};

const debugWorkspaceUI = new Map<string, DebugWorkspaceUI>();
const defaultDebugState: DebugState = { revision: 0, status: "idle" };
let runtimeState: DebugState = { ...defaultDebugState };
let runtimeOutput = "";
let inspectionRequestSequence = 0;
let lastHandledStopKey = "";
let debugStateChangeListener: (() => void) | null = null;

const debugIcons = {
  play: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m8 5 11 7-11 7Z"/></svg>`,
  pause: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 5v14M16 5v14"/></svg>`,
  stop: `<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="6" y="6" width="12" height="12" rx="1"/></svg>`,
  stepOver: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 7a7 7 0 0 1 12 5"/><path d="m15 9 3 3 3-3"/><path d="M12 15v6"/><path d="m9 18 3 3 3-3"/></svg>`,
  stepInto: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 7a7 7 0 0 1 12 5"/><path d="m15 9 3 3 3-3"/><path d="M12 13v8"/><path d="m9 18 3 3 3-3"/></svg>`,
  stepOut: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 12a7 7 0 0 1 12-5"/><path d="m15 10 3-3 3 3"/><path d="M12 21v-8"/><path d="m9 16 3-3 3 3"/></svg>`,
  settings: `<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a2 2 0 0 0 .4 2.2l.1.1-2.6 2.6-.1-.1a2 2 0 0 0-2.2-.4 2 2 0 0 0-1.2 1.8V21h-3.6v-.2A2 2 0 0 0 9 19a2 2 0 0 0-2.2.4l-.1.1-2.6-2.6.1-.1a2 2 0 0 0 .4-2.2A2 2 0 0 0 2.8 13H2v-3h.8A2 2 0 0 0 4.6 8.6a2 2 0 0 0-.4-2.2l-.1-.1 2.6-2.6.1.1A2 2 0 0 0 9 4.2 2 2 0 0 0 10.2 2H14v.2A2 2 0 0 0 15 4a2 2 0 0 0 2.2-.4l.1-.1 2.6 2.6-.1.1a2 2 0 0 0-.4 2.2A2 2 0 0 0 21.2 10h.8v3h-.8a2 2 0 0 0-1.8 2Z"/></svg>`,
  chevron: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 18 6-6-6-6"/></svg>`,
};

function ensureDebugWorkspaceUI(workspaceID: string): DebugWorkspaceUI {
  let ui = debugWorkspaceUI.get(workspaceID);
  if (!ui) {
    ui = {
      settings: null,
      settingsLoading: false,
      settingsError: "",
      operation: "",
      dockOpen: false,
      collapsedPanes: new Set(),
      threads: [],
      frames: [],
      scopes: [],
      selectedThreadId: 0,
      selectedFrameId: 0,
      variables: new Map(),
      expandedVariables: new Set(),
      inspectionLoading: false,
      inspectionError: "",
      callbacks: null,
      breakpoints: [],
    };
    debugWorkspaceUI.set(workspaceID, ui);
  }
  return ui;
}

export function getDebugState(): DebugState {
  return runtimeState;
}

export function isWorkspaceDebugActive(workspaceID: string): boolean {
  return isDebugSessionActive(runtimeStateForWorkspace(workspaceID));
}

export function setDebugStateChangeListener(listener: (() => void) | null): void {
  debugStateChangeListener = listener;
}

export function getSelectedDebugFrameID(workspaceID: string): number {
  const ui = ensureDebugWorkspaceUI(workspaceID);
  return ui.selectedFrameId || runtimeState.frameId || 0;
}

export function getWorkspaceDebugSettings(workspaceID: string) {
  return ensureDebugWorkspaceUI(workspaceID).settings;
}

export function applyWorkspaceDebugSettings(settings: WorkspaceDebugSettings) {
  const ui = ensureDebugWorkspaceUI(settings.workspaceId);
  ui.settings = settings;
  ui.settingsError = "";
  patchDebugChrome(settings.workspaceId);
}

export async function loadWorkspaceDebugSettings(
  workspaceID: string,
  options: { force?: boolean; patch?: boolean } = {},
) {
  const ui = ensureDebugWorkspaceUI(workspaceID);
  if (ui.settingsLoading || (ui.settings && !options.force)) {
    return ui.settings;
  }
  ui.settingsLoading = true;
  ui.settingsError = "";
  if (options.patch !== false) {
    patchDebugChrome(workspaceID);
  }
  try {
    ui.settings = await LoadWorkspaceDebugSettings(workspaceID);
    return ui.settings;
  } catch (error) {
    ui.settingsError = debugErrorMessage(error);
    return null;
  } finally {
    ui.settingsLoading = false;
    if (options.patch !== false) {
      patchDebugChrome(workspaceID);
    }
  }
}

export async function initializeCodeDebugger(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  const ui = ensureDebugWorkspaceUI(workspaceID);
  ui.callbacks = callbacks;
  const settingsPromise = loadWorkspaceDebugSettings(workspaceID);
  try {
    const loaded = await LoadDebugState(workspaceID);
    ui.breakpoints = loaded.breakpoints ?? ui.breakpoints;
    // A workspace-scoped idle response must not erase the app-wide session
    // owned by another workspace. Shift+F5 remains global across every view.
    if (
      !isDebugSessionActive(runtimeState) ||
      !runtimeState.workspaceId ||
      loaded.workspaceId === runtimeState.workspaceId ||
      isDebugSessionActive(loaded)
    ) {
      acceptDebugState(loaded);
    }
  } catch (error) {
    ui.settingsError ||= debugErrorMessage(error);
  }
  await settingsPromise;
  patchDebugChrome(workspaceID);
  refreshDebugEditorDecorations();
  if (runtimeState.status === "paused" && runtimeState.workspaceId === workspaceID) {
    void refreshDebugInspection(workspaceID);
  }
}

export function renderDebugToolbar(workspaceID: string): string {
  return `<div class="debug-toolbar" role="toolbar" aria-label="Debug controls" data-debug-toolbar data-debug-workspace-id="${escapeAttribute(workspaceID)}">${renderDebugToolbarContents(workspaceID)}</div>`;
}

function renderDebugToolbarContents(workspaceID: string): string {
  const ui = ensureDebugWorkspaceUI(workspaceID);
  const state = runtimeStateForWorkspace(workspaceID);
  const status = state.status;
  const active = isDebugSessionActive(state);
  const paused = status === "paused";
  const running = status === "running";
  const starting = status === "starting";
  const stopping = status === "stopping";
  const configurations = ui.settings?.configurations ?? [];
  const selected = ui.settings?.selectedConfiguration ?? "";
  const actionLabel = paused ? "Continue" : starting ? "Starting" : "Start debugging";
  const configurationOptions = configurations.length
    ? configurations.map((configuration) => `<option value="${escapeAttribute(configuration.name)}" ${configuration.name === selected ? "selected" : ""}>${escapeHtml(configuration.name)}${ui.settings?.implicit ? " (auto)" : ""}</option>`).join("")
    : `<option value="">${ui.settingsLoading ? "Loading configurations…" : "No debug configurations"}</option>`;
  return `
    <label class="debug-configuration-field">
      <span class="sr-only">Debug configuration</span>
      <select data-debug-configuration ${ui.settingsLoading || !configurations.length || active ? "disabled" : ""}>
        ${configurationOptions}
      </select>
    </label>
    <button class="icon-button debug-control is-primary" type="button" title="${actionLabel} (F5)" aria-label="${actionLabel}" data-debug-action="start" ${running || starting || stopping || (!configurations.length && !paused) ? "disabled" : ""}>${debugIcons.play}</button>
    <button class="icon-button debug-control" type="button" title="Pause" aria-label="Pause debugging" data-debug-action="pause" ${running && !ui.operation ? "" : "disabled"}>${debugIcons.pause}</button>
    <span class="debug-control-separator" aria-hidden="true"></span>
    <button class="icon-button debug-control" type="button" title="Step over (F10)" aria-label="Step over" data-debug-action="step-over" ${paused && !ui.operation ? "" : "disabled"}>${debugIcons.stepOver}</button>
    <button class="icon-button debug-control" type="button" title="Step into (F11)" aria-label="Step into" data-debug-action="step-into" ${paused && !ui.operation ? "" : "disabled"}>${debugIcons.stepInto}</button>
    <button class="icon-button debug-control" type="button" title="Step out (Shift+F11)" aria-label="Step out" data-debug-action="step-out" ${paused && !ui.operation ? "" : "disabled"}>${debugIcons.stepOut}</button>
    <button class="icon-button debug-control is-stop" type="button" title="Stop (Shift+F5)" aria-label="Stop debugging" data-debug-action="stop" ${active && !stopping ? "" : "disabled"}>${debugIcons.stop}</button>
    <button class="icon-button debug-control" type="button" title="Configure debugging" aria-label="Configure debugging" data-debug-action="configure">${debugIcons.settings}</button>
    <span class="debug-status-badge is-${escapeAttribute(status)}" data-debug-status>${escapeHtml(debugStatusLabel(state, ui.operation))}</span>
  `;
}

export function renderDebugDock(workspaceID: string): string {
  return `<aside class="debug-dock ${ensureDebugWorkspaceUI(workspaceID).dockOpen ? "is-open" : ""}" aria-label="Debugger" data-debug-dock data-debug-workspace-id="${escapeAttribute(workspaceID)}">${renderDebugDockContents(workspaceID)}</aside>`;
}

function renderDebugDockContents(workspaceID: string): string {
  const ui = ensureDebugWorkspaceUI(workspaceID);
  const state = runtimeStateForWorkspace(workspaceID);
  const hasSession = isDebugSessionActive(state) || state.status === "terminated" || state.status === "error";
  return `
    <button class="debug-dock-heading" type="button" data-debug-action="toggle-dock" aria-expanded="${ui.dockOpen}">
      <span class="debug-dock-chevron">${debugIcons.chevron}</span>
      <strong>Debug</strong>
      <span>${escapeHtml(debugDockSummary(state))}</span>
    </button>
    ${ui.dockOpen ? `
      <div class="debug-dock-body" style="${debugPaneGridStyle(ui)}">
        ${hasSession ? renderDebugInspection(workspaceID) : `<div class="debug-empty">Set a breakpoint in the gutter, then press F5 to start.</div>`}
      </div>
    ` : ""}
  `;
}

function renderDebugInspection(workspaceID: string): string {
  const ui = ensureDebugWorkspaceUI(workspaceID);
  const state = runtimeStateForWorkspace(workspaceID);
  const callStackCollapsed = ui.collapsedPanes.has("call-stack");
  const variablesCollapsed = ui.collapsedPanes.has("variables");
  const outputCollapsed = ui.collapsedPanes.has("output");
  return `
    <section class="debug-pane debug-call-stack-pane ${callStackCollapsed ? "is-collapsed" : ""}" aria-label="Call stack">
      <header>
        ${renderDebugPaneHeading("call-stack", "Call Stack", callStackCollapsed)}
        ${!callStackCollapsed && ui.threads.length > 1 ? `
          <select class="debug-thread-select" aria-label="Debug thread" data-debug-thread>
            ${ui.threads.map((thread) => `<option value="${thread.id}" ${thread.id === ui.selectedThreadId ? "selected" : ""}>${escapeHtml(thread.name)}</option>`).join("")}
          </select>
        ` : ""}
      </header>
      ${callStackCollapsed ? "" : state.status === "paused"
        ? ui.inspectionLoading
          ? `<div class="debug-empty"><span class="spinner" aria-hidden="true"></span> Loading stack…</div>`
          : ui.inspectionError
            ? `<div class="debug-empty is-error">${escapeHtml(ui.inspectionError)}</div>`
            : ui.frames.length
              ? `<div class="debug-stack-list" role="listbox" aria-label="Stack frames">${ui.frames.map((frame) => renderDebugStackFrame(frame, ui.selectedFrameId)).join("")}</div>`
              : `<div class="debug-empty">No stack frames were returned.</div>`
        : `<div class="debug-empty">${escapeHtml(debugStatusLabel(state, ui.operation))}</div>`}
    </section>
    <section class="debug-pane debug-variables-pane ${variablesCollapsed ? "is-collapsed" : ""}" aria-label="Variables">
      <header>${renderDebugPaneHeading("variables", "Variables", variablesCollapsed)}</header>
      ${variablesCollapsed ? "" : state.status !== "paused"
        ? `<div class="debug-empty">Variables are available while paused.</div>`
        : ui.scopes.length
          ? `<div class="debug-scopes">${ui.scopes.map((scope) => renderDebugScope(ui, scope)).join("")}</div>`
          : `<div class="debug-empty">${ui.inspectionLoading ? "Loading variables…" : "No variables are available for this frame."}</div>`}
    </section>
    <section class="debug-pane debug-output-pane ${outputCollapsed ? "is-collapsed" : ""}" aria-label="Debug output">
      <header>${renderDebugPaneHeading("output", "Output", outputCollapsed)}</header>
      ${outputCollapsed ? "" : `<pre data-debug-output>${escapeHtml(runtimeOutput || state.output || "Debug output will appear here.")}</pre>`}
    </section>
  `;
}

function renderDebugPaneHeading(pane: DebugPaneName, label: string, collapsed: boolean) {
  return `<button class="debug-pane-heading" type="button" data-debug-action="toggle-pane" data-debug-pane="${pane}" aria-expanded="${!collapsed}" title="${collapsed ? `Expand ${label}` : `Collapse ${label}`}">
    <span class="debug-pane-chevron">${debugIcons.chevron}</span>
    <strong>${label}</strong>
  </button>`;
}

function debugPaneGridStyle(ui: DebugWorkspaceUI) {
  return ([
    ["call-stack", "--debug-call-stack-width", "minmax(160px, 0.8fr)"],
    ["variables", "--debug-variables-width", "minmax(220px, 1.2fr)"],
    ["output", "--debug-output-width", "minmax(220px, 1fr)"],
  ] as const).map(([pane, property, width]) => `${property}: ${ui.collapsedPanes.has(pane) ? "38px" : width}`).join("; ");
}

export function bindDebugViewEvents(
  root: ParentNode,
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  ensureDebugWorkspaceUI(workspaceID).callbacks = callbacks;
  bindDebugChromeEvents(root, workspaceID);
  void initializeCodeDebugger(workspaceID, callbacks);
}

function bindDebugChromeEvents(root: ParentNode, workspaceID: string) {
  root.querySelectorAll<HTMLElement>("[data-debug-action]").forEach((element) => {
    element.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      void runDebugAction(workspaceID, element.dataset.debugAction ?? "", element);
    });
  });
  root.querySelector<HTMLSelectElement>("[data-debug-configuration]")?.addEventListener("change", (event) => {
    void selectDebugConfiguration(workspaceID, (event.currentTarget as HTMLSelectElement).value);
  });
  root.querySelector<HTMLSelectElement>("[data-debug-thread]")?.addEventListener("change", (event) => {
    void selectDebugThread(workspaceID, Number((event.currentTarget as HTMLSelectElement).value));
  });
}

function renderDebugStackFrame(frame: DebugStackFrame, selectedFrameID: number) {
  const location = frame.location ?? {};
  const path = location.name || location.path || "Unknown source";
  const line = location.line ? `:${location.line}` : "";
  return `
    <button class="debug-stack-frame ${frame.id === selectedFrameID ? "is-selected" : ""}" type="button" role="option" aria-selected="${frame.id === selectedFrameID}" data-debug-action="select-frame" data-debug-frame-id="${frame.id}">
      <strong>${escapeHtml(frame.name || "Frame")}</strong>
      <span title="${escapeAttribute(location.path || path)}">${escapeHtml(path)}${line}</span>
    </button>
  `;
}

function renderDebugScope(ui: DebugWorkspaceUI, scope: DebugScope) {
  const reference = scope.variablesReference;
  const page = ui.variables.get(reference);
  return `
    <details class="debug-scope" open>
      <summary>${escapeHtml(scope.name)}${scope.expensive ? ` <span>lazy</span>` : ""}</summary>
      <div class="debug-variable-list">
        ${page?.error ? `<div class="debug-empty is-error">${escapeHtml(page.error)}</div>` : ""}
        ${page?.items.length
          ? page.items.map((variable) => renderDebugVariable(ui, variable, 0, new Set([reference]))).join("")
          : page?.loading
            ? `<div class="debug-empty"><span class="spinner" aria-hidden="true"></span> Loading…</div>`
            : `<div class="debug-empty">No values</div>`}
        ${page && !page.complete && !page.loading ? renderLoadMoreVariables(reference) : ""}
      </div>
    </details>
  `;
}

function renderDebugVariable(
  ui: DebugWorkspaceUI,
  variable: DebugVariable,
  depth: number,
  ancestors: Set<number>,
): string {
  const reference = variable.variablesReference || 0;
  const expandable = reference > 0 && depth < 12 && !ancestors.has(reference);
  const expanded = expandable && ui.expandedVariables.has(reference);
  const page = ui.variables.get(reference);
  const nextAncestors = new Set(ancestors);
  if (reference) {
    nextAncestors.add(reference);
  }
  return `
    <div class="debug-variable" style="--debug-variable-depth: ${depth}">
      <button class="debug-variable-row" type="button" ${expandable ? `data-debug-action="toggle-variable" data-debug-variables-reference="${reference}" aria-expanded="${expanded}"` : "disabled"}>
        <span class="debug-variable-chevron">${expandable ? debugIcons.chevron : ""}</span>
        <span class="debug-variable-name">${escapeHtml(variable.name)}</span>
        <span class="debug-variable-value" title="${escapeAttribute(variable.value)}">${escapeHtml(variable.value)}</span>
        ${variable.type ? `<span class="debug-variable-type">${escapeHtml(variable.type)}</span>` : ""}
      </button>
      ${expanded ? `
        <div class="debug-variable-children">
          ${page?.error ? `<div class="debug-empty is-error">${escapeHtml(page.error)}</div>` : ""}
          ${page?.items.length ? page.items.map((child) => renderDebugVariable(ui, child, depth + 1, nextAncestors)).join("") : page?.loading ? `<div class="debug-empty"><span class="spinner" aria-hidden="true"></span> Loading…</div>` : `<div class="debug-empty">No children</div>`}
          ${page && !page.complete && !page.loading ? renderLoadMoreVariables(reference) : ""}
        </div>
      ` : ""}
    </div>
  `;
}

function renderLoadMoreVariables(reference: number) {
  return `<button class="debug-load-more" type="button" data-debug-action="load-more-variables" data-debug-variables-reference="${reference}">Load 100 more</button>`;
}

async function runDebugAction(workspaceID: string, action: string, element?: HTMLElement) {
  const ui = ensureDebugWorkspaceUI(workspaceID);
  const callbacks = ui.callbacks;
  if (!callbacks) {
    return;
  }
  if (action === "toggle-dock") {
    ui.dockOpen = !ui.dockOpen;
    patchDebugChrome(workspaceID);
    return;
  }
  if (action === "toggle-pane") {
    const pane = element?.dataset.debugPane as DebugPaneName | undefined;
    if (!pane || !(["call-stack", "variables", "output"] as string[]).includes(pane)) {
      return;
    }
    if (ui.collapsedPanes.has(pane)) {
      ui.collapsedPanes.delete(pane);
    } else {
      ui.collapsedPanes.add(pane);
    }
    patchDebugChrome(workspaceID);
    return;
  }
  if (action === "configure") {
    callbacks.openDebugSettings(workspaceID);
    return;
  }
  if (action === "select-frame") {
    const frameID = Number(element?.dataset.debugFrameId ?? 0);
    if (frameID) {
      await selectDebugFrame(workspaceID, frameID);
    }
    return;
  }
  if (action === "toggle-variable") {
    const reference = Number(element?.dataset.debugVariablesReference ?? 0);
    if (reference) {
      await toggleDebugVariable(workspaceID, reference);
    }
    return;
  }
  if (action === "load-more-variables") {
    const reference = Number(element?.dataset.debugVariablesReference ?? 0);
    if (reference) {
      await loadDebugVariablePage(workspaceID, reference, true);
    }
    return;
  }
  if (action === "start") {
    await startOrContinueDebug(workspaceID, callbacks);
    return;
  }
  if (action === "stop") {
    await stopActiveDebug(callbacks);
    return;
  }
  if (action === "pause") {
    await runSessionControl(workspaceID, "pause", PauseDebugSession);
    return;
  }
  if (action === "step-over") {
    await runSessionControl(workspaceID, "step over", StepOverDebugSession);
    return;
  }
  if (action === "step-into") {
    await runSessionControl(workspaceID, "step into", StepIntoDebugSession);
    return;
  }
  if (action === "step-out") {
    await runSessionControl(workspaceID, "step out", StepOutDebugSession);
  }
}

async function selectDebugConfiguration(workspaceID: string, name: string) {
  const ui = ensureDebugWorkspaceUI(workspaceID);
  try {
    ui.settings = await SetWorkspaceSelectedDebugConfiguration(workspaceID, name);
  } catch (error) {
    ui.callbacks?.pushToast(debugErrorMessage(error), "error");
  } finally {
    patchDebugChrome(workspaceID);
  }
}

export async function startOrContinueDebug(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  // There is only one session in Echo. Always control that session before
  // considering a launch in the currently selected workspace.
  if (isDebugSessionActive(runtimeState) && runtimeState.workspaceId) {
    if (runtimeState.status === "paused" && runtimeState.sessionId) {
      ensureDebugWorkspaceUI(runtimeState.workspaceId).callbacks ||= callbacks;
      await runSessionControl(runtimeState.workspaceId, "continue", ContinueDebugSession);
    }
    return;
  }
  const state = runtimeStateForWorkspace(workspaceID);
  if (state.status === "running" || state.status === "starting" || state.status === "stopping") {
    return;
  }
  if (state.status === "paused" && state.sessionId) {
    await runSessionControl(workspaceID, "continue", ContinueDebugSession);
    return;
  }
  const ui = ensureDebugWorkspaceUI(workspaceID);
  ui.callbacks = callbacks;
  if (!(await callbacks.saveDirtyWorkspaceFiles(workspaceID))) {
    callbacks.pushToast("Debugging did not start because a workspace file could not be saved.", "error");
    return;
  }
  ui.operation = "starting";
  ui.dockOpen = true;
  clearDebugInspection(ui);
  patchDebugChrome(workspaceID);
  try {
    const tab = activeCodeTab(workspaceID);
    acceptDebugState(await StartDebugSession(workspaceID, {
      configurationName: ui.settings?.selectedConfiguration ?? "",
      currentFile: tab && !tab.untitled && !tab.external ? tab.path : "",
    }));
  } catch (error) {
    callbacks.pushToast(debugErrorMessage(error), "error");
  } finally {
    ui.operation = "";
    patchAllDebugChrome();
    refreshDebugEditorDecorations();
  }
}

type SessionControl = (
  workspaceID: string,
  request: { sessionId: string },
) => Promise<DebugState>;

async function runSessionControl(
  workspaceID: string,
  label: string,
  control: SessionControl,
) {
  const ui = ensureDebugWorkspaceUI(workspaceID);
  const state = runtimeStateForWorkspace(workspaceID);
  if (!state.sessionId || ui.operation) {
    return;
  }
  ui.operation = label;
  if (label !== "pause") {
    clearDebugInspection(ui);
  }
  patchDebugChrome(workspaceID);
  try {
    acceptDebugState(await control(workspaceID, { sessionId: state.sessionId }));
  } catch (error) {
    ui.callbacks?.pushToast(debugErrorMessage(error), "error");
  } finally {
    ui.operation = "";
    patchAllDebugChrome();
    refreshDebugEditorDecorations();
  }
}

export async function stopActiveDebug(callbacks?: CodeViewCallbacks) {
  if (!runtimeState.sessionId || !runtimeState.workspaceId || runtimeState.status === "stopping") {
    return;
  }
  const workspaceID = runtimeState.workspaceId;
  const ui = ensureDebugWorkspaceUI(workspaceID);
  if (callbacks) {
    ui.callbacks ||= callbacks;
  }
  await runSessionControl(workspaceID, "stopping", StopDebugSession);
}

export function handleGlobalDebugShortcut(
  event: KeyboardEvent,
  workspaceID: string,
  callbacks: CodeViewCallbacks,
): boolean {
  if (event.altKey || event.ctrlKey || event.metaKey) {
    return false;
  }
  if (event.key === "F5") {
    event.preventDefault();
    event.stopPropagation();
    if (event.shiftKey) {
      void stopActiveDebug(callbacks);
    } else if (workspaceID) {
      void startOrContinueDebug(workspaceID, callbacks);
    }
    return true;
  }
  if (!event.shiftKey && event.key === "F9") {
    const mounted = mountedDebugEditor;
    if (!mounted || (workspaceID && mounted.workspaceID !== workspaceID)) {
      return false;
    }
    event.preventDefault();
    event.stopPropagation();
    const line = mounted.view.state.doc.lineAt(mounted.view.state.selection.main.head).number;
    void toggleEditorBreakpoint(mounted.workspaceID, mounted.path, mounted.view, line);
    return true;
  }
  if (!event.shiftKey && event.key === "F10") {
    event.preventDefault();
    event.stopPropagation();
    if (runtimeState.workspaceId && runtimeState.status === "paused") {
      void runSessionControl(runtimeState.workspaceId, "step over", StepOverDebugSession);
    }
    return true;
  }
  if (event.key === "F11") {
    event.preventDefault();
    event.stopPropagation();
    if (runtimeState.workspaceId && runtimeState.status === "paused") {
      void runSessionControl(
        runtimeState.workspaceId,
        event.shiftKey ? "step out" : "step into",
        event.shiftKey ? StepOutDebugSession : StepIntoDebugSession,
      );
    }
    return true;
  }
  return false;
}

export function applyDebugEvent(event: DebugEvent) {
  if (!event) {
    return;
  }
  const eventRevision = Number(event.revision || 0);
  if (eventRevision > 0 && eventRevision < Number(runtimeState.revision || 0)) {
    return;
  }
  if (
    event.sessionId &&
    runtimeState.sessionId &&
    event.sessionId !== runtimeState.sessionId &&
    isDebugSessionActive(runtimeState) &&
    eventRevision <= Number(runtimeState.revision || 0)
  ) {
    return;
  }
  if (event.state) {
    acceptDebugState(event.state);
  } else {
    runtimeState = {
      ...runtimeState,
      workspaceId: event.workspaceId || runtimeState.workspaceId,
      sessionId: event.sessionId || runtimeState.sessionId,
      revision: Math.max(runtimeState.revision, Number(event.revision || 0)),
    };
  }
  if (event.output) {
    appendDebugOutput(event.output);
  }
  if (event.type === "output" && event.output && !event.state) {
    patchDebugOutput(event.workspaceId || runtimeState.workspaceId || "");
    return;
  }
  if (event.message && event.type === "error") {
    const workspaceID = event.workspaceId || runtimeState.workspaceId || "";
    ensureDebugWorkspaceUI(workspaceID).callbacks?.pushToast(event.message, "error");
  }
  patchAllDebugChrome();
  refreshDebugEditorDecorations();
  const workspaceID = runtimeState.workspaceId ?? "";
  if (workspaceID && runtimeState.status === "paused") {
    void handleDebugStopped(workspaceID);
  } else if (workspaceID) {
    lastHandledStopKey = "";
    clearDebugInspection(ensureDebugWorkspaceUI(workspaceID));
  }
}

function acceptDebugState(next: DebugState) {
  if (!next) {
    return;
  }
  const previousWorkspaceID = runtimeState.workspaceId ?? "";
  const wasActive = isDebugSessionActive(runtimeState);
  const nextState = {
    ...defaultDebugState,
    ...next,
    revision: Number(next.revision || 0),
    status: next.status || "idle",
    breakpoints: next.breakpoints ?? [],
  };
  runtimeState = nextState;
  if (nextState.workspaceId) {
    const ui = ensureDebugWorkspaceUI(nextState.workspaceId);
    ui.breakpoints = nextState.breakpoints ?? [];
    if (isDebugSessionActive(nextState) && (!wasActive || previousWorkspaceID !== nextState.workspaceId)) {
      ui.dockOpen = true;
    }
  }
  if (typeof next.output === "string") {
    runtimeOutput = next.output;
  }
  debugStateChangeListener?.();
}

function appendDebugOutput(output: string) {
  runtimeOutput = (runtimeOutput + output).slice(-1024 * 1024);
}

function patchDebugOutput(workspaceID: string) {
  if (!workspaceID) return;
  const dock = findDebugElement("[data-debug-dock]", workspaceID);
  const output = dock?.querySelector<HTMLElement>("[data-debug-output]");
  if (!output) return;
  output.textContent = runtimeOutput || runtimeState.output || "Debug output will appear here.";
  output.scrollTop = output.scrollHeight;
}

function runtimeStateForWorkspace(workspaceID: string): DebugState {
  if (!runtimeState.workspaceId || runtimeState.workspaceId === workspaceID) {
    return runtimeState;
  }
  return defaultDebugState;
}

function isDebugSessionActive(state: DebugState) {
  return state.status === "starting" || state.status === "running" || state.status === "paused" || state.status === "stopping";
}

function debugStatusLabel(state: DebugState, operation: string) {
  if (operation) {
    return operation.charAt(0).toUpperCase() + operation.slice(1) + "…";
  }
  if (state.status === "error") {
    return state.error || "Debug error";
  }
  if (state.status === "terminated") {
    return "Terminated";
  }
  if (state.status === "idle") {
    return "Ready";
  }
  return state.status.charAt(0).toUpperCase() + state.status.slice(1);
}

function debugDockSummary(state: DebugState) {
  if (state.status === "paused" && state.currentLocation) {
    const location = state.currentLocation;
    return `${location.name || location.path || "Paused"}${location.line ? `:${location.line}` : ""}`;
  }
  return state.configuration || debugStatusLabel(state, "");
}

function patchAllDebugChrome() {
  for (const workspaceID of debugWorkspaceUI.keys()) {
    patchDebugChrome(workspaceID);
  }
}

export function patchDebugChrome(workspaceID: string) {
  const codeView = findCodeViewElement(workspaceID);
  codeView?.classList.toggle("is-debug-running", isWorkspaceDebugActive(workspaceID));
  const toolbar = findDebugElement("[data-debug-toolbar]", workspaceID);
  if (toolbar) {
    toolbar.innerHTML = renderDebugToolbarContents(workspaceID);
    bindDebugChromeEvents(toolbar, workspaceID);
  }
  const dock = findDebugElement("[data-debug-dock]", workspaceID);
  if (dock) {
    dock.classList.toggle("is-open", ensureDebugWorkspaceUI(workspaceID).dockOpen);
    dock.innerHTML = renderDebugDockContents(workspaceID);
    bindDebugChromeEvents(dock, workspaceID);
    const output = dock.querySelector<HTMLElement>("[data-debug-output]");
    if (output) {
      output.scrollTop = output.scrollHeight;
    }
  }
}

function findDebugElement(selector: string, workspaceID: string) {
  return Array.from(document.querySelectorAll<HTMLElement>(selector)).find(
    (element) => element.dataset.debugWorkspaceId === workspaceID,
  ) ?? null;
}

function findCodeViewElement(workspaceID: string) {
  return Array.from(document.querySelectorAll<HTMLElement>("[data-code-view]")).find(
    (element) => element.dataset.codeViewWorkspaceId === workspaceID,
  ) ?? null;
}

function clearDebugInspection(ui: DebugWorkspaceUI) {
  ui.threads = [];
  ui.frames = [];
  ui.scopes = [];
  ui.selectedThreadId = 0;
  ui.selectedFrameId = 0;
  ui.variables.clear();
  ui.expandedVariables.clear();
  ui.inspectionLoading = false;
  ui.inspectionError = "";
}

async function refreshDebugInspection(workspaceID: string) {
  const state = runtimeStateForWorkspace(workspaceID);
  const ui = ensureDebugWorkspaceUI(workspaceID);
  if (state.status !== "paused" || !state.sessionId) {
    return;
  }
  const requestSequence = ++inspectionRequestSequence;
  const sessionID = state.sessionId;
  ui.inspectionLoading = true;
  ui.inspectionError = "";
  ui.threads = [];
  ui.frames = [];
  ui.scopes = [];
  ui.variables.clear();
  ui.expandedVariables.clear();
  patchDebugChrome(workspaceID);
  try {
    const threadResponse = await LoadDebugThreads(workspaceID, { sessionId: sessionID });
    if (!debugInspectionIsCurrent(workspaceID, sessionID, requestSequence)) {
      return;
    }
    ui.threads = threadResponse.threads ?? [];
    const preferredThread = state.threadId || ui.selectedThreadId;
    ui.selectedThreadId = ui.threads.some((thread) => thread.id === preferredThread)
      ? preferredThread
      : ui.threads[0]?.id ?? 0;
    if (!ui.selectedThreadId) {
      return;
    }
    await loadDebugStackForThread(workspaceID, ui.selectedThreadId, sessionID, requestSequence);
  } catch (error) {
    if (debugInspectionIsCurrent(workspaceID, sessionID, requestSequence)) {
      ui.inspectionError = debugErrorMessage(error);
    }
  } finally {
    if (debugInspectionIsCurrent(workspaceID, sessionID, requestSequence)) {
      ui.inspectionLoading = false;
      patchDebugChrome(workspaceID);
    }
  }
}

async function loadDebugStackForThread(
  workspaceID: string,
  threadID: number,
  sessionID: string,
  requestSequence: number,
) {
  const ui = ensureDebugWorkspaceUI(workspaceID);
  const response = await LoadDebugStackTrace(workspaceID, {
    sessionId: sessionID,
    threadId: threadID,
    startFrame: 0,
    levels: 50,
  });
  if (!debugInspectionIsCurrent(workspaceID, sessionID, requestSequence)) {
    return;
  }
  ui.frames = response.stackFrames ?? [];
  const preferredFrame = runtimeState.frameId || ui.selectedFrameId;
  ui.selectedFrameId = ui.frames.some((frame) => frame.id === preferredFrame)
    ? preferredFrame
    : ui.frames[0]?.id ?? 0;
  if (ui.selectedFrameId) {
    await loadDebugFrameScopes(workspaceID, ui.selectedFrameId, sessionID, requestSequence);
  }
}

async function loadDebugFrameScopes(
  workspaceID: string,
  frameID: number,
  sessionID: string,
  requestSequence: number,
) {
  const ui = ensureDebugWorkspaceUI(workspaceID);
  const response = await LoadDebugScopes(workspaceID, {
    sessionId: sessionID,
    frameId: frameID,
  });
  if (!debugInspectionIsCurrent(workspaceID, sessionID, requestSequence)) {
    return;
  }
  ui.scopes = response.scopes ?? [];
  ui.variables.clear();
  ui.expandedVariables.clear();
  await Promise.all(
    ui.scopes
      .filter((scope) => scope.variablesReference > 0)
      .map((scope) => loadDebugVariablePage(workspaceID, scope.variablesReference, false, false)),
  );
}

function debugInspectionIsCurrent(
  workspaceID: string,
  sessionID: string,
  requestSequence: number,
) {
  return (
    requestSequence === inspectionRequestSequence &&
    runtimeState.workspaceId === workspaceID &&
    runtimeState.sessionId === sessionID &&
    runtimeState.status === "paused"
  );
}

async function handleDebugStopped(workspaceID: string) {
  const state = runtimeStateForWorkspace(workspaceID);
  const ui = ensureDebugWorkspaceUI(workspaceID);
  const location = state.currentLocation;
  const stopKey = `${state.sessionId}:${state.threadId}:${state.frameId}:${location?.path ?? ""}:${location?.line ?? 0}`;
  if (stopKey === lastHandledStopKey && ui.frames.length) {
    return;
  }
  lastHandledStopKey = stopKey;
  if (location?.path && location.line && ui.callbacks) {
    await ui.callbacks.openWorkspaceFileAtLine(workspaceID, location.path, location.line);
  }
  await refreshDebugInspection(workspaceID);
}

async function selectDebugThread(workspaceID: string, threadID: number) {
  const state = runtimeStateForWorkspace(workspaceID);
  const ui = ensureDebugWorkspaceUI(workspaceID);
  if (state.status !== "paused" || !state.sessionId || !ui.threads.some((thread) => thread.id === threadID)) {
    return;
  }
  const requestSequence = ++inspectionRequestSequence;
  ui.selectedThreadId = threadID;
  ui.selectedFrameId = 0;
  ui.inspectionLoading = true;
  ui.inspectionError = "";
  patchDebugChrome(workspaceID);
  try {
    await loadDebugStackForThread(workspaceID, threadID, state.sessionId, requestSequence);
  } catch (error) {
    if (debugInspectionIsCurrent(workspaceID, state.sessionId, requestSequence)) {
      ui.inspectionError = debugErrorMessage(error);
    }
  } finally {
    if (debugInspectionIsCurrent(workspaceID, state.sessionId, requestSequence)) {
      ui.inspectionLoading = false;
      patchDebugChrome(workspaceID);
    }
  }
}

async function selectDebugFrame(workspaceID: string, frameID: number) {
  const state = runtimeStateForWorkspace(workspaceID);
  const ui = ensureDebugWorkspaceUI(workspaceID);
  const frame = ui.frames.find((candidate) => candidate.id === frameID);
  if (!frame || state.status !== "paused" || !state.sessionId) {
    return;
  }
  const requestSequence = ++inspectionRequestSequence;
  ui.selectedFrameId = frameID;
  ui.inspectionLoading = true;
  ui.inspectionError = "";
  patchDebugChrome(workspaceID);
  if (frame.location?.path && frame.location.line && ui.callbacks) {
    await ui.callbacks.openWorkspaceFileAtLine(workspaceID, frame.location.path, frame.location.line);
  }
  try {
    await loadDebugFrameScopes(workspaceID, frameID, state.sessionId, requestSequence);
  } catch (error) {
    if (debugInspectionIsCurrent(workspaceID, state.sessionId, requestSequence)) {
      ui.inspectionError = debugErrorMessage(error);
    }
  } finally {
    if (debugInspectionIsCurrent(workspaceID, state.sessionId, requestSequence)) {
      ui.inspectionLoading = false;
      patchDebugChrome(workspaceID);
      refreshDebugEditorDecorations();
    }
  }
}

async function toggleDebugVariable(workspaceID: string, reference: number) {
  const ui = ensureDebugWorkspaceUI(workspaceID);
  if (ui.expandedVariables.has(reference)) {
    ui.expandedVariables.delete(reference);
    patchDebugChrome(workspaceID);
    return;
  }
  ui.expandedVariables.add(reference);
  patchDebugChrome(workspaceID);
  if (!ui.variables.has(reference)) {
    await loadDebugVariablePage(workspaceID, reference, false);
  }
}

async function loadDebugVariablePage(
  workspaceID: string,
  reference: number,
  append: boolean,
  patch = true,
) {
  const state = runtimeStateForWorkspace(workspaceID);
  const ui = ensureDebugWorkspaceUI(workspaceID);
  if (state.status !== "paused" || !state.sessionId || reference <= 0) {
    return;
  }
  const existing = ui.variables.get(reference);
  if (existing?.loading) {
    return;
  }
  const page: VariablePage = append && existing
    ? existing
    : { items: [], loading: false, complete: false, error: "" };
  page.loading = true;
  page.error = "";
  ui.variables.set(reference, page);
  if (patch) {
    patchDebugChrome(workspaceID);
  }
  const start = append ? page.items.length : 0;
  const sessionID = state.sessionId;
  try {
    const response = await LoadDebugVariables(workspaceID, {
      sessionId: sessionID,
      variablesReference: reference,
      start,
      count: 100,
    });
    if (runtimeState.sessionId !== sessionID || runtimeState.status !== "paused") {
      return;
    }
    const variables = response.variables ?? [];
    page.items = append ? [...page.items, ...variables] : variables;
    page.complete = variables.length < 100;
  } catch (error) {
    page.error = debugErrorMessage(error);
    page.complete = true;
  } finally {
    page.loading = false;
    if (patch) {
      patchDebugChrome(workspaceID);
    }
  }
}

function refreshDebugEditorDecorations() {
  updateMountedDebugEditor();
}

function debugErrorMessage(error: unknown) {
  if (error instanceof Error) {
    return error.message;
  }
  return String(error || "Unknown debugger error");
}

type EditorBreakpointMarker = {
  position: number;
  verified: boolean;
  message: string;
};

type DebugEditorSnapshot = {
  breakpoints: EditorBreakpointMarker[];
  executionPosition: number;
};

const setDebugEditorSnapshot = StateEffect.define<DebugEditorSnapshot>();

const debugEditorState = StateField.define<DebugEditorSnapshot>({
  create: () => ({ breakpoints: [], executionPosition: -1 }),
  update(value, transaction) {
    let next: DebugEditorSnapshot = {
      executionPosition: value.executionPosition < 0
        ? -1
        : transaction.changes.mapPos(value.executionPosition),
      breakpoints: value.breakpoints.map((breakpoint) => ({
        ...breakpoint,
        position: transaction.changes.mapPos(breakpoint.position),
      })),
    };
    for (const effect of transaction.effects) {
      if (effect.is(setDebugEditorSnapshot)) {
        next = effect.value;
      }
    }
    return next;
  },
});

class DebugBreakpointGutterMarker extends GutterMarker {
  constructor(
    readonly verified: boolean,
    readonly message: string,
  ) {
    super();
  }

  eq(other: DebugBreakpointGutterMarker) {
    return other.verified === this.verified && other.message === this.message;
  }

  toDOM() {
    const marker = document.createElement("span");
    marker.className = `cm-debug-breakpoint ${this.verified ? "is-verified" : "is-pending"}`;
    marker.title = this.message || (this.verified ? "Verified breakpoint" : "Pending breakpoint");
    marker.setAttribute("aria-label", marker.title);
    return marker;
  }
}

class DebugBreakpointSpacerMarker extends GutterMarker {
  toDOM() {
    const marker = document.createElement("span");
    marker.className = "cm-debug-breakpoint-spacer";
    return marker;
  }
}

const debugBreakpointSpacerMarker = new DebugBreakpointSpacerMarker();

const debugExecutionLine = Decoration.line({ class: "cm-debug-execution-line" });
let mountedDebugEditor: { view: EditorView; workspaceID: string; path: string } | null = null;
let lastExecutionReveal = "";

export function debugEditorExtension(workspaceID: string, path: string): Extension {
  const breakpointGutter = gutter({
    class: "cm-debug-gutter",
    initialSpacer: () => debugBreakpointSpacerMarker,
    renderEmptyElements: true,
    markers(view) {
      const builder = new RangeSetBuilder<GutterMarker>();
      for (const breakpoint of view.state.field(debugEditorState).breakpoints) {
        builder.add(
          breakpoint.position,
          breakpoint.position,
          new DebugBreakpointGutterMarker(breakpoint.verified, breakpoint.message),
        );
      }
      return builder.finish();
    },
    domEventHandlers: {
      mousedown(view, line, event) {
        if ((event as MouseEvent).button !== 0) {
          return false;
        }
        event.preventDefault();
        void toggleEditorBreakpoint(workspaceID, path, view, view.state.doc.lineAt(line.from).number);
        return true;
      },
    },
  });
  const executionField = StateField.define<DecorationSet>({
    create: () => Decoration.none,
    update(_value, transaction) {
      const snapshot = transaction.state.field(debugEditorState);
      if (snapshot.executionPosition < 0 || snapshot.executionPosition > transaction.state.doc.length) {
        return Decoration.none;
      }
      return Decoration.set([debugExecutionLine.range(snapshot.executionPosition)]);
    },
    provide: (field) => EditorView.decorations.from(field),
  });
  const mount = ViewPlugin.fromClass(class {
    private breakpointTimer = 0;

    constructor(readonly view: EditorView) {
      mountedDebugEditor = { view, workspaceID, path };
      window.setTimeout(updateMountedDebugEditor);
    }

    update(update: ViewUpdate) {
      mountedDebugEditor = { view: update.view, workspaceID, path };
      if (update.docChanged) {
        window.clearTimeout(this.breakpointTimer);
        this.breakpointTimer = window.setTimeout(() => {
          void synchronizeMappedBreakpoints(workspaceID, path, update.view);
        }, 250);
      }
    }

    destroy() {
      window.clearTimeout(this.breakpointTimer);
      releaseDebugEditor(this.view);
    }
  });
  const hover = hoverTooltip(async (view, position) => {
    const state = runtimeStateForWorkspace(workspaceID);
    const ui = ensureDebugWorkspaceUI(workspaceID);
    if (state.status !== "paused" || !state.sessionId || !ui.selectedFrameId) {
      return null;
    }
    const expression = debugExpressionAt(view, position);
    if (!expression) {
      return null;
    }
    const sessionID = state.sessionId;
    const revision = state.revision;
    try {
      const evaluated = await EvaluateDebugExpression(workspaceID, {
        sessionId: sessionID,
        expression: expression.value,
        frameId: ui.selectedFrameId,
        context: "hover",
      });
      if (
        runtimeState.sessionId !== sessionID ||
        runtimeState.revision !== revision ||
        runtimeState.status !== "paused"
      ) {
        return null;
      }
      return {
        pos: expression.from,
        end: expression.to,
        above: true,
        create() {
          const dom = document.createElement("div");
          dom.className = "debug-hover-tooltip";
          const value = document.createElement("span");
          value.textContent = evaluated.result;
          dom.append(value);
          if (evaluated.type) {
            const type = document.createElement("small");
            type.textContent = evaluated.type;
            dom.append(type);
          }
          return { dom };
        },
      };
    } catch {
      return null;
    }
  }, { hoverTime: 300 });
  return [debugEditorState, executionField, breakpointGutter, mount, hover];
}

export function releaseDebugEditor(view: EditorView) {
  if (mountedDebugEditor?.view === view) {
    mountedDebugEditor = null;
  }
}

function editorSnapshot(workspaceID: string, path: string, view: EditorView): DebugEditorSnapshot {
  const ui = ensureDebugWorkspaceUI(workspaceID);
  const breakpoints = ui.breakpoints
    .filter((breakpoint) => sameDebugPath(breakpoint.path, path) && breakpoint.line > 0 && breakpoint.line <= view.state.doc.lines)
    .map((breakpoint) => ({
      position: view.state.doc.line(breakpoint.line).from,
      verified: breakpoint.verified,
      message: breakpoint.message ?? "",
    }))
    .sort((left, right) => left.position - right.position);
  const location = runtimeStateForWorkspace(workspaceID).currentLocation;
  const executionPosition =
    runtimeState.status === "paused" &&
    location?.path &&
    sameDebugPath(location.path, path) &&
    location.line &&
    location.line <= view.state.doc.lines
      ? view.state.doc.line(location.line).from
      : -1;
  return { breakpoints, executionPosition };
}

function updateMountedDebugEditor() {
  const mounted = mountedDebugEditor;
  if (!mounted) {
    return;
  }
  const snapshot = editorSnapshot(mounted.workspaceID, mounted.path, mounted.view);
  const revealKey = `${runtimeState.sessionId ?? ""}:${runtimeState.revision}:${mounted.path}:${snapshot.executionPosition}`;
  const effects: StateEffect<unknown>[] = [setDebugEditorSnapshot.of(snapshot)];
  if (snapshot.executionPosition >= 0 && revealKey !== lastExecutionReveal) {
    effects.push(EditorView.scrollIntoView(snapshot.executionPosition, { y: "center" }));
    lastExecutionReveal = revealKey;
  }
  mounted.view.dispatch({ effects });
}

async function toggleEditorBreakpoint(workspaceID: string, path: string, view: EditorView, line: number) {
  const existing = view.state.field(debugEditorState).breakpoints;
  const requested = existing
    .map((breakpoint) => view.state.doc.lineAt(breakpoint.position).number)
    .filter((candidate) => candidate !== line);
  if (!existing.some((breakpoint) => view.state.doc.lineAt(breakpoint.position).number === line)) {
    requested.push(line);
  }
  await setEditorBreakpoints(workspaceID, path, requested);
}

async function synchronizeMappedBreakpoints(workspaceID: string, path: string, view: EditorView) {
  const lines = Array.from(new Set(view.state.field(debugEditorState).breakpoints
    .map((breakpoint) => view.state.doc.lineAt(breakpoint.position).number)))
    .sort((left, right) => left - right);
  const persisted = ensureDebugWorkspaceUI(workspaceID).breakpoints
    .filter((breakpoint) => sameDebugPath(breakpoint.path, path))
    .map((breakpoint) => breakpoint.line)
    .sort((left, right) => left - right);
  if (lines.join(",") !== persisted.join(",")) {
    await setEditorBreakpoints(workspaceID, path, lines);
  }
}

async function setEditorBreakpoints(workspaceID: string, path: string, lines: number[]) {
  const ui = ensureDebugWorkspaceUI(workspaceID);
  try {
    const state = await SetDebugBreakpoints(workspaceID, {
      sessionId: runtimeState.workspaceId === workspaceID ? runtimeState.sessionId : undefined,
      sourcePath: path,
      breakpoints: lines.sort((left, right) => left - right).map((line) => ({ line })),
    });
    ui.breakpoints = state.breakpoints ?? [];
    if (state.sessionId) {
      acceptDebugState(state);
    }
  } catch (error) {
    ui.callbacks?.pushToast(debugErrorMessage(error), "error");
  } finally {
    updateMountedDebugEditor();
    patchDebugChrome(workspaceID);
  }
}

function debugExpressionAt(view: EditorView, position: number) {
  const line = view.state.doc.lineAt(position);
  const offset = position - line.from;
  const left = line.text.slice(0, offset).match(/[A-Za-z_][A-Za-z0-9_.]*$/)?.[0] ?? "";
  const right = line.text.slice(offset).match(/^[A-Za-z0-9_.]*/)?.[0] ?? "";
  const value = left + right;
  if (!/^[A-Za-z_][A-Za-z0-9_]*(?:\.[A-Za-z_][A-Za-z0-9_]*)*$/.test(value)) {
    return null;
  }
  return { value, from: position - left.length, to: position + right.length };
}

function sameDebugPath(left: string, right: string) {
  const normalize = (value: string) => value.replace(/\\/g, "/").replace(/^\.\//, "").toLowerCase();
  const a = normalize(left);
  const b = normalize(right);
  return a === b || a.endsWith(`/${b}`) || b.endsWith(`/${a}`);
}
