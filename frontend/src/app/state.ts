
import { llm, services } from "../../wailsjs/go/models";
import type { ThemePaletteName } from "./theme";
import type { AppMode, ChatImageDraft, ChatMentionState, ChatVideoDraft, ContextMenuState, DashboardWidget, GitDiffViewMode, GitMenuPage, KanbanCardCreationDraft, MobileNavView, TaskEditorDraft, TaskInlineEditState, Toast } from "./types";

const endpointTopics = ["chat", "kanbanDecompose", "kanban", "inlineCode"] as const;
type EndpointTopicKey = (typeof endpointTopics)[number];

export type ChatKanbanTab = "chat" | "tasks" | "kanban";

export const state = {
  appState: null as services.AppState | null,
  settingsDraft: null as llm.Settings | null,
  webAccessDraft: null as services.WebAccessSettings | null,
  webAccessStatus: null as services.WebAccessStatus | null,
  webAccessQRCodeURL: "",
  settingsOpen: false,
  settingsEndpointEditId: "",
  agentModeEditingId: "",
  agentModeCreating: false,
  agentModeDraftName: "",
  agentModeDraftPrompt: "",
  agentModeDraftToolPermissions: [] as string[], // deprecated: kept for backward compat
  agentModeDraftPathPermissions: [] as string[], // deprecated: kept for backward compat
  agentModeDraftPermissions: {} as Record<string, string[]>, // tool name -> glob paths[]
  settingsThemePalette: "light" as ThemePaletteName,
  workspaceLetterDrafts: new Map<string, string>(),
  workspaceBuildCommandDrafts: new Map<string, string>(),
  appMode: "chat" as AppMode,
  dashboardPreviousMode: null as AppMode | null, // remembers mode before dashboard
  dashboardLayouts: defaultDashboardLayouts(), // per-view widget layouts (initialized with defaults)
  dashboardEditMode: false, // whether user is currently rearranging widgets
  mobileNavView: "chat" as MobileNavView,
  activeChatKanbanTab: new Map<string, ChatKanbanTab>(),
  formError: "",
  workspaceDropdownOpen: false,
  chatSessions: new Map<string, services.ChatSession>(),
  chatDrafts: new Map<string, string>(),
  chatImageDrafts: new Map<string, ChatImageDraft[]>(),
  chatVideoDrafts: new Map<string, ChatVideoDraft[]>(),
  chatComposerModes: new Map<string, "plan" | "edit">(),
  chatPlanModes: new Map<string, boolean>(),
  chatFileLinkCache: new Map<string, Promise<string | null>>(),
  chatMention: null as ChatMentionState | null,
  kanbanBoards: new Map<string, services.KanbanBoard>(),
  taskBoards: new Map<string, services.TaskBoard>(),
  taskEditorDrafts: new Map<string, TaskEditorDraft>(),
  selectedTaskIds: new Map<string, string>(),
  taskInlineEdits: new Map<string, TaskInlineEditState>(),
  showCompletedTaskWorkspaces: new Set<string>(),
  taskSearchQuery: new Map<string, string>(),
  taskFilterMode: new Map<string, "all" | "open" | "completed">(),
  taskEpicFilter: new Map<string, string>(),
  taskTagFilters: new Map<string, Set<string>>(),
  changeReviews: new Map<string, services.WorkspaceChangeReview>(),
  gitChangeReviews: new Map<string, services.WorkspaceGitChangeReview>(),
  gitRepositoryViews: new Map<string, services.WorkspaceGitRepositoryView>(),
  gitWorkingDiffs: new Map<string, services.WorkspaceGitChangedFile>(),
  gitWorkingDiffFailures: new Set<string>(),
  loadingGitWorkingDiffs: new Set<string>(),
  gitWorkingDiffGenerations: new Map<string, number>(),
  gitCommitDetails: new Map<string, services.WorkspaceGitCommitDetail>(),
  selectedGitRepositoryFolders: new Map<string, string>(),
  selectedGitCommitHashes: new Map<string, string>(),
  collapsedGitChangeFolders: new Map<string, Set<string>>(),
  collapsedGitChangeTrees: new Set<string>(),
  gitCommitMessageDrafts: new Map<string, string>(),
  gitNewBranchDrafts: new Map<string, string>(),
  gitSwitchBranchDrafts: new Map<string, string>(),
  gitMergeBranchDrafts: new Map<string, string>(),
  gitMenuPages: new Map<string, GitMenuPage>(),
  gitStashDetails: new Map<string, services.WorkspaceGitStashDetail>(),
  gitRepositoryOperations: new Map<string, string>(),
  gitDiffViewMode: "inline" as GitDiffViewMode,
  loadingGitRepositoryWorkspaces: new Set<string>(),
  loadingGitCommitDetails: new Set<string>(),
  executingPlans: new Set<string>(),
  creatingChatSkills: new Set<string>(),
  runningKanbanWorkspaces: new Set<string>(),
  kanbanRunStarts: new Map<string, number>(),
  kanbanRunElapsed: new Map<string, number>(),
  selectedKanbanCards: new Map<string, string>(),
  selectedTaskCards: new Map<string, string>(),
  openChangeReviewWorkspaces: new Set<string>(),
  openGitChangeWorkspaces: new Set<string>(),
  cardMessageDrafts: new Map<string, string>(),
  creatingKanbanCardWorkspaces: new Set<string>(),
  kanbanCardCreationDrafts: new Map<string, KanbanCardCreationDraft>(),
  expandedChatWorkspaces: new Set<string>(),
  expandedKanbanWorkspaces: new Set<string>(),
  editingMessageIds: new Set<string>(),
  expandedChangeReviewWorkspaces: new Set<string>(),
  expandedGitChangeWorkspaces: new Set<string>(),
  loadingGitChangeWorkspaces: new Set<string>(),
  toastSeq: 0,
  toasts: [] as Toast[],
  kanbanTimerID: null as number | null,
  kanbanRenderDebounceTimers: new Map<string, number>(), // workspaceID -> setTimeout handle for debounced card_progress patches
  contextMenu: null as ContextMenuState | null,
  agentModes: new Map<string, services.AgentMode[]>(),
  selectedAgentModeIds: new Map<string, string>(),
  creatingAgentModes: new Set<string>(),
  heartbeatIntervals: new Map<string, number>(), // workspaceID -> interval in milliseconds
  watchdogIntervals: new Map<string, number>(), // workspaceID -> interval in milliseconds
};

export type AgentModeDraft = {
  name: string;
  prompt: string;
  toolPermissionsText: string;
  pathPermissionsText: string;
};

export function getActiveChatKanbanTab(workspaceID: string): ChatKanbanTab {
  return state.activeChatKanbanTab.get(workspaceID) ?? "chat";
}

export function chatComposerModeFor(workspaceID: string): "plan" | "edit" {
  const mode = state.chatComposerModes.get(workspaceID);
  if (mode !== undefined) {
    return mode;
  }
  /* Derive chatPlanModes from the composer mode map. */
  state.chatPlanModes.delete(workspaceID);
  return "plan";
}

export function setChatComposerMode(workspaceID: string, mode: "plan" | "edit") {
  if (mode === "plan") {
    state.chatComposerModes.delete(workspaceID);
    state.chatPlanModes.delete(workspaceID);
  } else {
    state.chatComposerModes.set(workspaceID, mode);
    state.chatPlanModes.set(workspaceID, false);
  }
}

export const kanbanLaneLabels: Record<string, string> = {
  ready: "Ready",
  inProgress: "In Progress",
  blocked: "Blocked",
  done: "Done",
};

export function cloneSettings(settings: llm.Settings): llm.Settings {
  return llm.Settings.createFrom(JSON.parse(JSON.stringify(settings)));
}

export function cloneWebAccessSettings(settings: services.WebAccessSettings | null | undefined): services.WebAccessSettings {
  return services.WebAccessSettings.createFrom(JSON.parse(JSON.stringify(settings ?? {})));
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

export function chatVideoDraftsFor(workspaceID: string): ChatVideoDraft[] {
  return state.chatVideoDrafts.get(workspaceID) ?? [];
}

export function chatVideoDraftTotalBytes(workspaceID: string): number {
  return chatVideoDraftsFor(workspaceID).reduce((total, video) => total + video.bytes, 0);
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
  const mode = chatComposerModeFor(workspaceID);
  return mode === "plan";
}

export function chatAgentModeIDFor(workspaceID: string): string {
  const selected = state.selectedAgentModeIds.get(workspaceID);
  if (selected !== undefined && selected !== "") {
    return selected;
  }
  /* Fallback to built-in plan/general IDs for backward compatibility. */
  const mode = chatComposerModeFor(workspaceID);
  if (mode === "plan") {
    return "plan";
  }
  return "general";
}

export function agentModesForWorkspace(workspaceID: string): services.AgentMode[] {
  return state.agentModes.get(workspaceID) ?? [];
}

export function chatAgentModeNameFor(workspaceID: string): string {
  const id = chatAgentModeIDFor(workspaceID);
  const modes = agentModesForWorkspace(workspaceID);
  const mode = modes.find((m) => m.id === id);
  return mode?.name ?? id;
}

export function setChatAgentMode(workspaceID: string, modeID: string) {
  if (modeID) {
    state.selectedAgentModeIds.set(workspaceID, modeID);
  } else {
    state.selectedAgentModeIds.delete(workspaceID);
  }
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

export function taskBoardFor(workspaceID: string): services.TaskBoard {
  return (
    state.taskBoards.get(workspaceID) ??
    services.TaskBoard.createFrom({
      workspaceId: workspaceID,
      storagePath: "",
      doneStoragePath: "",
      workspaceStatePath: "",
      gitIgnored: false,
      doneGitIgnored: false,
      workspaceStateGitIgnored: false,
      tags: [],
      tasks: [],
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

export function gitRepositoryViewFor(workspaceID: string): services.WorkspaceGitRepositoryView {
  return (
    state.gitRepositoryViews.get(workspaceID) ??
    services.WorkspaceGitRepositoryView.createFrom({
      workspaceId: workspaceID,
      selectedFolderId: state.selectedGitRepositoryFolders.get(workspaceID) ?? "",
      repositories: [],
      repository: null,
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

export function chatCompletionNotificationsEnabled(settings: llm.Settings | null | undefined): boolean {
  // Default to true (enabled) when the field is absent/undefined.
  return (settings as { enableChatCompletionNotifications?: boolean } | null | undefined)
    ?.enableChatCompletionNotifications !== false;
}

export function kanbanCompleteNotificationsEnabled(settings: llm.Settings | null | undefined): boolean {
  // Default to true (enabled) when the field is absent/undefined.
  return (settings as { enableKanbanCompleteNotifications?: boolean } | null | undefined)
    ?.enableKanbanCompleteNotifications !== false;
}

export function limitKanbanConcurrencyEnabled(settings: llm.Settings | null | undefined): boolean {
  return (settings as { limitKanbanConcurrency?: boolean } | null | undefined)
    ?.limitKanbanConcurrency === true;
}

export function gitSplitDiffViewEnabled(settings: llm.Settings | null | undefined): boolean {
  return (settings as { disableGitSplitDiffView?: boolean } | null | undefined)
    ?.disableGitSplitDiffView !== true;
}

export function thinkingCorrectionEnabled(settings: llm.Settings | null | undefined): boolean {
  return (settings as { thinkingCorrection?: boolean } | null | undefined)
    ?.thinkingCorrection === true;
}

export function thinkingTokenBudgetEnabled(settings: llm.Settings | null | undefined): boolean {
  return (settings as { thinkingTokenBudget?: number } | null | undefined)
    ?.thinkingTokenBudget !== 0;
}

export function getActiveChatModelLabel(): string {
  const endpoints = state.settingsDraft?.endpoints ?? [];
  if (!endpoints.length) {
    return "";
  }
  const selection = state.settingsDraft?.endpointSelection;
  const endpointID = selection?.chat || endpoints[0].id;
  const endpoint = endpoints.find((ep) => ep.id === endpointID);
  return endpoint?.name?.trim() || endpoint?.model?.trim() || endpoint?.id?.trim() || "";
}

/* ------------------------------------------------------------------ */
/*  Dashboard layout helpers                                           */
/* ------------------------------------------------------------------ */

const dashboardModes: AppMode[] = ["chat", "tasks", "kanban", "code", "git", "dashboard"];

export function defaultDashboardLayouts(): Record<AppMode, DashboardWidget[]> {
  return {
    chat: [],
    tasks: [],
    kanban: [],
    code: [],
    git: [],
    dashboard: [
      { id: "chat-busy-status", view: "dashboard", title: "Chat Status", size: "small", order: 0 },
      { id: "kanban-summary", view: "dashboard", title: "Kanban Summary", size: "wide", order: 1 },
      { id: "system-workspaces", view: "dashboard", title: "Workspaces", size: "medium", order: 2 },
      { id: "chat-token-budget", view: "dashboard", title: "Token Budget", size: "small", order: 3 },
      { id: "tasks-overview", view: "dashboard", title: "Tasks Overview", size: "medium", order: 4 },
      { id: "kanban-progress", view: "dashboard", title: "Card Progress", size: "medium", order: 5 },
      { id: "git-branch", view: "dashboard", title: "Current Branch", size: "small", order: 6 },
      { id: "chat-recent", view: "dashboard", title: "Recent Chat", size: "large", order: 7 },
      { id: "git-recent-commits", view: "dashboard", title: "Recent Commits", size: "medium", order: 8 },
      { id: "system-heartbeat", view: "dashboard", title: "Heartbeat", size: "small", order: 9 },
    ],
    settings: [],
  };
}

export function getDashboardWidgets(view: AppMode): DashboardWidget[] {
  // Always use "dashboard" as the key for the unified dashboard
  const widgets = state.dashboardLayouts["dashboard"];
  if (widgets !== undefined) {
    return widgets;
  }
  // Initialize with defaults on first access
  const defaults = defaultDashboardLayouts();
  state.dashboardLayouts = defaults;
  return defaults["dashboard"] ?? [];
}

export function setDashboardWidgets(_view: AppMode, widgets: DashboardWidget[]) {
  // Always store under "dashboard" key for the unified dashboard
  state.dashboardLayouts["dashboard"] = widgets;
  scheduleDashboardSave();
}

// Debounced dashboard layout persistence
let dashboardSaveTimer: number | null = null;
function scheduleDashboardSave() {
  if (dashboardSaveTimer !== null) {
    clearTimeout(dashboardSaveTimer);
  }
  dashboardSaveTimer = window.setTimeout(async () => {
    dashboardSaveTimer = null;
    try {
      await saveDashboardLayoutsToBackend();
    } catch {
      /* Non-fatal: layout will persist on next change or restart. */
    }
  }, 500);
}

export async function loadDashboardLayoutsFromBackend(): Promise<void> {
  try {
    const { GetDashboardLayouts } = await import("../backend/services");
    const layouts = await GetDashboardLayouts();
    if (layouts && Object.keys(layouts).length > 0) {
      // Convert backend layout format to frontend DashboardWidget[] per view
      const converted: Record<AppMode, DashboardWidget[]> = {} as Record<AppMode, DashboardWidget[]>;
      for (const [view, widgets] of Object.entries(layouts)) {
        converted[view as AppMode] = widgets.map((w) => ({
          id: w.id as DashboardWidget["id"],
          view: w.view as AppMode,
          title: w.title,
          size: w.size as DashboardWidget["size"],
          order: w.order,
        }));
      }
      state.dashboardLayouts = converted;
    }
  } catch {
    /* Non-fatal: use defaults. */
  }
}

async function saveDashboardLayoutsToBackend(): Promise<void> {
  const { SaveDashboardLayout } = await import("../backend/services");
  for (const [view, widgets] of Object.entries(state.dashboardLayouts)) {
    await SaveDashboardLayout(view as string, widgets);
  }
}
