import { Virtualizer, elementScroll, observeElementOffset, observeElementRect } from "@tanstack/virtual-core";
import type { editor as MonacoEditor, IPosition as MonacoPosition, IRange as MonacoRange, languages as MonacoLanguages, Uri as MonacoUri } from "monaco-editor";
import { api } from "../../js/api.js";
import { openAddWorkspaceModal, openWorkspaceDropdown } from "../../js/workspaces.js";
import { on as onSocket, onState as onSocketState, send as sendSocket } from "../../js/ws.js";
import * as editorAPI from "./editorApi";
import type { APIError } from "./editorApi";
import { languageForPath, monaco } from "./language";
import { EchoLSPClient, fromLSPRange, type LSPDiagnosticSeverity, type LSPDocumentState } from "./lspClient";
import type { LSPProfile, LSPStatus, LSPWorkspaceEdit, WorkspaceLSPResponse } from "./lspTypes";
import { loadDiff as loadGitDiff } from "./gitApi";
import type { GitChange, GitDiffDocument, GitRepository } from "./gitTypes";
import { GitView } from "./gitView";
import {
  buildCodeChatEditorContext, formatCodeChatSelectionNotice, runCodeChatSavePreflight,
} from "./codeChatContext";
import { loadSession, saveSession } from "./persistence";
import { previewKindForPath, type PreviewKind } from "./preview";
import {
  CODE_ROUTE, chatCompletionTargetFromHash, codeOpenTargetFromHash, codeRouteHash,
  codeSidebarFromHash, routePathFromHash, type ChatCompletionTarget, type CodeSidebar,
} from "../navigation";
import { renderMobilePrimaryNav, renderPrimaryNav } from "../primaryNav";
import { setGitBadgeCount } from "../gitBadge";
import { randomUUID } from "../randomUUID";
import {
  mountChatSurface, type EditorContextPayload, type EditorContextSelection, type MountedChatSurface,
} from "../chatSurface";
import type { ChatReference } from "../chatMentions";
import { detachTerminalDock, mountTerminalDock } from "../terminal";
import { SearchView } from "./searchView";
import { NavigationModelCache } from "./navigationModelCache";
import { explorerDiagnosticPresentation, updateExplorerDiagnostic } from "./explorerDiagnostics";
import {
  CodeNavigationHistory, isLargeCodeNavigationJump, type CodeNavigationLocation,
} from "./codeNavigationHistory";
import { beginMruCycle, nextMruCycle, pruneMruCycle, removeFromMru, type MruCycleState } from "./mruTabOrder";
import type {
  FileRef, FileSnapshot, FsEntry, PersistedTab, PersistedWorkspaceSession,
  SearchResult, TextReplaceUpdate, TextSearchMatch, TextSearchOverlay, TrashItem, WorkspaceRoot,
} from "./types";
import { isRefWithin, joinRef, refKey } from "./types";
import {
  choiceDialog, closeContextMenu, copyText, escapeHTML, installMenuDismissal,
  promptDialog, showContextMenu, toast,
} from "./ui";
import { attachVideoVolumeControl } from "../mediaVolume";

type Workspace = { id: string; name: string; mainPath: string; folders: string[]; iconExt?: string };

type TreeNode = {
  key: string;
  ref: FileRef;
  name: string;
  hostPath: string;
  kind: "file" | "directory";
  isRoot: boolean;
  isSymlink: boolean;
  blockedReason?: string;
  depth: number;
  parentKey: string | null;
  loaded: boolean;
  loading: boolean;
  children: string[];
};

type OpenTab = {
  kind: "file" | "diff" | "media";
  id: string;
  ref: FileRef | null;
  title: string;
  hostPath: string;
  pinned: boolean;
  dirty: boolean;
  deleted: boolean;
  conflict: boolean;
  revision: string;
  hasBom: boolean;
  eol: "lf" | "crlf";
  model: MonacoEditor.ITextModel;
  viewState: MonacoEditor.ICodeEditorViewState | null;
  changeDisposable: { dispose(): void };
  applying: boolean;
  media?: { kind: PreviewKind; url: string };
  diff?: {
    repository: GitRepository;
    scope: "staged" | "unstaged" | "commit" | "stash";
    reviewRef?: string;
    fileRef?: FileRef;
    oldPath?: string;
    originalModel: MonacoEditor.ITextModel;
    viewState: MonacoEditor.IDiffEditorViewState | null;
    editable: boolean;
    unavailableReason?: string;
  };
};

type Command = { id: string; label: string; keybinding?: string; run(): unknown | Promise<unknown> };

const treeRowHeight = 22;
const minEditorFontSize = 8;
const maxEditorFontSize = 30;
const defaultEditorFontSize = 13.5;
let mountedView: CodeView | null = null;

export function mount(root: HTMLElement): void {
  mountedView = new CodeView(root);
  void mountedView.start();
}

export function unmount(): void {
  mountedView?.dispose();
  mountedView = null;
}

export function routeChanged(): void {
  mountedView?.handleRouteChange();
}

class CodeView {
  private readonly root: HTMLElement;
  private readonly abort = new AbortController();
  private workspaces: Workspace[] = [];
  private workspace: Workspace | null = null;
  private roots: WorkspaceRoot[] = [];
  private nodes = new Map<string, TreeNode>();
  private flatTree: TreeNode[] = [];
  private expanded = new Set<string>();
  private selectedTreeKey: string | null = null;
  private renamingKey: string | null = null;
  private draggingTreeKey: string | null = null;
  private treeDropTargetKey: string | null = null;
  private treeDropExpandKey: string | null = null;
  private treeDropExpandTimer = 0;
  private treeDragScrollFrame = 0;
  private treeDragScrollClientY = 0;
  private treeDragScrollActive = false;
  private tabs: OpenTab[] = [];
  private activeTabId: string | null = null;
  private mruTabIds: string[] = [];
  private mruCycle: MruCycleState | null = null;
  private mruSwitcherOverlay: HTMLElement | null = null;
  private untitledCounter = 1;
  private editor!: MonacoEditor.IStandaloneCodeEditor;
  private diffEditor!: MonacoEditor.IStandaloneDiffEditor;
  private gitView: GitView | null = null;
  private searchView: SearchView | null = null;
  private activeSidebar: CodeSidebar = "explorer";
  private splitGitDiff = true;
  private leadingWhitespaceIndicators = true;
  private editorFontSize = 13.5;
  private fullSettings: Record<string, unknown> = {};
  private editorFontSizeSaveTimer = 0;
  private modelReferences = new Map<MonacoEditor.ITextModel, number>();
  private treeScroller!: HTMLElement;
  private treeCanvas!: HTMLElement;
  private treeVirtualizer!: Virtualizer<HTMLElement, HTMLElement>;
  private disposeVirtualizer: (() => void) | null = null;
  private persistTimer = 0;
  private persistenceFailed = false;
  private explorerWidth = 280;
  private codeChatWidth = 360;
  private codeChatOpen = false;
  private codeChatSurface: MountedChatSurface | null = null;
  private diffSelectionSides = new Map<string, "original" | "modified">();
  private restoredTreeScrollTop = 0;
  private explorerRevealGeneration = 0;
  private lastSequence = 0;
  private pollTimer = 0;
  private commands: Command[] = [];
  private closeWorkspaceDropdown: (() => void) | null = null;
  private closeAddWorkspaceModal: (() => void) | null = null;
  private workspaceSwitching = false;
  private mediaTheme = window.matchMedia("(prefers-color-scheme: dark)");
  private openTarget: FileRef | null;
  private completionTarget: ChatCompletionTarget | null;
  private lsp: EchoLSPClient | null = null;
  private lspProfiles: LSPProfile[] = [];
  private lspState: LSPDocumentState = "none";
  private lspStatus: LSPStatus | undefined;
  private fileDiagnostics = new Map<string, LSPDiagnosticSeverity>();
  private readonly navigationModels = new NavigationModelCache<MonacoEditor.ITextModel>(6);
  private editorOpener: { dispose(): void } | null = null;
  private codeNavigation: CodeNavigationHistory | null = null;
  private navigationReady = false;
  private navigationSuppression = 0;
  private navigationRestoreGeneration = 0;
  private navigationSkipping = false;
  private lastNavigationLocation: CodeNavigationLocation | null = null;

  constructor(root: HTMLElement) {
    this.root = root;
    this.activeSidebar = codeSidebarFromHash(window.location.hash);
    this.openTarget = codeOpenTargetFromHash(window.location.hash);
    this.completionTarget = chatCompletionTargetFromHash(window.location.hash);
    if (this.completionTarget?.surface !== "code") this.completionTarget = null;
  }

  async start(): Promise<void> {
    try {
      const data = await api("/api/workspaces", { method: "GET" }) as { workspaces: Workspace[]; activeId: string };
      if (this.abort.signal.aborted) return;
      this.workspaces = data.workspaces || [];
      const targetWorkspace = this.completionTarget
        ? data.workspaces.find((workspace) => workspace.id === this.completionTarget!.workspaceId)
        : null;
      if (this.completionTarget && !targetWorkspace) {
        toast("The workspace for that completed chat is no longer available.", { sticky: true });
        this.completionTarget = null;
        window.history.replaceState(window.history.state, "", codeRouteHash(this.activeSidebar));
      }
      if (targetWorkspace && targetWorkspace.id !== data.activeId) {
        await api("/api/workspaces/active", { method: "PUT", body: { id: targetWorkspace.id } });
        window.dispatchEvent(new CustomEvent("echo:workspace-changed", { detail: { workspaceId: targetWorkspace.id } }));
      }
      this.workspace = targetWorkspace || data.workspaces.find((workspace) => workspace.id === data.activeId) || null;
      this.renderShell();
      this.installNavigation();
      installMenuDismissal(this.abort.signal);
      if (!this.workspace) {
        this.showNoWorkspace();
        return;
      }
      const [roots, settingsData, lspData] = await Promise.all([
        editorAPI.getRoots(this.workspace.id),
        api("/api/settings", { method: "GET" }).catch(() => null) as Promise<{ settings?: { disableGitSplitDiffView?: boolean; hideLeadingWhitespaceIndicators?: boolean; editorFontSize?: number } } | null>,
        editorAPI.getWorkspaceLSPConfig(this.workspace.id).catch(() => ({ config: {}, profiles: [], statuses: [] } as WorkspaceLSPResponse)),
      ]);
      if (this.abort.signal.aborted) return;
      this.roots = roots;
      this.splitGitDiff = settingsData?.settings?.disableGitSplitDiffView !== true;
      this.leadingWhitespaceIndicators = settingsData?.settings?.hideLeadingWhitespaceIndicators !== true;
      if (settingsData?.settings) this.fullSettings = { ...(settingsData.settings as Record<string, unknown>) };
      this.editorFontSize = this.clampEditorFontSize((settingsData?.settings?.editorFontSize as number | undefined) || 13.5);
      this.lspProfiles = lspData.profiles || [];
      this.codeNavigation = new CodeNavigationHistory(this.workspace.id, { createId: randomUUID });
      const historyLocation = this.openTarget ? null : this.codeNavigation.initialLocation();
      this.initializeEditor();
      this.lsp = new EchoLSPClient({
        workspaceId: this.workspace.id,
        initial: lspData,
        prepareWorkspaceEdit: (edit) => this.prepareLSPWorkspaceEdit(edit),
        applyWorkspaceEdit: (edit) => this.applyLSPWorkspaceEdit(edit),
        isURIAllowed: (uri) => Boolean(this.refForFileURI(uri)),
        diagnosticKey: (uri) => {
          const ref = this.refForFileURI(uri);
          return ref ? refKey(ref) : uri;
        },
        prepareURI: (uri) => this.prepareLSPURI(uri),
        onDocumentState: (state, status) => {
          this.lspState = state;
          this.lspStatus = status;
          this.renderStatus();
        },
        onDiagnosticsChange: (uri, severity) => this.updateFileDiagnostic(uri, severity),
        onMessage: (message, sticky) => toast(message, { sticky }),
      });
      this.initializeTree();
      this.registerCommands();
      this.installEvents();
      this.initializeGitView();
      this.initializeSearchView();
      await this.restoreWorkspace();
      if (this.abort.signal.aborted) return;
      if (this.roots.length) {
        await Promise.all(this.roots.map((root) => this.ensureRoot(root)));
      }
      if (this.abort.signal.aborted) return;
      await this.restoreTreeExpansion();
      if (this.abort.signal.aborted) return;
      this.renderTree();
      requestAnimationFrame(() => {
        if (!this.abort.signal.aborted) this.treeScroller.scrollTop = this.restoredTreeScrollTop;
      });
      if (this.openTarget) {
        const target = this.openTarget;
        this.openTarget = null;
        await this.openFile(target, true, false);
        if (this.abort.signal.aborted) return;
        await this.expandTo(target);
        if (this.abort.signal.aborted) return;
        window.history.replaceState(window.history.state, "", codeRouteHash("explorer"));
      }
      if (historyLocation) await this.restoreNavigationLocation(historyLocation);
      this.navigationReady = true;
      const initialNavigationLocation = this.captureNavigationLocation();
      if (initialNavigationLocation) {
        this.codeNavigation.attachInitial(initialNavigationLocation);
        this.lastNavigationLocation = initialNavigationLocation;
      }
      this.renderTabs();
      this.updateEditorSurface();
      this.subscribeFilesystem();
      if (this.completionTarget) {
        this.setCodeChatOpen(true);
      }
    } catch (error) {
      if (this.abort.signal.aborted) return;
      console.error("code view startup failed", error);
      this.showFatal(error);
    }
  }

  private renderShell(): void {
    const workspaceName = this.workspace?.name || "No workspace";
    const workspaceIconUrl = this.workspace?.iconExt
      ? `/api/workspaces/${encodeURIComponent(this.workspace.id)}/icon`
      : undefined;
    const explorerActive = this.activeSidebar === "explorer";
    this.root.innerHTML = `
      <div class="code-app-shell" style="--explorer-width:${this.explorerWidth}px;--code-chat-width:${this.codeChatWidth}px">
        ${renderPrimaryNav({
          active: this.activeSidebar,
          workspaceName,
          workspaceSelector: true,
          workspaceIconUrl,
        })}
        <section class="code-workbench">
          <div class="code-sidebar-backdrop" data-mobile-sidebar-backdrop aria-hidden="true"></div>
          <div class="code-sidebar">
          <aside class="code-explorer" aria-label="Explorer" data-sidebar-view="explorer"${explorerActive ? "" : " hidden"}>
            <header class="code-explorer-header">
              <span>EXPLORER</span>
              <div class="code-header-actions">
                <button type="button" title="New File" aria-label="New File" data-tree-action="new-file"><span class="codicon codicon-new-file"></span></button>
                <button type="button" title="New Folder" aria-label="New Folder" data-tree-action="new-folder"><span class="codicon codicon-new-folder"></span></button>
                <button type="button" title="Refresh Explorer" aria-label="Refresh Explorer" data-tree-action="refresh"><span class="codicon codicon-refresh"></span></button>
                <button type="button" title="Collapse All" aria-label="Collapse All" data-tree-action="collapse-all"><span class="codicon codicon-collapse-all"></span></button>
                <button type="button" title="Trash" aria-label="Trash" data-tree-action="trash"><span class="codicon codicon-trash"></span></button>
              </div>
            </header>
            <div class="code-workspace-title" title="${escapeHTML(workspaceName)}"><span class="codicon codicon-chevron-down"></span><strong>${escapeHTML(workspaceName)}</strong></div>
            <div class="code-tree" role="tree" aria-label="Workspace files" tabindex="0" data-code-tree>
              <div class="code-tree-canvas" data-tree-canvas></div>
            </div>
          </aside>
          <aside class="code-search-view" aria-label="Search" data-sidebar-view="search"${this.activeSidebar === "search" ? "" : " hidden"}></aside>
          <aside class="code-git-view" aria-label="Source Control" data-sidebar-view="git"${this.activeSidebar === "git" ? "" : " hidden"}></aside>
          </div>
          <div class="code-explorer-resizer" role="separator" aria-orientation="vertical" aria-label="Resize Explorer" tabindex="0"></div>
          <main class="code-editor-column">
            <div class="code-tabs-scroll" role="tablist" aria-label="Open editors" data-code-tabs><div class="code-tabs" data-tabs-list></div></div>
            <div class="code-editor-workspace">
              <div class="code-editor-pane">
                <nav class="code-breadcrumbs" aria-label="Breadcrumb" data-breadcrumbs>
                  <span class="code-breadcrumb-path" data-breadcrumb-path></span>
                  <button type="button" class="code-chat-toggle" title="Open Code Chat" aria-label="Open code assistant" aria-expanded="false" aria-controls="code-chat-dock" data-code-chat-toggle><span class="codicon codicon-comment-discussion"></span></button>
                </nav>
                <section class="code-editor-area">
                  <div class="code-editor-placeholder" data-editor-placeholder>
                    <span class="codicon codicon-code"></span>
                    <h2>Echo Code</h2>
                    <p>Open a file from Explorer or press <kbd>Ctrl+P</kbd>.</p>
                  </div>
                  <div class="code-monaco-host" data-monaco-host></div>
                  <div class="code-diff-toolbar" data-diff-toolbar hidden>
                    <span data-diff-label></span>
                    <button type="button" title="Previous Change" aria-label="Previous Change" data-diff-action="previous"><span class="codicon codicon-arrow-up"></span></button>
                    <button type="button" title="Next Change" aria-label="Next Change" data-diff-action="next"><span class="codicon codicon-arrow-down"></span></button>
                    <button type="button" title="Toggle Inline Diff" aria-label="Toggle Inline Diff" data-diff-action="layout"><span class="codicon codicon-layout"></span></button>
                  </div>
                  <div class="code-monaco-diff-host" data-monaco-diff-host hidden></div>
                  <div class="code-media-preview" data-media-preview-host hidden></div>
                  <div class="code-diff-unavailable" data-diff-unavailable hidden></div>
                </section>
                <footer class="code-statusbar" data-statusbar>
                  <div><button type="button" class="code-mobile-explorer" data-mobile-explorer aria-label="Toggle Sidebar" aria-expanded="false"><span class="codicon codicon-${this.sidebarIcon(this.activeSidebar)}"></span></button></div>
                  <div class="code-status-right"><button type="button" data-status="lsp" hidden>LSP</button><span data-status="cursor">Ln 1, Col 1</span><span>Spaces: 2</span><span>UTF-8</span><span data-status="eol">LF</span><span data-status="language">Plain Text</span></div>
                </footer>
              </div>
              <div class="code-chat-resizer" role="separator" aria-orientation="vertical" aria-label="Resize Code Chat" aria-valuemin="300" aria-valuemax="640" tabindex="0" hidden></div>
              <aside class="code-chat-dock" id="code-chat-dock" aria-label="Code Chat" hidden data-code-chat-dock></aside>
              <div class="code-chat-backdrop" data-code-chat-backdrop aria-hidden="true"></div>
            </div>
          </main>
        </section>
        <div data-region="terminal"></div>
        ${renderMobilePrimaryNav({ active: this.activeSidebar, workspaceName, workspaceSelector: true })}
      </div>
    `;
    mountTerminalDock(this.root.querySelector<HTMLElement>("[data-region=terminal]"), this.workspace);
  }

  private installNavigation(): void {
    const signal = this.abort.signal;
    this.root.querySelectorAll("[data-nav=chat]").forEach((button) => {
      button.addEventListener("click", () => { location.hash = "#/home"; }, { signal });
    });
    this.root.querySelectorAll("[data-nav=settings]").forEach((button) => {
      button.addEventListener("click", () => { location.hash = "#/settings"; }, { signal });
    });
    this.root.querySelectorAll<HTMLElement>("[data-code-sidebar]").forEach((button) => {
      button.addEventListener("click", () => {
        const view = button.dataset.codeSidebar;
        this.setSidebar(view === "git" || view === "search" ? view : "explorer");
      }, { signal });
    });
    this.root.querySelectorAll("[data-nav=workspace]:not(.workspace-dropdown-trigger)").forEach((button) => {
      button.addEventListener("click", () => { location.hash = "#/home"; }, { signal });
    });
    this.root.querySelectorAll<HTMLElement>(".workspace-dropdown-trigger").forEach((trigger) => {
      trigger.addEventListener("click", (event) => {
        event.stopPropagation();
        if (this.closeWorkspaceDropdown) {
          this.closeWorkspaceDropdown();
          return;
        }
        this.closeWorkspaceDropdown = openWorkspaceDropdown(trigger, {
          items: this.workspaces,
          selectedId: this.workspace?.id || "",
          onClose: () => { this.closeWorkspaceDropdown = null; },
          onSelect: (id: string) => { void this.switchWorkspace(id); },
          onAdd: () => {
            this.closeAddWorkspaceModal = openAddWorkspaceModal({
              onCreate: (workspace: Workspace) => { void this.switchWorkspace(workspace.id); },
            });
          },
        });
      }, { signal });
    });
  }

  private initializeGitView(): void {
    if (!this.workspace) return;
    const host = this.root.querySelector<HTMLElement>("[data-sidebar-view=git]");
    if (!host) return;
    this.gitView = new GitView(host, this.workspace.id, this.abort.signal, {
      roots: () => this.roots,
      openFile: async (ref, pin) => { await this.recordCodeNavigation(() => this.openFile(ref, pin)); },
      openDiff: async (repository, change, scope, ref, pin) => {
        await this.recordCodeNavigation(() => this.openGitDiff(repository, change, scope, ref, pin));
      },
      updateBadge: (count) => setGitBadgeCount(this.root, count),
    });
    void this.gitView.start();
  }

  private initializeSearchView(): void {
    if (!this.workspace) return;
    const host = this.root.querySelector<HTMLElement>("[data-sidebar-view=search]");
    if (!host) return;
    this.searchView = new SearchView(host, {
      workspaceId: this.workspace.id,
      signal: this.abort.signal,
      getOverlays: () => this.searchOverlays(),
      openResult: (ref, match, pin) => this.openSearchResult(ref, match, pin),
      confirmReplace: (details) => this.confirmSearchReplace(details),
      applyUpdates: (updates) => this.applySearchUpdates(updates),
      focusEditor: () => this.focusActiveEditor(),
    });
    if (this.activeSidebar === "search") this.searchView.open();
  }

  handleRouteChange(): void {
    if (routePathFromHash(window.location.hash) !== CODE_ROUTE) return;
    this.setSidebar(codeSidebarFromHash(window.location.hash), false);
  }

  private setSidebar(view: CodeSidebar, updateRoute = true): void {
    this.activeSidebar = view;
    this.root.querySelectorAll<HTMLElement>("[data-sidebar-view]").forEach((element) => { element.hidden = element.dataset.sidebarView !== view; });
    this.root.querySelectorAll<HTMLElement>("[data-code-sidebar]").forEach((button) => {
      const active = button.dataset.codeSidebar === view;
      button.classList.toggle("is-active", active);
      if (button.classList.contains("mobile-nav-tab")) {
        if (active) button.setAttribute("aria-current", "page");
        else button.removeAttribute("aria-current");
      }
    });
    const mobile = this.root.querySelector<HTMLElement>("[data-mobile-explorer] .codicon");
    if (mobile) mobile.className = `codicon codicon-${this.sidebarIcon(view)}`;
    if (updateRoute && routePathFromHash(window.location.hash) === CODE_ROUTE) {
      window.history.replaceState(window.history.state, "", codeRouteHash(view));
    }
    if (window.innerWidth <= 720) this.setMobileExplorer(true);
    if (view === "search") this.searchView?.open();
  }

  private sidebarIcon(view: CodeSidebar): string {
    if (view === "git") return "source-control";
    if (view === "search") return "search";
    return "files";
  }

  private searchOverlays(): TextSearchOverlay[] {
    const overlays: TextSearchOverlay[] = [];
    const seen = new Set<string>();
    for (const tab of this.tabs) {
      const ref = this.worktreeRef(tab);
      if (!ref || !tab.dirty) continue;
      const key = refKey(ref);
      if (seen.has(key)) continue;
      seen.add(key);
      overlays.push({ ref, revision: tab.revision, content: tab.model.getValue(), hasBom: tab.hasBom });
    }
    return overlays;
  }

  private async openSearchResult(ref: FileRef, match: TextSearchMatch, pin: boolean): Promise<void> {
    await this.recordCodeNavigation(async () => {
      await this.openFile(ref, pin);
      const tab = this.tabs.find((candidate) => {
        const candidateRef = this.worktreeRef(candidate);
        return candidateRef && refKey(candidateRef) === refKey(ref);
      });
      if (!tab) return;
      this.activateTab(tab.id, false);
      const target = tab.kind === "diff" ? this.diffEditor.getModifiedEditor() : this.editor;
      target.setSelection({
        startLineNumber: match.line, startColumn: match.column,
        endLineNumber: match.endLine, endColumn: match.endColumn,
      });
      target.revealRangeInCenter({
        startLineNumber: match.line, startColumn: match.column,
        endLineNumber: match.endLine, endColumn: match.endColumn,
      });
      target.focus();
    });
  }

  private async confirmSearchReplace(details: {
    scope: "match" | "file" | "all";
    matches: number;
    files: number;
    dirtyFiles: number;
  }): Promise<boolean> {
    const dirty = details.dirtyFiles
      ? ` ${details.dirtyFiles} affected file${details.dirtyFiles === 1 ? " has" : "s have"} unsaved edits; those complete buffers will be saved.`
      : "";
    const choice = await choiceDialog({
      title: details.scope === "all" ? "Replace all workspace results?" : "Save unsaved edits and replace?",
      message: `Replace ${details.matches.toLocaleString()} result${details.matches === 1 ? "" : "s"} in ${details.files.toLocaleString()} file${details.files === 1 ? "" : "s"}.${dirty}`,
      choices: [
        { id: "cancel", label: "Cancel" },
        { id: "replace", label: details.scope === "all" ? "Replace All" : "Replace", danger: true, primary: true },
      ],
    });
    return choice === "replace";
  }

  private async applySearchUpdates(updates: TextReplaceUpdate[]): Promise<void> {
    if (!this.workspace) return;
    for (const update of updates) {
      const tabs = this.tabs.filter((candidate) => {
        const candidateRef = this.worktreeRef(candidate);
        return candidateRef && refKey(candidateRef) === refKey(update.ref);
      });
      if (!tabs.length) continue;
      let snapshot: FileSnapshot;
      if (update.content !== undefined) {
        snapshot = {
          ref: update.ref, hostPath: tabs[0].hostPath, content: update.content,
          revision: update.revision, size: update.size, modifiedAt: update.modifiedAt,
          encoding: "utf-8", eol: update.eol, hasBom: update.hasBom,
        };
      } else {
        snapshot = await editorAPI.readFile(this.workspace.id, update.ref);
      }
      const models = new Set<MonacoEditor.ITextModel>();
      for (const tab of tabs) {
        if (models.has(tab.model)) continue;
        models.add(tab.model);
        this.applyDiskSnapshot(tab, snapshot);
      }
    }
    this.renderTabs();
    this.schedulePersist();
  }

  private focusActiveEditor(): void {
    const tab = this.activeTab();
    if (tab?.kind === "diff") this.diffEditor.getModifiedEditor().focus();
    else this.editor?.focus();
  }

  private showWorkspaceSearch(replace = false): void {
    let seed = "";
    const tab = this.activeTab();
    const editor = tab?.kind === "diff" ? this.diffEditor.getModifiedEditor() : this.editor;
    const selection = editor?.getSelection();
    if (selection && selection.startLineNumber === selection.endLineNumber && !selection.isEmpty()) {
      seed = editor.getModel()?.getValueInRange(selection) || "";
    }
    this.setSidebar("search");
    this.searchView?.open({ replace, seed });
  }

  private async switchWorkspace(workspaceId: string): Promise<void> {
    if (!workspaceId || workspaceId === this.workspace?.id || this.workspaceSwitching) return;
    this.workspaceSwitching = true;
    const hasDirtyBuffers = this.tabs.some((tab) => tab.dirty);
    const persisted = await this.persistNow();
    if (!persisted && hasDirtyBuffers) {
      this.workspaceSwitching = false;
      return;
    }
    try {
      await api("/api/workspaces/active", { method: "PUT", body: { id: workspaceId } });
      window.location.reload();
    } catch (error) {
      this.workspaceSwitching = false;
      toast(error instanceof Error ? error.message : String(error), { sticky: true });
    }
  }

  private showNoWorkspace(): void {
    const placeholder = this.root.querySelector<HTMLElement>("[data-editor-placeholder]");
    if (!placeholder) return;
    const choices = this.workspaces.map((workspace) => `
      <button type="button" class="code-workspace-choice" data-workspace-choice="${escapeHTML(workspace.id)}">
        <strong>${escapeHTML(workspace.name)}</strong><span>${escapeHTML(workspace.mainPath)}</span>
      </button>`).join("");
    placeholder.innerHTML = `<span class="codicon codicon-folder-opened"></span><h2>Open a workspace</h2>
      ${choices ? `<p>Choose a workspace for Echo Code.</p><div class="code-workspace-chooser">${choices}</div>` : `<p>Add a workspace from Chat before opening Echo Code.</p>`}
      <button type="button" class="code-primary-button" data-go-chat>${choices ? "Manage Workspaces" : "Go to Chat"}</button>`;
    placeholder.querySelector("[data-go-chat]")?.addEventListener("click", () => { location.hash = "#/home"; });
    placeholder.querySelectorAll<HTMLElement>("[data-workspace-choice]").forEach((button) => {
      button.addEventListener("click", async () => {
        try {
          await api("/api/workspaces/active", { method: "PUT", body: { id: button.dataset.workspaceChoice } });
          location.reload();
        } catch (error) {
          toast(error instanceof Error ? error.message : String(error));
        }
      });
    });
  }

  private showFatal(error: unknown): void {
    const message = error instanceof Error ? error.message : String(error);
    const target = this.root.querySelector<HTMLElement>(".code-workbench") || this.root;
    target.innerHTML = `<div class="code-fatal"><span class="codicon codicon-error"></span><h2>Could not open Echo Code</h2><p>${escapeHTML(message)}</p><button type="button" class="code-primary-button">Retry</button></div>`;
    target.querySelector("button")?.addEventListener("click", () => location.reload());
  }

  private initializeEditor(): void {
    const host = this.root.querySelector<HTMLElement>("[data-monaco-host]")!;
    this.editor = monaco.editor.create(host, {
      model: null,
      theme: this.mediaTheme.matches ? "vs-dark" : "vs",
      automaticLayout: true,
      fontFamily: "Cascadia Code, JetBrains Mono, Consolas, monospace",
      fontSize: this.editorFontSize,
      lineHeight: Math.round(this.editorFontSize * 1.48),
      lineNumbers: "on",
      minimap: { enabled: true, maxColumn: 120, renderCharacters: true, showSlider: "mouseover" },
      folding: true,
      foldingHighlight: true,
      glyphMargin: false,
      renderLineHighlight: "line",
      renderWhitespace: this.leadingWhitespaceIndicators ? "boundary" : "none",
      bracketPairColorization: { enabled: true, independentColorPoolPerBracketType: true },
      guides: { indentation: true, bracketPairs: true, highlightActiveIndentation: true },
      stickyScroll: { enabled: true },
      smoothScrolling: false,
      cursorSmoothCaretAnimation: "off",
      wordWrap: "off",
      selectionHighlight: true,
      selectionHighlightMultiline: false,
      occurrencesHighlight: "singleFile",
      padding: { top: 4, bottom: 4 },
      scrollBeyondLastLine: true,
      fixedOverflowWidgets: true,
      gotoLocation: {
        multipleDefinitions: "peek",
        multipleTypeDefinitions: "peek",
        multipleDeclarations: "peek",
        multipleImplementations: "peek",
        multipleReferences: "peek",
        alternativeDefinitionCommand: "editor.action.referenceSearch.trigger",
        alternativeTypeDefinitionCommand: "editor.action.referenceSearch.trigger",
        alternativeDeclarationCommand: "editor.action.referenceSearch.trigger",
      },
    });
    this.editor.onDidChangeCursorPosition(() => { this.renderStatus(); this.observeNavigationLocation(true); });
    this.editor.onDidChangeCursorSelection(() => this.updateCodeChatSelectionNotice());
    this.editor.onDidScrollChange(() => { this.observeNavigationLocation(false); this.schedulePersist(); });
    const diffHost = this.root.querySelector<HTMLElement>("[data-monaco-diff-host]")!;
    this.diffEditor = monaco.editor.createDiffEditor(diffHost, {
      theme: this.mediaTheme.matches ? "vs-dark" : "vs",
      automaticLayout: true,
      fontFamily: "Cascadia Code, JetBrains Mono, Consolas, monospace",
      fontSize: this.editorFontSize,
      lineHeight: Math.round(this.editorFontSize * 1.48),
      lineNumbers: "on",
      minimap: { enabled: true, maxColumn: 120, showSlider: "mouseover" },
      renderSideBySide: this.splitGitDiff,
      useInlineViewWhenSpaceIsLimited: true,
      originalEditable: false,
      readOnly: false,
      diffAlgorithm: "advanced",
      renderIndicators: true,
      renderOverviewRuler: true,
      selectionHighlight: true,
      selectionHighlightMultiline: false,
      occurrencesHighlight: "singleFile",
      ignoreTrimWhitespace: false,
      hideUnchangedRegions: { enabled: false },
      renderWhitespace: this.leadingWhitespaceIndicators ? "boundary" : "none",
      scrollBeyondLastLine: true,
      fixedOverflowWidgets: true,
      padding: { top: 4, bottom: 4 },
      gotoLocation: {
        multipleDefinitions: "peek",
        multipleTypeDefinitions: "peek",
        multipleDeclarations: "peek",
        multipleImplementations: "peek",
        multipleReferences: "peek",
        alternativeDefinitionCommand: "editor.action.referenceSearch.trigger",
        alternativeTypeDefinitionCommand: "editor.action.referenceSearch.trigger",
        alternativeDeclarationCommand: "editor.action.referenceSearch.trigger",
      },
    });
    const originalDiffEditor = this.diffEditor.getOriginalEditor();
    const modifiedDiffEditor = this.diffEditor.getModifiedEditor();
    originalDiffEditor.onDidFocusEditorText(() => this.setActiveDiffSelectionSide("original"));
    modifiedDiffEditor.onDidFocusEditorText(() => this.setActiveDiffSelectionSide("modified"));
    originalDiffEditor.onDidChangeCursorSelection(() => {
      if (originalDiffEditor.hasTextFocus()) this.setActiveDiffSelectionSide("original");
      else this.updateCodeChatSelectionNotice();
    });
    modifiedDiffEditor.onDidChangeCursorSelection(() => {
      if (modifiedDiffEditor.hasTextFocus()) this.setActiveDiffSelectionSide("modified");
      else this.updateCodeChatSelectionNotice();
    });
    modifiedDiffEditor.onDidChangeCursorPosition(() => { this.renderStatus(); this.observeNavigationLocation(true); });
    this.diffEditor.getModifiedEditor().onDidScrollChange(() => { this.observeNavigationLocation(false); this.schedulePersist(); });
    this.updateDiffLayoutState();
    this.editorOpener = monaco.editor.registerEditorOpener({
      openCodeEditor: (_source, resource, selectionOrPosition) => this.openNavigationTarget(resource, selectionOrPosition),
    });
    this.mediaTheme.addEventListener("change", () => monaco.editor.setTheme(this.mediaTheme.matches ? "vs-dark" : "vs"), { signal: this.abort.signal });
  }

  private initializeTree(): void {
    this.treeScroller = this.root.querySelector<HTMLElement>("[data-code-tree]")!;
    this.treeCanvas = this.root.querySelector<HTMLElement>("[data-tree-canvas]")!;
    this.treeVirtualizer = new Virtualizer<HTMLElement, HTMLElement>({
      count: 0,
      getScrollElement: () => this.treeScroller,
      estimateSize: () => treeRowHeight,
      getItemKey: (index) => this.flatTree[index]?.key || index,
      overscan: 10,
      observeElementRect,
      observeElementOffset,
      scrollToFn: elementScroll,
      onChange: () => this.renderTreeRows(),
    });
    this.disposeVirtualizer = this.treeVirtualizer._didMount();
    this.treeVirtualizer._willUpdate();
  }

  private async ensureRoot(root: WorkspaceRoot): Promise<void> {
    const key = refKey({ rootId: root.id, path: "" });
    if (!this.nodes.has(key)) {
      this.nodes.set(key, {
        key, ref: { rootId: root.id, path: "" }, name: root.label, hostPath: root.hostPath,
        kind: "directory", isRoot: true, isSymlink: false, depth: 0, parentKey: null,
        blockedReason: root.blockedReason, loaded: false, loading: false, children: [],
      });
    }
    this.expanded.add(key);
    await this.loadChildren(this.nodes.get(key)!);
  }

  private async loadChildren(node: TreeNode, force = false): Promise<void> {
    if (node.kind !== "directory" || node.blockedReason || node.loading || (node.loaded && !force) || !this.workspace) return;
    node.loading = true;
    this.renderTree();
    try {
      const entries = await editorAPI.listEntries(this.workspace.id, node.ref);
      for (const childKey of node.children) this.removeNodeBranch(childKey);
      node.children = entries.map((entry) => {
        const key = refKey(entry.ref);
        this.nodes.set(key, this.entryNode(entry, node));
        return key;
      });
      node.loaded = true;
    } catch (error) {
      toast(error instanceof Error ? error.message : String(error));
    } finally {
      node.loading = false;
      this.renderTree();
    }
  }

  private entryNode(entry: FsEntry, parent: TreeNode): TreeNode {
    return {
      key: refKey(entry.ref), ref: entry.ref, name: entry.name, hostPath: entry.hostPath,
      kind: entry.kind, isRoot: false, isSymlink: entry.isSymlink, blockedReason: entry.blockedReason,
      depth: parent.depth + 1, parentKey: parent.key, loaded: false, loading: false, children: [],
    };
  }

  private removeNodeBranch(key: string): void {
    const node = this.nodes.get(key);
    if (!node) return;
    node.children.forEach((child) => this.removeNodeBranch(child));
    this.nodes.delete(key);
  }

  private rebuildFlatTree(): void {
    const result: TreeNode[] = [];
    const append = (node: TreeNode) => {
      result.push(node);
      if (node.kind === "directory" && this.expanded.has(node.key)) {
        node.children.forEach((key) => {
          const child = this.nodes.get(key);
          if (child) append(child);
        });
      }
    };
    for (const root of this.roots) {
      const node = this.nodes.get(refKey({ rootId: root.id, path: "" }));
      if (node) append(node);
    }
    this.flatTree = result;
  }

  private renderTree(): void {
    if (!this.treeVirtualizer) return;
    this.rebuildFlatTree();
    this.treeVirtualizer.setOptions({
      ...this.treeVirtualizer.options,
      count: this.flatTree.length,
      getItemKey: (index) => this.flatTree[index]?.key || index,
    });
    this.treeVirtualizer._willUpdate();
    this.renderTreeRows();
  }

  private renderTreeRows(): void {
    if (!this.treeCanvas || !this.treeVirtualizer) return;
    const items = this.treeVirtualizer.getVirtualItems();
    this.treeCanvas.style.height = `${this.treeVirtualizer.getTotalSize()}px`;
    this.treeCanvas.innerHTML = items.map((virtual) => {
      const node = this.flatTree[virtual.index];
      if (!node) return "";
      const selected = node.key === this.selectedTreeKey;
      const expanded = this.expanded.has(node.key);
      const isDirectory = node.kind === "directory";
      const diagnostic = isDirectory ? undefined : this.fileDiagnostics.get(node.key);
      const diagnosticPresentation = explorerDiagnosticPresentation(diagnostic);
      const indent = 7 + node.depth * 14;
      const icon = isDirectory
        ? (expanded ? "folder-opened" : "folder")
        : this.fileIcon(node.name);
      const chevron = isDirectory
        ? `<span class="codicon codicon-${node.loading ? "loading codicon-modifier-spin" : expanded ? "chevron-down" : "chevron-right"}"></span>`
        : `<span class="code-tree-spacer"></span>`;
      const label = this.renamingKey === node.key
        ? `<input class="code-tree-rename" data-rename-input value="${escapeHTML(node.name)}" aria-label="Rename ${escapeHTML(node.name)}">`
        : `<span class="code-tree-label${diagnosticPresentation.className ? ` ${diagnosticPresentation.className}` : ""}">${escapeHTML(node.name)}</span>`;
      const draggable = !node.isRoot && !node.blockedReason;
      const dragging = node.key === this.draggingTreeKey;
      const dropTarget = node.key === this.treeDropTargetKey;
      const ariaLabel = diagnosticPresentation.description ? `${node.name}, ${diagnosticPresentation.description}` : node.name;
      const title = node.blockedReason || (diagnosticPresentation.description ? `${node.hostPath} — ${diagnosticPresentation.description}` : node.hostPath);
      return `<div class="code-tree-row ${selected ? "is-selected" : ""} ${node.blockedReason ? "is-blocked" : ""} ${dragging ? "is-dragging" : ""} ${dropTarget ? "is-drop-target" : ""}" role="treeitem" aria-label="${escapeHTML(ariaLabel)}" aria-selected="${selected}" aria-expanded="${isDirectory ? expanded : undefined}" draggable="${draggable}" data-tree-key="${escapeHTML(node.key)}" data-tree-kind="${node.kind}" data-tree-root="${node.isRoot}" style="transform:translateY(${virtual.start}px);padding-left:${indent}px" title="${escapeHTML(title)}">${chevron}<span class="codicon codicon-${icon} code-tree-icon"></span>${label}</div>`;
    }).join("");
    if (this.renamingKey) {
      requestAnimationFrame(() => {
        const input = this.treeCanvas.querySelector<HTMLInputElement>("[data-rename-input]");
        if (!input || document.activeElement === input) return;
        input.focus();
        const dot = input.value.lastIndexOf(".");
        input.setSelectionRange(0, dot > 0 ? dot : input.value.length);
      });
    }
  }

  private fileIcon(name: string): string {
    const extension = name.split(".").pop()?.toLowerCase();
    if (["png", "jpg", "jpeg", "gif", "svg", "webp", "bmp", "ico", "avif"].includes(extension || "")) return "file-media";
    if (["mp4", "m4v", "webm", "ogv"].includes(extension || "")) return "play-circle";
    if (["mp3", "wav", "ogg", "oga", "opus", "flac", "m4a", "aac", "weba"].includes(extension || "")) return "music";
    if (["json", "yaml", "yml", "toml", "ini"].includes(extension || "")) return "json";
    if (["md", "markdown", "txt"].includes(extension || "")) return "markdown";
    return "file-code";
  }

  private updateFileDiagnostic(uri: string, severity: LSPDiagnosticSeverity | null): void {
    if (updateExplorerDiagnostic(this.fileDiagnostics, uri, severity, (candidate) => this.refForFileURI(candidate))) {
      this.renderTreeRows();
    }
  }

  private syncTreeSelectionState(): void {
    if (!this.treeCanvas) return;
    this.treeCanvas.querySelectorAll<HTMLElement>("[data-tree-key]").forEach((element) => {
      const selected = element.dataset.treeKey === this.selectedTreeKey;
      element.classList.toggle("is-selected", selected);
      element.setAttribute("aria-selected", String(selected));
    });
  }

  private collapseAll(): void {
    this.expanded.clear();
    this.renderTree();
    this.schedulePersist();
    this.sendFilesystemSubscription();
  }

  private clampEditorFontSize(value: number): number {
    if (!Number.isFinite(value) || value <= 0) return defaultEditorFontSize;
    return Math.min(maxEditorFontSize, Math.max(minEditorFontSize, value));
  }

  private setEditorFontSize(value: number): void {
    const next = this.clampEditorFontSize(value);
    if (next === this.editorFontSize) return;
    this.editorFontSize = next;
    const lineHeight = Math.round(next * 1.48);
    this.editor?.updateOptions({ fontSize: next, lineHeight });
    this.diffEditor?.updateOptions({ fontSize: next, lineHeight });
    window.clearTimeout(this.editorFontSizeSaveTimer);
    const settings = { ...this.fullSettings, editorFontSize: next };
    this.editorFontSizeSaveTimer = window.setTimeout(() => {
      void api("/api/settings", { method: "PUT", body: { settings } }).catch(() => {
        /* best-effort persistence */
      });
    }, 150);
  }

  private async toggleNode(node: TreeNode): Promise<void> {
    this.selectedTreeKey = node.key;
    this.syncTreeSelectionState();
    if (node.kind === "directory" && !node.blockedReason) {
      if (this.expanded.has(node.key)) this.expanded.delete(node.key);
      else {
        this.expanded.add(node.key);
        await this.loadChildren(node);
      }
      this.renderTree();
      this.schedulePersist();
      this.sendFilesystemSubscription();
    } else if (node.kind === "file" && !node.blockedReason) {
      await this.recordCodeNavigation(() => this.openFile(node.ref, false, false));
      if (window.innerWidth <= 720) this.setMobileExplorer(false);
    }
  }

  private createModel(snapshot: FileSnapshot, id: string): OpenTab {
    const uri = this.modelURI(snapshot.ref, snapshot.hostPath);
    const shared = this.tabs.find((candidate) => candidate.kind === "diff" && candidate.diff?.editable && candidate.diff.fileRef && refKey(candidate.diff.fileRef) === refKey(snapshot.ref));
    let prepared = this.navigationModels.take(uri.toString());
    if (!prepared) {
      const equivalent = this.navigationModels.entries().find(([cachedURI]) => {
        const cachedRef = this.refForFileURI(cachedURI);
        return cachedRef && refKey(cachedRef) === refKey(snapshot.ref);
      });
      if (equivalent) prepared = this.navigationModels.take(equivalent[0]);
    }
    const reusable = shared?.model || prepared || monaco.editor.getModel(uri);
    if (prepared && prepared !== reusable) prepared.dispose();
    const model = reusable || monaco.editor.createModel(snapshot.content, languageForPath(snapshot.ref.path, this.lspProfiles), uri);
    if (prepared === model && !shared) {
      if (model.getValue() !== snapshot.content) model.setValue(snapshot.content);
      model.setEOL(snapshot.eol === "crlf" ? monaco.editor.EndOfLineSequence.CRLF : monaco.editor.EndOfLineSequence.LF);
    }
    this.lsp?.trackModel(model);
    this.retainModel(model);
    if (!reusable) model.setEOL(snapshot.eol === "crlf" ? monaco.editor.EndOfLineSequence.CRLF : monaco.editor.EndOfLineSequence.LF);
    const tab: OpenTab = {
      kind: "file", id, ref: snapshot.ref, title: snapshot.ref.path.split("/").pop() || snapshot.ref.path,
      hostPath: snapshot.hostPath, pinned: false, dirty: shared?.dirty || false, deleted: shared?.deleted || false, conflict: shared?.conflict || false,
      revision: shared?.revision || snapshot.revision, hasBom: shared?.hasBom ?? snapshot.hasBom, eol: shared?.eol || snapshot.eol,
      model, viewState: null, changeDisposable: { dispose() {} }, applying: false,
    };
    tab.changeDisposable = model.onDidChangeContent(() => {
      if (tab.applying) return;
      this.markModelDirty(model);
    });
    return tab;
  }

  private modelURI(ref: FileRef, hostPath?: string): MonacoUri {
    if (hostPath) return monaco.Uri.file(hostPath);
    const root = this.roots.find((candidate) => candidate.id === ref.rootId);
    if (!root) throw new Error(`Workspace root ${ref.rootId} is unavailable`);
    const separator = root.hostPath.includes("\\") ? "\\" : "/";
    const joined = ref.path
      ? `${root.hostPath.replace(/[\\/]$/, "")}${separator}${ref.path.replaceAll("/", separator)}`
      : root.hostPath;
    return monaco.Uri.file(joined);
  }

  private refForFileURI(uri: string): FileRef | null {
    let hostPath: string;
    try {
      const parsed = monaco.Uri.parse(uri);
      if (parsed.scheme !== "file") return null;
      hostPath = parsed.fsPath;
    } catch {
      return null;
    }
    const normalizedTarget = hostPath.replaceAll("\\", "/").replace(/\/$/, "");
    const roots = [...this.roots].sort((left, right) => right.hostPath.length - left.hostPath.length);
    for (const root of roots) {
      const normalizedRoot = root.hostPath.replaceAll("\\", "/").replace(/\/$/, "");
      const caseInsensitive = /^[a-z]:/i.test(normalizedRoot) || root.hostPath.includes("\\");
      const target = caseInsensitive ? normalizedTarget.toLowerCase() : normalizedTarget;
      const base = caseInsensitive ? normalizedRoot.toLowerCase() : normalizedRoot;
      if (target !== base && !target.startsWith(`${base}/`)) continue;
      const path = normalizedTarget.slice(normalizedRoot.length).replace(/^\//, "");
      return { rootId: root.id, path };
    }
    return null;
  }

  private async prepareLSPURI(uri: string): Promise<boolean> {
    const ref = this.refForFileURI(uri);
    if (!ref || !ref.path) return false;
    let resource: MonacoUri;
    try {
      resource = monaco.Uri.parse(uri);
    } catch {
      return false;
    }
    const key = resource.toString();
    const ready = await this.navigationModels.ensure(
      key,
      () => monaco.editor.getModel(resource),
      async () => {
        if (!this.workspace || this.abort.signal.aborted) return null;
        try {
          const snapshot = await editorAPI.readFile(this.workspace.id, ref);
          if (this.abort.signal.aborted) return null;
          const existing = monaco.editor.getModel(resource);
          if (existing) return existing;
          const model = monaco.editor.createModel(snapshot.content, languageForPath(ref.path, this.lspProfiles), resource);
          model.setEOL(snapshot.eol === "crlf" ? monaco.editor.EndOfLineSequence.CRLF : monaco.editor.EndOfLineSequence.LF);
          return model;
        } catch {
          return null;
        }
      },
    );
    if (ready && this.navigationModels.has(key)) this.sendFilesystemSubscription();
    return ready;
  }

  private async openNavigationTarget(resource: MonacoUri, selectionOrPosition?: MonacoRange | MonacoPosition): Promise<boolean> {
    return await this.recordCodeNavigation(async () => {
      const ref = this.refForFileURI(resource.toString());
      if (!ref || !ref.path) return false;
      if (!(await this.prepareLSPURI(resource.toString()))) return false;
      await this.openFile(ref, false, false);
      const tab = this.tabs.find((candidate) => {
        const candidateRef = this.worktreeRef(candidate);
        return candidateRef && refKey(candidateRef) === refKey(ref);
      });
      if (!tab) return false;
      this.activateTab(tab.id, false);
      const editor = this.activeCodeEditor();
      if (!editor) return false;
      if (selectionOrPosition) {
        if ("endLineNumber" in selectionOrPosition) {
          const position = {
            lineNumber: selectionOrPosition.startLineNumber,
            column: selectionOrPosition.startColumn,
          };
          editor.setPosition(position);
          editor.revealPositionInCenter(position);
        } else {
          editor.setPosition(selectionOrPosition);
          editor.revealPositionInCenter(selectionOrPosition);
        }
      }
      editor.focus();
      return true;
    });
  }

  private async openLSPWorkspaceEditURI(uri: string): Promise<boolean> {
    const ref = this.refForFileURI(uri);
    if (!ref || !ref.path) return false;
    const previous = this.activeTabId;
    await this.openFile(ref, true, false);
    if (previous && this.tabs.some((tab) => tab.id === previous)) this.activateTab(previous, false);
    return this.tabs.some((tab) => {
      const candidate = this.worktreeRef(tab);
      return candidate && refKey(candidate) === refKey(ref);
    });
  }

  private async prepareLSPWorkspaceEdit(edit: LSPWorkspaceEdit): Promise<MonacoLanguages.WorkspaceEdit> {
    const pending: Array<{ uri: string; version?: number | null; edits: Array<{ range: { start: { line: number; character: number }; end: { line: number; character: number } }; newText: string }> }> = [];
    for (const [uri, edits] of Object.entries(edit.changes || {})) pending.push({ uri, edits });
    for (const change of edit.documentChanges || []) {
      if ("kind" in change) throw new Error(`Language-server resource operation "${change.kind}" is not supported by Echo yet`);
      pending.push({ uri: change.textDocument.uri, version: change.textDocument.version, edits: change.edits });
    }
    for (const item of pending) {
      if (!this.refForFileURI(item.uri)) throw new Error(`Language server tried to edit a file outside this workspace: ${item.uri}`);
      if (!(await this.openLSPWorkspaceEditURI(item.uri))) throw new Error(`Could not open language-server edit target: ${item.uri}`);
    }
    const workspaceEdits: MonacoLanguages.IWorkspaceTextEdit[] = [];
    for (const item of pending) {
      const model = this.modelForLSPWorkspaceEditURI(item.uri);
      if (!model) throw new Error(`Language-server edit target is unavailable: ${item.uri}`);
      if (typeof item.version === "number" && item.version !== model.getVersionId()) {
        throw new Error(`Language-server edit for ${model.uri.fsPath} is stale (expected version ${item.version}, current version ${model.getVersionId()})`);
      }
      for (const textEdit of item.edits) {
        workspaceEdits.push({
          resource: model.uri,
          versionId: model.getVersionId(),
          textEdit: { range: fromLSPRange(textEdit.range), text: textEdit.newText },
        });
      }
    }
    return { edits: workspaceEdits };
  }

  private modelForLSPWorkspaceEditURI(uri: string): MonacoEditor.ITextModel | null {
    try {
      const exact = monaco.editor.getModel(monaco.Uri.parse(uri));
      if (exact) return exact;
    } catch {
      return null;
    }
    const ref = this.refForFileURI(uri);
    if (!ref) return null;
    const key = refKey(ref);
    const tab = this.tabs.find((candidate) => {
      const candidateRef = this.worktreeRef(candidate);
      return candidateRef && refKey(candidateRef) === key;
    });
    if (tab && tab.kind !== "media") return tab.model;
    return monaco.editor.getModels().find((model) => {
      const candidateRef = this.refForFileURI(model.uri.toString());
      return candidateRef && refKey(candidateRef) === key;
    }) || null;
  }

  private async applyLSPWorkspaceEdit(edit: LSPWorkspaceEdit): Promise<boolean> {
    const prepared = await this.prepareLSPWorkspaceEdit(edit);
    const grouped = new Map<MonacoEditor.ITextModel, MonacoEditor.IIdentifiedSingleEditOperation[]>();
    for (const workspaceEdit of prepared.edits) {
      if (!("textEdit" in workspaceEdit)) throw new Error("Language-server resource operations are not supported by Echo yet");
      const model = monaco.editor.getModel(workspaceEdit.resource);
      if (!model || (workspaceEdit.versionId !== undefined && workspaceEdit.versionId !== model.getVersionId())) {
        throw new Error(`Language-server edit for ${workspaceEdit.resource.fsPath} became stale before it could be applied`);
      }
      const edits = grouped.get(model) || [];
      edits.push({ range: workspaceEdit.textEdit.range, text: workspaceEdit.textEdit.text, forceMoveMarkers: true });
      grouped.set(model, edits);
    }
    for (const [model, edits] of grouped) model.pushEditOperations([], edits, () => null);
    return true;
  }

  private retainModel(model: MonacoEditor.ITextModel): void {
    this.modelReferences.set(model, (this.modelReferences.get(model) || 0) + 1);
  }

  private releaseModel(model: MonacoEditor.ITextModel): void {
    const references = (this.modelReferences.get(model) || 1) - 1;
    if (references > 0) {
      this.modelReferences.set(model, references);
      return;
    }
    this.modelReferences.delete(model);
    model.dispose();
  }

  private async openFile(ref: FileRef, pin: boolean, focusEditor = true, showErrors = true): Promise<boolean> {
    if (!this.workspace) return false;
    const previewKind = previewKindForPath(ref.path);
    if (previewKind) {
      await this.openMedia(ref, pin, focusEditor);
      return false;
    }
    const existing = this.tabs.find((tab) => tab.ref && refKey(tab.ref) === refKey(ref));
    if (existing) {
      if (pin) existing.pinned = true;
      this.activateTab(existing.id, focusEditor);
      this.renderTabs();
      this.sendFilesystemSubscription();
      return true;
    }
    try {
      const snapshot = await editorAPI.readFile(this.workspace.id, ref);
      const tab = this.createModel(snapshot, randomUUID());
      tab.pinned = pin;
      if (!pin) {
        const previewIndex = this.tabs.findIndex((candidate) => !candidate.pinned && !candidate.dirty);
        if (previewIndex >= 0) {
          this.disposeTab(this.tabs[previewIndex]);
          this.tabs.splice(previewIndex, 1, tab);
        } else {
          this.tabs.push(tab);
        }
      } else {
        this.tabs.push(tab);
      }
      this.activateTab(tab.id, focusEditor);
      this.renderTabs();
      this.schedulePersist();
      this.sendFilesystemSubscription();
      return true;
    } catch (error) {
      if (!showErrors) return false;
      const apiError = error as APIError;
      const message = apiError.payload?.code === "file_too_large"
        ? "This file is larger than Echo Code's 10 MiB editor limit."
        : apiError.payload?.code === "unsupported_file"
          ? "This file is binary or is not valid UTF-8."
          : error instanceof Error ? error.message : String(error);
      const choice = await choiceDialog({
        title: "Cannot edit this file", message,
        choices: [
          { id: "cancel", label: "Close" },
          { id: "reload", label: "Reload" },
          { id: "reveal", label: "Reveal on Echo host", primary: true },
        ],
      });
      if (choice === "reload") return await this.openFile(ref, pin, focusEditor, showErrors);
      if (choice === "reveal") await this.reveal(ref);
      return false;
    }
  }

  private treeMoveDestination(source: TreeNode | undefined, target: TreeNode | undefined): TreeNode | null {
    if (!source || source.isRoot || source.blockedReason || !target || target.kind !== "directory" || target.blockedReason) return null;
    if (source.ref.rootId !== target.ref.rootId || source.parentKey === target.key || isRefWithin(target.ref, source.ref)) return null;
    return target;
  }

  private setTreeDropTarget(key: string | null): void {
    if (this.treeDropTargetKey === key) return;
    if (this.treeDropTargetKey) {
      this.treeCanvas.querySelector<HTMLElement>(`[data-tree-key="${CSS.escape(this.treeDropTargetKey)}"]`)?.classList.remove("is-drop-target");
    }
    this.treeDropTargetKey = key;
    if (key) {
      this.treeCanvas.querySelector<HTMLElement>(`[data-tree-key="${CSS.escape(key)}"]`)?.classList.add("is-drop-target");
    }
    window.clearTimeout(this.treeDropExpandTimer);
    this.treeDropExpandTimer = 0;
    this.treeDropExpandKey = null;
    const target = key ? this.nodes.get(key) : null;
    if (!target || this.expanded.has(target.key) || target.loading) return;
    this.treeDropExpandKey = target.key;
    this.treeDropExpandTimer = window.setTimeout(() => {
      this.treeDropExpandTimer = 0;
      if (this.treeDropExpandKey !== target.key || this.treeDropTargetKey !== target.key) return;
      this.expanded.add(target.key);
      void this.loadChildren(target).then(() => {
        this.schedulePersist();
        this.sendFilesystemSubscription();
      });
    }, 600);
  }

  private clearTreeDragState(): void {
    window.clearTimeout(this.treeDropExpandTimer);
    this.treeDropExpandTimer = 0;
    this.treeDropExpandKey = null;
    if (this.treeDragScrollFrame) {
      cancelAnimationFrame(this.treeDragScrollFrame);
      this.treeDragScrollFrame = 0;
    }
    this.treeDragScrollActive = false;
    this.treeCanvas.querySelectorAll(".code-tree-row.is-dragging, .code-tree-row.is-drop-target")
      .forEach((row) => row.classList.remove("is-dragging", "is-drop-target"));
    this.draggingTreeKey = null;
    this.treeDropTargetKey = null;
  }

  private startTreeDragScroll(clientY: number): void {
    this.treeDragScrollClientY = clientY;
    if (this.treeDragScrollActive) return;
    this.treeDragScrollActive = true;
    const step = () => {
      this.treeDragScrollFrame = 0;
      if (!this.treeDragScrollActive) return;
      const bounds = this.treeScroller.getBoundingClientRect();
      const edge = 28;
      const speed = 12;
      if (this.treeDragScrollClientY < bounds.top + edge) {
        this.treeScroller.scrollTop -= speed;
      } else if (this.treeDragScrollClientY > bounds.bottom - edge) {
        this.treeScroller.scrollTop += speed;
      } else {
        this.treeDragScrollActive = false;
        return;
      }
      this.treeDragScrollFrame = requestAnimationFrame(step);
    };
    this.treeDragScrollFrame = requestAnimationFrame(step);
  }

  private rewrittenTreeRef(candidate: FileRef, previous: FileRef, next: FileRef): FileRef {
    if (!isRefWithin(candidate, previous)) return candidate;
    const suffix = candidate.path.slice(previous.path.length).replace(/^\//, "");
    return { rootId: next.rootId, path: suffix ? `${next.path}/${suffix}` : next.path };
  }

  private async moveTreeNode(node: TreeNode, destination: TreeNode): Promise<void> {
    if (!this.workspace || !this.treeMoveDestination(node, destination)) return;
    const previousRef = { ...node.ref };
    const expandedRefs = [...this.expanded]
      .map((key) => this.nodes.get(key)?.ref)
      .filter((ref): ref is FileRef => Boolean(ref));
    try {
      const result = await editorAPI.moveEntry(this.workspace.id, previousRef, destination.ref);
      const restoredExpansion = expandedRefs.map((ref) => this.rewrittenTreeRef(ref, previousRef, result.entry.ref));
      this.codeNavigation?.remapRef(previousRef, result.entry.ref);
      this.rewriteOpenRefs(previousRef, result.entry.ref, node.hostPath, result.entry.hostPath);
      this.rewriteExpandedRefs(previousRef, result.entry.ref);
      await this.refreshExplorer(restoredExpansion);
      await this.expandTo(result.entry.ref);
      this.schedulePersist();
      this.searchView?.refresh();
      this.sendFilesystemSubscription();
    } catch (error) {
      toast(error instanceof Error ? error.message : String(error), { sticky: true });
      this.renderTreeRows();
    }
  }

  // Media tabs carry a placeholder text model so the shared tab machinery
  // (view state, persistence, disposal) works unchanged; the visible surface
  // is an <img>/<video> fed by the /fs/media stream.
  private mediaStubModel(ref: FileRef): MonacoEditor.ITextModel {
    const uri = monaco.Uri.from({
      scheme: "echo-media", authority: this.workspace?.id || "workspace",
      path: `/${encodeURIComponent(ref.rootId)}/${ref.path.split("/").map(encodeURIComponent).join("/")}`,
    });
    const model = monaco.editor.getModel(uri) || monaco.editor.createModel("", "plaintext", uri);
    this.retainModel(model);
    return model;
  }

  private async openMedia(ref: FileRef, pin: boolean, focusEditor = true): Promise<void> {
    if (!this.workspace) return;
    const kind = previewKindForPath(ref.path);
    if (!kind) return;
    const existing = this.tabs.find((tab) => tab.ref && refKey(tab.ref) === refKey(ref));
    if (existing) {
      if (pin) existing.pinned = true;
      this.activateTab(existing.id, focusEditor);
      this.renderTabs();
      this.sendFilesystemSubscription();
      return;
    }
    const tab: OpenTab = {
      kind: "media", id: randomUUID(), ref, title: ref.path.split("/").pop() || ref.path,
      hostPath: "", pinned: pin, dirty: false, deleted: false, conflict: false, revision: "",
      hasBom: false, eol: "lf", model: this.mediaStubModel(ref), viewState: null,
      changeDisposable: { dispose() {} }, applying: false,
      media: { kind, url: editorAPI.mediaURL(this.workspace.id, ref) },
    };
    if (!pin) {
      const previewIndex = this.tabs.findIndex((candidate) => !candidate.pinned && !candidate.dirty);
      if (previewIndex >= 0) {
        this.disposeTab(this.tabs[previewIndex]);
        this.tabs.splice(previewIndex, 1, tab);
      } else {
        this.tabs.push(tab);
      }
    } else {
      this.tabs.push(tab);
    }
    this.activateTab(tab.id, focusEditor);
    this.renderTabs();
    this.schedulePersist();
    this.sendFilesystemSubscription();
  }

  private async openGitDiff(
    repository: GitRepository,
    change: GitChange | { path: string; oldPath?: string; ref?: FileRef },
    scope: "staged" | "unstaged" | "commit" | "stash",
    reviewRef: string | undefined,
    pin: boolean,
  ): Promise<void> {
    if (!this.workspace) return;
    const identity = `${repository.id}:${scope}:${reviewRef || ""}:${change.path}`;
    const existing = this.tabs.find((tab) => tab.kind === "diff" && tab.id === identity);
    if (existing) {
      if (pin) existing.pinned = true;
      this.activateTab(existing.id);
      this.renderTabs();
      return;
    }
    try {
      const document = await loadGitDiff(this.workspace.id, repository.id, {
        scope, path: change.path, oldPath: change.oldPath, ref: reviewRef,
      });
      if (this.abort.signal.aborted) return;
      const language = languageForPath(document.path, this.lspProfiles);
      const originalURI = monaco.Uri.from({
        scheme: "echo-git", authority: repository.id,
        path: `/${encodeURIComponent(scope)}/${encodeURIComponent(reviewRef || String(document.revision))}/${(document.oldPath || document.path).split("/").map(encodeURIComponent).join("/")}`,
      });
      const originalModel = monaco.editor.getModel(originalURI) || monaco.editor.createModel(document.original.content || "", language, originalURI);
      this.retainModel(originalModel);
      originalModel.setEOL(document.original.eol === "crlf" ? monaco.editor.EndOfLineSequence.CRLF : monaco.editor.EndOfLineSequence.LF);

      const shared = document.editable && document.ref
        ? this.tabs.find((candidate) => {
          const ref = this.worktreeRef(candidate);
          return ref && candidate.diff?.editable !== false && refKey(ref) === refKey(document.ref!);
        })
        : undefined;
      let modifiedModel: MonacoEditor.ITextModel;
      if (shared) {
        modifiedModel = shared.model;
      } else {
        const modifiedURI = document.editable && document.ref ? this.modelURI(document.ref) : monaco.Uri.from({
          scheme: "echo-git", authority: repository.id,
          path: `/${encodeURIComponent(scope)}/${encodeURIComponent(reviewRef || String(document.revision))}/${document.path.split("/").map(encodeURIComponent).join("/")}`,
          query: randomUUID(),
        });
        const reusable = monaco.editor.getModel(modifiedURI);
        modifiedModel = reusable || monaco.editor.createModel(document.modified.content || "", language, modifiedURI);
        if (!reusable) modifiedModel.setEOL(document.modified.eol === "crlf" ? monaco.editor.EndOfLineSequence.CRLF : monaco.editor.EndOfLineSequence.LF);
      }
      this.retainModel(modifiedModel);
      this.lsp?.trackModel(modifiedModel);
      const qualifier = scope === "unstaged" ? "Working Tree" : scope === "staged" ? "Index" : scope === "stash" ? "Stash" : shortGitRef(reviewRef || "Commit");
      const tab: OpenTab = {
        kind: "diff", id: identity, ref: null, title: `${document.path.split("/").pop() || document.path} (${qualifier})`,
        hostPath: document.path, pinned: pin, dirty: shared?.dirty || false, deleted: !document.modified.exists,
        conflict: shared?.conflict || false, revision: shared?.revision || document.modifiedRevision || "",
        hasBom: shared?.hasBom ?? Boolean(document.modified.hasBom), eol: shared?.eol || document.modified.eol,
        model: modifiedModel, viewState: null, changeDisposable: { dispose() {} }, applying: false,
        diff: {
          repository, scope, reviewRef, fileRef: document.ref, oldPath: document.oldPath,
          originalModel, viewState: null, editable: document.editable && document.kind === "text",
          unavailableReason: document.kind === "text" ? undefined : document.unavailableReason || "This Git object cannot be shown as text.",
        },
      };
      tab.changeDisposable = modifiedModel.onDidChangeContent(() => {
        if (tab.applying || !tab.diff?.editable) return;
        this.markModelDirty(modifiedModel);
      });
      if (!pin) {
        const previewIndex = this.tabs.findIndex((candidate) => !candidate.pinned && !candidate.dirty);
        if (previewIndex >= 0) {
          this.disposeTab(this.tabs[previewIndex]);
          this.tabs.splice(previewIndex, 1, tab);
        } else this.tabs.push(tab);
      } else this.tabs.push(tab);
      this.activateTab(tab.id);
      this.renderTabs();
      this.schedulePersist();
      this.sendFilesystemSubscription();
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      this.openUnavailableGitDiff(identity, repository, change, scope, reviewRef, pin, message);
      toast(message, { sticky: true });
    }
  }

  private openUnavailableGitDiff(
    identity: string,
    repository: GitRepository,
    change: GitChange | { path: string; oldPath?: string; ref?: FileRef },
    scope: "staged" | "unstaged" | "commit" | "stash",
    reviewRef: string | undefined,
    pin: boolean,
    reason: string,
  ): void {
    const language = languageForPath(change.path, this.lspProfiles);
    const originalModel = monaco.editor.createModel("", language, monaco.Uri.from({ scheme: "echo-git-missing", authority: repository.id, path: `/${randomUUID()}/original` }));
    const modifiedModel = monaco.editor.createModel("", language, monaco.Uri.from({ scheme: "echo-git-missing", authority: repository.id, path: `/${randomUUID()}/modified` }));
    this.retainModel(originalModel);
    this.retainModel(modifiedModel);
    const tab: OpenTab = {
      kind: "diff", id: identity, ref: null, title: `${change.path.split("/").pop() || change.path} (${scope})`,
      hostPath: change.path, pinned: pin, dirty: false, deleted: false, conflict: false, revision: "",
      hasBom: false, eol: "lf", model: modifiedModel, viewState: null, applying: false,
      changeDisposable: { dispose() {} },
      diff: {
        repository, scope, reviewRef, fileRef: change.ref, oldPath: change.oldPath,
        originalModel, viewState: null, editable: false,
        unavailableReason: `${reason} The Git revision may no longer be available; refresh Source Control and reopen this diff.`,
      },
    };
    if (!pin) {
      const previewIndex = this.tabs.findIndex((candidate) => !candidate.pinned && !candidate.dirty);
      if (previewIndex >= 0) {
        this.disposeTab(this.tabs[previewIndex]);
        this.tabs.splice(previewIndex, 1, tab);
      } else this.tabs.push(tab);
    } else this.tabs.push(tab);
    this.activateTab(tab.id);
    this.renderTabs();
    this.schedulePersist();
  }

  private worktreeRef(tab: OpenTab): FileRef | null {
    if (tab.kind === "diff") return tab.diff?.editable ? tab.diff.fileRef || null : null;
    return tab.ref;
  }

  private markModelDirty(model: MonacoEditor.ITextModel): void {
    for (const tab of this.tabs) {
      if (tab.model !== model) continue;
      tab.dirty = true;
      tab.pinned = true;
      tab.eol = model.getEOL() === "\r\n" ? "crlf" : "lf";
    }
    this.renderTabs();
    this.renderStatus();
    this.schedulePersist();
  }

  private activateTab(id: string, focusEditor = true, mru = true): void {
    const next = this.tabs.find((tab) => tab.id === id);
    if (!next) return;
    if (mru) this.touchMru(id);
    const active = this.activeTab();
    // Re-selecting the current tab must not re-attach its model or restore the
    // last captured view state. That state is intentionally only a snapshot
    // for switching away and back; restoring it here rewinds the live caret
    // and selection to an older editing position.
    if (active?.id === next.id) {
      this.syncActiveTabState();
      if (focusEditor) {
        if (next.kind === "diff") this.diffEditor.getModifiedEditor().focus();
        else if (next.kind !== "media") this.editor.focus();
      }
      this.updateCodeChatSelectionNotice();
      this.schedulePersist();
      return;
    }
    if (active && active.id !== next.id) {
      if (active.kind === "diff" && active.diff) active.diff.viewState = this.diffEditor.saveViewState();
      else active.viewState = this.editor.saveViewState();
    }
    this.activeTabId = id;
    this.syncActiveTabState();
    if (next.kind === "diff" && next.diff) {
      this.editor.setModel(null);
      this.diffEditor.updateOptions({ readOnly: !next.diff.editable, renderSideBySide: this.splitGitDiff });
      this.diffEditor.setModel({ original: next.diff.originalModel, modified: next.model });
      if (next.diff.viewState) this.diffEditor.restoreViewState(next.diff.viewState);
      if (focusEditor) this.diffEditor.getModifiedEditor().focus();
    } else if (next.kind === "media") {
      this.editor.setModel(null);
      this.diffEditor.setModel(null);
      this.renderMediaPreview(next);
    } else {
      this.diffEditor.setModel(null);
      this.editor.setModel(next.model);
      if (next.viewState) this.editor.restoreViewState(next.viewState);
      if (focusEditor) this.editor.focus();
    }
    this.lsp?.activateModel(next.model);
    this.updateEditorSurface();
    this.renderBreadcrumbs();
    this.renderStatus();
    this.revealTabInExplorer(next);
    this.updateCodeChatSelectionNotice();
    this.schedulePersist();
  }

  private touchMru(id: string): void {
    if (this.mruTabIds[0] === id) return;
    this.mruTabIds = [id, ...this.mruTabIds.filter((candidate) => candidate !== id)];
  }

  private activeTab(): OpenTab | undefined {
    return this.tabs.find((tab) => tab.id === this.activeTabId);
  }

  private syncActiveTabState(): void {
    const list = this.root.querySelector<HTMLElement>("[data-tabs-list]");
    if (!list) return;
    let activeElement: HTMLElement | null = null;
    list.querySelectorAll<HTMLElement>("[data-tab-id]").forEach((element) => {
      const active = element.dataset.tabId === this.activeTabId;
      element.classList.toggle("is-active", active);
      element.setAttribute("aria-selected", String(active));
      element.tabIndex = active ? 0 : -1;
      if (active) activeElement = element;
    });
    if (activeElement) {
      requestAnimationFrame(() => {
        if (activeElement?.isConnected) activeElement.scrollIntoView({ block: "nearest", inline: "nearest" });
      });
    }
  }

  private renderTabs(): void {
    const list = this.root.querySelector<HTMLElement>("[data-tabs-list]");
    if (!list) return;
    list.innerHTML = this.tabs.map((tab) => `
      <div class="code-tab ${tab.id === this.activeTabId ? "is-active" : ""} ${!tab.pinned ? "is-preview" : ""}" role="tab" aria-selected="${tab.id === this.activeTabId}" tabindex="${tab.id === this.activeTabId ? 0 : -1}" data-tab-id="${escapeHTML(tab.id)}" title="${escapeHTML(tab.hostPath || tab.title)}">
        <span class="codicon codicon-${this.tabIcon(tab)} code-tab-icon"></span>
        <span class="code-tab-title">${escapeHTML(tab.title)}</span>
        ${tab.conflict ? `<span class="codicon codicon-warning code-tab-conflict" title="Changed on disk"></span>` : ""}
        ${tab.deleted ? `<span class="codicon codicon-trash code-tab-conflict" title="Deleted on disk"></span>` : ""}
        <span class="code-tab-dirty ${tab.dirty ? "is-visible" : ""}" aria-label="${tab.dirty ? "Unsaved changes" : ""}"></span>
        <button type="button" class="code-tab-close" data-tab-close aria-label="Close ${escapeHTML(tab.title)}"><span class="codicon codicon-close"></span></button>
      </div>
    `).join("");
    this.syncActiveTabState();
    this.renderBreadcrumbs();
    if (this.mruCycle) this.renderMruSwitcher();
  }

  private tabIcon(tab: OpenTab): string {
    if (tab.kind === "diff") return "diff";
    if (tab.kind !== "media") return "file-code";
    if (tab.media?.kind === "video") return "play-circle";
    if (tab.media?.kind === "audio") return "music";
    return "file-media";
  }

  private mruTabContext(tab: OpenTab): string {
    const directoryFor = (path: string): string => {
      const normalized = path.replace(/\\/g, "/");
      return normalized.includes("/") ? normalized.slice(0, normalized.lastIndexOf("/")) : "";
    };
    if (tab.kind === "diff" && tab.diff) {
      const path = tab.diff.fileRef?.path || tab.diff.oldPath || tab.hostPath;
      const directory = directoryFor(path);
      const scope = tab.diff.scope === "unstaged" ? "Working Tree"
        : tab.diff.scope === "staged" ? "Index"
          : tab.diff.scope === "stash" ? "Stash"
            : shortGitRef(tab.diff.reviewRef || "Commit");
      return [tab.diff.repository.label, directory, scope].filter(Boolean).join(" · ");
    }
    if (tab.ref) {
      const root = this.roots.find((candidate) => candidate.id === tab.ref?.rootId);
      return [root?.label, directoryFor(tab.ref.path)].filter(Boolean).join(" · ");
    }
    return tab.kind === "file" ? "Untitled" : "";
  }

  private renderMruSwitcher(): void {
    const state = this.mruCycle;
    if (!state?.order.length) {
      this.removeMruSwitcher();
      return;
    }
    if (!this.mruSwitcherOverlay) {
      const overlay = document.createElement("div");
      overlay.className = "code-mru-switcher-overlay";
      overlay.innerHTML = `
        <section class="code-picker code-mru-switcher" aria-label="Recently used editors">
          <div class="code-picker-meta">Recently used editors</div>
          <div class="code-picker-list code-mru-switcher-list" role="listbox" aria-label="Recently used editors" data-mru-switcher-list></div>
        </section>`;
      overlay.addEventListener("click", (event) => {
        const row = (event.target as Element).closest<HTMLElement>("[data-mru-tab-id]");
        if (row?.dataset.mruTabId) {
          this.chooseMruTab(row.dataset.mruTabId);
        } else if (event.target === overlay) {
          this.finishMruCycle(true);
        }
      });
      document.body.appendChild(overlay);
      this.mruSwitcherOverlay = overlay;
    }
    const list = this.mruSwitcherOverlay.querySelector<HTMLElement>("[data-mru-switcher-list]");
    if (!list) return;
    list.innerHTML = state.order.map((id, index) => {
      const tab = this.tabs.find((candidate) => candidate.id === id);
      if (!tab) return "";
      const selected = index === state.index;
      const context = this.mruTabContext(tab);
      const states = [tab.dirty ? "Unsaved changes" : "", tab.deleted ? "Deleted on disk" : "", tab.conflict ? "Changed on disk" : ""].filter(Boolean);
      const accessibleName = [tab.title, context, ...states].filter(Boolean).join(", ");
      return `
        <button type="button" role="option" tabindex="-1" aria-label="${escapeHTML(accessibleName)}" aria-selected="${selected}" class="${selected ? "is-selected" : ""}" data-mru-tab-id="${escapeHTML(tab.id)}">
          <span class="codicon codicon-${this.tabIcon(tab)}"></span>
          <strong>${escapeHTML(tab.title)}</strong>
          <span class="code-mru-switcher-context">${escapeHTML(context)}</span>
          <span class="code-mru-switcher-status" aria-hidden="true">
            ${tab.dirty ? `<span class="code-mru-switcher-dirty" title="Unsaved changes"></span>` : ""}
            ${tab.deleted ? `<span class="codicon codicon-trash" title="Deleted on disk"></span>` : ""}
            ${tab.conflict ? `<span class="codicon codicon-warning" title="Changed on disk"></span>` : ""}
          </span>
        </button>`;
    }).join("");
    const selected = list.querySelector<HTMLElement>("[aria-selected=true]");
    requestAnimationFrame(() => {
      if (selected?.isConnected) selected.scrollIntoView({ block: "nearest" });
    });
  }

  private removeMruSwitcher(): void {
    this.mruSwitcherOverlay?.remove();
    this.mruSwitcherOverlay = null;
  }

  private finishMruCycle(focusEditor = false): void {
    const state = this.mruCycle;
    if (state) {
      const finalId = state.order[state.index];
      if (finalId) this.touchMru(finalId);
    }
    this.mruCycle = null;
    this.removeMruSwitcher();
    if (focusEditor && this.activeTab()?.kind !== "media") this.focusActiveEditor();
  }

  private chooseMruTab(id: string): void {
    const state = this.mruCycle;
    const index = state?.order.indexOf(id) ?? -1;
    if (!state || index < 0 || !this.tabs.some((tab) => tab.id === id)) return;
    this.mruCycle = { ...state, index };
    void this.recordCodeNavigation(() => this.activateTab(id, true, false));
    this.finishMruCycle();
  }

  private renderBreadcrumbs(): void {
    const target = this.root.querySelector<HTMLElement>("[data-breadcrumb-path]");
    const tab = this.activeTab();
    if (!target || !tab) {
      if (target) target.innerHTML = "";
      return;
    }
    if (tab.kind === "diff" && tab.diff) {
      target.innerHTML = `<span>${escapeHTML(tab.diff.repository.label)}</span><span class="codicon codicon-chevron-right"></span><span>${escapeHTML(tab.diff.oldPath || tab.title)}</span><span class="codicon codicon-chevron-right"></span><span>${escapeHTML(tab.diff.scope === "unstaged" ? "Working Tree" : tab.diff.scope === "staged" ? "Index" : tab.diff.scope)}</span>`;
      return;
    }
    if (!tab.ref) {
      target.innerHTML = `<span>${escapeHTML(tab.title)}</span>`;
      return;
    }
    const root = this.roots.find((candidate) => candidate.id === tab.ref?.rootId);
    const parts = tab.ref.path.split("/");
    target.innerHTML = [root?.label || "workspace", ...parts].map((part, index) => `
      ${index ? `<span class="codicon codicon-chevron-right"></span>` : ""}
      <button type="button" data-breadcrumb-index="${index}">${escapeHTML(part)}</button>
    `).join("");
  }

  private renderStatus(): void {
    const tab = this.activeTab();
    const position = tab?.kind === "diff" ? this.diffEditor?.getModifiedEditor().getPosition() : tab && tab.kind !== "media" ? this.editor?.getPosition() : undefined;
    const cursor = this.root.querySelector<HTMLElement>("[data-status=cursor]");
    const eol = this.root.querySelector<HTMLElement>("[data-status=eol]");
    const language = this.root.querySelector<HTMLElement>("[data-status=language]");
    const lsp = this.root.querySelector<HTMLButtonElement>("[data-status=lsp]");
    if (cursor) cursor.textContent = position ? `Ln ${position.lineNumber}, Col ${position.column}` : "Ln 1, Col 1";
    if (eol) eol.textContent = tab?.model.getEOL() === "\r\n" ? "CRLF" : "LF";
    if (language) language.textContent = tab ? monaco.languages.getLanguages().find((item) => item.id === tab.model.getLanguageId())?.aliases?.[0] || tab.model.getLanguageId() : "Plain Text";
    if (lsp) {
      const name = this.lspStatus?.name || this.lspStatus?.profileId || "LSP";
      lsp.hidden = this.lspState === "none";
      lsp.textContent = this.lspState === "owned" ? `${name} ✓`
        : this.lspState === "denied" ? `${name}: Take Over`
          : this.lspState === "failed" ? `${name}: Failed`
            : this.lspState === "connecting" ? `${name}: Connecting`
              : `${name}: Starting`;
      lsp.dataset.lspState = this.lspState;
      lsp.title = [this.lspStatus?.message, this.lspStatus?.stderr].filter(Boolean).join("\n\n") || "Language server status";
    }
  }

  private updateEditorSurface(): void {
    const placeholder = this.root.querySelector<HTMLElement>("[data-editor-placeholder]");
    const host = this.root.querySelector<HTMLElement>("[data-monaco-host]");
    const diffHost = this.root.querySelector<HTMLElement>("[data-monaco-diff-host]");
    const mediaHost = this.root.querySelector<HTMLElement>("[data-media-preview-host]");
    const toolbar = this.root.querySelector<HTMLElement>("[data-diff-toolbar]");
    const unavailable = this.root.querySelector<HTMLElement>("[data-diff-unavailable]");
    const tab = this.activeTab();
    const hasTab = Boolean(tab);
    const isDiff = tab?.kind === "diff" && Boolean(tab.diff);
    const isMedia = tab?.kind === "media" && Boolean(tab.media);
    const diffUnavailable = isDiff && Boolean(tab?.diff?.unavailableReason);
    if (placeholder) placeholder.hidden = hasTab;
    if (host) host.hidden = !hasTab || isDiff || isMedia;
    if (diffHost) diffHost.hidden = !isDiff || diffUnavailable;
    if (mediaHost) mediaHost.hidden = !isMedia;
    if (toolbar) toolbar.hidden = !isDiff || diffUnavailable;
    if (unavailable) {
      unavailable.hidden = !diffUnavailable;
      unavailable.innerHTML = diffUnavailable ? `<span class="codicon codicon-file-binary"></span><h2>Diff unavailable</h2><p>${escapeHTML(tab?.diff?.unavailableReason || "")}</p>` : "";
    }
    const label = this.root.querySelector<HTMLElement>("[data-diff-label]");
    if (label && isDiff) label.textContent = tab?.diff?.editable ? "Working Tree (editable)" : "Read-only Git snapshot";
    this.editor?.layout();
    this.diffEditor?.layout();
  }

  private renderMediaPreview(tab: OpenTab): void {
    const host = this.root.querySelector<HTMLElement>("[data-media-preview-host]");
    if (!host || !tab.media) return;
    const url = `${tab.media.url}&v=${Date.now()}`;
    const icon = tab.media.kind === "video" ? "play-circle" : tab.media.kind === "audio" ? "music" : "file-media";
    const noun = tab.media.kind === "video" ? "video" : tab.media.kind === "audio" ? "audio" : "image";
    const mediaTag = tab.media.kind === "video"
      ? `<video src="${escapeHTML(url)}" controls loop playsinline muted></video>`
      : tab.media.kind === "audio"
        ? `<audio src="${escapeHTML(url)}" controls preload="metadata"></audio>`
        : `<img src="${escapeHTML(url)}" alt="${escapeHTML(tab.title)}">`;
    host.innerHTML = `
      <div class="code-media-frame${tab.media.kind === "audio" ? " code-media-frame-audio" : ""}">
        ${mediaTag}
        <div class="code-media-volume-slot"></div>
        <div class="code-media-error" hidden>
          <span class="codicon codicon-${icon}"></span>
          <h2>This ${noun} cannot be displayed</h2>
          <p>The file may be larger than the preview limit or its container format is not playable.</p>
        </div>
      </div>`;
    const element = host.querySelector<HTMLVideoElement | HTMLImageElement | HTMLAudioElement>(
      tab.media.kind === "video" ? "video" : tab.media.kind === "audio" ? "audio" : "img",
    );
    const errorPanel = host.querySelector<HTMLElement>(".code-media-error");
    const volumeSlot = host.querySelector<HTMLElement>(".code-media-volume-slot");
    if (tab.media.kind === "video" && element instanceof HTMLVideoElement && volumeSlot) {
      volumeSlot.appendChild(attachVideoVolumeControl(element, "code-media-volume"));
    }
    // <img>, <video>, and <audio> all raise "error" when the stream fails
    // (missing file, oversized refusal, unplayable container), so one handler
    // covers every failure mode.
    element?.addEventListener("error", () => {
      if (!element || !errorPanel) return;
      element.hidden = true;
      errorPanel.hidden = false;
      if (volumeSlot) volumeSlot.hidden = true;
    }, { once: true });
  }

  private updateDiffLayoutState(): void {
    const host = this.root.querySelector<HTMLElement>("[data-monaco-diff-host]");
    if (host) host.dataset.diffLayout = this.splitGitDiff ? "split" : "inline";
    const button = this.root.querySelector<HTMLButtonElement>("[data-diff-action=layout]");
    if (button) {
      button.title = this.splitGitDiff ? "Use Inline Diff" : "Use Side-by-Side Diff";
      button.setAttribute("aria-label", button.title);
    }
  }

  private disposeTab(tab: OpenTab): void {
    tab.changeDisposable.dispose();
    this.releaseModel(tab.model);
    if (tab.diff) this.releaseModel(tab.diff.originalModel);
  }

  private async closeTab(tab: OpenTab, skipPrompt = false): Promise<boolean> {
    if (tab.dirty && !skipPrompt) {
      const choice = await choiceDialog({
        title: `Save changes to ${tab.title}?`, message: "Your changes will be lost if you close this editor without saving.",
        choices: [{ id: "cancel", label: "Cancel" }, { id: "discard", label: "Discard", danger: true }, { id: "save", label: "Save", primary: true }],
      });
      if (!choice || choice === "cancel") return false;
      if (choice === "save" && !(await this.saveTab(tab))) return false;
    }
    const index = this.tabs.indexOf(tab);
    this.tabs.splice(index, 1);
    this.mruTabIds = removeFromMru(this.mruTabIds, tab.id);
    if (this.mruCycle) this.mruCycle = pruneMruCycle(this.mruCycle, this.tabs.map((t) => t.id));
    this.disposeTab(tab);
    if (tab.id === this.activeTabId) {
      const next = this.tabs[Math.min(index, this.tabs.length - 1)];
      this.activeTabId = null;
      if (next) this.activateTab(next.id);
      else {
        this.editor.setModel(null);
        this.diffEditor.setModel(null);
        this.lsp?.activateModel(null);
        this.updateEditorSurface();
        this.renderBreadcrumbs();
      }
    }
    this.renderTabs();
    this.updateCodeChatSelectionNotice();
    this.schedulePersist();
    this.sendFilesystemSubscription();
    return true;
  }

  private newUntitled(): void {
    const title = `Untitled-${this.untitledCounter++}`;
    const id = randomUUID();
    const model = monaco.editor.createModel("", "plaintext", monaco.Uri.from({ scheme: "untitled", authority: this.workspace?.id || "workspace", path: `/${id}` }));
    this.retainModel(model);
    const tab: OpenTab = {
      kind: "file", id, ref: null, title, hostPath: "", pinned: true, dirty: false,
      deleted: false, conflict: false, revision: "", hasBom: false, eol: "lf", model,
      viewState: null, changeDisposable: { dispose() {} }, applying: false,
    };
    tab.changeDisposable = model.onDidChangeContent(() => {
      if (tab.applying) return;
      tab.dirty = true;
      this.renderTabs();
      this.schedulePersist();
    });
    this.tabs.push(tab);
    this.activateTab(tab.id);
    this.renderTabs();
  }

  private async saveTab(tab = this.activeTab()): Promise<boolean> {
    if (!tab || !this.workspace) return false;
    if (tab.kind === "diff") return this.saveEditableDiff(tab);
    if (tab.kind === "media") {
      toast("Media previews are read-only.");
      return false;
    }
    if (!tab.ref) return this.saveUntitled(tab);
    if (tab.deleted) {
      const choice = await choiceDialog({
        title: "Recreate deleted file?", message: `${tab.title} was deleted from disk. Saving will recreate it.`,
        choices: [{ id: "cancel", label: "Cancel" }, { id: "recreate", label: "Recreate", primary: true }],
      });
      if (choice !== "recreate") return false;
      try {
        await this.lsp?.formatBeforeSave(tab.model);
        const parentPath = tab.ref.path.includes("/") ? tab.ref.path.slice(0, tab.ref.path.lastIndexOf("/")) : "";
        const result = await editorAPI.createEntry(this.workspace.id, {
          parent: { rootId: tab.ref.rootId, path: parentPath }, name: tab.title, kind: "file",
          content: tab.model.getValue(), hasBom: tab.hasBom,
        });
        if (result.file) this.applySavedSnapshot(tab, result.file);
        tab.deleted = false;
        this.lsp?.didSave(tab.model);
        return true;
      } catch (error) {
        toast(error instanceof Error ? error.message : String(error));
        return false;
      }
    }
    try {
      await this.lsp?.formatBeforeSave(tab.model);
      const snapshot = await editorAPI.saveFile(this.workspace.id, {
        ref: tab.ref, content: tab.model.getValue(), expectedRevision: tab.revision, hasBom: tab.hasBom,
      });
      this.applySavedSnapshot(tab, snapshot);
      this.lsp?.didSave(tab.model);
      return true;
    } catch (error) {
      const apiError = error as APIError;
      if (apiError.payload?.code === "revision_conflict" && apiError.payload.details?.current) {
        return this.resolveSaveConflict(tab, apiError.payload.details.current);
      }
      toast(error instanceof Error ? error.message : String(error), { sticky: true });
      return false;
    }
  }

  private async saveEditableDiff(tab: OpenTab): Promise<boolean> {
    if (!this.workspace || !tab.diff?.editable || !tab.diff.fileRef) {
      toast("This Git snapshot is read-only.");
      return false;
    }
    const ref = tab.diff.fileRef;
    try {
      await this.lsp?.formatBeforeSave(tab.model);
      let snapshot: FileSnapshot;
      if (tab.deleted) {
        const parentPath = ref.path.includes("/") ? ref.path.slice(0, ref.path.lastIndexOf("/")) : "";
        const result = await editorAPI.createEntry(this.workspace.id, {
          parent: { rootId: ref.rootId, path: parentPath }, name: ref.path.split("/").pop() || tab.title,
          kind: "file", content: tab.model.getValue(), hasBom: tab.hasBom,
        });
        if (!result.file) return false;
        snapshot = result.file;
      } else {
        snapshot = await editorAPI.saveFile(this.workspace.id, {
          ref, content: tab.model.getValue(), expectedRevision: tab.revision, hasBom: tab.hasBom,
        });
      }
      this.applySavedSnapshot(tab, snapshot);
      this.lsp?.didSave(tab.model);
      return true;
    } catch (error) {
      const apiError = error as APIError;
      const disk = apiError.payload?.details?.current as FileSnapshot | undefined;
      if (apiError.payload?.code === "revision_conflict" && disk) {
        const choice = await choiceDialog({
          title: `${tab.title} changed on disk`, message: "Reload the disk version or overwrite it with this editable diff buffer.",
          choices: [{ id: "cancel", label: "Cancel" }, { id: "reload", label: "Reload" }, { id: "overwrite", label: "Overwrite", primary: true }],
        });
        if (choice === "reload") { this.applyDiskSnapshot(tab, disk); return false; }
        if (choice === "overwrite") {
          tab.revision = disk.revision;
          return this.saveEditableDiff(tab);
        }
        return false;
      }
      toast(error instanceof Error ? error.message : String(error), { sticky: true });
      return false;
    }
  }

  private applySavedSnapshot(tab: OpenTab, snapshot: FileSnapshot): void {
    for (const candidate of this.tabs) {
      if (candidate.model !== tab.model) continue;
      candidate.revision = snapshot.revision;
      candidate.hostPath = snapshot.hostPath;
      candidate.hasBom = snapshot.hasBom;
      candidate.eol = snapshot.eol;
      candidate.dirty = false;
      candidate.conflict = false;
      candidate.deleted = false;
      candidate.pinned = true;
    }
    this.renderTabs();
    this.renderStatus();
    this.schedulePersist();
  }

  private async resolveSaveConflict(tab: OpenTab, disk: FileSnapshot): Promise<boolean> {
    const choice = await choiceDialog({
      title: `${tab.title} changed on disk`,
      message: "Compare the versions, reload the file, or overwrite the current disk revision.",
      choices: [
        { id: "cancel", label: "Cancel" },
        { id: "compare", label: "Compare" },
        { id: "reload", label: "Reload from Disk" },
        { id: "overwrite", label: "Overwrite", primary: true },
      ],
    });
    if (choice === "compare") {
      await this.showDiff(tab, disk);
      return this.resolveSaveConflict(tab, disk);
    }
    if (choice === "reload") {
      this.applyDiskSnapshot(tab, disk);
      return false;
    }
    if (choice === "overwrite" && tab.ref && this.workspace) {
      try {
        const snapshot = await editorAPI.saveFile(this.workspace.id, {
          ref: tab.ref, content: tab.model.getValue(), expectedRevision: disk.revision, hasBom: tab.hasBom,
        });
        this.applySavedSnapshot(tab, snapshot);
        this.lsp?.didSave(tab.model);
        return true;
      } catch (error) {
        const next = error as APIError;
        if (next.payload?.code === "revision_conflict" && next.payload.details?.current) {
          return this.resolveSaveConflict(tab, next.payload.details.current);
        }
        toast(error instanceof Error ? error.message : String(error));
      }
    }
    return false;
  }

  private showDiff(tab: OpenTab, disk: FileSnapshot): Promise<void> {
    return new Promise((resolve) => {
      const overlay = document.createElement("div");
      overlay.className = "code-modal-overlay code-diff-overlay";
      overlay.innerHTML = `<section class="code-diff-dialog" role="dialog" aria-modal="true"><header><strong>Disk ↔ Unsaved — ${escapeHTML(tab.title)}</strong><button type="button" aria-label="Close"><span class="codicon codicon-close"></span></button></header><div data-diff-host></div></section>`;
      document.body.appendChild(overlay);
      const original = monaco.editor.createModel(disk.content, tab.model.getLanguageId());
      const modified = monaco.editor.createModel(tab.model.getValue(), tab.model.getLanguageId());
      const diff = monaco.editor.createDiffEditor(overlay.querySelector<HTMLElement>("[data-diff-host]")!, {
        theme: this.mediaTheme.matches ? "vs-dark" : "vs", automaticLayout: true, readOnly: true,
        originalEditable: false, minimap: { enabled: false }, renderSideBySide: true,
        selectionHighlight: true, selectionHighlightMultiline: false,
        occurrencesHighlight: "singleFile",
      });
      diff.setModel({ original, modified });
      const close = () => {
        diff.dispose();
        original.dispose();
        modified.dispose();
        overlay.remove();
        resolve();
      };
      overlay.querySelector("button")?.addEventListener("click", close);
      overlay.addEventListener("keydown", (event) => { if (event.key === "Escape") close(); });
      overlay.querySelector<HTMLButtonElement>("button")?.focus();
    });
  }

  private applyDiskSnapshot(tab: OpenTab, snapshot: FileSnapshot): void {
    const sharedTabs = this.tabs.filter((candidate) => candidate.model === tab.model);
    sharedTabs.forEach((candidate) => { candidate.applying = true; });
    tab.model.setValue(snapshot.content);
    tab.model.setEOL(snapshot.eol === "crlf" ? monaco.editor.EndOfLineSequence.CRLF : monaco.editor.EndOfLineSequence.LF);
    for (const candidate of sharedTabs) {
      candidate.applying = false;
      candidate.revision = snapshot.revision;
      candidate.hostPath = snapshot.hostPath;
      candidate.hasBom = snapshot.hasBom;
      candidate.eol = snapshot.eol;
      candidate.dirty = false;
      candidate.conflict = false;
      candidate.deleted = false;
    }
    this.renderTabs();
    this.renderStatus();
    this.schedulePersist();
  }

  private async saveUntitled(tab: OpenTab): Promise<boolean> {
    if (!this.workspace || !this.roots.length) return false;
    const destination = await this.saveAsDialog(tab.title);
    if (!destination) return false;
    const parent: FileRef = { rootId: destination.rootId, path: destination.directory };
    const destinationRef = joinRef(parent, destination.name);
    const duplicate = this.tabs.find((candidate) => candidate !== tab && candidate.ref && refKey(candidate.ref) === refKey(destinationRef));
    if (duplicate?.dirty) {
      const choice = await choiceDialog({
        title: `${destination.name} is already open`,
        message: "That editor has unsaved changes. Continuing will discard its buffer if this Save As succeeds.",
        choices: [
          { id: "cancel", label: "Cancel" },
          { id: "continue", label: "Discard Open Buffer and Continue", danger: true },
        ],
      });
      if (choice !== "continue") return false;
    }
    const closeDuplicate = async () => {
      if (duplicate && this.tabs.includes(duplicate)) await this.closeTab(duplicate, true);
    };
    try {
      const result = await editorAPI.createEntry(this.workspace.id, {
        parent, name: destination.name, kind: "file", content: tab.model.getValue(), hasBom: tab.hasBom,
      });
      if (!result.file) throw new Error("The server did not return the new file");
      await closeDuplicate();
      this.adoptFile(tab, result.file);
      await this.refreshParent(parent);
      return true;
    } catch (error) {
      const apiError = error as APIError;
      if (apiError.payload?.code === "already_exists") {
        const replace = await choiceDialog({
          title: "Replace existing file?", message: `${destination.name} already exists in this folder.`,
          choices: [{ id: "cancel", label: "Cancel" }, { id: "replace", label: "Replace", danger: true, primary: true }],
        });
        if (replace !== "replace") return false;
        try {
          const current = await editorAPI.readFile(this.workspace.id, destinationRef);
          const saved = await editorAPI.saveFile(this.workspace.id, {
            ref: destinationRef, content: tab.model.getValue(), expectedRevision: current.revision, hasBom: tab.hasBom,
          });
          await closeDuplicate();
          this.adoptFile(tab, saved);
          return true;
        } catch (replaceError) {
          toast(replaceError instanceof Error ? replaceError.message : String(replaceError));
          return false;
        }
      }
      toast(error instanceof Error ? error.message : String(error));
      return false;
    }
  }

  private adoptFile(tab: OpenTab, snapshot: FileSnapshot): void {
    const content = tab.model.getValue();
    const language = languageForPath(snapshot.ref.path, this.lspProfiles);
    const viewState = tab.id === this.activeTabId ? this.editor.saveViewState() : tab.viewState;
    tab.changeDisposable.dispose();
    this.releaseModel(tab.model);
    tab.ref = snapshot.ref;
    tab.title = snapshot.ref.path.split("/").pop() || snapshot.ref.path;
    tab.hostPath = snapshot.hostPath;
    tab.model = monaco.editor.createModel(content, language, this.modelURI(snapshot.ref, snapshot.hostPath));
    this.retainModel(tab.model);
    this.lsp?.trackModel(tab.model);
    tab.changeDisposable = tab.model.onDidChangeContent(() => {
      if (tab.applying) return;
      tab.dirty = true;
      tab.pinned = true;
      this.renderTabs();
      this.schedulePersist();
    });
    tab.viewState = viewState;
    this.applySavedSnapshot(tab, snapshot);
    this.lsp?.didSave(tab.model);
    if (tab.id === this.activeTabId) {
      this.editor.setModel(tab.model);
      this.lsp?.activateModel(tab.model);
      if (viewState) this.editor.restoreViewState(viewState);
    }
    this.renderBreadcrumbs();
  }

  private saveAsDialog(initialName: string): Promise<{ rootId: string; directory: string; name: string } | null> {
    return new Promise((resolve) => {
      const overlay = document.createElement("div");
      overlay.className = "code-modal-overlay";
      overlay.innerHTML = `
        <form class="code-modal code-save-as" role="dialog" aria-modal="true">
          <h2>Save As</h2>
          <label>Workspace folder<select name="rootId">${this.roots.map((root) => `<option value="${escapeHTML(root.id)}">${escapeHTML(root.label)} — ${escapeHTML(root.hostPath)}</option>`).join("")}</select></label>
          <label>Directory <span>(root-relative)</span><input name="directory" placeholder="src/components"></label>
          <label>File name<input name="name" value="${escapeHTML(initialName)}" required></label>
          <div class="code-modal-actions"><button type="button" data-cancel>Cancel</button><button type="submit" class="is-primary">Save</button></div>
        </form>`;
      const form = overlay.querySelector<HTMLFormElement>("form")!;
      const finish = (value: { rootId: string; directory: string; name: string } | null) => { overlay.remove(); resolve(value); };
      form.addEventListener("submit", (event) => {
        event.preventDefault();
        const values = new FormData(form);
        const directory = String(values.get("directory") || "").trim().replace(/^\/+|\/+$/g, "");
        const name = String(values.get("name") || "").trim();
        if (name) finish({ rootId: String(values.get("rootId")), directory, name });
      });
      overlay.querySelector("[data-cancel]")?.addEventListener("click", () => finish(null));
      overlay.addEventListener("keydown", (event) => { if (event.key === "Escape") finish(null); });
      document.body.appendChild(overlay);
      requestAnimationFrame(() => form.querySelector<HTMLInputElement>("[name=name]")?.select());
    });
  }

  private async beginRename(node: TreeNode): Promise<void> {
    if (node.isRoot) return;
    this.selectedTreeKey = node.key;
    this.renamingKey = node.key;
    this.renderTreeRows();
  }

  private async commitRename(node: TreeNode, newName: string): Promise<void> {
    this.renamingKey = null;
    newName = newName.trim();
    if (!newName || newName === node.name || !this.workspace) {
      this.renderTreeRows();
      return;
    }
    try {
      const previousRef = node.ref;
      const result = await editorAPI.renameEntry(this.workspace.id, previousRef, newName);
      this.codeNavigation?.remapRef(previousRef, result.entry.ref);
      this.rewriteOpenRefs(previousRef, result.entry.ref, node.hostPath, result.entry.hostPath);
      this.rewriteExpandedRefs(previousRef, result.entry.ref);
      const parent = node.parentKey ? this.nodes.get(node.parentKey) : null;
      if (parent) await this.reloadChildrenPreservingExpansion(parent);
      this.selectedTreeKey = refKey(result.entry.ref);
      this.renderTree();
      this.schedulePersist();
    } catch (error) {
      toast(error instanceof Error ? error.message : String(error));
      this.renderTreeRows();
    }
  }

  private rewriteOpenRefs(previous: FileRef, next: FileRef, previousHost: string, nextHost: string): void {
    for (const tab of this.tabs) {
      if (!tab.ref || !isRefWithin(tab.ref, previous)) continue;
      const suffix = tab.ref.path.slice(previous.path.length).replace(/^\//, "");
      const nextRef = { rootId: next.rootId, path: suffix ? `${next.path}/${suffix}` : next.path };
      const content = tab.model.getValue();
      const language = languageForPath(nextRef.path, this.lspProfiles);
      const viewState = tab.id === this.activeTabId ? this.editor.saveViewState() : tab.viewState;
      tab.changeDisposable.dispose();
      this.releaseModel(tab.model);
      tab.ref = nextRef;
      tab.title = nextRef.path.split("/").pop() || nextRef.path;
      tab.hostPath = tab.hostPath.startsWith(previousHost) ? nextHost + tab.hostPath.slice(previousHost.length) : nextHost;
      tab.model = monaco.editor.createModel(content, language, this.modelURI(nextRef, tab.hostPath));
      this.retainModel(tab.model);
      this.lsp?.trackModel(tab.model);
      tab.changeDisposable = tab.model.onDidChangeContent(() => {
        if (tab.applying) return;
        tab.dirty = true;
        tab.pinned = true;
        this.renderTabs();
        this.schedulePersist();
      });
      tab.viewState = viewState;
      if (tab.id === this.activeTabId) {
        this.editor.setModel(tab.model);
        this.lsp?.activateModel(tab.model);
        if (viewState) this.editor.restoreViewState(viewState);
      }
    }
    this.renderTabs();
  }

  private rewriteExpandedRefs(previous: FileRef, next: FileRef): void {
    const rewritten = new Set<string>();
    for (const key of this.expanded) {
      const node = this.nodes.get(key);
      if (!node || !isRefWithin(node.ref, previous)) {
        rewritten.add(key);
        continue;
      }
      const suffix = node.ref.path.slice(previous.path.length).replace(/^\//, "");
      rewritten.add(refKey({ rootId: next.rootId, path: suffix ? `${next.path}/${suffix}` : next.path }));
    }
    this.expanded = rewritten;
  }

  private async deleteNode(node: TreeNode): Promise<void> {
    if (node.isRoot || !this.workspace) return;
    const affected = this.tabs.filter((tab) => tab.ref && isRefWithin(tab.ref, node.ref));
    if (affected.some((tab) => tab.dirty)) {
      const choice = await choiceDialog({
        title: `Delete ${node.name}?`, message: "One or more open files inside this item have unsaved changes.",
        choices: [
          { id: "cancel", label: "Cancel" },
          { id: "discard", label: "Discard changes and delete", danger: true },
          { id: "save", label: "Save All, Then Delete", primary: true },
        ],
      });
      if (choice === "save") {
        for (const tab of affected.filter((candidate) => candidate.dirty)) {
          if (!(await this.saveTab(tab))) return;
        }
      } else if (choice !== "discard") return;
    } else {
      const choice = await choiceDialog({
        title: `Delete ${node.name}?`, message: node.kind === "directory" ? "The folder and its contents will move to Echo Trash." : "The file will move to Echo Trash.",
        choices: [{ id: "cancel", label: "Cancel" }, { id: "delete", label: "Move to Trash", danger: true, primary: true }],
      });
      if (choice !== "delete") return;
    }
    try {
      const item = await editorAPI.trashEntry(this.workspace.id, node.ref);
      for (const tab of [...affected]) await this.closeTab(tab, true);
      const parent = node.parentKey ? this.nodes.get(node.parentKey) : null;
      if (parent) await this.reloadChildrenPreservingExpansion(parent);
      toast(`Moved ${node.name} to Echo Trash`, {
        actionLabel: "Undo",
        action: async () => {
          await editorAPI.restoreTrash(this.workspace!.id, item.id);
          if (parent) await this.reloadChildrenPreservingExpansion(parent);
          toast(`Restored ${node.name}`);
        },
      });
    } catch (error) {
      toast(error instanceof Error ? error.message : String(error), { sticky: true });
    }
  }

  private async createUnderSelection(kind: "file" | "directory"): Promise<void> {
    if (!this.workspace || !this.roots.length) return;
    const selected = this.selectedTreeKey ? this.nodes.get(this.selectedTreeKey) : null;
    const parent = selected?.kind === "directory" ? selected : selected?.parentKey ? this.nodes.get(selected.parentKey) : this.nodes.get(refKey({ rootId: this.roots[0].id, path: "" }));
    if (!parent) return;
    const name = await promptDialog({ title: kind === "file" ? "New File" : "New Folder", label: "Name", confirmLabel: "Create" });
    if (!name) return;
    try {
      const result = await editorAPI.createEntry(this.workspace.id, { parent: parent.ref, name, kind });
      this.expanded.add(parent.key);
      await this.reloadChildrenPreservingExpansion(parent);
      this.selectedTreeKey = refKey(result.entry.ref);
      this.renderTree();
      if (kind === "file") await this.recordCodeNavigation(() => this.openFile(result.entry.ref, true));
    } catch (error) {
      toast(error instanceof Error ? error.message : String(error));
    }
  }

  private async refreshParent(ref: FileRef): Promise<void> {
    const key = refKey(ref);
    const node = this.nodes.get(key);
    if (node?.kind === "directory") await this.reloadChildrenPreservingExpansion(node);
  }

  private async reloadChildrenPreservingExpansion(node: TreeNode): Promise<void> {
    const descendants = [...this.expanded]
      .map((key) => {
        const separator = key.indexOf(":");
        return separator >= 0 ? { rootId: key.slice(0, separator), path: key.slice(separator + 1) } : null;
      })
      .filter((ref): ref is FileRef => Boolean(ref && ref.path && isRefWithin(ref, node.ref)))
      .sort((left, right) => left.path.split("/").length - right.path.split("/").length);
    await this.loadChildren(node, true);
    for (const ref of descendants) {
      await this.expandTo(ref, false);
      const restored = this.nodes.get(refKey(ref));
      if (!restored || restored.kind !== "directory") this.expanded.delete(refKey(ref));
    }
  }

  private async refreshExplorer(preservedExpansion?: FileRef[]): Promise<void> {
    const expandedRefs = preservedExpansion || [...this.expanded].map((key) => this.nodes.get(key)?.ref).filter(Boolean) as FileRef[];
    this.nodes.clear();
    this.expanded.clear();
    await Promise.all(this.roots.map((root) => this.ensureRoot(root)));
    for (const ref of expandedRefs.sort((a, b) => a.path.split("/").length - b.path.split("/").length)) {
      if (!ref.path) continue;
      await this.expandTo(ref, false);
    }
    this.renderTree();
  }

  private async expandTo(ref: FileRef, select = true): Promise<void> {
    const segments = ref.path.split("/").filter(Boolean);
    let current = this.nodes.get(refKey({ rootId: ref.rootId, path: "" }));
    if (!current) return;
    this.expanded.add(current.key);
    await this.loadChildren(current);
    for (let index = 0; index < segments.length; index++) {
      const nextPath = segments.slice(0, index + 1).join("/");
      const next = this.nodes.get(refKey({ rootId: ref.rootId, path: nextPath }));
      if (!next) break;
      current = next;
      if (next.kind === "directory" && (index < segments.length - 1 || this.expanded.has(next.key))) {
        this.expanded.add(next.key);
        await this.loadChildren(next);
      }
    }
    if (select && current) {
      this.selectedTreeKey = current.key;
      this.renderTree();
      const index = this.flatTree.findIndex((node) => node.key === current!.key);
      if (index >= 0) this.treeVirtualizer.scrollToIndex(index, { align: "auto" });
    }
  }

  private revealTabInExplorer(tab: OpenTab): void {
    const ref = this.worktreeRef(tab);
    const generation = ++this.explorerRevealGeneration;
    if (!ref) return;
    void this.expandTo(ref, false).then(() => {
      if (this.abort.signal.aborted || generation !== this.explorerRevealGeneration || this.activeTabId !== tab.id) return;
      const node = this.nodes.get(refKey(ref));
      if (!node || node.kind !== "file") return;
      if (this.selectedTreeKey !== node.key) {
        this.selectedTreeKey = node.key;
        this.renderTree();
      }
      const index = this.flatTree.findIndex((candidate) => candidate.key === node.key);
      if (index >= 0) this.treeVirtualizer.scrollToIndex(index, { align: "auto" });
      this.schedulePersist();
    });
  }

  private showTreeMenu(event: MouseEvent, node: TreeNode): void {
    this.selectedTreeKey = node.key;
    this.renderTreeRows();
    showContextMenu(event.clientX, event.clientY, [
      ...(node.kind === "directory" ? [
        { label: "New File", icon: "new-file", run: () => this.createUnderSelection("file") },
        { label: "New Folder", icon: "new-folder", run: () => this.createUnderSelection("directory") },
      ] : []),
      { label: "Rename", detail: "F2", icon: "edit", disabled: node.isRoot, separatorBefore: node.kind === "directory", run: () => this.beginRename(node) },
      { label: "Delete", detail: "Del", icon: "trash", danger: true, disabled: node.isRoot, run: () => this.deleteNode(node) },
      { label: "Reveal in File Browser", icon: "folder-opened", separatorBefore: true, run: () => this.reveal(node.ref) },
    ]);
  }

  private showTabMenu(event: MouseEvent, tab: OpenTab): void {
    showContextMenu(event.clientX, event.clientY, [
      { label: "Close", detail: "Ctrl+W", icon: "close", run: () => this.closeTab(tab) },
      { label: "Close Others", icon: "close-all", run: () => this.closeOthers(tab) },
      { label: "Copy Path", icon: "copy", separatorBefore: true, disabled: !tab.ref, run: () => this.copyPath(tab, false) },
      { label: "Copy Relative Path", icon: "copy", disabled: !tab.ref, run: () => this.copyPath(tab, true) },
      { label: "Show in File Browser", icon: "folder-opened", separatorBefore: true, disabled: !tab.ref, run: () => tab.ref && this.reveal(tab.ref) },
      { label: "Show in Folder Tree", icon: "list-tree", disabled: !tab.ref, run: () => tab.ref && this.expandTo(tab.ref) },
      { label: tab.pinned ? "Unpin" : "Pin", icon: "pinned", separatorBefore: true, run: () => { tab.pinned = !tab.pinned; this.renderTabs(); this.schedulePersist(); } },
    ]);
  }

  private async closeOthers(keep: OpenTab): Promise<void> {
    const others = this.tabs.filter((tab) => tab !== keep);
    const dirty = others.filter((tab) => tab.dirty);
    if (dirty.length) {
      const choice = await choiceDialog({
        title: `Close ${others.length} other editor${others.length === 1 ? "" : "s"}?`,
        message: `${dirty.length} editor${dirty.length === 1 ? " has" : "s have"} unsaved changes.`,
        choices: [{ id: "cancel", label: "Cancel" }, { id: "discard", label: "Discard All", danger: true }, { id: "save", label: "Save All", primary: true }],
      });
      if (!choice || choice === "cancel") return;
      if (choice === "save") {
        for (const tab of dirty) if (!(await this.saveTab(tab))) return;
      }
    }
    for (const tab of [...others]) await this.closeTab(tab, true);
    this.activateTab(keep.id);
  }

  private async copyPath(tab: OpenTab, relative: boolean): Promise<void> {
    if (!tab.ref) return;
    const root = this.roots.find((candidate) => candidate.id === tab.ref?.rootId);
    const value = relative
      ? (this.roots.length === 1 ? tab.ref.path : `${root?.label || "workspace"}/${tab.ref.path}`)
      : tab.hostPath;
    try {
      await copyText(value);
      toast(relative ? "Copied relative path" : "Copied path");
    } catch (error) {
      await choiceDialog({ title: "Copy path", message: value, detail: error instanceof Error ? error.message : String(error), choices: [{ id: "close", label: "Close", primary: true }] });
    }
  }

  private async reveal(ref: FileRef): Promise<void> {
    if (!this.workspace) return;
    try {
      await editorAPI.revealEntry(this.workspace.id, ref);
      toast("Opened on Echo host");
    } catch (error) {
      toast(error instanceof Error ? error.message : String(error));
    }
  }

  private async showTrash(): Promise<void> {
    if (!this.workspace) return;
    const overlay = document.createElement("div");
    overlay.className = "code-modal-overlay";
    overlay.innerHTML = `<section class="code-modal code-trash-dialog" role="dialog" aria-modal="true"><header><h2>Echo Trash</h2><button type="button" data-close aria-label="Close"><span class="codicon codicon-close"></span></button></header><div class="code-trash-list" data-trash-list><p>Loading…</p></div></section>`;
    document.body.appendChild(overlay);
    const close = () => overlay.remove();
    overlay.querySelector("[data-close]")?.addEventListener("click", close);
    overlay.addEventListener("keydown", (event) => { if (event.key === "Escape") close(); });
    const render = async () => {
      const list = overlay.querySelector<HTMLElement>("[data-trash-list]")!;
      try {
        const items = await editorAPI.listTrash(this.workspace!.id);
        list.innerHTML = items.length ? items.map((item) => `
          <div class="code-trash-item" data-trash-id="${escapeHTML(item.id)}">
            <span class="codicon codicon-${item.kind === "directory" ? "folder" : "file"}"></span>
            <div><strong>${escapeHTML(item.name)}</strong><span>${escapeHTML(item.hostPath)}</span><small>${new Date(item.deletedAt).toLocaleString()}</small></div>
            <button type="button" data-restore>Restore</button><button type="button" data-purge class="is-danger">Delete Permanently</button>
          </div>`).join("") : `<div class="code-empty-list"><span class="codicon codicon-trash"></span><p>Echo Trash is empty.</p></div>`;
        list.onclick = async (event) => {
          const row = (event.target as Element).closest<HTMLElement>("[data-trash-id]");
          if (!row) return;
          const id = row.dataset.trashId!;
          if ((event.target as Element).closest("[data-restore]")) {
            try {
              await editorAPI.restoreTrash(this.workspace!.id, id);
              await this.refreshExplorer();
              await render();
              toast("Item restored");
            } catch (error) { toast(error instanceof Error ? error.message : String(error)); }
          } else if ((event.target as Element).closest("[data-purge]")) {
            const confirm = await choiceDialog({
              title: "Delete permanently?", message: "This item cannot be recovered after permanent deletion.",
              choices: [{ id: "cancel", label: "Cancel" }, { id: "delete", label: "Delete Permanently", danger: true }],
            });
            if (confirm === "delete") {
              await editorAPI.purgeTrash(this.workspace!.id, id);
              await render();
            }
          }
        };
      } catch (error) {
        list.innerHTML = `<p class="code-error-text">${escapeHTML(error instanceof Error ? error.message : String(error))}</p>`;
      }
    };
    await render();
    overlay.querySelector<HTMLButtonElement>("[data-close]")?.focus();
  }

  private registerCommands(): void {
    this.commands = [
      { id: "file.save", label: "File: Save", keybinding: "Ctrl+S", run: () => this.saveTab() },
      { id: "file.saveAs", label: "File: Save As…", keybinding: "Ctrl+Shift+S", run: () => this.saveAsActive() },
      { id: "file.new", label: "File: New Untitled File", keybinding: "Ctrl+N", run: () => this.newUntitled() },
      { id: "file.quickOpen", label: "Go to File…", keybinding: "Ctrl+P", run: () => this.showQuickOpen() },
      { id: "view.commandPalette", label: "View: Show Command Palette", keybinding: "Ctrl+Shift+P", run: () => this.showCommandPalette() },
      { id: "navigation.back", label: "Go: Back", keybinding: "Alt+Left", run: () => window.history.back() },
      { id: "navigation.forward", label: "Go: Forward", keybinding: "Alt+Right", run: () => window.history.forward() },
      { id: "search.findInFiles", label: "Search: Find in Files", keybinding: "Ctrl+Shift+F", run: () => this.showWorkspaceSearch() },
      { id: "search.replaceInFiles", label: "Search: Replace in Files", keybinding: "Ctrl+Shift+H", run: () => this.showWorkspaceSearch(true) },
      { id: "file.close", label: "View: Close Editor", keybinding: "Ctrl+W", run: () => { const tab = this.activeTab(); if (tab) return this.closeTab(tab); } },
      { id: "explorer.refresh", label: "Explorer: Refresh", run: () => this.refreshExplorer() },
      { id: "explorer.collapseAll", label: "Explorer: Collapse All", run: () => this.collapseAll() },
      { id: "explorer.newFile", label: "Explorer: New File", run: () => this.createUnderSelection("file") },
      { id: "explorer.newFolder", label: "Explorer: New Folder", run: () => this.createUnderSelection("directory") },
      { id: "explorer.trash", label: "Explorer: Open Echo Trash", run: () => this.showTrash() },
      { id: "editor.find", label: "Editor: Find", keybinding: "Ctrl+F", run: () => this.showEditorFind() },
      { id: "editor.replace", label: "Editor: Replace", keybinding: "Ctrl+H", run: () => this.showEditorFind(true) },
      { id: "editor.gotoLine", label: "Go to Line/Column…", keybinding: "Ctrl+G", run: () => this.editor.trigger("echo", "editor.action.gotoLine", null) },
      { id: "editor.rename", label: "Editor: Rename Symbol", keybinding: "F2", run: () => this.activeCodeEditor()?.trigger("echo", "editor.action.rename", null) },
      { id: "editor.duplicateSelection", label: "Editor: Duplicate Selection", keybinding: "Ctrl+D", run: () => this.activeCodeEditor()?.trigger("echo", "editor.action.duplicateSelection", null) },
      { id: "editor.hover", label: "Editor: Show Hover", run: () => this.activeCodeEditor()?.trigger("echo", "editor.action.showHover", {}) },
      { id: "editor.goToDefinition", label: "Go to Definition", keybinding: "F12", run: () => this.activeCodeEditor()?.trigger("echo", "editor.action.revealDefinition", null) },
      { id: "editor.peekDefinition", label: "Peek Definition", keybinding: "Alt+F12", run: () => this.activeCodeEditor()?.trigger("echo", "editor.action.peekDefinition", null) },
      { id: "editor.goToDeclaration", label: "Go to Declaration", run: () => this.activeCodeEditor()?.trigger("echo", "editor.action.revealDeclaration", null) },
      { id: "editor.peekDeclaration", label: "Peek Declaration", run: () => this.activeCodeEditor()?.trigger("echo", "editor.action.peekDeclaration", null) },
      { id: "editor.goToTypeDefinition", label: "Go to Type Definition", run: () => this.activeCodeEditor()?.trigger("echo", "editor.action.goToTypeDefinition", null) },
      { id: "editor.peekTypeDefinition", label: "Peek Type Definition", run: () => this.activeCodeEditor()?.trigger("echo", "editor.action.peekTypeDefinition", null) },
      { id: "editor.goToImplementation", label: "Go to Implementations", keybinding: "Ctrl+F12", run: () => this.activeCodeEditor()?.trigger("echo", "editor.action.goToImplementation", null) },
      { id: "editor.peekImplementation", label: "Peek Implementations", keybinding: "Ctrl+Shift+F12", run: () => this.activeCodeEditor()?.trigger("echo", "editor.action.peekImplementation", null) },
      { id: "editor.peekReferences", label: "Peek References", keybinding: "Shift+F12", run: () => this.activeCodeEditor()?.trigger("echo", "editor.action.referenceSearch.trigger", null) },
      { id: "editor.format", label: "Editor: Format Document", keybinding: "Shift+Alt+F", run: () => this.formatActiveDocument(false) },
      { id: "editor.formatSelection", label: "Editor: Format Selection", keybinding: "Ctrl+K Ctrl+F", run: () => this.formatActiveDocument(true) },
      { id: "editor.workspaceSymbols", label: "Go to Symbol in Workspace…", keybinding: "Ctrl+Shift+O", run: () => this.showWorkspaceSymbols() },
      { id: "editor.undo", label: "Editor: Undo", keybinding: "Ctrl+Z", run: () => this.editor.trigger("echo", "undo", null) },
      { id: "editor.redo", label: "Editor: Redo", keybinding: "Ctrl+Shift+Z", run: () => this.editor.trigger("echo", "redo", null) },
      { id: "editor.cursorBelow", label: "Editor: Add Cursor Below", keybinding: "Ctrl+Alt+Down", run: () => this.editor.trigger("echo", "editor.action.insertCursorBelow", null) },
      { id: "editor.fold", label: "Editor: Fold", run: () => this.editor.trigger("echo", "editor.fold", null) },
      { id: "editor.unfold", label: "Editor: Unfold", run: () => this.editor.trigger("echo", "editor.unfold", null) },
      { id: "editor.bracket", label: "Editor: Go to Bracket", run: () => this.editor.trigger("echo", "editor.action.jumpToBracket", null) },
    ];
  }

  private activeCodeEditor(): MonacoEditor.ICodeEditor | null {
    const tab = this.activeTab();
    return tab?.kind === "diff" ? this.diffEditor?.getModifiedEditor() || null : this.editor || null;
  }

  private captureNavigationLocation(): CodeNavigationLocation | null {
    const tab = this.activeTab();
    if (!this.workspace || !tab || tab.kind === "media" || (tab.kind === "diff" && !tab.diff?.editable)) return null;
    const ref = this.worktreeRef(tab);
    const editor = this.activeCodeEditor();
    if (!ref || !editor?.getModel()) return null;
    return {
      workspaceId: this.workspace.id,
      ref: { ...ref },
      tabId: tab.id,
      editorKind: tab.kind === "diff" ? "diff" : "file",
      selections: (editor.getSelections() || []).map((selection) => ({
        selectionStartLineNumber: selection.selectionStartLineNumber,
        selectionStartColumn: selection.selectionStartColumn,
        positionLineNumber: selection.positionLineNumber,
        positionColumn: selection.positionColumn,
      })),
      scrollTop: editor.getScrollTop(),
      scrollLeft: editor.getScrollLeft(),
    };
  }

  private observeNavigationLocation(recordLargeJump: boolean): void {
    if (!this.navigationReady || this.navigationSuppression || !this.codeNavigation) return;
    const next = this.captureNavigationLocation();
    if (!next) return;
    const previous = this.lastNavigationLocation;
    if (recordLargeJump && isLargeCodeNavigationJump(previous, next)) {
      this.codeNavigation.recordTransition(previous, next);
    } else {
      this.codeNavigation.updateCurrent(next);
    }
    this.lastNavigationLocation = next;
  }

  private async recordCodeNavigation<T>(operation: () => T | Promise<T>): Promise<T> {
    if (!this.navigationReady || !this.codeNavigation) return await operation();
    const source = this.captureNavigationLocation();
    this.navigationSuppression++;
    try {
      return await operation();
    } finally {
      this.navigationSuppression--;
      const destination = this.captureNavigationLocation();
      this.codeNavigation.recordTransition(source, destination);
      this.lastNavigationLocation = destination;
    }
  }

  private async restoreNavigationLocation(location: CodeNavigationLocation): Promise<boolean> {
    if (!this.workspace || location.workspaceId !== this.workspace.id) return false;
    this.navigationSuppression++;
    try {
      let tab = location.tabId ? this.tabs.find((candidate) => candidate.id === location.tabId) : undefined;
      const existingTabRef = tab ? this.worktreeRef(tab) : null;
      if (!tab || !existingTabRef || refKey(existingTabRef) !== refKey(location.ref)) {
        if (!(await this.openFile(location.ref, false, false, false))) return false;
        tab = this.tabs.find((candidate) => {
          const ref = this.worktreeRef(candidate);
          return ref && refKey(ref) === refKey(location.ref);
        });
      }
      if (!tab) return false;
      const tabRef = this.worktreeRef(tab);
      if (!tabRef || refKey(tabRef) !== refKey(location.ref)) return false;
      /* A closed editable diff falls back to its workspace file because the
         location deliberately stores no Git snapshot payload. */
      if (tab.kind === "media" || (tab.kind === "diff" && !tab.diff?.editable)) return false;
      this.activateTab(tab.id, false);
      const editor = this.activeCodeEditor();
      const model = editor?.getModel();
      if (!editor || !model) return false;
      const selections = location.selections.map((selection) => {
        const start = model.validatePosition({
          lineNumber: selection.selectionStartLineNumber,
          column: selection.selectionStartColumn,
        });
        const position = model.validatePosition({
          lineNumber: selection.positionLineNumber,
          column: selection.positionColumn,
        });
        return {
          selectionStartLineNumber: start.lineNumber,
          selectionStartColumn: start.column,
          positionLineNumber: position.lineNumber,
          positionColumn: position.column,
        };
      });
      if (selections.length) editor.setSelections(selections);
      editor.setScrollPosition({ scrollTop: Math.max(0, location.scrollTop), scrollLeft: Math.max(0, location.scrollLeft) });
      editor.focus();
      const restored = this.captureNavigationLocation() || location;
      this.codeNavigation?.finishTraversal(restored);
      this.lastNavigationLocation = restored;
      return true;
    } finally {
      this.navigationSuppression--;
    }
  }

  private async handleNavigationTraversal(state: unknown): Promise<void> {
    if (!this.codeNavigation) return;
    const departing = this.navigationSkipping ? null : this.captureNavigationLocation();
    this.navigationSkipping = false;
    const traversal = this.codeNavigation.beginTraversal(state, departing);
    if (routePathFromHash(window.location.hash) !== CODE_ROUTE || !traversal) return;
    this.setSidebar(codeSidebarFromHash(window.location.hash), false);
    const generation = ++this.navigationRestoreGeneration;
    if (await this.restoreNavigationLocation(traversal.location)) return;
    if (generation !== this.navigationRestoreGeneration) return;
    toast(`Could not restore ${traversal.location.ref.path}; skipping that navigation location.`);
    if (traversal.direction) {
      this.navigationSkipping = true;
      window.history.go(traversal.direction);
    }
  }

  private showEditorFind(replace = false): void {
    const editor = this.activeCodeEditor();
    if (!editor?.getModel()) return;
    editor.trigger("echo", replace ? "editor.action.startFindReplaceAction" : "actions.find", null);
  }

  private async formatActiveDocument(selectionOnly: boolean): Promise<void> {
    const editor = this.activeCodeEditor();
    const model = editor?.getModel();
    if (!editor || !model || !this.lsp) return;
    const selection = editor.getSelection();
    if (selectionOnly && (!selection || selection.isEmpty())) {
      toast("Select text before formatting a selection.");
      return;
    }
    try {
      await this.lsp.format(model, selectionOnly ? selection! : undefined);
    } catch (error) {
      toast(error instanceof Error ? error.message : String(error), { sticky: true });
    }
  }

  private async saveAsActive(): Promise<void> {
    const active = this.activeTab();
    if (!active) return;
    if (active.kind === "diff") {
      await this.saveEditableDiff(active);
      return;
    }
    if (!active.ref) {
      await this.saveUntitled(active);
      return;
    }
    const duplicate = this.newUntitledFrom(active.model.getValue(), active.title);
    duplicate.hasBom = active.hasBom;
    await this.saveUntitled(duplicate);
  }

  private newUntitledFrom(content: string, title: string, id: string = randomUUID()): OpenTab {
    const model = monaco.editor.createModel(content, languageForPath(title, this.lspProfiles), monaco.Uri.from({ scheme: "untitled", authority: this.workspace?.id || "workspace", path: `/${id}` }));
    this.retainModel(model);
    const tab: OpenTab = {
      kind: "file", id, ref: null, title, hostPath: "", pinned: true, dirty: true,
      deleted: false, conflict: false, revision: "", hasBom: false, eol: model.getEOL() === "\r\n" ? "crlf" : "lf",
      model, viewState: null, changeDisposable: { dispose() {} }, applying: false,
    };
    tab.changeDisposable = model.onDidChangeContent(() => { tab.dirty = true; this.renderTabs(); this.schedulePersist(); });
    this.tabs.push(tab);
    this.activateTab(tab.id);
    this.renderTabs();
    return tab;
  }

  private showQuickOpen(): void {
    if (!this.workspace) return;
    const overlay = document.createElement("div");
    overlay.className = "code-picker-overlay";
    overlay.innerHTML = `<section class="code-picker" role="dialog" aria-modal="true"><div class="code-picker-input"><span class="codicon codicon-search"></span><input aria-label="Go to File" placeholder="Type a file name"></div><div class="code-picker-meta" data-picker-meta>Type to search workspace files</div><div class="code-picker-list" role="listbox" data-picker-list></div></section>`;
    document.body.appendChild(overlay);
    const input = overlay.querySelector<HTMLInputElement>("input")!;
    const list = overlay.querySelector<HTMLElement>("[data-picker-list]")!;
    const meta = overlay.querySelector<HTMLElement>("[data-picker-meta]")!;
    let results: SearchResult[] = [];
    let selected = 0;
    let timer = 0;
    let closed = false;
    let searchGeneration = 0;
    const close = () => { closed = true; window.clearTimeout(timer); overlay.remove(); };
    const render = () => {
      list.innerHTML = results.map((result, index) => {
        const root = this.roots.find((candidate) => candidate.id === result.ref.rootId);
        const directory = result.ref.path.includes("/") ? result.ref.path.slice(0, result.ref.path.lastIndexOf("/")) : root?.label || "";
        return `<button type="button" role="option" aria-selected="${index === selected}" class="${index === selected ? "is-selected" : ""}" data-result-index="${index}"><span class="codicon codicon-file-code"></span><strong>${escapeHTML(result.name)}</strong><span>${escapeHTML(directory)}</span></button>`;
      }).join("");
    };
    const search = async () => {
      const generation = ++searchGeneration;
      const query = input.value;
      try {
        const response = await editorAPI.searchFiles(this.workspace!.id, query);
        if (closed || generation !== searchGeneration || query !== input.value) return;
        results = response.items;
        selected = Math.min(selected, Math.max(0, results.length - 1));
        meta.textContent = response.indexing ? `Indexing… ${response.indexed.toLocaleString()} files available` : `${response.indexed.toLocaleString()} files indexed${response.truncated ? " (index limit reached)" : ""}`;
        render();
        if (response.indexing) timer = window.setTimeout(search, 450);
      } catch (error) {
        meta.textContent = error instanceof Error ? error.message : String(error);
      }
    };
    const choose = async () => {
      const result = results[selected];
      if (!result) return;
      close();
      await this.recordCodeNavigation(() => this.openFile(result.ref, true));
    };
    input.addEventListener("input", () => {
      searchGeneration++;
      window.clearTimeout(timer);
      timer = window.setTimeout(search, 120);
    });
    overlay.addEventListener("click", (event) => {
      if (event.target === overlay) close();
      const row = (event.target as Element).closest<HTMLElement>("[data-result-index]");
      if (row) { selected = Number(row.dataset.resultIndex); void choose(); }
    });
    overlay.addEventListener("keydown", (event) => {
      if (event.key === "Escape") close();
      else if (event.key === "ArrowDown") { event.preventDefault(); selected = Math.min(results.length - 1, selected + 1); render(); }
      else if (event.key === "ArrowUp") { event.preventDefault(); selected = Math.max(0, selected - 1); render(); }
      else if (event.key === "Enter") { event.preventDefault(); void choose(); }
    });
    input.focus();
    void search();
  }

  private showCommandPalette(): void {
    const overlay = document.createElement("div");
    overlay.className = "code-picker-overlay";
    overlay.innerHTML = `<section class="code-picker" role="dialog" aria-modal="true"><div class="code-picker-input"><span>&gt;</span><input aria-label="Command Palette" placeholder="Type a command"></div><div class="code-picker-list" role="listbox" data-picker-list></div></section>`;
    document.body.appendChild(overlay);
    const input = overlay.querySelector<HTMLInputElement>("input")!;
    const list = overlay.querySelector<HTMLElement>("[data-picker-list]")!;
    let filtered = this.commands;
    let selected = 0;
    const close = () => overlay.remove();
    const render = () => {
      const query = input.value.trim().toLowerCase();
      filtered = this.commands.filter((command) => command.label.toLowerCase().includes(query));
      selected = Math.min(selected, Math.max(0, filtered.length - 1));
      list.innerHTML = filtered.map((command, index) => `<button type="button" role="option" class="${index === selected ? "is-selected" : ""}" data-command-index="${index}"><span class="codicon codicon-terminal"></span><strong>${escapeHTML(command.label)}</strong>${command.keybinding ? `<kbd>${escapeHTML(command.keybinding)}</kbd>` : ""}</button>`).join("");
    };
    const choose = () => { const command = filtered[selected]; close(); if (command) void command.run(); };
    input.addEventListener("input", render);
    overlay.addEventListener("click", (event) => {
      if (event.target === overlay) close();
      const row = (event.target as Element).closest<HTMLElement>("[data-command-index]");
      if (row) { selected = Number(row.dataset.commandIndex); choose(); }
    });
    overlay.addEventListener("keydown", (event) => {
      if (event.key === "Escape") close();
      else if (event.key === "ArrowDown") { event.preventDefault(); selected = Math.min(filtered.length - 1, selected + 1); render(); }
      else if (event.key === "ArrowUp") { event.preventDefault(); selected = Math.max(0, selected - 1); render(); }
      else if (event.key === "Enter") { event.preventDefault(); choose(); }
    });
    render();
    input.focus();
  }

  private installEvents(): void {
    const signal = this.abort.signal;
    window.addEventListener("popstate", (event) => { void this.handleNavigationTraversal(event.state); }, { signal });
    window.addEventListener("pagehide", () => this.codeNavigation?.dispose(this.captureNavigationLocation()), { signal });
    this.treeCanvas.addEventListener("click", (event) => {
      if ((event.target as Element).closest("[data-rename-input]")) return;
      const row = (event.target as Element).closest<HTMLElement>("[data-tree-key]");
      if (!row) return;
      const node = this.nodes.get(row.dataset.treeKey || "");
      if (!node) return;
      this.treeScroller.focus();
      void this.toggleNode(node);
    }, { signal });
    this.treeCanvas.addEventListener("dblclick", (event) => {
      const row = (event.target as Element).closest<HTMLElement>("[data-tree-key]");
      const node = row ? this.nodes.get(row.dataset.treeKey || "") : null;
      if (node?.kind === "file" && !node.blockedReason) void this.recordCodeNavigation(() => this.openFile(node.ref, true, false));
    }, { signal });
    this.treeCanvas.addEventListener("contextmenu", (event) => {
      event.preventDefault();
      const row = (event.target as Element).closest<HTMLElement>("[data-tree-key]");
      const node = row ? this.nodes.get(row.dataset.treeKey || "") : null;
      if (node) this.showTreeMenu(event, node);
    }, { signal });
    this.treeCanvas.addEventListener("dragstart", (event) => {
      const row = (event.target as Element).closest<HTMLElement>("[data-tree-key]");
      const node = row ? this.nodes.get(row.dataset.treeKey || "") : null;
      if (!row || !node || node.isRoot || node.blockedReason || !event.dataTransfer) {
        event.preventDefault();
        return;
      }
      this.renamingKey = null;
      this.draggingTreeKey = node.key;
      row.classList.add("is-dragging");
      event.dataTransfer.effectAllowed = "move";
      event.dataTransfer.setData("application/x-echo-tree-entry", node.key);
      event.dataTransfer.setData("text/plain", node.name);
    }, { signal });
    this.treeCanvas.addEventListener("dragover", (event) => {
      const source = this.nodes.get(this.draggingTreeKey || "");
      const row = (event.target as Element).closest<HTMLElement>("[data-tree-key]");
      const target = row ? this.nodes.get(row.dataset.treeKey || "") : undefined;
      const destination = this.treeMoveDestination(source, target);
      this.setTreeDropTarget(destination?.key || null);
      this.startTreeDragScroll(event.clientY);
      if (!destination || !event.dataTransfer) return;
      event.preventDefault();
      event.dataTransfer.dropEffect = "move";
    }, { signal });
    this.treeCanvas.addEventListener("drop", (event) => {
      const source = this.nodes.get(this.draggingTreeKey || "");
      const row = (event.target as Element).closest<HTMLElement>("[data-tree-key]");
      const target = row ? this.nodes.get(row.dataset.treeKey || "") : undefined;
      const destination = this.treeMoveDestination(source, target);
      if (destination) event.preventDefault();
      this.clearTreeDragState();
      if (source && destination) void this.moveTreeNode(source, destination);
    }, { signal });
    this.treeCanvas.addEventListener("dragend", () => this.clearTreeDragState(), { signal });
    this.treeCanvas.addEventListener("dragleave", (event) => {
      if (!this.treeCanvas.contains(event.relatedTarget as Node | null)) this.setTreeDropTarget(null);
    }, { signal });
    this.treeCanvas.addEventListener("keydown", (event) => {
      const input = (event.target as Element).closest<HTMLInputElement>("[data-rename-input]");
      if (!input || !this.renamingKey) return;
      const node = this.nodes.get(this.renamingKey);
      if (!node) return;
      if (event.key === "Enter") { event.preventDefault(); void this.commitRename(node, input.value); }
      if (event.key === "Escape") { event.preventDefault(); this.renamingKey = null; this.renderTreeRows(); this.treeScroller.focus(); }
    }, { signal });
    this.treeCanvas.addEventListener("focusout", (event) => {
      const input = (event.target as Element).closest<HTMLInputElement>("[data-rename-input]");
      if (!input || !this.renamingKey) return;
      const node = this.nodes.get(this.renamingKey);
      if (node) window.setTimeout(() => { if (this.renamingKey === node.key) void this.commitRename(node, input.value); }, 0);
    }, { signal });
    this.treeScroller.addEventListener("keydown", (event) => this.handleTreeKeyboard(event), { signal });
    this.treeScroller.addEventListener("scroll", () => this.schedulePersist(), { signal, passive: true });

    this.root.querySelector("[data-tree-action=new-file]")?.addEventListener("click", () => void this.createUnderSelection("file"), { signal });
    this.root.querySelector("[data-tree-action=new-folder]")?.addEventListener("click", () => void this.createUnderSelection("directory"), { signal });
    this.root.querySelector("[data-tree-action=refresh]")?.addEventListener("click", () => void this.refreshExplorer(), { signal });
    this.root.querySelector("[data-tree-action=collapse-all]")?.addEventListener("click", () => this.collapseAll(), { signal });
    this.root.querySelector("[data-tree-action=trash]")?.addEventListener("click", () => void this.showTrash(), { signal });
    this.root.querySelector("[data-status=lsp]")?.addEventListener("click", () => {
      if (this.lspState === "denied") {
        this.lsp?.takeOverActiveDocument();
        return;
      }
      if (this.lspState === "failed" && this.workspace && this.lspStatus) {
        void editorAPI.restartLanguageServer(this.workspace.id, this.lspStatus.profileId).catch((error) => toast(error instanceof Error ? error.message : String(error), { sticky: true }));
        return;
      }
      location.hash = "#/settings?section=lsp";
    }, { signal });
    this.root.querySelector("[data-mobile-explorer]")?.addEventListener("click", () => {
      const shell = this.root.querySelector<HTMLElement>(".code-app-shell");
      this.setMobileExplorer(!shell?.classList.contains("is-explorer-open"));
    }, { signal });
    this.root.querySelector("[data-mobile-sidebar-backdrop]")?.addEventListener("click", () => {
      this.setMobileExplorer(false);
    }, { signal });
    this.root.querySelector("[data-code-chat-toggle]")?.addEventListener("click", () => {
      this.setCodeChatOpen(!this.codeChatOpen);
    }, { signal });
    this.root.querySelector("[data-code-chat-backdrop]")?.addEventListener("click", () => {
      this.setCodeChatOpen(false, true);
    }, { signal });
    this.root.querySelector("[data-diff-toolbar]")?.addEventListener("click", (event) => {
      const action = (event.target as Element).closest<HTMLElement>("[data-diff-action]")?.dataset.diffAction;
      if (action === "previous" || action === "next") void this.diffEditor.goToDiff(action);
      if (action === "layout") {
        this.splitGitDiff = !this.splitGitDiff;
        this.diffEditor.updateOptions({ renderSideBySide: this.splitGitDiff });
        this.updateDiffLayoutState();
      }
    }, { signal });

    const tabs = this.root.querySelector<HTMLElement>("[data-code-tabs]")!;
    tabs.addEventListener("click", (event) => {
      const element = (event.target as Element).closest<HTMLElement>("[data-tab-id]");
      if (!element) return;
      const tab = this.tabs.find((candidate) => candidate.id === element.dataset.tabId);
      if (!tab) return;
      if ((event.target as Element).closest("[data-tab-close]")) void this.closeTab(tab);
      else void this.recordCodeNavigation(() => this.activateTab(tab.id));
    }, { signal });
    tabs.addEventListener("mousedown", (event) => {
      // Suppress the browser's native middle-click autoscroll when the tab bar
      // overflows and becomes scrollable, so middle-click keeps closing tabs
      // instead of starting a horizontal scroll drag.
      if (event.button !== 1) return;
      if (!(event.target as Element).closest("[data-tab-id]")) return;
      event.preventDefault();
    }, { signal });
    tabs.addEventListener("auxclick", (event) => {
      if (event.button !== 1) return;
      const element = (event.target as Element).closest<HTMLElement>("[data-tab-id]");
      if (!element) return;
      const tab = this.tabs.find((candidate) => candidate.id === element.dataset.tabId);
      if (!tab) return;
      event.preventDefault();
      void this.closeTab(tab);
    }, { signal });
    tabs.addEventListener("dblclick", (event) => {
      const element = (event.target as Element).closest<HTMLElement>("[data-tab-id]");
      if (element) {
        const tab = this.tabs.find((candidate) => candidate.id === element.dataset.tabId);
        if (tab) { tab.pinned = true; this.renderTabs(); this.schedulePersist(); }
      } else {
        this.newUntitled();
      }
    }, { signal });
    tabs.addEventListener("contextmenu", (event) => {
      const element = (event.target as Element).closest<HTMLElement>("[data-tab-id]");
      if (!element) return;
      event.preventDefault();
      const tab = this.tabs.find((candidate) => candidate.id === element.dataset.tabId);
      if (tab) this.showTabMenu(event, tab);
    }, { signal });
    tabs.addEventListener("wheel", (event) => {
      if (Math.abs(event.deltaY) > Math.abs(event.deltaX)) {
        tabs.scrollLeft += event.deltaY;
        event.preventDefault();
      }
    }, { signal, passive: false });

    this.root.querySelector("[data-breadcrumbs]")?.addEventListener("click", (event) => {
      const button = (event.target as Element).closest<HTMLElement>("[data-breadcrumb-index]");
      const tab = this.activeTab();
      if (!button || !tab?.ref) return;
      const index = Number(button.dataset.breadcrumbIndex);
      const parts = tab.ref.path.split("/");
      const target: FileRef = { rootId: tab.ref.rootId, path: index === 0 ? "" : parts.slice(0, index).join("/") };
      void this.expandTo(target);
    }, { signal });

    document.addEventListener("keydown", (event) => this.handleGlobalKeyboard(event), { signal, capture: true });
    document.addEventListener("click", (event) => this.handleReferencePeekClick(event), { signal, capture: true });
    document.addEventListener("keyup", (event) => {
      if (event.key === "Control" || event.key === "Meta") this.finishMruCycle();
    }, { signal });
    window.addEventListener("blur", () => this.finishMruCycle(), { signal });
    document.addEventListener("wheel", (event) => {
      if (!event.ctrlKey) return;
      const target = event.target as Element | null;
      const inEditor = Boolean(target?.closest && target.closest("[data-monaco-host], [data-monaco-diff-host]"));
      if (!inEditor) return;
      event.preventDefault();
      event.stopPropagation();
      this.setEditorFontSize(this.editorFontSize + (event.deltaY < 0 ? 1 : -1));
    }, { signal, capture: true, passive: false });
    document.addEventListener("visibilitychange", () => { if (document.visibilityState === "hidden") void this.persistNow(); }, { signal });
    window.addEventListener("beforeunload", (event) => {
      void this.persistNow();
      if (this.persistenceFailed && this.tabs.some((tab) => tab.dirty)) event.preventDefault();
    }, { signal });
    this.installResizer();
    this.installCodeChatResizer();
  }

  private showWorkspaceSymbols(): void {
    if (!this.lsp) return;
    const overlay = document.createElement("div");
    overlay.className = "code-picker-overlay";
    overlay.innerHTML = `<section class="code-picker" role="dialog" aria-modal="true"><div class="code-picker-input"><span class="codicon codicon-symbol-method"></span><input aria-label="Workspace Symbol Search" placeholder="Type a symbol name"></div><div class="code-picker-meta" data-picker-meta>Search symbols from running language servers</div><div class="code-picker-list" role="listbox" data-picker-list></div></section>`;
    document.body.appendChild(overlay);
    const input = overlay.querySelector<HTMLInputElement>("input")!;
    const list = overlay.querySelector<HTMLElement>("[data-picker-list]")!;
    const meta = overlay.querySelector<HTMLElement>("[data-picker-meta]")!;
    let results: Array<{ name: string; containerName?: string; location?: { uri: string; range: { start: { line: number; character: number }; end: { line: number; character: number } } } }> = [];
    let selected = 0;
    let generation = 0;
    let timer = 0;
    const close = () => { window.clearTimeout(timer); overlay.remove(); };
    const render = () => {
      list.innerHTML = results.map((symbol, index) => `<button type="button" role="option" aria-selected="${index === selected}" class="${index === selected ? "is-selected" : ""}" data-symbol-index="${index}"><span class="codicon codicon-symbol-method"></span><strong>${escapeHTML(symbol.name)}</strong><span>${escapeHTML(symbol.containerName || symbol.location?.uri || "")}</span></button>`).join("");
    };
    const search = async () => {
      const current = ++generation;
      try {
        const groups = await this.lsp!.workspaceSymbols(input.value.trim());
        if (current !== generation || !overlay.isConnected) return;
        results = groups.flatMap((group) => group.symbols).filter((symbol) => symbol?.location?.uri && this.refForFileURI(symbol.location.uri));
        selected = Math.min(selected, Math.max(0, results.length - 1));
        meta.textContent = `${results.length} symbol${results.length === 1 ? "" : "s"}`;
        render();
      } catch (error) {
        if (current === generation) meta.textContent = error instanceof Error ? error.message : String(error);
      }
    };
    const choose = async () => {
      const symbol = results[selected];
      if (!symbol?.location) return;
      let resource: MonacoUri;
      try {
        resource = monaco.Uri.parse(symbol.location.uri);
      } catch {
        return;
      }
      const range = fromLSPRange(symbol.location.range);
      if (await this.openNavigationTarget(resource, { lineNumber: range.startLineNumber, column: range.startColumn })) close();
    };
    input.addEventListener("input", () => { generation++; window.clearTimeout(timer); timer = window.setTimeout(search, 120); });
    overlay.addEventListener("click", (event) => {
      if (event.target === overlay) close();
      const row = (event.target as Element).closest<HTMLElement>("[data-symbol-index]");
      if (row) { selected = Number(row.dataset.symbolIndex); void choose(); }
    });
    overlay.addEventListener("keydown", (event) => {
      if (event.key === "Escape") close();
      else if (event.key === "ArrowDown") { event.preventDefault(); selected = Math.min(results.length - 1, selected + 1); render(); }
      else if (event.key === "ArrowUp") { event.preventDefault(); selected = Math.max(0, selected - 1); render(); }
      else if (event.key === "Enter") { event.preventDefault(); void choose(); }
    });
    input.focus();
    void search();
  }

  private handleTreeKeyboard(event: KeyboardEvent): void {
    if ((event.target as Element).closest("[data-rename-input]")) return;
    const currentIndex = this.flatTree.findIndex((node) => node.key === this.selectedTreeKey);
    let nextIndex = currentIndex;
    if (event.key === "ArrowDown") nextIndex = Math.min(this.flatTree.length - 1, currentIndex + 1);
    else if (event.key === "ArrowUp") nextIndex = Math.max(0, currentIndex < 0 ? 0 : currentIndex - 1);
    else if (event.key === "Home") nextIndex = 0;
    else if (event.key === "End") nextIndex = this.flatTree.length - 1;
    else if (event.key === "F2") {
      const node = this.nodes.get(this.selectedTreeKey || "");
      if (node) { event.preventDefault(); void this.beginRename(node); }
      return;
    } else if (event.key === "Delete") {
      const node = this.nodes.get(this.selectedTreeKey || "");
      if (node) { event.preventDefault(); void this.deleteNode(node); }
      return;
    } else if (event.key === "Enter" || event.key === " ") {
      const node = this.nodes.get(this.selectedTreeKey || "");
      if (node) { event.preventDefault(); void this.toggleNode(node); }
      return;
    } else if (event.key === "ArrowRight") {
      const node = this.nodes.get(this.selectedTreeKey || "");
      if (node?.kind === "directory" && !this.expanded.has(node.key)) { event.preventDefault(); void this.toggleNode(node); }
      return;
    } else if (event.key === "ArrowLeft") {
      const node = this.nodes.get(this.selectedTreeKey || "");
      if (node?.kind === "directory" && this.expanded.has(node.key)) { event.preventDefault(); void this.toggleNode(node); }
      else if (node?.parentKey) { this.selectedTreeKey = node.parentKey; this.renderTree(); }
      return;
    } else return;
    if (nextIndex >= 0 && this.flatTree[nextIndex]) {
      event.preventDefault();
      this.selectedTreeKey = this.flatTree[nextIndex].key;
      this.renderTreeRows();
      this.treeVirtualizer.scrollToIndex(nextIndex, { align: "auto" });
    }
  }

  private handleGlobalKeyboard(event: KeyboardEvent): void {
    if (document.querySelector(".code-modal-overlay, .code-picker-overlay")) return;
    if (this.handleReferencePeekKeyboard(event)) return;
    if (event.key === "Escape" && this.root.querySelector("[data-chat-mention-picker]") && document.activeElement?.closest(".code-chat-surface")) return;
    if (event.key === "Escape" && this.codeChatOpen) {
      // When a file search/replace is active, Escape closes the search first;
      // only fall through to closing the code chat once the search is dismissed.
      if (this.searchIsActive()) return;
      event.preventDefault();
      this.setCodeChatOpen(false, true);
      return;
    }
    if (event.key === "Escape" && this.root.querySelector(".code-app-shell.is-explorer-open")) {
      event.preventDefault();
      this.setMobileExplorer(false);
      return;
    }
    const modifier = event.ctrlKey || event.metaKey;
    const key = event.key.toLowerCase();
    const activeEditor = this.activeCodeEditor();
    const activeTab = this.activeTab();
    const editorHasTextFocus = activeEditor?.hasTextFocus() === true;
    const hasMultilineSelection = activeEditor?.getSelections()
      ?.some((selection) => selection.startLineNumber !== selection.endLineNumber) === true;
    const selectionIsEditable = activeTab?.kind === "file" || activeTab?.diff?.editable === true;
    const shouldHandleTab = (key === "tab" || event.code === "Tab")
      && editorHasTextFocus && selectionIsEditable && (event.shiftKey || hasMultilineSelection);
    if (!modifier && event.altKey && !event.shiftKey && key === "arrowleft") {
      event.preventDefault();
      event.stopPropagation();
      window.history.back();
    } else if (!modifier && event.altKey && !event.shiftKey && key === "arrowright") {
      event.preventDefault();
      event.stopPropagation();
      window.history.forward();
    } else if (activeEditor && !modifier && !event.altKey && shouldHandleTab) {
      event.preventDefault();
      event.stopPropagation();
      activeEditor.trigger("echo", event.shiftKey ? "outdent" : "tab", null);
    } else if (modifier && event.shiftKey && key === "f") { event.preventDefault(); event.stopPropagation(); this.showWorkspaceSearch(); }
    else if (modifier && !event.shiftKey && key === "f" && activeEditor?.getModel()) { event.preventDefault(); event.stopPropagation(); this.showEditorFind(); }
    else if (modifier && event.shiftKey && key === "o") { event.preventDefault(); event.stopPropagation(); this.showWorkspaceSymbols(); }
    else if (modifier && event.shiftKey && key === "f12" && this.activeCodeEditor()?.hasTextFocus()) { event.preventDefault(); event.stopPropagation(); this.activeCodeEditor()?.trigger("echo", "editor.action.peekImplementation", null); }
    else if (modifier && !event.shiftKey && key === "f12" && this.activeCodeEditor()?.hasTextFocus()) { event.preventDefault(); event.stopPropagation(); this.activeCodeEditor()?.trigger("echo", "editor.action.goToImplementation", null); }
    else if (!modifier && event.shiftKey && !event.altKey && key === "f12" && this.activeCodeEditor()?.hasTextFocus()) { event.preventDefault(); event.stopPropagation(); this.activeCodeEditor()?.trigger("echo", "editor.action.referenceSearch.trigger", null); }
    else if (!modifier && event.shiftKey && event.altKey && key === "f") { event.preventDefault(); event.stopPropagation(); void this.formatActiveDocument(false); }
    else if (modifier && event.shiftKey && key === "h") { event.preventDefault(); event.stopPropagation(); this.showWorkspaceSearch(true); }
    else if (modifier && event.shiftKey && key === "j" && this.activeSidebar === "search") { event.preventDefault(); event.stopPropagation(); this.searchView?.toggleDetails(); }
    else if (modifier && event.shiftKey && key === "p") { event.preventDefault(); event.stopPropagation(); this.showCommandPalette(); }
    else if (modifier && event.shiftKey && key === "s") { event.preventDefault(); event.stopPropagation(); void this.saveAsActive(); }
    else if (modifier && key === "p") { event.preventDefault(); event.stopPropagation(); this.showQuickOpen(); }
    else if (modifier && key === "s") { event.preventDefault(); event.stopPropagation(); void this.saveTab(); }
    else if (modifier && !event.shiftKey && key === "e") { event.preventDefault(); event.stopPropagation(); this.setCodeChatOpen(!this.codeChatOpen); }
    else if (modifier && key === "w") { const tab = this.activeTab(); if (tab) { event.preventDefault(); event.stopPropagation(); void this.closeTab(tab); } }
    else if (modifier && key === "n") { event.preventDefault(); event.stopPropagation(); this.newUntitled(); }
    else if (modifier && !event.shiftKey && key === "d" && this.activeCodeEditor()?.hasTextFocus()) { event.preventDefault(); event.stopPropagation(); this.activeCodeEditor()?.trigger("echo", "editor.action.duplicateSelection", null); }
    else if (modifier && key === "tab") {
      event.preventDefault();
      event.stopPropagation();
      this.cycleCodeTabs(event.shiftKey);
    } else if (event.key === "F2" && this.treeScroller.contains(document.activeElement)) {
      const node = this.nodes.get(this.selectedTreeKey || "");
      if (node) { event.preventDefault(); void this.beginRename(node); }
    } else if (event.key === "F2" && this.activeCodeEditor()?.hasTextFocus()) {
      event.preventDefault();
      this.activeCodeEditor()?.trigger("echo", "editor.action.rename", null);
    } else if (event.key === "F4" && this.activeSidebar === "search") {
      event.preventDefault();
      this.searchView?.navigateResult(event.shiftKey ? -1 : 1);
    }
  }

  private referencePeekContext(target: EventTarget | null): {
    editor: MonacoEditor.ICodeEditor;
    tree: HTMLElement;
    match: HTMLElement | null;
  } | null {
    if (!(target instanceof Element)) return null;
    const tree = target.closest<HTMLElement>(".reference-zone-widget .ref-tree");
    if (!tree) return null;
    const editors = [
      this.editor,
      this.diffEditor.getOriginalEditor(),
      this.diffEditor.getModifiedEditor(),
    ];
    const editor = editors.find((candidate) => candidate.getDomNode()?.contains(tree));
    if (!editor) return null;
    return {
      editor,
      tree,
      match: tree.querySelector<HTMLElement>(".monaco-list-row.focused .referenceMatch"),
    };
  }

  private handleReferencePeekKeyboard(event: KeyboardEvent): boolean {
    if (event.ctrlKey || event.metaKey || event.altKey || event.shiftKey) return false;
    const context = this.referencePeekContext(event.target);
    if (!context) return false;
    if (event.key === "Enter" && context.match) {
      event.preventDefault();
      event.stopPropagation();
      context.editor.trigger("echo", "openReference", null);
      return true;
    }
    if (event.key === "ArrowUp" || event.key === "ArrowDown") {
      requestAnimationFrame(() => {
        if (this.abort.signal.aborted || !context.tree.isConnected) return;
        this.referencePeekContext(context.tree)?.match?.click();
      });
    }
    return false;
  }

  private handleReferencePeekClick(event: MouseEvent): void {
    if (event.button !== 0 || event.detail !== 2 || event.ctrlKey || event.metaKey || event.altKey || event.shiftKey) return;
    const context = this.referencePeekContext(event.target);
    if (!context?.match || !context.match.contains(event.target as Node)) return;
    event.preventDefault();
    event.stopPropagation();
    context.editor.trigger("echo", "openReference", null);
  }

  /**
   * True while a file search/replace is currently active: either the workspace
   * search sidebar input is focused, or Monaco's in-editor find/replace widget
   * is visible. Used so Escape closes the active search before falling back to
   * closing the code chat.
   */
  private searchIsActive(): boolean {
    if (this.activeSidebar === "search" && document.activeElement instanceof HTMLInputElement) return true;
    const editor = this.activeCodeEditor();
    if (!editor) return false;
    const findController = editor.getContribution<MonacoEditor.IEditorContribution & { getState(): { isRevealed: boolean } }>("editor.contrib.findController");
    return findController?.getState().isRevealed === true;
  }

  private cycleCodeTabs(reverse: boolean): void {
    if (this.tabs.length <= 1) return;
    if (!this.mruCycle) {
      this.mruCycle = beginMruCycle(
        this.mruTabIds,
        this.activeTabId,
        this.tabs.map((tab) => tab.id),
      );
    }
    const { nextId, state } = nextMruCycle(this.mruCycle, reverse);
    this.mruCycle = state;
    if (nextId) void this.recordCodeNavigation(() => this.activateTab(nextId, true, false));
    this.renderMruSwitcher();
  }

  private installResizer(): void {
    const resizer = this.root.querySelector<HTMLElement>(".code-explorer-resizer")!;
    const shell = this.root.querySelector<HTMLElement>(".code-app-shell")!;
    const setWidth = (value: number) => {
      this.explorerWidth = Math.max(220, Math.min(520, value));
      shell.style.setProperty("--explorer-width", `${this.explorerWidth}px`);
      this.editor?.layout();
    };
    resizer.addEventListener("pointerdown", (event) => {
      if (window.innerWidth <= 720) return;
      event.preventDefault();
      resizer.setPointerCapture(event.pointerId);
      const startX = event.clientX;
      const startWidth = this.explorerWidth;
      const move = (moveEvent: PointerEvent) => setWidth(startWidth + moveEvent.clientX - startX);
      const up = () => {
        resizer.removeEventListener("pointermove", move);
        resizer.removeEventListener("pointerup", up);
        resizer.classList.remove("is-dragging");
        this.schedulePersist();
      };
      resizer.classList.add("is-dragging");
      resizer.addEventListener("pointermove", move);
      resizer.addEventListener("pointerup", up);
    }, { signal: this.abort.signal });
    resizer.addEventListener("dblclick", () => { setWidth(280); this.schedulePersist(); }, { signal: this.abort.signal });
    resizer.addEventListener("keydown", (event) => {
      if (event.key === "ArrowLeft") { setWidth(this.explorerWidth - 10); event.preventDefault(); }
      if (event.key === "ArrowRight") { setWidth(this.explorerWidth + 10); event.preventDefault(); }
    }, { signal: this.abort.signal });
  }

  private installCodeChatResizer(): void {
    const resizer = this.root.querySelector<HTMLElement>(".code-chat-resizer")!;
    const setWidth = (value: number) => this.applyCodeChatWidth(value);
    resizer.setAttribute("aria-valuenow", String(this.codeChatWidth));
    resizer.addEventListener("pointerdown", (event) => {
      if (window.innerWidth <= 720) return;
      event.preventDefault();
      resizer.setPointerCapture(event.pointerId);
      const startX = event.clientX;
      const startWidth = this.codeChatWidth;
      const move = (moveEvent: PointerEvent) => setWidth(startWidth + startX - moveEvent.clientX);
      const up = () => {
        resizer.removeEventListener("pointermove", move);
        resizer.removeEventListener("pointerup", up);
        resizer.classList.remove("is-dragging");
        this.schedulePersist();
      };
      resizer.classList.add("is-dragging");
      resizer.addEventListener("pointermove", move);
      resizer.addEventListener("pointerup", up);
    }, { signal: this.abort.signal });
    resizer.addEventListener("dblclick", () => { setWidth(360); this.schedulePersist(); }, { signal: this.abort.signal });
    resizer.addEventListener("keydown", (event) => {
      if (event.key === "ArrowLeft") { setWidth(this.codeChatWidth + 10); event.preventDefault(); }
      if (event.key === "ArrowRight") { setWidth(this.codeChatWidth - 10); event.preventDefault(); }
      if (event.key === "Home") { setWidth(300); event.preventDefault(); }
      if (event.key === "End") { setWidth(640); event.preventDefault(); }
      if (event.defaultPrevented) this.schedulePersist();
    }, { signal: this.abort.signal });
    window.addEventListener("resize", () => {
      if (window.innerWidth > 720) setWidth(this.codeChatWidth);
      this.editor?.layout();
      this.diffEditor?.layout();
    }, { signal: this.abort.signal });
  }

  private applyCodeChatWidth(value: number): void {
    const workbench = this.root.querySelector<HTMLElement>(".code-editor-workspace");
    const available = workbench?.clientWidth || window.innerWidth;
    const maximum = Math.max(300, Math.min(640, Math.floor(available / 2)));
    this.codeChatWidth = Math.max(300, Math.min(maximum, value));
    this.root.querySelector<HTMLElement>(".code-app-shell")?.style.setProperty("--code-chat-width", `${this.codeChatWidth}px`);
    const resizer = this.root.querySelector<HTMLElement>(".code-chat-resizer");
    resizer?.setAttribute("aria-valuemax", String(maximum));
    resizer?.setAttribute("aria-valuenow", String(this.codeChatWidth));
    this.editor?.layout();
    this.diffEditor?.layout();
  }

  private setCodeChatOpen(open: boolean, restoreFocus = false): void {
    if (!this.workspace) return;
    const shell = this.root.querySelector<HTMLElement>(".code-app-shell");
    const dock = this.root.querySelector<HTMLElement>("[data-code-chat-dock]");
    const resizer = this.root.querySelector<HTMLElement>(".code-chat-resizer");
    const toggle = this.root.querySelector<HTMLButtonElement>("[data-code-chat-toggle]");
    if (!shell || !dock || !resizer || !toggle) return;
    if (open && !this.codeChatSurface) {
      this.codeChatSurface = mountChatSurface(dock, {
        workspaceId: this.workspace.id,
        surface: "code",
        title: "CODE CHAT",
        onClose: () => this.setCodeChatOpen(false, true),
        beforeSend: () => this.prepareEditorContext(),
        onActivateReference: (reference) => this.activateChatReference(reference),
        onStreamingChange: (streaming) => {
          toggle.classList.toggle("is-streaming", streaming);
          const action = this.codeChatOpen ? "Close code assistant" : "Open code assistant";
          toggle.setAttribute("aria-label", streaming ? `${action} (response streaming)` : action);
        },
        expectedChatId: this.completionTarget?.chatId,
        onExpectedChatResolved: (found) => {
          if (!found) toast("That completed Code Chat is no longer available.", { sticky: true });
          this.completionTarget = null;
          window.history.replaceState(window.history.state, "", codeRouteHash(this.activeSidebar));
        },
      });
      this.updateCodeChatSelectionNotice();
    }
    this.codeChatOpen = open;
    shell.classList.toggle("is-code-chat-open", open);
    dock.hidden = !open;
    resizer.hidden = !open;
    toggle.setAttribute("aria-expanded", String(open));
    toggle.title = open ? "Close Code Chat" : "Open Code Chat";
    const action = open ? "Close code assistant" : "Open code assistant";
    toggle.setAttribute("aria-label", toggle.classList.contains("is-streaming") ? `${action} (response streaming)` : action);
    if (open && window.innerWidth <= 720) this.setMobileExplorer(false);
    requestAnimationFrame(() => {
      this.editor?.layout();
      this.diffEditor?.layout();
      if (open) this.codeChatSurface?.focus();
      else if (restoreFocus) toggle.focus();
    });
  }

  private async activateChatReference(reference: ChatReference): Promise<void> {
    if (reference.kind === "file") {
      await this.recordCodeNavigation(() => this.openFile(reference.ref, true));
      return;
    }
    this.setSidebar("explorer");
    await this.expandTo(reference.ref);
    this.selectedTreeKey = refKey(reference.ref);
    this.renderTree();
  }

  private async prepareEditorContext(): Promise<EditorContextPayload | false> {
    const saved = await runCodeChatSavePreflight(
      this.tabs,
      (tab) => tab.dirty && Boolean(this.worktreeRef(tab)),
      (tab) => this.saveTab(tab),
    );
    if (!saved) {
      toast("Save the open file changes before sending them to Code Chat.", { sticky: true });
      return false;
    }
    return this.buildEditorContext();
  }

  private setActiveDiffSelectionSide(side: "original" | "modified"): void {
    const tab = this.activeTab();
    if (tab?.kind === "diff") this.diffSelectionSides.set(tab.id, side);
    this.updateCodeChatSelectionNotice();
  }

  private activeEditorSelections(): EditorContextSelection[] {
    const tab = this.activeTab();
    if (!tab || tab.kind === "media") return [];
    const side = tab.kind === "diff" ? this.diffSelectionSides.get(tab.id) || "modified" : undefined;
    const editor = tab.kind === "diff"
      ? side === "original" ? this.diffEditor.getOriginalEditor() : this.diffEditor.getModifiedEditor()
      : this.editor;
    const model = editor.getModel();
    if (!model) return [];
    return (editor.getSelections() || [])
      .filter((selection) => !selection.isEmpty())
      .map((selection) => ({
        ...(side ? { side } : {}),
        startLine: selection.startLineNumber,
        startColumn: selection.startColumn,
        endLine: selection.endLineNumber,
        endColumn: selection.endColumn,
        text: model.getValueInRange(selection),
      }));
  }

  private updateCodeChatSelectionNotice(): void {
    const tab = this.activeTab();
    const selections = this.activeEditorSelections();
    this.codeChatSurface?.setContextNotice(tab ? formatCodeChatSelectionNotice(tab.title, selections) : null);
  }

  private buildEditorContext(): EditorContextPayload {
    const activeSelections = this.activeEditorSelections();
    return buildCodeChatEditorContext(this.tabs.map((tab) => {
      const ref = tab.kind === "diff" ? tab.diff?.fileRef || tab.ref || undefined : tab.ref || undefined;
      const root = ref ? this.roots.find((candidate) => candidate.id === ref.rootId) : undefined;
      return {
        id: tab.id,
        kind: tab.kind === "diff" ? "diff" : tab.ref ? "file" : "untitled",
        title: tab.title,
        dirty: tab.dirty,
        ref,
        reference: ref ? `${root?.referenceLabel || root?.label || "workspace"}${ref.path ? `/${ref.path}` : ""}` : undefined,
        content: !ref && tab.kind !== "diff" ? tab.model.getValue() : undefined,
        selections: tab.id === this.activeTabId ? activeSelections : undefined,
        diff: tab.diff ? {
          repository: tab.diff.repository.label,
          scope: tab.diff.scope,
          reviewRef: tab.diff.reviewRef,
          oldPath: tab.diff.oldPath,
        } : undefined,
      };
    }), this.activeTabId);
  }

  private setMobileExplorer(open: boolean): void {
    if (open && this.codeChatOpen) this.setCodeChatOpen(false);
    this.root.querySelector(".code-app-shell")?.classList.toggle("is-explorer-open", open);
    this.root.querySelector("[data-mobile-explorer]")?.setAttribute("aria-expanded", String(open));
  }

  private async restoreWorkspace(): Promise<void> {
    if (!this.workspace) return;
    let saved: PersistedWorkspaceSession | null = null;
    try { saved = await loadSession(this.workspace.id); } catch (error) { console.warn("restore editor session", error); }
    if (this.abort.signal.aborted) return;
    if (!saved || (saved.version !== 1 && saved.version !== 2 && saved.version !== 3)) {
      this.applyCodeChatWidth(this.codeChatWidth);
      return;
    }
    this.explorerWidth = Math.max(220, Math.min(520, saved.explorerWidth || 280));
    this.applyCodeChatWidth(saved.codeChatWidth || 360);
    const shell = this.root.querySelector<HTMLElement>(".code-app-shell");
    shell?.style.setProperty("--explorer-width", `${this.explorerWidth}px`);
    this.expanded = new Set(saved.expanded || []);
    this.selectedTreeKey = saved.selectedTreeKey || null;
    for (const persisted of saved.tabs || []) {
      if (this.abort.signal.aborted) return;
      await this.restoreTab(persisted);
    }
    if (this.abort.signal.aborted) return;
    // restoreTab temporarily attaches models so Monaco can materialize their
    // view state. Clear that transient active marker before choosing the
    // persisted active editor, otherwise a later activation could save one
    // model's view state onto a different tab.
    this.activeTabId = null;
    const untitledNumbers = (saved.tabs || [])
      .map((tab) => /^Untitled-(\d+)$/.exec(tab.title))
      .filter(Boolean)
      .map((match) => Number(match![1]));
    this.untitledCounter = Math.max(1, ...untitledNumbers.map((value) => value + 1));
    const active = this.tabs.find((tab) => tab.id === saved!.activeTabId) || this.tabs[0];
    if (active) {
      this.activateTab(active.id);
      const persisted = saved.tabs.find((tab) => tab.id === active.id);
      if (active.kind !== "media") {
        const restoredEditor = active.kind === "diff" ? this.diffEditor.getModifiedEditor() : this.editor;
        if (persisted?.cursor) restoredEditor.setPosition(persisted.cursor);
        if (typeof persisted?.scrollTop === "number") restoredEditor.setScrollTop(persisted.scrollTop);
      }
    }
    this.restoredTreeScrollTop = saved.treeScrollTop || 0;
  }

  private async restoreTab(persisted: PersistedTab): Promise<void> {
    if (!this.workspace) return;
    try {
      if (persisted.kind === "media") {
        const ref = persisted.ref;
        if (!ref) return;
        const existing = this.tabs.find((candidate) => candidate.ref && refKey(candidate.ref) === refKey(ref));
        if (existing) {
          existing.pinned = persisted.pinned;
          return;
        }
        await this.openMedia(ref, true, false);
        const opened = this.tabs.find((candidate) => candidate.ref && refKey(candidate.ref) === refKey(ref));
        if (opened) opened.pinned = persisted.pinned;
        return;
      }
      if (persisted.kind === "diff" && persisted.diff) {
        await this.openGitDiff(
          persisted.diff.repository,
          { path: persisted.diff.path, oldPath: persisted.diff.oldPath, ref: persisted.diff.fileRef },
          persisted.diff.scope,
          persisted.diff.reviewRef,
          true,
        );
        if (this.abort.signal.aborted) return;
        const tab = this.tabs.find((candidate) => candidate.id === persisted.id);
        if (!tab) return;
        tab.pinned = persisted.pinned;
        if (persisted.dirty && persisted.content !== undefined && tab.diff?.editable) {
          tab.applying = true;
          tab.model.setValue(persisted.content);
          tab.applying = false;
          this.markModelDirty(tab.model);
        }
        tab.deleted = persisted.deleted;
        tab.revision = persisted.revision;
        this.captureRestoredViewState(tab, persisted);
        return;
      }
      if (!persisted.ref) {
        const tab = this.newUntitledFrom(persisted.content || "", persisted.title, persisted.id);
        tab.dirty = persisted.dirty;
        this.captureRestoredViewState(tab, persisted);
        return;
      }
      let disk: FileSnapshot | null = null;
      try { disk = await editorAPI.readFile(this.workspace.id, persisted.ref); } catch { /* preserve dirty orphan below */ }
      if (this.abort.signal.aborted) return;
      if (!disk && !persisted.dirty) return;
      const snapshot: FileSnapshot = disk || {
        ref: persisted.ref, hostPath: persisted.hostPath, content: persisted.content || "", revision: persisted.revision,
        size: (persisted.content || "").length, modifiedAt: "", encoding: "utf-8", eol: persisted.eol, hasBom: persisted.hasBom,
      };
      if (persisted.dirty && persisted.content !== undefined) {
        snapshot.content = persisted.content;
        snapshot.eol = persisted.eol;
        snapshot.hasBom = persisted.hasBom;
      }
      const tab = this.createModel(snapshot, persisted.id);
      tab.pinned = persisted.pinned;
      tab.dirty = persisted.dirty;
      tab.deleted = !disk || persisted.deleted;
      tab.conflict = Boolean(disk && persisted.dirty && disk.revision !== persisted.revision);
      tab.revision = persisted.dirty ? persisted.revision : disk?.revision || persisted.revision;
      this.tabs.push(tab);
      this.captureRestoredViewState(tab, persisted);
    } catch (error) {
      if (!this.abort.signal.aborted) console.warn(`restore tab ${persisted.title}`, error);
    }
  }

  private captureRestoredViewState(tab: OpenTab, persisted: PersistedTab): void {
    if (tab.kind === "diff" && tab.diff) {
      this.diffEditor.setModel({ original: tab.diff.originalModel, modified: tab.model });
      if (persisted.cursor) this.diffEditor.getModifiedEditor().setPosition(persisted.cursor);
      if (typeof persisted.scrollTop === "number") this.diffEditor.getModifiedEditor().setScrollTop(persisted.scrollTop);
      tab.diff.viewState = this.diffEditor.saveViewState();
      return;
    }
    this.editor.setModel(tab.model);
    if (persisted.cursor) this.editor.setPosition(persisted.cursor);
    if (typeof persisted.scrollTop === "number") this.editor.setScrollTop(persisted.scrollTop);
    tab.viewState = this.editor.saveViewState();
  }

  private async restoreTreeExpansion(): Promise<void> {
    const refs = [...this.expanded].map((key) => {
      const separator = key.indexOf(":");
      return separator >= 0 ? { rootId: key.slice(0, separator), path: key.slice(separator + 1) } : null;
    }).filter(Boolean) as FileRef[];
    for (const ref of refs.sort((left, right) => left.path.split("/").length - right.path.split("/").length)) {
      if (ref.path) await this.expandTo(ref, false);
    }
  }

  private schedulePersist(): void {
    window.clearTimeout(this.persistTimer);
    this.persistTimer = window.setTimeout(() => void this.persistNow(), 750);
  }

  private async persistNow(): Promise<boolean> {
    if (!this.workspace) return true;
    window.clearTimeout(this.persistTimer);
    const active = this.activeTab();
    if (active?.kind === "diff" && active.diff) active.diff.viewState = this.diffEditor.saveViewState();
    else if (active) active.viewState = this.editor.saveViewState();
    const tabs: PersistedTab[] = this.tabs.map((tab) => {
      const diffState = tab.diff?.viewState?.modified;
      const position = tab.id === this.activeTabId
        ? (tab.kind === "diff" ? this.diffEditor.getModifiedEditor().getPosition() : this.editor.getPosition()) || undefined
        : tab.kind === "diff" ? diffState?.cursorState[0]?.position : tab.viewState?.cursorState[0]?.position;
      return {
        kind: tab.kind, id: tab.id, ref: tab.ref, title: tab.title, hostPath: tab.hostPath, pinned: tab.pinned,
        preview: !tab.pinned, dirty: tab.dirty, deleted: tab.deleted, revision: tab.revision,
        hasBom: tab.hasBom, eol: tab.eol,
        ...(tab.dirty || (tab.kind === "file" && !tab.ref) ? { content: tab.model.getValue() } : {}),
        cursor: position ? { lineNumber: position.lineNumber, column: position.column } : undefined,
        scrollTop: tab.id === this.activeTabId
          ? (tab.kind === "diff" ? this.diffEditor.getModifiedEditor().getScrollTop() : this.editor.getScrollTop())
          : tab.kind === "diff" ? diffState?.viewState.scrollTop : tab.viewState?.viewState.scrollTop,
        diff: tab.diff ? {
          repository: tab.diff.repository, scope: tab.diff.scope, reviewRef: tab.diff.reviewRef,
          fileRef: tab.diff.fileRef, oldPath: tab.diff.oldPath, path: tab.hostPath,
          editable: tab.diff.editable,
        } : undefined,
      };
    });
    try {
      await saveSession(this.workspace.id, {
        version: 3, activeTabId: this.activeTabId, tabs, expanded: [...this.expanded],
        selectedTreeKey: this.selectedTreeKey,
        explorerWidth: this.explorerWidth, codeChatWidth: this.codeChatWidth,
        treeScrollTop: this.treeScroller?.scrollTop || 0,
      });
      this.persistenceFailed = false;
      return true;
    } catch (error) {
      if (!this.persistenceFailed) toast(`Unsaved-buffer recovery is unavailable: ${error instanceof Error ? error.message : String(error)}`, { sticky: true });
      this.persistenceFailed = true;
      return false;
    }
  }

  private subscribeFilesystem(): void {
    if (!this.workspace) return;
    const workspaceId = this.workspace.id;
    let hasOpened = false;
    const unsubscribeState = onSocketState((state: string) => {
      if (state !== "open") return;
      this.sendFilesystemSubscription();
      if (hasOpened) {
        this.lastSequence = 0;
        void this.refreshExplorer();
        void this.pollOpenTabs();
      }
      hasOpened = true;
    });
    const unsubscribeChanges = onSocket("workspace_fs_changed", (data: object) => {
      const event = data as {
        workspaceId: string;
        sequence: number;
        changes: Array<{ op: string; ref: FileRef }>;
      };
      if (event.workspaceId !== workspaceId) return;
      if (this.lastSequence && event.sequence !== this.lastSequence + 1) {
        this.enablePollingFallback();
        void this.refreshExplorer();
      }
      this.lastSequence = event.sequence;
      void this.applyFilesystemChanges(event.changes || []);
    });
    const unsubscribeResync = onSocket("fs_resync_required", (data: object) => {
      const event = data as { workspaceId: string; sequence: number };
      if (event.workspaceId !== workspaceId) return;
      this.lastSequence = event.sequence;
      this.enablePollingFallback();
      void this.refreshExplorer();
      void this.pollOpenTabs();
    });
    this.abort.signal.addEventListener("abort", () => {
      unsubscribeState();
      unsubscribeChanges();
      unsubscribeResync();
      sendSocket({ type: "fs_unsubscribe", workspaceId });
    }, { once: true });
    this.sendFilesystemSubscription();
  }

  private sendFilesystemSubscription(): void {
    if (!this.workspace) return;
    const refs = new Map<string, FileRef>();
    for (const key of this.expanded) {
      const ref = this.nodes.get(key)?.ref;
      if (ref) refs.set(refKey(ref), ref);
    }
    for (const tab of this.tabs) {
      const ref = this.worktreeRef(tab);
      if (ref) refs.set(refKey(ref), ref);
    }
    for (const [uri] of this.navigationModels.entries()) {
      const ref = this.refForFileURI(uri);
      if (ref) refs.set(refKey(ref), ref);
    }
    sendSocket({ type: "fs_subscribe", workspaceId: this.workspace.id, refs: [...refs.values()] });
  }

  private async applyFilesystemChanges(changes: Array<{ op: string; ref: FileRef }>): Promise<void> {
    const parents = new Map<string, FileRef>();
    for (const change of changes) {
      this.navigationModels.invalidate((uri) => {
        const ref = this.refForFileURI(uri);
        return Boolean(ref && isRefWithin(ref, change.ref));
      });
      const slash = change.ref.path.lastIndexOf("/");
      const parent = { rootId: change.ref.rootId, path: slash >= 0 ? change.ref.path.slice(0, slash) : "" };
      parents.set(refKey(parent), parent);
      for (const tab of this.tabs) {
        const tabRef = this.worktreeRef(tab);
        if (!tabRef || !isRefWithin(tabRef, change.ref)) continue;
        if (change.op === "delete" || change.op === "rename") {
          tab.deleted = true;
          if (tab.dirty) tab.conflict = true;
        } else if (change.op === "write" && refKey(tabRef) === refKey(change.ref)) {
          if (tab.dirty) tab.conflict = true;
          else await this.reloadCleanTab(tab);
        }
      }
    }
    for (const parent of parents.values()) {
      const node = this.nodes.get(refKey(parent));
      if (node?.loaded) await this.reloadChildrenPreservingExpansion(node);
    }
    this.renderTabs();
    this.schedulePersist();
    this.searchView?.refresh();
    this.sendFilesystemSubscription();
  }

  private async reloadCleanTab(tab: OpenTab): Promise<void> {
    const ref = this.worktreeRef(tab);
    if (!this.workspace || !ref || tab.dirty) return;
    if (tab.kind === "media") return;
    try {
      const snapshot = await editorAPI.readFile(this.workspace.id, ref);
      tab.deleted = false;
      if (snapshot.revision !== tab.revision) {
        const activeEditor = tab.kind === "diff" ? this.diffEditor.getModifiedEditor() : this.editor;
        const viewState = tab.id === this.activeTabId
          ? activeEditor.saveViewState()
          : tab.kind === "diff" ? tab.diff?.viewState?.modified : tab.viewState;
        this.applyDiskSnapshot(tab, snapshot);
        if (tab.id === this.activeTabId && viewState) activeEditor.restoreViewState(viewState);
      } else {
        tab.conflict = false;
      }
    } catch (error) {
      const apiError = error as APIError;
      if (apiError.status === 404) tab.deleted = true;
      else tab.conflict = true;
    }
  }

  private enablePollingFallback(): void {
    if (this.pollTimer) return;
    toast("Live file watching needs periodic resynchronization for this workspace.");
    this.pollTimer = window.setInterval(() => void this.pollOpenTabs(), 2000);
  }

  private async pollOpenTabs(): Promise<void> {
    for (const tab of this.tabs) {
      const ref = this.worktreeRef(tab);
      if (!ref) continue;
      if (tab.dirty && this.workspace) {
        try {
          const disk = await editorAPI.readFile(this.workspace.id, ref);
          tab.deleted = false;
          tab.conflict = disk.revision !== tab.revision;
        } catch (error) {
          const apiError = error as APIError;
          if (apiError.status === 404) tab.deleted = true;
          tab.conflict = true;
        }
      } else {
        await this.reloadCleanTab(tab);
      }
    }
    await this.refreshExplorer();
    this.renderTabs();
  }

  dispose(): void {
    if (this.abort.signal.aborted) return;
    this.finishMruCycle();
    detachTerminalDock(this.root.querySelector<HTMLElement>("[data-region=terminal]"));
    void this.persistNow();
    this.codeNavigation?.dispose(this.captureNavigationLocation());
    this.codeNavigation = null;
    this.closeWorkspaceDropdown?.();
    this.closeWorkspaceDropdown = null;
    this.closeAddWorkspaceModal?.();
    this.closeAddWorkspaceModal = null;
    this.codeChatSurface?.dispose();
    this.codeChatSurface = null;
    this.abort.abort();
    window.clearTimeout(this.persistTimer);
    window.clearTimeout(this.treeDropExpandTimer);
    if (this.treeDragScrollFrame) cancelAnimationFrame(this.treeDragScrollFrame);
    window.clearInterval(this.pollTimer);
    closeContextMenu();
    this.editorOpener?.dispose();
    this.editorOpener = null;
    this.lsp?.dispose();
    this.lsp = null;
    this.disposeVirtualizer?.();
    for (const tab of this.tabs) this.disposeTab(tab);
    this.tabs = [];
    this.navigationModels.dispose();
    this.editor?.dispose();
    this.diffEditor?.dispose();
  }
}

function shortGitRef(ref: string): string {
  return /^[0-9a-f]{10,}$/i.test(ref) ? ref.slice(0, 9) : ref;
}
