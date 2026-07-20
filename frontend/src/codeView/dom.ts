import { patchChildrenFromHtml } from "../markdown";
import { codeIcons } from "./icons";
import { renderCodeQuickOpenResults, renderFileList, renderTextSearchResults } from "./render";
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
    status.textContent = `${tab.external ? "External file - " : ""}${tab.untitled ? codeTabName(tab) : tab.path} - ${formatBytes(tab.bytes)} - ${state}`;
  }
}

export function patchInlineCodeChatOutput(
  workspaceID: string,
  path: string,
  requestID: string,
  outputHtml: string,
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
  let output = root.querySelector<HTMLElement>("[data-inline-code-output]");
  if (!output) {
    output = document.createElement("div");
    output.className = "inline-code-chat-output";
    output.dataset.inlineCodeOutput = "";
    root.append(output);
  }
  const openSections = new Set(
    Array.from(output.querySelectorAll<HTMLDetailsElement>("details[data-debug-section]"))
      .filter((section) => section.open)
      .map((section) => section.dataset.debugSection ?? ""),
  );
  patchChildrenFromHtml(output, outputHtml);
  output.querySelectorAll<HTMLDetailsElement>("details[data-debug-section]").forEach((section) => {
    section.open = openSections.has(section.dataset.debugSection ?? "");
  });
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
  panel.querySelectorAll<HTMLButtonElement>("[data-code-text-search-option]").forEach((button) => {
    const option = button.dataset.codeTextSearchOption ?? "";
    const active = option === "regex"
      ? state.textSearchRegex
      : option === "case"
        ? state.textSearchCaseSensitive
        : option === "word"
          ? state.textSearchWholeWord
          : false;
    button.classList.toggle("is-active", active);
    button.setAttribute("aria-pressed", String(active));
  });
  syncTextSearchField(panel, "query", state.textSearchQuery);
  syncTextSearchField(panel, "include", state.textSearchInclude);
  syncTextSearchField(panel, "exclude", state.textSearchExclude);
  patchTextSearchResults(workspaceID, callbacks);
}

function syncTextSearchField(
  panel: HTMLElement,
  field: "query" | "include" | "exclude",
  value: string,
) {
  const input = panel.querySelector<HTMLInputElement>(
    `[data-code-text-search-field="${field}"]`,
  );
  if (input && input.value !== value) {
    input.value = value;
  }
}

export function patchTextSearchResults(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  const results = document.querySelector<HTMLElement>("[data-code-text-search-results]");
  if (!results) {
    callbacks.render();
    return;
  }
  results.innerHTML = renderTextSearchResults(workspaceID);
  codeTextSearchEventBinder?.(results, workspaceID, callbacks);
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
