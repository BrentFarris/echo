import { services } from "../../wailsjs/go/models";

export type ToastTone = "info" | "success" | "error";
export type CodeEntryKind = "file" | "directory" | "other";
export type CodeCreateKind = "file" | "folder";

export type CodeViewCallbacks = {
  render: () => void;
  pushToast: (message: string, tone?: ToastTone) => void;
  errorMessage: (error: unknown) => string;
  leadingWhitespaceIndicatorsEnabled: () => boolean;
  showCodePathContextMenu: (
    workspaceID: string,
    path: string,
    kind: CodeEntryKind,
    label: string,
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
  selectionAnchor: number;
  selectionHead: number;
  scrollTop: number;
  scrollLeft: number;
  pendingRevealPosition?: number;
  pendingRevealScroll?: "center";
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
  submitting: boolean;
  response: string;
  error: string;
  requestID: string;
  renderKey: number;
};

export type InlineCodePromptEvent = {
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
  textSearchTimerID: number | null;
  textSearchFocusedField: "" | "query" | "include" | "exclude";
  preservingTextSearchFocus: boolean;
  inlineChat: InlineCodeChatState | null;
  referencesPanel: CodeReferencePanelState | null;
  quickOpen: CodeQuickOpenState;
};
