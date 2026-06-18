import { ReadWorkspaceFile, SaveWorkspaceFile } from "../../wailsjs/go/services/SystemService";
import { services } from "../../wailsjs/go/models";
import { captureCodeTreeScroll, patchDirtyUI } from "./dom";
import { applySavedFile, activeCodeTab, codeStates, ensureCodeState, findTab, promoteTabMruPath, pruneTabMruPaths, removeTabMruPath, tabSwitcherPaths, workspaceFileChanged, wrapIndex } from "./state";
import type { CodeFileTab, CodeViewCallbacks } from "./types";
import { clamp, editableWorkspaceFile, fileContentOffsetToEditorPosition, sleep } from "./utils";
import { replaceMountedEditorContent, saveMountedEditorContent } from "./editor";

const openTabFileWatchIntervalMs = 1500;
let openTabFileWatchTimerID: number | null = null;
let openTabFileWatchRunning = false;
let openTabFileWatchCallbacks: CodeViewCallbacks | null = null;
const openTabFileWatchErrors = new Map<string, string>();

export async function refreshOpenCodeTabsFromDisk(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  if (!workspaceID) {
    return;
  }
  startOpenTabFileWatch(callbacks);
  await reloadOpenCodeTabsFromDisk([workspaceID], callbacks);
}

export function startOpenTabFileWatch(callbacks: CodeViewCallbacks) {
  openTabFileWatchCallbacks = callbacks;
  if (openTabFileWatchTimerID !== null) {
    return;
  }
  openTabFileWatchTimerID = window.setInterval(() => {
    const latestCallbacks = openTabFileWatchCallbacks;
    if (!latestCallbacks) {
      return;
    }
    void reloadOpenCodeTabsFromDisk(watchedOpenTabWorkspaceIDs(), latestCallbacks);
  }, openTabFileWatchIntervalMs);
}

function stopOpenTabFileWatch() {
  if (openTabFileWatchTimerID !== null) {
    window.clearInterval(openTabFileWatchTimerID);
    openTabFileWatchTimerID = null;
  }
  openTabFileWatchRunning = false;
}

function watchedOpenTabWorkspaceIDs() {
  const workspaceIDs: string[] = [];
  codeStates.forEach((state, workspaceID) => {
    if (state.tabs.length > 0) {
      workspaceIDs.push(workspaceID);
    }
  });
  return workspaceIDs;
}

async function reloadOpenCodeTabsFromDisk(
  workspaceIDs: string[],
  callbacks: CodeViewCallbacks,
) {
  if (openTabFileWatchRunning) {
    return;
  }
  const uniqueWorkspaceIDs = [...new Set(workspaceIDs.filter(Boolean))];
  if (uniqueWorkspaceIDs.length === 0) {
    stopOpenTabFileWatch();
    return;
  }

  openTabFileWatchRunning = true;
  try {
    saveMountedEditorContent();
    for (const workspaceID of uniqueWorkspaceIDs) {
      await reloadWorkspaceOpenCodeTabsFromDisk(workspaceID, callbacks);
    }
  } finally {
    openTabFileWatchRunning = false;
    if (watchedOpenTabWorkspaceIDs().length === 0) {
      stopOpenTabFileWatch();
    }
  }
}

async function reloadWorkspaceOpenCodeTabsFromDisk(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  const state = codeStates.get(workspaceID);
  if (!state || state.tabs.length === 0) {
    return;
  }

  const openPaths = state.tabs.map((tab) => tab.path);
  for (const path of openPaths) {
    const tab = findTab(workspaceID, path);
    if (!tab || tab.dirty || tab.saving) {
      continue;
    }

    try {
      const file = services.WorkspaceFile.createFrom(
        await ReadWorkspaceFile(workspaceID, path),
      );
      const latest = findTab(workspaceID, path);
      if (!latest || latest.dirty || latest.saving) {
        continue;
      }
      openTabFileWatchErrors.delete(openTabFileWatchKey(workspaceID, path));
      if (!workspaceFileChanged(latest, file)) {
        continue;
      }
      applySavedFile(workspaceID, file);
      const reloadedTab = findTab(workspaceID, path);
      replaceMountedEditorContent(
        workspaceID,
        path,
        reloadedTab?.content ?? editableWorkspaceFile(file).content,
      );
    } catch (error) {
      const latest = findTab(workspaceID, path);
      if (!latest || latest.dirty) {
        continue;
      }
      const message = callbacks.errorMessage(error);
      const key = openTabFileWatchKey(workspaceID, path);
      if (openTabFileWatchErrors.get(key) === message) {
        continue;
      }
      openTabFileWatchErrors.set(key, message);
      callbacks.pushToast(`Could not reload ${path}: ${message}`, "error");
    }
  }
}

function openTabFileWatchKey(workspaceID: string, path: string) {
  return `${workspaceID}\u0000${path}`;
}

export function handleCodeTabSwitcherKeydown(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
  event: KeyboardEvent,
): boolean {
  if (!event.ctrlKey || event.altKey || event.key !== "Tab") {
    return false;
  }
  event.preventDefault();
  event.stopPropagation();
  saveMountedEditorContent();

  const state = ensureCodeState(workspaceID);
  pruneTabMruPaths(state);
  if (state.tabs.length <= 1) {
    state.tabSwitcher = null;
    return true;
  }

  const direction = event.shiftKey ? -1 : 1;
  if (!state.tabSwitcher) {
    const paths = tabSwitcherPaths(state);
    const activeIndex = Math.max(0, paths.indexOf(state.activePath));
    const selectedIndex = wrapIndex(activeIndex + direction, paths.length);
    state.tabSwitcher = { paths, selectedIndex };
    state.activePath = paths[selectedIndex] ?? state.activePath;
    callbacks.render();
    return true;
  }

  state.tabSwitcher.selectedIndex = wrapIndex(
    state.tabSwitcher.selectedIndex + direction,
    state.tabSwitcher.paths.length,
  );
  state.activePath =
    state.tabSwitcher.paths[state.tabSwitcher.selectedIndex] ?? state.activePath;
  callbacks.render();
  return true;
}

export function finishCodeTabSwitcher(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
): boolean {
  const state = ensureCodeState(workspaceID);
  if (!state.tabSwitcher) {
    return false;
  }
  const activePath = state.activePath;
  state.tabSwitcher = null;
  promoteTabMruPath(state, activePath);
  callbacks.render();
  return true;
}

export function clearCodeTabSwitcher(workspaceID: string) {
  ensureCodeState(workspaceID).tabSwitcher = null;
}

export async function saveActiveCodeFile(workspaceID: string, callbacks: CodeViewCallbacks) {
  saveMountedEditorContent();
  const tab = activeCodeTab(workspaceID);
  if (!tab || tab.saving || !tab.dirty) {
    return;
  }
  tab.saving = true;
  patchDirtyUI(workspaceID, tab);
  try {
    const saved = await SaveWorkspaceFile(
      workspaceID,
      tab.path,
      tab.content,
      tab.modifiedAt,
    );
    applySavedFile(workspaceID, services.WorkspaceFile.createFrom(saved));
    callbacks.pushToast("File saved.", "success");
  } catch (error) {
    callbacks.pushToast(callbacks.errorMessage(error), "error");
  } finally {
    const latest = findTab(workspaceID, tab.path);
    if (latest) {
      latest.saving = false;
      patchDirtyUI(workspaceID, latest);
    }
  }
}

export async function openCodeFile(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
  options: { temporary: boolean; selectionPosition?: number },
) {
  if (!path) {
    return;
  }
  captureCodeTreeScroll(workspaceID);
  saveMountedEditorContent();
  const state = ensureCodeState(workspaceID);
  if (state.openingPath === path) {
    return;
  }
  const existing = findTab(workspaceID, path);
  if (existing) {
    applyCodeTabSelection(existing, options.selectionPosition);
    activateCodeTab(workspaceID, existing.path, callbacks);
    return;
  }

  const temporaryIndex = state.tabs.findIndex((tab) => tab.temporary && !tab.dirty);
  const replacedTemporaryPath =
    options.temporary && temporaryIndex >= 0
      ? state.tabs[temporaryIndex]?.path ?? ""
      : "";
  state.openingPath = path;
  callbacks.render();
  try {
    const file = await ReadWorkspaceFile(workspaceID, path);
    const opened = services.WorkspaceFile.createFrom(file);
    const editable = editableWorkspaceFile(opened);
    const nextTab: CodeFileTab = {
      path: opened.path,
      content: editable.content,
      savedContent: editable.content,
      lineSeparator: editable.lineSeparator,
      bytes: editable.bytes,
      modifiedAt: opened.modifiedAt,
      dirty: false,
      saving: false,
      temporary: options.temporary,
      selectionAnchor: 0,
      selectionHead: 0,
      scrollTop: 0,
      scrollLeft: 0,
    };
    applyCodeTabSelection(nextTab, options.selectionPosition);
    if (options.temporary && temporaryIndex >= 0) {
      state.tabs[temporaryIndex] = nextTab;
      removeTabMruPath(state, replacedTemporaryPath);
    } else {
      state.tabs.push(nextTab);
    }
    state.activePath = opened.path;
    promoteTabMruPath(state, opened.path);
  } catch (error) {
    callbacks.pushToast(callbacks.errorMessage(error), "error");
  } finally {
    state.openingPath = "";
    callbacks.render();
  }
}

function applyCodeTabSelection(tab: CodeFileTab, position: number | undefined) {
  if (position === undefined) {
    return;
  }
  const target = fileContentOffsetToEditorPosition(tab.content, tab.lineSeparator, position);
  tab.selectionAnchor = target;
  tab.selectionHead = target;
  tab.pendingRevealPosition = target;
}

export async function openPinnedCodeFile(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  if (!path) {
    return;
  }
  const state = ensureCodeState(workspaceID);
  if (state.openingPath === path) {
    await waitForOpeningPath(workspaceID, path);
    const opened = findTab(workspaceID, path);
    if (opened) {
      opened.temporary = false;
      activateCodeTab(workspaceID, opened.path, callbacks);
    }
    return;
  }
  const existing = findTab(workspaceID, path);
  if (existing) {
    existing.temporary = false;
    activateCodeTab(workspaceID, existing.path, callbacks);
    return;
  }
  await openCodeFile(workspaceID, path, callbacks, { temporary: false });
}

export async function openWorkspaceCodeFile(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  await openCodeFile(workspaceID, path, callbacks, { temporary: false });
}

export function closeCodeTab(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  saveMountedEditorContent();
  const state = ensureCodeState(workspaceID);
  const index = state.tabs.findIndex((tab) => tab.path === path);
  if (index < 0) {
    return;
  }
  const tab = state.tabs[index];
  if (tab.dirty && !window.confirm(`Close ${tab.path} with unsaved changes?`)) {
    return;
  }
  state.tabs.splice(index, 1);
  removeTabMruPath(state, path);
  if (state.activePath === path) {
    state.activePath =
      state.tabs[Math.max(0, index - 1)]?.path ?? state.tabs[0]?.path ?? "";
    promoteTabMruPath(state, state.activePath);
  }
  state.tabSwitcher = null;
  pruneTabMruPaths(state);
  callbacks.render();
}

export function pinCodeTab(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  const tab = findTab(workspaceID, path);
  if (!tab || !tab.temporary) {
    return;
  }
  tab.temporary = false;
  callbacks.render();
}

async function waitForOpeningPath(workspaceID: string, path: string) {
  for (let attempt = 0; attempt < 20; attempt++) {
    if (ensureCodeState(workspaceID).openingPath !== path) {
      return;
    }
    await sleep(25);
  }
}

export function activateCodeTab(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  const state = ensureCodeState(workspaceID);
  if (!path || !state.tabs.some((tab) => tab.path === path)) {
    return;
  }
  saveMountedEditorContent();
  state.tabSwitcher = null;
  state.activePath = path;
  promoteTabMruPath(state, path);
  callbacks.render();
}
