import { services } from "../../wailsjs/go/models";
import { SearchWorkspaceText } from "../backend/services";
import { patchTextSearchPanel, patchTextSearchResults } from "./dom";
import { ensureCodeState } from "./state";
import { openCodeFile } from "./tabs";
import type { CodeViewCallbacks } from "./types";

const textSearchDelayMs = 120;

export function openTextSearch(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
  initialQuery = "",
) {
  const state = ensureCodeState(workspaceID);
  state.textSearchOpen = true;
  state.explorerDrawerOpen = true;
  state.textSearchFocusedField = "query";
  if (initialQuery) {
    state.textSearchQuery = initialQuery;
  }
  callbacks.render();
  focusTextSearchQuery();
  if (initialQuery) {
    runTextSearchNow(workspaceID, callbacks);
  }
}

export function closeTextSearch(workspaceID: string, callbacks: CodeViewCallbacks) {
  const state = ensureCodeState(workspaceID);
  state.textSearchOpen = false;
  state.textSearchFocusedField = "";
  state.textSearchRequestSeq++;
  state.textSearchStreamID = "";
  state.textSearchLoading = false;
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
  state.textSearchStreamID = "";
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
    revealInExplorer: false,
  });
}

function scheduleTextSearch(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
  delay: number,
) {
  const state = ensureCodeState(workspaceID);
  state.textSearchRequestSeq++;
  state.textSearchStreamID = "";
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
    const searchID = `${Date.now()}-${sequence}`;
    state.textSearchStreamID = searchID;
    const result = await SearchWorkspaceText(
      workspaceID,
      services.WorkspaceTextSearchRequest.createFrom({
        searchId: searchID,
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

export type WorkspaceTextSearchEvent = {
  workspaceId: string;
  searchId: string;
  type: "started" | "matches" | "complete";
  files?: services.WorkspaceTextSearchFileResult[];
  matchCount?: number;
  fileCount?: number;
  filesSearched?: number;
  filesSkipped?: number;
  truncated?: boolean;
  result?: services.WorkspaceTextSearchResult;
};

export function applyWorkspaceTextSearchEvent(
  event: WorkspaceTextSearchEvent,
  callbacks: CodeViewCallbacks,
) {
  const state = ensureCodeState(event.workspaceId);
  if (!event.searchId || event.searchId !== state.textSearchStreamID) {
    return;
  }
  if (event.type === "started" && event.result) {
    state.textSearchResult = services.WorkspaceTextSearchResult.createFrom(event.result);
    state.textSearchLoading = true;
    patchTextSearchResults(event.workspaceId, callbacks);
    return;
  }
  if (event.type === "matches") {
    const result = state.textSearchResult;
    if (!result) {
      return;
    }
    result.files = [
      ...(result.files ?? []),
      ...(event.files ?? []).map((file) =>
        services.WorkspaceTextSearchFileResult.createFrom(file),
      ),
    ];
    result.matchCount = event.matchCount ?? result.matchCount;
    result.fileCount = event.fileCount ?? result.files.length;
    result.filesSearched = event.filesSearched ?? result.filesSearched;
    result.filesSkipped = event.filesSkipped ?? result.filesSkipped;
    result.truncated = event.truncated ?? result.truncated;
    state.textSearchLoading = true;
    patchTextSearchResults(event.workspaceId, callbacks);
    return;
  }
  if (event.type === "complete" && event.result) {
    state.textSearchResult = services.WorkspaceTextSearchResult.createFrom(event.result);
    state.textSearchLoading = false;
    patchTextSearchResults(event.workspaceId, callbacks);
  }
}

function focusTextSearchQuery() {
  window.requestAnimationFrame(() => {
    const input = document.querySelector<HTMLInputElement>(
      '[data-code-text-search-field="query"]',
    );
    input?.focus({ preventScroll: true });
    input?.setSelectionRange(input.value.length, input.value.length);
  });
}

function textSearchField(value: string): "" | "query" | "include" | "exclude" {
  if (value === "query" || value === "include" || value === "exclude") {
    return value;
  }
  return "";
}
