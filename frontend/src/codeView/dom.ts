import { patchChildrenFromHtml } from "../markdown";
import { codeIcons } from "./icons";
import { renderCodeQuickOpenResults, renderFileList, renderTextSearchPanelContent } from "./render";
import { activeCodeTab, ensureCodeState, explorerWidthStorageKey, maxExplorerWidth, minExplorerWidth } from "./state";
import type { CodeFileTab, CodeViewCallbacks } from "./types";
import { clamp, codeTabName, formatBytes } from "./utils";

type CodeTreeEventBinder = (root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) => void;
type CodeTextSearchEventBinder = (root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) => void;
type CodeQuickOpenEventBinder = (root: ParentNode, workspaceID: string, callbacks: CodeViewCallbacks) => void;
let codeTreeEventBinder: CodeTreeEventBinder | null = null;
let codeTextSearchEventBinder: CodeTextSearchEventBinder | null = null;
let codeQuickOpenEventBinder: CodeQuickOpenEventBinder | null = null;

export function setCodeTreeEventBinder(binder: CodeTreeEventBinder) {
  codeTreeEventBinder = binder;
}

export function setCodeTextSearchEventBinder(binder: CodeTextSearchEventBinder) {
  codeTextSearchEventBinder = binder;
}

export function setCodeQuickOpenEventBinder(binder: CodeQuickOpenEventBinder) {
  codeQuickOpenEventBinder = binder;
}

export function patchCodeTree(workspaceID: string, callbacks: CodeViewCallbacks) {
  const tree = document.querySelector<HTMLElement>("[data-code-tree]");
  if (!tree) {
    callbacks.render();
    return;
  }
  tree.innerHTML = renderFileList(workspaceID);
  restoreCodeTreeScroll(workspaceID);
  codeTreeEventBinder?.(tree, workspaceID, callbacks);
}

export function scrollSelectedCodeTreeEntryIntoView() {
  const selected = document.querySelector<HTMLElement>("[data-code-tree] .code-tree-row.is-selected");
  selected?.scrollIntoView({ block: "nearest" });
}

export function patchDirtyUI(workspaceID: string, tab: CodeFileTab) {
  document.querySelectorAll<HTMLElement>("[data-code-tab]").forEach((element) => {
    if (element.dataset.codeTab !== tab.path) {
      return;
    }
    element.classList.toggle("is-dirty", tab.dirty);
    element.classList.toggle("is-temporary", tab.temporary);
    let dot = element.querySelector<HTMLElement>(".dirty-dot");
    if (tab.dirty && !dot) {
      dot = document.createElement("span");
      dot.className = "dirty-dot";
      dot.setAttribute("aria-label", "Unsaved changes");
      element.querySelector(".code-tab-main")?.appendChild(dot);
    }
    if (!tab.dirty) {
      dot?.remove();
    }
  });
  document.querySelectorAll<HTMLElement>("[data-code-untitled]").forEach((element) => {
    if (element.dataset.codeUntitled !== tab.path) {
      return;
    }
    let dot = element.querySelector<HTMLElement>(".dirty-dot");
    if (tab.dirty && !dot) {
      dot = document.createElement("span");
      dot.className = "dirty-dot";
      dot.setAttribute("aria-label", "Unsaved changes");
      element.querySelector(".code-temporary-file-main")?.appendChild(dot);
    }
    if (!tab.dirty) {
      dot?.remove();
    }
  });
  if (activeCodeTab(workspaceID)?.path !== tab.path) {
    return;
  }
  const save = document.querySelector<HTMLButtonElement>("[data-code-save]");
  if (save) {
    save.disabled = (!tab.untitled && !tab.dirty) || tab.saving;
    save.innerHTML = `${tab.saving ? `<span class="spinner" aria-hidden="true"></span>` : codeIcons.save}<span>Save</span>`;
  }
  const dirtySummary = document.querySelector<HTMLElement>("[data-code-dirty-summary]");
  if (dirtySummary) {
    const dirtyCount = ensureCodeState(workspaceID).tabs.filter((candidate) => candidate.dirty).length;
    dirtySummary.textContent = dirtyCount ? `${dirtyCount} unsaved` : "Files";
  }
  const status = document.querySelector<HTMLElement>("[data-code-status]");
  if (status) {
    const state = tab.saving
      ? "Saving"
      : tab.dirty
        ? "Unsaved changes"
        : tab.untitled
          ? "Temporary file"
          : "Saved";
    status.textContent = `${tab.untitled ? codeTabName(tab) : tab.path} - ${formatBytes(tab.bytes)} - ${state}`;
  }
}

export function patchInlineCodeChatResponse(
  workspaceID: string,
  path: string,
  requestID: string,
  responseHtml: string,
  onMissing: () => void,
) {
  const root =
    Array.from(document.querySelectorAll<HTMLElement>("[data-inline-code-chat]")).find((candidate) =>
      candidate.dataset.inlineWorkspaceId === workspaceID &&
      candidate.dataset.inlinePath === path &&
      candidate.dataset.inlineRequestId === requestID,
    ) ?? null;
  if (!root) {
    onMissing();
    return;
  }
  let response = root.querySelector<HTMLElement>("[data-inline-code-response]");
  if (!response) {
    response = document.createElement("div");
    response.className = "inline-code-chat-response markdown-body";
    response.dataset.inlineCodeResponse = "";
    root.append(response);
  }
  patchChildrenFromHtml(response, responseHtml);
}

export function captureCodeTreeScroll(workspaceID: string) {
  const tree = document.querySelector<HTMLElement>("[data-code-tree]");
  if (!tree) {
    return;
  }
  ensureCodeState(workspaceID).codeTreeScrollTop = tree.scrollTop;
}

export function restoreCodeTreeScroll(workspaceID: string) {
  const tree = document.querySelector<HTMLElement>("[data-code-tree]");
  if (!tree) {
    return;
  }
  tree.scrollTop = ensureCodeState(workspaceID).codeTreeScrollTop;
}

export function patchSearchResults(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  const state = ensureCodeState(workspaceID);
  state.preservingSearchFocus = state.searchFocused;
  patchCodeTree(workspaceID, callbacks);
  state.preservingSearchFocus = false;
}

export function patchTextSearchPanel(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  const panel = document.querySelector<HTMLElement>("[data-code-text-search-panel]");
  if (!panel) {
    callbacks.render();
    return;
  }
  const state = ensureCodeState(workspaceID);
  state.preservingTextSearchFocus = Boolean(state.textSearchFocusedField);
  panel.innerHTML = renderTextSearchPanelContent(workspaceID);
  codeTextSearchEventBinder?.(panel, workspaceID, callbacks);
  state.preservingTextSearchFocus = false;
}

export function patchQuickOpen(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  const results = document.querySelector<HTMLElement>("[data-code-quick-open-results]");
  if (!results) {
    callbacks.render();
    return;
  }
  results.innerHTML = renderCodeQuickOpenResults(workspaceID);
  const input = document.querySelector<HTMLInputElement>("[data-code-quick-open-input]");
  input?.focus();
  codeQuickOpenEventBinder?.(results, workspaceID, callbacks);
  scrollSelectedQuickOpenItemIntoView();
}

function scrollSelectedQuickOpenItemIntoView() {
  const selected = document.querySelector<HTMLElement>(".code-quick-open-item.is-selected");
  selected?.scrollIntoView({ block: "nearest" });
}

export function startExplorerResize(event: PointerEvent, workspaceID: string) {
  if (event.button !== 0) {
    return;
  }
  event.preventDefault();
  const state = ensureCodeState(workspaceID);
  const startX = event.clientX;
  const startWidth = state.explorerWidth;
  const workspace = document.querySelector<HTMLElement>(".code-workspace");
  const updateWidth = (nextWidth: number) => {
    state.explorerWidth = clamp(
      nextWidth,
      minExplorerWidth,
      Math.min(maxExplorerWidth, Math.max(minExplorerWidth, window.innerWidth - 420)),
    );
    workspace?.style.setProperty("--code-explorer-width", `${state.explorerWidth}px`);
  };
  const move = (moveEvent: PointerEvent) => {
    updateWidth(startWidth + moveEvent.clientX - startX);
  };
  const up = () => {
    localStorage.setItem(explorerWidthStorageKey, String(state.explorerWidth));
    window.removeEventListener("pointermove", move);
    window.removeEventListener("pointerup", up);
  };
  window.addEventListener("pointermove", move);
  window.addEventListener("pointerup", up, { once: true });
}
