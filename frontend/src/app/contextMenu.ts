
import { getAppCallbacks } from "./callbacks";
import { appRoot } from "./dom";
import { icons } from "./icons";
import { state } from "./state";
import type { ContextMenuState } from "./types";
import { escapeAttribute, escapeHtml } from "./utils";

export function renderContextMenu(menu: ContextMenuState): string {
  return `\
    <div class="workspace-context-menu" data-context-menu style="left:${menu.x}px;top:${menu.y}px">\
      <button\
        class="workspace-context-menu-item"\
        type="button"\
        data-action="show-in-explorer"\
        data-workspace-id="${escapeAttribute(menu.workspaceId)}"\
        data-workspace-path="${escapeAttribute(menu.workspacePath ?? "")}"\
      >\
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 7v10a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V9l-6-6H5a2 2 0 0 0-2 2Z"/></svg>\
        <span class="workspace-context-menu-label">${escapeHtml(menu.displayPath)}</span>\
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
