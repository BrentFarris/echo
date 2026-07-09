
import { services } from "../../wailsjs/go/models";
import type { CodeEntryKind, CodeViewCallbacks } from "../codeView/types";

export type AppMode = "chat" | "tasks" | "kanban" | "code" | "settings" | "git";

export type MobileNavView = AppMode | "git";

export type ContextMenuState = {
  workspaceId: string;
  displayPath: string;
  workspacePath?: string;
  codePath?: string;
  codeKind?: CodeEntryKind;
  editorPath?: string;
  editorPosition?: number | null;
  spellCheckWord?: string;
  spellCheckSuggestions?: string[];
  spellCheckFrom?: number;
  spellCheckTo?: number;
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

export type TaskEvent = {
  workspaceId: string;
  taskId?: string;
  type: string;
  board: services.TaskBoard;
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
  sourceTaskId?: string;
  sourceTaskUpdatedAt?: string;
};

export type TaskEditorDraft = {
  taskId?: string;
  title: string;
  details: string;
  acceptanceCriteria: string;
  tags: string;
  priority: string;
  expectedUpdatedAt?: string;
};

export type TaskInlineEditField = "title" | "tags" | "details" | "acceptanceCriteria" | "priority";

export type TaskInlineEditState = {
  taskId: string;
  field: TaskInlineEditField;
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
