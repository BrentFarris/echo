import { setCodeTreeEventBinder, restoreCodeTreeScroll, startExplorerResize } from "./dom";
import { ensureCodeState } from "./state";
import type { CodeViewCallbacks } from "./types";
import { handleSearchInput, refreshCodeTree, toggleDirectory, toggleIgnoredFilter } from "./explorer";
import { activateCodeTab, closeCodeTab, openCodeFile, openPinnedCodeFile, pinCodeTab, saveActiveCodeFile, startOpenTabFileWatch } from "./tabs";
import { mountActiveCodeEditor } from "./editor";

export function bindCodeViewEvents(root: ParentNode, callbacks: CodeViewCallbacks) {
  const view = root.querySelector<HTMLElement>("[data-code-view]");
  const workspaceID = view?.dataset.codeViewWorkspaceId ?? "";
  if (!workspaceID) {
    return;
  }

  bindCodeTreeEvents(root, workspaceID, callbacks);

  root.querySelectorAll<HTMLElement>("[data-code-tab-main]").forEach((element) => {
    element.addEventListener("mousedown", (event) => {
      if (event.button !== 1) {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
    });
    element.addEventListener("auxclick", (event) => {
      if (event.button !== 1) {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      closeCodeTab(workspaceID, element.dataset.codePath ?? "", callbacks);
    });
    element.addEventListener("dblclick", (event) => {
      event.preventDefault();
      event.stopPropagation();
      pinCodeTab(workspaceID, element.dataset.codePath ?? "", callbacks);
    });
  });

  const search = root.querySelector<HTMLInputElement>("[data-code-search]");
  search?.addEventListener("input", (event) => {
    handleSearchInput(workspaceID, event.currentTarget as HTMLInputElement, callbacks);
  });
  search?.addEventListener("focus", () => {
    ensureCodeState(workspaceID).searchFocused = true;
  });
  search?.addEventListener("blur", () => {
    const state = ensureCodeState(workspaceID);
    if (!state.preservingSearchFocus) {
      state.searchFocused = false;
    }
  });
  if (search && ensureCodeState(workspaceID).searchFocused) {
    search.focus();
    search.setSelectionRange(search.value.length, search.value.length);
  }

  const resizer = root.querySelector<HTMLElement>("[data-code-resizer]");
  resizer?.addEventListener("pointerdown", (event) => {
    startExplorerResize(event, workspaceID);
  });

  restoreCodeTreeScroll(workspaceID);
  startOpenTabFileWatch(callbacks);
  void mountActiveCodeEditor(workspaceID, callbacks, { openCodeFile, saveActiveCodeFile });
}

function bindCodeTreeEvents(root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) {
  bindCodeActionEvents(root, workspaceID, callbacks);
  bindCodeFileRowEvents(root, workspaceID, callbacks);
  bindCodeBrowserRowContextMenus(root, workspaceID, callbacks);
}

function bindCodeActionEvents(root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) {
  root.querySelectorAll<HTMLElement>("[data-code-action]").forEach((element) => {
    element.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      void handleCodeAction(element, workspaceID, callbacks);
    });
  });
}

function bindCodeFileRowEvents(root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) {
  root.querySelectorAll<HTMLElement>("[data-code-file-row]").forEach((element) => {
    element.addEventListener("click", (event) => {
      event.preventDefault();
      void openCodeFile(workspaceID, element.dataset.codePath ?? "", callbacks, { temporary: true });
    });
    element.addEventListener("dblclick", (event) => {
      event.preventDefault();
      void openPinnedCodeFile(workspaceID, element.dataset.codePath ?? "", callbacks);
    });
    element.addEventListener("keydown", (event) => {
      if (event.key !== "Enter") {
        return;
      }
      event.preventDefault();
      void openCodeFile(workspaceID, element.dataset.codePath ?? "", callbacks, { temporary: true });
    });
  });
}

function bindCodeBrowserRowContextMenus(root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) {
  root.querySelectorAll<HTMLElement>("[data-code-browser-row]").forEach((element) => {
    element.addEventListener("contextmenu", (event) => {
      event.preventDefault();
      event.stopPropagation();
      const path = element.dataset.codePath ?? "";
      if (!path) {
        return;
      }
      callbacks.showCodePathContextMenu(
        workspaceID,
        path,
        element.getAttribute("title") ?? path,
        event.clientX,
        event.clientY,
      );
    });
  });
}

async function handleCodeAction(target: HTMLElement, workspaceID: string, callbacks: CodeViewCallbacks) {
  const action = target.dataset.codeAction ?? "";
  const path = target.dataset.codePath ?? "";
  if (action === "toggle-filter") {
    toggleIgnoredFilter(workspaceID, callbacks);
    return;
  }
  if (action === "refresh-tree") {
    await refreshCodeTree(workspaceID, callbacks);
    return;
  }
  if (action === "toggle-directory") {
    await toggleDirectory(workspaceID, path, callbacks);
    return;
  }
  if (action === "activate-tab" || action === "activate-switcher-tab") {
    activateCodeTab(workspaceID, path, callbacks);
    return;
  }
  if (action === "close-tab") {
    closeCodeTab(workspaceID, path, callbacks);
    return;
  }
  if (action === "save-active-file") {
    await saveActiveCodeFile(workspaceID, callbacks);
  }
}

setCodeTreeEventBinder(bindCodeTreeEvents);
