import "./styles.css";
import {
  applyInlineCodePromptEvent,
  bindCodeViewEvents,
  clearCodeTabSwitcher,
  destroyCodeEditor,
  ensureCodeViewRootLoaded,
  finishCodeTabSwitcher,
  handleCodeTabSwitcherKeydown,
  openWorkspaceCodeFile,
  refreshOpenCodeTabsFromDisk,
  renderCodeView,
  saveActiveCodeFile,
} from "./codeView";
import {
  elementFromHtml,
  morphElement,
  patchChildrenFromHtml,
  patchMarkdownElement,
  renderMarkdown,
} from "./markdown";
import {
  ChooseWorkspaceFolder,
  ChooseWorkspaceIcon,
  CloseKanbanCardDetail,
  ClearDoneKanbanCards,
  ClearWorkspaceChangeReview,
  ClearChat,
  ClearWorkspaceIcon,
  DeleteWorkspace,
  ExecutePlan,
  AddKanbanCardMessage,
  CreateKanbanCardFromChatMessage,
  LoadChatSession,
  LoadKanbanBoard,
  LoadState,
  LoadWorkspaceChangeReview,
  LoadWorkspaceGitChanges,
  MoveKanbanCard,
  OpenKanbanCardDetail,
  OpenWorkspaceExplorer,
  ResetKanbanCard,
  ResolveWorkspaceTextFilePath,
  SaveSettings,
  SearchWorkspaceFiles,
  SendChatMessageWithAttachments,
  SetActiveWorkspace,
  SetWorkspaceLetter,
  StartKanbanExecution,
  StopKanbanCard,
  StopKanbanExecution,
  StopChatStream,
  RetryChatMessage,
  EditChatMessage,
  UpdateKanbanCardDescription,
} from "../wailsjs/go/services/SystemService";
import { llm, services } from "../wailsjs/go/models";
import { EventsOn } from "../wailsjs/runtime/runtime";

const app = document.querySelector<HTMLDivElement>("#app");

if (!app) {
  throw new Error("Echo app root was not found.");
}

const appRoot = app;

const icons = {
  plus: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14"/></svg>`,
  settings: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 15.5a3.5 3.5 0 1 0 0-7 3.5 3.5 0 0 0 0 7Z"/><path d="M19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 0 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.6v.3a2 2 0 0 1-4 0V21a1.7 1.7 0 0 0-1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1A2 2 0 0 1 4.2 17l.1-.1a1.7 1.7 0 0 0 .3-1.9 1.7 1.7 0 0 0-1.6-1H2.7a2 2 0 0 1 0-4H3a1.7 1.7 0 0 0 1.6-1 1.7 1.7 0 0 0-.3-1.9L4.2 7A2 2 0 0 1 7 4.2l.1.1A1.7 1.7 0 0 0 9 4.6 1.7 1.7 0 0 0 10 3V2.7a2 2 0 0 1 4 0V3a1.7 1.7 0 0 0 1 1.6 1.7 1.7 0 0 0 1.9-.3l.1-.1A2 2 0 0 1 19.8 7l-.1.1a1.7 1.7 0 0 0-.3 1.9 1.7 1.7 0 0 0 1.6 1h.3a2 2 0 0 1 0 4H21a1.7 1.7 0 0 0-1.6 1Z"/></svg>`,
  refresh: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M21 12a9 9 0 0 1-15 6.7L3 16"/><path d="M3 21v-5h5"/><path d="M3 12a9 9 0 0 1 15-6.7L21 8"/><path d="M21 3v5h-5"/></svg>`,
  send: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m22 2-7 20-4-9-9-4Z"/><path d="M22 2 11 13"/></svg>`,
  stop: `<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="6" y="6" width="12" height="12" rx="1"/></svg>`,
  execute: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m8 5 11 7-11 7Z"/><path d="M4 5v14"/></svg>`,
  kanban: `<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="3" y="4" width="5" height="16" rx="1"/><rect x="10" y="4" width="4" height="11" rx="1"/><rect x="16" y="4" width="5" height="14" rx="1"/></svg>`,
  file: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M14 2H6a2 2 0 0 0-2 2v16a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V8Z"/><path d="M14 2v6h6"/></svg>`,
  image: `<svg viewBox="0 0 24 24" aria-hidden="true"><rect x="3" y="3" width="18" height="18" rx="2"/><circle cx="8.5" cy="8.5" r="1.5"/><path d="m21 15-5-5L5 21"/></svg>`,
  trash: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="m19 6-1 14H6L5 6"/><path d="M10 11v5M14 11v5"/></svg>`,
  expand: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M15 3h6v6"/><path d="m21 3-7 7"/><path d="M9 21H3v-6"/><path d="m3 21 7-7"/></svg>`,
  collapse: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 3v5H3"/><path d="m3 8 6-6"/><path d="M16 21v-5h5"/><path d="m21 16-6 6"/></svg>`,
  code: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m16 18 6-6-6-6"/><path d="m8 6-6 6 6 6"/></svg>`,
  arrowUp: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m18 15-6-6-6 6"/></svg>`,
  arrowDown: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m6 9 6 6 6-6"/></svg>`,
  check: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg>`,
  x: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M18 6 6 18M6 6l12 12"/></svg>`,
  edit: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M17 3a2.85 2.85 0 0 1 4 4L7.5 20.5 2 22l1.5-5.5Z"/><path d="m15 5 4 4"/></svg>`,
  retry: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M21 12a9 9 0 0 1-15 6.7L3 16"/><path d="M3 21v-5h5"/><path d="M3 12a9 9 0 0 1 15-6.7L21 8"/><path d="M21 3v5h-5"/></svg>`,
};

type AppMode = "chat-kanban" | "code";

let appState: services.AppState | null = null;
let settingsDraft: llm.Settings | null = null;
let settingsOpen = false;
const workspaceLetterDrafts = new Map<string, string>();
let appMode: AppMode = "chat-kanban";
let formError = "";
const chatSessions = new Map<string, services.ChatSession>();
const chatDrafts = new Map<string, string>();
const chatImageDrafts = new Map<string, ChatImageDraft[]>();
const chatPlanModes = new Map<string, boolean>();
const chatFileLinkCache = new Map<string, Promise<string | null>>();
let chatMention: ChatMentionState | null = null;
const kanbanBoards = new Map<string, services.KanbanBoard>();
const changeReviews = new Map<string, services.WorkspaceChangeReview>();
const gitChangeReviews = new Map<string, services.WorkspaceGitChangeReview>();
const executingPlans = new Set<string>();
const runningKanbanWorkspaces = new Set<string>();
const kanbanRunStarts = new Map<string, number>();
const kanbanRunElapsed = new Map<string, number>();
const selectedKanbanCards = new Map<string, string>();
const openChangeReviewWorkspaces = new Set<string>();
const openGitChangeWorkspaces = new Set<string>();
const cardMessageDrafts = new Map<string, string>();
const expandedChatWorkspaces = new Set<string>();
const expandedKanbanWorkspaces = new Set<string>();
const editingMessageIds = new Set<string>();
const expandedChangeReviewWorkspaces = new Set<string>();
const expandedGitChangeWorkspaces = new Set<string>();
const loadingGitChangeWorkspaces = new Set<string>();
let toastSeq = 0;
let toasts: Toast[] = [];
let kanbanTimerID: number | null = null;
type ContextMenuState = {
  workspaceId: string;
  folderPath: string;
  x: number;
  y: number;
};
let contextMenu: ContextMenuState | null = null;

const kanbanLaneLabels: Record<string, string> = {
  ready: "Ready",
  inProgress: "In Progress",
  blocked: "Blocked",
  done: "Done",
};

type ChatStreamEvent = {
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

type KanbanEvent = {
  workspaceId: string;
  cardId?: string;
  type: string;
  board: services.KanbanBoard;
};

type FileChangesEvent = {
  workspaceId: string;
  type: string;
  fileCount: number;
  changeCount: number;
};

type Toast = {
  id: string;
  tone: "info" | "success" | "error";
  message: string;
};

type ChatMentionState = {
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

type ChatImageDraft = {
  id: string;
  name: string;
  mediaType: string;
  dataUrl: string;
  bytes: number;
};

type ScrollSnapshot = {
  scrollTop: number;
  atBottom: boolean;
};

const scrollStickinessThreshold = 48;
const chatMentionSearchDelay = 160;
const chatMentionResultLimit = 8;
const maxChatImageDrafts = 4;
const maxChatImageBytes = 10 * 1024 * 1024;
const maxChatImageMessageBytes = 20 * 1024 * 1024;
const supportedChatImageTypes = new Set(["image/png", "image/jpeg", "image/webp", "image/gif"]);

function cloneSettings(settings: llm.Settings): llm.Settings {
  return llm.Settings.createFrom(JSON.parse(JSON.stringify(settings)));
}

function activeWorkspace(): services.Workspace | null {
  if (!appState) {
    return null;
  }
  const workspaces = appState.workspaces ?? [];
  return (
    workspaces.find(
      (workspace) => workspace.id === appState?.activeWorkspaceId,
    ) ?? null
  );
}

function escapeHtml(value: string): string {
  return value
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#039;");
}

function escapeAttribute(value: string): string {
  return escapeHtml(value).replaceAll("`", "&#096;");
}

function fileName(path: string): string {
  return path.split("/").pop() || path;
}

function formatBytes(bytes: number): string {
  if (bytes < 1024) {
    return `${bytes} B`;
  }
  if (bytes < 1024 * 1024) {
    return `${(bytes / 1024).toFixed(bytes < 10 * 1024 ? 1 : 0)} KB`;
  }
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

function chatImageDraftsFor(workspaceID: string): ChatImageDraft[] {
  return chatImageDrafts.get(workspaceID) ?? [];
}

function chatImageDraftTotalBytes(workspaceID: string): number {
  return chatImageDraftsFor(workspaceID).reduce((total, image) => total + image.bytes, 0);
}

function isSupportedChatImageType(mediaType: string): boolean {
  return supportedChatImageTypes.has(mediaType.toLowerCase());
}

function fileToDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.addEventListener("load", () => resolve(String(reader.result ?? "")));
    reader.addEventListener("error", () => reject(reader.error ?? new Error("Unable to read pasted image.")));
    reader.readAsDataURL(file);
  });
}

function chatSessionFor(workspaceID: string): services.ChatSession {
  return (
    chatSessions.get(workspaceID) ??
    services.ChatSession.createFrom({
      workspaceId: workspaceID,
      messages: [],
      busy: false,
    })
  );
}

function chatPlanModeFor(workspaceID: string): boolean {
  return chatPlanModes.get(workspaceID) ?? true;
}

function kanbanBoardFor(workspaceID: string): services.KanbanBoard {
  return (
    kanbanBoards.get(workspaceID) ??
    services.KanbanBoard.createFrom({
      workspaceId: workspaceID,
      ready: [],
      inProgress: [],
      blocked: [],
      done: [],
    })
  );
}

function changeReviewFor(workspaceID: string): services.WorkspaceChangeReview {
  return (
    changeReviews.get(workspaceID) ??
    services.WorkspaceChangeReview.createFrom({
      workspaceId: workspaceID,
      fileCount: 0,
      changeCount: 0,
      files: [],
      changes: [],
    })
  );
}

function gitChangeReviewFor(workspaceID: string): services.WorkspaceGitChangeReview {
  return (
    gitChangeReviews.get(workspaceID) ??
    services.WorkspaceGitChangeReview.createFrom({
      workspaceId: workspaceID,
      fileCount: 0,
      files: [],
    })
  );
}

function kanbanCards(board: services.KanbanBoard): services.KanbanCard[] {
  return [
    ...(board.ready ?? []),
    ...(board.inProgress ?? []),
    ...(board.blocked ?? []),
    ...(board.done ?? []),
  ];
}

function selectedKanbanCard(board: services.KanbanBoard): services.KanbanCard | null {
  const selectedID = selectedKanbanCards.get(board.workspaceId);
  return kanbanCards(board).find((card) => card.id === selectedID) ?? null;
}

function laneLabel(lane = "ready"): string {
  return kanbanLaneLabels[lane] ?? "Ready";
}

function changeOperationLabel(operation = ""): string {
  switch (operation) {
    case "created":
      return "Created";
    case "deleted":
      return "Deleted";
    case "edited":
      return "Edited";
    case "renamed":
      return "Renamed";
    case "copied":
      return "Copied";
    case "conflicted":
      return "Conflicted";
    default:
      return operation || "Changed";
  }
}

function changeSourceLabel(source: services.WorkspaceChangeSource): string {
  if (source.type === "kanban") {
    return `Kanban ${source.cardTitle || source.cardId || "card"}`;
  }
  if (source.type === "inline") {
    return "Inline code";
  }
  if (source.type === "chat") {
    return "Chat";
  }
  return source.type || "AI";
}

function formatElapsedTime(milliseconds: number): string {
  const totalSeconds = Math.max(0, Math.floor(milliseconds / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  const paddedSeconds = String(seconds).padStart(2, "0");
  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, "0")}:${paddedSeconds}`;
  }
  return `${minutes}:${paddedSeconds}`;
}

function kanbanElapsedLabel(workspaceID: string, now = Date.now()): string {
  const startedAt = kanbanRunStarts.get(workspaceID);
  if (startedAt) {
    return formatElapsedTime(now - startedAt);
  }
  const elapsed = kanbanRunElapsed.get(workspaceID);
  return elapsed === undefined ? "0:00" : formatElapsedTime(elapsed);
}

function hasKanbanRuntime(workspaceID: string): boolean {
  return kanbanRunStarts.has(workspaceID) || kanbanRunElapsed.has(workspaceID);
}

function markKanbanRunStarted(workspaceID: string) {
  if (!kanbanRunStarts.has(workspaceID)) {
    kanbanRunStarts.set(workspaceID, Date.now());
    kanbanRunElapsed.set(workspaceID, 0);
  }
  runningKanbanWorkspaces.add(workspaceID);
  syncKanbanTimer();
}

function finishKanbanRun(workspaceID: string) {
  const startedAt = kanbanRunStarts.get(workspaceID);
  if (startedAt) {
    kanbanRunElapsed.set(workspaceID, Math.max(0, Date.now() - startedAt));
  }
  runningKanbanWorkspaces.delete(workspaceID);
  kanbanRunStarts.delete(workspaceID);
  syncKanbanTimer();
}

function forgetKanbanRun(workspaceID: string) {
  runningKanbanWorkspaces.delete(workspaceID);
  kanbanRunStarts.delete(workspaceID);
  kanbanRunElapsed.delete(workspaceID);
  syncKanbanTimer();
}

function syncKanbanTimer() {
  if (kanbanRunStarts.size > 0 && kanbanTimerID === null) {
    kanbanTimerID = window.setInterval(patchKanbanElapsedTimes, 1000);
  }
  if (kanbanRunStarts.size === 0 && kanbanTimerID !== null) {
    window.clearInterval(kanbanTimerID);
    kanbanTimerID = null;
  }
  patchKanbanElapsedTimes();
}

function patchKanbanElapsedTimes() {
  const now = Date.now();
  appRoot.querySelectorAll<HTMLElement>("[data-kanban-elapsed]").forEach((element) => {
    const workspaceID = element.dataset.workspaceId ?? "";
    if (!hasKanbanRuntime(workspaceID)) {
      return;
    }
    const label = kanbanElapsedLabel(workspaceID, now);
    element.textContent = label;
    element
      .closest<HTMLElement>("[data-kanban-runtime]")
      ?.setAttribute(
        "aria-label",
        runningKanbanWorkspaces.has(workspaceID) ? `Echo has been working for ${label}` : `Echo worked for ${label}`,
      );
  });
}

function fieldValue<K extends keyof llm.Settings>(key: K): string {
  const value = settingsDraft?.[key];
  return value === undefined || value === null ? "" : String(value);
}

function workspaceLetter(workspace: services.Workspace): string {
  return (workspace.letter ?? "").trim() || workspace.displayName.slice(0, 1).toUpperCase() || "W";
}

function renderWorkspaceIcon(workspace: services.Workspace): string {
  const iconURL = (workspace.iconUrl ?? "").trim();
  if (iconURL) {
    return `<img class="workspace-icon-image" src="${escapeAttribute(iconURL)}" alt="">`;
  }
  return `<span class="workspace-icon-label">${escapeHtml(workspaceLetter(workspace))}</span>`;
}

function workspaceLetterDraft(workspace: services.Workspace): string {
  return workspaceLetterDrafts.get(workspace.id) ?? (workspace.letter ?? "");
}

function hydrateWorkspaceLetterDrafts(workspaces: services.Workspace[]) {
  workspaceLetterDrafts.clear();
  workspaces.forEach((workspace) => {
    workspaceLetterDrafts.set(workspace.id, workspace.letter ?? "");
  });
}

function pushToast(message: string, tone: Toast["tone"] = "info") {
  const cleanMessage = message.trim();
  if (!cleanMessage) {
    return;
  }
  const toast = {
    id: `toast-${++toastSeq}`,
    tone,
    message: cleanMessage,
  };
  toasts = [...toasts.slice(-3), toast];
  window.setTimeout(() => {
    dismissToast(toast.id);
  }, tone === "error" ? 9000 : 5200);
}

function dismissToast(id: string) {
  const next = toasts.filter((toast) => toast.id !== id);
  if (next.length === toasts.length) {
    return;
  }
  toasts = next;
  render();
}

function errorMessage(error: unknown): string {
  const raw = error instanceof Error ? error.message : String(error);
  if (raw.includes("send chat request") || raw.includes("connection refused") || raw.includes("No connection could be made")) {
    return `Could not reach the LLM endpoint. Check Settings and try again. ${raw}`;
  }
  if (raw.includes("context deadline exceeded") || raw.includes("Client.Timeout")) {
    return `The LLM endpoint timed out. Increase Timeout Seconds or check the endpoint. ${raw}`;
  }
  return raw;
}

type ChatFilePathMatch = {
  start: number;
  end: number;
  display: string;
  candidate: string;
};

const chatFilePathPattern =
  /(["'`])([^"'`\r\n]*[\\/][^"'`\r\n]*)\1|(?:[A-Za-z]:[\\/]|\.{1,2}[\\/]|[A-Za-z0-9_.@()-]+[\\/])(?:[^\s<>"'`|]+[\\/])*[^\s<>"'`|]+/g;
const trailingChatFilePathPunctuation = /[.,;!?\])}]+$/;

function extractChatFilePathMatches(text: string): ChatFilePathMatch[] {
  const matches: ChatFilePathMatch[] = [];
  chatFilePathPattern.lastIndex = 0;
  for (const match of text.matchAll(chatFilePathPattern)) {
    const quoted = match[2];
    const raw = quoted ?? match[0];
    const rawStart = (match.index ?? 0) + (quoted === undefined ? 0 : 1);
    let display = raw;
    let end = rawStart + display.length;
    const trailing = display.match(trailingChatFilePathPunctuation)?.[0] ?? "";
    if (trailing) {
      display = display.slice(0, -trailing.length);
      end -= trailing.length;
    }
    if (!display || !/[\\/]/.test(display) || display.includes("://")) {
      continue;
    }
    matches.push({
      start: rawStart,
      end,
      display,
      candidate: display,
    });
  }
  return matches;
}

function resolveChatFilePath(workspaceID: string, candidate: string): Promise<string | null> {
  const key = `${workspaceID}\0${candidate}`;
  let cached = chatFileLinkCache.get(key);
  if (!cached) {
    cached = ResolveWorkspaceTextFilePath(workspaceID, candidate)
      .then((path) => path || null)
      .catch(() => null);
    chatFileLinkCache.set(key, cached);
    cached.then((path) => {
      if (!path && chatFileLinkCache.get(key) === cached) {
        chatFileLinkCache.delete(key);
      }
    });
  }
  return cached;
}

function chatFileLinkTargets(root: ParentNode): HTMLElement[] {
  const selector = [
    ".chat-message.from-assistant [data-message-content]",
    ".chat-message.from-assistant [data-message-reasoning]",
    ".chat-message.from-assistant .tool-call code",
    ".chat-message.from-assistant .tool-call pre",
  ].join(", ");
  const targets = Array.from(root.querySelectorAll<HTMLElement>(selector));
  if (root instanceof HTMLElement && root.matches(selector)) {
    targets.unshift(root);
  }
  return targets;
}

async function linkifyAssistantFilePaths(root: ParentNode = appRoot) {
  const workspace = activeWorkspace();
  if (!workspace || workspace.missing) {
    return;
  }
  for (const target of chatFileLinkTargets(root)) {
    await linkifyFilePathsInElement(target, workspace.id);
  }
}

async function linkifyFilePathsInElement(container: HTMLElement, workspaceID: string) {
  if (!container.isConnected) {
    return;
  }
  const textNodes: Text[] = [];
  const walker = document.createTreeWalker(container, NodeFilter.SHOW_TEXT, {
    acceptNode(node) {
      const text = node.nodeValue ?? "";
      if (!text.includes("/") && !text.includes("\\")) {
        return NodeFilter.FILTER_REJECT;
      }
      const parent = node.parentElement;
      if (!parent || parent.closest("a, button, textarea, script, style")) {
        return NodeFilter.FILTER_REJECT;
      }
      return extractChatFilePathMatches(text).length
        ? NodeFilter.FILTER_ACCEPT
        : NodeFilter.FILTER_REJECT;
    },
  });
  while (walker.nextNode()) {
    textNodes.push(walker.currentNode as Text);
  }

  for (const node of textNodes) {
    const text = node.nodeValue ?? "";
    const matches = extractChatFilePathMatches(text);
    if (!matches.length) {
      continue;
    }
    const resolved = await Promise.all(
      matches.map((match) => resolveChatFilePath(workspaceID, match.candidate)),
    );
    if (!node.parentNode || node.nodeValue !== text) {
      continue;
    }

    const fragment = document.createDocumentFragment();
    let cursor = 0;
    let changed = false;
    matches.forEach((match, index) => {
      const path = resolved[index];
      if (!path) {
        return;
      }
      fragment.append(text.slice(cursor, match.start));
      const link = document.createElement("a");
      link.href = "#";
      link.className = "chat-file-link";
      link.dataset.chatFileLink = "";
      link.dataset.workspaceId = workspaceID;
      link.dataset.workspacePath = path;
      link.textContent = match.display;
      bindChatFileLink(link);
      fragment.append(link);
      cursor = match.end;
      changed = true;
    });
    if (!changed) {
      continue;
    }
    fragment.append(text.slice(cursor));
    node.parentNode.replaceChild(fragment, node);
  }
}

function bindChatFileLinks(root: ParentNode) {
  root
    .querySelectorAll<HTMLElement>("[data-chat-file-link]")
    .forEach(bindChatFileLink);
}

function bindChatFileLink(link: HTMLElement) {
  if (link.dataset.chatFileLinkBound) {
    return;
  }
  link.dataset.chatFileLinkBound = "true";
  link.addEventListener("click", handleChatFileLinkClick);
}

async function handleChatFileLinkClick(event: MouseEvent) {
  event.preventDefault();
  const link = event.currentTarget as HTMLElement;
  const workspace = activeWorkspace();
  const workspaceID = link.dataset.workspaceId ?? "";
  const path = link.dataset.workspacePath ?? "";
  if (!workspace || workspace.missing || workspace.id !== workspaceID || !path) {
    return;
  }
  appMode = "code";
  const loading = ensureCodeViewRootLoaded(workspace.id);
  render();
  await loading;
  await openWorkspaceCodeFile(workspace.id, path, codeViewCallbacks());
}

type ChatMentionMatch = {
  triggerStart: number;
  query: string;
  caret: number;
};

function activeChatMentionMatch(input: HTMLTextAreaElement): ChatMentionMatch | null {
  if (input.selectionStart !== input.selectionEnd) {
    return null;
  }
  const caret = input.selectionStart;
  const beforeCaret = input.value.slice(0, caret);
  const match = beforeCaret.match(/(^|\s)@([^\s@]*)$/);
  if (!match) {
    return null;
  }
  const query = match[2] ?? "";
  return {
    triggerStart: beforeCaret.length - query.length - 1,
    query,
    caret,
  };
}

function chatMentionFor(workspaceID: string): ChatMentionState | null {
  return chatMention?.workspaceId === workspaceID ? chatMention : null;
}

function clearChatMention() {
  const timerID = chatMention?.timerID;
  if (timerID !== undefined && timerID !== null) {
    window.clearTimeout(timerID);
  }
  chatMention = null;
}

function syncChatMentionForInput(workspaceID: string, input: HTMLTextAreaElement) {
  const match = activeChatMentionMatch(input);
  if (!match) {
    if (chatMentionFor(workspaceID)) {
      clearChatMention();
      patchChatMentionPicker();
    }
    return;
  }

  let mention = chatMentionFor(workspaceID);
  const changed =
    !mention ||
    mention.query !== match.query ||
    mention.triggerStart !== match.triggerStart;

  if (!mention) {
    mention = {
      workspaceId: workspaceID,
      triggerStart: match.triggerStart,
      query: match.query,
      results: [],
      loading: false,
      error: "",
      selectedIndex: 0,
      requestSeq: 0,
      timerID: null,
    };
    chatMention = mention;
  }

  mention.triggerStart = match.triggerStart;
  mention.query = match.query;

  if (!changed) {
    patchChatMentionPicker();
    return;
  }

  mention.requestSeq++;
  mention.loading = true;
  mention.error = "";
  mention.results = [];
  mention.selectedIndex = 0;
  if (mention.timerID !== null) {
    window.clearTimeout(mention.timerID);
  }
  const sequence = mention.requestSeq;
  mention.timerID = window.setTimeout(() => {
    void runChatMentionSearch(workspaceID, sequence);
  }, chatMentionSearchDelay);
  patchChatMentionPicker();
}

async function runChatMentionSearch(workspaceID: string, sequence: number) {
  const mention = chatMentionFor(workspaceID);
  if (!mention || sequence !== mention.requestSeq) {
    return;
  }
  mention.timerID = null;
  patchChatMentionPicker();
  try {
    const result = await SearchWorkspaceFiles(workspaceID, mention.query, false);
    const model = services.WorkspaceFileSearchResult.createFrom(result);
    const latest = chatMentionFor(workspaceID);
    if (!latest || sequence !== latest.requestSeq) {
      return;
    }
    latest.results = (model.entries ?? []).filter((entry) => entry.kind === "file");
    latest.error = "";
    clampChatMentionSelection(latest);
  } catch (error) {
    const latest = chatMentionFor(workspaceID);
    if (latest && sequence === latest.requestSeq) {
      latest.results = [];
      latest.error = errorMessage(error);
      latest.selectedIndex = 0;
    }
  } finally {
    const latest = chatMentionFor(workspaceID);
    if (latest && sequence === latest.requestSeq) {
      latest.loading = false;
      latest.timerID = null;
      patchChatMentionPicker();
    }
  }
}

function visibleChatMentionEntries(mention: ChatMentionState): services.WorkspaceFileEntry[] {
  return mention.results.slice(0, chatMentionResultLimit);
}

function clampChatMentionSelection(mention: ChatMentionState) {
  const count = visibleChatMentionEntries(mention).length;
  mention.selectedIndex = count
    ? Math.min(Math.max(mention.selectedIndex, 0), count - 1)
    : 0;
}

function moveChatMentionSelection(delta: number) {
  if (!chatMention) {
    return;
  }
  const entries = visibleChatMentionEntries(chatMention);
  if (!entries.length) {
    return;
  }
  chatMention.selectedIndex =
    (chatMention.selectedIndex + delta + entries.length) % entries.length;
  patchChatMentionPicker();
}

function selectChatMentionIndex(index: number) {
  if (!chatMention) {
    return;
  }
  chatMention.selectedIndex = index;
  clampChatMentionSelection(chatMention);
  const entry = visibleChatMentionEntries(chatMention)[chatMention.selectedIndex];
  if (entry) {
    insertChatMention(entry);
  }
}

function insertChatMention(entry: services.WorkspaceFileEntry) {
  const workspace = activeWorkspace();
  const input = appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]");
  if (!workspace || !input || !chatMentionFor(workspace.id)) {
    return;
  }
  const match = activeChatMentionMatch(input);
  const mention = chatMentionFor(workspace.id);
  if (!mention) {
    return;
  }
  const triggerStart = match?.triggerStart ?? mention.triggerStart;
  const caret = match?.caret ?? input.selectionStart;
  const suffix = input.value.slice(caret);
  const trailingSpace = suffix.length === 0 || !/^\s/.test(suffix) ? " " : "";
  const replacement = formatChatMentionPath(entry.path);
  const nextValue =
    input.value.slice(0, triggerStart) + replacement + trailingSpace + suffix;
  const nextCaret = triggerStart + replacement.length + trailingSpace.length;
  input.value = nextValue;
  chatDrafts.set(workspace.id, nextValue);
  clearChatMention();
  input.focus();
  input.setSelectionRange(nextCaret, nextCaret);
  patchChatControls();
  patchChatMentionPicker();
}

function formatChatMentionPath(path: string): string {
  if (!/\s/.test(path)) {
    return `@${path}`;
  }
  return `@"${path.replaceAll('"', '\\"')}"`;
}

function renderChatMentionPicker(workspaceID: string): string {
  const mention = chatMentionFor(workspaceID);
  if (!mention) {
    return "";
  }
  const entries = visibleChatMentionEntries(mention);
  let content = "";
  if (mention.loading) {
    content = `<div class="chat-mention-status"><span class="spinner" aria-hidden="true"></span><span>Searching files...</span></div>`;
  } else if (mention.error) {
    content = `<div class="chat-mention-status is-error">${escapeHtml(mention.error)}</div>`;
  } else if (!entries.length) {
    content = `<div class="chat-mention-status">No matching files.</div>`;
  } else {
    content = entries
      .map((entry, index) => renderChatMentionOption(entry, index, index === mention.selectedIndex))
      .join("");
  }
  return `
    <div class="chat-mention-picker" id="chat-mention-list" role="listbox" aria-label="Workspace files" data-chat-mention-picker>
      ${content}
    </div>
  `;
}

function renderChatMentionOption(
  entry: services.WorkspaceFileEntry,
  index: number,
  selected: boolean,
): string {
  return `
    <button
      class="chat-mention-option ${selected ? "is-active" : ""}"
      id="chat-mention-option-${index}"
      type="button"
      role="option"
      aria-selected="${selected}"
      title="${escapeAttribute(entry.path)}"
      data-chat-mention-option
      data-mention-index="${index}"
    >
      <span class="chat-mention-icon">${icons.file}</span>
      <span class="chat-mention-name">
        <strong>${escapeHtml(fileName(entry.path))}</strong>
        <span>${escapeHtml(entry.path)}</span>
      </span>
      <span class="chat-mention-size">${escapeHtml(formatBytes(entry.bytes ?? 0))}</span>
    </button>
  `;
}

function patchChatMentionPicker() {
  const workspace = activeWorkspace();
  const input = appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]");
  const wrapper = appRoot.querySelector<HTMLElement>("[data-chat-input-wrap]");
  if (!workspace || !input || !wrapper) {
    return;
  }
  const existing = wrapper.querySelector<HTMLElement>("[data-chat-mention-picker]");
  const nextHtml = renderChatMentionPicker(workspace.id).trim();
  if (!nextHtml) {
    existing?.remove();
    input.setAttribute("aria-expanded", "false");
    input.removeAttribute("aria-controls");
    input.removeAttribute("aria-activedescendant");
    return;
  }

  const next = elementFromHtml(nextHtml);
  if (existing) {
    existing.replaceWith(next);
  } else {
    wrapper.append(next);
  }
  bindChatMentionOptions(wrapper);
  input.setAttribute("aria-expanded", "true");
  input.setAttribute("aria-controls", "chat-mention-list");
  const mention = chatMentionFor(workspace.id);
  if (mention && visibleChatMentionEntries(mention).length) {
    input.setAttribute("aria-activedescendant", `chat-mention-option-${mention.selectedIndex}`);
  } else {
    input.removeAttribute("aria-activedescendant");
  }
}

function bindChatMentionOptions(root: ParentNode) {
  root.querySelectorAll<HTMLElement>("[data-chat-mention-option]").forEach((option) => {
    option.addEventListener("mousedown", (event) => {
      event.preventDefault();
    });
    option.addEventListener("click", (event) => {
      event.preventDefault();
      selectChatMentionIndex(Number(option.dataset.mentionIndex ?? "0"));
    });
  });
}

function renderContextMenu(state: ContextMenuState): string {
  return `\
    <div class="workspace-context-menu" data-context-menu style="left:${state.x}px;top:${state.y}px">\
      <button\
        class="workspace-context-menu-item"\
        type="button"\
        data-action="show-in-explorer"\
        data-workspace-id="${escapeAttribute(state.workspaceId)}"\
      >\
        <svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 7v10a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V9l-6-6H5a2 2 0 0 0-2 2Z"/></svg>\
        <span class="workspace-context-menu-label">${escapeHtml(state.folderPath)}</span>\
      </button>\
    </div>\
  `;
}

function dismissContextMenu() {
  if (!contextMenu) {
    return;
  }
  contextMenu = null;
  render();
}

function renderToasts(): string {
  if (!toasts.length) {
    return "";
  }
  return `
    <div class="toast-region" role="status" aria-live="polite" aria-atomic="true">
      ${toasts
        .map(
          (toast) => `
            <div class="toast toast-${toast.tone}">
              <span>${escapeHtml(toast.message)}</span>
              <button class="icon-button" type="button" title="Dismiss" aria-label="Dismiss notification" data-action="dismiss-toast" data-toast-id="${escapeAttribute(toast.id)}">
                ${icons.x}
              </button>
            </div>
          `,
        )
        .join("")}
    </div>
  `;
}

function renderSpinnerLabel(label: string): string {
  return `
    <span class="busy-status" aria-live="polite">
      <span class="spinner" aria-hidden="true"></span>
      <span>${escapeHtml(label)}</span>
    </span>
  `;
}

function render() {
  destroyCodeEditor();
  const chatScroll = captureScrollSnapshot("[data-chat-log]");
  const cardDetailScroll = captureScrollSnapshot("[data-card-detail]");
  const changeReviewScroll = captureScrollSnapshot("[data-change-review]");
  const hadDialog = Boolean(appRoot.querySelector('[role="dialog"]'));
  const workspace = activeWorkspace();
  const workspaces = appState?.workspaces ?? [];
  const showingCode = appMode === "code" && Boolean(workspace) && !workspace?.missing;
  if (
    chatMention &&
    (!workspace || workspace.missing || showingCode || settingsOpen || workspace.id !== chatMention.workspaceId)
  ) {
    clearChatMention();
  }

  appRoot.innerHTML = `
    <div class="app-shell">
      <aside class="gutter" aria-label="Primary">
        <nav class="workspace-rail" aria-label="Workspaces">
          ${workspaces
            .map(
              (item) => `
                <button
                  class="gutter-button workspace-button ${item.active ? "is-active" : ""} ${item.missing ? "is-missing" : ""}"
                  type="button"
                  title="${escapeHtml(item.folderPath)}"
                  aria-label="${escapeHtml(item.displayName)}${item.missing ? " missing" : ""}"
                  data-action="activate-workspace"
                  data-workspace-id="${escapeHtml(item.id)}"
                >${renderWorkspaceIcon(item)}</button>
              `,
            )
            .join("")}
        </nav>
        <div class="gutter-actions">
          <button class="gutter-button icon-button" type="button" title="Add workspace" aria-label="Add workspace" data-action="add-workspace">
            ${icons.plus}
          </button>
          <button class="gutter-button icon-button" type="button" title="Settings" aria-label="Settings" data-action="open-settings">
            ${icons.settings}
          </button>
        </div>
      </aside>
      <main class="main-content">
        <section class="workspace-panel ${showingCode ? "is-code-mode" : ""}" aria-labelledby="${showingCode ? "code-title" : "workspace-title"}">
          ${
            showingCode && workspace
              ? renderCodeView(workspace)
              : `
                <div class="workspace-heading-row">
                  <strong id="workspace-title">${workspace ? escapeHtml(workspace.displayName) : "Workspace"}</strong><span class="heading-path">${workspace ? escapeHtml(workspace.folderPath) : ""}</span>
                  ${
                    workspace && !workspace.missing
                      ? `<button class="secondary-button icon-text-button" type="button" data-action="open-code-view">
                          ${icons.code}
                          <span>Code</span>
                        </button>`
                      : ""
                  }
                </div>
                ${workspace?.missing ? renderMissingWorkspace(workspace) : renderWorkspacePanels(workspace, workspaces.length)}
              `
          }
        </section>
        ${showingCode && workspace && openGitChangeWorkspaces.has(workspace.id) ? renderGitChangesDrawer(workspace, gitChangeReviewFor(workspace.id)) : ""}
      </main>
      ${settingsOpen ? renderSettingsOverlay(workspaces) : ""}
      ${renderToasts()}
      ${contextMenu ? renderContextMenu(contextMenu) : ""}
    </div>
  `;

  bindEvents();
  restoreScrollSnapshot("[data-chat-log]", chatScroll);
  restoreScrollSnapshot("[data-card-detail]", cardDetailScroll);
  restoreScrollSnapshot("[data-change-review]", changeReviewScroll);
  if (!hadDialog) {
    focusInitialElement();
  }
  void linkifyAssistantFilePaths();
}

function renderWorkspacePanels(workspace: services.Workspace | null, workspaceCount: number): string {
  const board = workspace ? kanbanBoardFor(workspace.id) : null;
  const review = workspace ? changeReviewFor(workspace.id) : null;
  const running = workspace ? runningKanbanWorkspaces.has(workspace.id) : false;
  const decomposing = workspace ? executingPlans.has(workspace.id) : false;
  const hasCards = board ? kanbanCards(board).length > 0 : false;
  const hasDoneCards = board ? (board.done ?? []).length > 0 : false;
  const chatExpanded = workspace ? expandedChatWorkspaces.has(workspace.id) : false;
  const kanbanExpanded = workspace && !chatExpanded ? expandedKanbanWorkspaces.has(workspace.id) : false;
  const kanbanSizeLabel = kanbanExpanded ? "Collapse Kanban" : "Expand Kanban";
  const reviewCount = review?.fileCount ?? 0;
  return `
    <div class="split-panels ${chatExpanded ? "is-chat-expanded" : ""} ${kanbanExpanded ? "is-kanban-expanded" : ""}">
      ${renderChatPanel(workspace, chatExpanded)}
      <section class="work-panel kanban-panel" aria-labelledby="kanban-title">
        <div class="panel-heading">
          <div class="kanban-heading-main">
            <span>Kanban</span>
            <strong id="kanban-title">${workspace ? escapeHtml(workspace.displayName) : `${workspaceCount} workspace${workspaceCount === 1 ? "" : "s"}`}</strong>
            ${workspace && hasKanbanRuntime(workspace.id) ? renderKanbanRuntime(workspace.id, running) : ""}
          </div>
          ${
            workspace
              ? `<div class="kanban-actions">
                  <button class="secondary-button icon-text-button change-review-button" type="button" title="Review AI file changes" data-action="open-change-review">
                    ${icons.file}
                    <span>Changes</span>
                    <span class="change-count-badge">${escapeHtml(String(reviewCount))}</span>
                  </button>
                  <button class="icon-button" type="button" title="${kanbanSizeLabel}" aria-label="${kanbanSizeLabel}" aria-pressed="${kanbanExpanded}" data-action="toggle-kanban-size">
                    ${kanbanExpanded ? icons.collapse : icons.expand}
                  </button>
                  <button class="icon-text-button primary-button" type="button" data-action="start-agents" ${running || !hasCards ? "disabled" : ""}>
                    ${icons.execute}
                    <span class="run-button">Run</span>
                  </button>
                  <button class="icon-button danger-button" type="button" title="Clear done cards" aria-label="Clear done Kanban cards" data-action="clear-done-cards" ${hasDoneCards ? "" : "disabled"}>
                    ${icons.trash}
                  </button>
                  <button class="icon-button stop-button" type="button" title="Stop agents" aria-label="Stop agents" data-action="stop-agents" ${running ? "" : "disabled"}>
                    ${icons.stop}
                  </button>
                </div>`
              : ""
          }
        </div>
        ${
          board
            ? decomposing && !hasCards
              ? renderDecompositionState()
              : hasCards
                ? renderKanbanBoard(board)
                : renderEmptyBoard()
            : `<div class="empty-state">Add a workspace to create cards.</div>`
        }
      </section>
    </div>
    ${board ? renderKanbanDetail(board) : ""}
    ${workspace && openChangeReviewWorkspaces.has(workspace.id) ? renderChangeReviewDrawer(workspace, review ?? changeReviewFor(workspace.id)) : ""}
  `;
}

function renderKanbanRuntime(workspaceID: string, running: boolean): string {
  const elapsed = kanbanElapsedLabel(workspaceID);
  const status = running ? "Working" : "Finished";
  return `
    <div class="kanban-runtime" role="timer" aria-label="${running ? "Echo has been working" : "Echo worked"} for ${elapsed}" data-kanban-runtime>
      ${running ? `<span class="spinner" aria-hidden="true"></span>` : icons.check}
      <span>${status}</span>
      <time data-kanban-elapsed data-workspace-id="${escapeAttribute(workspaceID)}">${escapeHtml(elapsed)}</time>
    </div>
  `;
}

function renderEmptyBoard(): string {
  return `
    <div class="empty-state board-empty">
      <strong>No cards yet</strong>
      <span>Chat through a plan, then execute it to create Ready cards.</span>
    </div>
  `;
}

function renderDecompositionState(): string {
  return `
    <div class="empty-state board-empty decomposition-state" role="status" aria-live="polite">
      <span class="spinner decomposition-spinner" aria-hidden="true"></span>
      <strong>Decomposing cards</strong>
      <span>Echo is converting the chat plan into Ready cards.</span>
    </div>
  `;
}

function renderKanbanBoard(board: services.KanbanBoard): string {
  return `
    <div class="kanban-board" aria-label="Kanban lanes">
      ${renderKanbanLane("Ready", board.ready ?? [])}
      ${renderKanbanLane("In Progress", board.inProgress ?? [])}
      ${renderKanbanLane("Blocked", board.blocked ?? [])}
      ${renderKanbanLane("Done", board.done ?? [])}
    </div>
  `;
}

function renderKanbanLane(title: string, cards: services.KanbanCard[]): string {
  return `
    <section class="kanban-lane" aria-label="${escapeAttribute(title)}">
      <header>
        <strong>${escapeHtml(title)}</strong>
        <span>${cards.length}</span>
      </header>
      <div class="kanban-cards">
        ${
          cards.length
            ? cards.map(renderKanbanCard).join("")
            : `<p class="lane-empty">No cards</p>`
        }
      </div>
    </section>
  `;
}

function renderKanbanCard(card: services.KanbanCard): string {
  const criteria = card.acceptanceCriteria ?? [];
  const dependencies = card.dependencies ?? [];
  const dependencyStatuses = card.dependencyStatuses ?? [];
  const blockedBy = card.blockedBy ?? [];
  const unavailable = card.lane === "ready" && !card.eligible;
  return `
    <button
      class="kanban-card ${unavailable ? "is-unavailable" : ""}"
      type="button"
      data-action="open-card"
      data-card-id="${escapeAttribute(card.id)}"
      aria-label="Open ${escapeAttribute(card.title)} details"
    >
      <header>
        <strong>${escapeHtml(card.title)}</strong>
        <span>${escapeHtml(card.id)}</span>
      </header>
      <p>${escapeHtml(card.description)}</p>
      ${
        criteria.length
          ? `<ul>${criteria.map((item) => `<li>${escapeHtml(item)}</li>`).join("")}</ul>`
          : ""
      }
      ${
        dependencies.length
          ? `<div class="card-dependencies">${blockedBy.length ? "Waiting on" : "After"} ${
              dependencyStatuses.length
                ? dependencyStatuses
                    .map(
                      (dependency) =>
                        `${escapeHtml(dependency.title || dependency.id)} (${escapeHtml(laneLabel(dependency.status))})`,
                    )
                    .join(", ")
                : dependencies.map(escapeHtml).join(", ")
            }</div>`
          : ""
      }
    </button>
  `;
}

function renderKanbanDetail(board: services.KanbanBoard): string {
  const card = selectedKanbanCard(board);
  if (!card) {
    return "";
  }

  const dependencies = card.dependencyStatuses ?? [];
  const criteria = card.acceptanceCriteria ?? [];
  const transcript = card.progressTranscript ?? [];
  const blocked = card.lane === "ready" && !card.eligible;
  const canReset = card.lane !== "ready" || transcript.length > 0;
  const canEditDescription = card.lane === "ready" && !runningKanbanWorkspaces.has(board.workspaceId);
  const draftKey = `${board.workspaceId}:${card.id}`;
  const cardDraft = cardMessageDrafts.get(draftKey) ?? "";
  return `
    <aside class="card-detail-backdrop" role="dialog" aria-modal="true" aria-labelledby="card-detail-title">
      <section class="card-detail" data-card-detail>
        <header class="card-detail-header">
          <div>
            <p class="eyebrow">${escapeHtml(card.id)} - ${escapeHtml(laneLabel(card.status || card.lane))}</p>
            <h2 id="card-detail-title">${escapeHtml(card.title)}</h2>
          </div>
          <button class="icon-button close-button" type="button" title="Close" aria-label="Close card details" data-action="close-card">
            ${icons.x}
          </button>
        </header>

        <div class="status-controls" aria-label="Card status">
          ${renderLaneButton(card, "ready")}
          ${renderLaneButton(card, "inProgress", blocked)}
          ${renderLaneButton(card, "blocked")}
          ${renderLaneButton(card, "done")}
        </div>

        ${blocked ? `<p class="blocked-note">Unavailable until prerequisites are Done.</p>` : ""}
        <div class="card-detail-actions">
          <button class="secondary-button icon-text-button" type="button" data-action="reset-card" data-card-id="${escapeAttribute(card.id)}" ${canReset ? "" : "disabled"}>
            ${icons.refresh}
            <span>Reset</span>
          </button>
          ${
            card.lane === "inProgress"
              ? `<button class="secondary-button icon-text-button stop-card-button" type="button" data-action="stop-card" data-card-id="${escapeAttribute(card.id)}">
                  ${icons.stop}
                  <span>Stop</span>
                </button>`
              : ""
          }
        </div>

        <section class="detail-section">
          <h3>Description</h3>
          ${
            canEditDescription
              ? `<form class="card-description-form" data-card-description-form data-card-id="${escapeAttribute(card.id)}">
                  <textarea name="description" rows="5" aria-label="Card description" data-card-description-input>${escapeHtml(card.description)}</textarea>
                  <button class="primary-button icon-text-button" type="submit" disabled>
                    ${icons.check}
                    <span>Save</span>
                  </button>
                </form>`
              : `<p>${escapeHtml(card.description)}</p>`
          }
        </section>

        <section class="detail-section">
          <h3>Dependencies</h3>
          ${
            dependencies.length
              ? `<div class="dependency-list">${dependencies
                  .map(
                    (dependency) => `
                      <div class="dependency-row ${dependency.done ? "is-done" : ""}">
                        <strong>${escapeHtml(dependency.title || dependency.id)}</strong>
                        <span>${escapeHtml(laneLabel(dependency.status))}</span>
                      </div>
                    `,
                  )
                  .join("")}</div>`
              : `<p>No dependencies.</p>`
          }
        </section>

        <section class="detail-section">
          <h3>Acceptance Criteria</h3>
          ${
            criteria.length
              ? `<ul>${criteria.map((item) => `<li>${escapeHtml(item)}</li>`).join("")}</ul>`
              : `<p>No acceptance criteria recorded.</p>`
          }
        </section>

        <section class="detail-section" data-card-progress-section>
          ${renderProgressSectionContent(transcript)}
        </section>

        ${
          card.lane === "blocked"
            ? `<form class="card-message-form" data-card-message-form data-card-id="${escapeAttribute(card.id)}">
                <textarea name="message" rows="3" placeholder="Add direction..." aria-label="Message for card" data-card-message-input>${escapeHtml(cardDraft)}</textarea>
                <button class="primary-button icon-text-button" type="submit" ${cardDraft.trim() ? "" : "disabled"}>
                  ${icons.send}
                  <span>Send</span>
                </button>
              </form>`
            : ""
        }
      </section>
    </aside>
  `;
}

function renderChangeReviewDrawer(
  workspace: services.Workspace,
  review: services.WorkspaceChangeReview,
): string {
  const files = review.files ?? [];
  const hasChanges = (review.changeCount ?? 0) > 0;
  const expanded = expandedChangeReviewWorkspaces.has(workspace.id);
  const sizeLabel = expanded ? "Collapse AI changes" : "Expand AI changes";
  return `
    <aside class="change-review-backdrop ${expanded ? "is-expanded" : ""}" role="dialog" aria-modal="true" aria-labelledby="change-review-title">
      <section class="change-review ${expanded ? "is-expanded" : ""}" data-change-review>
        <header class="change-review-header">
          <div>
            <p class="eyebrow">${escapeHtml(workspace.displayName)}</p>
            <h2 id="change-review-title">AI Changes</h2>
          </div>
          <div class="change-review-header-actions">
            <button class="icon-button" type="button" title="${sizeLabel}" aria-label="${sizeLabel}" aria-pressed="${expanded}" data-action="toggle-change-review-size">
              ${expanded ? icons.collapse : icons.expand}
            </button>
            <button class="icon-button close-button" type="button" title="Close" aria-label="Close AI changes" data-action="close-change-review">
              ${icons.x}
            </button>
          </div>
        </header>

        <div class="change-review-summary" aria-label="Change summary">
          <span>${escapeHtml(String(review.fileCount ?? files.length))} files</span>
          <span>${escapeHtml(String(review.changeCount ?? 0))} tool changes</span>
        </div>

        <div class="change-review-actions">
          <button class="icon-button" type="button" title="Previous change" aria-label="Previous change" data-action="previous-change" ${files.length ? "" : "disabled"}>
            ${icons.arrowUp}
          </button>
          <button class="icon-button" type="button" title="Next change" aria-label="Next change" data-action="next-change" ${files.length ? "" : "disabled"}>
            ${icons.arrowDown}
          </button>
          <button class="secondary-button icon-text-button" type="button" data-action="clear-change-review" ${hasChanges ? "" : "disabled"}>
            ${icons.trash}
            <span>Clear</span>
          </button>
        </div>

        ${
          files.length
            ? `<div class="change-file-list">${files.map(renderChangedFile).join("")}</div>`
            : `<div class="empty-state compact">No AI file changes recorded.</div>`
        }
      </section>
    </aside>
  `;
}

function renderGitChangesDrawer(
  workspace: services.Workspace,
  review: services.WorkspaceGitChangeReview,
): string {
  const files = review.files ?? [];
  const expanded = expandedGitChangeWorkspaces.has(workspace.id);
  const loading = loadingGitChangeWorkspaces.has(workspace.id);
  const sizeLabel = expanded ? "Collapse Git changes" : "Expand Git changes";
  return `
    <aside class="change-review-backdrop ${expanded ? "is-expanded" : ""}" role="dialog" aria-modal="true" aria-labelledby="git-change-review-title">
      <section class="change-review ${expanded ? "is-expanded" : ""}" data-change-review>
        <header class="change-review-header">
          <div>
            <p class="eyebrow">${escapeHtml(workspace.displayName)}</p>
            <h2 id="git-change-review-title">Git Changes</h2>
          </div>
          <div class="change-review-header-actions">
            <button class="icon-button" type="button" title="${sizeLabel}" aria-label="${sizeLabel}" aria-pressed="${expanded}" data-action="toggle-git-changes-size">
              ${expanded ? icons.collapse : icons.expand}
            </button>
            <button class="icon-button close-button" type="button" title="Close" aria-label="Close Git changes" data-action="close-git-changes">
              ${icons.x}
            </button>
          </div>
        </header>

        <div class="change-review-summary" aria-label="Git change summary">
          <span>${escapeHtml(String(review.fileCount ?? files.length))} files</span>
          ${loading ? `<span><span class="spinner" aria-hidden="true"></span>Refreshing</span>` : ""}
        </div>

        <div class="change-review-actions">
          <button class="icon-button" type="button" title="Previous change" aria-label="Previous change" data-action="previous-change" ${files.length ? "" : "disabled"}>
            ${icons.arrowUp}
          </button>
          <button class="icon-button" type="button" title="Next change" aria-label="Next change" data-action="next-change" ${files.length ? "" : "disabled"}>
            ${icons.arrowDown}
          </button>
          <button class="secondary-button icon-text-button" type="button" data-action="refresh-git-changes" ${loading ? "disabled" : ""}>
            ${loading ? `<span class="spinner" aria-hidden="true"></span>` : icons.refresh}
            <span>Refresh</span>
          </button>
        </div>

        ${
          files.length
            ? `<div class="change-file-list">${files.map(renderGitChangedFile).join("")}</div>`
            : `<div class="empty-state compact">${loading ? renderSpinnerLabel("Loading Git changes") : "No Git changes."}</div>`
        }
      </section>
    </aside>
  `;
}

function renderChangedFile(file: services.WorkspaceChangedFile): string {
  return `
    <article class="change-file" data-change-file>
      <header>
        <div class="change-file-title">
          ${icons.file}
          <strong title="${escapeAttribute(file.path)}">${escapeHtml(file.path)}</strong>
        </div>
        <span class="change-operation is-${escapeAttribute(file.operation)}">${escapeHtml(changeOperationLabel(file.operation))}</span>
      </header>
      ${renderChangeSources(file.sources ?? [])}
      ${file.diffAvailable && file.diff ? renderChangeDiff(file.diff) : renderChangeMetadata(file)}
    </article>
  `;
}

function renderGitChangedFile(file: services.WorkspaceGitChangedFile): string {
  return `
    <article class="change-file" data-change-file>
      <header>
        <div class="change-file-title">
          ${icons.file}
          <strong title="${escapeAttribute(file.path)}">${escapeHtml(file.path)}</strong>
        </div>
        <span class="change-operation is-${escapeAttribute(file.operation)}">${escapeHtml(changeOperationLabel(file.operation))}</span>
      </header>
      ${renderGitChangeStatus(file)}
      ${file.diffAvailable && file.diff ? renderChangeDiff(file.diff) : renderGitChangeMetadata(file)}
    </article>
  `;
}

function renderGitChangeStatus(file: services.WorkspaceGitChangedFile): string {
  const chips: string[] = [];
  if (file.oldPath) {
    chips.push(`<span title="${escapeAttribute(file.oldPath)}">from <em>${escapeHtml(file.oldPath)}</em></span>`);
  }
  if (file.status) {
    chips.push(`<span>status <em>${escapeHtml(file.status)}</em></span>`);
  }
  if (file.indexStatus) {
    chips.push(`<span>index <em>${escapeHtml(gitStatusLabel(file.indexStatus))}</em></span>`);
  }
  if (file.worktreeStatus) {
    chips.push(`<span>worktree <em>${escapeHtml(gitStatusLabel(file.worktreeStatus))}</em></span>`);
  }
  if (!chips.length) {
    return "";
  }
  return `<div class="change-sources" aria-label="Git status">${chips.join("")}</div>`;
}

function renderChangeSources(sources: services.WorkspaceChangeSource[]): string {
  if (!sources.length) {
    return "";
  }
  return `
    <div class="change-sources" aria-label="Change sources">
      ${sources
        .map(
          (source) => `
            <span title="${escapeAttribute(source.toolName || "AI tool")}">
              ${escapeHtml(changeSourceLabel(source))}
              ${source.toolName ? `<em>${escapeHtml(source.toolName)}</em>` : ""}
            </span>
          `,
        )
        .join("")}
    </div>
  `;
}

function renderGitChangeMetadata(file: services.WorkspaceGitChangedFile): string {
  return `
    <div class="change-metadata">
      <span>${escapeHtml(gitDiffUnavailableLabel(file))}</span>
    </div>
  `;
}

function gitDiffUnavailableLabel(file: services.WorkspaceGitChangedFile): string {
  if (file.operation === "created" && file.status === "??") {
    return "Diff is unavailable for this untracked file.";
  }
  return "Diff is unavailable for this Git change.";
}

function gitStatusLabel(status: string): string {
  switch (status) {
    case "A":
      return "added";
    case "C":
      return "copied";
    case "D":
      return "deleted";
    case "M":
      return "modified";
    case "R":
      return "renamed";
    case "U":
      return "unmerged";
    case "?":
      return "untracked";
    default:
      return status;
  }
}

function renderChangeDiff(diff: string): string {
  const lines = diff.split("\n");
  const rendered = lines
    .map((line) => {
      let kind = "context";
      if (line.startsWith("+") && !line.startsWith("+++")) {
        kind = "added";
      } else if (line.startsWith("-") && !line.startsWith("---")) {
        kind = "removed";
      } else if (line.startsWith("@@") || line.startsWith("---") || line.startsWith("+++")) {
        kind = "meta";
      }
      const marker = kind === "added" || kind === "removed" ? " data-change-line" : "";
      return `<span class="change-diff-line is-${kind}"${marker}>${escapeHtml(line || " ")}</span>`;
    })
    .join("");
  return `<pre class="change-diff"><code>${rendered}</code></pre>`;
}

function renderChangeMetadata(file: services.WorkspaceChangedFile): string {
  const before = file.before;
  const after = file.after;
  const beforeLabel = before ? `${formatBytes(before.bytes || 0)} ${before.binary ? "binary" : before.large ? "large" : "file"}` : "not present";
  const afterLabel = after ? `${formatBytes(after.bytes || 0)} ${after.binary ? "binary" : after.large ? "large" : "file"}` : "not present";
  return `
    <div class="change-metadata">
      <span>Before: ${escapeHtml(beforeLabel)}</span>
      <span>After: ${escapeHtml(afterLabel)}</span>
      ${before?.sha256 ? `<code title="${escapeAttribute(before.sha256)}">before ${escapeHtml(before.sha256.slice(0, 12))}</code>` : ""}
      ${after?.sha256 ? `<code title="${escapeAttribute(after.sha256)}">after ${escapeHtml(after.sha256.slice(0, 12))}</code>` : ""}
    </div>
  `;
}

function renderLaneButton(card: services.KanbanCard, lane: string, blocked = false): string {
  const active = card.lane === lane;
  return `
    <button
      class="status-button ${active ? "is-active" : ""}"
      type="button"
      data-action="move-card"
      data-card-id="${escapeAttribute(card.id)}"
      data-lane="${escapeAttribute(lane)}"
      ${active || blocked ? "disabled" : ""}
    >${escapeHtml(laneLabel(lane))}</button>
  `;
}

function renderProgressEntry(entry: services.KanbanProgressEntry): string {
  return `
    <article class="transcript-entry">
      <header>
        <strong>${escapeHtml(entry.title || entry.type || "Progress")}</strong>
        ${entry.status ? `<span>${escapeHtml(laneLabel(entry.status))}</span>` : ""}
      </header>
      <p>${escapeHtml(entry.content)}</p>
    </article>
  `;
}

function renderProgressSectionContent(transcript: services.KanbanProgressEntry[]): string {
  return `
    <h3>Progress Transcript</h3>
    ${
      transcript.length
        ? `<div class="transcript-list" data-transcript-list>${transcript.map(renderProgressEntry).join("")}</div>`
        : `<p>No progress recorded yet.</p>`
    }
  `;
}

function renderChatPanel(workspace: services.Workspace | null, expanded = false): string {
  if (!workspace) {
    return `
      <section class="work-panel chat-panel" aria-labelledby="chat-title">
        <div class="panel-heading">
          <span>Chat</span>
          <strong id="chat-title">No workspace</strong>
        </div>
        <div class="empty-state">Add a workspace to start planning.</div>
      </section>
    `;
  }

  const session = chatSessionFor(workspace.id);
  const messages = session.messages ?? [];
  const draft = chatDrafts.get(workspace.id) ?? "";
  const imageDrafts = chatImageDraftsFor(workspace.id);
  const executing = executingPlans.has(workspace.id);
  const canSend = !session.busy && !executing && (draft.trim().length > 0 || imageDrafts.length > 0);
  const sizeLabel = expanded ? "Collapse chat" : "Expand chat";
  const executeLabel = executing ? "Decomposing cards" : "Execute plan";
  const mentionOpen = Boolean(chatMentionFor(workspace.id));
  const planMode = chatPlanModeFor(workspace.id);
  return `
    <section class="work-panel chat-panel" aria-labelledby="chat-title" aria-busy="${session.busy || executing}" data-chat-panel data-workspace-id="${escapeAttribute(workspace.id)}">
      <div class="panel-heading chat-heading">
        <div class="chat-actions">
        <div style="width: 5em;">
          <span>Chat</span>
          <br/>
          <strong id="chat-title">${executing ? renderSpinnerLabel("Triage") : session.busy ? "Working" : "Ready"}</strong>
        </div>
          <button class="icon-button" type="button" title="${sizeLabel}" aria-label="${sizeLabel}" aria-pressed="${expanded}" data-action="toggle-chat-size">
            ${expanded ? icons.collapse : icons.expand}
          </button>
          <button class="icon-button execute-button ${executing ? "is-busy" : ""}" type="button" title="${executeLabel}" aria-label="${executeLabel}" data-action="execute-plan" ${session.busy || executing || messages.length === 0 ? "disabled" : ""}>
            ${executing ? `<span class="spinner" aria-hidden="true"></span>` : icons.execute}
          </button>
          <button class="icon-button" type="button" title="Clear chat" aria-label="Clear chat" data-action="clear-chat" ${session.busy || executing || messages.length === 0 ? "disabled" : ""}>
            ${icons.trash}
          </button>
          <button class="icon-button stop-button" type="button" title="Stop stream" aria-label="Stop stream" data-action="stop-chat" ${session.busy ? "" : "disabled"}>
            ${icons.stop}
          </button>
        </div>
      </div>
      <div class="chat-log" data-chat-log>
        ${
          messages.length
            ? messages.map(renderChatMessage).join("")
            : `<div class="empty-state chat-empty">Ask Echo to inspect, plan, or break down work for this workspace.</div>`
        }
      </div>
      <form class="chat-composer" data-chat-form>
        <div class="chat-input-wrap" data-chat-input-wrap>
          <textarea
            name="message"
            rows="3"
            placeholder="Ask for a plan..."
            aria-label="Message Echo"
            aria-autocomplete="list"
            aria-expanded="${mentionOpen}"
            ${mentionOpen ? `aria-controls="chat-mention-list"` : ""}
            spellcheck="true"
            data-chat-input
            ${session.busy || executing ? "disabled" : ""}
          >${escapeHtml(draft)}</textarea>
          ${renderChatImageDrafts(workspace.id, session.busy || executing)}
          ${renderChatMentionPicker(workspace.id)}
        </div>
        <div>
        <label class="chat-plan-toggle" title="Plan mode researches and plans without changing files">
          <span>Plan</span><br/>
            <input
              type="checkbox"
              data-chat-plan-toggle
              ${planMode ? "checked" : ""}
              ${session.busy || executing ? "disabled" : ""}
            >
          </label>
          <button class="primary-button icon-button send-button" type="submit" title="Send" aria-label="Send message" ${canSend ? "" : "disabled"}>
          ${icons.send}
          </button>
        </div>
      </form>
    </section>
  `;
}

function renderChatImageDrafts(workspaceID: string, disabled: boolean): string {
  const drafts = chatImageDraftsFor(workspaceID);
  if (!drafts.length) {
    return "";
  }
  return `
    <div class="chat-image-drafts" data-chat-image-drafts>
      ${drafts
        .map(
          (draft) => `
            <div class="chat-image-chip">
              <img src="${escapeAttribute(draft.dataUrl)}" alt="">
              <span>
                <strong>${escapeHtml(draft.name)}</strong>
                <small>${escapeHtml(formatBytes(draft.bytes))}</small>
              </span>
              <button class="icon-button" type="button" title="Remove image" aria-label="Remove ${escapeAttribute(draft.name)}" data-action="remove-chat-image" data-image-id="${escapeAttribute(draft.id)}" ${disabled ? "disabled" : ""}>
                ${icons.x}
              </button>
            </div>
          `,
        )
        .join("")}
    </div>
  `;
}

function renderChatMessage(message: services.ChatMessage): string {
  const roleLabel = message.role === "user" ? "You" : "Echo";
  const status = message.status && message.status !== "complete"
    ? `<span data-message-status>${escapeHtml(message.status)}</span>`
    : "";
  const isUser = message.role === "user";
  const isEditing = editingMessageIds.has(message.id);
  return `
    <article class="chat-message ${isUser ? "from-user" : "from-assistant"}" data-message-id="${escapeAttribute(message.id)}">
      <header>
        <strong>${roleLabel}</strong>
        ${status}
        ${isUser ? renderEditControls(message, isEditing) : renderAssistantControls(message)}
      </header>
      ${renderChatMessageImages(message)}
      ${isEditing
        ? renderEditTextarea(message)
        : `<div class="markdown-body" data-message-content>${renderMarkdown(message.content ?? "")}</div>`
      }
      ${message.error ? `<p class="message-error" data-message-error>${escapeHtml(message.error)}</p>` : `<p class="message-error" data-message-error hidden></p>`}
      ${renderDebugSections(message)}
    </article>
  `;
}

function renderAssistantControls(message: services.ChatMessage): string {
  const isStreaming = isAssistantMessageStreaming(message);
  const canCreateCard = canCreateKanbanCardFromMessage(message);
  return `
    <div class="chat-message-actions">
      <button class="icon-button chat-retry-trigger" type="button" title="Regenerate response" aria-label="Regenerate response" data-action="retry-message" data-message-id="${escapeAttribute(message.id)}">
        ${isStreaming ? '<span class="spinner" aria-hidden="true"></span>' : icons.retry}
      </button>
      <button class="icon-button chat-kanban-trigger" type="button" title="Create Kanban card" aria-label="Create Kanban card from response" data-action="create-card-from-message" data-message-id="${escapeAttribute(message.id)}" ${canCreateCard ? "" : "disabled"}>
        ${icons.kanban}
      </button>
    </div>
  `;
}

function isAssistantMessageStreaming(message: services.ChatMessage): boolean {
  return message.status === "streaming" || message.status === "retrying" || message.status === "in_progress";
}

function canCreateKanbanCardFromMessage(message: services.ChatMessage): boolean {
  return message.status === "complete" && (message.content ?? "").trim().length > 0;
}

function renderEditControls(message: services.ChatMessage, isEditing: boolean): string {
  if (isEditing) {
    return "";
  }
  return `
    <div class="chat-message-actions">
      <button class="icon-button chat-edit-trigger" type="button" title="Edit message" aria-label="Edit message" data-action="edit-message" data-message-id="${escapeAttribute(message.id)}">
        ${icons.edit}
      </button>
    </div>
  `;
}

function renderEditTextarea(message: services.ChatMessage): string {
  const escapedContent = escapeHtml(message.content ?? "");
  return `
    <form class="chat-edit-form" data-chat-edit-form data-message-id="${escapeAttribute(message.id)}">
      <textarea class="chat-edit-textarea" rows="3" spellcheck="true" data-chat-edit-input aria-label="Edit message">${escapedContent}</textarea>
      <div class="chat-edit-actions">
        <button class="primary-button" type="submit" data-action="save-message-edit">Save</button>
        <button class="secondary-button" type="button" data-action="cancel-message-edit">Cancel</button>
      </div>
    </form>
  `;
}

function renderChatMessageImages(message: services.ChatMessage): string {
  const images = message.images ?? [];
  if (!images.length) {
    return "";
  }
  return `
    <div class="chat-message-images">
      ${images
        .map(
          (image) => `
            <figure class="chat-message-image">
              ${image.dataUrl ? `<img src="${escapeAttribute(image.dataUrl)}" alt="${escapeAttribute(image.name)}">` : `<span>${icons.image}</span>`}
              <figcaption>
                <strong>${escapeHtml(image.name)}</strong>
                <span>${escapeHtml(image.path || image.source)} - ${escapeHtml(formatBytes(image.bytes ?? 0))}</span>
              </figcaption>
            </figure>
          `,
        )
        .join("")}
    </div>
  `;
}

function renderDebugSections(message: services.ChatMessage): string {
  if (message.role !== "assistant") {
    return "";
  }
  const hasReasoning = Boolean(message.reasoning);
  const toolCalls = message.toolCalls ?? [];
  if (!hasReasoning && toolCalls.length === 0) {
    return `<div class="debug-stack" data-debug-stack></div>`;
  }
  return `
    <div class="debug-stack" data-debug-stack>
      ${hasReasoning ? renderReasoning(message.reasoning ?? "") : ""}
      ${toolCalls.length ? renderToolCalls(toolCalls) : ""}
    </div>
  `;
}

function renderReasoning(reasoning: string): string {
  return `
    <details class="debug-section" data-debug-section="reasoning">
      <summary>Thinking</summary>
      <div class="debug-content" data-message-reasoning>${renderMarkdown(reasoning)}</div>
    </details>
  `;
}

function renderToolCalls(toolCalls: services.ChatToolActivity[]): string {
  return `
    <details class="debug-section" data-debug-section="tools">
      <summary>Tools</summary>
      <div class="tool-list" data-tool-list>
        ${toolCalls.map(renderToolCall).join("")}
      </div>
    </details>
  `;
}

function renderToolCall(toolCall: services.ChatToolActivity): string {
  return `
    <div class="tool-call">
      <div>
        <strong>${escapeHtml(toolCall.name || "tool")}</strong>
        <span>${escapeHtml(toolCall.status)}</span>
      </div>
      ${toolCall.arguments ? `<code>${escapeHtml(toolCall.arguments)}</code>` : ""}
      ${toolCall.error ? `<p>${escapeHtml(toolCall.error)}</p>` : ""}
      ${toolCall.result ? `<pre>${escapeHtml(toolCall.result)}</pre>` : ""}
    </div>
  `;
}

function renderMissingWorkspace(workspace: services.Workspace): string {
  return `
    <section class="missing-panel" aria-labelledby="missing-title">
      <div>
        <p class="eyebrow">Workspace unavailable</p>
        <h2 id="missing-title">Folder missing</h2>
      </div>
      <p>${escapeHtml(workspace.error || "Echo cannot find this workspace folder.")}</p>
      <code>${escapeHtml(workspace.folderPath)}</code>
      <div class="missing-actions">
        <button class="primary-button icon-text-button" type="button" data-action="refresh-workspaces">
          ${icons.refresh}
          <span>Retry</span>
        </button>
        <button class="secondary-button" type="button" data-action="delete-workspace" data-workspace-id="${escapeHtml(workspace.id)}">Remove</button>
      </div>
    </section>
  `;
}

function renderSettingsOverlay(workspaces: services.Workspace[]): string {
  const hasSettingsValues = Boolean(fieldValue("endpoint").trim() || fieldValue("model").trim());
  return `
    <div class="overlay" role="dialog" aria-modal="true" aria-labelledby="settings-title">
      <form class="settings-panel" data-settings-form>
        <header class="settings-header">
          <div>
            <p class="eyebrow">Settings</p>
            <h2 id="settings-title">LLM Configuration</h2>
          </div>
          <button class="icon-button close-button" type="button" title="Close" aria-label="Close settings" data-action="close-settings">
            ${icons.x}
          </button>
        </header>

        ${formError ? `<p class="form-error" role="alert">${escapeHtml(formError)}</p>` : ""}
        ${hasSettingsValues ? "" : `<p class="empty-state compact">No settings are loaded. Enter an OpenAI-compatible endpoint and model to recover.</p>`}

        <div class="settings-grid">
          <label class="field field-wide">
            <span>Endpoint</span>
            <input name="endpoint" required type="url" value="${escapeHtml(fieldValue("endpoint"))}" autocomplete="off" data-initial-focus />
          </label>
          <label class="field field-wide">
            <span>SearXNG URL</span>
            <input name="searxngUrl" type="url" value="${escapeHtml(fieldValue("searxngUrl"))}" autocomplete="off" />
          </label>
          <label class="field field-wide">
            <span>Model</span>
            <input name="model" required type="text" value="${escapeHtml(fieldValue("model"))}" autocomplete="off" />
          </label>
          <label class="field">
            <span>Temperature</span>
            <input name="temperature" type="number" min="0" max="2" step="0.01" value="${escapeHtml(fieldValue("temperature"))}" />
          </label>
          <label class="field">
            <span>Top K</span>
            <input name="topK" type="number" min="0" step="1" value="${escapeHtml(fieldValue("topK"))}" />
          </label>
          <label class="field">
            <span>Top P</span>
            <input name="topP" type="number" min="0" max="1" step="0.01" value="${escapeHtml(fieldValue("topP"))}" />
          </label>
          <label class="field">
            <span>Min P</span>
            <input name="minP" type="number" min="0" max="1" step="0.01" value="${escapeHtml(fieldValue("minP"))}" />
          </label>
          <label class="field">
            <span>Context Length</span>
            <input name="contextLength" type="number" min="1" step="1" value="${escapeHtml(fieldValue("contextLength"))}" />
          </label>
          <label class="field">
            <span>Max Tokens</span>
            <input name="maxTokens" type="number" min="1" step="1" value="${escapeHtml(fieldValue("maxTokens"))}" />
          </label>
          <label class="field">
            <span>Timeout Seconds</span>
            <input name="timeoutSeconds" type="number" min="1" step="1" value="${escapeHtml(fieldValue("timeoutSeconds"))}" />
          </label>
          <label class="field">
            <span>Frequency Penalty</span>
            <input name="frequencyPenalty" type="number" min="-2" max="2" step="0.01" value="${escapeHtml(fieldValue("frequencyPenalty"))}" />
          </label>
          <label class="field">
            <span>Presence Penalty</span>
            <input name="presencePenalty" type="number" min="-2" max="2" step="0.01" value="${escapeHtml(fieldValue("presencePenalty"))}" />
          </label>
          <label class="field">
            <span>Repetition Penalty</span>
            <input name="repetitionPenalty" type="number" min="0" step="0.01" value="${escapeHtml(fieldValue("repetitionPenalty"))}" />
          </label>
        </div>

        <section class="workspace-settings" aria-labelledby="workspace-settings-title">
          <h3 id="workspace-settings-title">Workspaces</h3>
          <div class="workspace-list">
            ${
              workspaces.length
                ? workspaces
                    .map(
                      (workspace) => `
                        <div class="workspace-row">
                          <div class="workspace-row-main">
                            <strong>${escapeHtml(workspace.displayName)}${workspace.missing ? " - Missing" : ""}</strong>
                            <span>${escapeHtml(workspace.folderPath)}</span>
                          </div>
                          <div class="workspace-icon-setting" aria-label="Workspace icon for ${escapeAttribute(workspace.displayName)}">
                            <span class="workspace-icon-preview" aria-hidden="true">${renderWorkspaceIcon(workspace)}</span>
                            <button class="icon-button" type="button" title="Choose workspace icon" aria-label="Choose icon for ${escapeAttribute(workspace.displayName)}" data-action="choose-workspace-icon" data-workspace-id="${escapeAttribute(workspace.id)}">
                              ${icons.image}
                            </button>
                            <button class="icon-button" type="button" title="Clear workspace icon" aria-label="Clear icon for ${escapeAttribute(workspace.displayName)}" data-action="clear-workspace-icon" data-workspace-id="${escapeAttribute(workspace.id)}" ${(workspace.iconUrl ?? "").trim() ? "" : "disabled"}>
                              ${icons.x}
                            </button>
                          </div>
                          <label class="workspace-letter-field">
                            <span>Label</span>
                            <input
                              name="workspaceLetter"
                              type="text"
                              value="${escapeHtml(workspaceLetterDraft(workspace))}"
                              aria-label="Workspace icon label for ${escapeHtml(workspace.displayName)}"
                              data-workspace-letter
                              data-workspace-id="${escapeHtml(workspace.id)}"
                            />
                          </label>
                          <button class="icon-button danger-button" type="button" title="Delete workspace" aria-label="Delete ${escapeHtml(workspace.displayName)}" data-action="delete-workspace" data-workspace-id="${escapeHtml(workspace.id)}">
                            ${icons.trash}
                          </button>
                        </div>
                      `,
                    )
                    .join("")
                : `<p class="empty-state compact">No workspaces added.</p>`
            }
          </div>
        </section>

        <footer class="settings-footer">
          <button class="secondary-button" type="button" data-action="reset-settings">Reset</button>
          <button class="primary-button" type="submit">Save</button>
        </footer>
      </form>
    </div>
  `;
}

function bindEvents() {
  bindActionEvents(appRoot);

  const form = appRoot.querySelector<HTMLFormElement>("[data-settings-form]");
  form?.addEventListener("submit", handleSettingsSubmit);
  form
    ?.querySelectorAll<HTMLInputElement>("input")
    .forEach((input) => input.addEventListener("input", handleSettingsInput));

  bindChatEvents(appRoot);
  bindCardDescriptionEvents(appRoot);
  bindCardMessageEvents(appRoot);
  bindCodeViewEvents(appRoot, codeViewCallbacks());

  // Context menu on workspace gutter buttons
  appRoot.querySelectorAll<HTMLElement>('[data-action="activate-workspace"]').forEach((button) => {
    button.addEventListener("contextmenu", (event: MouseEvent) => {
      event.preventDefault();
      const workspaceId = button.dataset.workspaceId ?? "";
      const folderPath = button.title ?? "";
      if (!workspaceId || !folderPath) {
        return;
      }
      contextMenu = { workspaceId, folderPath, x: event.clientX, y: event.clientY };
      render();

      // Clamp to viewport boundaries so the menu stays fully visible
      const menuEl = appRoot.querySelector<HTMLElement>("[data-context-menu]");
      if (menuEl && contextMenu) {
        const rect = menuEl.getBoundingClientRect();
        let newX = contextMenu.x;
        let newY = contextMenu.y;

        if (rect.right > window.innerWidth) {
          newX = Math.max(0, window.innerWidth - rect.width - 4);
        }
        if (rect.bottom > window.innerHeight) {
          newY = Math.max(0, window.innerHeight - rect.height - 4);
        }

        if (newX !== contextMenu.x || newY !== contextMenu.y) {
          contextMenu = { ...contextMenu, x: newX, y: newY };
          render();
        }
      }
    });
  });

  // Dismiss context menu on outside pointer down
  document.addEventListener(
    "pointerdown",
    (event: PointerEvent) => {
      if (!contextMenu) {
        return;
      }
      const target = event.target;
      if (!(target instanceof Node)) {
        return;
      }
      const menuEl = appRoot.querySelector<HTMLElement>("[data-context-menu]");
      if (menuEl && menuEl.contains(target)) {
        return;
      }
      dismissContextMenu();
    },
    true,
  );
}

function codeViewCallbacks() {
  return {
    render,
    pushToast,
    errorMessage,
  };
}

function bindActionEvents(root: ParentNode) {
  root.querySelectorAll<HTMLElement>("[data-action]").forEach((element) => {
    element.addEventListener("click", handleAction);
  });
}

function bindChatEvents(root: ParentNode) {
  const chatForm = root.querySelector<HTMLFormElement>("[data-chat-form]");
  chatForm?.addEventListener("submit", handleChatSubmit);
  root
    .querySelectorAll<HTMLTextAreaElement>("[data-chat-input]")
    .forEach((input) => {
      input.addEventListener("input", handleChatInput);
      input.addEventListener("keydown", handleChatKeydown);
      input.addEventListener("paste", handleChatPaste);
    });
  root
    .querySelectorAll<HTMLInputElement>("[data-chat-plan-toggle]")
    .forEach((input) => input.addEventListener("change", handleChatPlanModeChange));
  bindChatMentionOptions(root);
  bindChatFileLinks(root);
  bindChatEditForms(root);
}

function bindCardMessageEvents(root: ParentNode) {
  const form = root.querySelector<HTMLFormElement>("[data-card-message-form]");
  form?.addEventListener("submit", handleCardMessageSubmit);
  root
    .querySelectorAll<HTMLTextAreaElement>("[data-card-message-input]")
    .forEach((input) => input.addEventListener("input", handleCardMessageInput));
}

function bindChatEditForms(root: ParentNode) {
  root
    .querySelectorAll<HTMLFormElement>("[data-chat-edit-form]")
    .forEach((form) => form.addEventListener("submit", handleChatEditSubmit));
  root
    .querySelectorAll<HTMLTextAreaElement>("[data-chat-edit-input]")
    .forEach((input) => {
      input.addEventListener("keydown", handleChatEditKeydown);
    });
}

function bindCardDescriptionEvents(root: ParentNode) {
  const form = root.querySelector<HTMLFormElement>("[data-card-description-form]");
  form?.addEventListener("submit", handleCardDescriptionSubmit);
  root
    .querySelectorAll<HTMLTextAreaElement>("[data-card-description-input]")
    .forEach((input) => input.addEventListener("input", handleCardDescriptionInput));
}

function focusInitialElement() {
  const dialog = appRoot.querySelector<HTMLElement>('[role="dialog"]');
  if (!dialog) {
    return;
  }
  if (document.activeElement && dialog.contains(document.activeElement)) {
    return;
  }
  const focusTarget = dialog.querySelector<HTMLElement>(
    "[data-initial-focus], input, textarea, button:not(:disabled), [tabindex]:not([tabindex='-1'])",
  );
  focusTarget?.focus();
}

function handleGlobalPointerDown(event: PointerEvent) {
  if (!chatMention) {
    return;
  }
  const target = event.target;
  if (!(target instanceof Node)) {
    return;
  }
  const form = appRoot.querySelector<HTMLElement>("[data-chat-form]");
  if (form?.contains(target)) {
    return;
  }
  clearChatMention();
  patchChatMentionPicker();
}

function handleGlobalKeydown(event: KeyboardEvent) {
  if (appMode === "code" && !settingsOpen) {
    const workspace = activeWorkspace();
    if (
      workspace &&
      handleCodeTabSwitcherKeydown(workspace.id, codeViewCallbacks(), event)
    ) {
      return;
    }
  }
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") {
    if (appMode !== "code" || settingsOpen) {
      return;
    }
    const workspace = activeWorkspace();
    if (!workspace) {
      return;
    }
    event.preventDefault();
    void saveActiveCodeFile(workspace.id, codeViewCallbacks());
    return;
  }
  if (chatMention) {
    if (event.key !== "Escape") {
      return;
    }
    event.preventDefault();
    clearChatMention();
    patchChatMentionPicker();
    return;
  }
  const workspace = activeWorkspace();
  if (
    workspace &&
    (openChangeReviewWorkspaces.has(workspace.id) || openGitChangeWorkspaces.has(workspace.id)) &&
    (event.key === "ArrowDown" || event.key === "ArrowUp")
  ) {
    const target = event.target;
    if (
      target instanceof HTMLInputElement ||
      target instanceof HTMLTextAreaElement ||
      target instanceof HTMLSelectElement
    ) {
      return;
    }
    event.preventDefault();
    const direction = event.key === "ArrowDown" ? 1 : -1;
    if (event.ctrlKey || event.metaKey) {
      scrollChangeReviewFile(direction);
    } else {
      scrollChangeReview(direction);
    }
    return;
  }
  if (event.key !== "Escape") {
    return;
  }
  if (settingsOpen) {
    event.preventDefault();
    settingsOpen = false;
    formError = "";
    render();
    return;
  }
  if (workspace && openGitChangeWorkspaces.has(workspace.id)) {
    event.preventDefault();
    openGitChangeWorkspaces.delete(workspace.id);
    expandedGitChangeWorkspaces.delete(workspace.id);
    loadingGitChangeWorkspaces.delete(workspace.id);
    render();
    return;
  }
  if (appMode === "code") {
    return;
  }
  if (!workspace) {
    return;
  }
  if (openChangeReviewWorkspaces.has(workspace.id)) {
    event.preventDefault();
    openChangeReviewWorkspaces.delete(workspace.id);
    render();
    return;
  }
  const cardID = selectedKanbanCards.get(workspace.id) ?? "";
  if (!cardID) {
    return;
  }
  event.preventDefault();
  void closeSelectedCardDetail(workspace.id).finally(render);
}

function handleGlobalKeyup(event: KeyboardEvent) {
  if (appMode !== "code" || settingsOpen || event.key !== "Control") {
    return;
  }
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  if (finishCodeTabSwitcher(workspace.id, codeViewCallbacks())) {
    event.preventDefault();
  }
}

function handleGlobalWindowBlur() {
  if (appMode !== "code") {
    return;
  }
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  finishCodeTabSwitcher(workspace.id, codeViewCallbacks());
}

function scrollChangeReview(direction: 1 | -1) {
  const review = appRoot.querySelector<HTMLElement>("[data-change-review]");
  if (!review) {
    return;
  }
  const changes = Array.from(review.querySelectorAll<HTMLElement>("[data-change-line]"));
  if (!changes.length) {
    return;
  }

  const currentIndex = changes.findIndex((change) =>
    change.classList.contains("is-current"),
  );
  let targetIndex: number;
  if (direction > 0) {
    targetIndex = currentIndex < 0 ? 0 : (currentIndex + 1) % changes.length;
  } else {
    targetIndex = currentIndex <= 0 ? changes.length - 1 : currentIndex - 1;
  }
  const target = changes[targetIndex];
  markCurrentChangeTarget(review, target);
  target.scrollIntoView({ behavior: "smooth", block: "center" });
}

function scrollChangeReviewFile(direction: 1 | -1) {
  const review = appRoot.querySelector<HTMLElement>("[data-change-review]");
  if (!review) {
    return;
  }
  const files = Array.from(review.querySelectorAll<HTMLElement>("[data-change-file]"));
  if (!files.length) {
    return;
  }

  const currentIndex = currentChangeFileIndex(files);
  let targetIndex: number;
  if (direction > 0) {
    targetIndex = currentIndex < 0 ? 0 : (currentIndex + 1) % files.length;
  } else {
    targetIndex = currentIndex <= 0 ? files.length - 1 : currentIndex - 1;
  }
  const targetFile = files[targetIndex];
  const fileChanges = Array.from(targetFile.querySelectorAll<HTMLElement>("[data-change-line]"));
  const targetLine = direction > 0 ? fileChanges[0] : fileChanges[fileChanges.length - 1];
  markCurrentChangeTarget(review, targetLine ?? targetFile);
  targetFile.scrollIntoView({ behavior: "smooth", block: "start" });
}

function currentChangeFileIndex(files: HTMLElement[]): number {
  const currentFileIndex = files.findIndex((file) =>
    file.classList.contains("is-current"),
  );
  if (currentFileIndex >= 0) {
    return currentFileIndex;
  }
  return files.findIndex((file) =>
    Boolean(file.querySelector("[data-change-line].is-current")),
  );
}

function markCurrentChangeTarget(review: HTMLElement, target: HTMLElement) {
  review
    .querySelectorAll<HTMLElement>("[data-change-line].is-current")
    .forEach((change) => change.classList.remove("is-current"));
  review
    .querySelectorAll<HTMLElement>("[data-change-file].is-current")
    .forEach((file) => file.classList.remove("is-current"));

  const targetFile = target.closest<HTMLElement>("[data-change-file]");
  targetFile?.classList.add("is-current");
  if (target.matches("[data-change-line]")) {
    target.classList.add("is-current");
  }
}

async function handleAction(event: Event) {
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
      try {
        await OpenWorkspaceExplorer(workspaceID);
        pushToast("Opened folder in Explorer.", "success");
      } catch (error) {
        pushToast(errorMessage(error), "error");
      }
      dismissContextMenu();
      return;
    }
    if (action === "open-code-view") {
      const workspace = activeWorkspace();
      if (!workspace || workspace.missing) {
        return;
      }
      appMode = "code";
      const loading = ensureCodeViewRootLoaded(workspace.id);
      render();
      await loading;
      render();
      return;
    }
    if (action === "close-code-view") {
      const workspace = activeWorkspace();
      if (workspace) {
        clearCodeTabSwitcher(workspace.id);
        openGitChangeWorkspaces.delete(workspace.id);
        expandedGitChangeWorkspaces.delete(workspace.id);
        loadingGitChangeWorkspaces.delete(workspace.id);
      }
      appMode = "chat-kanban";
      render();
      return;
    }
    if (action === "open-git-changes") {
      const workspace = activeWorkspace();
      if (!workspace || workspace.missing) {
        return;
      }
      openGitChangeWorkspaces.add(workspace.id);
      loadingGitChangeWorkspaces.add(workspace.id);
      render();
      try {
        gitChangeReviews.set(workspace.id, await LoadWorkspaceGitChanges(workspace.id));
      } catch (error) {
        openGitChangeWorkspaces.delete(workspace.id);
        expandedGitChangeWorkspaces.delete(workspace.id);
        throw error;
      } finally {
        loadingGitChangeWorkspaces.delete(workspace.id);
      }
      render();
      return;
    }
    if (action === "close-git-changes") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      openGitChangeWorkspaces.delete(workspace.id);
      expandedGitChangeWorkspaces.delete(workspace.id);
      loadingGitChangeWorkspaces.delete(workspace.id);
      render();
      return;
    }
    if (action === "toggle-git-changes-size") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      if (expandedGitChangeWorkspaces.has(workspace.id)) {
        expandedGitChangeWorkspaces.delete(workspace.id);
      } else {
        expandedGitChangeWorkspaces.add(workspace.id);
      }
      render();
      return;
    }
    if (action === "refresh-git-changes") {
      const workspace = activeWorkspace();
      if (!workspace || loadingGitChangeWorkspaces.has(workspace.id)) {
        return;
      }
      loadingGitChangeWorkspaces.add(workspace.id);
      render();
      try {
        gitChangeReviews.set(workspace.id, await LoadWorkspaceGitChanges(workspace.id));
      } finally {
        loadingGitChangeWorkspaces.delete(workspace.id);
      }
      render();
      return;
    }
    if (action === "open-settings") {
      const workspace = activeWorkspace();
      if (workspace) {
        clearCodeTabSwitcher(workspace.id);
      }
      settingsOpen = true;
      formError = "";
      settingsDraft ??= cloneSettings(appState!.settings);
      hydrateWorkspaceLetterDrafts(appState?.workspaces ?? []);
      render();
    }
    if (action === "close-settings") {
      settingsOpen = false;
      formError = "";
      render();
    }
    if (action === "reset-settings") {
      settingsDraft = cloneSettings(appState!.settings);
      hydrateWorkspaceLetterDrafts(appState?.workspaces ?? []);
      formError = "";
      render();
    }
    if (action === "add-workspace") {
      appState = await ChooseWorkspaceFolder();
      await loadActiveChatSession();
      await loadActiveKanbanBoard();
      await loadActiveChangeReview();
      await loadActiveCodeViewIfNeeded();
      pushToast("Workspace list updated.", "success");
      render();
    }
    if (action === "refresh-workspaces") {
      appState = await LoadState();
      await loadActiveChatSession();
      await loadActiveKanbanBoard();
      await loadActiveChangeReview();
      await loadActiveCodeViewIfNeeded();
      pushToast(
        activeWorkspace()?.missing
          ? "Workspace folder is still unavailable."
          : "Workspace folder recovered.",
        activeWorkspace()?.missing ? "error" : "success",
      );
      render();
    }
    if (action === "choose-workspace-icon") {
      appState = await ChooseWorkspaceIcon(workspaceID);
      pushToast("Workspace icon updated.", "success");
      render();
    }
    if (action === "clear-workspace-icon") {
      appState = await ClearWorkspaceIcon(workspaceID);
      pushToast("Workspace icon cleared.", "success");
      render();
    }
    if (action === "activate-workspace") {
      const current = activeWorkspace();
      if (current && current.id !== workspaceID) {
        await closeSelectedCardDetail(current.id);
        openChangeReviewWorkspaces.delete(current.id);
        openGitChangeWorkspaces.delete(current.id);
        expandedGitChangeWorkspaces.delete(current.id);
        loadingGitChangeWorkspaces.delete(current.id);
      }
      appState = await SetActiveWorkspace(workspaceID);
      await loadActiveChatSession();
      await loadActiveKanbanBoard();
      await loadActiveChangeReview();
      await loadActiveCodeViewIfNeeded();
      render();
    }
    if (action === "execute-plan") {
      const workspace = activeWorkspace();
      if (!workspace || executingPlans.has(workspace.id)) {
        return;
      }
      executingPlans.add(workspace.id);
      render();
      try {
        kanbanBoards.set(workspace.id, await ExecutePlan(workspace.id));
        pushToast("Plan converted into Ready cards.", "success");
      } finally {
        executingPlans.delete(workspace.id);
      }
      render();
    }
    if (action === "toggle-chat-size") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      if (expandedChatWorkspaces.has(workspace.id)) {
        expandedChatWorkspaces.delete(workspace.id);
      } else {
        expandedChatWorkspaces.add(workspace.id);
        expandedKanbanWorkspaces.delete(workspace.id);
      }
      render();
    }
    if (action === "toggle-kanban-size") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      if (expandedKanbanWorkspaces.has(workspace.id)) {
        expandedKanbanWorkspaces.delete(workspace.id);
      } else {
        expandedKanbanWorkspaces.add(workspace.id);
        expandedChatWorkspaces.delete(workspace.id);
      }
      render();
    }
    if (action === "open-change-review") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      await closeSelectedCardDetail(workspace.id);
      changeReviews.set(workspace.id, await LoadWorkspaceChangeReview(workspace.id));
      openChangeReviewWorkspaces.add(workspace.id);
      render();
    }
    if (action === "close-change-review") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      openChangeReviewWorkspaces.delete(workspace.id);
      expandedChangeReviewWorkspaces.delete(workspace.id);
      render();
    }
    if (action === "toggle-change-review-size") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      if (expandedChangeReviewWorkspaces.has(workspace.id)) {
        expandedChangeReviewWorkspaces.delete(workspace.id);
      } else {
        expandedChangeReviewWorkspaces.add(workspace.id);
      }
      render();
    }
    if (action === "clear-change-review") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      changeReviews.set(workspace.id, await ClearWorkspaceChangeReview(workspace.id));
      pushToast("AI change review cleared.");
      render();
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
      chatImageDrafts.set(
        workspace.id,
        chatImageDraftsFor(workspace.id).filter((image) => image.id !== imageID),
      );
      patchChatPanel();
      patchChatControls();
    }
    if (action === "start-agents") {
      const workspace = activeWorkspace();
      if (!workspace || runningKanbanWorkspaces.has(workspace.id)) {
        return;
      }
      markKanbanRunStarted(workspace.id);
      render();
      try {
        kanbanBoards.set(workspace.id, await StartKanbanExecution(workspace.id, 2));
      } catch (error) {
        forgetKanbanRun(workspace.id);
        throw error;
      }
      pushToast("Kanban agents started.", "success");
      render();
    }
    if (action === "stop-agents") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      kanbanBoards.set(workspace.id, await StopKanbanExecution(workspace.id));
      finishKanbanRun(workspace.id);
      pushToast("Kanban agents stopped.");
      render();
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
      kanbanBoards.set(workspace.id, board);
      const selectedID = selectedKanbanCards.get(workspace.id);
      if (selectedID && !kanbanCards(board).some((card) => card.id === selectedID)) {
        selectedKanbanCards.delete(workspace.id);
      }
      const clearedCount = beforeDoneCount - (board.done?.length ?? 0);
      pushToast(
        clearedCount > 0
          ? `${clearedCount} done card${clearedCount === 1 ? "" : "s"} cleared.`
          : "Done cards are still needed by unfinished cards.",
        clearedCount > 0 ? "success" : "info",
      );
      render();
    }
    if (action === "open-card") {
      const workspace = activeWorkspace();
      const cardID = target.dataset.cardId ?? "";
      if (!workspace || !cardID) {
        return;
      }
      selectedKanbanCards.set(workspace.id, cardID);
      kanbanBoards.set(workspace.id, await OpenKanbanCardDetail(workspace.id, cardID));
      render();
    }
    if (action === "stop-card") {
      const workspace = activeWorkspace();
      const cardID = target.dataset.cardId ?? "";
      if (!workspace || !cardID) {
        return;
      }
      kanbanBoards.set(workspace.id, await StopKanbanCard(workspace.id, cardID));
      selectedKanbanCards.set(workspace.id, cardID);
      pushToast("Card agent stopped.");
      render();
    }
    if (action === "reset-card") {
      const workspace = activeWorkspace();
      const cardID = target.dataset.cardId ?? "";
      if (!workspace || !cardID || !window.confirm("Reset this card and clear its progress transcript?")) {
        return;
      }
      kanbanBoards.set(workspace.id, await ResetKanbanCard(workspace.id, cardID));
      selectedKanbanCards.set(workspace.id, cardID);
      pushToast("Card reset.", "success");
      render();
    }
    if (action === "close-card") {
      const workspace = activeWorkspace();
      if (workspace) {
        const cardID = selectedKanbanCards.get(workspace.id) ?? "";
        if (cardID) {
          kanbanBoards.set(workspace.id, await CloseKanbanCardDetail(workspace.id, cardID));
        }
        selectedKanbanCards.delete(workspace.id);
      }
      render();
    }
    if (action === "move-card") {
      const workspace = activeWorkspace();
      const cardID = target.dataset.cardId ?? "";
      const lane = target.dataset.lane ?? "";
      if (!workspace || !cardID || !lane) {
        return;
      }
      kanbanBoards.set(workspace.id, await MoveKanbanCard(workspace.id, cardID, lane));
      selectedKanbanCards.set(workspace.id, cardID);
      pushToast(`Card moved to ${laneLabel(lane)}.`, "success");
      render();
    }
    if (action === "create-card-from-message") {
      const workspace = activeWorkspace();
      const messageID = target.dataset.messageId ?? "";
      if (!workspace || !messageID) {
        return;
      }
      kanbanBoards.set(workspace.id, await CreateKanbanCardFromChatMessage(workspace.id, messageID));
      pushToast("Message converted into a Ready card.", "success");
      render();
      return;
    }
    if (action === "retry-message") {
      const workspace = activeWorkspace();
      const messageID = target.dataset.messageId ?? "";
      if (!workspace || !messageID) {
        return;
      }
      editingMessageIds.delete(messageID);
      try {
        chatSessions.set(workspace.id, await RetryChatMessage(workspace.id, messageID));
        pushToast("Response regenerated.", "success");
      } catch (error) {
        pushToast(errorMessage(error), "error");
      }
      render();
      scrollChatToBottom();
      return;
    }
    if (action === "stop-chat") {
      const workspace = activeWorkspace();
      if (!workspace) {
        return;
      }
      chatSessions.set(workspace.id, await StopChatStream(workspace.id));
      patchChatPanel();
    }
    if (action === "clear-chat") {
      const workspace = activeWorkspace();
      if (!workspace || !window.confirm("Clear the current chat?")) {
        return;
      }
      chatSessions.set(workspace.id, await ClearChat(workspace.id));
      chatDrafts.set(workspace.id, "");
      chatImageDrafts.delete(workspace.id);
      patchChatPanel();
    }
    if (action === "edit-message") {
      const workspace = activeWorkspace();
      const messageID = target.dataset.messageId ?? "";
      if (!workspace || !messageID) {
        return;
      }
      editingMessageIds.add(messageID);
      render();
      const textarea = appRoot.querySelector<HTMLTextAreaElement>(`[data-chat-edit-input][data-message-id="${CSS.escape(messageID)}"]`);
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
        editingMessageIds.delete(messageID);
        render();
      }
      return;
    }
    if (action === "delete-workspace") {
      const workspace = appState?.workspaces.find((item) => item.id === workspaceID);
      if (!workspace || !window.confirm(`Delete ${workspace.displayName} from Echo?`)) {
        return;
      }
      await closeSelectedCardDetail(workspaceID);
      appState = await DeleteWorkspace(workspaceID);
      kanbanBoards.delete(workspaceID);
      changeReviews.delete(workspaceID);
      gitChangeReviews.delete(workspaceID);
      selectedKanbanCards.delete(workspaceID);
      openChangeReviewWorkspaces.delete(workspaceID);
      openGitChangeWorkspaces.delete(workspaceID);
      expandedChatWorkspaces.delete(workspaceID);
      expandedKanbanWorkspaces.delete(workspaceID);
      expandedChangeReviewWorkspaces.delete(workspaceID);
      expandedGitChangeWorkspaces.delete(workspaceID);
      loadingGitChangeWorkspaces.delete(workspaceID);
      chatPlanModes.delete(workspaceID);
      chatImageDrafts.delete(workspaceID);
      forgetKanbanRun(workspaceID);
      if (!activeWorkspace() || activeWorkspace()?.missing) {
        appMode = "chat-kanban";
      } else {
        await loadActiveCodeViewIfNeeded();
      }
      pushToast("Workspace removed.", "success");
      render();
    }
  } catch (error) {
    const message = errorMessage(error);
    if (settingsOpen) {
      formError = message;
    } else {
      formError = "";
      pushToast(message, "error");
    }
    render();
  }
}

function handleCardDescriptionInput(event: Event) {
  const workspace = activeWorkspace();
  const card = workspace ? selectedKanbanCard(kanbanBoardFor(workspace.id)) : null;
  if (!workspace || !card) {
    return;
  }
  const input = event.currentTarget as HTMLTextAreaElement;
  const button = input.form?.querySelector<HTMLButtonElement>('button[type="submit"]');
  if (button) {
    const nextDescription = input.value.trim();
    button.disabled = nextDescription.length === 0 || nextDescription === card.description.trim();
  }
}

async function handleCardDescriptionSubmit(event: SubmitEvent) {
  event.preventDefault();
  const workspace = activeWorkspace();
  const form = event.currentTarget as HTMLFormElement;
  const cardID = form.dataset.cardId ?? "";
  const input = form.querySelector<HTMLTextAreaElement>("[data-card-description-input]");
  if (!workspace || !cardID || !input) {
    return;
  }
  const description = input.value.trim();
  if (!description) {
    return;
  }

  try {
    kanbanBoards.set(workspace.id, await UpdateKanbanCardDescription(workspace.id, cardID, description));
    selectedKanbanCards.set(workspace.id, cardID);
    pushToast("Card description updated.", "success");
    render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    render();
  }
}

function handleCardMessageInput(event: Event) {
  const workspace = activeWorkspace();
  const card = workspace ? selectedKanbanCard(kanbanBoardFor(workspace.id)) : null;
  if (!workspace || !card) {
    return;
  }
  const input = event.currentTarget as HTMLTextAreaElement;
  const key = `${workspace.id}:${card.id}`;
  cardMessageDrafts.set(key, input.value);
  const button = input.form?.querySelector<HTMLButtonElement>('button[type="submit"]');
  if (button) {
    button.disabled = input.value.trim().length === 0;
  }
}

async function handleCardMessageSubmit(event: SubmitEvent) {
  event.preventDefault();
  const workspace = activeWorkspace();
  const form = event.currentTarget as HTMLFormElement;
  const cardID = form.dataset.cardId ?? "";
  if (!workspace || !cardID) {
    return;
  }
  const key = `${workspace.id}:${cardID}`;
  const message = (cardMessageDrafts.get(key) ?? "").trim();
  if (!message) {
    return;
  }

  try {
    kanbanBoards.set(workspace.id, await AddKanbanCardMessage(workspace.id, cardID, message));
    cardMessageDrafts.delete(key);
    selectedKanbanCards.set(workspace.id, cardID);
    pushToast("Card returned to Ready.", "success");
    render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    render();
  }
}

function handleSettingsInput(event: Event) {
  const input = event.currentTarget as HTMLInputElement;
  if (input.dataset.workspaceLetter !== undefined) {
    workspaceLetterDrafts.set(input.dataset.workspaceId ?? "", input.value);
    formError = "";
    return;
  }
  if (!settingsDraft) {
    return;
  }

  const numericFields = new Set([
    "temperature",
    "topK",
    "topP",
    "minP",
    "contextLength",
    "maxTokens",
    "frequencyPenalty",
    "presencePenalty",
    "repetitionPenalty",
    "timeoutSeconds",
  ]);
  const value = numericFields.has(input.name) ? Number(input.value) : input.value;
  settingsDraft = llm.Settings.createFrom({
    ...settingsDraft,
    [input.name]: Number.isNaN(value) ? 0 : value,
  });
  formError = "";
}

async function handleSettingsSubmit(event: SubmitEvent) {
  event.preventDefault();
  if (!settingsDraft) {
    return;
  }
  if (!settingsDraft.endpoint.trim()) {
    formError = "Endpoint is required.";
    render();
    return;
  }
  if (!settingsDraft.model.trim()) {
    formError = "Model is required.";
    render();
    return;
  }

  try {
    appState = await SaveSettings(settingsDraft);
    for (const workspace of appState.workspaces ?? []) {
      const draft = workspaceLetterDrafts.get(workspace.id);
      if (draft !== undefined && draft !== (workspace.letter ?? "")) {
        appState = await SetWorkspaceLetter(workspace.id, draft);
      }
    }
    settingsDraft = cloneSettings(appState.settings);
    hydrateWorkspaceLetterDrafts(appState.workspaces ?? []);
    settingsOpen = false;
    formError = "";
    pushToast("Settings saved.", "success");
    render();
  } catch (error) {
    formError = errorMessage(error);
    render();
  }
}

function handleChatInput(event: Event) {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  const input = event.currentTarget as HTMLTextAreaElement;
  chatDrafts.set(workspace.id, input.value);
  syncChatMentionForInput(workspace.id, input);
  patchChatControls();
}

function handleChatPaste(event: ClipboardEvent) {
  const workspace = activeWorkspace();
  if (!workspace || chatSessionFor(workspace.id).busy || executingPlans.has(workspace.id)) {
    return;
  }
  const files = Array.from(event.clipboardData?.items ?? [])
    .filter((item) => item.kind === "file")
    .map((item) => item.getAsFile())
    .filter((file): file is File => file !== null && file.type.startsWith("image/"));
  if (!files.length) {
    return;
  }
  event.preventDefault();
  void addPastedChatImages(workspace.id, files);
}

async function addPastedChatImages(workspaceID: string, files: File[]) {
  const current = chatImageDraftsFor(workspaceID);
  const accepted: ChatImageDraft[] = [];
  let totalBytes = chatImageDraftTotalBytes(workspaceID);
  for (const file of files) {
    const mediaType = file.type.toLowerCase();
    if (!isSupportedChatImageType(mediaType)) {
      pushToast(`Unsupported image format: ${file.type || file.name}`, "error");
      continue;
    }
    if (file.size > maxChatImageBytes) {
      pushToast(`${file.name || "Pasted image"} is larger than ${formatBytes(maxChatImageBytes)}.`, "error");
      continue;
    }
    if (current.length + accepted.length >= maxChatImageDrafts) {
      pushToast(`A message can include at most ${maxChatImageDrafts} images.`, "error");
      break;
    }
    if (totalBytes + file.size > maxChatImageMessageBytes) {
      pushToast(`Attached images are larger than ${formatBytes(maxChatImageMessageBytes)}.`, "error");
      break;
    }
    try {
      accepted.push({
        id: `draft-${Date.now()}-${Math.random().toString(36).slice(2)}`,
        name: file.name || `pasted-image-${current.length + accepted.length + 1}`,
        mediaType,
        dataUrl: await fileToDataURL(file),
        bytes: file.size,
      });
      totalBytes += file.size;
    } catch (error) {
      pushToast(errorMessage(error), "error");
    }
  }
  if (!accepted.length) {
    return;
  }
  chatImageDrafts.set(workspaceID, [...current, ...accepted]);
  patchChatPanel();
  patchChatControls();
  appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]")?.focus();
}

function handleChatPlanModeChange(event: Event) {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  const input = event.currentTarget as HTMLInputElement;
  chatPlanModes.set(workspace.id, input.checked);
}

function handleChatKeydown(event: KeyboardEvent) {
  if (handleChatMentionKeydown(event)) {
    return;
  }
  if (event.key !== "Enter" || event.shiftKey || event.isComposing) {
    return;
  }
  event.preventDefault();
  const input = event.currentTarget as HTMLTextAreaElement;
  input.form?.requestSubmit();
}

function handleChatMentionKeydown(event: KeyboardEvent): boolean {
  const workspace = activeWorkspace();
  const input = event.currentTarget as HTMLTextAreaElement;
  const mention = workspace ? chatMentionFor(workspace.id) : null;
  if (!workspace || !mention) {
    return false;
  }
  const match = activeChatMentionMatch(input);
  if (!match || match.triggerStart !== mention.triggerStart) {
    clearChatMention();
    patchChatMentionPicker();
    return false;
  }
  if (event.key === "Escape") {
    event.preventDefault();
    event.stopPropagation();
    clearChatMention();
    patchChatMentionPicker();
    return true;
  }
  if (event.key === "ArrowDown") {
    event.preventDefault();
    event.stopPropagation();
    moveChatMentionSelection(1);
    return true;
  }
  if (event.key === "ArrowUp") {
    event.preventDefault();
    event.stopPropagation();
    moveChatMentionSelection(-1);
    return true;
  }
  if (event.key !== "Enter" && event.key !== "Tab") {
    return false;
  }
  const entries = visibleChatMentionEntries(mention);
  if (!entries.length) {
    return false;
  }
  event.preventDefault();
  event.stopPropagation();
  insertChatMention(entries[mention.selectedIndex] ?? entries[0]);
  return true;
}

async function handleChatSubmit(event: SubmitEvent) {
  event.preventDefault();
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  const message = (chatDrafts.get(workspace.id) ?? "").trim();
  const imageDrafts = chatImageDraftsFor(workspace.id);
  const session = chatSessionFor(workspace.id);
  if ((!message && imageDrafts.length === 0) || session.busy || executingPlans.has(workspace.id)) {
    return;
  }

  try {
    const nextSession = await SendChatMessageWithAttachments(
      workspace.id,
      services.ChatMessageRequest.createFrom({
        content: message,
        planMode: chatPlanModeFor(workspace.id),
        images: imageDrafts.map((image) =>
          services.ChatImageInput.createFrom({
            id: image.id,
            name: image.name,
            mediaType: image.mediaType,
            dataUrl: image.dataUrl,
            bytes: image.bytes,
          }),
        ),
      }),
    );
    chatDrafts.set(workspace.id, "");
    chatImageDrafts.delete(workspace.id);
    clearChatMention();
    chatSessions.set(workspace.id, nextSession);
    render();
    scrollChatToBottom();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    render();
  }
}

async function handleChatEditSubmit(event: SubmitEvent) {
  event.preventDefault();
  const workspace = activeWorkspace();
  const form = event.currentTarget as HTMLFormElement;
  const messageID = form.dataset.messageId ?? "";
  if (!workspace || !messageID) {
    return;
  }
  const textarea = form.querySelector<HTMLTextAreaElement>("[data-chat-edit-input]");
  const newContent = textarea?.value ?? "";
  const trimmed = newContent.trim();
  if (!trimmed) {
    pushToast("Message cannot be empty.", "error");
    return;
  }

  try {
    chatSessions.set(workspace.id, await EditChatMessage(workspace.id, messageID, trimmed));
    editingMessageIds.delete(messageID);
    render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    render();
  }
}

function handleChatEditKeydown(event: KeyboardEvent) {
  if (event.key === "Escape") {
    event.preventDefault();
    const input = event.currentTarget as HTMLTextAreaElement;
    const form = input.closest<HTMLFormElement>("[data-chat-edit-form]");
    const messageID = form?.dataset.messageId ?? "";
    if (messageID) {
      editingMessageIds.delete(messageID);
      render();
    }
    return;
  }
  if (event.key !== "Enter" || event.shiftKey || event.isComposing) {
    return;
  }
  event.preventDefault();
  const input = event.currentTarget as HTMLTextAreaElement;
  input.form?.requestSubmit();
}

async function loadActiveChatSession() {
  const workspace = activeWorkspace();
  if (!workspace || workspace.missing) {
    return;
  }
  chatSessions.set(workspace.id, await LoadChatSession(workspace.id));
}

async function loadActiveKanbanBoard() {
  const workspace = activeWorkspace();
  if (!workspace || workspace.missing) {
    return;
  }
  kanbanBoards.set(workspace.id, await LoadKanbanBoard(workspace.id));
}

async function loadActiveChangeReview() {
  const workspace = activeWorkspace();
  if (!workspace || workspace.missing) {
    return;
  }
  changeReviews.set(workspace.id, await LoadWorkspaceChangeReview(workspace.id));
}

async function loadActiveCodeViewIfNeeded() {
  if (appMode !== "code") {
    return;
  }
  const workspace = activeWorkspace();
  if (!workspace || workspace.missing) {
    appMode = "chat-kanban";
    return;
  }
  await ensureCodeViewRootLoaded(workspace.id);
}

async function closeSelectedCardDetail(workspaceID: string) {
  const cardID = selectedKanbanCards.get(workspaceID) ?? "";
  if (!cardID) {
    return;
  }
  try {
    kanbanBoards.set(workspaceID, await CloseKanbanCardDetail(workspaceID, cardID));
  } catch {
  } finally {
    selectedKanbanCards.delete(workspaceID);
  }
}

function applyChatStreamEvent(event: ChatStreamEvent) {
  const session = chatSessionFor(event.workspaceId);
  const messages = session.messages ?? [];
  const message = messages.find((item) => item.id === event.messageId);
  if (!message) {
    return;
  }

  if (event.type === "token") {
    message.content = `${message.content ?? ""}${event.content ?? ""}`;
  }
  if (event.type === "reasoning") {
    message.reasoning = `${message.reasoning ?? ""}${event.reasoning ?? ""}`;
  }
  if (event.type === "tool_call" && event.toolCall) {
    const toolCalls = message.toolCalls ?? [];
    const index = toolCalls.findIndex(
      (item) =>
        (item.id && item.id === event.toolCall?.id) ||
        (!item.id && item.name === event.toolCall?.name),
    );
    if (index >= 0) {
      toolCalls[index] = services.ChatToolActivity.createFrom(event.toolCall);
    } else {
      toolCalls.push(services.ChatToolActivity.createFrom(event.toolCall));
    }
    message.toolCalls = toolCalls;
  }
  if (event.type === "complete" || event.type === "canceled" || event.type === "error") {
    session.busy = false;
    session.streamId = "";
    message.status = event.type === "complete" ? "complete" : event.type;
    message.error = event.error ?? "";
  }
  if (event.type === "retrying") {
    message.status = "retrying";
    message.error = "";
  }

  chatSessions.set(event.workspaceId, session);
  if (activeWorkspace()?.id === event.workspaceId) {
    const keepChatPinned = isElementScrolledNearBottom(
      appRoot.querySelector<HTMLElement>("[data-chat-log]"),
    );
    patchChatMessage(message, event.type !== "token");
    patchChatControls();
    if (keepChatPinned) {
      scrollChatToBottom();
    }
  }
}

function applyKanbanEvent(event: KanbanEvent) {
  kanbanBoards.set(event.workspaceId, services.KanbanBoard.createFrom(event.board));
  if (event.type === "card_started") {
    markKanbanRunStarted(event.workspaceId);
  }
  if (event.type === "scheduler_complete") {
    finishKanbanRun(event.workspaceId);
    void refreshWorkspaceChangeReview(event.workspaceId);
  }
  if (activeWorkspace()?.id === event.workspaceId) {
    if (event.type === "card_progress" && patchOpenCardProgress(event)) {
      return;
    }
    render();
  }
}

function applyFileChangesEvent(event: FileChangesEvent) {
  void refreshOpenCodeTabsFromDisk(event.workspaceId, codeViewCallbacks());
  const existing = changeReviewFor(event.workspaceId);
  changeReviews.set(
    event.workspaceId,
    services.WorkspaceChangeReview.createFrom({
      ...existing,
      workspaceId: event.workspaceId,
      fileCount: event.fileCount,
      changeCount: event.changeCount,
    }),
  );
  if (openChangeReviewWorkspaces.has(event.workspaceId)) {
    void refreshWorkspaceChangeReview(event.workspaceId);
    return;
  }
  if (activeWorkspace()?.id === event.workspaceId) {
    render();
  }
}

async function refreshWorkspaceChangeReview(workspaceID: string) {
  try {
    changeReviews.set(workspaceID, await LoadWorkspaceChangeReview(workspaceID));
    if (activeWorkspace()?.id === workspaceID) {
      render();
    }
  } catch (error) {
    pushToast(errorMessage(error), "error");
  }
}

function patchChatMessage(message: services.ChatMessage, patchDebug = true) {
  const element = appRoot.querySelector<HTMLElement>(
    `[data-message-id="${CSS.escape(message.id)}"]`,
  );
  if (!element) {
    patchChatPanel();
    return;
  }
  const content = element.querySelector<HTMLElement>("[data-message-content]");
  if (content) {
    patchMarkdownElement(content, message.content ?? "");
  }
  const error = element.querySelector<HTMLElement>("[data-message-error]");
  if (error) {
    error.textContent = message.error ?? "";
    error.hidden = !message.error;
  }
  patchMessageStatus(element, message);
  patchMessageActions(element, message);
  const debugStack = element.querySelector<HTMLElement>("[data-debug-stack]");
  if (patchDebug && debugStack) {
    patchDebugSections(debugStack, message);
  }
  if (message.role === "assistant") {
    void linkifyAssistantFilePaths(element);
  }
}

function patchMessageStatus(element: HTMLElement, message: services.ChatMessage) {
  const header = element.querySelector<HTMLElement>("header");
  if (!header) {
    return;
  }
  const status = message.status && message.status !== "complete" ? message.status : "";
  let statusElement = header.querySelector<HTMLElement>("[data-message-status]");
  if (!status) {
    statusElement?.remove();
    return;
  }
  if (!statusElement) {
    statusElement = document.createElement("span");
    statusElement.dataset.messageStatus = "";
    header.insertBefore(statusElement, header.querySelector(".chat-message-actions"));
  }
  statusElement.textContent = status;
}

function patchMessageActions(element: HTMLElement, message: services.ChatMessage) {
  if (message.role !== "assistant") {
    return;
  }
  const retry = element.querySelector<HTMLButtonElement>(".chat-retry-trigger");
  if (retry) {
    const shouldShowSpinner = isAssistantMessageStreaming(message);
    const hasSpinner = Boolean(retry.querySelector(".spinner"));
    if (shouldShowSpinner && !hasSpinner) {
      retry.innerHTML = '<span class="spinner" aria-hidden="true"></span>';
    }
    if (!shouldShowSpinner && hasSpinner) {
      retry.innerHTML = icons.retry;
    }
  }
  const kanban = element.querySelector<HTMLButtonElement>(".chat-kanban-trigger");
  if (kanban) {
    kanban.disabled = !canCreateKanbanCardFromMessage(message);
  }
}

function patchDebugSections(stack: HTMLElement, message: services.ChatMessage) {
  if (message.role !== "assistant") {
    return;
  }

  const reasoning = message.reasoning ?? "";
  const toolCalls = message.toolCalls ?? [];
  let reasoningSection = stack.querySelector<HTMLElement>(
    '[data-debug-section="reasoning"]',
  );
  if (reasoning) {
    if (!reasoningSection) {
      reasoningSection = elementFromHtml(renderReasoning(""));
      const toolsSection = stack.querySelector<HTMLElement>(
        '[data-debug-section="tools"]',
      );
      stack.insertBefore(reasoningSection, toolsSection);
    }
    const reasoningContent = reasoningSection.querySelector<HTMLElement>(
      "[data-message-reasoning]",
    );
    if (reasoningContent) {
      patchMarkdownElement(reasoningContent, reasoning);
    } else {
      morphElement(reasoningSection, elementFromHtml(renderReasoning(reasoning)));
    }
  } else {
    reasoningSection?.remove();
  }

  let toolsSection = stack.querySelector<HTMLElement>(
    '[data-debug-section="tools"]',
  );
  if (toolCalls.length) {
    if (!toolsSection) {
      toolsSection = elementFromHtml(renderToolCalls([]));
      stack.appendChild(toolsSection);
    }
    const toolList = toolsSection.querySelector<HTMLElement>("[data-tool-list]");
    if (toolList) {
      patchChildrenFromHtml(toolList, toolCalls.map(renderToolCall).join(""));
    } else {
      morphElement(toolsSection, elementFromHtml(renderToolCalls(toolCalls)));
    }
  } else {
    toolsSection?.remove();
  }
}

function patchChatPanel() {
  const workspace = activeWorkspace();
  const panel = appRoot.querySelector<HTMLElement>("[data-chat-panel]");
  if (!workspace || !panel) {
    render();
    return;
  }
  const next = document.createElement("template");
  next.innerHTML = renderChatPanel(workspace, expandedChatWorkspaces.has(workspace.id)).trim();
  const replacement = next.content.firstElementChild as HTMLElement;
  panel.replaceWith(replacement);
  bindActionEvents(replacement);
  bindChatEvents(replacement);
  void linkifyAssistantFilePaths(replacement);
}

function patchOpenCardProgress(event: KanbanEvent): boolean {
  const board = kanbanBoardFor(event.workspaceId);
  const card = selectedKanbanCard(board);
  if (!card || card.id !== event.cardId) {
    return false;
  }

  const detail = appRoot.querySelector<HTMLElement>("[data-card-detail]");
  const section = detail?.querySelector<HTMLElement>("[data-card-progress-section]");
  if (!detail || !section) {
    return false;
  }

  const keepPinned = isElementScrolledNearBottom(detail);
  patchChildrenFromHtml(section, renderProgressSectionContent(card.progressTranscript ?? []));
  if (keepPinned) {
    detail.scrollTop = detail.scrollHeight;
  }
  return true;
}

function patchChatControls() {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  const session = chatSessionFor(workspace.id);
  const draft = chatDrafts.get(workspace.id) ?? "";
  const imageDrafts = chatImageDraftsFor(workspace.id);
  const input = appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]");
  const send = appRoot.querySelector<HTMLButtonElement>(".send-button");
  const stop = appRoot.querySelector<HTMLButtonElement>(".stop-button");
  const execute = appRoot.querySelector<HTMLButtonElement>(".execute-button");
  const clear = appRoot.querySelector<HTMLButtonElement>('[data-action="clear-chat"]');
  const planToggle = appRoot.querySelector<HTMLInputElement>("[data-chat-plan-toggle]");
  const title = appRoot.querySelector<HTMLElement>("#chat-title");
  const panel = appRoot.querySelector<HTMLElement>("[data-chat-panel]");
  const executing = executingPlans.has(workspace.id);
  if (input) {
    input.disabled = session.busy || executing;
  }
  if (send) {
    send.disabled = session.busy || executing || (draft.trim().length === 0 && imageDrafts.length === 0);
  }
  if (stop) {
    stop.disabled = !session.busy;
  }
  if (execute) {
    execute.disabled = session.busy || executing || (session.messages ?? []).length === 0;
    execute.classList.toggle("is-busy", executing);
    execute.title = executing ? "Decomposing cards" : "Execute plan";
    execute.setAttribute("aria-label", execute.title);
    execute.innerHTML = executing ? `<span class="spinner" aria-hidden="true"></span>` : icons.execute;
  }
  if (clear) {
    clear.disabled = session.busy || executing || (session.messages ?? []).length === 0;
  }
  if (planToggle) {
    planToggle.disabled = session.busy || executing;
    planToggle.checked = chatPlanModeFor(workspace.id);
  }
  if (title) {
    title.innerHTML = executing ? renderSpinnerLabel("Triage") : session.busy ? "Working" : "Ready";
  }
  if (panel) {
    panel.setAttribute("aria-busy", String(session.busy || executing));
  }
}

function scrollChatToBottom() {
  const log = appRoot.querySelector<HTMLElement>("[data-chat-log]");
  if (log) {
    log.scrollTop = log.scrollHeight;
  }
}

function captureScrollSnapshot(selector: string): ScrollSnapshot | null {
  const element = appRoot.querySelector<HTMLElement>(selector);
  if (!element) {
    return null;
  }
  return {
    scrollTop: element.scrollTop,
    atBottom: isElementScrolledNearBottom(element),
  };
}

function restoreScrollSnapshot(selector: string, snapshot: ScrollSnapshot | null) {
  if (!snapshot) {
    return;
  }
  const element = appRoot.querySelector<HTMLElement>(selector);
  if (!element) {
    return;
  }
  const maxScrollTop = Math.max(0, element.scrollHeight - element.clientHeight);
  element.scrollTop = snapshot.atBottom
    ? element.scrollHeight
    : Math.min(snapshot.scrollTop, maxScrollTop);
}

function isElementScrolledNearBottom(element: HTMLElement | null): boolean {
  if (!element) {
    return true;
  }
  return (
    element.scrollHeight - element.scrollTop - element.clientHeight <=
    scrollStickinessThreshold
  );
}

async function initialize() {
  try {
    appState = await LoadState();
    settingsDraft = cloneSettings(appState.settings);
    await loadActiveChatSession();
    await loadActiveKanbanBoard();
    await loadActiveChangeReview();
  } catch (error) {
    appState = services.AppState.createFrom({
      settings: llm.Settings.createFrom({ endpoint: "", model: "" }),
      workspaces: [],
      activeWorkspaceId: "",
    });
    settingsDraft = cloneSettings(appState.settings);
    pushToast(errorMessage(error), "error");
  }
  render();
}

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
