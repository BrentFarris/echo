
import { getAppCallbacks } from "./callbacks";
import { appRoot } from "./dom";
import { icons } from "./icons";
import { state } from "./state";
import type { ContextMenuState } from "./types";
import { escapeAttribute, escapeHtml } from "./utils";

export function renderContextMenu(menu: ContextMenuState): string {
  if (menu.codeTabPath) {
    return renderCodeTabContextMenu(menu);
  }
  if (menu.codePath) {
    return renderCodeContextMenu(menu);
  }
  if (menu.gitPath) {
    return renderGitContextMenu(menu);
  }
  return `\
    <div class="workspace-context-menu" data-context-menu style="left:${menu.x}px;top:${menu.y}px">\
      <button\
        class="workspace-context-menu-item"\
        type="button"\
        title="${escapeAttribute(menu.displayPath)}"\
        data-action="show-in-explorer"\
        data-workspace-id="${escapeAttribute(menu.workspaceId)}"\
        data-workspace-path="${escapeAttribute(menu.workspacePath ?? "")}"\
      >\
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 7v10a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V9l-6-6H5a2 2 0 0 0-2 2Z"/></svg>\
        <span class="workspace-context-menu-label">Show in Explorer</span>\
      </button>\
    </div>\
  `;
}

function renderCodeTabContextMenu(menu: ContextMenuState): string {
  const tabPath = menu.codeTabPath ?? "";
  const pathActionsDisabled = menu.codeTabUntitled === true;
  const workspaceActionsDisabled = pathActionsDisabled || menu.codeTabExternal === true;
  const item = (
    action: string,
    label: string,
    icon: string,
    disabled = false,
  ) => `\
    <button\
      class="workspace-context-menu-item"\
      type="button"\
      role="menuitem"\
      data-action="${escapeAttribute(action)}"\
      data-workspace-id="${escapeAttribute(menu.workspaceId)}"\
      data-code-tab-path="${escapeAttribute(tabPath)}"\
      data-code-tab-external="${menu.codeTabExternal === true ? "true" : "false"}"\
      ${disabled ? "disabled aria-disabled=\"true\"" : ""}\
    >\
      ${icon}\
      <span class="workspace-context-menu-label">${escapeHtml(label)}</span>\
    </button>\
  `;

  return `\
    <div class="workspace-context-menu code-tab-context-menu" role="menu" aria-label="Tab actions for ${escapeAttribute(menu.displayPath)}" data-context-menu style="left:${menu.x}px;top:${menu.y}px">\
      ${item("close-code-tab", "Close", icons.x)}\
      ${item("close-other-code-tabs", "Close Others", icons.collapse, !menu.codeTabCanCloseOthers)}\
      ${item("close-code-tabs-to-right", "Close to the Right", icons.arrowRight, !menu.codeTabCanCloseToRight)}\
      ${item("close-saved-code-tabs", "Close Saved", icons.check, !menu.codeTabCanCloseSaved)}\
      ${item("close-all-code-tabs", "Close All", icons.x)}\
      <hr class="workspace-context-menu-divider" />\
      ${item("copy-code-tab-path", "Copy Path", icons.copy, pathActionsDisabled)}\
      ${item("copy-code-tab-relative-path", "Copy Relative Path", icons.copy, workspaceActionsDisabled)}\
      <hr class="workspace-context-menu-divider" />\
      ${item("reveal-code-tab-in-explorer", "Reveal in Explorer", icons.folder, pathActionsDisabled)}\
      ${item("reveal-code-tab-in-workspace", "Reveal in Workspace", icons.file, workspaceActionsDisabled)}\
    </div>\
  `;
}

function renderGitContextMenu(menu: ContextMenuState): string {
  const gitPath = menu.gitPath ?? "";
  const isFolder = menu.gitKind === "folder";
  return `\
    <div class="workspace-context-menu" data-context-menu style="left:${menu.x}px;top:${menu.y}px">\
      <button\
        class="workspace-context-menu-item danger-button"\
        type="button"\
        title="${escapeAttribute(`Revert ${menu.displayPath}`)}"\
        data-action="${isFolder ? "revert-git-folder" : "revert-git-file"}"\
        data-${isFolder ? "git-folder-path" : "git-file-path"}="${escapeAttribute(gitPath)}"\
      >\
        ${icons.undo}\
        <span class="workspace-context-menu-label">${isFolder ? "Revert folder changes" : "Revert file changes"}</span>\
      </button>\
      <hr class="workspace-context-menu-divider" />\
      <button\
        class="workspace-context-menu-item"\
        type="button"\
        title="${escapeAttribute(menu.displayPath)}"\
        data-action="show-in-explorer"\
        data-workspace-id="${escapeAttribute(menu.workspaceId)}"\
        data-workspace-path="${escapeAttribute(menu.workspacePath ?? gitPath)}"\
      >\
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 7v10a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V9l-6-6H5a2 2 0 0 0-2 2Z"/></svg>\
        <span class="workspace-context-menu-label">Show in Explorer</span>\
      </button>\
    </div>\
  `;
}

function renderCodeContextMenu(menu: ContextMenuState): string {
  const codePath = menu.codePath ?? "";
  const codeKind = menu.codeKind ?? "other";
  const canRenameCodePath = (codeKind === "file" || codeKind === "directory") && codePath.includes("/");
  return `\
    <div class="workspace-context-menu" data-context-menu style="left:${menu.x}px;top:${menu.y}px">\
      <button\
        class="workspace-context-menu-item"\
        type="button"\
        data-action="code-create-file"\
        data-workspace-id="${escapeAttribute(menu.workspaceId)}"\
        data-code-path="${escapeAttribute(codePath)}"\
        data-code-kind="${escapeAttribute(codeKind)}"\
      >\
        ${icons.file}\
        <span class="workspace-context-menu-label">Add file</span>\
      </button>\
      <button\
        class="workspace-context-menu-item"\
        type="button"\
        data-action="code-create-folder"\
        data-workspace-id="${escapeAttribute(menu.workspaceId)}"\
        data-code-path="${escapeAttribute(codePath)}"\
        data-code-kind="${escapeAttribute(codeKind)}"\
      >\
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z"/></svg>\
        <span class="workspace-context-menu-label">Add folder</span>\
      </button>\
      ${canRenameCodePath ? `<button\
        class="workspace-context-menu-item"\
        type="button"\
        data-action="code-rename-path"\
        data-workspace-id="${escapeAttribute(menu.workspaceId)}"\
        data-code-path="${escapeAttribute(codePath)}"\
        data-code-kind="${escapeAttribute(codeKind)}"\
      >\
        ${icons.edit}\
        <span class="workspace-context-menu-label">Rename</span>\
      </button>` : ""}\
      ${canRenameCodePath ? `<button\
        class="workspace-context-menu-item danger-button"\
        type="button"\
        data-action="code-delete-path"\
        data-workspace-id="${escapeAttribute(menu.workspaceId)}"\
        data-code-path="${escapeAttribute(codePath)}"\
        data-code-kind="${escapeAttribute(codeKind)}"\
      >\
        ${icons.trash}\
        <span class="workspace-context-menu-label">Delete</span>\
      </button>` : ""}\
      <hr class="workspace-context-menu-divider" />\
      <button\
        class="workspace-context-menu-item"\
        type="button"\
        title="${escapeAttribute(menu.displayPath)}"\
        data-action="show-in-explorer"\
        data-workspace-id="${escapeAttribute(menu.workspaceId)}"\
        data-workspace-path="${escapeAttribute(menu.workspacePath ?? codePath)}"\
      >\
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 7v10a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V9l-6-6H5a2 2 0 0 0-2 2Z"/></svg>\
        <span class="workspace-context-menu-label">Show in Explorer</span>\
      </button>\
      <button\
        class="workspace-context-menu-item"\
        type="button"\
        data-action="add-file-to-chat"\
        data-workspace-id="${escapeAttribute(menu.workspaceId)}"\
        data-code-path="${escapeAttribute(codePath)}"\
        data-code-kind="${escapeAttribute(codeKind)}"\
      >\
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M7.9 20A9 9 0 1 0 4 16.1L2 22Z"/></svg>\
        <span class="workspace-context-menu-label">Add to chat</span>\
      </button>\
    </div>\
  `;
}

export function showContextMenu(menu: ContextMenuState) {
  state.contextMenu = menu;
  getAppCallbacks().render();

  const menuEl = appRoot.querySelector<HTMLElement>("[data-context-menu]");
  if (!menuEl || !state.contextMenu) {
    return;
  }
  const rect = menuEl.getBoundingClientRect();
  let newX = state.contextMenu.x;
  let newY = state.contextMenu.y;

  if (rect.right > window.innerWidth) {
    newX = Math.max(0, window.innerWidth - rect.width - 4);
  }
  if (rect.bottom > window.innerHeight) {
    newY = Math.max(0, window.innerHeight - rect.height - 4);
  }

  if (newX !== state.contextMenu.x || newY !== state.contextMenu.y) {
    state.contextMenu = { ...state.contextMenu, x: newX, y: newY };
    getAppCallbacks().render();
  }
}

export function dismissContextMenu() {
  if (!state.contextMenu) {
    return;
  }
  state.contextMenu = null;
  getAppCallbacks().render();
}
