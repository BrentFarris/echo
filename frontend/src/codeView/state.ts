import { services } from "../../wailsjs/go/models";
import type { CodeEntryKind, CodeFileTab, CodeWorkspaceState, DirectoryState } from "./types";
import { clamp, editableWorkspaceFile, editorDocumentLengthForFileContent } from "./utils";

const ignoredDirectoryNames = new Set([
  ".git",
  ".next",
  ".vite",
  "bin",
  "build",
  "coverage",
  "dist",
  "node_modules",
  "obj",
  "target",
]);
export const codeStates = new Map<string, CodeWorkspaceState>();
export const explorerWidthStorageKey = "echo:code-explorer-width";
const defaultExplorerWidth = 300;
export const minExplorerWidth = 220;
export const maxExplorerWidth = 640;

function storedExplorerWidth(): number {
  const raw = Number(localStorage.getItem(explorerWidthStorageKey));
  if (!Number.isFinite(raw) || raw <= 0) {
    return defaultExplorerWidth;
  }
  return clamp(raw, minExplorerWidth, maxExplorerWidth);
}

export function ensureCodeState(workspaceID: string): CodeWorkspaceState {
  let state = codeStates.get(workspaceID);
  if (!state) {
    state = {
      directories: new Map(),
      expandedPaths: new Set(["."]),
      tabs: [],
      activePath: "",
      selectedPath: "",
      selectedKind: "other",
      selectedEntries: new Map(),
      selectionAnchorPath: "",
      selectionAnchorKind: "other",
      tabMruPaths: [],
      navigationHistory: {
        entries: [],
        currentIndex: -1,
        maxSize: 100,
      },
      tabSwitcher: null,
      pendingCreate: null,
      pendingRename: null,
      drag: null,
      showIgnored: false,
      openingPath: "",
      explorerWidth: storedExplorerWidth(),
      explorerDrawerOpen: false,
      codeTreeScrollTop: 0,
      searchQuery: "",
      searchResults: [],
      searchLoading: false,
      searchTruncated: false,
      searchRequestSeq: 0,
      searchTimerID: null,
      searchFocused: false,
      preservingSearchFocus: false,
      untitledSeq: 0,
      temporaryFilesExpanded: false,
      textSearchOpen: false,
      textSearchQuery: "",
      textSearchInclude: "",
      textSearchExclude: "",
      textSearchRegex: false,
      textSearchCaseSensitive: false,
      textSearchWholeWord: false,
      textSearchResult: null,
      textSearchLoading: false,
      textSearchError: "",
      textSearchRequestSeq: 0,
      textSearchTimerID: null,
      textSearchFocusedField: "",
      preservingTextSearchFocus: false,
      inlineChat: null,
      referencesPanel: null,
      quickOpen: {
        open: false,
        query: "",
        results: [],
        loading: false,
        truncated: false,
        selectedIndex: 0,
        requestSeq: 0,
        timerID: null,
      },
    };
    codeStates.set(workspaceID, state);
  }
  return state;
}

export function hasDirtyCodeTabs(workspaceID: string): boolean {
  return ensureCodeState(workspaceID).tabs.some((tab) => tab.dirty);
}

export function applySavedFile(workspaceID: string, file: services.WorkspaceFile) {
  const tab = findTab(workspaceID, file.path);
  if (!tab) {
    return;
  }
  const editable = editableWorkspaceFile(file);
  tab.content = editable.content;
  tab.savedContent = editable.content;
  tab.lineSeparator = editable.lineSeparator;
  tab.bytes = editable.bytes;
  tab.modifiedAt = file.modifiedAt;
  tab.dirty = false;
  tab.untitled = false;
  const docLength = editorDocumentLengthForFileContent(tab.content, tab.lineSeparator);
  tab.selectionAnchor = clamp(tab.selectionAnchor, 0, docLength);
  tab.selectionHead = clamp(tab.selectionHead, 0, docLength);
}

export function activeCodeTab(workspaceID: string): CodeFileTab | null {
  const state = ensureCodeState(workspaceID);
  return state.tabs.find((tab) => tab.path === state.activePath) ?? null;
}

export function findTab(workspaceID: string, path: string): CodeFileTab | null {
  return ensureCodeState(workspaceID).tabs.find((tab) => tab.path === path) ?? null;
}

export function tabSwitcherPaths(state: CodeWorkspaceState): string[] {
  pruneTabMruPaths(state);
  if (state.activePath && !state.tabMruPaths.includes(state.activePath)) {
    return [state.activePath, ...state.tabMruPaths];
  }
  return [...state.tabMruPaths];
}

export function promoteTabMruPath(state: CodeWorkspaceState, path: string) {
  if (!path) {
    return;
  }
  pruneTabMruPaths(state);
  if (!state.tabs.some((tab) => tab.path === path)) {
    return;
  }
  state.tabMruPaths = [
    path,
    ...state.tabMruPaths.filter((candidate) => candidate !== path),
  ];
}

export function removeTabMruPath(state: CodeWorkspaceState, path: string) {
  if (!path) {
    return;
  }
  state.tabMruPaths = state.tabMruPaths.filter((candidate) => candidate !== path);
  if (state.tabSwitcher) {
    state.tabSwitcher.paths = state.tabSwitcher.paths.filter(
      (candidate) => candidate !== path,
    );
    state.tabSwitcher.selectedIndex = clamp(
      state.tabSwitcher.selectedIndex,
      0,
      Math.max(0, state.tabSwitcher.paths.length - 1),
    );
  }
}

export function pruneTabMruPaths(state: CodeWorkspaceState) {
  const openPaths = state.tabs.map((tab) => tab.path);
  const openPathSet = new Set(openPaths);
  const seen = new Set<string>();
  const paths: string[] = [];
  state.tabMruPaths.forEach((path) => {
    if (!openPathSet.has(path) || seen.has(path)) {
      return;
    }
    seen.add(path);
    paths.push(path);
  });
  openPaths.forEach((path) => {
    if (seen.has(path)) {
      return;
    }
    seen.add(path);
    paths.push(path);
  });
  state.tabMruPaths = paths;
}

export function wrapIndex(index: number, length: number): number {
  if (length <= 0) {
    return 0;
  }
  return ((index % length) + length) % length;
}

export function directoryStateFor(
  state: CodeWorkspaceState,
  path: string,
): DirectoryState {
  const key = path || ".";
  let directory = state.directories.get(key);
  if (!directory) {
    directory = {
      entries: [],
      loaded: false,
      loading: false,
      error: "",
    };
    state.directories.set(key, directory);
  }
  return directory;
}

export type CodeTreeSelectionEntry = {
  path: string;
  kind: string;
};

export type CodeTreeSelectionOptions = {
  mode?: "single" | "toggle" | "range";
  rangeEntries?: CodeTreeSelectionEntry[];
  additiveRange?: boolean;
};

export function normalizeCodeEntryKind(kind: string): CodeEntryKind {
  if (kind === "file" || kind === "directory") {
    return kind;
  }
  return "other";
}

export function isCodeTreeEntrySelected(
  state: CodeWorkspaceState,
  path: string,
): boolean {
  if (!path) {
    return false;
  }
  ensureCodeTreeSelectionState(state);
  return state.selectedEntries.has(path) || state.selectedPath === path;
}

export function selectCodeTreeEntryInState(
  state: CodeWorkspaceState,
  path: string,
  kind: string,
  options: CodeTreeSelectionOptions = {},
) {
  if (!path) {
    return;
  }
  ensureCodeTreeSelectionState(state);
  const entryKind = normalizeCodeEntryKind(kind);
  if (options.mode === "toggle") {
    toggleCodeTreeSelection(state, path, entryKind);
    return;
  }
  if (options.mode === "range") {
    selectCodeTreeRange(state, path, entryKind, options);
    return;
  }
  setSingleCodeTreeSelection(state, path, entryKind);
}

export function setSingleCodeTreeSelection(
  state: CodeWorkspaceState,
  path: string,
  kind: CodeEntryKind,
) {
  state.selectedPath = path;
  state.selectedKind = kind;
  state.selectedEntries = new Map([[path, kind]]);
  state.selectionAnchorPath = path;
  state.selectionAnchorKind = kind;
}

export function rewriteCodeTreeSelectionPaths(
  state: CodeWorkspaceState,
  rewrite: (path: string) => string,
) {
  ensureCodeTreeSelectionState(state);
  state.selectedPath = rewrite(state.selectedPath);
  state.selectionAnchorPath = rewrite(state.selectionAnchorPath);
  state.selectedEntries = uniqueSelectionEntries(
    Array.from(state.selectedEntries.entries()).map(([path, kind]) => [
      rewrite(path),
      kind,
    ]),
  );
}

export function filteredEntries(
  state: CodeWorkspaceState,
  entries: services.WorkspaceFileEntry[],
): services.WorkspaceFileEntry[] {
  if (state.showIgnored) {
    return entries;
  }
  return entries.filter((entry) => {
    if (entry.kind !== "directory") {
      return true;
    }
    return !ignoredDirectoryNames.has(entry.name.toLowerCase());
  });
}

function ensureCodeTreeSelectionState(state: CodeWorkspaceState) {
  if (!state.selectedEntries) {
    state.selectedEntries = new Map();
  }
  if (state.selectedPath && !state.selectedEntries.has(state.selectedPath)) {
    state.selectedEntries.set(state.selectedPath, state.selectedKind);
  }
  state.selectionAnchorPath = state.selectionAnchorPath ?? "";
  state.selectionAnchorKind = state.selectionAnchorKind ?? "other";
}

function toggleCodeTreeSelection(
  state: CodeWorkspaceState,
  path: string,
  kind: CodeEntryKind,
) {
  const next = new Map(state.selectedEntries);
  if (next.has(path)) {
    next.delete(path);
  } else {
    next.set(path, kind);
  }
  state.selectedEntries = next;
  if (next.has(path)) {
    state.selectedPath = path;
    state.selectedKind = kind;
  } else {
    const last = Array.from(next.entries()).at(-1);
    state.selectedPath = last?.[0] ?? "";
    state.selectedKind = last?.[1] ?? "other";
  }
  state.selectionAnchorPath = path;
  state.selectionAnchorKind = kind;
}

function selectCodeTreeRange(
  state: CodeWorkspaceState,
  path: string,
  kind: CodeEntryKind,
  options: CodeTreeSelectionOptions,
) {
  const entries = (options.rangeEntries ?? []).filter((entry) => entry.path);
  const anchorPath = firstVisibleAnchorPath(state, entries, path);
  const anchorIndex = entries.findIndex((entry) => entry.path === anchorPath);
  const targetIndex = entries.findIndex((entry) => entry.path === path);
  if (anchorIndex < 0 || targetIndex < 0) {
    setSingleCodeTreeSelection(state, path, kind);
    return;
  }

  const [from, to] = anchorIndex < targetIndex
    ? [anchorIndex, targetIndex]
    : [targetIndex, anchorIndex];
  const next = options.additiveRange
    ? new Map(state.selectedEntries)
    : new Map<string, CodeEntryKind>();
  entries.slice(from, to + 1).forEach((entry) => {
    next.set(entry.path, normalizeCodeEntryKind(entry.kind));
  });
  state.selectedEntries = next;
  state.selectedPath = path;
  state.selectedKind = kind;
  if (!state.selectionAnchorPath) {
    state.selectionAnchorPath = anchorPath;
    state.selectionAnchorKind = normalizeCodeEntryKind(
      entries[anchorIndex]?.kind ?? kind,
    );
  }
}

function firstVisibleAnchorPath(
  state: CodeWorkspaceState,
  entries: CodeTreeSelectionEntry[],
  fallbackPath: string,
): string {
  const visiblePaths = new Set(entries.map((entry) => entry.path));
  for (const path of [state.selectionAnchorPath, state.selectedPath, fallbackPath]) {
    if (path && visiblePaths.has(path)) {
      return path;
    }
  }
  return fallbackPath;
}

function uniqueSelectionEntries(
  entries: Array<[string, CodeEntryKind]>,
): Map<string, CodeEntryKind> {
  const next = new Map<string, CodeEntryKind>();
  entries.forEach(([path, kind]) => {
    if (path) {
      next.set(path, kind);
    }
  });
  return next;
}

export function workspaceFileChanged(
  tab: CodeFileTab,
  file: services.WorkspaceFile,
) {
  const editable = editableWorkspaceFile(file);
  return (
    tab.content !== editable.content ||
    tab.savedContent !== editable.content ||
    tab.bytes !== editable.bytes ||
    tab.modifiedAt !== file.modifiedAt
  );
}
