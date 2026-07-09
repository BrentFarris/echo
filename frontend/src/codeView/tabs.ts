import { ChooseWorkspaceFileSavePath, ReadExternalTextFile, ReadWorkspaceFile, ReadWorkspaceMediaFile, ResolveWorkspaceTextFilePath, SaveExternalTextFile, SaveWorkspaceFile, SaveWorkspaceFileAs } from "../backend/services";
import { services } from "../../wailsjs/go/models";
import { captureCodeTreeScroll, patchDirtyUI } from "./dom";
import { applySavedFile, activeCodeTab, codeStates, ensureCodeState, findTab, promoteTabMruPath, pruneTabMruPaths, removeTabMruPath, tabSwitcherPaths, workspaceFileChanged, wrapIndex } from "./state";
import {
  applyCodeNavigationLocationToTab,
  captureActiveCodeNavigationLocation,
  codeNavigationLocationFromTab,
  commitCodeNavigationHistoryIndex,
  peekCodeNavigationHistoryTarget,
  recordCodeNavigationTransition,
  removeCodeNavigationHistoryEntry,
  rewriteCodeNavigationHistoryPaths,
  syncCurrentCodeNavigationLocation,
} from "./navigation";
import type { CodeFileTab, CodeNavigationLocation, CodeViewCallbacks } from "./types";
import { clamp, codeTabName, editableWorkspaceFile, fileContentOffsetToEditorPosition, isMediaFile, isUntitledCodePath, sleep, untitledCodeTabPrefix } from "./utils";
import { replaceMountedEditorContent, saveMountedEditorContent } from "./editor";
import { revealCodeFileInTree } from "./treeReveal";

const openTabFileWatchIntervalMs = 1500;
let openTabFileWatchTimerID: number | null = null;
let openTabFileWatchRunning = false;
let openTabFileWatchCallbacks: CodeViewCallbacks | null = null;
const openTabFileWatchErrors = new Map<string, string>();

type OpenCodeFileOptions = {
  temporary: boolean;
  selectionPosition?: number;
  selectionLine?: number;
  restoredLocation?: CodeNavigationLocation;
  recordNavigation?: boolean;
  suppressErrorToast?: boolean;
};

type ActivateCodeTabOptions = {
  saveMountedEditor?: boolean;
  recordNavigation?: boolean;
  sourceLocation?: CodeNavigationLocation | null;
};

type DirtyCodeTabCloseChoice = "save" | "cancel" | "discard";

let dirtyCodeTabDialogOpen = false;
let dirtyCodeTabDialogSeq = 0;

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
  if (
    openTabFileWatchTimerID !== null ||
    watchedOpenTabWorkspaceIDs().length === 0
  ) {
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
    if (state.tabs.some((tab) => !tab.untitled)) {
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
    if (!tab || tab.untitled || tab.dirty || tab.saving || tab.isMedia) {
      continue;
    }

    try {
      const file = services.WorkspaceFile.createFrom(
        tab.external
          ? await ReadExternalTextFile(path)
          : await ReadWorkspaceFile(workspaceID, path),
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
    const sourceLocation = captureActiveCodeNavigationLocation(workspaceID) ?? undefined;
    const paths = tabSwitcherPaths(state);
    const activeIndex = Math.max(0, paths.indexOf(state.activePath));
    const selectedIndex = wrapIndex(activeIndex + direction, paths.length);
    state.tabSwitcher = { paths, selectedIndex, sourceLocation };
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
  saveMountedEditorContent();
  const activePath = state.activePath;
  const sourceLocation = state.tabSwitcher.sourceLocation ?? null;
  state.tabSwitcher = null;
  promoteTabMruPath(state, activePath);
  recordCodeNavigationTransition(
    workspaceID,
    sourceLocation,
    captureActiveCodeNavigationLocation(workspaceID),
  );
  callbacks.render();
  if (!isUntitledCodePath(activePath)) {
    void revealCodeFileInTree(workspaceID, activePath, callbacks);
  }
  return true;
}

export function clearCodeTabSwitcher(workspaceID: string) {
  ensureCodeState(workspaceID).tabSwitcher = null;
}

export async function saveActiveCodeFile(workspaceID: string, callbacks: CodeViewCallbacks) {
  saveMountedEditorContent();
  const tab = activeCodeTab(workspaceID);
  if (!tab) {
    return false;
  }
  return saveCodeTab(workspaceID, tab.path, callbacks);
}

async function saveCodeTab(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  const tab = findTab(workspaceID, path);
  if (!tab || tab.saving) {
    return false;
  }
  if (tab.untitled) {
    return saveUntitledCodeTab(workspaceID, tab, callbacks);
  }
  if (!tab.dirty) {
    return true;
  }
  tab.saving = true;
  patchDirtyUI(workspaceID, tab);
  try {
    const savedContentBeforeSave = tab.content;
    const savedLineSeparatorBeforeSave = tab.lineSeparator;
    const saved = tab.external
      ? await SaveExternalTextFile(tab.path, tab.content, tab.modifiedAt)
      : await SaveWorkspaceFile(workspaceID, tab.path, tab.content, tab.modifiedAt);
    const latestBeforeApply = findTab(workspaceID, tab.path);
    const editorChangedDuringSave = Boolean(
      latestBeforeApply &&
        (latestBeforeApply.content !== savedContentBeforeSave ||
          latestBeforeApply.lineSeparator !== savedLineSeparatorBeforeSave),
    );
    const savedFile = services.WorkspaceFile.createFrom(saved);
    applySavedFile(workspaceID, savedFile);
    const savedTab = findTab(workspaceID, savedFile.path);
    if (
      savedTab &&
      (editorChangedDuringSave ||
        savedTab.content !== savedContentBeforeSave ||
        savedTab.lineSeparator !== savedLineSeparatorBeforeSave)
    ) {
      replaceMountedEditorContent(workspaceID, savedFile.path, savedTab.content);
    }
    callbacks.pushToast("File saved.", "success");
    void callbacks.refreshGitChanges(workspaceID);
    return true;
  } catch (error) {
    callbacks.pushToast(callbacks.errorMessage(error), "error");
    return false;
  } finally {
    const latest = findTab(workspaceID, tab.path);
    if (latest) {
      latest.saving = false;
      patchDirtyUI(workspaceID, latest);
    }
  }
}

async function saveUntitledCodeTab(
  workspaceID: string,
  tab: CodeFileTab,
  callbacks: CodeViewCallbacks,
) {
  tab.saving = true;
  patchDirtyUI(workspaceID, tab);
  const originalPath = tab.path;
  try {
    const selectedPath = await ChooseWorkspaceFileSavePath(
      workspaceID,
      codeTabName(tab),
    );
    if (!selectedPath) {
      return false;
    }

    const state = ensureCodeState(workspaceID);
    const conflict = state.tabs.find(
      (candidate) =>
        candidate !== tab &&
        !candidate.untitled &&
        sameWorkspacePath(candidate.path, selectedPath),
    );
    if (conflict) {
      callbacks.pushToast(`${selectedPath} is already open.`, "error");
      return false;
    }

    const contentBeforeSave = tab.content;
    const lineSeparatorBeforeSave = tab.lineSeparator;
    const saved = services.WorkspaceFile.createFrom(
      await SaveWorkspaceFileAs(workspaceID, selectedPath, contentBeforeSave),
    );
    const editorChangedDuringSave =
      tab.content !== contentBeforeSave ||
      tab.lineSeparator !== lineSeparatorBeforeSave;
    rewriteCodeTabPath(workspaceID, tab, saved.path);
    const latestContent = tab.content;
    const latestLineSeparator = tab.lineSeparator;
    applySavedFile(workspaceID, saved);
    if (editorChangedDuringSave) {
      tab.content = latestContent;
      tab.lineSeparator = latestLineSeparator;
      tab.bytes = new TextEncoder().encode(latestContent).length;
      tab.dirty = tab.content !== tab.savedContent;
    }
    tab.temporary = false;
    tab.untitled = false;
    if (!state.tabs.some((candidate) => candidate.untitled)) {
      state.temporaryFilesExpanded = false;
    }
    state.directories.clear();
    tab.saving = false;
    callbacks.pushToast("File saved.", "success");
    void callbacks.refreshGitChanges(workspaceID);
    callbacks.render();
    void revealCodeFileInTree(workspaceID, saved.path, callbacks);
    return true;
  } catch (error) {
    callbacks.pushToast(callbacks.errorMessage(error), "error");
    return false;
  } finally {
    tab.saving = false;
    if (tab.path === originalPath) {
      patchDirtyUI(workspaceID, tab);
    }
  }
}

function rewriteCodeTabPath(
  workspaceID: string,
  tab: CodeFileTab,
  nextPath: string,
) {
  const state = ensureCodeState(workspaceID);
  const previousPath = tab.path;
  tab.path = nextPath;
  if (state.activePath === previousPath) {
    state.activePath = nextPath;
  }
  state.tabMruPaths = state.tabMruPaths.map((path) =>
    path === previousPath ? nextPath : path,
  );
  if (state.tabSwitcher) {
    state.tabSwitcher.paths = state.tabSwitcher.paths.map((path) =>
      path === previousPath ? nextPath : path,
    );
    if (state.tabSwitcher.sourceLocation?.path === previousPath) {
      state.tabSwitcher.sourceLocation.path = nextPath;
    }
  }
  rewriteCodeNavigationHistoryPaths(workspaceID, previousPath, nextPath);
  promoteTabMruPath(state, nextPath);
}

export function createUntitledCodeFile(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  saveMountedEditorContent();
  const state = ensureCodeState(workspaceID);
  let path = "";
  do {
    path = `${untitledCodeTabPrefix}Untitled-${++state.untitledSeq}`;
  } while (state.tabs.some((tab) => tab.path === path));

  const tab: CodeFileTab = {
    path,
    content: "",
    savedContent: "",
    lineSeparator: "\n",
    bytes: 0,
    modifiedAt: "",
    dirty: false,
    saving: false,
    temporary: false,
    untitled: true,
    external: false,
    selectionAnchor: 0,
    selectionHead: 0,
    scrollTop: 0,
    scrollLeft: 0,
  };
  state.tabs.push(tab);
  state.activePath = path;
  state.temporaryFilesExpanded = true;
  state.tabSwitcher = null;
  promoteTabMruPath(state, path);
  callbacks.render();
}

export function toggleTemporaryFiles(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  const state = ensureCodeState(workspaceID);
  if (!state.tabs.some((tab) => tab.untitled)) {
    state.temporaryFilesExpanded = false;
    return;
  }
  state.temporaryFilesExpanded = !state.temporaryFilesExpanded;
  callbacks.render();
}

export async function openCodeFile(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
  options: OpenCodeFileOptions,
): Promise<boolean> {
  if (!path) {
    return false;
  }
  let openedPath = "";
  captureCodeTreeScroll(workspaceID);
  saveMountedEditorContent();
  const sourceLocation =
    options.recordNavigation === false
      ? null
      : captureActiveCodeNavigationLocation(workspaceID);
  const state = ensureCodeState(workspaceID);
  if (state.openingPath === path) {
    return false;
  }
  const existing = findTab(workspaceID, path);
  if (existing) {
    applyCodeTabOpenLocation(existing, options);
    if (options.recordNavigation !== false) {
      recordCodeNavigationTransition(
        workspaceID,
        sourceLocation,
        codeNavigationLocationFromTab(existing),
      );
    }
    activateCodeTab(workspaceID, existing.path, callbacks, {
      saveMountedEditor: false,
      recordNavigation: false,
    });
    return true;
  }

  const temporaryIndex = state.tabs.findIndex((tab) => tab.temporary && !tab.dirty);
  const replacedTemporaryPath =
    options.temporary && temporaryIndex >= 0
      ? state.tabs[temporaryIndex]?.path ?? ""
      : "";
  state.openingPath = path;
  callbacks.render();
  try {
    // Handle media files (images and videos) separately
    if (isMediaFile(path)) {
      return await openCodeMediaFile(workspaceID, path, callbacks, options, sourceLocation, temporaryIndex, replacedTemporaryPath);
    }

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
      untitled: false,
      external: false,
      selectionAnchor: 0,
      selectionHead: 0,
      scrollTop: 0,
      scrollLeft: 0,
    };
    applyCodeTabOpenLocation(nextTab, options);
    if (options.temporary && temporaryIndex >= 0) {
      state.tabs[temporaryIndex] = nextTab;
      removeTabMruPath(state, replacedTemporaryPath);
    } else {
      state.tabs.push(nextTab);
    }
    state.activePath = opened.path;
    openedPath = opened.path;
    promoteTabMruPath(state, opened.path);
    if (options.recordNavigation !== false) {
      recordCodeNavigationTransition(
        workspaceID,
        sourceLocation,
        codeNavigationLocationFromTab(nextTab),
      );
    }
    return true;
  } catch (error) {
    if (!options.suppressErrorToast) {
      callbacks.pushToast(callbacks.errorMessage(error), "error");
    }
    return false;
  } finally {
    state.openingPath = "";
    callbacks.render();
    if (openedPath) {
      void revealCodeFileInTree(workspaceID, openedPath, callbacks);
    }
  }
}

async function openCodeMediaFile(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
  options: OpenCodeFileOptions,
  sourceLocation: CodeNavigationLocation | null,
  temporaryIndex: number,
  replacedTemporaryPath: string,
): Promise<boolean> {
  const state = ensureCodeState(workspaceID);
  let openedPath = "";

  try {
    const media = await ReadWorkspaceMediaFile(workspaceID, path);
    const created = services.WorkspaceMediaFile.createFrom(media);
    const nextTab: CodeFileTab = {
      path: created.path,
      content: "",
      savedContent: "",
      lineSeparator: "\n",
      bytes: created.bytes,
      modifiedAt: "",
      dirty: false,
      saving: false,
      temporary: options.temporary,
      untitled: false,
      external: false,
      selectionAnchor: 0,
      selectionHead: 0,
      scrollTop: 0,
      scrollLeft: 0,
      isMedia: true,
      mediaMimeType: created.mimeType,
      mediaDataUrl: created.dataUrl,
      mediaLoading: false,
    };
    if (options.temporary && temporaryIndex >= 0) {
      state.tabs[temporaryIndex] = nextTab;
      removeTabMruPath(state, replacedTemporaryPath);
    } else {
      state.tabs.push(nextTab);
    }
    state.activePath = created.path;
    openedPath = created.path;
    promoteTabMruPath(state, created.path);
    if (options.recordNavigation !== false) {
      recordCodeNavigationTransition(
        workspaceID,
        sourceLocation,
        codeNavigationLocationFromTab(nextTab),
      );
    }
    return true;
  } catch (error) {
    if (!options.suppressErrorToast) {
      callbacks.pushToast(callbacks.errorMessage(error), "error");
    }
    return false;
  } finally {
    state.openingPath = "";
    callbacks.render();
    if (openedPath) {
      void revealCodeFileInTree(workspaceID, openedPath, callbacks);
    }
  }
}

function applyCodeTabOpenLocation(tab: CodeFileTab, options: OpenCodeFileOptions) {
  if (options.restoredLocation) {
    applyCodeNavigationLocationToTab(tab, options.restoredLocation);
    return;
  }
  if (options.selectionPosition !== undefined) {
    applyCodeTabSelection(tab, options.selectionPosition);
    return;
  }
  applyCodeTabLineSelection(tab, options.selectionLine);
}

function applyCodeTabSelection(tab: CodeFileTab, position: number | undefined) {
  if (position === undefined) {
    return;
  }
  const target = fileContentOffsetToEditorPosition(tab.content, tab.lineSeparator, position);
  tab.selectionAnchor = target;
  tab.selectionHead = target;
  tab.pendingRevealPosition = target;
  tab.pendingRevealScroll = "center";
}

function applyCodeTabLineSelection(tab: CodeFileTab, line: number | undefined) {
  if (line === undefined) {
    return;
  }
  const targetLine = Math.max(1, Math.floor(line));
  const offset = fileContentOffsetForLine(tab.content, tab.lineSeparator, targetLine);
  applyCodeTabSelection(tab, offset);
}

function fileContentOffsetForLine(content: string, lineSeparator: string, line: number) {
  if (line <= 1 || content === "") {
    return 0;
  }
  let currentLine = 1;
  let offset = 0;
  while (currentLine < line) {
    const nextBreak = content.indexOf(lineSeparator, offset);
    if (nextBreak < 0) {
      return content.length;
    }
    offset = nextBreak + lineSeparator.length;
    currentLine++;
  }
  return offset;
}

export async function openPinnedCodeFile(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  if (!path) {
    return false;
  }
  const state = ensureCodeState(workspaceID);
  if (state.openingPath === path) {
    await waitForOpeningPath(workspaceID, path);
    const opened = findTab(workspaceID, path);
    if (opened) {
      opened.temporary = false;
      activateCodeTab(workspaceID, opened.path, callbacks);
      return true;
    }
    return false;
  }
  const existing = findTab(workspaceID, path);
  if (existing) {
    existing.temporary = false;
    activateCodeTab(workspaceID, existing.path, callbacks);
    return true;
  }
  return openCodeFile(workspaceID, path, callbacks, { temporary: false });
}

export async function openWorkspaceCodeFile(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  await openCodeFile(workspaceID, path, callbacks, { temporary: false });
}

export async function openDroppedCodeFile(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  if (!path) {
    return false;
  }
  try {
    const workspacePath = await ResolveWorkspaceTextFilePath(workspaceID, path);
    return openPinnedCodeFile(workspaceID, workspacePath, callbacks);
  } catch {
    return openExternalCodeFile(workspaceID, path, callbacks);
  }
}

async function openExternalCodeFile(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  captureCodeTreeScroll(workspaceID);
  saveMountedEditorContent();
  const state = ensureCodeState(workspaceID);
  const existing = state.tabs.find(
    (tab) => tab.external && sameWorkspacePath(tab.path, path),
  );
  if (existing) {
    activateCodeTab(workspaceID, existing.path, callbacks);
    return true;
  }
  if (state.openingPath === path) {
    return false;
  }

  state.openingPath = path;
  callbacks.render();
  try {
    const opened = services.WorkspaceFile.createFrom(await ReadExternalTextFile(path));
    const editable = editableWorkspaceFile(opened);
    const tab: CodeFileTab = {
      path: opened.path,
      content: editable.content,
      savedContent: editable.content,
      lineSeparator: editable.lineSeparator,
      bytes: editable.bytes,
      modifiedAt: opened.modifiedAt,
      dirty: false,
      saving: false,
      temporary: false,
      untitled: false,
      external: true,
      selectionAnchor: 0,
      selectionHead: 0,
      scrollTop: 0,
      scrollLeft: 0,
    };
    state.tabs.push(tab);
    state.activePath = tab.path;
    promoteTabMruPath(state, tab.path);
    return true;
  } catch (error) {
    callbacks.pushToast(callbacks.errorMessage(error), "error");
    return false;
  } finally {
    state.openingPath = "";
    callbacks.render();
  }
}

export async function openWorkspaceCodeFileAtLine(
  workspaceID: string,
  path: string,
  line: number,
  callbacks: CodeViewCallbacks,
) {
  return openCodeFile(workspaceID, path, callbacks, {
    temporary: false,
    selectionLine: line,
  });
}

export async function navigateCodeHistory(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
  direction: -1 | 1,
) {
  saveMountedEditorContent();
  syncCurrentCodeNavigationLocation(
    workspaceID,
    captureActiveCodeNavigationLocation(workspaceID),
  );

  let skippedUnavailableTarget = false;
  for (;;) {
    const target = peekCodeNavigationHistoryTarget(workspaceID, direction);
    if (!target) {
      if (skippedUnavailableTarget) {
        callbacks.pushToast("Navigation target is no longer available.", "info");
      }
      return false;
    }

    const opened = await openCodeFile(workspaceID, target.location.path, callbacks, {
      temporary: false,
      restoredLocation: target.location,
      recordNavigation: false,
      suppressErrorToast: true,
    });
    const active = activeCodeTab(workspaceID);
    if (opened && active && sameWorkspacePath(active.path, target.location.path)) {
      commitCodeNavigationHistoryIndex(workspaceID, target.index);
      return true;
    }

    removeCodeNavigationHistoryEntry(workspaceID, target.index);
    skippedUnavailableTarget = true;
  }
}

export function closeCodeTab(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
): Promise<boolean> {
  return closeCodeTabAtPath(workspaceID, path, callbacks);
}

export function closeActiveCodeTab(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
): Promise<boolean> {
  saveMountedEditorContent();
  const tab = activeCodeTab(workspaceID);
  if (!tab) {
    return Promise.resolve(false);
  }
  return closeCodeTabAtPath(workspaceID, tab.path, callbacks);
}

async function closeCodeTabAtPath(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  saveMountedEditorContent();
  const state = ensureCodeState(workspaceID);
  let index = state.tabs.findIndex((tab) => tab.path === path);
  if (index < 0) {
    return false;
  }
  let tab = state.tabs[index];
  if (tab.dirty) {
    const choice = await promptDirtyCodeTabClose(tab);
    if (choice === "cancel") {
      return false;
    }
    if (choice === "save" && !(await saveCodeTab(workspaceID, tab.path, callbacks))) {
      return false;
    }
    path = tab.path;
    index = state.tabs.indexOf(tab);
    if (index < 0) {
      return false;
    }
    tab = state.tabs[index];
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
  if (!state.tabs.some((candidate) => candidate.untitled)) {
    state.temporaryFilesExpanded = false;
  }
  callbacks.render();
  if (state.activePath && !activeCodeTab(workspaceID)?.external && !isUntitledCodePath(state.activePath)) {
    void revealCodeFileInTree(workspaceID, state.activePath, callbacks);
  }
  return true;
}

function promptDirtyCodeTabClose(tab: CodeFileTab): Promise<DirtyCodeTabCloseChoice> {
  if (dirtyCodeTabDialogOpen) {
    return Promise.resolve("cancel");
  }
  dirtyCodeTabDialogOpen = true;
  const titleID = `code-close-dirty-title-${++dirtyCodeTabDialogSeq}`;

  return new Promise((resolve) => {
    const overlay = document.createElement("div");
    overlay.className = "code-close-dirty-overlay";
    overlay.setAttribute("role", "dialog");
    overlay.setAttribute("aria-modal", "true");
    overlay.setAttribute("aria-labelledby", titleID);

    const panel = document.createElement("div");
    panel.className = "code-close-dirty-dialog";

    const title = document.createElement("h2");
    title.id = titleID;
    title.textContent = "Unsaved changes";

    const message = document.createElement("p");
    message.textContent = `${codeTabName(tab)} has unsaved changes.`;

    const actions = document.createElement("div");
    actions.className = "code-close-dirty-actions";

    const save = document.createElement("button");
    save.className = "primary-button";
    save.type = "button";
    save.textContent = "Save";

    const cancel = document.createElement("button");
    cancel.className = "secondary-button";
    cancel.type = "button";
    cancel.textContent = "Cancel";
    cancel.dataset.initialFocus = "";

    const discard = document.createElement("button");
    discard.className = "secondary-button danger-button";
    discard.type = "button";
    discard.textContent = "Close without saving";

    const finish = (choice: DirtyCodeTabCloseChoice) => {
      dirtyCodeTabDialogOpen = false;
      document.removeEventListener("keydown", handleKeydown, true);
      overlay.remove();
      resolve(choice);
    };
    const handleKeydown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      finish("cancel");
    };

    save.addEventListener("click", () => finish("save"));
    cancel.addEventListener("click", () => finish("cancel"));
    discard.addEventListener("click", () => finish("discard"));
    overlay.addEventListener("pointerdown", (event) => {
      if (event.target === overlay) {
        finish("cancel");
      }
    });
    document.addEventListener("keydown", handleKeydown, true);

    actions.append(save, cancel, discard);
    panel.append(title, message, actions);
    overlay.append(panel);
    document.body.append(overlay);
    cancel.focus();
  });
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
  options: ActivateCodeTabOptions = {},
) {
  const state = ensureCodeState(workspaceID);
  if (!path || !state.tabs.some((tab) => tab.path === path)) {
    return;
  }
  if (options.saveMountedEditor !== false) {
    saveMountedEditorContent();
  }
  const sourceLocation =
    options.recordNavigation === false
      ? null
      : options.sourceLocation ?? captureActiveCodeNavigationLocation(workspaceID);
  state.tabSwitcher = null;
  state.activePath = path;
  promoteTabMruPath(state, path);
  if (options.recordNavigation !== false) {
    recordCodeNavigationTransition(
      workspaceID,
      sourceLocation,
      activeCodeTab(workspaceID)
        ? codeNavigationLocationFromTab(activeCodeTab(workspaceID)!)
        : null,
    );
  }
  callbacks.render();
  if (!activeCodeTab(workspaceID)?.external && !isUntitledCodePath(path)) {
    void revealCodeFileInTree(workspaceID, path, callbacks);
  }
}

function sameWorkspacePath(left: string, right: string) {
  return left.replaceAll("\\", "/").toLowerCase() === right.replaceAll("\\", "/").toLowerCase();
}
