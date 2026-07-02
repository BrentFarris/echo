
import { services } from "../../wailsjs/go/models";
import type { CodeEntryKind, CodeViewCallbacks } from "../codeView/types";

export type AppMode = "chat" | "kanban" | "code";

export type ContextMenuState = {
  workspaceId: string;
  displayPath: string;
  workspacePath?: string;
  codePath?: string;
  codeKind?: CodeEntryKind;
  x: number;
  y: number;
};

export type ChatStreamEvent = {
  workspaceId: string;
  streamId: string;
  messageId: string;
  type: string;
  content?: string;
  reasoning?: string;
  toolCall?: services.ChatToolActivity;
  error?: string;
  finishReason?: string;
};

export type KanbanEvent = {
  workspaceId: string;
  cardId?: string;
  type: string;
  board: services.KanbanBoard;
};

export type FileChangesEvent = {
  workspaceId: string;
  type: string;
  fileCount: number;
  changeCount: number;
};

export type Toast = {
  id: string;
  tone: "info" | "success" | "error";
  message: string;
};

export type ChatMentionState = {
  workspaceId: string;
  triggerStart: number;
  query: string;
  results: services.WorkspaceFileEntry[];
  loading: boolean;
  error: string;
  selectedIndex: number;
  requestSeq: number;
  timerID: number | null;
};

export type ChatImageDraft = {
  id: string;
  name: string;
  mediaType: string;
  dataUrl: string;
  bytes: number;
};

export type ChatVideoDraft = {
  id: string;
  name: string;
  mediaType: string;
  dataUrl: string;
  bytes: number;
};

export type KanbanCardCreationDraft = {
  title: string;
  description: string;
  acceptanceCriteria: string;
};

export type ScrollSnapshot = {
  scrollTop: number;
  atBottom: boolean;
};

export type AppCallbacks = {
  render: () => void;
  pushToast: (message: string, tone?: Toast["tone"]) => void;
  errorMessage: (error: unknown) => string;
  codeViewCallbacks: () => CodeViewCallbacks;
  bindActionEvents: (root: ParentNode) => void;
  bindChatEvents: (root: ParentNode) => void;
};
