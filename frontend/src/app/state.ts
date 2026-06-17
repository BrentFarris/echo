
import { llm, services } from "../../wailsjs/go/models";
import type { ThemePaletteName } from "./theme";
import type { AppMode, ChatImageDraft, ChatMentionState, ContextMenuState, Toast } from "./types";

export const state = {
  appState: null as services.AppState | null,
  settingsDraft: null as llm.Settings | null,
  settingsOpen: false,
  settingsThemePalette: "light" as ThemePaletteName,
  workspaceLetterDrafts: new Map<string, string>(),
  appMode: "chat-kanban" as AppMode,
  formError: "",
  chatSessions: new Map<string, services.ChatSession>(),
  chatDrafts: new Map<string, string>(),
  chatImageDrafts: new Map<string, ChatImageDraft[]>(),
  chatPlanModes: new Map<string, boolean>(),
  chatFileLinkCache: new Map<string, Promise<string | null>>(),
  chatMention: null as ChatMentionState | null,
  kanbanBoards: new Map<string, services.KanbanBoard>(),
  changeReviews: new Map<string, services.WorkspaceChangeReview>(),
  gitChangeReviews: new Map<string, services.WorkspaceGitChangeReview>(),
  executingPlans: new Set<string>(),
  runningKanbanWorkspaces: new Set<string>(),
  kanbanRunStarts: new Map<string, number>(),
  kanbanRunElapsed: new Map<string, number>(),
  selectedKanbanCards: new Map<string, string>(),
  openChangeReviewWorkspaces: new Set<string>(),
  openGitChangeWorkspaces: new Set<string>(),
  cardMessageDrafts: new Map<string, string>(),
  expandedChatWorkspaces: new Set<string>(),
  expandedKanbanWorkspaces: new Set<string>(),
  editingMessageIds: new Set<string>(),
  expandedChangeReviewWorkspaces: new Set<string>(),
  expandedGitChangeWorkspaces: new Set<string>(),
  loadingGitChangeWorkspaces: new Set<string>(),
  toastSeq: 0,
  toasts: [] as Toast[],
  kanbanTimerID: null as number | null,
  contextMenu: null as ContextMenuState | null,
};

export const kanbanLaneLabels: Record<string, string> = {
  ready: "Ready",
  inProgress: "In Progress",
  blocked: "Blocked",
  done: "Done",
};

export function cloneSettings(settings: llm.Settings): llm.Settings {
  return llm.Settings.createFrom(JSON.parse(JSON.stringify(settings)));
}

export function activeWorkspace(): services.Workspace | null {
  if (!state.appState) {
    return null;
  }
  const workspaces = state.appState.workspaces ?? [];
  return (
    workspaces.find(
      (workspace) => workspace.id === state.appState?.activeWorkspaceId,
    ) ?? null
  );
}

export function chatImageDraftsFor(workspaceID: string): ChatImageDraft[] {
  return state.chatImageDrafts.get(workspaceID) ?? [];
}

export function chatImageDraftTotalBytes(workspaceID: string): number {
  return chatImageDraftsFor(workspaceID).reduce((total, image) => total + image.bytes, 0);
}

export function chatSessionFor(workspaceID: string): services.ChatSession {
  return (
    state.chatSessions.get(workspaceID) ??
    services.ChatSession.createFrom({
      workspaceId: workspaceID,
      messages: [],
      busy: false,
    })
  );
}

export function chatPlanModeFor(workspaceID: string): boolean {
  return state.chatPlanModes.get(workspaceID) ?? true;
}

export function kanbanBoardFor(workspaceID: string): services.KanbanBoard {
  return (
    state.kanbanBoards.get(workspaceID) ??
    services.KanbanBoard.createFrom({
      workspaceId: workspaceID,
      ready: [],
      inProgress: [],
      blocked: [],
      done: [],
    })
  );
}

export function changeReviewFor(workspaceID: string): services.WorkspaceChangeReview {
  return (
    state.changeReviews.get(workspaceID) ??
    services.WorkspaceChangeReview.createFrom({
      workspaceId: workspaceID,
      fileCount: 0,
      changeCount: 0,
      files: [],
      changes: [],
    })
  );
}

export function gitChangeReviewFor(workspaceID: string): services.WorkspaceGitChangeReview {
  return (
    state.gitChangeReviews.get(workspaceID) ??
    services.WorkspaceGitChangeReview.createFrom({
      workspaceId: workspaceID,
      fileCount: 0,
      files: [],
    })
  );
}

export function kanbanCards(board: services.KanbanBoard): services.KanbanCard[] {
  return [
    ...(board.ready ?? []),
    ...(board.inProgress ?? []),
    ...(board.blocked ?? []),
    ...(board.done ?? []),
  ];
}

export function selectedKanbanCard(board: services.KanbanBoard): services.KanbanCard | null {
  const selectedID = state.selectedKanbanCards.get(board.workspaceId);
  return kanbanCards(board).find((card) => card.id === selectedID) ?? null;
}

export function fieldValue<K extends keyof llm.Settings>(key: K): string {
  const value = state.settingsDraft?.[key];
  return value === undefined || value === null ? "" : String(value);
}

export function leadingWhitespaceIndicatorsEnabled(settings: llm.Settings | null | undefined): boolean {
  return (settings as { hideLeadingWhitespaceIndicators?: boolean } | null | undefined)
    ?.hideLeadingWhitespaceIndicators !== true;
}

export function notificationSoundsEnabled(settings: llm.Settings | null | undefined): boolean {
  return (settings as { disableNotificationSounds?: boolean } | null | undefined)
    ?.disableNotificationSounds !== true;
}

export function enableThinkingEnabled(settings: llm.Settings | null | undefined): boolean {
  return (settings as { enableThinking?: boolean } | null | undefined)
    ?.enableThinking !== false;
}

export function thinkingCorrectionEnabled(settings: llm.Settings | null | undefined): boolean {
  return (settings as { thinkingCorrection?: boolean } | null | undefined)
    ?.thinkingCorrection === true;
}
