
import { applyDebugEvent, applyInlineCodePromptEvent, ensureCodeViewRootLoaded, finishCodeTabSwitcher, openDroppedCodeFile, openWorkspaceCodeFileAtLine, refreshOpenCodeTabsFromDisk, saveActiveCodeFile, saveDirtyWorkspaceCodeTabs, setCodeGitChangeProvider } from "../codeView";
import { LoadRuntimeStatus, LoadState, LoadWebAccessStatus, ListAgentModes, ReadWorkspaceMediaFile } from "../backend/services";
import { llm, services } from "../../wailsjs/go/models";
import { EventsOn, OnFileDrop } from "../backend/runtime";
import { initializeWebAccessTokenFromURL, isWailsRuntime, webConnectionOn } from "../backend/web";
import { bindActionEvents } from "./actions";
import { setAppCallbacks } from "./callbacks";
import { bindChatEvents, applyChatStreamEvent, isSupportedChatImageType, isSupportedChatVideoType, patchChatControls, patchChatPanel } from "./chat";
import { applyFileChangesEvent, loadActiveChangeReview, refreshWorkspaceChangeReview } from "./changes";
import { showContextMenu } from "./contextMenu";
import { handleGlobalKeydown, handleGlobalKeyup, handleGlobalPointerDown, handleGlobalWindowBlur } from "./events";
import { applyKanbanEvent, applyHeartbeatEvent, applyLivenessEvent, applyWatchdogEvent, loadActiveKanbanBoard, markKanbanRunStarted } from "./kanban";
import { gitChangedLineNumbersForFile, gitChangeStateForPath } from "./git";
import { render } from "./render";
import { activeWorkspace, chatImageDraftsFor, chatSessionFor, chatVideoDraftsFor, cloneSettings, cloneWebAccessSettings, leadingWhitespaceIndicatorsEnabled, state, loadDashboardLayoutsFromBackend } from "./state";
import { applyTheme } from "./theme";
import { applyTaskEvent, loadActiveTaskBoard } from "./tasks";
import { pushToast } from "./toasts";
import type { ChatStreamEvent, FileChangesEvent, HeartbeatEvent, KanbanEvent, LivenessEvent, TaskEvent, WatchdogEvent } from "./types";
import { errorMessage } from "./utils";
import { loadActiveChatSession } from "./chat";
import type { CodeEntryKind } from "../codeView/types";
import type { DebugEvent } from "../codeView/debugTypes";
import { loadTokenBudget } from "./budget";
import { loadLivenessConfig } from "./liveness";

let realtimeResyncTimer = 0;

function scheduleWebRealtimeResync() {
  if (isWailsRuntime()) {
    return;
  }
  window.clearTimeout(realtimeResyncTimer);
  realtimeResyncTimer = window.setTimeout(() => {
    if (!activeWorkspace()) {
      return;
    }
    void Promise.allSettled([
      loadActiveChatSession(),
      loadActiveKanbanBoard(),
    ]);
  }, 100);
}

function codeViewCallbacks() {
  return {
    render,
    pushToast,
    errorMessage,
    leadingWhitespaceIndicatorsEnabled: () =>
      leadingWhitespaceIndicatorsEnabled(state.appState?.settings ?? state.settingsDraft),
    gitChangedLineNumbers: gitChangedLineNumbersForFile,
    gitChangeStateForPath,
    refreshGitChanges: refreshWorkspaceChangeReview,
    saveDirtyWorkspaceFiles: (workspaceID: string) =>
      saveDirtyWorkspaceCodeTabs(workspaceID, codeViewCallbacks()),
    openWorkspaceFileAtLine: (workspaceID: string, path: string, line: number) =>
      openWorkspaceCodeFileAtLine(workspaceID, path, line, codeViewCallbacks()),
    openDebugSettings() {
      const button = document.querySelector<HTMLButtonElement>('[data-action="open-settings"]');
      button?.click();
      window.setTimeout(() => {
        document.querySelector<HTMLElement>("#debug-settings-title")?.scrollIntoView({ block: "start" });
      }, 150);
    },
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
    try {
      const activeWS = state.appState?.activeWorkspaceId ?? "";
      const modes = await ListAgentModes(activeWS);
      if (activeWS) {
        state.agentModes.set(activeWS, modes);
      }
    } catch {
      /* Non-fatal: agent modes will load on first chat render. */
    }
    applyTheme(state.appState.settings);
    await loadActiveChatSession();
    await loadActiveKanbanBoard();
    await loadActiveTaskBoard();
    await loadActiveChangeReview();
    // Load persisted dashboard layouts from backend
    await loadDashboardLayoutsFromBackend();
    const activeWS = state.appState?.activeWorkspaceId ?? "";
    if (activeWS) {
      void loadTokenBudget(activeWS);
      void loadLivenessConfig(activeWS);
      await refreshWorkspaceChangeReview(activeWS);
    }
    const runtimeStatus = await LoadRuntimeStatus();
    for (const workspaceID of runtimeStatus.activeKanbanWorkspaceIds ?? []) {
      markKanbanRunStarted(workspaceID);
    }
    // Restore heartbeat intervals from backend for all workspaces
    if (state.appState?.heartbeatConfigs) {
      for (const [wsID, cfg] of Object.entries(state.appState.heartbeatConfigs)) {
        if (cfg.enabled && cfg.interval > 0) {
          state.heartbeatIntervals.set(wsID, cfg.interval);
        }
      }
    }
    // Restore watchdog intervals from backend for all workspaces
    if (state.appState?.watchdogConfigs) {
      for (const [wsID, cfg] of Object.entries(state.appState.watchdogConfigs)) {
        if (cfg.enabled && cfg.interval > 0) {
          state.watchdogIntervals.set(wsID, cfg.interval);
        }
      }
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

  if (!isWailsRuntime()) {
    webConnectionOn(scheduleWebRealtimeResync);
    document.addEventListener("visibilitychange", () => {
      if (document.visibilityState === "visible") {
        scheduleWebRealtimeResync();
      }
    });
    window.addEventListener("pageshow", scheduleWebRealtimeResync);
    window.addEventListener("online", scheduleWebRealtimeResync);
  }

  setAppCallbacks({
    render,
    pushToast,
    errorMessage,
    codeViewCallbacks,
    bindActionEvents,
    bindChatEvents,
  });
  setCodeGitChangeProvider(gitChangeStateForPath);

  OnFileDrop((_x, _y, paths) => {
    void openDroppedFiles(paths);
  });

  EventsOn("echo:chat:event", (event: ChatStreamEvent) => {
    applyChatStreamEvent(event);
  });

  EventsOn("echo:inline-code:event", (event) => {
    applyInlineCodePromptEvent(event);
  });

  EventsOn("echo:debug:event", (event: DebugEvent) => {
    applyDebugEvent(event);
  });

  EventsOn("echo:kanban:event", (event: KanbanEvent) => {
    applyKanbanEvent(event);
  });

  EventsOn("echo:file-changes:event", (event: FileChangesEvent) => {
    applyFileChangesEvent(event);
    void refreshWorkspaceChangeReview(event.workspaceId);
  });

  EventsOn("echo:heartbeat:event", (event: HeartbeatEvent) => {
    applyHeartbeatEvent(event);
  });

  EventsOn("echo:liveness:event", (event: LivenessEvent) => {
    applyLivenessEvent(event);
  });

  EventsOn("echo:watchdog:event", (event: WatchdogEvent) => {
    applyWatchdogEvent(event);
  });

  EventsOn("echo:tasks:event", (event: TaskEvent) => {
    applyTaskEvent(event);
  });

  EventsOn("echo:agent-mode:event", (modes) => {
    const wsID = state.appState?.activeWorkspaceId ?? "";
    if (wsID && Array.isArray(modes)) {
      state.agentModes.set(wsID, modes);
    }
    render();
  });

  document.addEventListener("keydown", handleGlobalKeydown, true);
  document.addEventListener("keyup", handleGlobalKeyup, true);
  document.addEventListener("pointerdown", handleGlobalPointerDown);
  window.addEventListener("blur", handleGlobalWindowBlur);

  void initialize();
}

async function openDroppedFiles(paths: string[]) {
  const workspace = activeWorkspace();
  if (!workspace) {
    pushToast("Select a workspace before dropping a file.", "error");
    return;
  }
  const uniquePaths = [...new Set(paths.map((path) => path.trim()).filter(Boolean))];
  if (uniquePaths.length === 0) {
    return;
  }

  // If in chat mode, try to attach media files to the composer instead of opening them.
  if (state.appMode === "chat" && !chatSessionFor(workspace.id).busy && !state.executingPlans.has(workspace.id)) {
    const imagePaths: string[] = [];
    const videoPaths: string[] = [];
    const otherPaths: string[] = [];

    for (const path of uniquePaths) {
      const ext = path.split(".").pop()?.toLowerCase() ?? "";
      if (["png", "jpg", "jpeg", "gif", "webp"].includes(ext)) {
        imagePaths.push(path);
      } else if (["mp4", "webm", "mov", "m4v"].includes(ext)) {
        videoPaths.push(path);
      } else {
        otherPaths.push(path);
      }
    }

    // If all dropped files are media, attach them to chat.
    if (otherPaths.length === 0 && (imagePaths.length > 0 || videoPaths.length > 0)) {
      const session = chatSessionFor(workspace.id);
      if (session.busy) return;

      for (const path of imagePaths) {
        try {
          const result = await ReadWorkspaceMediaFile(workspace.id, path);
          if (!result || !isSupportedChatImageType(result.mimeType || "")) {
            pushToast(`Unsupported image format for ${path.split(/[\\/]/).pop()}`, "error");
            continue;
          }
          const name = path.split(/[\\/]/).pop() ?? "image";
          state.chatImageDrafts.set(workspace.id, [
            ...chatImageDraftsFor(workspace.id),
            {
              id: `draft-${Date.now()}-${Math.random().toString(36).slice(2)}`,
              name,
              mediaType: result.mimeType,
              dataUrl: result.dataUrl,
              bytes: result.bytes,
            },
          ]);
          patchChatPanel();
          patchChatControls();
        } catch {
          pushToast(`Could not read image: ${path.split(/[\\/]/).pop()}`, "error");
        }
      }

      for (const path of videoPaths) {
        try {
          const result = await ReadWorkspaceMediaFile(workspace.id, path);
          if (!result || !isSupportedChatVideoType(result.mimeType || "")) {
            pushToast(`Unsupported video format for ${path.split(/[\\/]/).pop()}`, "error");
            continue;
          }
          const name = path.split(/[\\/]/).pop() ?? "video";
          state.chatVideoDrafts.set(workspace.id, [
            ...chatVideoDraftsFor(workspace.id),
            {
              id: `draft-${Date.now()}-${Math.random().toString(36).slice(2)}`,
              name,
              mediaType: result.mimeType,
              dataUrl: result.dataUrl,
              bytes: result.bytes,
            },
          ]);
          patchChatPanel();
          patchChatControls();
        } catch {
          pushToast(`Could not read video: ${path.split(/[\\/]/).pop()}`, "error");
        }
      }

      return;
    }
  }

  // Fall through to code view for non-media files or mixed drops.
  state.appMode = "code";
  const loading = ensureCodeViewRootLoaded(workspace.id);
  render();
  await loading;
  for (const path of uniquePaths) {
    await openDroppedCodeFile(workspace.id, path, codeViewCallbacks());
  }
}
