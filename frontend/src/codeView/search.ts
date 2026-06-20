import { services } from "../../wailsjs/go/models";
import { SearchWorkspaceText } from "../backend/services";
import { patchTextSearchPanel } from "./dom";
import { ensureCodeState } from "./state";
import { openCodeFile } from "./tabs";
import type { CodeViewCallbacks } from "./types";

const textSearchDelayMs = 220;

export function openTextSearch(workspaceID: string, callbacks: CodeViewCallbacks) {
  const state = ensureCodeState(workspaceID);
  state.textSearchOpen = true;
  state.explorerDrawerOpen = true;
  state.textSearchFocusedField = "query";
  callbacks.render();
}

export function closeTextSearch(workspaceID: string, callbacks: CodeViewCallbacks) {
  const state = ensureCodeState(workspaceID);
  state.textSearchOpen = false;
  state.textSearchFocusedField = "";
  callbacks.render();
}

export function handleTextSearchFieldInput(
  workspaceID: string,
  input: HTMLInputElement,
  callbacks: CodeViewCallbacks,
) {
  const state = ensureCodeState(workspaceID);
  const field = textSearchField(input.dataset.codeTextSearchField ?? "");
  if (!field) {
    return;
  }
  state.textSearchFocusedField = field;
  if (field === "query") {
    state.textSearchQuery = input.value;
  } else if (field === "include") {
    state.textSearchInclude = input.value;
  } else {
    state.textSearchExclude = input.value;
  }
  scheduleTextSearch(workspaceID, callbacks, textSearchDelayMs);
}

export function toggleTextSearchOption(
  workspaceID: string,
  option: string,
  callbacks: CodeViewCallbacks,
) {
  const state = ensureCodeState(workspaceID);
  if (option === "regex") {
    state.textSearchRegex = !state.textSearchRegex;
  } else if (option === "case") {
    state.textSearchCaseSensitive = !state.textSearchCaseSensitive;
  } else if (option === "word") {
    state.textSearchWholeWord = !state.textSearchWholeWord;
  } else {
    return;
  }
  state.textSearchFocusedField = "query";
  scheduleTextSearch(workspaceID, callbacks, 0);
}

export function runTextSearchNow(workspaceID: string, callbacks: CodeViewCallbacks) {
  const state = ensureCodeState(workspaceID);
  state.textSearchRequestSeq++;
  if (state.textSearchTimerID !== null) {
    window.clearTimeout(state.textSearchTimerID);
    state.textSearchTimerID = null;
  }
  void runTextSearch(workspaceID, callbacks, state.textSearchRequestSeq);
}

export async function openTextSearchMatch(
  workspaceID: string,
  element: HTMLElement,
  callbacks: CodeViewCallbacks,
) {
  const path = element.dataset.codeTextSearchPath ?? "";
  const offset = Number(element.dataset.codeTextSearchOffset ?? "");
  if (!path || !Number.isFinite(offset)) {
    return;
  }
  ensureCodeState(workspaceID).explorerDrawerOpen = false;
  await openCodeFile(workspaceID, path, callbacks, {
    temporary: true,
    selectionPosition: offset,
  });
}

function scheduleTextSearch(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
  delay: number,
) {
  const state = ensureCodeState(workspaceID);
  state.textSearchRequestSeq++;
  if (state.textSearchTimerID !== null) {
    window.clearTimeout(state.textSearchTimerID);
    state.textSearchTimerID = null;
  }
  if (!state.textSearchQuery) {
    state.textSearchResult = null;
    state.textSearchError = "";
    state.textSearchLoading = false;
    patchTextSearchPanel(workspaceID, callbacks);
    return;
  }
  state.textSearchLoading = true;
  state.textSearchError = "";
  patchTextSearchPanel(workspaceID, callbacks);
  const sequence = state.textSearchRequestSeq;
  state.textSearchTimerID = window.setTimeout(() => {
    void runTextSearch(workspaceID, callbacks, sequence);
  }, delay);
}

async function runTextSearch(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
  sequence: number,
) {
  const state = ensureCodeState(workspaceID);
  if (!state.textSearchQuery) {
    state.textSearchLoading = false;
    state.textSearchResult = null;
    state.textSearchError = "";
    patchTextSearchPanel(workspaceID, callbacks);
    return;
  }
  state.textSearchLoading = true;
  state.textSearchError = "";
  patchTextSearchPanel(workspaceID, callbacks);
  try {
    const result = await SearchWorkspaceText(
      workspaceID,
      services.WorkspaceTextSearchRequest.createFrom({
        query: state.textSearchQuery,
        regex: state.textSearchRegex,
        caseSensitive: state.textSearchCaseSensitive,
        wholeWord: state.textSearchWholeWord,
        include: state.textSearchInclude,
        exclude: state.textSearchExclude,
        includeIgnored: state.showIgnored,
      }),
    );
    if (sequence !== state.textSearchRequestSeq) {
      return;
    }
    state.textSearchResult = services.WorkspaceTextSearchResult.createFrom(result);
  } catch (error) {
    if (sequence === state.textSearchRequestSeq) {
      state.textSearchResult = null;
      state.textSearchError = callbacks.errorMessage(error);
    }
  } finally {
    if (sequence === state.textSearchRequestSeq) {
      state.textSearchLoading = false;
      state.textSearchTimerID = null;
      patchTextSearchPanel(workspaceID, callbacks);
    }
  }
}

function textSearchField(value: string): "" | "query" | "include" | "exclude" {
  if (value === "query" || value === "include" || value === "exclude") {
    return value;
  }
  return "";
}
