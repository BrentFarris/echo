
import { destroyCodeEditor, renderCodeView } from "../codeView";
import { renderChatPanel, clearChatMention, linkifyAssistantFilePaths } from "./chat";
import { renderChangeReviewDrawer } from "./changes";
import { renderContextMenu } from "./contextMenu";
import { appRoot, captureScrollSnapshot, focusInitialElement, restoreScrollSnapshot } from "./dom";
import { bindEvents } from "./events";
import { renderGitRepositoryDrawer } from "./git";
import { icons } from "./icons";
import { kanbanBoardFor, changeReviewFor, gitRepositoryViewFor, activeWorkspace, kanbanCards, state } from "./state";
import { renderSettingsOverlay } from "./settings";
import { renderToasts } from "./toasts";
import { escapeHtml, workspaceFolderSummary } from "./utils";
import { renderWorkspaceIcon, renderMissingWorkspace } from "./workspace";
import { hasKanbanRuntime, renderDecompositionState, renderEmptyBoard, renderKanbanBoard, renderKanbanDetail, renderKanbanRuntime } from "./kanban";
import { services } from "../../wailsjs/go/models";

export function render() {
  destroyCodeEditor();
  const chatScroll = captureScrollSnapshot("[data-chat-log]");
  const cardDetailScroll = captureScrollSnapshot("[data-card-detail]");
  const changeReviewScroll = captureScrollSnapshot("[data-change-review]");
  const settingsScroll = captureScrollSnapshot("[data-settings-form]");
  const hadDialog = Boolean(appRoot.querySelector('[role="dialog"]'));
  const workspace = activeWorkspace();
  const workspaces = state.appState?.workspaces ?? [];
  const showingCode = state.appMode === "code" && Boolean(workspace);
  const showGitChanges = workspace && state.openGitChangeWorkspaces.has(workspace.id);

  // Preserve the live draft value from the existing textarea before DOM destruction.
  const existingTextarea = appRoot.querySelector<HTMLTextAreaElement>("textarea[data-chat-input]");
  const preservedDraft = existingTextarea?.value ?? "";

  if (
    state.chatMention &&
    (!workspace || showingCode || state.settingsOpen || workspace.id !== state.chatMention.workspaceId)
  ) {
    clearChatMention();
  }

  appRoot.innerHTML = `
    <div class="app-shell">
      <aside class="gutter" aria-label="Primary">
        <nav class="workspace-rail" aria-label="Workspaces" data-workspace-rail>
          ${workspaces
            .map(
              (item) => `
                <button
                  class="gutter-button workspace-button ${item.active ? "is-active" : ""} ${item.missing ? "is-missing" : ""}"
                  type="button"
                  draggable="true"
                  title="${escapeHtml(workspaceFolderSummary(item))}"
                  aria-label="${escapeHtml(item.displayName)}${item.missing ? " missing" : ""}"
                  data-action="activate-workspace"
                  data-workspace-drag-item
                  data-workspace-id="${escapeHtml(item.id)}"
                >${renderWorkspaceIcon(item)}</button>
              `,
            )
            .join("")}
        </nav>
        <div class="gutter-actions">
          <button class="gutter-button icon-button" type="button" title="Add workspace" aria-label="Add workspace" data-action="add-workspace">
            ${icons.plus}
          </button>
          <button class="gutter-button icon-button" type="button" title="Settings" aria-label="Settings" data-action="open-settings">
            ${icons.settings}
          </button>
        </div>
      </aside>
      <main class="main-content">
        <section class="workspace-panel ${showingCode ? "is-code-mode" : ""}" aria-labelledby="${showingCode ? "code-title" : "workspace-title"}">
          ${
            showingCode && workspace
              ? renderCodeView(workspace)
              : `
                <div class="workspace-heading-row">
                  <div class="workspace-heading-main">
                    <strong id="workspace-title">${workspace ? escapeHtml(workspace.displayName) : "Workspace"}</strong><span class="heading-path">${workspace ? escapeHtml(workspaceFolderSummary(workspace)) : ""}</span>
                  </div>
                  ${
                    workspace
                      ? `<div class="workspace-heading-actions">
                          <button class="secondary-button icon-text-button" type="button" data-action="open-git-changes">
                            ${icons.git}
                            <span>Git</span>
                          </button>
                          <button class="secondary-button icon-text-button" type="button" data-action="open-code-view">
                            ${icons.code}
                            <span>Code</span>
                          </button>
                        </div>`
                      : ""
                  }
                </div>
                ${workspace ? renderWorkspacePanels(workspace, workspaces.length) : ""}
              `
          }
        </section>
        ${showGitChanges ? renderGitRepositoryDrawer(workspace, gitRepositoryViewFor(workspace.id)) : ""}
      </main>
      ${state.settingsOpen ? renderSettingsOverlay(workspaces) : ""}
      ${renderToasts()}
      ${state.contextMenu ? renderContextMenu(state.contextMenu) : ""}
    </div>
  `;

  bindEvents();
  restoreScrollSnapshot("[data-chat-log]", chatScroll);
  restoreScrollSnapshot("[data-card-detail]", cardDetailScroll);
  restoreScrollSnapshot("[data-change-review]", changeReviewScroll);
  restoreScrollSnapshot("[data-settings-form]", settingsScroll);
  if (!hadDialog) {
    focusInitialElement();
  }
  void linkifyAssistantFilePaths();

  // Restore the preserved draft value to the newly created textarea if it differs from what was rendered.
  const restoredTextarea = appRoot.querySelector<HTMLTextAreaElement>("textarea[data-chat-input]");
  if (restoredTextarea && restoredTextarea.value !== preservedDraft) {
    restoredTextarea.value = preservedDraft;
  }
}

export function renderWorkspacePanels(workspace: services.Workspace | null, workspaceCount: number): string {
  const board = workspace ? kanbanBoardFor(workspace.id) : null;
  const review = workspace ? changeReviewFor(workspace.id) : null;
  const running = workspace ? state.runningKanbanWorkspaces.has(workspace.id) : false;
  const decomposing = workspace ? state.executingPlans.has(workspace.id) : false;
  const hasCards = board ? kanbanCards(board).length > 0 : false;
  const hasDoneCards = board ? (board.done ?? []).length > 0 : false;
  const chatExpanded = workspace ? state.expandedChatWorkspaces.has(workspace.id) : false;
  const kanbanExpanded = workspace && !chatExpanded ? state.expandedKanbanWorkspaces.has(workspace.id) : false;
  const kanbanSizeLabel = kanbanExpanded ? "Collapse Kanban" : "Expand Kanban";
  const reviewCount = review?.fileCount ?? 0;
  return `
    <div class="split-panels ${chatExpanded ? "is-chat-expanded" : ""} ${kanbanExpanded ? "is-kanban-expanded" : ""}">
      ${renderChatPanel(workspace, chatExpanded)}
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
                  <button class="icon-button" type="button" title="${kanbanSizeLabel}" aria-label="${kanbanSizeLabel}" aria-pressed="${kanbanExpanded}" data-action="toggle-kanban-size">
                    ${kanbanExpanded ? icons.collapse : icons.expand}
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
    </div>
    ${board ? renderKanbanDetail(board) : ""}
    ${workspace && state.openChangeReviewWorkspaces.has(workspace.id) ? renderChangeReviewDrawer(workspace, review ?? changeReviewFor(workspace.id)) : ""}
  `;
}
