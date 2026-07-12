
import { FindWorkspaceFileDefinition } from "../backend/services";
import { services } from "../../wailsjs/go/models";
import { codeStates } from "../codeView/state";
import { getAppCallbacks } from "./callbacks";
import { appRoot } from "./dom";
import { icons } from "./icons";
import { state } from "./state";
import type { ContextMenuState } from "./types";
import { escapeAttribute, escapeHtml } from "./utils";

export function renderContextMenu(menu: ContextMenuState): string {
  if (menu.editorPath !== undefined) {
    return renderEditorContextMenu(menu);
  }
  if (menu.codePath) {
    return renderCodeContextMenu(menu);
  }
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

function renderEditorContextMenu(menu: ContextMenuState): string {
  const hasSymbol = menu.editorPosition != null;
  const isDefinitionValid = menu.editorPositionValid === true;
  const isPending = menu.editorPositionValidating === true;
  const isDisabled = !hasSymbol || (!isDefinitionValid && !isPending);
  const tooltipText = !hasSymbol
    ? "No symbol detected at cursor position"
    : isPending
      ? "Checking for definition..."
      : !isDefinitionValid
        ? "No definition found for this symbol"
        : "";
  const hasSpellCheck = menu.spellCheckWord != null;
  const hasSuggestions = menu.spellCheckSuggestions != null && menu.spellCheckSuggestions.length > 0;

  let suggestionsHTML = "";
  if (hasSpellCheck) {
    const addLabel = `Add "${escapeHtml(menu.spellCheckWord ?? "")}" to dictionary`;
    suggestionsHTML = `\
      <hr class="workspace-context-menu-divider" />
      <button
        class="workspace-context-menu-item"
        type="button"
        data-action="editor-spell-add-dictionary"
        data-workspace-id="${escapeAttribute(menu.workspaceId)}"
        data-editor-path="${escapeAttribute(menu.editorPath ?? "")}"
        data-spell-word="${escapeAttribute(menu.spellCheckWord ?? "")}"
      >
        <span class="workspace-context-menu-label">${escapeHtml(addLabel)}</span>
      </button>
    `;
    if (hasSuggestions) {
      const wordLabel = `Did you mean "${escapeHtml(menu.spellCheckWord ?? "")}"?`;
      suggestionsHTML += `\
        <hr class="workspace-context-menu-divider" />
        <div class="workspace-context-menu-section-label">${escapeHtml(wordLabel)}</div>
        ${menu.spellCheckSuggestions!.map((suggestion) => `
          <button
            class="workspace-context-menu-item"
            type="button"
            data-action="editor-spell-suggest"
            data-workspace-id="${escapeAttribute(menu.workspaceId)}"
            data-editor-path="${escapeAttribute(menu.editorPath ?? "")}"
            data-suggestion="${escapeAttribute(suggestion)}"
            data-spell-from="${menu.spellCheckFrom ?? ""}"
            data-spell-to="${menu.spellCheckTo ?? ""}"
          >
            <span class="workspace-context-menu-label">${escapeHtml(suggestion)}</span>
          </button>
        `).join("")}
      `;
    }
  }

  return `<div class="workspace-context-menu" data-context-menu style="left:${menu.x}px;top:${menu.y}px">\
      <button\
        class="workspace-context-menu-item"\
        type="button"\
        data-action="editor-go-to-definition"\
        data-workspace-id="${escapeAttribute(menu.workspaceId)}"\
        data-editor-path="${escapeAttribute(menu.editorPath ?? "")}"\
        data-editor-position="${hasSymbol ? String(menu.editorPosition) : "-1"}"\
        ${isDisabled ? `data-tooltip="${escapeAttribute(tooltipText)}" disabled` : ''}\
      >\
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><polyline points="15 3 21 3 21 9"/><line x1="10" y1="14" x2="21" y2="3"/></svg>\
        <span class="workspace-context-menu-label">Go to Definition</span>\
      </button>\
      ${suggestionsHTML}\
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

  // If there's a valid editor position, asynchronously validate that the symbol
  // actually maps to an LSP definition. Start disabled; enable only if found.
  if (menu.editorPath != null && menu.editorPosition != null) {
    state.contextMenu = { ...state.contextMenu, editorPositionValidating: true };
    getAppCallbacks().render();
    void validateEditorDefinition(menu);
  }

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

async function validateEditorDefinition(menu: ContextMenuState) {
  try {
    // Get file content from the active tab.
    const codeState = codeStates.get(menu.workspaceId);
    const tab = codeState?.tabs.find((t) => t.path === menu.editorPath);
    const content = tab?.content ?? "";
    if (!content || menu.editorPosition == null) {
      return;
    }

    const response = services.WorkspaceDefinitionResponse.createFrom(
      await FindWorkspaceFileDefinition(
        menu.workspaceId,
        services.WorkspaceDefinitionRequest.createFrom({
          filePath: menu.editorPath,
          content,
          position: menu.editorPosition,
        }),
      ),
    );

    // Only update if the context menu is still open for this file.
    const currentMenu = state.contextMenu;
    if (currentMenu == null || currentMenu.editorPath !== menu.editorPath) {
      return;
    }

    const isValid = response.found && !!response.targetPath;
    if (isValid !== (currentMenu.editorPositionValid === true)) {
      state.contextMenu = { ...currentMenu, editorPositionValid: isValid, editorPositionValidating: false };
      getAppCallbacks().render();
    } else if (currentMenu.editorPositionValidating === true) {
      // Validation result unchanged but flag needs clearing.
      state.contextMenu = { ...currentMenu, editorPositionValidating: false };
      getAppCallbacks().render();
    }
  } catch {
    // LSP validation failed — keep button disabled.
  }
}
