
import { clearCodeTabSwitcher, ensureCodeViewRootLoaded, refreshOpenCodeTabsFromDisk, startCodeCreate, startCodeRename } from "../codeView";
import { ChooseWorkspaceFolder, ChooseWorkspaceFolderForWorkspace, ChooseWorkspaceIcon, ClearChat, ClearDoneKanbanCards, ClearWorkspaceChangeReview, ClearWorkspaceIcon, CloseKanbanCardDetail, CreateKanbanCardFromChatMessage, CreateSkillFromChat, DeleteKanbanCard, DeleteWorkspace, ExecutePlan, LoadState, LoadWebAccessStatus, LoadWorkspaceChangeReview, MoveKanbanCard, OpenKanbanCardDetail, OpenWorkspaceExplorer, OpenWorkspacePathExplorer, PrepareRebuildAndRelaunch, PruneChatMessage, RemoveWorkspaceFolder, ResetKanbanCard, RetryChatMessage, RotateWebAccessToken, SetActiveWorkspace, StartKanbanExecution, StopChatStream, StopKanbanCard, StopKanbanExecution } from "../backend/services";
import { appRoot } from "./dom";
import { getAppCallbacks } from "./callbacks";
import { loadActiveChangeReview, refreshWorkspaceChangeReview, scrollChangeReview } from "./changes";
import { loadActiveCodeViewIfNeeded } from "./codeViewBridge";
import { dismissContextMenu } from "./contextMenu";
import { dropWorkspaceGitRepositoryState, openGitChangeInCode, openWorkspaceGitRepository, refreshWorkspaceGitRepository, revertWorkspaceGitChanges, revertWorkspaceGitFile, selectGitCommit, syncWorkspaceGitRepository } from "./git";
import { closeSelectedCardDetail, finishKanbanRun, forgetKanbanRun, loadActiveKanbanBoard, markKanbanRunStarted, maybePlayKanbanBoardNotification } from "./kanban";
import { playNotificationSound } from "./notifications";
import { addLLMEndpoint, deleteLLMEndpoint, editLLMEndpoint, finishEditingLLMEndpoint } from "./settings";
import { activeWorkspace, chatImageDraftsFor, chatPlanModeFor, chatSessionFor, chatVideoDraftsFor, kanbanBoardFor, kanbanCards, limitKanbanConcurrencyEnabled, state } from "./state";
import { clearChatMention, loadActiveChatSession, patchChatControls, patchChatPanel, scrollChatToBottom } from "./chat";
import { cloneSettings, cloneWebAccessSettings } from "./state";
import type { AppMode, MobileNavView } from "./types";
import { applyTheme, settingsWithThemeDefaults, themePaletteNames } from "./theme";
import { pushToast, dismissToast } from "./toasts";
import { copyTextToClipboard, errorMessage, laneLabel } from "./utils";
import { hydrateWorkspaceLetterDrafts } from "./workspace";

export async function handleAction(event: Event) {
  const target = event.currentTarget as HTMLElement;
  const action = target.dataset.action;
  const workspaceID = target.dataset.workspaceId ?? "";

  try {
    if (action === "dismiss-toast") {
      dismissToast(target.dataset.toastId ?? "");
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
      state.appMode = "chat";
      state.mobileNavView = "chat";
      getAppCallbacks().render();
      return;
    }
    if (action === "open-git-changes") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
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
      state.mobileNavView = "kanban";
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
    if (action === "add-workspace") {
      state.appState = await ChooseWorkspaceFolder();
      await loadActiveChatSession();
      await loadActiveKanbanBoard();
      await loadActiveChangeReview();
      await loadActiveCodeViewIfNeeded();
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
      pushToast("Workspace folder removed.", "success");
      getAppCallbacks().render();
      return;
    }
    if (action === "refresh-workspaces") {
      state.appState = await LoadState();
      await loadActiveChatSession();
      await loadActiveKanbanBoard();
      await loadActiveChangeReview();
      await loadActiveCodeViewIfNeeded();
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
      if (view === "chat" || view === "kanban") {
        state.appMode = view as AppMode;
        state.mobileNavView = view as MobileNavView;
        const workspace = activeWorkspace();
        if (workspace) {
          state.activeChatKanbanTab.set(workspace.id, view);
        }
      } else if (view === "code") {
        state.appMode = "code";
        state.mobileNavView = "code";
        const workspace = activeWorkspace();
        if (workspace) {
          state.openGitChangeWorkspaces.delete(workspace.id);
        }
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
      await loadActiveChangeReview();
      await loadActiveCodeViewIfNeeded();
      state.workspaceDropdownOpen = false;
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
      } finally {
        state.executingPlans.delete(workspace.id);
      }
      getAppCallbacks().render();
    }
    if (action === "toggle-chat-size") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      if (state.expandedChatWorkspaces.has(workspace.id)) {
        state.expandedChatWorkspaces.delete(workspace.id);
      } else {
        state.expandedChatWorkspaces.add(workspace.id);
        state.expandedKanbanWorkspaces.delete(workspace.id);
      }
      getAppCallbacks().render();
    }
    if (action === "toggle-kanban-size") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      if (state.expandedKanbanWorkspaces.has(workspace.id)) {
        state.expandedKanbanWorkspaces.delete(workspace.id);
      } else {
        state.expandedKanbanWorkspaces.add(workspace.id);
        state.expandedChatWorkspaces.delete(workspace.id);
      }
      getAppCallbacks().render();
    }
    if (action === "set-chat-kanban-tab") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      const tab = target.dataset.tab;
      if (tab === "chat" || tab === "kanban") {
        state.activeChatKanbanTab.set(workspace.id, tab);
      }
      getAppCallbacks().render();
    }
    if (action === "open-change-review") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      await closeSelectedCardDetail(workspace.id);
      state.changeReviews.set(workspace.id, await LoadWorkspaceChangeReview(workspace.id));
      state.openChangeReviewWorkspaces.add(workspace.id);
      getAppCallbacks().render();
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
        state.chatSessions.set(
          workspace.id,
          await RetryChatMessage(workspace.id, messageID, chatPlanModeFor(workspace.id)),
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
      state.chatSessions.set(workspace.id, await StopChatStream(workspace.id));
      patchChatPanel();
    }
    if (action === "create-chat-skill") {
      const workspace = activeWorkspace();
      if (!workspace || state.creatingChatSkills.has(workspace.id)) {
        return;
      }
      state.creatingChatSkills.add(workspace.id);
      patchChatPanel();
      try {
        const skill = await CreateSkillFromChat(workspace.id);
        pushToast(`Created skill ${skill.id}.`, "success");
      } finally {
        state.creatingChatSkills.delete(workspace.id);
        patchChatPanel();
      }
      return;
    }
    if (action === "clear-chat") {
      const workspace = activeWorkspace();
      if (!workspace || !window.confirm("Clear the current chat?")) {
        return;
      }
      state.chatSessions.set(workspace.id, await ClearChat(workspace.id));
      state.chatDrafts.set(workspace.id, "");
      state.chatImageDrafts.delete(workspace.id);
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
      state.chatSessions.set(workspace.id, await PruneChatMessage(workspace.id, messageID));
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
    if (action === "delete-workspace") {
      const workspace = state.appState?.workspaces.find((item) => item.id === workspaceID);
      if (!workspace || !window.confirm(`Delete ${workspace.displayName} from Echo?`)) {
        return;
      }
      await closeSelectedCardDetail(workspaceID);
      state.appState = await DeleteWorkspace(workspaceID);
      state.kanbanBoards.delete(workspaceID);
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
      state.chatPlanModes.delete(workspaceID);
      state.chatImageDrafts.delete(workspaceID);
      state.activeChatKanbanTab.delete(workspaceID);
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

export function bindActionEvents(root: ParentNode) {
  root.querySelectorAll<HTMLElement>("[data-action]").forEach((element) => {
    element.addEventListener("click", handleAction);
  });
}
