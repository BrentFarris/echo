
import { applyInlineCodePromptEvent, finishCodeTabSwitcher, refreshOpenCodeTabsFromDisk, saveActiveCodeFile } from "../codeView";
import { LoadState } from "../../wailsjs/go/services/SystemService";
import { llm, services } from "../../wailsjs/go/models";
import { EventsOn } from "../../wailsjs/runtime/runtime";
import { bindActionEvents } from "./actions";
import { setAppCallbacks } from "./callbacks";
import { bindChatEvents, applyChatStreamEvent } from "./chat";
import { applyFileChangesEvent } from "./changes";
import { showContextMenu } from "./contextMenu";
import { handleGlobalKeydown, handleGlobalKeyup, handleGlobalPointerDown, handleGlobalWindowBlur } from "./events";
import { applyKanbanEvent, loadActiveKanbanBoard } from "./kanban";
import { render } from "./render";
import { cloneSettings, leadingWhitespaceIndicatorsEnabled, state } from "./state";
import { pushToast } from "./toasts";
import type { ChatStreamEvent, FileChangesEvent, KanbanEvent } from "./types";
import { errorMessage } from "./utils";
import { loadActiveChatSession } from "./chat";
import { loadActiveChangeReview } from "./changes";

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
      label: string,
      x: number,
      y: number,
    ) {
      showContextMenu({
        workspaceId,
        workspacePath: path,
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
    await loadActiveChatSession();
    await loadActiveKanbanBoard();
    await loadActiveChangeReview();
  } catch (error) {
    state.appState = services.AppState.createFrom({
      settings: llm.Settings.createFrom({ endpoint: "", model: "" }),
      workspaces: [],
      activeWorkspaceId: "",
    });
    state.settingsDraft = cloneSettings(state.appState.settings);
    pushToast(errorMessage(error), "error");
  }
  render();
}

export function startApp() {
  setAppCallbacks({
    render,
    pushToast,
    errorMessage,
    codeViewCallbacks,
    bindActionEvents,
    bindChatEvents,
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
