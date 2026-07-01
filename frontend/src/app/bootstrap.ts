
import { applyInlineCodePromptEvent, ensureCodeViewRootLoaded, finishCodeTabSwitcher, openDroppedCodeFile, refreshOpenCodeTabsFromDisk, saveActiveCodeFile } from "../codeView";
import { LoadRuntimeStatus, LoadState, LoadWebAccessStatus } from "../backend/services";
import { llm, services } from "../../wailsjs/go/models";
import { EventsOn, OnFileDrop } from "../backend/runtime";
import { initializeWebAccessTokenFromURL } from "../backend/web";
import { bindActionEvents } from "./actions";
import { setAppCallbacks } from "./callbacks";
import { bindChatEvents, applyChatStreamEvent } from "./chat";
import { applyFileChangesEvent } from "./changes";
import { showContextMenu } from "./contextMenu";
import { handleGlobalKeydown, handleGlobalKeyup, handleGlobalPointerDown, handleGlobalWindowBlur } from "./events";
import { applyKanbanEvent, loadActiveKanbanBoard, markKanbanRunStarted } from "./kanban";
import { render } from "./render";
import { activeWorkspace, cloneSettings, cloneWebAccessSettings, leadingWhitespaceIndicatorsEnabled, state } from "./state";
import { applyTheme } from "./theme";
import { pushToast } from "./toasts";
import type { ChatStreamEvent, FileChangesEvent, KanbanEvent } from "./types";
import { errorMessage } from "./utils";
import { loadActiveChatSession } from "./chat";
import { loadActiveChangeReview } from "./changes";
import type { CodeEntryKind } from "../codeView/types";

function codeViewCallbacks() {
  return {
    render,
    pushToast,
    errorMessage,
    leadingWhitespaceIndicatorsEnabled: () =>
      leadingWhitespaceIndicatorsEnabled(state.appState?.settings ?? state.settingsDraft),
    showCodePathContextMenu(
      workspaceId: string,
      path: string,
      kind: CodeEntryKind,
      label: string,
      x: number,
      y: number,
    ) {
      showContextMenu({
        workspaceId,
        workspacePath: path,
        codePath: path,
        codeKind: kind,
        displayPath: label || path,
        x,
        y,
      });
    },
  };
}

async function initialize() {
  try {
    state.appState = await LoadState();
    state.settingsDraft = cloneSettings(state.appState.settings);
    state.webAccessDraft = cloneWebAccessSettings(state.appState.webAccess);
    state.webAccessStatus = await LoadWebAccessStatus();
    applyTheme(state.appState.settings);
    await loadActiveChatSession();
    await loadActiveKanbanBoard();
    await loadActiveChangeReview();
    const runtimeStatus = await LoadRuntimeStatus();
    for (const workspaceID of runtimeStatus.activeKanbanWorkspaceIds ?? []) {
      markKanbanRunStarted(workspaceID);
    }
  } catch (error) {
    state.appState = services.AppState.createFrom({
      settings: llm.Settings.createFrom({ endpoint: "", model: "" }),
      webAccess: services.WebAccessSettings.createFrom({ enabled: false, bindHost: "0.0.0.0", port: 3740, accessToken: "" }),
      workspaces: [],
      activeWorkspaceId: "",
    });
    state.settingsDraft = cloneSettings(state.appState.settings);
    state.webAccessDraft = cloneWebAccessSettings(state.appState.webAccess);
    applyTheme(state.appState.settings);
    pushToast(errorMessage(error), "error");
  }
  render();
}

export function startApp() {
  initializeWebAccessTokenFromURL();

  setAppCallbacks({
    render,
    pushToast,
    errorMessage,
    codeViewCallbacks,
    bindActionEvents,
    bindChatEvents,
  });

  OnFileDrop((_x, _y, paths) => {
    void openDroppedTextFiles(paths);
  });

  EventsOn("echo:chat:event", (event: ChatStreamEvent) => {
    applyChatStreamEvent(event);
  });

  EventsOn("echo:inline-code:event", (event) => {
    applyInlineCodePromptEvent(event);
  });

  EventsOn("echo:kanban:event", (event: KanbanEvent) => {
    applyKanbanEvent(event);
  });

  EventsOn("echo:file-changes:event", (event: FileChangesEvent) => {
    applyFileChangesEvent(event);
  });

  document.addEventListener("keydown", handleGlobalKeydown, true);
  document.addEventListener("keyup", handleGlobalKeyup, true);
  document.addEventListener("pointerdown", handleGlobalPointerDown);
  window.addEventListener("blur", handleGlobalWindowBlur);

  void initialize();
}

async function openDroppedTextFiles(paths: string[]) {
  const workspace = activeWorkspace();
  if (!workspace) {
    pushToast("Select a workspace before dropping a text file.", "error");
    return;
  }
  const uniquePaths = [...new Set(paths.map((path) => path.trim()).filter(Boolean))];
  if (uniquePaths.length === 0) {
    return;
  }

  state.appMode = "code";
  const loading = ensureCodeViewRootLoaded(workspace.id);
  render();
  await loading;
  for (const path of uniquePaths) {
    await openDroppedCodeFile(workspace.id, path, codeViewCallbacks());
  }
}
