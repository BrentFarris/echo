
import { clearCodeTabSwitcher, deleteSelectedCodePaths, ensureCodeViewRootLoaded, refreshOpenCodeTabsFromDisk, startCodeCreate, startCodeRename } from "../codeView";
import { ChooseWorkspaceFolder, ChooseWorkspaceFolderForWorkspace, ChooseWorkspaceIcon, ClearDoneKanbanCards, ClearKanbanCardRecovery, ClearWorkspaceChangeReview, ClearWorkspaceIcon, CloseKanbanCardDetail, CreateKanbanCardFromChatMessage, DeleteKanbanCard, DeleteWorkspace, ExecutePlan, GetHeartbeatConfig, LoadState, LoadWebAccessStatus, ListAgentModes, LoadWorkspaceChangeReview, MoveKanbanCard, OpenKanbanCardDetail, OpenWorkspaceExplorer, OpenWorkspacePathExplorer, PrepareRebuildAndRelaunch, PruneChatMessage, RemoveWorkspaceFolder, ResetKanbanCard, RetryChatMessage, RotateWebAccessToken, SetActiveWorkspace, StartKanbanExecution, StopChatStream, StopKanbanCard, StopKanbanExecution } from "../backend/services";
import { appRoot } from "./dom";
import { getAppCallbacks } from "./callbacks";
import { loadActiveChangeReview, refreshWorkspaceChangeReview, scrollChangeReview } from "./changes";
import { loadActiveCodeViewIfNeeded } from "./codeViewBridge";
import { dismissContextMenu } from "./contextMenu";
import { closeGitMenu, closeGitStashReview, dropWorkspaceGitRepositoryState, openGitChangeInCode, openGitMenuPage, openWorkspaceGitRepository, refreshWorkspaceGitRepository, revertWorkspaceGitChanges, revertWorkspaceGitFile, runGitMenuCommand, selectGitCommit, stageWorkspaceGitChanges, stageWorkspaceGitFile, stageWorkspaceGitFolder, syncWorkspaceGitRepository, toggleGitChangeSection, toggleGitDiffViewMode, toggleGitHistory, toggleGitSourceSidebar, unstageWorkspaceGitChanges, unstageWorkspaceGitFile, unstageWorkspaceGitFolder } from "./git";
import { closeSelectedCardDetail, finishKanbanRun, forgetKanbanRun, loadActiveKanbanBoard, markKanbanRunStarted, maybePlayKanbanBoardNotification, toggleHeartbeatInterval, toggleWatchdogInterval } from "./kanban";
import { playNotificationSound } from "./notifications";
import { addLLMEndpoint, cancelAgentMode, deleteAgentModeSettings, deleteLLMEndpoint, editLLMEndpoint, finishEditingLLMEndpoint, saveAgentMode, saveNewAgentMode, startCreateAgentMode, startEditAgentMode } from "./settings";
import { activeWorkspace, chatImageDraftsFor, chatPlanModeFor, chatAgentModeIDFor, chatComposerModeFor, setChatComposerMode, chatSessionFor, chatVideoDraftsFor, getActiveChatKanbanTab, kanbanBoardFor, kanbanCards, limitKanbanConcurrencyEnabled, state, getDashboardWidgets, setDashboardWidgets, defaultDashboardLayouts } from "./state";
import { applyChatSessionSnapshot, clearChatMention, loadActiveChatSession, patchChatControls, patchChatPanel, scrollChatToBottom } from "./chat";
import { cloneSettings, cloneWebAccessSettings } from "./state";
import type { AppMode, MobileNavView, WidgetId, WidgetSize } from "./types";
import { applyTheme, settingsWithThemeDefaults, themePaletteNames } from "./theme";
import { pushToast, dismissToast } from "./toasts";
import { loadActiveTaskBoard } from "./tasks";
import { copyTextToClipboard, errorMessage, laneLabel } from "./utils";
import { hydrateWorkspaceLetterDrafts } from "./workspace";
import { resetTokenBudget, loadTokenBudget } from "./budget";
import { loadLivenessConfig } from "./liveness";
import { availableWidgets } from "./dashboard/grid";

export async function handleAction(event: Event) {
  const target = event.currentTarget as HTMLElement;
  const action = target.dataset.action;
  const workspaceID = target.dataset.workspaceId ?? "";

  try {
    if (action === "dismiss-toast") {
      dismissToast(target.dataset.toastId ?? "");
      return;
    }
    if (action === "reset-budget") {
      const wsID = target.dataset.workspaceId ?? "";
      if (!wsID) return;
      void resetTokenBudget(wsID);
      return;
    }
    if (action === "show-in-explorer") {
      const workspaceID = target.dataset.workspaceId ?? "";
      if (!workspaceID) {
        return;
      }
      const workspacePath = target.dataset.workspacePath ?? "";
      try {
        if (workspacePath) {
          await OpenWorkspacePathExplorer(workspaceID, workspacePath);
        } else {
          await OpenWorkspaceExplorer(workspaceID);
        }
        pushToast("Opened in Explorer.", "success");
      } catch (error) {
        pushToast(errorMessage(error), "error");
      }
      dismissContextMenu();
      return;
    }
    if (action === "code-create-file" || action === "code-create-folder") {
      const workspaceID = target.dataset.workspaceId ?? "";
      const path = target.dataset.codePath ?? "";
      const kind = target.dataset.codeKind ?? "";
      if (!workspaceID || !path) {
        return;
      }
      dismissContextMenu();
      await startCodeCreate(
        workspaceID,
        path,
        kind,
        action === "code-create-file" ? "file" : "folder",
        getAppCallbacks().codeViewCallbacks(),
      );
      return;
    }
    if (action === "code-rename-path") {
      const workspaceID = target.dataset.workspaceId ?? "";
      const path = target.dataset.codePath ?? "";
      const kind = target.dataset.codeKind ?? "";
      if (!workspaceID || !path) {
        return;
      }
      dismissContextMenu();
      await startCodeRename(workspaceID, path, kind, getAppCallbacks().codeViewCallbacks());
      return;
    }
    if (action === "code-delete-path") {
      const workspaceID = target.dataset.workspaceId ?? "";
      if (!workspaceID) {
        return;
      }
      dismissContextMenu();
      await deleteSelectedCodePaths(workspaceID, getAppCallbacks().codeViewCallbacks());
      return;
    }
    if (action === "open-code-view") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      state.appMode = "code";
      state.mobileNavView = "code";
      state.openGitChangeWorkspaces.delete(workspace.id);
      const loading = ensureCodeViewRootLoaded(workspace.id);
      getAppCallbacks().render();
      await loading;
      getAppCallbacks().render();
      return;
    }
    if (action === "close-code-view") {
      const workspace = activeWorkspace();
      if (workspace) {
        clearCodeTabSwitcher(workspace.id);
        state.openGitChangeWorkspaces.delete(workspace.id);
        state.expandedGitChangeWorkspaces.delete(workspace.id);
        state.loadingGitChangeWorkspaces.delete(workspace.id);
        state.loadingGitRepositoryWorkspaces.delete(workspace.id);
      }
      const tab = workspace ? getActiveChatKanbanTab(workspace.id) : "chat";
      state.appMode = tab;
      state.mobileNavView = tab;
      getAppCallbacks().render();
      return;
    }
    if (action === "open-dashboard") {
      state.dashboardPreviousMode = state.appMode;
      state.appMode = "dashboard";
      state.mobileNavView = "dashboard" as MobileNavView;
      getAppCallbacks().render();
      return;
    }
    if (action === "close-dashboard") {
      const prev = state.dashboardPreviousMode ?? "chat";
      state.appMode = prev;
      state.mobileNavView = prev as MobileNavView;
      getAppCallbacks().render();
      return;
    }
    // Dashboard widget grid edit actions
    if (action === "dashboard-edit-toggle") {
      state.dashboardEditMode = !state.dashboardEditMode;
      getAppCallbacks().render();
      return;
    }
    if (action === "widget-remove") {
      const widgetId = target.dataset.widgetId ?? "";
      const widgets = getDashboardWidgets("dashboard");
      const filtered = widgets.filter((w) => w.id !== widgetId);
      setDashboardWidgets("dashboard", filtered);
      getAppCallbacks().render();
      return;
    }
    if (action === "widget-add") {
      const widgetId = target.dataset.widgetId as WidgetId;
      const widgetSize = target.dataset.widgetSize as WidgetSize;
      const widgets = getDashboardWidgets("dashboard");
      // Look up title from availableWidgets map
      const allAvail = availableWidgets["dashboard"] ?? [];
      const def = allAvail.find((a) => a.id === widgetId);
      if (!def) return;
      const newOrder = widgets.length;
      widgets.push({ id: widgetId, view: "dashboard", title: def.title, size: widgetSize, order: newOrder });
      setDashboardWidgets("dashboard", widgets);
      getAppCallbacks().render();
      return;
    }
    if (action === "widget-move-up") {
      const widgetId = target.dataset.widgetId ?? "";
      const widgets = getDashboardWidgets("dashboard");
      const idx = widgets.findIndex((w) => w.id === widgetId);
      if (idx > 0) {
        [widgets[idx - 1], widgets[idx]] = [widgets[idx], widgets[idx - 1]];
        setDashboardWidgets("dashboard", widgets);
        getAppCallbacks().render();
      }
      return;
    }
    if (action === "widget-move-down") {
      const widgetId = target.dataset.widgetId ?? "";
      const widgets = getDashboardWidgets("dashboard");
      const idx = widgets.findIndex((w) => w.id === widgetId);
      if (idx >= 0 && idx < widgets.length - 1) {
        [widgets[idx], widgets[idx + 1]] = [widgets[idx + 1], widgets[idx]];
        setDashboardWidgets("dashboard", widgets);
        getAppCallbacks().render();
      }
      return;
    }
    if (action === "reset-dashboard-layout") {
      const defaults = defaultDashboardLayouts();
      const defaultWidgets = defaults["dashboard"] ?? [];
      setDashboardWidgets("dashboard", [...defaultWidgets]);
      getAppCallbacks().render();
      return;
    }
    // Aliases for add-widget / remove-widget / move-widget-up / move-widget-down
    // (grid.ts uses widget-add / widget-remove / widget-move-up / widget-move-down)
    if (action === "add-widget") {
      const widgetId = target.dataset.widgetId as WidgetId;
      const widgetSize = target.dataset.widgetSize as WidgetSize;
      const widgets = getDashboardWidgets("dashboard");
      const allAvail = availableWidgets["dashboard"] ?? [];
      const def = allAvail.find((a) => a.id === widgetId);
      if (!def) return;
      const newOrder = widgets.length;
      widgets.push({ id: widgetId, view: "dashboard", title: def.title, size: widgetSize, order: newOrder });
      setDashboardWidgets("dashboard", widgets);
      getAppCallbacks().render();
      return;
    }
    if (action === "remove-widget") {
      const widgetId = target.dataset.widgetId ?? "";
      const widgets = getDashboardWidgets("dashboard");
      const filtered = widgets.filter((w) => w.id !== widgetId);
      setDashboardWidgets("dashboard", filtered);
      getAppCallbacks().render();
      return;
    }
    if (action === "move-widget-up") {
      const widgetId = target.dataset.widgetId ?? "";
      const widgets = getDashboardWidgets("dashboard");
      const idx = widgets.findIndex((w) => w.id === widgetId);
      if (idx > 0) {
        [widgets[idx - 1], widgets[idx]] = [widgets[idx], widgets[idx - 1]];
        setDashboardWidgets("dashboard", widgets);
        getAppCallbacks().render();
      }
      return;
    }
    if (action === "move-widget-down") {
      const widgetId = target.dataset.widgetId ?? "";
      const widgets = getDashboardWidgets("dashboard");
      const idx = widgets.findIndex((w) => w.id === widgetId);
      if (idx >= 0 && idx < widgets.length - 1) {
        [widgets[idx], widgets[idx + 1]] = [widgets[idx + 1], widgets[idx]];
        setDashboardWidgets("dashboard", widgets);
        getAppCallbacks().render();
      }
      return;
    }
    if (action === "open-git-changes") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      state.appMode = "git";
      state.mobileNavView = "git";
      await openWorkspaceGitRepository(workspace.id);
      return;
    }
    if (action === "close-git-changes") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      state.openGitChangeWorkspaces.delete(workspace.id);
      state.expandedGitChangeWorkspaces.delete(workspace.id);
      state.loadingGitChangeWorkspaces.delete(workspace.id);
      state.loadingGitRepositoryWorkspaces.delete(workspace.id);
      const tab = getActiveChatKanbanTab(workspace.id);
      state.appMode = tab;
      state.mobileNavView = tab;
      getAppCallbacks().render();
      return;
    }
    if (action === "toggle-git-changes-size") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      if (state.expandedGitChangeWorkspaces.has(workspace.id)) {
        state.expandedGitChangeWorkspaces.delete(workspace.id);
      } else {
        state.expandedGitChangeWorkspaces.add(workspace.id);
      }
      getAppCallbacks().render();
      return;
    }
    if (action === "refresh-git-changes") {
      const workspace = activeWorkspace();
      if (!workspace || state.loadingGitRepositoryWorkspaces.has(workspace.id)) {
        return;
      }
      await refreshWorkspaceGitRepository(workspace.id);
      return;
    }
    if (action === "sync-git-branch") {
      const workspace = activeWorkspace();
      if (!workspace || state.loadingGitRepositoryWorkspaces.has(workspace.id)) {
        return;
      }
      await syncWorkspaceGitRepository(workspace.id);
      return;
    }
    if (action === "open-git-menu-page") {
      openGitMenuPage(target.dataset.gitMenuPage ?? "root");
      return;
    }
    if (action === "close-git-menu") {
      closeGitMenu();
      return;
    }
    if (action === "run-git-menu-command") {
      await runGitMenuCommand(target.dataset.gitCommand ?? "");
      return;
    }
    if (action === "close-git-stash-review") {
      closeGitStashReview();
      return;
    }
    if (action === "toggle-git-sidebar") {
      toggleGitSourceSidebar();
      return;
    }
    if (action === "toggle-git-history") {
      toggleGitHistory();
      return;
    }
    if (action === "toggle-git-change-section") {
      toggleGitChangeSection(target.dataset.gitChangeSection ?? "");
      return;
    }
    if (action === "toggle-git-diff-view") {
      toggleGitDiffViewMode();
      return;
    }
    if (action === "stage-git-file") {
      await stageWorkspaceGitFile(target.dataset.gitFilePath ?? "");
      return;
    }
    if (action === "unstage-git-file") {
      await unstageWorkspaceGitFile(target.dataset.gitFilePath ?? "");
      return;
    }
    if (action === "stage-git-folder") {
      await stageWorkspaceGitFolder(target.dataset.gitFolderPath ?? "");
      return;
    }
    if (action === "unstage-git-folder") {
      await unstageWorkspaceGitFolder(target.dataset.gitFolderPath ?? "");
      return;
    }
    if (action === "stage-git-changes") {
      await stageWorkspaceGitChanges();
      return;
    }
    if (action === "unstage-git-changes") {
      await unstageWorkspaceGitChanges();
      return;
    }
    if (action === "revert-git-file") {
      await revertWorkspaceGitFile(target.dataset.gitFilePath ?? "");
      return;
    }
    if (action === "revert-git-changes") {
      await revertWorkspaceGitChanges();
      return;
    }
    if (action === "open-git-change-in-code") {
      await openGitChangeInCode(
        target.dataset.gitFilePath ?? "",
        Number.parseInt(target.dataset.gitTargetLine ?? "1", 10),
      );
      return;
    }
    if (action === "select-git-commit") {
      await selectGitCommit(target.dataset.commitHash ?? "");
      return;
    }
    if (action === "copy-git-commit-hash") {
      const hash = target.dataset.commitHash ?? "";
      if (!hash) {
        pushToast("Commit hash is unavailable.", "error");
        return;
      }
      await copyTextToClipboard(hash);
      pushToast("Commit hash copied.", "success");
      return;
    }
    if (action === "open-settings") {
      const workspace = activeWorkspace();
      if (workspace) {
        clearCodeTabSwitcher(workspace.id);
      }
      state.settingsOpen = true;
      state.formError = "";
      state.settingsEndpointEditId = "";
      state.settingsDraft = cloneSettings(state.appState!.settings);
      state.webAccessDraft = cloneWebAccessSettings(state.appState!.webAccess);
      state.webAccessStatus = await LoadWebAccessStatus();
      applyTheme(state.settingsDraft);
      hydrateWorkspaceLetterDrafts(state.appState?.workspaces ?? []);
      getAppCallbacks().render();
    }
    if (action === "close-settings") {
      state.settingsOpen = false;
      state.formError = "";
      state.settingsEndpointEditId = "";
      cancelAgentMode();
      applyTheme(state.appState?.settings);
      getAppCallbacks().render();
    }
    if (action === "reset-settings") {
      state.settingsDraft = cloneSettings(state.appState!.settings);
      state.settingsEndpointEditId = "";
      state.webAccessDraft = cloneWebAccessSettings(state.appState!.webAccess);
      state.webAccessStatus = await LoadWebAccessStatus();
      applyTheme(state.settingsDraft);
      hydrateWorkspaceLetterDrafts(state.appState?.workspaces ?? []);
      cancelAgentMode();
      state.formError = "";
      getAppCallbacks().render();
    }
    if (action === "add-llm-endpoint") {
      addLLMEndpoint();
      getAppCallbacks().render();
      return;
    }
    if (action === "edit-llm-endpoint") {
      editLLMEndpoint(target.dataset.endpointId ?? "");
      getAppCallbacks().render();
      return;
    }
    if (action === "finish-edit-llm-endpoint") {
      finishEditingLLMEndpoint();
      getAppCallbacks().render();
      return;
    }
    if (action === "delete-llm-endpoint") {
      if (!window.confirm("Delete this LLM endpoint?")) {
        return;
      }
      deleteLLMEndpoint(target.dataset.endpointId ?? "");
      getAppCallbacks().render();
      return;
    }
    if (action === "create-agent-mode") {
      startCreateAgentMode();
      getAppCallbacks().render();
      return;
    }
    if (action === "edit-agent-mode") {
      const modeID = target.dataset.agentModeId ?? "";
      if (!modeID) return;
      startEditAgentMode(modeID);
      getAppCallbacks().render();
      return;
    }
    if (action === "cancel-agent-mode") {
      cancelAgentMode();
      getAppCallbacks().render();
      return;
    }
    if (action === "save-new-agent-mode") {
      await saveNewAgentMode();
      return;
    }
    if (action === "save-agent-mode") {
      const modeID = target.dataset.agentModeId ?? "";
      if (!modeID) return;
      await saveAgentMode(modeID);
      return;
    }
    if (action === "delete-agent-mode-settings") {
      const modeID = target.dataset.agentModeId ?? "";
      if (!modeID) return;
      await deleteAgentModeSettings(modeID);
      return;
    }
    if (action === "rotate-web-access-token") {
      state.webAccessQRCodeURL = "";
      state.appState = await RotateWebAccessToken();
      state.webAccessDraft = cloneWebAccessSettings(state.appState.webAccess);
      state.webAccessStatus = await LoadWebAccessStatus();
      pushToast("Web access token rotated.", "success");
      getAppCallbacks().render();
      return;
    }
    if (action === "show-web-access-qr") {
      state.webAccessQRCodeURL = target.dataset.webAccessUrl ?? "";
      getAppCallbacks().render();
      return;
    }
    if (action === "hide-web-access-qr") {
      state.webAccessQRCodeURL = "";
      getAppCallbacks().render();
      return;
    }
    if (action === "set-theme-palette") {
      const palette = target.dataset.themePalette;
      if (palette && (themePaletteNames as string[]).includes(palette)) {
        state.settingsThemePalette = palette as typeof state.settingsThemePalette;
        getAppCallbacks().render();
      }
    }
    if (action === "restore-theme-defaults") {
      if (!state.settingsDraft) {
        return;
      }
      state.settingsDraft = settingsWithThemeDefaults(state.settingsDraft);
      state.formError = "";
      applyTheme(state.settingsDraft);
      getAppCallbacks().render();
    }
    if (action === "rebuild-and-relaunch") {
      const echoWorkspace = findEchoSourceWorkspaceForAction();
      if (!echoWorkspace) {
        pushToast("Add the Echo source workspace first.", "error");
        return;
      }
      if (state.runningKanbanWorkspaces.has(echoWorkspace.id)) {
        pushToast("Cannot rebuild while Kanban agents are running.", "error");
        return;
      }
      if (!window.confirm("Rebuild and relaunch Echo?\n\nThis will shut down the current instance, rebuild the application, and launch the new build.\n\nAny unsaved work may be lost.")) {
        return;
      }
      try {
        await PrepareRebuildAndRelaunch(echoWorkspace.id);
        pushToast("Rebuild started. Echo is shutting down...", "info");
      } catch (error) {
        pushToast(errorMessage(error), "error");
      }
      return;
    }
    if (action === "request-push-notification-permission") {
      const { handleRequestPushNotificationPermission } = await import("../app/settings");
      void handleRequestPushNotificationPermission();
      return;
    }
    if (action === "add-workspace") {
      state.appState = await ChooseWorkspaceFolder();
      await loadActiveChatSession();
      await loadActiveKanbanBoard();
      await loadActiveChangeReview();
      await loadActiveCodeViewIfNeeded();
      await loadActiveChangesViewIfNeeded();
      pushToast("Workspace list updated.", "success");
      getAppCallbacks().render();
    }
    if (action === "add-workspace-folder") {
      if (!workspaceID) {
        return;
      }
      state.appState = await ChooseWorkspaceFolderForWorkspace(workspaceID);
      await refreshWorkspaceChangeReview(workspaceID);
      await refreshOpenCodeTabsFromDisk(workspaceID, getAppCallbacks().codeViewCallbacks());
      await loadActiveChangesViewIfNeeded();
      pushToast("Workspace folder added.", "success");
      getAppCallbacks().render();
      return;
    }
    if (action === "remove-workspace-folder") {
      const folderID = target.dataset.folderId ?? "";
      if (!workspaceID || !folderID || !window.confirm("Remove this folder from the workspace?")) {
        return;
      }
      state.appState = await RemoveWorkspaceFolder(workspaceID, folderID);
      state.changeReviews.delete(workspaceID);
      state.gitChangeReviews.delete(workspaceID);
      dropWorkspaceGitRepositoryState(workspaceID);
      state.loadingGitChangeWorkspaces.delete(workspaceID);
      await refreshOpenCodeTabsFromDisk(workspaceID, getAppCallbacks().codeViewCallbacks());
      await loadActiveChangesViewIfNeeded();
      pushToast("Workspace folder removed.", "success");
      getAppCallbacks().render();
      return;
    }
    if (action === "refresh-workspaces") {
      state.appState = await LoadState();
      await loadActiveChatSession();
      await loadActiveKanbanBoard();
      await loadActiveTaskBoard();
      await loadActiveChangeReview();
      await loadActiveCodeViewIfNeeded();
      await loadActiveChangesViewIfNeeded();
      pushToast(
        activeWorkspace()?.missing
          ? "One or more folders are still unavailable."
          : "Workspace folders refreshed.",
        activeWorkspace()?.missing ? "error" : "success",
      );
      getAppCallbacks().render();
    }
    if (action === "choose-workspace-icon") {
      state.appState = await ChooseWorkspaceIcon(workspaceID);
      pushToast("Workspace icon updated.", "success");
      getAppCallbacks().render();
    }
    if (action === "clear-workspace-icon") {
      state.appState = await ClearWorkspaceIcon(workspaceID);
      pushToast("Workspace icon cleared.", "success");
      getAppCallbacks().render();
    }
    if (action === "switch-view") {
      const view = target.dataset.view;
      if (!view) {
        return;
      }
      const workspace = activeWorkspace();
      if (view === "chat" || view === "tasks" || view === "kanban") {
        state.appMode = view as AppMode;
        state.mobileNavView = view as MobileNavView;
        if (workspace) {
          state.activeChatKanbanTab.set(workspace.id, view);
          if (view === "tasks") {
            await loadActiveTaskBoard();
          }
        }
      } else if (view === "code") {
        state.appMode = "code";
        state.mobileNavView = "code";
        if (workspace) {
          state.openGitChangeWorkspaces.delete(workspace.id);
        }
      } else if (view === "git") {
        if (!workspace) {
          return;
        }
        state.appMode = "git";
        state.mobileNavView = "git";
        await openWorkspaceGitRepository(workspace.id);
        return;
      }
      getAppCallbacks().bindActionEvents(appRoot);
      getAppCallbacks().render();
      return;
    }
    if (action === "toggle-workspace-dropdown") {
      state.workspaceDropdownOpen = !state.workspaceDropdownOpen;
      if (state.workspaceDropdownOpen) {
        // Move focus to the first workspace option for accessibility.
        const dropdown = appRoot.querySelector<HTMLDivElement>(
          '.mobile-nav-workspace-dropdown',
        );
        if (dropdown) {
          const firstOption = dropdown.querySelector<HTMLButtonElement>('button');
          if (firstOption) {
            firstOption.focus();
          }
        }
      }
      getAppCallbacks().render();
      return;
    }
    if (action === "dismiss-workspace-dropdown") {
      state.workspaceDropdownOpen = false;
      getAppCallbacks().render();
      return;
    }
    if (action === "activate-workspace") {
      const current = activeWorkspace();
      if (current && current.id !== workspaceID) {
        await closeSelectedCardDetail(current.id);
        state.openChangeReviewWorkspaces.delete(current.id);
        state.openGitChangeWorkspaces.delete(current.id);
        state.expandedGitChangeWorkspaces.delete(current.id);
        state.loadingGitChangeWorkspaces.delete(current.id);
        state.loadingGitRepositoryWorkspaces.delete(current.id);
      }
      state.appState = await SetActiveWorkspace(workspaceID);
      await loadActiveChatSession();
      await loadActiveKanbanBoard();
      await loadActiveTaskBoard();
      await loadActiveChangeReview();
      await loadActiveCodeViewIfNeeded();
      /* Reload agent modes for the new workspace. */
      try {
        const modes = await ListAgentModes(workspaceID);
        state.agentModes.set(workspaceID, modes);
      } catch {
        /* Non-fatal: will load on first chat render. */
      }
      /* Restore heartbeat interval from backend. */
      try {
        const hbConfig = await GetHeartbeatConfig(workspaceID);
        if (hbConfig.enabled && hbConfig.interval > 0) {
          state.heartbeatIntervals.set(workspaceID, hbConfig.interval);
        } else {
          state.heartbeatIntervals.delete(workspaceID);
        }
      } catch {
        /* Non-fatal. */
      }
      /* Load token budget for the new workspace. */
      void loadTokenBudget(workspaceID);
      void loadLivenessConfig(workspaceID);
      state.workspaceDropdownOpen = false;
      await loadActiveChangesViewIfNeeded();
      getAppCallbacks().render();
    }
    if (action === "execute-plan") {
      const workspace = activeWorkspace();
      if (!workspace || state.executingPlans.has(workspace.id)) {
        return;
      }
      state.executingPlans.add(workspace.id);
      getAppCallbacks().render();
      try {
        const board = await ExecutePlan(workspace.id);
        state.kanbanBoards.set(workspace.id, board);
        if ((board.ready ?? []).length > 0) {
          playNotificationSound();
        }
        pushToast("Plan converted into Ready cards.", "success");
        state.appMode = "kanban";
        state.mobileNavView = "kanban";
        state.activeChatKanbanTab.set(workspace.id, "kanban");
      } finally {
        state.executingPlans.delete(workspace.id);
      }
      getAppCallbacks().render();
    }
    if (action === "open-change-review") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      await closeSelectedCardDetail(workspace.id);
      state.appMode = "git";
      state.mobileNavView = "git";
      await openWorkspaceGitRepository(workspace.id);
    }
    if (action === "close-change-review") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      state.openChangeReviewWorkspaces.delete(workspace.id);
      state.expandedChangeReviewWorkspaces.delete(workspace.id);
      getAppCallbacks().render();
    }
    if (action === "toggle-change-review-size") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      if (state.expandedChangeReviewWorkspaces.has(workspace.id)) {
        state.expandedChangeReviewWorkspaces.delete(workspace.id);
      } else {
        state.expandedChangeReviewWorkspaces.add(workspace.id);
      }
      getAppCallbacks().render();
    }
    if (action === "clear-change-review") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      state.changeReviews.set(workspace.id, await ClearWorkspaceChangeReview(workspace.id));
      pushToast("AI change review cleared.");
      getAppCallbacks().render();
    }
    if (action === "previous-change") {
      scrollChangeReview(-1);
    }
    if (action === "next-change") {
      scrollChangeReview(1);
    }
    if (action === "remove-chat-image") {
      const workspace = activeWorkspace();
      const imageID = target.dataset.imageId ?? "";
      if (!workspace || !imageID) {
        return;
      }
      state.chatImageDrafts.set(
        workspace.id,
        chatImageDraftsFor(workspace.id).filter((image) => image.id !== imageID),
      );
      patchChatPanel();
      patchChatControls();
    }
    if (action === "remove-chat-video") {
      const workspace = activeWorkspace();
      const videoID = target.dataset.videoId ?? "";
      if (!workspace || !videoID) {
        return;
      }
      state.chatVideoDrafts.set(
        workspace.id,
        chatVideoDraftsFor(workspace.id).filter((video) => video.id !== videoID),
      );
      patchChatPanel();
      patchChatControls();
    }
    if (action === "toggle-heartbeat") {
      const workspaceID = target.dataset.workspaceId ?? "";
      if (!workspaceID) return;
      void toggleHeartbeatInterval(workspaceID).then(() => getAppCallbacks().render());
      return;
    }
    if (action === "toggle-watchdog") {
      const workspaceID = target.dataset.workspaceId ?? "";
      if (!workspaceID) return;
      void toggleWatchdogInterval(workspaceID);
      return;
    }
    if (action === "start-agents") {
      const workspace = activeWorkspace();
      if (!workspace || state.runningKanbanWorkspaces.has(workspace.id)) {
        return;
      }
      markKanbanRunStarted(workspace.id);
      getAppCallbacks().render();
      try {
        const concurrency = limitKanbanConcurrencyEnabled(state.appState?.settings) ? 1 : 2;
        state.kanbanBoards.set(workspace.id, await StartKanbanExecution(workspace.id, concurrency));
      } catch (error) {
        forgetKanbanRun(workspace.id);
        throw error;
      }
      pushToast("Kanban agents started.", "success");
      getAppCallbacks().render();
    }
    if (action === "open-create-ready-card") {
      const workspace = activeWorkspace();
      if (!workspace || state.runningKanbanWorkspaces.has(workspace.id)) {
        return;
      }
      state.creatingKanbanCardWorkspaces.add(workspace.id);
      if (!state.kanbanCardCreationDrafts.has(workspace.id)) {
        state.kanbanCardCreationDrafts.set(workspace.id, {
          title: "",
          description: "",
          acceptanceCriteria: "",
        });
      }
      getAppCallbacks().render();
      return;
    }
    if (action === "cancel-create-ready-card") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      state.creatingKanbanCardWorkspaces.delete(workspace.id);
      state.kanbanCardCreationDrafts.delete(workspace.id);
      getAppCallbacks().render();
      return;
    }
    if (action === "stop-agents") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      state.kanbanBoards.set(workspace.id, await StopKanbanExecution(workspace.id));
      finishKanbanRun(workspace.id);
      pushToast("Kanban agents stopped.");
      getAppCallbacks().render();
    }
    if (action === "clear-done-cards") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      const beforeDoneCount = kanbanBoardFor(workspace.id).done?.length ?? 0;
      if (beforeDoneCount === 0) {
        return;
      }
      const board = await ClearDoneKanbanCards(workspace.id);
      state.kanbanBoards.set(workspace.id, board);
      const selectedID = state.selectedKanbanCards.get(workspace.id);
      if (selectedID && !kanbanCards(board).some((card) => card.id === selectedID)) {
        state.selectedKanbanCards.delete(workspace.id);
      }
      const clearedCount = beforeDoneCount - (board.done?.length ?? 0);
      pushToast(
        clearedCount > 0
          ? `${clearedCount} done card${clearedCount === 1 ? "" : "s"} cleared.`
          : "Done cards are still needed by unfinished cards.",
        clearedCount > 0 ? "success" : "info",
      );
      getAppCallbacks().render();
    }
    if (action === "open-card") {
      const workspace = activeWorkspace();
      const cardID = target.dataset.cardId ?? "";
      if (!workspace || !cardID) {
        return;
      }
      state.selectedKanbanCards.set(workspace.id, cardID);
      state.kanbanBoards.set(workspace.id, await OpenKanbanCardDetail(workspace.id, cardID));
      getAppCallbacks().render();
    }
    if (action === "stop-card") {
      const workspace = activeWorkspace();
      const cardID = target.dataset.cardId ?? "";
      if (!workspace || !cardID) {
        return;
      }
      state.kanbanBoards.set(workspace.id, await StopKanbanCard(workspace.id, cardID));
      state.selectedKanbanCards.set(workspace.id, cardID);
      pushToast("Card agent stopped.");
      getAppCallbacks().render();
    }
    if (action === "reset-card") {
      const workspace = activeWorkspace();
      const cardID = target.dataset.cardId ?? "";
      if (!workspace || !cardID || !window.confirm("Reset this card and clear its progress transcript?")) {
        return;
      }
      state.kanbanBoards.set(workspace.id, await ResetKanbanCard(workspace.id, cardID));
      state.selectedKanbanCards.set(workspace.id, cardID);
      pushToast("Card reset.", "success");
      getAppCallbacks().render();
    }
    if (action === "delete-card") {
      const workspace = activeWorkspace();
      const cardID = target.dataset.cardId ?? "";
      if (!workspace || !cardID) {
        return;
      }
      const currentBoard = kanbanBoardFor(workspace.id);
      const card = kanbanCards(currentBoard).find((item) => item.id === cardID);
      if (!card) {
        return;
      }
      const confirmMessage = card.lane === "ready"
        ? `Delete "${card.title || card.id}" and any cards that depend on it?`
        : `Delete "${card.title || card.id}"?`;
      if (!window.confirm(confirmMessage)) {
        return;
      }
      const beforeCount = kanbanCards(currentBoard).length;
      const board = await DeleteKanbanCard(workspace.id, cardID);
      state.kanbanBoards.set(workspace.id, board);
      const selectedID = state.selectedKanbanCards.get(workspace.id);
      if (selectedID && !kanbanCards(board).some((item) => item.id === selectedID)) {
        state.selectedKanbanCards.delete(workspace.id);
      }
      const deletedCount = beforeCount - kanbanCards(board).length;
      pushToast(`${deletedCount} card${deletedCount === 1 ? "" : "s"} deleted.`, "success");
      getAppCallbacks().render();
      return;
    }
    if (action === "close-card") {
      const workspace = activeWorkspace();
      if (workspace) {
        const cardID = state.selectedKanbanCards.get(workspace.id) ?? "";
        if (cardID) {
          state.kanbanBoards.set(workspace.id, await CloseKanbanCardDetail(workspace.id, cardID));
        }
        state.selectedKanbanCards.delete(workspace.id);
      }
      getAppCallbacks().render();
    }
    if (action === "move-card") {
      const workspace = activeWorkspace();
      const cardID = target.dataset.cardId ?? "";
      const lane = target.dataset.lane ?? "";
      if (!workspace || !cardID || !lane) {
        return;
      }
      const previousBoard = kanbanBoardFor(workspace.id);
      const board = await MoveKanbanCard(workspace.id, cardID, lane);
      state.kanbanBoards.set(workspace.id, board);
      state.selectedKanbanCards.set(workspace.id, cardID);
      maybePlayKanbanBoardNotification(previousBoard, board);
      pushToast(`Card moved to ${laneLabel(lane)}.`, "success");
      getAppCallbacks().render();
    }
    if (action === "clear-card-recovery") {
      const workspace = activeWorkspace();
      const cardID = target.dataset.cardId ?? "";
      if (!workspace || !cardID) {
        return;
      }
      state.kanbanBoards.set(workspace.id, await ClearKanbanCardRecovery(workspace.id, cardID));
      state.selectedKanbanCards.set(workspace.id, cardID);
      pushToast("Recovery state cleared.", "success");
      getAppCallbacks().render();
    }
    if (action === "create-card-from-message") {
      const workspace = activeWorkspace();
      const messageID = target.dataset.messageId ?? "";
      if (!workspace || !messageID) {
        return;
      }
      const board = await CreateKanbanCardFromChatMessage(workspace.id, messageID);
      state.kanbanBoards.set(workspace.id, board);
      if ((board.ready ?? []).length > 0) {
        playNotificationSound();
      }
      pushToast("Message converted into a Ready card.", "success");
      getAppCallbacks().render();
      return;
    }
    if (action === "copy-message") {
      const workspace = activeWorkspace();
      const messageID = target.dataset.messageId ?? "";
      if (!workspace || !messageID) {
        return;
      }
      const message = (chatSessionFor(workspace.id).messages ?? []).find((item) => item.id === messageID);
      const content = message?.content ?? "";
      if (!content) {
        pushToast("Message has no text to copy.", "error");
        return;
      }
      await copyTextToClipboard(content);
      pushToast("Message copied.", "success");
      return;
    }
    if (action === "retry-message") {
      const workspace = activeWorkspace();
      const messageID = target.dataset.messageId ?? "";
      if (!workspace || !messageID) {
        return;
      }
      state.editingMessageIds.delete(messageID);
      try {
        applyChatSessionSnapshot(
          await RetryChatMessage(workspace.id, messageID, chatAgentModeIDFor(workspace.id)),
        );
        pushToast("Response regenerated.", "success");
      } catch (error) {
        pushToast(errorMessage(error), "error");
      }
      getAppCallbacks().render();
      scrollChatToBottom();
      return;
    }
    if (action === "stop-chat") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      applyChatSessionSnapshot(await StopChatStream(workspace.id));
      patchChatPanel();
    }
    if (action === "prune-chat-message") {
      const workspace = activeWorkspace();
      const messageID = target.dataset.messageId ?? "";
      if (
        !workspace ||
        !messageID ||
        !window.confirm("Prune this message? It will be removed from chat, future AI context, and Kanban generation.")
      ) {
        return;
      }
      applyChatSessionSnapshot(await PruneChatMessage(workspace.id, messageID));
      state.editingMessageIds.delete(messageID);
      pushToast("Message pruned.", "success");
      patchChatPanel();
      return;
    }
    if (action === "edit-message") {
      const workspace = activeWorkspace();
      const messageID = target.dataset.messageId ?? "";
      if (!workspace || !messageID) {
        return;
      }
      state.editingMessageIds.add(messageID);
      getAppCallbacks().render();
      const form = appRoot.querySelector<HTMLFormElement>(
        `[data-chat-edit-form][data-message-id="${CSS.escape(messageID)}"]`,
      );
      const textarea = form?.querySelector<HTMLTextAreaElement>("[data-chat-edit-input]");
      textarea?.focus();
      textarea?.setSelectionRange(textarea.value.length, textarea.value.length);
      return;
    }
    if (action === "cancel-message-edit") {
      const form = (event.currentTarget as HTMLElement).closest<HTMLFormElement>(
        "[data-chat-edit-form]",
      );
      const messageID = form?.dataset.messageId ?? "";
      if (messageID) {
        state.editingMessageIds.delete(messageID);
        getAppCallbacks().render();
      }
      return;
    }
    if (action === "attach-file") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      // Trigger the attachment toggle button to open the media picker menu.
      const toggleBtn = appRoot.querySelector<HTMLButtonElement>("[data-chat-attachment-toggle]");
      if (toggleBtn && !toggleBtn.disabled) {
        toggleBtn.click();
      }
      return;
    }
    if (action === "delete-workspace") {
      const workspace = state.appState?.workspaces.find((item) => item.id === workspaceID);
      if (!workspace || !window.confirm(`Delete ${workspace.displayName} from Echo?`)) {
        return;
      }
      await closeSelectedCardDetail(workspaceID);
      state.appState = await DeleteWorkspace(workspaceID);
      state.kanbanBoards.delete(workspaceID);
      state.taskBoards.delete(workspaceID);
      state.taskEditorDrafts.delete(workspaceID);
      state.showCompletedTaskWorkspaces.delete(workspaceID);
      state.changeReviews.delete(workspaceID);
      state.gitChangeReviews.delete(workspaceID);
      dropWorkspaceGitRepositoryState(workspaceID);
      state.selectedKanbanCards.delete(workspaceID);
      state.creatingKanbanCardWorkspaces.delete(workspaceID);
      state.kanbanCardCreationDrafts.delete(workspaceID);
      state.openChangeReviewWorkspaces.delete(workspaceID);
      state.openGitChangeWorkspaces.delete(workspaceID);
      state.expandedChatWorkspaces.delete(workspaceID);
      state.expandedKanbanWorkspaces.delete(workspaceID);
      state.expandedChangeReviewWorkspaces.delete(workspaceID);
      state.expandedGitChangeWorkspaces.delete(workspaceID);
      state.loadingGitChangeWorkspaces.delete(workspaceID);
      state.chatComposerModes.delete(workspaceID);
      state.chatPlanModes.delete(workspaceID);
      state.chatImageDrafts.delete(workspaceID);
      state.activeChatKanbanTab.delete(workspaceID);
      state.agentModes.delete(workspaceID);
      forgetKanbanRun(workspaceID);
      if (!activeWorkspace()) {
        state.appMode = "chat";
      } else {
        await loadActiveCodeViewIfNeeded();
      }
      pushToast("Workspace removed.", "success");
      getAppCallbacks().render();
    }
  } catch (error) {
    const message = errorMessage(error);
    if (state.settingsOpen) {
      state.formError = message;
    } else {
      state.formError = "";
      pushToast(message, "error");
    }
    getAppCallbacks().render();
  }
}

function findEchoSourceWorkspaceForAction() {
  const workspaces = state.appState?.workspaces ?? [];
  for (const workspace of workspaces) {
    const folders = workspace.folders ?? [];
    for (const folder of folders) {
      if (!folder.missing && folder.path && /[/\\]echo$/i.test(folder.path)) {
        return workspace;
      }
    }
  }
  return null;
}

async function loadActiveChangesViewIfNeeded() {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  if (state.appMode !== "git") {
    await refreshWorkspaceChangeReview(workspace.id);
    return;
  }
  await openWorkspaceGitRepository(workspace.id);
}

export function bindActionEvents(root: ParentNode) {
  root.querySelectorAll<HTMLElement>("[data-action]").forEach((element) => {
    element.addEventListener("click", handleAction);
  });
}
