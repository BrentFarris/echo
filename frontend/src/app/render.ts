import { destroyCodeEditor, renderCodeView } from "../codeView";
import {
  patchChatPanel,
  patchChatControls,
  renderChatPanel,
  clearChatMention,
  linkifyAssistantFilePaths,
} from "./chat";
import { renderChangeReviewDrawer } from "./changes";
import { renderContextMenu } from "./contextMenu";
import { appRoot, focusInitialElement } from "./dom";
import { bindEvents } from "./events";
import { renderGitRepositoryDrawer } from "./git";
import { icons } from "./icons";
import { kanbanBoardFor, changeReviewFor, gitRepositoryViewFor, activeWorkspace, kanbanCards, state, getActiveChatKanbanTab } from "./state";
import { renderSettingsOverlay } from "./settings";
import { renderToasts } from "./toasts";
import { renderTaskPanel } from "./tasks";
import { escapeHtml, workspaceFolderSummary } from "./utils";
import { renderWorkspaceIcon, renderMissingWorkspace } from "./workspace";
import { hasKanbanRuntime, renderCreateKanbanCardDialog, renderDecompositionState, renderEmptyBoard, renderKanbanBoard, renderKanbanDetail, renderKanbanRuntime } from "./kanban";
import { services } from "../../wailsjs/go/models";

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

/** Replace the innerHTML of a named region, creating the region element
 *  lazily on first write. */
function updateRegion(shell: HTMLElement, name: string, html: string): void {
  let region = shell.querySelector(`[data-region="${name}"]`) as HTMLElement;
  if (!region) {
    region = document.createElement("div");
    region.dataset.region = name;
    shell.appendChild(region);
  }
  region.innerHTML = html;
}

/* ------------------------------------------------------------------ */
/*  Public                                                             */
/* ------------------------------------------------------------------ */

export function render(): void {
  destroyCodeEditor();
  const hadDialog = Boolean(appRoot.querySelector('[role="dialog"]'));

  const workspace = activeWorkspace();
  const workspaces = state.appState?.workspaces ?? [];

  if (
    state.chatMention &&
    (!workspace || state.appMode === "code" || state.settingsOpen || workspace.id !== state.chatMention.workspaceId)
  ) {
    clearChatMention();
  }

  const shell = ensureShell();

  updateRegion(shell, "left-nav", buildLeftNav(workspaces, workspace));
  updateRegion(shell, "main", buildMain(workspace, workspaces));
  updateRegion(
    shell,
    "mobile-nav",
    renderMobileBottomNav(workspaces, workspace),
  );
  updateRegion(shell, "overlays", buildOverlays());

  bindEvents();
  if (!hadDialog) {
    focusInitialElement();
  }
  void linkifyAssistantFilePaths();
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
              >${escapeHtml(ws.displayName)}</button>
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
        <button class="nav-icon-button${mode === "tasks" ? " is-active" : ""}" type="button" title="Tasks" aria-label="Tasks" data-action="switch-view" data-view="tasks">${icons.tasks}</button>
        <button class="nav-icon-button${mode === "kanban" ? " is-active" : ""}" type="button" title="Kanban" aria-label="Kanban" data-action="switch-view" data-view="kanban">${icons.kanban}</button>
      </nav>
      <div class="left-nav-actions">
        <button class="nav-icon-button${mode === "code" ? " is-active" : ""}" type="button" title="Code" aria-label="Code view" data-action="${mode === "code" ? "close-code-view" : "open-code-view"}">${icons.code}</button>
        <button class="nav-icon-button${mode === "git" ? " is-active" : ""}" type="button" title="Git Changes" aria-label="Git changes" data-action="${mode === "git" ? "close-git-changes" : "open-git-changes"}">${icons.git}</button>
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

  return `
    <main class="main-content">
      <section class="workspace-panel" aria-labelledby="${getPanelTitleId(mode)}">
        ${mode === "code" && workspace
          ? renderCodeView(workspace)
          : mode === "git" && workspace
            ? renderGitRepositoryDrawer(workspace, gitRepositoryViewFor(workspace.id))
            : workspace
              ? renderWorkspacePanels(workspace, workspaces.length)
              : ""}
      </section>
    </main>
  `;
}

function getPanelTitleId(mode: string): string {
  switch (mode) {
    case "code": return "code-title";
    case "kanban": return "kanban-title";
    case "tasks": return "tasks-title";
    default: return "chat-title";
  }
}

function buildOverlays(): string {
  const parts: string[] = [];
  if (state.settingsOpen) {
    parts.push(renderSettingsOverlay(state.appState?.workspaces ?? []));
  }
  parts.push(renderToasts());
  if (state.contextMenu) {
    parts.push(renderContextMenu(state.contextMenu));
  }
  return parts.join("\n");
}

/* ------------------------------------------------------------------ */
/*  Preserved helpers                                                  */
/* ------------------------------------------------------------------ */

export function renderWorkspacePanels(workspace: services.Workspace | null, workspaceCount: number): string {
  const mode = state.appMode;
  const board = workspace ? kanbanBoardFor(workspace.id) : null;
  const review = workspace ? changeReviewFor(workspace.id) : null;
  const running = workspace ? state.runningKanbanWorkspaces.has(workspace.id) : false;
  const decomposing = workspace ? state.executingPlans.has(workspace.id) : false;
  const hasCards = board ? kanbanCards(board).length > 0 : false;
  const hasDoneCards = board ? (board.done ?? []).length > 0 : false;
  const reviewCount = review?.fileCount ?? 0;

  let mainPanel = "";

  if (mode === "chat") {
    mainPanel = renderChatPanel(workspace, true);
  } else if (mode === "tasks") {
    mainPanel = workspace ? renderTaskPanel(workspace) : `<div class="empty-state">Add a workspace to create tasks.</div>`;
  } else if (mode === "kanban") {
    mainPanel = `
      <section class="work-panel kanban-panel" aria-labelledby="kanban-title">
        <div class="panel-heading">
          <div class="kanban-heading-main">
            <span>Kanban</span>
            <strong id="kanban-title">${workspace ? escapeHtml(workspace.displayName) : `${workspaceCount} workspace${workspaceCount === 1 ? "" : "s"}`}</strong>
            ${workspace && hasKanbanRuntime(workspace.id) ? renderKanbanRuntime(workspace.id, running) : ""}
          </div>
          ${
            workspace
              ? `<div class="kanban-actions">
                  <button class="secondary-button icon-text-button change-review-button" type="button" title="Review AI file changes" data-action="open-change-review">
                    ${icons.file}
                    <span>Changes</span>
                    <span class="change-count-badge">${escapeHtml(String(reviewCount))}</span>
                  </button>
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
    ${workspace && state.creatingKanbanCardWorkspaces.has(workspace.id) && !running ? renderCreateKanbanCardDialog(workspace.id) : ""}
    ${workspace && state.openChangeReviewWorkspaces.has(workspace.id) ? renderChangeReviewDrawer(workspace, review ?? changeReviewFor(workspace.id)) : ""}
  `;
}

function renderMobileBottomNav(
  workspaces: services.Workspace[],
  workspace: services.Workspace | null,
): string {
  const appName = "Echo";
  const activeMobileView = state.mobileNavView;
  return `
    <nav class="mobile-bottom-nav" role="navigation" aria-label="Main navigation">
      <div class="mobile-nav-brand">
        ${workspaces.length > 0 && workspace ? `<button class="mobile-nav-pill${state.workspaceDropdownOpen ? " is-open" : ""}" type="button" aria-label="Workspace selector" aria-expanded="${state.workspaceDropdownOpen}" data-action="toggle-workspace-dropdown" id="mobile-nav-pill">${escapeHtml(workspace.displayName)}</button>` : ""}
        <span class="mobile-nav-app-name">${appName}</span>
      </div>
      ${state.workspaceDropdownOpen ? `
        <div class="mobile-nav-workspace-dropdown" role="menu" aria-label="Workspace list" data-mobile-workspace-dropdown>
          ${workspaces.map((ws) => `
            <button class="mobile-nav-workspace-option${ws.id === workspace?.id ? " is-active" : ""}" type="button" role="menuitem" data-action="activate-workspace" data-workspace-id="${escapeHtml(ws.id)}"${ws.id === workspace?.id ? ' aria-current="page"' : ''}>${escapeHtml(ws.displayName)}${ws.missing ? ' <span class="is-missing">(missing)</span>' : ''}</button>
          `).join("")}
        </div>
      ` : ""}
      <div class="mobile-nav-tabs" role="tablist" aria-label="View tabs">
        <button class="mobile-nav-tab${activeMobileView === "chat" ? " is-active" : ""}" type="button" title="Chat" aria-label="Chat" aria-pressed="${activeMobileView === "chat"}" role="tab" aria-selected="${activeMobileView === "chat"}" tabindex="${activeMobileView === "chat" ? "0" : "-1"}" data-mobile-nav-tab-index="0" data-action="switch-view" data-view="chat">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M21 15a2 2 0 0 1-2 2H7l-4 4V5a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2z"/></svg>
        </button>
        <button class="mobile-nav-tab${activeMobileView === "tasks" ? " is-active" : ""}" type="button" title="Tasks" aria-label="Tasks" aria-pressed="${activeMobileView === "tasks"}" role="tab" aria-selected="${activeMobileView === "tasks"}" tabindex="${activeMobileView === "tasks" ? "0" : "-1"}" data-mobile-nav-tab-index="1" data-action="switch-view" data-view="tasks">
          ${icons.tasks}
        </button>
        <button class="mobile-nav-tab${activeMobileView === "kanban" ? " is-active" : ""}" type="button" title="Kanban" aria-label="Kanban" aria-pressed="${activeMobileView === "kanban"}" role="tab" aria-selected="${activeMobileView === "kanban"}" tabindex="${activeMobileView === "kanban" ? "0" : "-1"}" data-mobile-nav-tab-index="2" data-action="switch-view" data-view="kanban">
          <svg viewBox="0 0 24 24" aria-hidden="true"><rect x="3" y="4" width="5" height="16" rx="1"/><rect x="10" y="4" width="4" height="11" rx="1"/><rect x="16" y="4" width="5" height="14" rx="1"/></svg>
        </button>
        <button class="mobile-nav-tab${activeMobileView === "code" ? " is-active" : ""}" type="button" title="Code" aria-label="Code view" aria-pressed="${activeMobileView === "code"}" role="tab" aria-selected="${activeMobileView === "code"}" tabindex="${activeMobileView === "code" ? "0" : "-1"}" data-mobile-nav-tab-index="3" data-action="${activeMobileView === "code" ? "close-code-view" : "open-code-view"}">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="m16 18 6-6-6-6"/><path d="m8 6-6 6 6 6"/></svg>
        </button>
        <button class="mobile-nav-tab${activeMobileView === "git" ? " is-active" : ""}" type="button" title="Git Changes" aria-label="Git changes" aria-pressed="${activeMobileView === "git"}" role="tab" aria-selected="${activeMobileView === "git"}" tabindex="${activeMobileView === "git" ? "0" : "-1"}" data-mobile-nav-tab-index="4" data-action="${activeMobileView === "git" ? "close-git-changes" : "open-git-changes"}">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M6 3v12"/><circle cx="6" cy="18" r="3"/><circle cx="18" cy="6" r="3"/><path d="M18 9a9 9 0 0 1-9 9"/></svg>
        </button>
        <button class="mobile-nav-tab${activeMobileView === "settings" ? " is-active" : ""}" type="button" title="Settings" aria-label="Settings" aria-pressed="${activeMobileView === "settings"}" role="tab" aria-selected="${activeMobileView === "settings"}" tabindex="${activeMobileView === "settings" ? "0" : "-1"}" data-mobile-nav-tab-index="5" data-action="open-settings">
          <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7Z"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 0 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.3a2 0 0 1-4 0V21a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1A2 2 0 0 1 4.2 17l.1-.1a1.7 1.7 0 0 0 .3-1.9 1.7 1.7 0 0 0-1.6-1H2.7a2 2 0 0 1 0-4H3a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L4.2 7A2 2 0 0 1 7 4.2l.1.1A1.7 1.7 0 0 0 9 4.6 1.7 1.7 0 0 0 10 3V2.7a2 0 0 1 4 0V3a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1A2 2 0 0 1 19.8 7l-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.3a2 2 0 0 1 0 4H21a1.7 1.7 0 0 0-1.6 1Z"/></svg>
        </button>
      </div>
    </nav>
  `;
}
