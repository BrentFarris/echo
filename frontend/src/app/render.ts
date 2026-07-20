import { destroyCodeEditor, renderCodeQuickOpen, renderCodeView } from "../codeView";
import {
  patchChatPanel,
  patchChatControls,
  renderChatPanel,
  clearChatMention,
  linkifyAssistantFilePaths,
} from "./chat";
import { loadTokenBudget, renderBudgetBar } from "./budget";
import { renderChangeReviewDrawer } from "./changes";
import { renderContextMenu } from "./contextMenu";
import { appRoot, focusInitialElement } from "./dom";
import { bindEvents } from "./events";
import { renderGitRepositoryPage } from "./git";
import { icons } from "./icons";
import { kanbanBoardFor, gitRepositoryViewFor, activeWorkspace, kanbanCards, state, getActiveChatKanbanTab, changeReviewFor, leadingWhitespaceIndicatorsEnabled } from "./state";
import { renderSettingsOverlay } from "./settings";
import { renderToasts } from "./toasts";
import { renderTaskPanel, renderTaskDetail } from "./tasks";
import { escapeHtml, escapeAttribute, workspaceFolderSummary } from "./utils";
import { renderWorkspaceIcon, renderMissingWorkspace } from "./workspace";
import { hasKanbanRuntime, getHeartbeatInterval, heartbeatIntervalLabel, getWatchdogInterval, watchdogIntervalLabel, renderCreateKanbanCardDialog, renderDecompositionState, renderEmptyBoard, renderKanbanBoard, renderKanbanDetail, renderKanbanRuntime } from "./kanban";
import { services } from "../../wailsjs/go/models";
import { renderDashboard } from "./dashboard";
import { updateWindowTitle } from "./title";

/* ------------------------------------------------------------------ */
/*  Workspace activity helpers                                         */
/* ------------------------------------------------------------------ */

function getWorkspaceActivityStatus(workspaceID: string): {
  isChatBusy: boolean;
  isKanbanRunning: boolean;
  activeAgentCount: number;
  lastMessageSnippet: string;
} {
  const summary = state.workspaceActivitySummaries.get(workspaceID);
  if (!summary) {
    return { isChatBusy: false, isKanbanRunning: false, activeAgentCount: 0, lastMessageSnippet: "" };
  }
  return {
    isChatBusy: summary.isChatBusy,
    isKanbanRunning: summary.isKanbanRunning,
    activeAgentCount: summary.activeAgentCount,
    lastMessageSnippet: summary.lastMessageSnippet ?? "",
  };
}

function renderWorkspaceActivityStatus(workspaceID: string): string {
  const status = getWorkspaceActivityStatus(workspaceID);
  let dotClass = "workspace-activity-dot is-idle";
  let activityText = "";

  if (status.isChatBusy) {
    dotClass = "workspace-activity-dot is-chat-busy";
    activityText = "Streaming…";
  } else if (status.isKanbanRunning) {
    dotClass = "workspace-activity-dot is-kanban-running";
    activityText = status.activeAgentCount > 0
      ? `${status.activeAgentCount} agent${status.activeAgentCount > 1 ? "s" : ""} running`
      : "Kanban running";
  }

  if (!activityText && status.lastMessageSnippet) {
    activityText = status.lastMessageSnippet;
  }

  return `
    <span class="${dotClass}" aria-hidden="true"></span>
    ${activityText ? `<span class="workspace-activity-text" title="${escapeAttribute(activityText)}">${escapeHtml(activityText)}</span>` : ""}
  `;
}

/** Persistent app-shell wrapper.  Creating it once inside appRoot means
 *  that subsequent renders only swap individual region fragments instead
 *  of tearing down the entire DOM tree, eliminating jank during rapid
 *  streaming updates. */
function ensureShell(): HTMLElement {
  let shell = appRoot.querySelector(".app-shell") as HTMLElement;
  if (!shell) {
    shell = document.createElement("div");
    shell.className = "app-shell";
    appRoot.appendChild(shell);
  }
  return shell;
}

type RegionUpdate = {
  element: HTMLElement;
  changed: boolean;
};

const renderedRegionHTML = new WeakMap<HTMLElement, string>();
let mountedCodeViewBoundaryKey = "";

/** Replace a named region only when its rendered output changed.  Local
 *  patch helpers own any DOM changes between region renders. */
function updateRegion(
  shell: HTMLElement,
  name: string,
  html: string,
  force = false,
): RegionUpdate {
  let region = shell.querySelector(`[data-region="${name}"]`) as HTMLElement;
  if (!region) {
    region = document.createElement("div");
    region.dataset.region = name;
    shell.appendChild(region);
  }
  if (!force && renderedRegionHTML.get(region) === html) {
    return { element: region, changed: false };
  }
  region.innerHTML = html;
  renderedRegionHTML.set(region, html);
  return { element: region, changed: true };
}

type RenderFocusSnapshot = {
  key: string;
  selectionStart: number | null;
  selectionEnd: number | null;
  selectionDirection: "forward" | "backward" | "none" | null;
};

type RenderScrollSnapshot = {
  top: number;
  left: number;
};

function captureRenderScrollSnapshots(): Map<string, RenderScrollSnapshot> {
  const snapshots = new Map<string, RenderScrollSnapshot>();
  const documentScroller = document.scrollingElement;
  if (documentScroller && (documentScroller.scrollTop || documentScroller.scrollLeft)) {
    snapshots.set("__document__", {
      top: documentScroller.scrollTop,
      left: documentScroller.scrollLeft,
    });
  }
  appRoot.querySelectorAll<HTMLElement>("*").forEach((element) => {
    if (!isScrollableForRenderSnapshot(element)) {
      return;
    }
    const key = renderScrollKey(element);
    if (!key) {
      return;
    }
    snapshots.set(key, {
      top: element.scrollTop,
      left: element.scrollLeft,
    });
  });
  return snapshots;
}

function restoreRenderScrollSnapshots(snapshots: Map<string, RenderScrollSnapshot>): void {
  const documentSnapshot = snapshots.get("__document__");
  if (documentSnapshot) {
    window.scrollTo(documentSnapshot.left, documentSnapshot.top);
  }
  appRoot.querySelectorAll<HTMLElement>("*").forEach((element) => {
    const snapshot = snapshots.get(renderScrollKey(element));
    if (!snapshot) {
      return;
    }
    const maxTop = Math.max(0, element.scrollHeight - element.clientHeight);
    const maxLeft = Math.max(0, element.scrollWidth - element.clientWidth);
    element.scrollTop = Math.min(snapshot.top, maxTop);
    element.scrollLeft = Math.min(snapshot.left, maxLeft);
  });
}

function captureRenderFocusSnapshot(): RenderFocusSnapshot | null {
  const active = document.activeElement;
  if (
    !(active instanceof HTMLElement) ||
    !appRoot.contains(active) ||
    !(
      active instanceof HTMLInputElement ||
      active instanceof HTMLTextAreaElement ||
      active.isContentEditable
    )
  ) {
    return null;
  }
  let selectionStart: number | null = null;
  let selectionEnd: number | null = null;
  let selectionDirection: "forward" | "backward" | "none" | null = null;
  if (active instanceof HTMLInputElement || active instanceof HTMLTextAreaElement) {
    try {
      selectionStart = active.selectionStart;
      selectionEnd = active.selectionEnd;
      selectionDirection = active.selectionDirection;
    } catch {
      // Some input types do not expose a text selection.
    }
  }
  return {
    key: renderScrollKey(active),
    selectionStart,
    selectionEnd,
    selectionDirection,
  };
}

function restoreRenderFocusSnapshot(snapshot: RenderFocusSnapshot | null): void {
  if (!snapshot) {
    return;
  }
  const element = Array.from(appRoot.querySelectorAll<HTMLElement>("*")).find(
    (candidate) => renderScrollKey(candidate) === snapshot.key,
  );
  if (!element) {
    return;
  }
  element.focus({ preventScroll: true });
  if (
    snapshot.selectionStart !== null &&
    snapshot.selectionEnd !== null &&
    (element instanceof HTMLInputElement || element instanceof HTMLTextAreaElement)
  ) {
    try {
      element.setSelectionRange(
        snapshot.selectionStart,
        snapshot.selectionEnd,
        snapshot.selectionDirection ?? undefined,
      );
    } catch {
      // Some input types can be focused but do not support setSelectionRange.
    }
  }
}

function isScrollableForRenderSnapshot(element: HTMLElement): boolean {
  // CodeMirror persists its own scroll state. Restoring a generic render
  // snapshot here can overwrite an explicit same-file navigation reveal.
  if (element.classList.contains("cm-scroller")) {
    return false;
  }
  return (
    element.scrollTop > 0 ||
    element.scrollLeft > 0 ||
    element.scrollHeight > element.clientHeight + 1 ||
    element.scrollWidth > element.clientWidth + 1
  );
}

function renderScrollKey(element: HTMLElement): string {
  const parts: string[] = [];
  let current: HTMLElement | null = element;
  while (current && current !== appRoot) {
    parts.push(renderScrollSegment(current));
    current = current.parentElement;
  }
  return parts.reverse().join(">");
}

function renderScrollSegment(element: HTMLElement): string {
  const data = stableRenderScrollData(element);
  const id = element.id ? `#${element.id}` : "";
  const classes = Array.from(element.classList)
    .filter((className) => !className.startsWith("is-"))
    .sort()
    .join(".");
  const classPart = classes ? `.${classes}` : "";
  return `${element.tagName.toLowerCase()}${id}${data}${classPart}:nth-of-type(${elementIndex(element)})`;
}

function stableRenderScrollData(element: HTMLElement): string {
  const ignored = new Set(["action", "initialFocus", "messageId", "toastId"]);
  return Object.keys(element.dataset)
    .filter((key) => !ignored.has(key))
    .sort()
    .map((key) => {
      const value = element.dataset[key];
      return value === "" || value === undefined ? `[data-${kebabCase(key)}]` : `[data-${kebabCase(key)}="${value}"]`;
    })
    .join("");
}

function kebabCase(value: string): string {
  return value.replace(/[A-Z]/g, (match) => `-${match.toLowerCase()}`);
}

function elementIndex(element: HTMLElement): number {
  let index = 1;
  let sibling = element.previousElementSibling;
  while (sibling) {
    if (sibling.tagName === element.tagName) {
      index++;
    }
    sibling = sibling.previousElementSibling;
  }
  return index;
}

/* ------------------------------------------------------------------ */
/*  Public                                                             */
/* ------------------------------------------------------------------ */

export function render(): void {
  renderApp(false);
}

/** Code-owned structural updates use this entry point. Background app events
 *  use render(), which keeps an already-mounted code workspace attached. */
export function renderCodeViewUI(): void {
  renderApp(true);
}

function renderApp(refreshCodeView: boolean): void {
  const workspace = activeWorkspace();
  updateWindowTitle();
  if (state.appMode !== "code" || !workspace) {
    destroyCodeEditor();
  }
  const hadDialog = Boolean(appRoot.querySelector('[role="dialog"]'));
  const scrollSnapshots = captureRenderScrollSnapshots();
  const focusSnapshot = captureRenderFocusSnapshot();

  const workspaces = state.appState?.workspaces ?? [];

  if (
    state.chatMention &&
    (!workspace || state.appMode === "code" || state.settingsOpen || workspace.id !== state.chatMention.workspaceId)
  ) {
    clearChatMention();
  }

  const shell = ensureShell();
  const changedRegions: HTMLElement[] = [];
  const leftNav = updateRegion(shell, "left-nav", buildLeftNav(workspaces, workspace));
  if (leftNav.changed) {
    changedRegions.push(leftNav.element);
  }

  const mountedCodeView = shell.querySelector<HTMLElement>(
    '[data-region="main"] [data-code-view]',
  );
  const nextCodeViewBoundaryKey = codeViewBoundaryKey(workspace);
  const preserveMountedCodeView =
    !refreshCodeView &&
    state.appMode === "code" &&
    Boolean(workspace) &&
    mountedCodeViewBoundaryKey === nextCodeViewBoundaryKey &&
    mountedCodeView?.dataset.codeViewWorkspaceId === workspace?.id;
  if (!preserveMountedCodeView) {
    const main = updateRegion(
      shell,
      "main",
      buildMain(workspace, workspaces),
      refreshCodeView || mountedCodeViewBoundaryKey !== nextCodeViewBoundaryKey,
    );
    if (main.changed) {
      changedRegions.push(main.element);
    }
    mountedCodeViewBoundaryKey = state.appMode === "code"
      ? nextCodeViewBoundaryKey
      : "";
  }

  const mobileNav = updateRegion(
    shell,
    "mobile-nav",
    renderMobileBottomNav(workspaces, workspace),
  );
  if (mobileNav.changed) {
    changedRegions.push(mobileNav.element);
  }
  const overlays = updateRegion(shell, "overlays", buildOverlays());
  if (overlays.changed) {
    changedRegions.push(overlays.element);
  }

  changedRegions.forEach((region) => bindEvents(region));
  restoreRenderScrollSnapshots(scrollSnapshots);
  restoreRenderFocusSnapshot(focusSnapshot);
  window.requestAnimationFrame(() => restoreRenderScrollSnapshots(scrollSnapshots));
  if (!hadDialog) {
    focusInitialElement();
  }
  void linkifyAssistantFilePaths();
}

function codeViewBoundaryKey(workspace: services.Workspace | null): string {
  if (state.appMode !== "code" || !workspace) {
    return "";
  }
  const settings = state.appState?.settings ?? state.settingsDraft;
  return JSON.stringify({
    workspaceId: workspace.id,
    displayName: workspace.displayName,
    missing: workspace.missing,
    folders: (workspace.folders ?? []).map((folder) => ({
      id: folder.id,
      label: folder.label,
      path: folder.path,
      missing: folder.missing,
    })),
    leadingWhitespaceIndicators: leadingWhitespaceIndicatorsEnabled(settings),
  });
}

/* ------------------------------------------------------------------ */
/*  Region builders                                                    */
/* ------------------------------------------------------------------ */

function buildLeftNav(
  workspaces: services.Workspace[],
  workspace: services.Workspace | null,
): string {
  const mode = state.appMode;
  const dropdownOpen = state.workspaceDropdownOpen;
  const changesBadge = renderChangesNavBadge(workspace);

  return `
    <aside class="left-nav" aria-label="Primary">
      <div class="left-nav-workspace" data-workspace-dropdown-container>
        <button
          class="nav-icon-button workspace-dropdown-trigger${dropdownOpen ? " is-open" : ""}"
          type="button"
          title="${workspace ? escapeHtml(workspace.displayName) : "Select workspace"}"
          aria-label="Workspace selector"
          aria-expanded="${dropdownOpen}"
          data-action="toggle-workspace-dropdown"
        >${workspace ? renderWorkspaceIcon(workspace) : icons.plus}</button>
        ${dropdownOpen ? `
          <div class="workspace-dropdown" role="menu" aria-label="Workspace list" data-workspace-dropdown>
            ${workspaces.map((ws) => `
              <button
                class="workspace-dropdown-option${ws.id === workspace?.id ? " is-active" : ""} ${ws.missing ? "is-missing" : ""}"
                type="button"
                role="menuitem"
                data-action="activate-workspace"
                data-workspace-id="${escapeHtml(ws.id)}"
                title="${escapeHtml(workspaceFolderSummary(ws))}"
              >
                <span class="workspace-dropdown-option-main">
                  ${escapeHtml(ws.displayName)}
                  ${renderWorkspaceActivityStatus(ws.id)}
                </span>
              </button>
            `).join("")}
            <div class="workspace-dropdown-divider"></div>
            <button
              class="workspace-dropdown-option"
              type="button"
              role="menuitem"
              data-action="add-workspace"
            >Add workspace</button>
          </div>
        ` : ""}
      </div>
      <nav class="left-nav-buttons" aria-label="Views">
        <button class="nav-icon-button${mode === "chat" ? " is-active" : ""}" type="button" title="Chat" aria-label="Chat" data-action="switch-view" data-view="chat">${icons.chat}</button>
        <button class="nav-icon-button${mode === "kanban" ? " is-active" : ""}" type="button" title="Kanban" aria-label="Kanban" data-action="switch-view" data-view="kanban">${icons.kanban}</button>
      </nav>
      <div class="left-nav-actions">
        <button class="nav-icon-button${mode === "code" ? " is-active" : ""}" type="button" title="Code" aria-label="Code view" data-action="${mode === "code" ? "close-code-view" : "open-code-view"}">${icons.code}</button>
        <button class="nav-icon-button${mode === "tasks" ? " is-active" : ""}" type="button" title="Tasks" aria-label="Tasks" data-action="switch-view" data-view="tasks">${icons.tasks}</button>
        <button class="nav-icon-button${mode === "git" ? " is-active" : ""}" type="button" title="Git" aria-label="Git" data-action="switch-view" data-view="git">${icons.git}${changesBadge}</button>
        <button class="nav-icon-button${mode === "dashboard" ? " is-active" : ""}" type="button" title="Dashboard" aria-label="Dashboard" data-action="${mode === "dashboard" ? "close-dashboard" : "open-dashboard"}">${icons.dashboard}</button>
        <button class="nav-icon-button" type="button" title="Settings" aria-label="Settings" data-action="open-settings">${icons.settings}</button>
      </div>
    </aside>
  `;
}

function buildMain(
  workspace: services.Workspace | null,
  workspaces: services.Workspace[],
): string {
  const mode = state.appMode;

  if (mode === "dashboard") {
    return `
      <main class="main-content">
        <section class="workspace-panel" aria-label="Dashboard">
          ${renderDashboard()}
        </section>
      </main>
    `;
  }

  return `
    <main class="main-content">
      <section class="workspace-panel${mode === "code" ? " is-code-mode" : ""}${mode === "git" ? " is-git-mode" : ""}" aria-label="${escapeAttribute(getPanelLabel(mode))}">
        ${mode === "code" && workspace
          ? renderCodeView(workspace)
          : mode === "git" && workspace
            ? renderGitRepositoryPage(workspace, gitRepositoryViewFor(workspace.id))
            : workspace
              ? renderWorkspacePanels(workspace)
              : ""}
      </section>
    </main>
  `;
}

function getPanelLabel(mode: string): string {
  switch (mode) {
    case "code": return "Code";
    case "git": return "Git";
    case "kanban": return "Kanban";
    case "tasks": return "Backlog";
    case "dashboard": return "Dashboard";
    default: return "Chat";
  }
}

function buildOverlays(): string {
  const parts: string[] = [];
  if (state.settingsOpen) {
    parts.push(renderSettingsOverlay(state.appState?.workspaces ?? []));
  }
  if (state.savedCommandEditingId) {
    parts.push(renderSavedCommandDialog());
  }
  parts.push(renderToasts());
  if (state.contextMenu) {
    parts.push(renderContextMenu(state.contextMenu));
  }
  const workspace = activeWorkspace();
  if (workspace && state.appMode !== "code") {
    parts.push(renderCodeQuickOpen(workspace.id, true));
  }
  return parts.join("\n");
}

/* ------------------------------------------------------------------ */
/*  Preserved helpers                                                  */
/* ------------------------------------------------------------------ */

export function renderWorkspacePanels(workspace: services.Workspace | null): string {
  const mode = state.appMode;
  const board = workspace ? kanbanBoardFor(workspace.id) : null;
  const running = workspace ? state.runningKanbanWorkspaces.has(workspace.id) : false;
  const decomposing = workspace ? state.executingPlans.has(workspace.id) : false;
  const hasCards = board ? kanbanCards(board).length > 0 : false;
  const hasDoneCards = board ? (board.done ?? []).length > 0 : false;

  let mainPanel = "";

  if (mode === "chat") {
    mainPanel = `
      ${workspace ? renderBudgetBar(workspace.id) : ""}
      ${renderChatPanel(workspace, true)}
      ${workspace ? renderTerminalPanel(workspace) : ""}
    `;
  } else if (mode === "tasks") {
    mainPanel = workspace ? renderTaskPanel(workspace) : `<div class="empty-state">Add a workspace to create tasks.</div>`;
  } else if (mode === "kanban") {
    mainPanel = `
      <section class="work-panel kanban-panel" aria-label="Kanban">
        ${workspace ? renderBudgetBar(workspace.id) : ""}
        <div class="panel-heading kanban-toolbar">
          ${workspace && hasKanbanRuntime(workspace.id) ? `<div class="kanban-heading-main">${renderKanbanRuntime(workspace.id, running)}</div>` : `<div></div>`}
          ${
            workspace
              ? `<div class="kanban-actions">
                  <button class="secondary-button icon-text-button" type="button" data-action="open-create-ready-card" ${running ? "disabled" : ""}>
                    ${icons.plus}
                    <span>New card</span>
                  </button>
                  <button class="icon-text-button primary-button" type="button" data-action="start-agents" ${running || !hasCards ? "disabled" : ""}>
                    ${icons.execute}
                    <span class="run-button">Run</span>
                  </button>
                  <button class="icon-button danger-button" type="button" title="Clear done cards" aria-label="Clear done Kanban cards" data-action="clear-done-cards" ${hasDoneCards ? "" : "disabled"}>
                    ${icons.trash}
                  </button>
                  <button class="icon-button stop-button" type="button" title="Stop agents" aria-label="Stop agents" data-action="stop-agents" ${running ? "" : "disabled"}>
                    ${icons.stop}
                  </button>
                  <button class="secondary-button icon-text-button heartbeat-toggle-button" type="button" title="Auto-run Kanban at interval (click to cycle)" aria-label="Heartbeat toggle" data-action="toggle-heartbeat" data-workspace-id="${escapeAttribute(workspace.id)}">
                    ${icons.refresh}
                    <span>Auto: ${escapeHtml(heartbeatIntervalLabel(getHeartbeatInterval(workspace.id)))}</span>
                  </button>
                  <button class="secondary-button icon-text-button watchdog-toggle-button" type="button" title="Watchdog verification interval (click to cycle)" aria-label="Watchdog toggle" data-action="toggle-watchdog" data-workspace-id="${escapeAttribute(workspace.id)}">
                    ${icons.search}
                    <span>Watchdog: ${escapeHtml(watchdogIntervalLabel(getWatchdogInterval(workspace.id)))}</span>
                  </button>
                </div>`
              : ""
          }
        </div>
        ${
          board
            ? decomposing && !hasCards
              ? renderDecompositionState()
              : hasCards
                ? renderKanbanBoard(board)
                : renderEmptyBoard()
            : `<div class="empty-state">Add a workspace to create cards.</div>`
        }
      </section>
    `;
  }

  return `
    ${mainPanel}
    ${mode === "kanban" && board ? renderKanbanDetail(board) : ""}
    ${mode === "tasks" && workspace ? renderTaskDetail(workspace) : ""}
    ${workspace && state.creatingKanbanCardWorkspaces.has(workspace.id) && !running ? renderCreateKanbanCardDialog(workspace.id) : ""}
  `;
}

function renderMobileBottomNav(
  workspaces: services.Workspace[],
  workspace: services.Workspace | null,
): string {
  const appName = "Echo";
  const activeMobileView = state.mobileNavView;
  const changesBadge = renderChangesNavBadge(workspace);
  return `
    <nav class="mobile-bottom-nav" role="navigation" aria-label="Main navigation">
      <div class="mobile-nav-brand">
        ${workspaces.length > 0 && workspace ? `<button class="mobile-nav-pill${state.workspaceDropdownOpen ? " is-open" : ""}" type="button" aria-label="Workspace selector" aria-expanded="${state.workspaceDropdownOpen}" data-action="toggle-workspace-dropdown" id="mobile-nav-pill">${escapeHtml(workspace.displayName)}</button>` : `<button class="mobile-nav-pill mobile-nav-add-workspace" type="button" aria-label="Add workspace" data-action="add-workspace">${icons.plus}<span>Add</span></button>`}
        <span class="mobile-nav-app-name">${appName}</span>
      </div>
      ${state.workspaceDropdownOpen ? `
        <div class="mobile-nav-workspace-dropdown" role="menu" aria-label="Workspace list" data-mobile-workspace-dropdown>
          ${workspaces.map((ws) => `
            <button class="mobile-nav-workspace-option${ws.id === workspace?.id ? " is-active" : ""}" type="button" role="menuitem" data-action="activate-workspace" data-workspace-id="${escapeHtml(ws.id)}"${ws.id === workspace?.id ? ' aria-current="page"' : ''}>${escapeHtml(ws.displayName)}${ws.missing ? ' <span class="is-missing">(missing)</span>' : ''}${renderWorkspaceActivityStatus(ws.id)}</button>
          `).join("")}
          ${workspaces.length > 0 ? `
            <div class="workspace-dropdown-divider"></div>
            <button class="mobile-nav-workspace-option" type="button" role="menuitem" data-action="add-workspace">Add workspace</button>
          ` : ""}
        </div>
      ` : ""}
      <div class="mobile-nav-tabs" role="tablist" aria-label="View tabs">
        <button class="mobile-nav-tab${activeMobileView === "chat" ? " is-active" : ""}" type="button" title="Chat" aria-label="Chat" aria-pressed="${activeMobileView === "chat"}" role="tab" aria-selected="${activeMobileView === "chat"}" tabindex="${activeMobileView === "chat" ? "0" : "-1"}" data-mobile-nav-tab-index="0" data-action="switch-view" data-view="chat">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>
        </button>
        <button class="mobile-nav-tab${activeMobileView === "kanban" ? " is-active" : ""}" type="button" title="Kanban" aria-label="Kanban" aria-pressed="${activeMobileView === "kanban"}" role="tab" aria-selected="${activeMobileView === "kanban"}" tabindex="${activeMobileView === "kanban" ? "0" : "-1"}" data-mobile-nav-tab-index="1" data-action="switch-view" data-view="kanban">
          <svg viewBox="0 0 24 24" aria-hidden="true"><rect x="3" y="4" width="5" height="16" rx="1"/><rect x="10" y="4" width="4" height="11" rx="1"/><rect x="16" y="4" width="5" height="14" rx="1"/></svg>
        </button>
        <button class="mobile-nav-tab${activeMobileView === "code" ? " is-active" : ""}" type="button" title="Code" aria-label="Code view" aria-pressed="${activeMobileView === "code"}" role="tab" aria-selected="${activeMobileView === "code"}" tabindex="${activeMobileView === "code" ? "0" : "-1"}" data-mobile-nav-tab-index="2" data-action="${activeMobileView === "code" ? "close-code-view" : "open-code-view"}">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m16 18 6-6-6-6"/><path d="m8 6-6 6 6 6"/></svg>
        </button>
        <button class="mobile-nav-tab${activeMobileView === "tasks" ? " is-active" : ""}" type="button" title="Tasks" aria-label="Tasks" aria-pressed="${activeMobileView === "tasks"}" role="tab" aria-selected="${activeMobileView === "tasks"}" tabindex="${activeMobileView === "tasks" ? "0" : "-1"}" data-mobile-nav-tab-index="3" data-action="switch-view" data-view="tasks">
          ${icons.tasks}
        </button>
        <button class="mobile-nav-tab${activeMobileView === "git" ? " is-active" : ""}" type="button" title="Changes" aria-label="Changes" aria-pressed="${activeMobileView === "git"}" role="tab" aria-selected="${activeMobileView === "git"}" tabindex="${activeMobileView === "git" ? "0" : "-1"}" data-mobile-nav-tab-index="4" data-action="switch-view" data-view="git">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 3v12"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="6" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/></svg>${changesBadge}
        </button>
        <button class="mobile-nav-tab${activeMobileView === "dashboard" ? " is-active" : ""}" type="button" title="Dashboard" aria-label="Dashboard" aria-pressed="${activeMobileView === "dashboard"}" role="tab" aria-selected="${activeMobileView === "dashboard"}" tabindex="${activeMobileView === "dashboard" ? "0" : "-1"}" data-mobile-nav-tab-index="5" data-action="${activeMobileView === "dashboard" ? "close-dashboard" : "open-dashboard"}">
          ${icons.dashboard}
        </button>
        <button class="mobile-nav-tab${activeMobileView === "settings" ? " is-active" : ""}" type="button" title="Settings" aria-label="Settings" aria-pressed="${activeMobileView === "settings"}" role="tab" aria-selected="${activeMobileView === "settings"}" tabindex="${activeMobileView === "settings" ? "0" : "-1"}" data-mobile-nav-tab-index="6" data-action="open-settings">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7Z"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 0 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.3a2 0 0 1-4 0V21a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1A2 2 0 0 1 4.2 17l.1-.1a1.7 1.7 0 0 0 .3-1.9 1.7 1.7 0 0 0-1.6-1H2.7a2 2 0 0 1 0-4H3a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L4.2 7A2 2 0 0 1 7 4.2l.1.1A1.7 1.7 0 0 0 9 4.6 1.7 1.7 0 0 0 10 3V2.7a2 0 0 1 4 0V3a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1A2 2 0 0 1 19.8 7l-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.3a2 2 0 0 1 0 4H21a1.7 1.7 0 0 0-1.6 1Z"/></svg>
        </button>
      </div>
    </nav>
  `;
}

function renderChangesNavBadge(workspace: services.Workspace | null): string {
  if (!workspace) {
    return "";
  }
  const count = pendingChangesCount(workspace.id);
  if (count <= 0) {
    return "";
  }
  const label = `${count} pending change${count === 1 ? "" : "s"}`;
  return `<span class="nav-change-badge" aria-label="${escapeHtml(label)}">${escapeHtml(count > 99 ? "99+" : String(count))}</span>`;
}

function pendingChangesCount(workspaceID: string): number {
  const repository = gitRepositoryViewFor(workspaceID).repository;
  if (repository) {
    return Math.max(0, repository.fileCount ?? 0);
  }
  return Math.max(0, changeReviewFor(workspaceID).fileCount ?? 0);
}

/* ------------------------------------------------------------------ */
/*  Terminal panel                                                     */
/* ------------------------------------------------------------------ */

import type { ShellCommandRun } from "./types";

export function renderTerminalPanel(workspace: services.Workspace): string {
  const isOpen = state.terminalOpen.has(workspace.id);
  const runs = state.terminalRuns.get(workspace.id) ?? [];
  const draft = state.terminalDrafts.get(workspace.id) ?? "";
  const hasRunning = runs.some((r) => r.status === "running");
  const savedCmds = state.savedCommands.get(workspace.id) ?? [];
  const isSavedOpen = state.savedCommandOpenSections.has(workspace.id);

  return `
    <details class="terminal-panel" data-terminal-panel data-workspace-id="${escapeAttribute(workspace.id)}" ${isOpen ? "open" : ""}>
      <summary data-action="toggle-terminal" data-workspace-id="${escapeAttribute(workspace.id)}">
        ${icons.terminal}
        <span>Terminal</span>
        ${hasRunning ? `<span class="spinner terminal-spinner"></span>` : ""}
      </summary>
      <div class="terminal-content">
        <div class="terminal-runs" data-terminal-runs>
          ${runs.map((run) => renderTerminalRun(run)).join("")}
        </div>
        ${renderSavedCommandsSection(workspace)}
        <div class="terminal-input-row">
          <span class="terminal-prompt">$</span>
          <input
            class="terminal-input"
            type="text"
            placeholder="Enter command..."
            value="${escapeAttribute(draft)}"
            data-terminal-input
            data-workspace-id="${escapeAttribute(workspace.id)}"
            aria-label="Terminal command input"
          />
          <button class="icon-button terminal-run-button" type="button" title="Run command" data-action="run-shell-command" data-workspace-id="${escapeAttribute(workspace.id)}">
            ${icons.execute}
          </button>
        </div>
      </div>
    </details>
  `;
}

function renderSavedCommandsSection(workspace: services.Workspace): string {
  const savedCmds = state.savedCommands.get(workspace.id) ?? [];
  const isSavedOpen = state.savedCommandOpenSections.has(workspace.id);
  const wsIdAttr = escapeAttribute(workspace.id);

  if (savedCmds.length === 0) {
    // Show minimal section with just the "Save current" button when no commands exist
    return `
      <div class="terminal-saved-commands">
        <div class="terminal-saved-bar">
          <button type="button" class="icon-button terminal-save-current-button" title="Save current command" data-action="add-saved-command" data-workspace-id="${wsIdAttr}">
            ${icons.plus} Save current
          </button>
        </div>
      </div>
    `;
  }

  return `
    <details class="terminal-saved-commands" ${isSavedOpen ? "open" : ""}>
      <summary class="terminal-saved-header" data-action="toggle-saved-commands" data-workspace-id="${wsIdAttr}">
        Saved Commands (${savedCmds.length})
      </summary>
      <div class="terminal-saved-list">
        ${savedCmds.map((sc) => renderSavedCommandItem(sc, workspace.id)).join("")}
        <div class="terminal-saved-bar">
          <button type="button" class="icon-button terminal-save-current-button" title="Save current command" data-action="add-saved-command" data-workspace-id="${wsIdAttr}">
            ${icons.plus} Save current
          </button>
        </div>
      </div>
    </details>
  `;
}

function renderSavedCommandDialog(): string {
  const editingId = state.savedCommandEditingId;
  if (!editingId) return "";
  const isEdit = !editingId.startsWith("new-");
  const title = isEdit ? "Edit Command" : "New Command";
  const ws = activeWorkspace();
  const wsIdAttr = ws ? escapeAttribute(ws.id) : "";

  return `
    <div class="saved-command-dialog-overlay" data-saved-command-overlay role="dialog" aria-modal="true">
      <div class="saved-command-dialog" data-saved-command-dialog>
        <h3>${escapeHtml(title)}</h3>
        <input
          type="text"
          placeholder="Name"
          value="${escapeAttribute(state.savedCommandDraftName)}"
          data-saved-edit-name
          aria-label="Command name"
          autofocus
        />
        <input
          type="text"
          placeholder="Command"
          value="${escapeAttribute(state.savedCommandDraftCommand)}"
          data-saved-edit-command
          aria-label="Command text"
          style="font-family: 'Cascadia Mono', monospace;"
        />
        <div class="dialog-actions">
          <button type="button" class="secondary-button" data-action="cancel-edit-command" data-workspace-id="${wsIdAttr}">Cancel</button>
          <button type="button" class="primary-button" data-action="save-edited-command" data-workspace-id="${wsIdAttr}" data-saved-edit-id="${escapeAttribute(editingId)}">Save</button>
        </div>
      </div>
    </div>
  `;
}

function renderSavedCommandItem(sc: services.SavedCommand, workspaceId: string): string {
  const wsIdAttr = escapeAttribute(workspaceId);

  return `
    <div class="terminal-saved-item" data-action="run-saved-command" data-workspace-id="${wsIdAttr}" data-saved-id="${escapeAttribute(sc.id)}">
      <div class="terminal-saved-info">
        <span class="terminal-saved-name" title="${escapeAttribute(sc.name)}">${escapeHtml(sc.name)}</span>
        <span class="terminal-saved-cmd" title="${escapeAttribute(sc.command)}">${escapeHtml(sc.command)}</span>
      </div>
      <div class="terminal-saved-actions">
        <button type="button" class="icon-button terminal-edit-saved-button" title="Edit ${escapeAttribute(sc.name)}" data-action="edit-saved-command" data-workspace-id="${wsIdAttr}" data-saved-id="${escapeAttribute(sc.id)}">
          ${icons.edit}
        </button>
        <button type="button" class="icon-button danger-button terminal-delete-saved-button" title="Delete ${escapeAttribute(sc.name)}" data-action="delete-saved-command" data-workspace-id="${wsIdAttr}" data-saved-id="${escapeAttribute(sc.id)}">
          ${icons.trash}
        </button>
      </div>
    </div>
  `;
}

function renderTerminalRun(run: ShellCommandRun): string {
  const isRunning = run.status === "running";
  let statusHtml = "";

  if (isRunning) {
    statusHtml = `<span class="spinner terminal-run-spinner"></span><button class="icon-button danger-button terminal-stop-button" type="button" title="Stop command" data-action="stop-shell-command" data-workspace-id="${escapeAttribute(run.id.split(':')[0])}" data-run-id="${escapeAttribute(run.id)}">${icons.stop}</button>`;
  } else if (run.status === "completed" || run.status === "timed-out") {
    const exitClass = run.exitCode === 0 ? "terminal-exit-success" : "terminal-exit-error";
    const durationSec = run.durationMs !== undefined ? (run.durationMs / 1000).toFixed(1) + "s" : "";
    statusHtml = `<span class="terminal-exit-badge ${exitClass}">Exit ${run.exitCode ?? "?"}</span>${durationSec ? `<time class="terminal-duration">${escapeHtml(durationSec)}</time>` : ""}`;
  }

  return `
    <div class="terminal-run" data-terminal-run data-run-id="${escapeAttribute(run.id)}">
      <div class="terminal-run-header">
        <code class="terminal-command">${escapeHtml(run.command || "(command)")}</code>
        <div class="terminal-run-status">${statusHtml}</div>
      </div>
      ${run.lines.length > 0 ? `
        <div class="terminal-output" data-terminal-output-for="${escapeAttribute(run.id)}">
          ${run.lines.map((line) => `<div class="terminal-line terminal-${line.type}"><span>${escapeHtml(line.text)}</span></div>`).join("")}
        </div>
      ` : ""}
    </div>
  `;
}
