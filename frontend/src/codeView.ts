import { HighlightStyle, syntaxHighlighting } from "@codemirror/language";
import { languages as languageData } from "@codemirror/language-data";
import { EditorState, Prec, StateEffect, StateField, Transaction, type Extension, type Text } from "@codemirror/state";
import {
  Decoration,
  type DecorationSet,
  EditorView,
  WidgetType,
  keymap,
} from "@codemirror/view";
import { basicSetup } from "codemirror";
import { tags } from "@lezer/highlight";
import {
  ListWorkspaceDirectory,
  ReadWorkspaceFile,
  SaveWorkspaceFile,
  SearchWorkspaceFiles,
  SubmitInlineCodePrompt,
} from "../wailsjs/go/services/SystemService";
import { services } from "../wailsjs/go/models";
import { patchChildrenFromHtml, renderMarkdown } from "./markdown";

type ToastTone = "info" | "success" | "error";

type CodeViewCallbacks = {
  render: () => void;
  pushToast: (message: string, tone?: ToastTone) => void;
  errorMessage: (error: unknown) => string;
};

type DirectoryState = {
  entries: services.WorkspaceFileEntry[];
  loaded: boolean;
  loading: boolean;
  error: string;
};

type CodeFileTab = {
  path: string;
  content: string;
  savedContent: string;
  lineSeparator: string;
  bytes: number;
  modifiedAt: string;
  dirty: boolean;
  saving: boolean;
  temporary: boolean;
  selectionAnchor: number;
  selectionHead: number;
  scrollTop: number;
  scrollLeft: number;
};

type CodeTabSwitcherState = {
  paths: string[];
  selectedIndex: number;
};

type InlineCodeChatState = {
  path: string;
  anchorPosition: number;
  selectedText: string;
  draft: string;
  submitting: boolean;
  response: string;
  error: string;
  requestID: string;
  renderKey: number;
};

type InlineCodePromptEvent = {
  workspaceId: string;
  requestId?: string;
  filePath: string;
  type: string;
  content?: string;
  toolCall?: services.ChatToolActivity;
  affectedPaths?: string[];
  error?: string;
  finishReason?: string;
};

type CodeWorkspaceState = {
  directories: Map<string, DirectoryState>;
  expandedPaths: Set<string>;
  tabs: CodeFileTab[];
  activePath: string;
  tabMruPaths: string[];
  tabSwitcher: CodeTabSwitcherState | null;
  showIgnored: boolean;
  openingPath: string;
  explorerWidth: number;
  codeTreeScrollTop: number;
  searchQuery: string;
  searchResults: services.WorkspaceFileEntry[];
  searchLoading: boolean;
  searchTruncated: boolean;
  searchRequestSeq: number;
  searchTimerID: number | null;
  searchFocused: boolean;
  preservingSearchFocus: boolean;
  inlineChat: InlineCodeChatState | null;
};

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
const codeStates = new Map<string, CodeWorkspaceState>();
const explorerWidthStorageKey = "echo:code-explorer-width";
const defaultExplorerWidth = 300;
const minExplorerWidth = 220;
const maxExplorerWidth = 640;
const inlineSnippetContextLines = 40;
const inlineSnippetMaxBytes = 24 * 1024;
const tabSize = 4;
const inlineFocusSubstringMaxBytes = 4 * 1024;
const inlineReloadRetryDelays = [75, 175, 350, 650];
const openTabFileWatchIntervalMs = 1500;

let mountedEditor: EditorView | null = null;
let mountedEditorWorkspaceID = "";
let mountedEditorPath = "";
let editorMountToken = 0;
let inlineChatRenderSeq = 0;
let inlinePromptRequestSeq = 0;
let openTabFileWatchTimerID: number | null = null;
let openTabFileWatchRunning = false;
let openTabFileWatchCallbacks: CodeViewCallbacks | null = null;
const openTabFileWatchErrors = new Map<string, string>();

const setInlineCodeChatEffect = StateEffect.define<number>();
const clearInlineCodeChatEffect = StateEffect.define<void>();

const codeIcons = {
  back: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m12 19-7-7 7-7"/><path d="M19 12H5"/></svg>`,
  chevron: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m9 18 6-6-6-6"/></svg>`,
  close: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M18 6 6 18M6 6l12 12"/></svg>`,
  code: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m16 18 6-6-6-6"/><path d="m8 6-6 6 6 6"/></svg>`,
  file: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6"/></svg>`,
  folder: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6a2 2 0 0 1 2-2h5l2 2h7a2 2 0 0 1 2 2v10a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z"/></svg>`,
  git: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m15 3 6 6-6 6"/><path d="M21 9H9a6 6 0 0 0 0 12h1"/><path d="M3 3v5h5"/><path d="M3 8a6 6 0 0 1 6-5h2"/></svg>`,
  refresh: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M21 12a9 9 0 0 1-15 6.7L3 16"/><path d="M3 21v-5h5"/><path d="M3 12a9 9 0 0 1 15-6.7L21 8"/><path d="M21 3v5h-5"/></svg>`,
  save: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M19 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h11l5 5v11a2 2 0 0 1-2 2Z"/><path d="M17 21v-8H7v8"/><path d="M7 3v5h8"/></svg>`,
};

const codeEditorTheme = EditorView.theme({
  "&": {
    height: "100%",
    backgroundColor: "var(--code-editor-bg)",
    color: "var(--code-editor-text)",
  },
  ".cm-scroller": {
    fontFamily: '"Cascadia Mono", "SFMono-Regular", Consolas, monospace',
    fontSize: "0.88rem",
    lineHeight: "1.55",
  },
  ".cm-content": {
    caretColor: "var(--code-editor-caret)",
  },
  "&.cm-focused .cm-cursor, .cm-dropCursor": {
    borderLeftColor: "var(--code-editor-caret)",
  },
  ".cm-gutters": {
    backgroundColor: "var(--code-editor-gutter-bg)",
    color: "var(--code-editor-gutter-text)",
    borderRight: "1px solid var(--code-editor-border)",
  },
  ".cm-activeLine": {
    backgroundColor: "transparent",
    boxShadow: "inset 2px 0 0 var(--code-editor-active-line)",
  },
  ".cm-activeLineGutter": {
    backgroundColor: "var(--code-editor-active-gutter)",
  },
  ".cm-selectionBackground, &.cm-focused .cm-selectionBackground, &.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground": {
    backgroundColor: "var(--code-editor-selection) !important",
  },
  "& ::selection, &::selection": {
    backgroundColor: "var(--code-editor-selection) !important",
    color: "var(--code-editor-text) !important",
  },
  ".cm-selectionMatch": {
    backgroundColor: "var(--code-editor-selection-match)",
  },
  ".cm-matchingBracket": {
    backgroundColor: "var(--code-editor-selection-match)",
    color: "var(--code-editor-text)",
  },
  ".cm-nonmatchingBracket": {
    color: "var(--code-editor-invalid)",
  },
  "&.cm-focused": {
    outline: "none",
  },
});

const codeHighlightStyle = HighlightStyle.define([
  { tag: tags.comment, color: "var(--code-token-comment)" },
  { tag: [tags.keyword, tags.controlKeyword, tags.definitionKeyword, tags.moduleKeyword], color: "var(--code-token-keyword)", fontWeight: "600" },
  { tag: [tags.atom, tags.bool, tags.null], color: "var(--code-token-atom)" },
  { tag: [tags.string, tags.special(tags.string), tags.character], color: "var(--code-token-string)" },
  { tag: [tags.number, tags.integer, tags.float], color: "var(--code-token-number)" },
  { tag: [tags.regexp, tags.escape], color: "var(--code-token-special)" },
  { tag: tags.variableName, color: "var(--code-token-variable)" },
  { tag: [tags.definition(tags.variableName), tags.function(tags.variableName)], color: "var(--code-token-function)" },
  { tag: [tags.typeName, tags.className, tags.namespace], color: "var(--code-token-type)" },
  { tag: [tags.propertyName, tags.attributeName], color: "var(--code-token-property)" },
  { tag: [tags.operator, tags.operatorKeyword, tags.punctuation], color: "var(--code-token-punctuation)" },
  { tag: tags.meta, color: "var(--code-token-meta)" },
  { tag: tags.invalid, color: "var(--code-editor-invalid)" },
]);

function tabIndentionExtensions(): Extension[] {
  return [
    EditorState.tabSize.of(tabSize),
    Prec.highest(
      keymap.of([
        {
          key: "Tab",
          run: (view) => {
            view.dispatch({
              changes: {
                from: view.state.selection.main.from,
                to: view.state.selection.main.to,
                insert: "\t",
              },
            });
            return true;
          },
        },
      ]),
    ),
  ];
}

export function ensureCodeState(workspaceID: string): CodeWorkspaceState {
  let state = codeStates.get(workspaceID);
  if (!state) {
    state = {
      directories: new Map(),
      expandedPaths: new Set(["."]),
      tabs: [],
      activePath: "",
      tabMruPaths: [],
      tabSwitcher: null,
      showIgnored: false,
      openingPath: "",
      explorerWidth: storedExplorerWidth(),
      codeTreeScrollTop: 0,
      searchQuery: "",
      searchResults: [],
      searchLoading: false,
      searchTruncated: false,
      searchRequestSeq: 0,
      searchTimerID: null,
      searchFocused: false,
      preservingSearchFocus: false,
      inlineChat: null,
    };
    codeStates.set(workspaceID, state);
  }
  return state;
}

export async function ensureCodeViewRootLoaded(workspaceID: string) {
  const root = directoryStateFor(ensureCodeState(workspaceID), ".");
  if (root.loaded || root.loading) {
    return;
  }
  await loadDirectory(workspaceID, ".");
}

export function renderCodeView(workspace: services.Workspace): string {
  const state = ensureCodeState(workspace.id);
  const activeTab = activeCodeTab(workspace.id);
  const dirtyCount = state.tabs.filter((tab) => tab.dirty).length;
  const saveDisabled = !activeTab || !activeTab.dirty || activeTab.saving;
  const filterLabel = state.showIgnored ? "Hide ignored" : "Show ignored";
  return `
    <section
      class="code-view"
      aria-labelledby="code-title"
      data-code-view
      data-code-view-workspace-id="${escapeAttribute(workspace.id)}"
    >
      <header class="code-view-heading">
        <div>
          <strong id="code-title">${escapeHtml(workspace.displayName)}</strong><span class="heading-path">${escapeHtml(workspace.folderPath)}</span>
        </div>
        <div class="code-view-actions">
          <button class="secondary-button icon-text-button" type="button" data-action="close-code-view">
            ${codeIcons.back}
            <span>Chat</span>
          </button>
          <button class="secondary-button icon-text-button" type="button" data-action="open-git-changes">
            ${codeIcons.git}
            <span>Git</span>
          </button>
          <button class="secondary-button icon-text-button" type="button" data-code-action="toggle-filter" aria-pressed="${state.showIgnored}">
            ${codeIcons.code}
            <span>${escapeHtml(filterLabel)}</span>
          </button>
          <button class="primary-button icon-text-button" type="button" data-code-action="save-active-file" data-code-save ${saveDisabled ? "disabled" : ""}>
            ${activeTab?.saving ? `<span class="spinner" aria-hidden="true"></span>` : codeIcons.save}
            <span>Save</span>
          </button>
        </div>
      </header>
      <div class="code-workspace" style="--code-explorer-width: ${state.explorerWidth}px">
        <aside class="code-explorer" aria-label="Workspace files">
          <div class="code-explorer-meta">
            <span data-code-dirty-summary>${dirtyCount ? `${dirtyCount} unsaved` : "Files"}</span>
            <button class="icon-button" type="button" title="Refresh files" aria-label="Refresh files" data-code-action="refresh-tree">
              ${codeIcons.refresh}
            </button>
          </div>
          <label class="code-search">
            <span>Search files</span>
            <input
              type="search"
              value="${escapeAttribute(state.searchQuery)}"
              placeholder="Search files..."
              aria-label="Search files"
              data-code-search
            />
          </label>
          <div class="code-tree" role="tree" data-code-tree>
            ${renderFileList(workspace.id)}
          </div>
        </aside>
        <div class="code-resizer" role="separator" aria-label="Resize file list" aria-orientation="vertical" tabindex="0" data-code-resizer></div>
        <section class="code-editor-pane" aria-label="Code editor">
          ${renderCodeTabs(workspace.id)}
          ${renderCodeTabSwitcher(workspace.id)}
          <div class="code-editor-frame">
            ${
              activeTab
                ? `<div class="code-editor-mount" data-code-editor-mount></div>`
                : `<div class="empty-state code-empty">
                    <strong>No file open</strong>
                    <span>Select a text file in the workspace tree.</span>
                  </div>`
            }
          </div>
          <footer class="code-status-line" data-code-status>
            ${renderCodeStatus(activeTab, state.openingPath)}
          </footer>
        </section>
      </div>
    </section>
  `;
}

export function bindCodeViewEvents(root: ParentNode, callbacks: CodeViewCallbacks) {
  const view = root.querySelector<HTMLElement>("[data-code-view]");
  const workspaceID = view?.dataset.codeViewWorkspaceId ?? "";
  if (!workspaceID) {
    return;
  }

  bindCodeActionEvents(root, workspaceID, callbacks);
  bindCodeFileRowEvents(root, workspaceID, callbacks);

  root.querySelectorAll<HTMLElement>("[data-code-tab-main]").forEach((element) => {
    element.addEventListener("mousedown", (event) => {
      if (event.button !== 1) {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
    });
    element.addEventListener("auxclick", (event) => {
      if (event.button !== 1) {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      closeCodeTab(workspaceID, element.dataset.codePath ?? "", callbacks);
    });
    element.addEventListener("dblclick", (event) => {
      event.preventDefault();
      event.stopPropagation();
      pinCodeTab(workspaceID, element.dataset.codePath ?? "", callbacks);
    });
  });

  const search = root.querySelector<HTMLInputElement>("[data-code-search]");
  search?.addEventListener("input", (event) => {
    handleSearchInput(workspaceID, event.currentTarget as HTMLInputElement, callbacks);
  });
  search?.addEventListener("focus", () => {
    ensureCodeState(workspaceID).searchFocused = true;
  });
  search?.addEventListener("blur", () => {
    const state = ensureCodeState(workspaceID);
    if (!state.preservingSearchFocus) {
      state.searchFocused = false;
    }
  });
  if (search && ensureCodeState(workspaceID).searchFocused) {
    search.focus();
    search.setSelectionRange(search.value.length, search.value.length);
  }

  const resizer = root.querySelector<HTMLElement>("[data-code-resizer]");
  resizer?.addEventListener("pointerdown", (event) => {
    startExplorerResize(event, workspaceID);
  });

  restoreCodeTreeScroll(workspaceID);
  startOpenTabFileWatch(callbacks);
  void mountActiveCodeEditor(workspaceID, callbacks);
}

function bindCodeActionEvents(
  root: ParentNode,
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  root.querySelectorAll<HTMLElement>("[data-code-action]").forEach((element) => {
    element.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      void handleCodeAction(element, workspaceID, callbacks);
    });
  });
}

function bindCodeFileRowEvents(
  root: ParentNode,
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  root.querySelectorAll<HTMLElement>("[data-code-file-row]").forEach((element) => {
    element.addEventListener("click", (event) => {
      event.preventDefault();
      void openCodeFile(workspaceID, element.dataset.codePath ?? "", callbacks, {
        temporary: true,
      });
    });
    element.addEventListener("dblclick", (event) => {
      event.preventDefault();
      void openPinnedCodeFile(workspaceID, element.dataset.codePath ?? "", callbacks);
    });
    element.addEventListener("keydown", (event) => {
      if (event.key !== "Enter") {
        return;
      }
      event.preventDefault();
      void openCodeFile(workspaceID, element.dataset.codePath ?? "", callbacks, {
        temporary: true,
      });
    });
  });
}

export function destroyCodeEditor() {
  saveMountedEditorContent();
  if (mountedEditor) {
    mountedEditor.destroy();
  }
  mountedEditor = null;
  mountedEditorWorkspaceID = "";
  mountedEditorPath = "";
  editorMountToken++;
}

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

function startOpenTabFileWatch(callbacks: CodeViewCallbacks) {
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

export function hasDirtyCodeTabs(workspaceID: string): boolean {
  return ensureCodeState(workspaceID).tabs.some((tab) => tab.dirty);
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

async function handleCodeAction(
  target: HTMLElement,
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  const action = target.dataset.codeAction ?? "";
  const path = target.dataset.codePath ?? "";
  if (action === "toggle-filter") {
    const state = ensureCodeState(workspaceID);
    state.showIgnored = !state.showIgnored;
    scheduleWorkspaceSearch(workspaceID, callbacks, 0);
    callbacks.render();
    return;
  }
  if (action === "refresh-tree") {
    captureCodeTreeScroll(workspaceID);
    const state = ensureCodeState(workspaceID);
    state.directories.clear();
    state.expandedPaths = new Set(["."]);
    patchCodeTree(workspaceID, callbacks);
    await loadDirectory(workspaceID, ".");
    patchCodeTree(workspaceID, callbacks);
    return;
  }
  if (action === "toggle-directory") {
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
    return;
  }
  if (action === "activate-tab") {
    activateCodeTab(workspaceID, path, callbacks);
    return;
  }
  if (action === "activate-switcher-tab") {
    activateCodeTab(workspaceID, path, callbacks);
    return;
  }
  if (action === "close-tab") {
    closeCodeTab(workspaceID, path, callbacks);
    return;
  }
  if (action === "save-active-file") {
    await saveActiveCodeFile(workspaceID, callbacks);
  }
}

function handleSearchInput(
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

function scheduleWorkspaceSearch(
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

async function openCodeFile(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
  options: { temporary: boolean },
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
    const nextTab = {
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

async function openPinnedCodeFile(
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

async function loadDirectory(workspaceID: string, path: string) {
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

function closeCodeTab(
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

function pinCodeTab(
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

async function mountActiveCodeEditor(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  const mount = document.querySelector<HTMLElement>("[data-code-editor-mount]");
  const tab = activeCodeTab(workspaceID);
  destroyCodeEditor();
  if (!mount || !tab) {
    return;
  }

  const token = ++editorMountToken;
  const extensions = [
    basicSetup,
    ...tabIndentionExtensions(),
    EditorState.lineSeparator.of(tab.lineSeparator),
    EditorView.lineWrapping,
    codeEditorTheme,
    syntaxHighlighting(codeHighlightStyle),
    inlineCodeChatExtension(workspaceID, tab.path, callbacks),
    EditorView.updateListener.of((update) => {
      if (update.selectionSet || update.docChanged) {
        updateTabEditorState(workspaceID, tab.path, update.view);
      }
      if (!update.docChanged) {
        return;
      }
      updateTabContent(
        workspaceID,
        tab.path,
        editorStateToFileContent(update.state),
      );
    }),
  ];
  const language = await languageExtensionForPath(tab.path);
  if (token !== editorMountToken) {
    return;
  }
  if (language) {
    extensions.push(language);
  }
  const docLength = tab.content.length;
  const selectionAnchor = clamp(tab.selectionAnchor, 0, docLength);
  const selectionHead = clamp(tab.selectionHead, 0, docLength);
  mountedEditor = new EditorView({
    state: EditorState.create({
      doc: tab.content,
      selection: { anchor: selectionAnchor, head: selectionHead },
      extensions,
    }),
    parent: mount,
  });
  mountedEditorWorkspaceID = workspaceID;
  mountedEditorPath = tab.path;
  mountedEditor.scrollDOM.scrollTop = tab.scrollTop;
  mountedEditor.scrollDOM.scrollLeft = tab.scrollLeft;
  mountedEditor.focus();
}

function inlineCodeChatExtension(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  const field = StateField.define<DecorationSet>({
    create(state) {
      return inlineCodeChatDecorations(state, workspaceID, path, callbacks);
    },
    update(decorations, transaction) {
      decorations = decorations.map(transaction.changes);
      const chat = inlineChatForPath(workspaceID, path);
      if (chat && transaction.docChanged) {
        chat.anchorPosition = transaction.changes.mapPos(chat.anchorPosition);
      }
      for (const effect of transaction.effects) {
        if (effect.is(clearInlineCodeChatEffect)) {
          decorations = Decoration.none;
        }
        if (effect.is(setInlineCodeChatEffect)) {
          decorations = inlineCodeChatDecorations(
            transaction.state,
            workspaceID,
            path,
            callbacks,
          );
        }
      }
      return decorations;
    },
    provide: (field) => EditorView.decorations.from(field),
  });

  return [
    field,
    Prec.highest(keymap.of([
      {
        key: "Ctrl-i",
        preventDefault: true,
        run(view) {
          openInlineCodeChat(workspaceID, path, view);
          return true;
        },
      },
      {
        key: "Mod-i",
        preventDefault: true,
        run(view) {
          openInlineCodeChat(workspaceID, path, view);
          return true;
        },
      },
    ])),
  ];
}

function inlineCodeChatDecorations(
  editorState: EditorState,
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
): DecorationSet {
  const chat = inlineChatForPath(workspaceID, path);
  if (!chat) {
    return Decoration.none;
  }
  const anchorPosition = clamp(chat.anchorPosition, 0, editorState.doc.length);
  const line = editorState.doc.lineAt(anchorPosition);
  return Decoration.set([
    Decoration.widget({
      widget: new InlineCodeChatWidget(workspaceID, path, callbacks, chat.renderKey),
      block: true,
      side: 1,
    }).range(line.to),
  ]);
}

class InlineCodeChatWidget extends WidgetType {
  constructor(
    private readonly workspaceID: string,
    private readonly path: string,
    private readonly callbacks: CodeViewCallbacks,
    private readonly renderKey: number,
  ) {
    super();
  }

  eq(other: WidgetType) {
    return (
      other instanceof InlineCodeChatWidget &&
      other.workspaceID === this.workspaceID &&
      other.path === this.path &&
      other.renderKey === this.renderKey
    );
  }

  toDOM() {
    const chat = inlineChatForPath(this.workspaceID, this.path);
    const root = document.createElement("div");
    root.className = "inline-code-chat";
    root.dataset.inlineCodeChat = "";
    root.dataset.inlineWorkspaceId = this.workspaceID;
    root.dataset.inlinePath = this.path;
    if (!chat) {
      return root;
    }
    root.dataset.inlineRequestId = chat.requestID;

    const form = document.createElement("form");
    form.className = "inline-code-chat-form";

    const textarea = document.createElement("textarea");
    textarea.className = "inline-code-chat-input";
    textarea.rows = Math.max(2, Math.min(7, chat.draft.split("\n").length));
    textarea.placeholder = "Ask about this code...";
    textarea.value = chat.draft;
    textarea.disabled = chat.submitting;
    textarea.setAttribute("aria-label", "Inline code prompt");

    const submit = document.createElement("button");
    submit.className = "primary-button inline-code-chat-submit";
    submit.type = "submit";
    submit.disabled = chat.submitting || !chat.draft.trim();
    submit.textContent = chat.submitting ? "Sending" : "Send";

    const canSubmitDraft = () => {
      const latest = inlineChatForPath(this.workspaceID, this.path);
      return Boolean(latest && !latest.submitting && latest.draft.trim());
    };
    const syncSubmitState = () => {
      const latest = inlineChatForPath(this.workspaceID, this.path);
      submit.disabled = !latest || latest.submitting || !latest.draft.trim();
    };

    textarea.addEventListener("input", () => {
      const latest = inlineChatForPath(this.workspaceID, this.path);
      if (latest) {
        latest.draft = textarea.value;
      }
      syncSubmitState();
    });
    textarea.addEventListener("keydown", (event) => {
      if (event.key !== "Enter" || event.shiftKey || event.isComposing) {
        return;
      }
      if (!canSubmitDraft()) {
        return;
      }
      event.preventDefault();
      form.requestSubmit(submit);
    });

    const actions = document.createElement("div");
    actions.className = "inline-code-chat-actions";

    const close = document.createElement("button");
    close.className = "secondary-button inline-code-chat-close";
    close.type = "button";
    close.textContent = "Close";
    close.addEventListener("click", () => {
      closeInlineCodeChat(this.workspaceID, this.path);
    });

    actions.append(submit, close);
    form.append(textarea, actions);
    form.addEventListener("submit", (event) => {
      event.preventDefault();
      void submitInlineCodeChat(this.workspaceID, this.path, this.callbacks);
    });

    root.append(form);
    if (chat.error) {
      const error = document.createElement("div");
      error.className = "inline-code-chat-error";
      error.textContent = chat.error;
      root.append(error);
    }
    if (chat.response) {
      const response = document.createElement("div");
      response.className = "inline-code-chat-response markdown-body";
      response.dataset.inlineCodeResponse = "";
      response.innerHTML = renderMarkdown(chat.response);
      root.append(response);
    }
    return root;
  }

  ignoreEvent() {
    return true;
  }
}

function inlineChatForPath(workspaceID: string, path: string) {
  const chat = ensureCodeState(workspaceID).inlineChat;
  if (!chat || chat.path !== path) {
    return null;
  }
  return chat;
}

function openInlineCodeChat(
  workspaceID: string,
  path: string,
  view: EditorView,
) {
  const selection = view.state.selection.main;
  const line = view.state.doc.lineAt(selection.head);
  const previous = inlineChatForPath(workspaceID, path);
  ensureCodeState(workspaceID).inlineChat = {
    path,
    anchorPosition: selection.head,
    selectedText: selectedEditorText(view),
    draft: previous?.draft ?? "",
    submitting: false,
    response: "",
    error: "",
    requestID: previous?.requestID ?? "",
    renderKey: nextInlineChatRenderKey(),
  };
  view.dispatch({ effects: setInlineCodeChatEffect.of(line.to) });
  focusInlineCodeChatInput();
}

function closeInlineCodeChat(workspaceID: string, path: string) {
  const state = ensureCodeState(workspaceID);
  if (state.inlineChat?.path !== path) {
    return;
  }
  state.inlineChat = null;
  if (mountedEditor && mountedEditorWorkspaceID === workspaceID && mountedEditorPath === path) {
    mountedEditor.dispatch({ effects: clearInlineCodeChatEffect.of(undefined) });
    mountedEditor.focus();
  }
}

export function applyInlineCodePromptEvent(event: InlineCodePromptEvent) {
  const chat = inlineChatForPath(event.workspaceId, event.filePath);
  if (!chat || !event.requestId || chat.requestID !== event.requestId) {
    return;
  }
  if (event.type === "token") {
    chat.response = `${chat.response ?? ""}${event.content ?? ""}`;
    chat.error = "";
    patchInlineCodeChatResponse(event.workspaceId, event.filePath, chat);
    return;
  }
  if (event.type === "error") {
    chat.submitting = false;
    chat.error = event.error ?? "Inline code prompt failed.";
    refreshInlineCodeChatWidget(event.workspaceId, event.filePath);
  }
}

async function submitInlineCodeChat(
  workspaceID: string,
  path: string,
  callbacks: CodeViewCallbacks,
) {
  const state = ensureCodeState(workspaceID);
  const chat = inlineChatForPath(workspaceID, path);
  if (!chat || chat.submitting) {
    return;
  }
  const prompt = chat.draft.trim();
  if (!prompt) {
    return;
  }
  const view = mountedEditorWorkspaceID === workspaceID && mountedEditorPath === path ? mountedEditor : null;
  if (!view) {
    callbacks.pushToast("Open the file before sending the inline prompt.", "error");
    return;
  }
  const requestID = nextInlinePromptRequestID();
  const request = buildInlineCodePromptRequest(view, path, chat, prompt, requestID);
  const tab = findTab(workspaceID, path);
  if (tab?.dirty) {
    const confirmed = window.confirm("Save this file before sending the inline prompt?");
    if (!confirmed) {
      return;
    }
    await saveActiveCodeFile(workspaceID, callbacks);
    const savedTab = findTab(workspaceID, path);
    if (savedTab?.dirty) {
      return;
    }
  }

  chat.submitting = true;
  chat.response = "";
  chat.error = "";
  chat.requestID = requestID;
  refreshInlineCodeChatWidget(workspaceID, path);

  try {
    const response = services.InlineCodePromptResponse.createFrom(
      await SubmitInlineCodePrompt(
        workspaceID,
        services.InlineCodePromptRequest.createFrom(request),
      ),
    );
    const latest = state.inlineChat;
    if (!latest || latest.path !== path || latest.requestID !== requestID) {
      return;
    }
    const reloaded = await reloadInlineCodePromptTabs(
      workspaceID,
      path,
      response.affectedPaths ?? [],
      (response.toolCalls ?? []).length > 0,
      callbacks,
    );
    const content = (response.content ?? "").trim();
    if (!content) {
      state.inlineChat = null;
      if (reloaded) {
        callbacks.render();
      } else {
        refreshInlineCodeChatWidget(workspaceID, path);
      }
      return;
    }
    latest.submitting = false;
    latest.response = content;
    latest.error = "";
    latest.renderKey = nextInlineChatRenderKey();
    if (reloaded) {
      callbacks.render();
    } else {
      refreshInlineCodeChatWidget(workspaceID, path);
    }
  } catch (error) {
    const latest = inlineChatForPath(workspaceID, path);
    if (!latest || latest.requestID !== requestID) {
      return;
    }
    latest.submitting = false;
    latest.error = callbacks.errorMessage(error);
    latest.renderKey = nextInlineChatRenderKey();
    refreshInlineCodeChatWidget(workspaceID, path);
  }
}

function buildInlineCodePromptRequest(
  view: EditorView,
  path: string,
  chat: InlineCodeChatState,
  prompt: string,
  requestID: string,
) {
  const doc = view.state.doc;
  const anchorPosition = clamp(chat.anchorPosition, 0, doc.length);
  const anchorLineInfo = doc.lineAt(anchorPosition);
  const anchorLine = anchorLineInfo.number;
  let startLine = clamp(anchorLine - inlineSnippetContextLines, 1, doc.lines);
  let endLine = clamp(anchorLine + inlineSnippetContextLines, 1, doc.lines);
  let contextSubstring = substringFromDocLines(doc, startLine, endLine);
  while (
    new TextEncoder().encode(contextSubstring).length > inlineSnippetMaxBytes &&
    (startLine < anchorLine || endLine > anchorLine)
  ) {
    if (endLine > anchorLine) {
      endLine--;
      contextSubstring = substringFromDocLines(doc, startLine, endLine);
    }
    if (
      new TextEncoder().encode(contextSubstring).length <= inlineSnippetMaxBytes ||
      (startLine >= anchorLine && endLine <= anchorLine)
    ) {
      break;
    }
    if (startLine < anchorLine) {
      startLine++;
      contextSubstring = substringFromDocLines(doc, startLine, endLine);
    }
  }
  if (new TextEncoder().encode(contextSubstring).length > inlineSnippetMaxBytes) {
    contextSubstring = contextSubstring.slice(0, inlineSnippetMaxBytes);
  }
  return {
    requestId: requestID,
    filePath: path,
    prompt,
    cursorToken: tokenAroundLineOffset(anchorLineInfo.text, anchorPosition - anchorLineInfo.from),
    cursorLineText: anchorLineInfo.text,
    focusSubstring: substringAroundDocPosition(doc, anchorPosition, inlineFocusSubstringMaxBytes),
    contextSubstring,
    selectedText: chat.selectedText,
  };
}

function tokenAroundLineOffset(lineText: string, offset: number) {
  let probe = clamp(offset, 0, lineText.length);
  if (!isIdentifierCharacter(lineText.charAt(probe)) && probe > 0 && isIdentifierCharacter(lineText.charAt(probe - 1))) {
    probe--;
  }
  if (!isIdentifierCharacter(lineText.charAt(probe))) {
    return "";
  }

  let start = probe;
  while (start > 0 && isIdentifierCharacter(lineText.charAt(start - 1))) {
    start--;
  }
  let end = probe;
  while (end < lineText.length && isIdentifierCharacter(lineText.charAt(end))) {
    end++;
  }
  return lineText.slice(start, end);
}

function isIdentifierCharacter(character: string) {
  return /^[\p{L}\p{N}_$]$/u.test(character);
}

function substringAroundDocPosition(
  doc: Text,
  position: number,
  maxBytes: number,
) {
  let start = clamp(position - Math.floor(maxBytes / 2), 0, doc.length);
  let end = clamp(position + Math.ceil(maxBytes / 2), 0, doc.length);
  let substring = doc.sliceString(start, end);
  const encoder = new TextEncoder();
  while (encoder.encode(substring).length > maxBytes && (start < position || end > position)) {
    if (end > position) {
      end--;
      substring = doc.sliceString(start, end);
    }
    if (encoder.encode(substring).length <= maxBytes || (start >= position && end <= position)) {
      break;
    }
    if (start < position) {
      start++;
      substring = doc.sliceString(start, end);
    }
  }
  return substring;
}

function substringFromDocLines(
  doc: Text,
  startLine: number,
  endLine: number,
) {
  const lines: string[] = [];
  for (let lineNumber = startLine; lineNumber <= endLine; lineNumber++) {
    lines.push(doc.line(lineNumber).text);
  }
  return lines.join("\n");
}

function selectedEditorText(view: EditorView) {
  const selection = view.state.selection.main;
  if (selection.empty) {
    return "";
  }
  return view.state.doc.sliceString(selection.from, selection.to);
}

async function reloadInlineCodePromptTabs(
  workspaceID: string,
  promptedPath: string,
  paths: string[],
  usedTools: boolean,
  callbacks: CodeViewCallbacks,
) {
  saveMountedEditorContent();
  let reloaded = false;
  const affectedPaths = new Set(paths.map((path) => path.trim()).filter(Boolean));
  for (const path of uniqueInlineReloadPaths(promptedPath, paths)) {
    const tab = findTab(workspaceID, path);
    if (!tab) {
      continue;
    }
    if (tab.dirty) {
      callbacks.pushToast(`${path} changed on disk; unsaved editor content was left open.`, "info");
      continue;
    }
    try {
      const waitForChange =
        affectedPaths.has(path) ||
        (affectedPaths.size === 0 && usedTools && path === promptedPath);
      const file = await readWorkspaceFileUntilChanged(workspaceID, tab, waitForChange);
      if (!workspaceFileChanged(tab, file)) {
        continue;
      }
      applySavedFile(workspaceID, file);
      const reloadedTab = findTab(workspaceID, path);
      replaceMountedEditorContent(workspaceID, path, reloadedTab?.content ?? editableWorkspaceFile(file).content);
      reloaded = true;
    } catch (error) {
      callbacks.pushToast(callbacks.errorMessage(error), "error");
    }
  }
  return reloaded;
}

async function readWorkspaceFileUntilChanged(
  workspaceID: string,
  tab: CodeFileTab,
  waitForChange: boolean,
) {
  let file = services.WorkspaceFile.createFrom(await ReadWorkspaceFile(workspaceID, tab.path));
  if (!waitForChange || workspaceFileChanged(tab, file)) {
    return file;
  }
  for (const delay of inlineReloadRetryDelays) {
    await sleep(delay);
    file = services.WorkspaceFile.createFrom(await ReadWorkspaceFile(workspaceID, tab.path));
    if (workspaceFileChanged(tab, file)) {
      return file;
    }
  }
  return file;
}

function uniqueInlineReloadPaths(promptedPath: string, affectedPaths: string[]) {
  const paths = new Set<string>();
  const add = (path: string) => {
    const trimmed = path.trim();
    if (trimmed) {
      paths.add(trimmed);
    }
  };
  add(promptedPath);
  affectedPaths.forEach(add);
  return paths;
}

function workspaceFileChanged(
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

function refreshInlineCodeChatWidget(workspaceID: string, path: string) {
  if (!mountedEditor || mountedEditorWorkspaceID !== workspaceID || mountedEditorPath !== path) {
    return;
  }
  const chat = inlineChatForPath(workspaceID, path);
  if (!chat) {
    mountedEditor.dispatch({ effects: clearInlineCodeChatEffect.of(undefined) });
    return;
  }
  chat.renderKey = nextInlineChatRenderKey();
  const line = mountedEditor.state.doc.lineAt(clamp(chat.anchorPosition, 0, mountedEditor.state.doc.length));
  mountedEditor.dispatch({ effects: setInlineCodeChatEffect.of(line.to) });
  focusInlineCodeChatInput();
}

function patchInlineCodeChatResponse(
  workspaceID: string,
  path: string,
  chat: InlineCodeChatState,
) {
  if (!mountedEditor || mountedEditorWorkspaceID !== workspaceID || mountedEditorPath !== path) {
    return;
  }
  const root =
    Array.from(document.querySelectorAll<HTMLElement>("[data-inline-code-chat]")).find((candidate) =>
      candidate.dataset.inlineWorkspaceId === workspaceID &&
      candidate.dataset.inlinePath === path &&
      candidate.dataset.inlineRequestId === chat.requestID,
    ) ?? null;
  if (!root) {
    refreshInlineCodeChatWidget(workspaceID, path);
    return;
  }
  let response = root.querySelector<HTMLElement>("[data-inline-code-response]");
  if (!response) {
    response = document.createElement("div");
    response.className = "inline-code-chat-response markdown-body";
    response.dataset.inlineCodeResponse = "";
    root.append(response);
  }
  patchChildrenFromHtml(response, renderMarkdown(chat.response));
}

function focusInlineCodeChatInput() {
  window.setTimeout(() => {
    const input = document.querySelector<HTMLTextAreaElement>(".inline-code-chat-input");
    if (!input || input.disabled) {
      return;
    }
    input.focus();
    input.setSelectionRange(input.value.length, input.value.length);
  }, 0);
}

function nextInlineChatRenderKey() {
  inlineChatRenderSeq++;
  return inlineChatRenderSeq;
}

function nextInlinePromptRequestID() {
  inlinePromptRequestSeq++;
  return `inline-${Date.now()}-${inlinePromptRequestSeq}`;
}

async function languageExtensionForPath(path: string) {
  const fileName = path.split("/").pop() ?? path;
  const extension = fileName.includes(".")
    ? fileName.split(".").pop()?.toLowerCase() ?? ""
    : "";
  const match = languageData.find((language) => {
    if (language.filename?.test(fileName)) {
      return true;
    }
    return extension !== "" && language.extensions.includes(extension);
  });
  if (!match) {
    return null;
  }
  try {
    return await match.load();
  } catch {
    return null;
  }
}

function renderFileList(workspaceID: string): string {
  const state = ensureCodeState(workspaceID);
  if (state.searchQuery.trim()) {
    return renderSearchResults(workspaceID);
  }
  return renderDirectoryEntries(workspaceID, ".", 0);
}

function patchCodeTree(workspaceID: string, callbacks: CodeViewCallbacks) {
  const tree = document.querySelector<HTMLElement>("[data-code-tree]");
  if (!tree) {
    callbacks.render();
    return;
  }
  tree.innerHTML = renderFileList(workspaceID);
  restoreCodeTreeScroll(workspaceID);
  bindCodeActionEvents(tree, workspaceID, callbacks);
  bindCodeFileRowEvents(tree, workspaceID, callbacks);
}

function renderSearchResults(workspaceID: string): string {
  const state = ensureCodeState(workspaceID);
  if (state.searchLoading) {
    return `<div class="code-tree-note"><span class="spinner" aria-hidden="true"></span><span>Searching...</span></div>`;
  }
  if (!state.searchResults.length) {
    return `<div class="code-tree-note">No matches.</div>`;
  }
  const results = state.searchResults
    .map((entry) => renderSearchEntry(workspaceID, state, entry))
    .join("");
  return `
    <div class="code-search-results">
      ${state.searchTruncated ? `<div class="code-tree-note">Showing first 200 matches.</div>` : ""}
      ${results}
    </div>
  `;
}

function renderSearchEntry(
  workspaceID: string,
  state: CodeWorkspaceState,
  entry: services.WorkspaceFileEntry,
): string {
  const active = state.activePath === entry.path;
  const icon = entry.kind === "directory" ? codeIcons.folder : codeIcons.file;
  if (entry.kind !== "file") {
    return `
      <div class="code-tree-row code-tree-search-row" role="treeitem" title="${escapeAttribute(entry.path)}" style="--tree-depth: 0">
        <span class="code-tree-spacer"></span>
        <span class="code-tree-entry-icon">${icon}</span>
        <span class="code-tree-search-name">
          <strong>${escapeHtml(entry.name)}</strong>
          <span>${escapeHtml(entry.path)}</span>
        </span>
        <span class="code-tree-size">Folder</span>
      </div>
    `;
  }
  return `
    <button
      class="code-tree-row code-tree-file code-tree-search-row ${active ? "is-active" : ""}"
      type="button"
      role="treeitem"
      title="${escapeAttribute(entry.path)}"
      style="--tree-depth: 0"
      data-code-file-row
      data-code-path="${escapeAttribute(entry.path)}"
    >
      <span class="code-tree-spacer"></span>
      <span class="code-tree-entry-icon">${icon}</span>
      <span class="code-tree-search-name">
        <strong>${escapeHtml(entry.name)}</strong>
        <span>${escapeHtml(entry.path)}</span>
      </span>
      <span class="code-tree-size">${escapeHtml(formatBytes(entry.bytes ?? 0))}</span>
    </button>
  `;
}

function renderDirectoryEntries(
  workspaceID: string,
  path: string,
  depth: number,
): string {
  const state = ensureCodeState(workspaceID);
  const directory = directoryStateFor(state, path);
  if (directory.loading && !directory.loaded) {
    return `<div class="code-tree-note"><span class="spinner" aria-hidden="true"></span><span>Loading files...</span></div>`;
  }
  if (directory.error) {
    return `<div class="code-tree-error">${escapeHtml(directory.error)}</div>`;
  }
  if (!directory.loaded) {
    return `<div class="code-tree-note">Open Code to load files.</div>`;
  }

  const entries = filteredEntries(state, directory.entries);
  if (!entries.length) {
    return `<div class="code-tree-note">No files.</div>`;
  }
  return entries
    .map((entry) => renderFileEntry(workspaceID, state, entry, depth))
    .join("");
}

function renderFileEntry(
  workspaceID: string,
  state: CodeWorkspaceState,
  entry: services.WorkspaceFileEntry,
  depth: number,
): string {
  const active = state.activePath === entry.path;
  if (entry.kind === "directory") {
    const expanded = state.expandedPaths.has(entry.path);
    const childDirectory = directoryStateFor(state, entry.path);
    return `
      <div class="code-tree-item">
        <button
          class="code-tree-row code-tree-directory ${expanded ? "is-expanded" : ""}"
          type="button"
          role="treeitem"
          aria-expanded="${expanded}"
          style="--tree-depth: ${depth}"
          data-code-action="toggle-directory"
          data-code-path="${escapeAttribute(entry.path)}"
        >
          <span class="code-tree-chevron">${codeIcons.chevron}</span>
          <span class="code-tree-entry-icon">${codeIcons.folder}</span>
          <span class="code-tree-name">${escapeHtml(entry.name)}</span>
        </button>
        ${
          expanded
            ? `<div role="group">
                ${
                  childDirectory.loading && !childDirectory.loaded
                    ? `<div class="code-tree-note nested" style="--tree-depth: ${depth + 1}"><span class="spinner" aria-hidden="true"></span><span>Loading...</span></div>`
                    : renderDirectoryEntries(workspaceID, entry.path, depth + 1)
                }
              </div>`
            : ""
        }
      </div>
    `;
  }
  return `
    <button
      class="code-tree-row code-tree-file ${active ? "is-active" : ""}"
      type="button"
      role="treeitem"
      title="${escapeAttribute(entry.path)}"
      style="--tree-depth: ${depth}"
      data-code-file-row
      data-code-path="${escapeAttribute(entry.path)}"
    >
      <span class="code-tree-spacer"></span>
      <span class="code-tree-entry-icon">${codeIcons.file}</span>
      <span class="code-tree-name">${escapeHtml(entry.name)}</span>
      <span class="code-tree-size">${escapeHtml(formatBytes(entry.bytes ?? 0))}</span>
    </button>
  `;
}

function renderCodeTabs(workspaceID: string): string {
  const state = ensureCodeState(workspaceID);
  if (!state.tabs.length) {
    return `<div class="code-tabs is-empty"></div>`;
  }
  return `
    <div class="code-tabs" role="tablist" aria-label="Open files">
      ${state.tabs
        .map((tab) => {
          const active = state.activePath === tab.path;
          return `
            <div class="code-tab ${active ? "is-active" : ""} ${tab.dirty ? "is-dirty" : ""} ${tab.temporary ? "is-temporary" : ""}" data-code-tab="${escapeAttribute(tab.path)}">
              <button class="code-tab-main" type="button" role="tab" aria-selected="${active}" title="${escapeAttribute(tab.path)}" data-code-action="activate-tab" data-code-tab-main data-code-path="${escapeAttribute(tab.path)}">
                <span>${escapeHtml(fileName(tab.path))}</span>
                ${tab.dirty ? `<span class="dirty-dot" aria-label="Unsaved changes"></span>` : ""}
              </button>
              <button class="code-tab-close" type="button" title="Close ${escapeAttribute(fileName(tab.path))}" aria-label="Close ${escapeAttribute(fileName(tab.path))}" data-code-action="close-tab" data-code-path="${escapeAttribute(tab.path)}">
                ${codeIcons.close}
              </button>
            </div>
          `;
        })
        .join("")}
    </div>
  `;
}

function renderCodeTabSwitcher(workspaceID: string): string {
  const state = ensureCodeState(workspaceID);
  const switcher = state.tabSwitcher;
  if (!switcher || switcher.paths.length <= 1) {
    return "";
  }
  const tabsByPath = new Map(state.tabs.map((tab) => [tab.path, tab]));
  return `
    <div class="code-tab-switcher" role="listbox" aria-label="Open file tabs">
      ${switcher.paths
        .map((path, index) => {
          const tab = tabsByPath.get(path);
          if (!tab) {
            return "";
          }
          const selected = index === switcher.selectedIndex;
          return `
            <button
              class="code-tab-switcher-item ${selected ? "is-selected" : ""}"
              type="button"
              role="option"
              aria-selected="${selected}"
              title="${escapeAttribute(tab.path)}"
              data-code-action="activate-switcher-tab"
              data-code-path="${escapeAttribute(tab.path)}"
            >
              <span class="code-tab-switcher-name">${escapeHtml(fileName(tab.path))}</span>
              <span class="code-tab-switcher-path">${escapeHtml(tab.path)}</span>
              ${tab.dirty ? `<span class="dirty-dot" aria-label="Unsaved changes"></span>` : ""}
            </button>
          `;
        })
        .join("")}
    </div>
  `;
}

function renderCodeStatus(tab: CodeFileTab | null, openingPath: string): string {
  if (openingPath) {
    return `Opening ${escapeHtml(openingPath)}...`;
  }
  if (!tab) {
    return "No file selected.";
  }
  const state = tab.saving ? "Saving" : tab.dirty ? "Unsaved changes" : "Saved";
  return `${escapeHtml(tab.path)} - ${escapeHtml(formatBytes(tab.bytes))} - ${state}`;
}

function updateTabContent(workspaceID: string, path: string, content: string) {
  const tab = findTab(workspaceID, path);
  if (!tab) {
    return;
  }
  tab.content = content;
  tab.bytes = new TextEncoder().encode(content).length;
  tab.dirty = tab.content !== tab.savedContent;
  if (tab.temporary && tab.dirty) {
    tab.temporary = false;
  }
  tab.selectionAnchor = clamp(tab.selectionAnchor, 0, tab.content.length);
  tab.selectionHead = clamp(tab.selectionHead, 0, tab.content.length);
  patchDirtyUI(workspaceID, tab);
}

function updateTabEditorState(workspaceID: string, path: string, view: EditorView) {
  const tab = findTab(workspaceID, path);
  if (!tab) {
    return;
  }
  const selection = view.state.selection.main;
  tab.selectionAnchor = selection.anchor;
  tab.selectionHead = selection.head;
  tab.scrollTop = view.scrollDOM.scrollTop;
  tab.scrollLeft = view.scrollDOM.scrollLeft;
}

function replaceMountedEditorContent(workspaceID: string, path: string, content: string) {
  if (!mountedEditor || mountedEditorWorkspaceID !== workspaceID || mountedEditorPath !== path) {
    return;
  }
  const scrollTop = mountedEditor.scrollDOM.scrollTop;
  const scrollLeft = mountedEditor.scrollDOM.scrollLeft;
  const selection = mountedEditor.state.selection.main;
  mountedEditor.dispatch({
    changes: { from: 0, to: mountedEditor.state.doc.length, insert: content },
    selection: {
      anchor: clamp(selection.anchor, 0, content.length),
      head: clamp(selection.head, 0, content.length),
    },
    annotations: Transaction.addToHistory.of(false),
  });
  mountedEditor.scrollDOM.scrollTop = scrollTop;
  mountedEditor.scrollDOM.scrollLeft = scrollLeft;
  updateTabEditorState(workspaceID, path, mountedEditor);
}

function patchDirtyUI(workspaceID: string, tab: CodeFileTab) {
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
  if (activeCodeTab(workspaceID)?.path !== tab.path) {
    return;
  }
  const save = document.querySelector<HTMLButtonElement>("[data-code-save]");
  if (save) {
    save.disabled = !tab.dirty || tab.saving;
    save.innerHTML = `${tab.saving ? `<span class="spinner" aria-hidden="true"></span>` : codeIcons.save}<span>Save</span>`;
  }
  const dirtySummary = document.querySelector<HTMLElement>("[data-code-dirty-summary]");
  if (dirtySummary) {
    const dirtyCount = ensureCodeState(workspaceID).tabs.filter((candidate) => candidate.dirty).length;
    dirtySummary.textContent = dirtyCount ? `${dirtyCount} unsaved` : "Files";
  }
  const status = document.querySelector<HTMLElement>("[data-code-status]");
  if (status) {
    const state = tab.saving ? "Saving" : tab.dirty ? "Unsaved changes" : "Saved";
    status.textContent = `${tab.path} - ${formatBytes(tab.bytes)} - ${state}`;
  }
}

function saveMountedEditorContent() {
  if (!mountedEditor || !mountedEditorWorkspaceID || !mountedEditorPath) {
    return;
  }
  updateTabEditorState(mountedEditorWorkspaceID, mountedEditorPath, mountedEditor);
  updateTabContent(
    mountedEditorWorkspaceID,
    mountedEditorPath,
    editorStateToFileContent(mountedEditor.state),
  );
}

function applySavedFile(workspaceID: string, file: services.WorkspaceFile) {
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
  tab.selectionAnchor = clamp(tab.selectionAnchor, 0, tab.content.length);
  tab.selectionHead = clamp(tab.selectionHead, 0, tab.content.length);
}

function sleep(delay: number) {
  return new Promise<void>((resolve) => window.setTimeout(resolve, delay));
}

async function waitForOpeningPath(workspaceID: string, path: string) {
  for (let attempt = 0; attempt < 20; attempt++) {
    if (ensureCodeState(workspaceID).openingPath !== path) {
      return;
    }
    await sleep(25);
  }
}

function activateCodeTab(
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

function activeCodeTab(workspaceID: string): CodeFileTab | null {
  const state = ensureCodeState(workspaceID);
  return state.tabs.find((tab) => tab.path === state.activePath) ?? null;
}

function findTab(workspaceID: string, path: string): CodeFileTab | null {
  return ensureCodeState(workspaceID).tabs.find((tab) => tab.path === path) ?? null;
}

function tabSwitcherPaths(state: CodeWorkspaceState): string[] {
  pruneTabMruPaths(state);
  if (state.activePath && !state.tabMruPaths.includes(state.activePath)) {
    return [state.activePath, ...state.tabMruPaths];
  }
  return [...state.tabMruPaths];
}

function promoteTabMruPath(state: CodeWorkspaceState, path: string) {
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

function removeTabMruPath(state: CodeWorkspaceState, path: string) {
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

function pruneTabMruPaths(state: CodeWorkspaceState) {
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

function wrapIndex(index: number, length: number): number {
  if (length <= 0) {
    return 0;
  }
  return ((index % length) + length) % length;
}

function directoryStateFor(
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

function filteredEntries(
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

function captureCodeTreeScroll(workspaceID: string) {
  const tree = document.querySelector<HTMLElement>("[data-code-tree]");
  if (!tree) {
    return;
  }
  ensureCodeState(workspaceID).codeTreeScrollTop = tree.scrollTop;
}

function restoreCodeTreeScroll(workspaceID: string) {
  const tree = document.querySelector<HTMLElement>("[data-code-tree]");
  if (!tree) {
    return;
  }
  tree.scrollTop = ensureCodeState(workspaceID).codeTreeScrollTop;
}

function patchSearchResults(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  const state = ensureCodeState(workspaceID);
  state.preservingSearchFocus = state.searchFocused;
  patchCodeTree(workspaceID, callbacks);
  state.preservingSearchFocus = false;
}

function startExplorerResize(event: PointerEvent, workspaceID: string) {
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

function storedExplorerWidth(): number {
  const raw = Number(localStorage.getItem(explorerWidthStorageKey));
  if (!Number.isFinite(raw) || raw <= 0) {
    return defaultExplorerWidth;
  }
  return clamp(raw, minExplorerWidth, maxExplorerWidth);
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(max, Math.max(min, value));
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  if (bytes < 1024 * 1024) {
    return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`;
  }
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function fileName(path: string): string {
  return path.split("/").pop() || path;
}

function detectLineSeparator(content: string): string {
  let crlf = 0;
  let lf = 0;
  for (let index = 0; index < content.length; index++) {
    const char = content[index];
    if (char === "\r") {
      if (content[index + 1] === "\n") {
        crlf++;
        index++;
      } else {
        lf++;
      }
    } else if (char === "\n") {
      lf++;
    }
  }
  return crlf > 0 && crlf >= lf ? "\r\n" : "\n";
}

function editableWorkspaceFile(file: services.WorkspaceFile) {
  const lineSeparator = detectLineSeparator(file.content);
  const content = normalizeEditorLineBreaks(file.content, lineSeparator);
  return {
    content,
    lineSeparator,
    bytes: new TextEncoder().encode(content).length,
  };
}

function normalizeEditorLineBreaks(content: string, lineSeparator: string): string {
  const normalized = content.replace(/\r\n?|\n|\u0085|\u2028|\u2029/g, "\n");
  return lineSeparator === "\r\n" ? normalized.replaceAll("\n", "\r\n") : normalized;
}

function editorStateToFileContent(state: EditorState): string {
  return state.sliceDoc(0);
}

function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function escapeAttribute(value: string): string {
  return escapeHtml(value).replaceAll("`", "&#096;");
}
