import { ListWorkspaceDirectory, SearchWorkspaceFiles } from "../../wailsjs/go/services/SystemService";
import { services } from "../../wailsjs/go/models";
import { captureCodeTreeScroll, patchCodeTree, patchSearchResults } from "./dom";
import { directoryStateFor, ensureCodeState } from "./state";
import type { CodeViewCallbacks } from "./types";

export async function ensureCodeViewRootLoaded(workspaceID: string) {
  const root = directoryStateFor(ensureCodeState(workspaceID), ".");
  if (root.loaded || root.loading) {
    return;
  }
  await loadDirectory(workspaceID, ".");
}

export function toggleIgnoredFilter(workspaceID: string, callbacks: CodeViewCallbacks) {
  const state = ensureCodeState(workspaceID);
  state.showIgnored = !state.showIgnored;
  scheduleWorkspaceSearch(workspaceID, callbacks, 0);
  callbacks.render();
}

export async function refreshCodeTree(workspaceID: string, callbacks: CodeViewCallbacks) {
  captureCodeTreeScroll(workspaceID);
  const state = ensureCodeState(workspaceID);
  state.directories.clear();
  state.expandedPaths = new Set(["."]);
  patchCodeTree(workspaceID, callbacks);
  await loadDirectory(workspaceID, ".");
  patchCodeTree(workspaceID, callbacks);
}

export async function toggleDirectory(workspaceID: string, path: string, callbacks: CodeViewCallbacks) {
  captureCodeTreeScroll(workspaceID);
  const state = ensureCodeState(workspaceID);
  if (state.expandedPaths.has(path)) {
    state.expandedPaths.delete(path);
    patchCodeTree(workspaceID, callbacks);
    return;
  }
  state.expandedPaths.add(path);
  patchCodeTree(workspaceID, callbacks);
  await loadDirectory(workspaceID, path);
  patchCodeTree(workspaceID, callbacks);
}

export function handleSearchInput(
  workspaceID: string,
  input: HTMLInputElement,
  callbacks: CodeViewCallbacks,
) {
  captureCodeTreeScroll(workspaceID);
  const state = ensureCodeState(workspaceID);
  state.searchFocused = true;
  state.searchQuery = input.value;
  state.searchRequestSeq++;
  if (state.searchTimerID !== null) {
    window.clearTimeout(state.searchTimerID);
    state.searchTimerID = null;
  }
  if (!state.searchQuery.trim()) {
    state.searchResults = [];
    state.searchLoading = false;
    state.searchTruncated = false;
    patchSearchResults(workspaceID, callbacks);
    return;
  }
  state.searchLoading = true;
  patchSearchResults(workspaceID, callbacks);
  state.searchTimerID = window.setTimeout(() => {
    void runWorkspaceSearch(workspaceID, callbacks);
  }, 180);
}

export function scheduleWorkspaceSearch(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
  delay: number,
) {
  const state = ensureCodeState(workspaceID);
  if (!state.searchQuery.trim()) {
    return;
  }
  state.searchRequestSeq++;
  state.searchLoading = true;
  if (state.searchTimerID !== null) {
    window.clearTimeout(state.searchTimerID);
  }
  state.searchTimerID = window.setTimeout(() => {
    void runWorkspaceSearch(workspaceID, callbacks);
  }, delay);
}

async function runWorkspaceSearch(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  const state = ensureCodeState(workspaceID);
  const query = state.searchQuery.trim();
  const sequence = state.searchRequestSeq;
  if (!query) {
    state.searchLoading = false;
    state.searchResults = [];
    state.searchTruncated = false;
    patchSearchResults(workspaceID, callbacks);
    return;
  }
  patchSearchResults(workspaceID, callbacks);
  try {
    const result = await SearchWorkspaceFiles(
      workspaceID,
      query,
      state.showIgnored,
    );
    if (sequence !== state.searchRequestSeq) {
      return;
    }
    const model = services.WorkspaceFileSearchResult.createFrom(result);
    state.searchResults = model.entries ?? [];
    state.searchTruncated = model.truncated;
  } catch (error) {
    if (sequence === state.searchRequestSeq) {
      callbacks.pushToast(callbacks.errorMessage(error), "error");
      state.searchResults = [];
      state.searchTruncated = false;
    }
  } finally {
    if (sequence === state.searchRequestSeq) {
      state.searchLoading = false;
      state.searchTimerID = null;
      patchSearchResults(workspaceID, callbacks);
    }
  }
}

export async function loadDirectory(workspaceID: string, path: string) {
  const state = ensureCodeState(workspaceID);
  const directory = directoryStateFor(state, path);
  if (directory.loading) {
    return;
  }
  directory.loading = true;
  directory.error = "";
  try {
    const loaded = await ListWorkspaceDirectory(workspaceID, path);
    const model = services.WorkspaceDirectory.createFrom(loaded);
    const target = directoryStateFor(state, model.path);
    target.entries = model.entries ?? [];
    target.loaded = true;
    target.error = "";
  } catch (error) {
    directory.error = error instanceof Error ? error.message : String(error);
  } finally {
    directory.loading = false;
  }
}
