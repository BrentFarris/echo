import { setCodeQuickOpenEventBinder, setCodeTextSearchEventBinder, setCodeTreeEventBinder, restoreCodeTreeScroll, startExplorerResize } from "./dom";
import { ensureCodeState, isCodeTreeEntrySelected, type CodeTreeSelectionEntry } from "./state";
import type { CodeViewCallbacks } from "./types";
import { cancelPendingCodeCreate, cancelPendingCodeRename, clearCodeDrag, collapseCodeTree, deleteSelectedCodePaths, dropCodeDrag, handleSearchInput, refreshCodeTree, selectCodeTreeEntry, startCodeDrag, startSelectedCodeCreate, startSelectedCodeRename, submitPendingCodeCreate, submitPendingCodeRename, toggleDirectory, toggleIgnoredFilter, updateCodeDropTarget, updatePendingCodeCreateName, updatePendingCodeRenameName } from "./explorer";
import { activateCodeTab, closeCodeTab, createUntitledCodeFile, navigateCodeHistory, openCodeFile, openPinnedCodeFile, pinCodeTab, saveActiveCodeFile, startOpenTabFileWatch, toggleTemporaryFiles } from "./tabs";
import { mountActiveCodeEditor } from "./editor";
import { openInlineCodeChatAtCursor } from "./inlineChat";
import { closeQuickOpen, handleQuickOpenInput, moveQuickOpenSelection, openQuickOpenSelection } from "./quickOpen";
import { closeTextSearch, handleTextSearchFieldInput, openTextSearch, openTextSearchMatch, runTextSearchNow, toggleTextSearchOption } from "./search";
import { bindDebugViewEvents } from "./debug";

export function bindCodeViewEvents(root: ParentNode, callbacks: CodeViewCallbacks) {
  const view = root.querySelector<HTMLElement>("[data-code-view]");
  const workspaceID = view?.dataset.codeViewWorkspaceId ?? "";
  if (!view || !workspaceID) {
    return;
  }

  bindCodeHistoryMouseButtonEvents(view, workspaceID, callbacks);
  bindCodeTreeEvents(root, workspaceID, callbacks);
  bindCodeTextSearchEvents(root, workspaceID, callbacks);
  bindCodeQuickOpenEvents(root, workspaceID, callbacks);
  bindDebugViewEvents(root, workspaceID, callbacks);

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
      void closeCodeTab(workspaceID, element.dataset.codePath ?? "", callbacks);
    });
    element.addEventListener("dblclick", (event) => {
      event.preventDefault();
      event.stopPropagation();
      pinCodeTab(workspaceID, element.dataset.codePath ?? "", callbacks);
    });
  });

  root.querySelectorAll<HTMLElement>("[data-code-tab]").forEach((element) => {
    element.addEventListener("contextmenu", (event) => {
      event.preventDefault();
      event.stopPropagation();
      const state = ensureCodeState(workspaceID);
      const path = element.dataset.codeTab ?? "";
      const index = state.tabs.findIndex((tab) => tab.path === path);
      const tab = state.tabs[index];
      if (!tab || index < 0) {
        return;
      }
      callbacks.showCodeTabContextMenu(
        workspaceID,
        {
          path: tab.path,
          label: element.querySelector<HTMLElement>(".code-tab-title")?.textContent ?? tab.path,
          untitled: tab.untitled,
          external: tab.external,
          canCloseOthers: state.tabs.length > 1,
          canCloseToRight: index < state.tabs.length - 1,
          canCloseSaved: state.tabs.some((candidate) => !candidate.dirty),
        },
        event.clientX,
        event.clientY,
      );
    });
  });

  bindUntitledFileCreationEvents(root, workspaceID, callbacks);

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
  void mountActiveCodeEditor(workspaceID, callbacks, { openCodeFile, navigateCodeHistory, saveActiveCodeFile });
}

function bindCodeHistoryMouseButtonEvents(
  view: HTMLElement,
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  const preventSideButtonDefault = (event: MouseEvent) => {
    const direction = codeHistoryMouseButtonDirection(event);
    if (!direction) {
      return;
    }
    event.preventDefault();
    event.stopPropagation();
    event.stopImmediatePropagation();
    if (event.type === "mousedown") {
      void navigateCodeHistory(workspaceID, callbacks, direction);
    }
  };

  view.addEventListener("mousedown", preventSideButtonDefault, { capture: true });
  view.addEventListener("mouseup", preventSideButtonDefault, { capture: true });
  view.addEventListener("auxclick", preventSideButtonDefault, { capture: true });
}

function codeHistoryMouseButtonDirection(event: MouseEvent): -1 | 1 | null {
  if (event.button === 3) {
    return -1;
  }
  if (event.button === 4) {
    return 1;
  }
  return null;
}

function bindUntitledFileCreationEvents(
  root: ParentNode,
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  const createFromBlankArea = (event: MouseEvent) => {
    if (event.target !== event.currentTarget) {
      return;
    }
    event.preventDefault();
    createUntitledCodeFile(workspaceID, callbacks);
  };
  root.querySelector<HTMLElement>("[data-code-tabs]")?.addEventListener(
    "dblclick",
    createFromBlankArea,
  );
  root.querySelector<HTMLElement>("[data-code-empty]")?.addEventListener(
    "dblclick",
    createFromBlankArea,
  );
}

function bindCodeTreeEvents(root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) {
  bindCodeActionEvents(root, workspaceID, callbacks);
  bindCodeFileRowEvents(root, workspaceID, callbacks);
  bindCodeBrowserRowSelectionEvents(root, workspaceID);
  bindCodeBrowserRowKeyboardEvents(root, workspaceID, callbacks);
  bindCodeBrowserRowContextMenus(root, workspaceID, callbacks);
  bindCodeBrowserRowDragEvents(root, workspaceID, callbacks);
  bindCodeCreateInputEvents(root, workspaceID, callbacks);
  bindCodeRenameInputEvents(root, workspaceID, callbacks);
}

function bindCodeTextSearchEvents(root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) {
  const state = ensureCodeState(workspaceID);
  root.querySelectorAll<HTMLElement>('[data-code-action="toggle-text-search-option"]').forEach((element) => {
    element.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      toggleTextSearchOption(workspaceID, element.dataset.codeTextSearchOption ?? "", callbacks);
    });
  });

  root.querySelectorAll<HTMLInputElement>("[data-code-text-search-field]").forEach((input) => {
    input.addEventListener("input", () => {
      handleTextSearchFieldInput(workspaceID, input, callbacks);
    });
    input.addEventListener("focus", () => {
      const field = input.dataset.codeTextSearchField ?? "";
      if (field === "query" || field === "include" || field === "exclude") {
        ensureCodeState(workspaceID).textSearchFocusedField = field;
      }
    });
    input.addEventListener("blur", () => {
      const latest = ensureCodeState(workspaceID);
      if (!latest.preservingTextSearchFocus) {
        latest.textSearchFocusedField = "";
      }
    });
    input.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        event.stopPropagation();
        runTextSearchNow(workspaceID, callbacks);
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        closeTextSearch(workspaceID, callbacks);
      }
    });
  });

  root.querySelectorAll<HTMLElement>("[data-code-text-search-match]").forEach((element) => {
    element.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      void openTextSearchMatch(workspaceID, element, callbacks);
    });
    element.addEventListener("keydown", (event) => {
      if (event.key !== "Enter") {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      void openTextSearchMatch(workspaceID, element, callbacks);
    });
  });

  if (state.textSearchFocusedField) {
    const input = root.querySelector<HTMLInputElement>(`[data-code-text-search-field="${state.textSearchFocusedField}"]`);
    input?.focus();
    input?.setSelectionRange(input.value.length, input.value.length);
  }
}

export function bindCodeQuickOpenEvents(root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) {
  const input = root.querySelector<HTMLInputElement>("[data-code-quick-open-input]");
  input?.addEventListener("input", () => {
    handleQuickOpenInput(workspaceID, input, callbacks);
  });
  input?.addEventListener("keydown", (event) => {
    if (event.key === "ArrowDown") {
      event.preventDefault();
      event.stopPropagation();
      moveQuickOpenSelection(workspaceID, 1, callbacks);
      return;
    }
    if (event.key === "ArrowUp") {
      event.preventDefault();
      event.stopPropagation();
      moveQuickOpenSelection(workspaceID, -1, callbacks);
      return;
    }
    if (event.key === "Enter") {
      event.preventDefault();
      event.stopPropagation();
      void openQuickOpenSelection(workspaceID, callbacks);
      return;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      event.stopPropagation();
      closeQuickOpen(workspaceID, callbacks);
    }
  });

  root.querySelector<HTMLElement>("[data-code-quick-open-close]")?.addEventListener("click", (event) => {
    event.preventDefault();
    event.stopPropagation();
    closeQuickOpen(workspaceID, callbacks);
  });

  bindCodeQuickOpenResultEvents(root, workspaceID, callbacks);
}

function bindCodeQuickOpenResultEvents(root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) {
  root.querySelectorAll<HTMLElement>("[data-code-quick-open-item]").forEach((element) => {
    element.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      void openQuickOpenSelection(workspaceID, callbacks, element.dataset.codePath ?? "");
    });
  });
}

export function handleGlobalCodeTreeNavigation(
  workspaceID: string,
  event: KeyboardEvent,
): boolean {
  if (
    (event.key !== "ArrowDown" && event.key !== "ArrowUp") ||
    event.altKey ||
    event.ctrlKey ||
    event.metaKey ||
    event.shiftKey
  ) {
    return false;
  }
  const state = ensureCodeState(workspaceID);
  if (state.textSearchOpen || state.quickOpen.open || state.pendingCreate || state.pendingRename) {
    return false;
  }
  const explorer = document.querySelector<HTMLElement>(".code-explorer:not(.code-text-search-sidebar)");
  const tree = explorer?.querySelector<HTMLElement>("[data-code-tree]");
  const target = event.target instanceof Element ? event.target : null;
  const activeElement = document.activeElement;
  const treeContextActive = Boolean(
    (activeElement && explorer?.contains(activeElement)) ||
    target?.closest(".code-explorer") ||
    activeElement === document.body,
  );
  if (
    !explorer ||
    !tree ||
    !state.selectedPath ||
    !treeContextActive ||
    Boolean(target?.closest("input, textarea, select, .cm-editor"))
  ) {
    return false;
  }
  const rows = visibleCodeBrowserRows(tree);
  const targetRow = target?.closest<HTMLElement>("[data-code-browser-row]") ?? null;
  const current = targetRow && rows.includes(targetRow)
    ? targetRow
    : rows.find((row) => row.dataset.codePath === state.selectedPath);
  if (!current) {
    return false;
  }
  event.preventDefault();
  event.stopPropagation();
  focusAdjacentCodeBrowserRow(tree, workspaceID, current, event.key === "ArrowDown" ? 1 : -1);
  return true;
}

function bindCodeActionEvents(root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) {
  root.querySelectorAll<HTMLElement>("[data-code-action]").forEach((element) => {
    element.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopImmediatePropagation();
      void handleCodeAction(element, workspaceID, callbacks, event);
    });
  });
}

function bindCodeFileRowEvents(root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) {
  root.querySelectorAll<HTMLElement>("[data-code-file-row]").forEach((element) => {
    element.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopImmediatePropagation();
      const selectionOnly = selectCodeBrowserRowFromMouse(root, workspaceID, element, event);
      if (selectionOnly) {
        updateCodeBrowserRowSelectionClasses(root, workspaceID);
        return;
      }
      ensureCodeState(workspaceID).explorerDrawerOpen = false;
      void openCodeFile(workspaceID, element.dataset.codePath ?? "", callbacks, { temporary: true });
    });
    element.addEventListener("dblclick", (event) => {
      event.preventDefault();
      event.stopImmediatePropagation();
      if (hasCodeSelectionModifier(event)) {
        selectCodeBrowserRowFromMouse(root, workspaceID, element, event);
        updateCodeBrowserRowSelectionClasses(root, workspaceID);
        return;
      }
      selectCodeTreeEntry(workspaceID, element.dataset.codePath ?? "", element.dataset.codeKind ?? "file");
      ensureCodeState(workspaceID).explorerDrawerOpen = false;
      void openPinnedCodeFile(workspaceID, element.dataset.codePath ?? "", callbacks);
    });
  });
}

function bindCodeBrowserRowSelectionEvents(root: ParentNode, workspaceID: string) {
  root.querySelectorAll<HTMLElement>("[data-code-browser-row]").forEach((element) => {
    element.addEventListener("click", (event) => {
      selectCodeBrowserRowFromMouse(root, workspaceID, element, event);
      updateCodeBrowserRowSelectionClasses(root, workspaceID);
    });
  });
}

function bindCodeBrowserRowKeyboardEvents(root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) {
  root.querySelectorAll<HTMLElement>("[data-code-browser-row]").forEach((element) => {
    element.addEventListener("keydown", (event) => {
      void handleCodeBrowserRowKeydown(root, element, workspaceID, callbacks, event);
    });
  });
}

async function handleCodeBrowserRowKeydown(
  root: ParentNode,
  element: HTMLElement,
  workspaceID: string,
  callbacks: CodeViewCallbacks,
  event: KeyboardEvent,
) {
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    event.preventDefault();
    event.stopPropagation();
    focusAdjacentCodeBrowserRow(root, workspaceID, element, event.key === "ArrowDown" ? 1 : -1);
    return;
  }
  if (event.key === "ArrowLeft") {
    event.preventDefault();
    event.stopPropagation();
    if (isCodeDirectoryRow(element) && element.getAttribute("aria-expanded") === "true") {
      await toggleFocusedDirectory(workspaceID, element, callbacks);
    }
    return;
  }
  if (event.key === "ArrowRight") {
    event.preventDefault();
    event.stopPropagation();
    if (isCodeDirectoryRow(element) && element.getAttribute("aria-expanded") !== "true") {
      await toggleFocusedDirectory(workspaceID, element, callbacks);
    }
    return;
  }
  if (event.key === "F2") {
    event.preventDefault();
    event.stopPropagation();
    selectCodeTreeEntry(workspaceID, element.dataset.codePath ?? "", element.dataset.codeKind ?? "");
    await startSelectedCodeRename(workspaceID, callbacks);
    return;
  }
  if (event.key === "Delete") {
    event.preventDefault();
    event.stopPropagation();
    await deleteSelectedCodePaths(workspaceID, callbacks);
    return;
  }
  if (event.key !== "Enter") {
    return;
  }
  event.preventDefault();
  event.stopPropagation();
  await activateFocusedCodeBrowserRow(workspaceID, element, callbacks);
}

function focusAdjacentCodeBrowserRow(
  root: ParentNode,
  workspaceID: string,
  current: HTMLElement,
  direction: -1 | 1,
) {
  const rows = visibleCodeBrowserRows(root);
  if (!rows.length) {
    return;
  }
  const state = ensureCodeState(workspaceID);
  const currentIndex = rows.indexOf(current);
  const selectedIndex = rows.findIndex((row) => row.dataset.codePath === state.selectedPath);
  const index = currentIndex >= 0 ? currentIndex : selectedIndex;
  const nextIndex = Math.min(rows.length - 1, Math.max(0, (index >= 0 ? index : 0) + direction));
  focusCodeBrowserRow(workspaceID, rows[nextIndex]);
}

function visibleCodeBrowserRows(root: ParentNode) {
  return Array.from(root.querySelectorAll<HTMLElement>("[data-code-browser-row]")).filter((row) => row.offsetParent !== null);
}

function visibleCodeBrowserSelectionEntries(root: ParentNode): CodeTreeSelectionEntry[] {
  return visibleCodeBrowserRows(root)
    .map((row) => ({
      path: row.dataset.codePath ?? "",
      kind: row.dataset.codeKind ?? "",
    }))
    .filter((entry) => entry.path);
}

function selectCodeBrowserRowFromMouse(
  root: ParentNode,
  workspaceID: string,
  row: HTMLElement,
  event: MouseEvent,
): boolean {
  const path = row.dataset.codePath ?? "";
  const kind = row.dataset.codeKind ?? "";
  if (!path) {
    return false;
  }
  if (event.shiftKey) {
    selectCodeTreeEntry(workspaceID, path, kind, {
      mode: "range",
      rangeEntries: visibleCodeBrowserSelectionEntries(root),
      additiveRange: event.ctrlKey || event.metaKey,
    });
    return true;
  }
  if (event.ctrlKey || event.metaKey) {
    selectCodeTreeEntry(workspaceID, path, kind, { mode: "toggle" });
    return true;
  }
  selectCodeTreeEntry(workspaceID, path, kind);
  return false;
}

function updateCodeBrowserRowSelectionClasses(root: ParentNode, workspaceID: string) {
  const state = ensureCodeState(workspaceID);
  root.querySelectorAll<HTMLElement>("[data-code-browser-row]").forEach((row) => {
    const selected = isCodeTreeEntrySelected(state, row.dataset.codePath ?? "");
    row.classList.toggle("is-selected", selected);
    row.setAttribute("aria-selected", String(selected));
  });
}

function hasCodeSelectionModifier(event: MouseEvent): boolean {
  return event.shiftKey || event.ctrlKey || event.metaKey;
}

function focusCodeBrowserRow(workspaceID: string, row: HTMLElement) {
  selectCodeTreeEntry(workspaceID, row.dataset.codePath ?? "", row.dataset.codeKind ?? "");
  row.focus({ preventScroll: true });
  row.scrollIntoView({ block: "nearest" });
}

async function activateFocusedCodeBrowserRow(
  workspaceID: string,
  element: HTMLElement,
  callbacks: CodeViewCallbacks,
) {
  const path = element.dataset.codePath ?? "";
  const kind = element.dataset.codeKind ?? "";
  selectCodeTreeEntry(workspaceID, path, kind);
  if (kind === "directory") {
    await toggleFocusedDirectory(workspaceID, element, callbacks);
    return;
  }
  if (kind === "file") {
    ensureCodeState(workspaceID).explorerDrawerOpen = false;
    await openCodeFile(workspaceID, path, callbacks, { temporary: true });
  }
}

async function toggleFocusedDirectory(
  workspaceID: string,
  element: HTMLElement,
  callbacks: CodeViewCallbacks,
) {
  selectCodeTreeEntry(workspaceID, element.dataset.codePath ?? "", element.dataset.codeKind ?? "directory");
  await toggleDirectory(workspaceID, element.dataset.codePath ?? "", callbacks);
  focusSelectedCodeBrowserRow();
}

function focusSelectedCodeBrowserRow() {
  window.setTimeout(() => {
    const selected = document.querySelector<HTMLElement>("[data-code-tree] .code-tree-row.is-selected");
    selected?.focus({ preventScroll: true });
    selected?.scrollIntoView({ block: "nearest" });
  }, 0);
}

function isCodeDirectoryRow(element: HTMLElement) {
  return element.dataset.codeKind === "directory";
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
      const kind = element.dataset.codeKind ?? "";
      const state = ensureCodeState(workspaceID);
      if (!isCodeTreeEntrySelected(state, path)) {
        selectCodeTreeEntry(workspaceID, path, kind);
        updateCodeBrowserRowSelectionClasses(root, workspaceID);
      } else {
        state.selectedPath = path;
        state.selectedKind = kind === "file" || kind === "directory" ? kind : "other";
      }
      callbacks.showCodePathContextMenu(
        workspaceID,
        path,
        kind === "file" || kind === "directory" ? kind : "other",
        element.getAttribute("title") ?? path,
        event.clientX,
        event.clientY,
      );
    });
  });
}

function bindCodeBrowserRowDragEvents(root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) {
  root.querySelectorAll<HTMLElement>("[data-code-browser-row]").forEach((element) => {
    element.addEventListener("dragstart", (event) => {
      const path = element.dataset.codePath ?? "";
      const kind = element.dataset.codeKind ?? "";
      if (!startCodeDrag(workspaceID, path, kind)) {
        event.preventDefault();
        return;
      }
      event.stopPropagation();
      if (event.dataTransfer) {
        event.dataTransfer.effectAllowed = "move";
        event.dataTransfer.setData("text/plain", path);
      }
    });
    element.addEventListener("dragenter", (event) => {
      handleCodeDragTargetEvent(event, element, workspaceID, callbacks);
    });
    element.addEventListener("dragover", (event) => {
      handleCodeDragTargetEvent(event, element, workspaceID, callbacks);
    });
    element.addEventListener("drop", (event) => {
      if (!updateCodeDropTarget(
        workspaceID,
        element.dataset.codePath ?? "",
        element.dataset.codeKind ?? "",
        callbacks,
      )) {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      void dropCodeDrag(
        workspaceID,
        element.dataset.codePath ?? "",
        element.dataset.codeKind ?? "",
        callbacks,
      );
    });
    element.addEventListener("dragend", () => {
      clearCodeDrag(workspaceID, callbacks);
    });
  });
}

function handleCodeDragTargetEvent(
  event: DragEvent,
  element: HTMLElement,
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  if (!updateCodeDropTarget(
    workspaceID,
    element.dataset.codePath ?? "",
    element.dataset.codeKind ?? "",
    callbacks,
  )) {
    if (event.dataTransfer) {
      event.dataTransfer.dropEffect = "none";
    }
    return;
  }
  event.preventDefault();
  event.stopPropagation();
  if (event.dataTransfer) {
    event.dataTransfer.dropEffect = "move";
  }
}

function bindCodeCreateInputEvents(root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) {
  root.querySelectorAll<HTMLInputElement>("[data-code-create-input]").forEach((input) => {
    input.addEventListener("input", () => {
      updatePendingCodeCreateName(workspaceID, input.value);
    });
    input.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        event.stopPropagation();
        void submitPendingCodeCreate(workspaceID, input.value, callbacks);
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        cancelPendingCodeCreate(workspaceID, callbacks);
      }
    });
    input.addEventListener("blur", () => {
      cancelPendingCodeCreate(workspaceID, callbacks);
    });
  });
}

function bindCodeRenameInputEvents(root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) {
  root.querySelectorAll<HTMLInputElement>("[data-code-rename-input]").forEach((input) => {
    input.addEventListener("input", () => {
      updatePendingCodeRenameName(workspaceID, input.value);
    });
    input.addEventListener("keydown", (event) => {
      if (event.key === "Enter") {
        event.preventDefault();
        event.stopPropagation();
        void submitPendingCodeRename(workspaceID, input.value, callbacks);
        return;
      }
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        cancelPendingCodeRename(workspaceID, callbacks);
      }
    });
    input.addEventListener("blur", () => {
      cancelPendingCodeRename(workspaceID, callbacks);
    });
  });
}

async function handleCodeAction(
  target: HTMLElement,
  workspaceID: string,
  callbacks: CodeViewCallbacks,
  event: MouseEvent,
) {
  const action = target.dataset.codeAction ?? "";
  const path = target.dataset.codePath ?? "";
  if (action === "toggle-filter") {
    toggleIgnoredFilter(workspaceID, callbacks);
    if (ensureCodeState(workspaceID).textSearchOpen) {
      runTextSearchNow(workspaceID, callbacks);
    }
    return;
  }
  if (action === "open-explorer-drawer") {
    ensureCodeState(workspaceID).explorerDrawerOpen = true;
    callbacks.render();
    return;
  }
  if (action === "close-explorer-drawer") {
    ensureCodeState(workspaceID).explorerDrawerOpen = false;
    callbacks.render();
    return;
  }
  if (action === "open-inline-chat") {
    openInlineCodeChatAtCursor(workspaceID, callbacks);
    return;
  }
  if (action === "create-temporary-file") {
    createUntitledCodeFile(workspaceID, callbacks);
    return;
  }
  if (action === "toggle-temporary-files") {
    toggleTemporaryFiles(workspaceID, callbacks);
    return;
  }
  if (action === "open-text-search") {
    openTextSearch(workspaceID, callbacks);
    return;
  }
  if (action === "close-text-search") {
    closeTextSearch(workspaceID, callbacks);
    return;
  }
  if (action === "refresh-tree") {
    await refreshCodeTree(workspaceID, callbacks);
    return;
  }
  if (action === "create-selected-file") {
    await startSelectedCodeCreate(workspaceID, "file", callbacks);
    return;
  }
  if (action === "create-selected-folder") {
    await startSelectedCodeCreate(workspaceID, "folder", callbacks);
    return;
  }
  if (action === "collapse-tree") {
    collapseCodeTree(workspaceID, callbacks);
    return;
  }
  if (action === "toggle-directory") {
    if (hasCodeSelectionModifier(event)) {
      selectCodeBrowserRowFromMouse(
        target.closest("[data-code-tree]") ?? document,
        workspaceID,
        target,
        event,
      );
      updateCodeBrowserRowSelectionClasses(
        target.closest("[data-code-tree]") ?? document,
        workspaceID,
      );
      return;
    }
    selectCodeTreeEntry(workspaceID, path, target.dataset.codeKind ?? "directory");
    await toggleDirectory(workspaceID, path, callbacks);
    return;
  }
  if (action === "activate-tab" || action === "activate-switcher-tab") {
    activateCodeTab(workspaceID, path, callbacks);
    return;
  }
  if (action === "close-tab") {
    await closeCodeTab(workspaceID, path, callbacks);
    return;
  }
  if (action === "save-active-file") {
    await saveActiveCodeFile(workspaceID, callbacks);
  }
}

setCodeTreeEventBinder(bindCodeTreeEvents);
setCodeTextSearchEventBinder(bindCodeTextSearchEvents);
setCodeQuickOpenEventBinder(bindCodeQuickOpenResultEvents);
