import { SearchWorkspaceFiles } from "../backend/services";
import { services } from "../../wailsjs/go/models";
import { patchQuickOpen } from "./dom";
import { ensureCodeState } from "./state";
import { openPinnedCodeFile } from "./tabs";
import type { CodeViewCallbacks } from "./types";
import { clamp } from "./utils";

const quickOpenSearchDelayMs = 120;

export function openQuickOpen(workspaceID: string, callbacks: CodeViewCallbacks) {
  const state = ensureCodeState(workspaceID);
  clearQuickOpenTimer(workspaceID);
  state.quickOpen.open = true;
  state.quickOpen.query = "";
  state.quickOpen.results = [];
  state.quickOpen.loading = false;
  state.quickOpen.truncated = false;
  state.quickOpen.selectedIndex = 0;
  state.quickOpen.requestSeq++;
  callbacks.render();
  focusQuickOpenInput();
}

export function closeQuickOpen(workspaceID: string, callbacks: CodeViewCallbacks) {
  const state = ensureCodeState(workspaceID);
  if (!state.quickOpen.open) {
    return;
  }
  clearQuickOpenTimer(workspaceID);
  state.quickOpen.open = false;
  state.quickOpen.query = "";
  state.quickOpen.results = [];
  state.quickOpen.loading = false;
  state.quickOpen.truncated = false;
  state.quickOpen.selectedIndex = 0;
  state.quickOpen.requestSeq++;
  callbacks.render();
}

export function handleQuickOpenInput(
  workspaceID: string,
  input: HTMLInputElement,
  callbacks: CodeViewCallbacks,
) {
  const state = ensureCodeState(workspaceID);
  state.quickOpen.query = input.value;
  state.quickOpen.selectedIndex = 0;
  state.quickOpen.requestSeq++;
  clearQuickOpenTimer(workspaceID);
  if (!state.quickOpen.query.trim()) {
    state.quickOpen.results = [];
    state.quickOpen.loading = false;
    state.quickOpen.truncated = false;
    patchQuickOpen(workspaceID, callbacks);
    return;
  }
  state.quickOpen.loading = true;
  state.quickOpen.truncated = false;
  patchQuickOpen(workspaceID, callbacks);
  state.quickOpen.timerID = window.setTimeout(() => {
    void runQuickOpenSearch(workspaceID, callbacks);
  }, quickOpenSearchDelayMs);
}

export function moveQuickOpenSelection(
  workspaceID: string,
  direction: -1 | 1,
  callbacks: CodeViewCallbacks,
) {
  const quickOpen = ensureCodeState(workspaceID).quickOpen;
  if (!quickOpen.open || quickOpen.results.length === 0) {
    return;
  }
  quickOpen.selectedIndex = wrapQuickOpenIndex(
    quickOpen.selectedIndex + direction,
    quickOpen.results.length,
  );
  patchQuickOpen(workspaceID, callbacks);
}

export async function openQuickOpenSelection(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
  path?: string,
) {
  const state = ensureCodeState(workspaceID);
  const selectedPath = path || state.quickOpen.results[state.quickOpen.selectedIndex]?.path || "";
  if (!selectedPath) {
    return;
  }
  clearQuickOpenTimer(workspaceID);
  state.quickOpen.open = false;
  state.quickOpen.query = "";
  state.quickOpen.results = [];
  state.quickOpen.loading = false;
  state.quickOpen.truncated = false;
  state.quickOpen.selectedIndex = 0;
  state.quickOpen.requestSeq++;
  const opened = await openPinnedCodeFile(workspaceID, selectedPath, callbacks);
  if (!opened) {
    callbacks.render();
  }
}

async function runQuickOpenSearch(
  workspaceID: string,
  callbacks: CodeViewCallbacks,
) {
  const state = ensureCodeState(workspaceID);
  const query = state.quickOpen.query.trim();
  const sequence = state.quickOpen.requestSeq;
  if (!query) {
    state.quickOpen.loading = false;
    state.quickOpen.results = [];
    state.quickOpen.truncated = false;
    patchQuickOpen(workspaceID, callbacks);
    return;
  }
  try {
    const result = await SearchWorkspaceFiles(workspaceID, query, false);
    if (sequence !== state.quickOpen.requestSeq) {
      return;
    }
    const model = services.WorkspaceFileSearchResult.createFrom(result);
    state.quickOpen.results = (model.entries ?? []).filter((entry) => entry.kind === "file");
    state.quickOpen.truncated = model.truncated;
    state.quickOpen.selectedIndex = clamp(
      state.quickOpen.selectedIndex,
      0,
      Math.max(0, state.quickOpen.results.length - 1),
    );
  } catch (error) {
    if (sequence === state.quickOpen.requestSeq) {
      callbacks.pushToast(callbacks.errorMessage(error), "error");
      state.quickOpen.results = [];
      state.quickOpen.truncated = false;
    }
  } finally {
    if (sequence === state.quickOpen.requestSeq) {
      state.quickOpen.loading = false;
      state.quickOpen.timerID = null;
      patchQuickOpen(workspaceID, callbacks);
    }
  }
}

function clearQuickOpenTimer(workspaceID: string) {
  const quickOpen = ensureCodeState(workspaceID).quickOpen;
  if (quickOpen.timerID !== null) {
    window.clearTimeout(quickOpen.timerID);
    quickOpen.timerID = null;
  }
}

function focusQuickOpenInput() {
  window.setTimeout(() => {
    const input = document.querySelector<HTMLInputElement>("[data-code-quick-open-input]");
    input?.focus();
    input?.select();
  }, 0);
}

function wrapQuickOpenIndex(index: number, length: number) {
  if (length <= 0) {
    return 0;
  }
  return ((index % length) + length) % length;
}
