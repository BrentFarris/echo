import { CreateWorkspaceFile, CreateWorkspaceFolder, ListWorkspaceDirectory, MoveWorkspacePath, SearchWorkspaceFiles } from "../backend/services";
import { services } from "../../wailsjs/go/models";
import { captureCodeTreeScroll, patchCodeTree, patchSearchResults } from "./dom";
import { rewriteCodeNavigationHistoryPaths } from "./navigation";
import { directoryStateFor, ensureCodeState } from "./state";
import { openPinnedCodeFile } from "./tabs";
import { saveMountedEditorContent } from "./editor";
import type { CodeCreateKind, CodeEntryKind, CodeViewCallbacks, CodeWorkspaceState } from "./types";

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

export function startCodeDrag(workspaceID: string, path: string, kind: string): boolean {
  const entryKind = normalizeCodeEntryKind(kind);
  if (!path || (entryKind !== "file" && entryKind !== "directory") || !sourceParentPath(path)) {
    return false;
  }
  const state = ensureCodeState(workspaceID);
  state.pendingCreate = null;
  state.drag = {
    sourcePath: path,
    sourceKind: entryKind,
    targetPath: "",
    targetParentPath: "",
    moving: false,
  };
  selectCodeTreeEntry(workspaceID, path, entryKind);
  return true;
}

export function updateCodeDropTarget(
  workspaceID: string,
  targetPath: string,
  targetKind: string,
  callbacks: CodeViewCallbacks,
): boolean {
  const state = ensureCodeState(workspaceID);
  const drag = state.drag;
  if (!drag || drag.moving) {
    return false;
  }
  const targetParentPath = createParentPath(targetPath, normalizeCodeEntryKind(targetKind));
  const valid = validMoveTarget(drag.sourcePath, drag.sourceKind, targetParentPath);
  const nextTargetPath = valid ? targetPath : "";
  const nextTargetParentPath = valid ? targetParentPath : "";
  if (drag.targetPath !== nextTargetPath || drag.targetParentPath !== nextTargetParentPath) {
    drag.targetPath = nextTargetPath;
    drag.targetParentPath = nextTargetParentPath;
    patchCodeTree(workspaceID, callbacks);
  }
  return valid;
}

export async function dropCodeDrag(
  workspaceID: string,
  targetPath: string,
  targetKind: string,
  callbacks: CodeViewCallbacks,
) {
  const state = ensureCodeState(workspaceID);
  const drag = state.drag;
  if (!drag || drag.moving) {
    return;
  }
  const targetParentPath = createParentPath(targetPath, normalizeCodeEntryKind(targetKind));
  if (!validMoveTarget(drag.sourcePath, drag.sourceKind, targetParentPath)) {
    clearCodeDrag(workspaceID, callbacks);
    return;
  }

  const sourcePath = drag.sourcePath;
  const sourceParent = sourceParentPath(sourcePath);
  drag.targetPath = targetPath;
  drag.targetParentPath = targetParentPath;
  drag.moving = true;
  saveMountedEditorContent();
  patchCodeTree(workspaceID, callbacks);

  try {
    const moved = services.WorkspaceFileEntry.createFrom(
      await MoveWorkspacePath(workspaceID, sourcePath, targetParentPath),
    );
    rewriteMovedCodePaths(state, sourcePath, moved.path);
    rewriteCodeNavigationHistoryPaths(workspaceID, sourcePath, moved.path);
    clearWorkspaceSearchState(workspaceID);
    pruneDirectoryCacheAfterMove(state, sourcePath, sourceParent, targetParentPath, moved.path);
    state.selectedPath = moved.path;
    state.selectedKind = normalizeCodeEntryKind(moved.kind);
    state.drag = null;
    directoryAncestors(targetParentPath).forEach((ancestor) => state.expandedPaths.add(ancestor));
    state.expandedPaths.add(targetParentPath);
    if (sourceParent) {
      await loadDirectory(workspaceID, sourceParent);
    }
    await loadDirectory(workspaceID, targetParentPath);
    callbacks.pushToast("Moved.", "success");
  } catch (error) {
    state.drag = null;
    callbacks.pushToast(callbacks.errorMessage(error), "error");
  } finally {
    patchCodeTree(workspaceID, callbacks);
  }
}

export function clearCodeDrag(workspaceID: string, callbacks: CodeViewCallbacks) {
  const state = ensureCodeState(workspaceID);
  if (!state.drag || state.drag.moving) {
    return;
  }
  state.drag = null;
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

function sourceParentPath(path: string): string {
  const slash = path.lastIndexOf("/");
  if (slash <= 0) {
    return "";
  }
  return path.slice(0, slash);
}

function validMoveTarget(sourcePath: string, sourceKind: CodeEntryKind, targetParentPath: string): boolean {
  if (!sourcePath || !targetParentPath) {
    return false;
  }
  if (targetParentPath === sourceParentPath(sourcePath)) {
    return false;
  }
  if (sourceKind === "directory" && (targetParentPath === sourcePath || targetParentPath.startsWith(`${sourcePath}/`))) {
    return false;
  }
  return true;
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

function rewriteMovedCodePaths(state: CodeWorkspaceState, sourcePath: string, destinationPath: string) {
  state.tabs.forEach((tab) => {
    tab.path = movedCodePath(tab.path, sourcePath, destinationPath);
  });
  state.activePath = movedCodePath(state.activePath, sourcePath, destinationPath);
  state.openingPath = movedCodePath(state.openingPath, sourcePath, destinationPath);
  state.selectedPath = movedCodePath(state.selectedPath, sourcePath, destinationPath);
  if (state.inlineChat) {
    state.inlineChat.path = movedCodePath(state.inlineChat.path, sourcePath, destinationPath);
  }
  state.tabMruPaths = uniqueMovedPaths(state.tabMruPaths, sourcePath, destinationPath);
  if (state.tabSwitcher) {
    state.tabSwitcher.paths = uniqueMovedPaths(state.tabSwitcher.paths, sourcePath, destinationPath);
    state.tabSwitcher.selectedIndex = Math.min(
      state.tabSwitcher.selectedIndex,
      Math.max(0, state.tabSwitcher.paths.length - 1),
    );
  }
}

function uniqueMovedPaths(paths: string[], sourcePath: string, destinationPath: string): string[] {
  const seen = new Set<string>();
  const next: string[] = [];
  paths.forEach((path) => {
    const moved = movedCodePath(path, sourcePath, destinationPath);
    if (!moved || seen.has(moved)) {
      return;
    }
    seen.add(moved);
    next.push(moved);
  });
  return next;
}

function movedCodePath(path: string, sourcePath: string, destinationPath: string): string {
  if (!path) {
    return path;
  }
  if (path === sourcePath) {
    return destinationPath;
  }
  if (path.startsWith(`${sourcePath}/`)) {
    return `${destinationPath}${path.slice(sourcePath.length)}`;
  }
  return path;
}

function pruneDirectoryCacheAfterMove(
  state: CodeWorkspaceState,
  sourcePath: string,
  sourceParentPath: string,
  targetParentPath: string,
  destinationPath: string,
) {
  const deletePrefixes = [sourcePath, destinationPath].filter(Boolean);
  Array.from(state.directories.keys()).forEach((path) => {
    if (
      path === sourceParentPath ||
      path === targetParentPath ||
      deletePrefixes.some((prefix) => path === prefix || path.startsWith(`${prefix}/`))
    ) {
      state.directories.delete(path);
    }
  });
  state.expandedPaths = new Set(
    Array.from(state.expandedPaths).filter(
      (path) => path !== sourcePath && !path.startsWith(`${sourcePath}/`),
    ),
  );
  state.expandedPaths.add(".");
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
