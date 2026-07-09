import { services } from "../../wailsjs/go/models";
import type { CodeFileTab, CodeWorkspaceState, DirectoryState } from "./types";
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
export const spellCheckIgnoreListKey = "echo:spell-check-ignore-list";

function loadSpellCheckIgnoreList(): Set<string> {
  try {
    const raw = localStorage.getItem(spellCheckIgnoreListKey);
    if (raw) {
      const parsed = JSON.parse(raw) as unknown;
      if (Array.isArray(parsed)) {
        return new Set(parsed.filter((item: unknown) => typeof item === "string"));
      }
    }
  } catch {
    // ignore parse errors and start fresh
  }
  return new Set<string>();
}

export function saveSpellCheckIgnoreList(list: Set<string>): void {
  try {
    localStorage.setItem(spellCheckIgnoreListKey, JSON.stringify([...list]));
  } catch {
    // storage full or unavailable — silently ignore
  }
}

/**
 * Add a word to the spell-check ignore list and persist.
 * Returns true if the word was newly added, false if it was already ignored.
 */
export function addToSpellCheckDictionary(workspaceID: string, word: string): boolean {
  const lower = word.toLowerCase().trim();
  if (!lower || lower.length <= 1) return false;
  const state = ensureCodeState(workspaceID);
  const wasNew = !state.spellCheckIgnoreList.has(lower);
  state.spellCheckIgnoreList.add(lower);
  saveSpellCheckIgnoreList(state.spellCheckIgnoreList);
  return wasNew;
}

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
      spellCheckIgnoreList: loadSpellCheckIgnoreList(),
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
