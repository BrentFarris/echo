import { services } from "../../wailsjs/go/models";

export type ToastTone = "info" | "success" | "error";

export type CodeViewCallbacks = {
  render: () => void;
  pushToast: (message: string, tone?: ToastTone) => void;
  errorMessage: (error: unknown) => string;
  leadingWhitespaceIndicatorsEnabled: () => boolean;
  showCodePathContextMenu: (
    workspaceID: string,
    path: string,
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
  selectionAnchor: number;
  selectionHead: number;
  scrollTop: number;
  scrollLeft: number;
  pendingRevealPosition?: number;
};

export type CodeTabSwitcherState = {
  paths: string[];
  selectedIndex: number;
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

export type CodeWorkspaceState = {
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
