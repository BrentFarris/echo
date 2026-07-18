import { services } from "../../wailsjs/go/models";

export type ToastTone = "info" | "success" | "error";
export type CodeEntryKind = "file" | "directory" | "other";
export type CodeCreateKind = "file" | "folder";
export type CodeGitChangeState = "" | "created" | "modified";

export type CodeTabContextMenu = {
  path: string;
  label: string;
  untitled: boolean;
  external: boolean;
  canCloseOthers: boolean;
  canCloseToRight: boolean;
  canCloseSaved: boolean;
};

export type CodeViewCallbacks = {
  render: () => void;
  activateCodeView: (workspaceID: string) => void;
  pushToast: (message: string, tone?: ToastTone) => void;
  errorMessage: (error: unknown) => string;
  leadingWhitespaceIndicatorsEnabled: () => boolean;
  gitChangedLineNumbers: (workspaceID: string, path: string) => number[];
  gitChangeStateForPath: (workspaceID: string, path: string, kind: CodeEntryKind) => CodeGitChangeState;
  refreshGitChanges: (workspaceID: string) => Promise<void>;
  saveDirtyWorkspaceFiles: (workspaceID: string) => Promise<boolean>;
  openWorkspaceFileAtLine: (
    workspaceID: string,
    path: string,
    line: number,
  ) => Promise<unknown>;
  openDebugSettings: (workspaceID: string) => void;
  showCodePathContextMenu: (
    workspaceID: string,
    path: string,
    kind: CodeEntryKind,
    label: string,
    x: number,
    y: number,
  ) => void;
  showCodeTabContextMenu: (
    workspaceID: string,
    menu: CodeTabContextMenu,
    x: number,
    y: number,
  ) => void;
};

export type DirectoryState = {
  entries: services.WorkspaceFileEntry[];
  loaded: boolean;
  loading: boolean;
  error: string;
};

export type CodeFileTab = {
  path: string;
  content: string;
  savedContent: string;
  lineSeparator: string;
  bytes: number;
  modifiedAt: string;
  dirty: boolean;
  saving: boolean;
  temporary: boolean;
  untitled: boolean;
  external: boolean;
  selectionAnchor: number;
  selectionHead: number;
  scrollTop: number;
  scrollLeft: number;
  pendingRevealPosition?: number;
  pendingRevealScroll?: "center";
  isMedia?: boolean;
  mediaMimeType?: string;
  mediaDataUrl?: string;
  mediaLoading?: boolean;
  mediaError?: string;
  zoomLevel?: number;
};

export type CodeNavigationLocation = {
  path: string;
  selectionAnchor: number;
  selectionHead: number;
  scrollTop: number;
  scrollLeft: number;
};

export type CodeNavigationHistoryState = {
  entries: CodeNavigationLocation[];
  currentIndex: number;
  maxSize: number;
};

export type CodeTabSwitcherState = {
  paths: string[];
  selectedIndex: number;
  sourceLocation?: CodeNavigationLocation;
};

export type PendingCodeCreate = {
  kind: CodeCreateKind;
  parentPath: string;
  name: string;
  submitting: boolean;
  error: string;
};

export type PendingCodeRename = {
  path: string;
  kind: CodeEntryKind;
  name: string;
  originalName: string;
  submitting: boolean;
  error: string;
};

export type CodeDragState = {
  sourcePath: string;
  sourceKind: CodeEntryKind;
  targetPath: string;
  targetParentPath: string;
  moving: boolean;
};

export type InlineCodeChatState = {
  path: string;
  anchorPosition: number;
  selectedText: string;
  draft: string;
  mention: InlineCodeMentionState | null;
  submitting: boolean;
  status: string;
  response: string;
  reasoning: string;
  toolCalls: services.ChatToolActivity[];
  error: string;
  requestID: string;
  renderKey: number;
};

export type InlineCodeMentionState = {
  triggerStart: number;
  query: string;
  results: services.WorkspaceFileEntry[];
  loading: boolean;
  error: string;
  selectedIndex: number;
  requestSeq: number;
  timerID: number | null;
};

export type InlineCodePromptEvent = {
  workspaceId: string;
  requestId?: string;
  filePath: string;
  type: string;
  content?: string;
  reasoning?: string;
  toolCall?: services.ChatToolActivity;
  affectedPaths?: string[];
  error?: string;
  finishReason?: string;
};

export type CodeReferencePanelState = {
  path: string;
  title: string;
  anchorPosition: number;
  locations: services.WorkspaceReferenceLocation[];
  selectedIndex: number;
  expandedPaths: string[];
  renderKey: number;
};

export type CodeQuickOpenState = {
  open: boolean;
  query: string;
  results: services.WorkspaceFileEntry[];
  loading: boolean;
  truncated: boolean;
  selectedIndex: number;
  requestSeq: number;
  timerID: number | null;
};

export type CodeWorkspaceState = {
  directories: Map<string, DirectoryState>;
  expandedPaths: Set<string>;
  tabs: CodeFileTab[];
  activePath: string;
  selectedPath: string;
  selectedKind: CodeEntryKind;
  selectedEntries: Map<string, CodeEntryKind>;
  selectionAnchorPath: string;
  selectionAnchorKind: CodeEntryKind;
  tabMruPaths: string[];
  navigationHistory: CodeNavigationHistoryState;
  tabSwitcher: CodeTabSwitcherState | null;
  pendingCreate: PendingCodeCreate | null;
  pendingRename: PendingCodeRename | null;
  drag: CodeDragState | null;
  showIgnored: boolean;
  openingPath: string;
  explorerWidth: number;
  explorerDrawerOpen: boolean;
  codeTreeScrollTop: number;
  searchQuery: string;
  searchResults: services.WorkspaceFileEntry[];
  searchLoading: boolean;
  searchTruncated: boolean;
  searchRequestSeq: number;
  searchTimerID: number | null;
  searchFocused: boolean;
  preservingSearchFocus: boolean;
  untitledSeq: number;
  temporaryFilesExpanded: boolean;
  textSearchOpen: boolean;
  textSearchQuery: string;
  textSearchInclude: string;
  textSearchExclude: string;
  textSearchRegex: boolean;
  textSearchCaseSensitive: boolean;
  textSearchWholeWord: boolean;
  textSearchResult: services.WorkspaceTextSearchResult | null;
  textSearchLoading: boolean;
  textSearchError: string;
  textSearchRequestSeq: number;
  textSearchStreamID: string;
  textSearchTimerID: number | null;
  textSearchFocusedField: "" | "query" | "include" | "exclude";
  textSearchSelectQuery: boolean;
  preservingTextSearchFocus: boolean;
  inlineChat: InlineCodeChatState | null;
  referencesPanel: CodeReferencePanelState | null;
  quickOpen: CodeQuickOpenState;
};
