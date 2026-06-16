import { CreateWorkspaceFile, CreateWorkspaceFolder, ListWorkspaceDirectory, SearchWorkspaceFiles } from "../../wailsjs/go/services/SystemService";
import { services } from "../../wailsjs/go/models";
import { captureCodeTreeScroll, patchCodeTree, patchSearchResults } from "./dom";
import { directoryStateFor, ensureCodeState } from "./state";
import { openPinnedCodeFile } from "./tabs";
import type { CodeCreateKind, CodeEntryKind, CodeViewCallbacks } from "./types";

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
  state.pendingCreate = null;
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

export function selectCodeTreeEntry(workspaceID: string, path: string, kind: string) {
  if (!path) {
    return;
  }
  const state = ensureCodeState(workspaceID);
  state.selectedPath = path;
  state.selectedKind = normalizeCodeEntryKind(kind);
}

export async function startSelectedCodeCreate(
  workspaceID: string,
  createKind: CodeCreateKind,
  callbacks: CodeViewCallbacks,
) {
  const state = ensureCodeState(workspaceID);
  if (!state.selectedPath) {
    callbacks.pushToast("Select a file or folder first.", "error");
    return;
  }
  await startCodeCreate(
    workspaceID,
    state.selectedPath,
    state.selectedKind,
    createKind,
    callbacks,
  );
}

export async function startCodeCreate(
  workspaceID: string,
  path: string,
  kind: string,
  createKind: CodeCreateKind,
  callbacks: CodeViewCallbacks,
) {
  const state = ensureCodeState(workspaceID);
  const entryKind = normalizeCodeEntryKind(kind);
  const parentPath = createParentPath(path, entryKind);
  if (!parentPath) {
    callbacks.pushToast("Select a workspace folder before creating files.", "error");
    return;
  }

  captureCodeTreeScroll(workspaceID);
  selectCodeTreeEntry(workspaceID, path, entryKind);
  clearWorkspaceSearchState(workspaceID);
  state.pendingCreate = {
    kind: createKind,
    parentPath,
    name: "",
    submitting: false,
    error: "",
  };

  const ancestors = directoryAncestors(parentPath);
  ancestors.forEach((ancestor) => state.expandedPaths.add(ancestor));
  for (const ancestor of ancestors) {
    await loadDirectory(workspaceID, ancestor);
  }
  callbacks.render();
  focusPendingCreateInput();
}

export function collapseCodeTree(workspaceID: string, callbacks: CodeViewCallbacks) {
  const state = ensureCodeState(workspaceID);
  captureCodeTreeScroll(workspaceID);
  state.expandedPaths = new Set(["."]);
  state.pendingCreate = null;
  patchCodeTree(workspaceID, callbacks);
}

export function updatePendingCodeCreateName(workspaceID: string, name: string) {
  const pending = ensureCodeState(workspaceID).pendingCreate;
  if (!pending || pending.submitting) {
    return;
  }
  pending.name = name;
}

export function cancelPendingCodeCreate(workspaceID: string, callbacks: CodeViewCallbacks) {
  const state = ensureCodeState(workspaceID);
  if (!state.pendingCreate || state.pendingCreate.submitting) {
    return;
  }
  state.pendingCreate = null;
  patchCodeTree(workspaceID, callbacks);
}

export async function submitPendingCodeCreate(
  workspaceID: string,
  name: string,
  callbacks: CodeViewCallbacks,
) {
  const state = ensureCodeState(workspaceID);
  const pending = state.pendingCreate;
  if (!pending || pending.submitting) {
    return;
  }
  pending.name = name.trim();
  if (!pending.name) {
    callbacks.pushToast("Name is required.", "error");
    focusPendingCreateInput();
    return;
  }

  pending.submitting = true;
  pending.error = "";
  patchCodeTree(workspaceID, callbacks);
  try {
    if (pending.kind === "file") {
      const created = services.WorkspaceFile.createFrom(
        await CreateWorkspaceFile(workspaceID, pending.parentPath, pending.name),
      );
      state.pendingCreate = null;
      await loadDirectory(workspaceID, pending.parentPath);
      state.selectedPath = created.path;
      state.selectedKind = "file";
      patchCodeTree(workspaceID, callbacks);
      await openPinnedCodeFile(workspaceID, created.path, callbacks);
      callbacks.pushToast("File created.", "success");
      return;
    }

    const created = services.WorkspaceFileEntry.createFrom(
      await CreateWorkspaceFolder(workspaceID, pending.parentPath, pending.name),
    );
    state.pendingCreate = null;
    await loadDirectory(workspaceID, pending.parentPath);
    state.selectedPath = created.path;
    state.selectedKind = normalizeCodeEntryKind(created.kind);
    patchCodeTree(workspaceID, callbacks);
    callbacks.pushToast("Folder created.", "success");
  } catch (error) {
    pending.submitting = false;
    pending.error = callbacks.errorMessage(error);
    callbacks.pushToast(pending.error, "error");
    patchCodeTree(workspaceID, callbacks);
    focusPendingCreateInput();
  }
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

function normalizeCodeEntryKind(kind: string): CodeEntryKind {
  if (kind === "file" || kind === "directory") {
    return kind;
  }
  return "other";
}

function createParentPath(path: string, kind: CodeEntryKind): string {
  if (!path) {
    return "";
  }
  if (kind === "directory") {
    return path;
  }
  const slash = path.lastIndexOf("/");
  if (slash <= 0) {
    return "";
  }
  return path.slice(0, slash);
}

function directoryAncestors(path: string): string[] {
  const cleanPath = path.trim().replaceAll("\\", "/").replace(/^\/+|\/+$/g, "");
  if (!cleanPath || cleanPath === ".") {
    return ["."];
  }
  const ancestors = ["."];
  let current = "";
  cleanPath.split("/").forEach((part) => {
    current = current ? `${current}/${part}` : part;
    ancestors.push(current);
  });
  return ancestors;
}

function clearWorkspaceSearchState(workspaceID: string) {
  const state = ensureCodeState(workspaceID);
  if (state.searchTimerID !== null) {
    window.clearTimeout(state.searchTimerID);
    state.searchTimerID = null;
  }
  state.searchQuery = "";
  state.searchResults = [];
  state.searchLoading = false;
  state.searchTruncated = false;
  state.searchFocused = false;
  state.preservingSearchFocus = false;
}

function focusPendingCreateInput() {
  window.setTimeout(() => {
    const input = document.querySelector<HTMLInputElement>("[data-code-create-input]");
    if (!input) {
      return;
    }
    input.focus();
    input.select();
  }, 0);
}
