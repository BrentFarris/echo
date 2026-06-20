
import { bindCodeViewEvents, finishCodeTabSwitcher, handleCodeTabSwitcherKeydown, saveActiveCodeFile } from "../codeView";
import { bindActionEvents } from "./actions";
import { getAppCallbacks } from "./callbacks";
import { bindChatEvents, clearChatMention, patchChatMentionPicker } from "./chat";
import { scrollChangeReview, scrollChangeReviewFile } from "./changes";
import { dismissContextMenu, showContextMenu } from "./contextMenu";
import { appRoot } from "./dom";
import { bindGitEvents } from "./git";
import { closeSelectedCardDetail, bindCardDescriptionEvents, bindCardMessageEvents, bindCardDirectionEvents } from "./kanban";
import { bindSettingsEvents } from "./settings";
import { activeWorkspace, state } from "./state";
import { applyTheme } from "./theme";

export function bindEvents() {
  bindActionEvents(appRoot);
  bindSettingsEvents(appRoot);
  bindChatEvents(appRoot);
  bindCardDescriptionEvents(appRoot);
  bindCardDirectionEvents(appRoot);
  bindCardMessageEvents(appRoot);
  bindGitEvents(appRoot);
  bindCodeViewEvents(appRoot, getAppCallbacks().codeViewCallbacks());

  appRoot.querySelectorAll<HTMLElement>('[data-action="activate-workspace"]').forEach((button) => {
    button.addEventListener("contextmenu", (event: MouseEvent) => {
      event.preventDefault();
      const workspaceId = button.dataset.workspaceId ?? "";
      const folderPath = button.title ?? "";
      if (!workspaceId || !folderPath) {
        return;
      }
      showContextMenu({
        workspaceId,
        displayPath: folderPath,
        x: event.clientX,
        y: event.clientY,
      });
    });
  });

  document.addEventListener(
    "pointerdown",
    (event: PointerEvent) => {
      if (!state.contextMenu) {
        return;
      }
      const target = event.target;
      if (!(target instanceof Node)) {
        return;
      }
      const menuEl = appRoot.querySelector<HTMLElement>("[data-context-menu]");
      if (menuEl && menuEl.contains(target)) {
        return;
      }
      dismissContextMenu();
    },
    true,
  );
}

export function handleGlobalPointerDown(event: PointerEvent) {
  if (!state.chatMention) {
    return;
  }
  const target = event.target;
  if (!(target instanceof Node)) {
    return;
  }
  const form = appRoot.querySelector<HTMLElement>("[data-chat-form]");
  if (form?.contains(target)) {
    return;
  }
  clearChatMention();
  patchChatMentionPicker();
}

export function handleGlobalKeydown(event: KeyboardEvent) {
  if (state.appMode === "code" && !state.settingsOpen) {
    const workspace = activeWorkspace();
    if (
      workspace &&
      handleCodeTabSwitcherKeydown(workspace.id, getAppCallbacks().codeViewCallbacks(), event)
    ) {
      return;
    }
  }
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") {
    if (state.appMode !== "code" || state.settingsOpen) {
      return;
    }
    const workspace = activeWorkspace();
    if (!workspace) {
      return;
    }
    event.preventDefault();
    void saveActiveCodeFile(workspace.id, getAppCallbacks().codeViewCallbacks());
    return;
  }
  if (state.chatMention) {
    if (event.key !== "Escape") {
      return;
    }
    event.preventDefault();
    clearChatMention();
    patchChatMentionPicker();
    return;
  }
  const workspace = activeWorkspace();
  if (
    workspace &&
    (state.openChangeReviewWorkspaces.has(workspace.id) || state.openGitChangeWorkspaces.has(workspace.id)) &&
    (event.key === "ArrowDown" || event.key === "ArrowUp")
  ) {
    const target = event.target;
    if (
      target instanceof HTMLInputElement ||
      target instanceof HTMLTextAreaElement ||
      target instanceof HTMLSelectElement
    ) {
      return;
    }
    event.preventDefault();
    const direction = event.key === "ArrowDown" ? 1 : -1;
    if (event.ctrlKey || event.metaKey) {
      scrollChangeReviewFile(direction);
    } else {
      scrollChangeReview(direction);
    }
    return;
  }
  if (event.key !== "Escape") {
    return;
  }
  if (state.settingsOpen) {
    event.preventDefault();
    state.settingsOpen = false;
    state.formError = "";
    applyTheme(state.appState?.settings);
    getAppCallbacks().render();
    return;
  }
  if (workspace && state.openGitChangeWorkspaces.has(workspace.id)) {
    event.preventDefault();
    state.openGitChangeWorkspaces.delete(workspace.id);
    state.expandedGitChangeWorkspaces.delete(workspace.id);
    state.loadingGitChangeWorkspaces.delete(workspace.id);
    state.loadingGitRepositoryWorkspaces.delete(workspace.id);
    getAppCallbacks().render();
    return;
  }
  if (state.appMode === "code") {
    return;
  }
  if (!workspace) {
    return;
  }
  if (state.openChangeReviewWorkspaces.has(workspace.id)) {
    event.preventDefault();
    state.openChangeReviewWorkspaces.delete(workspace.id);
    getAppCallbacks().render();
    return;
  }
  const cardID = state.selectedKanbanCards.get(workspace.id) ?? "";
  if (!cardID) {
    return;
  }
  event.preventDefault();
  void closeSelectedCardDetail(workspace.id).finally(getAppCallbacks().render);
}

export function handleGlobalKeyup(event: KeyboardEvent) {
  if (state.appMode !== "code" || state.settingsOpen || event.key !== "Control") {
    return;
  }
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  if (finishCodeTabSwitcher(workspace.id, getAppCallbacks().codeViewCallbacks())) {
    event.preventDefault();
  }
}

export function handleGlobalWindowBlur() {
  if (state.appMode !== "code") {
    return;
  }
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  finishCodeTabSwitcher(workspace.id, getAppCallbacks().codeViewCallbacks());
}
