import "./styles.css";
import {
  applyInlineCodePromptEvent,
  bindCodeViewEvents,
  destroyCodeEditor,
  ensureCodeViewRootLoaded,
  openWorkspaceCodeFile,
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
  CloseKanbanCardDetail,
  ClearChat,
  DeleteWorkspace,
  ExecutePlan,
  AddKanbanCardMessage,
  LoadChatSession,
  LoadKanbanBoard,
  LoadState,
  MoveKanbanCard,
  OpenKanbanCardDetail,
  ResetKanbanCard,
  ResolveWorkspaceTextFilePath,
  SaveSettings,
  SendChatMessage,
  SetActiveWorkspace,
  SetWorkspaceLetter,
  StartKanbanExecution,
  StopKanbanCard,
  StopKanbanExecution,
  StopChatStream,
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
  trash: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M3 6h18"/><path d="M8 6V4h8v2"/><path d="m19 6-1 14H6L5 6"/><path d="M10 11v5M14 11v5"/></svg>`,
  expand: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M15 3h6v6"/><path d="m21 3-7 7"/><path d="M9 21H3v-6"/><path d="m3 21 7-7"/></svg>`,
  collapse: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M8 3v5H3"/><path d="m3 8 6-6"/><path d="M16 21v-5h5"/><path d="m21 16-6 6"/></svg>`,
  code: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m16 18 6-6-6-6"/><path d="m8 6-6 6 6 6"/></svg>`,
  x: `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M18 6 6 18M6 6l12 12"/></svg>`,
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
const chatFileLinkCache = new Map<string, Promise<string | null>>();
const kanbanBoards = new Map<string, services.KanbanBoard>();
const executingPlans = new Set<string>();
const runningKanbanWorkspaces = new Set<string>();
const kanbanRunStarts = new Map<string, number>();
const selectedKanbanCards = new Map<string, string>();
const cardMessageDrafts = new Map<string, string>();
const expandedChatWorkspaces = new Set<string>();
const expandedKanbanWorkspaces = new Set<string>();
let toastSeq = 0;
let toasts: Toast[] = [];
let kanbanTimerID: number | null = null;

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

type Toast = {
  id: string;
  tone: "info" | "success" | "error";
  message: string;
};

type ScrollSnapshot = {
  scrollTop: number;
  atBottom: boolean;
};

const scrollStickinessThreshold = 48;

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
  return startedAt ? formatElapsedTime(now - startedAt) : "0:00";
}

function markKanbanRunStarted(workspaceID: string) {
  if (!kanbanRunStarts.has(workspaceID)) {
    kanbanRunStarts.set(workspaceID, Date.now());
  }
  runningKanbanWorkspaces.add(workspaceID);
  syncKanbanTimer();
}

function clearKanbanRun(workspaceID: string) {
  runningKanbanWorkspaces.delete(workspaceID);
  kanbanRunStarts.delete(workspaceID);
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
    const startedAt = kanbanRunStarts.get(workspaceID);
    if (!startedAt) {
      return;
    }
    const label = formatElapsedTime(now - startedAt);
    element.textContent = label;
    element
      .closest<HTMLElement>("[data-kanban-runtime]")
      ?.setAttribute("aria-label", `Echo has been working for ${label}`);
  });
}

function fieldValue<K extends keyof llm.Settings>(key: K): string {
  const value = settingsDraft?.[key];
  return value === undefined || value === null ? "" : String(value);
}

function workspaceLetter(workspace: services.Workspace): string {
  return (workspace.letter ?? "").trim() || workspace.displayName.slice(0, 1).toUpperCase() || "W";
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
  const hadDialog = Boolean(appRoot.querySelector('[role="dialog"]'));
  const workspace = activeWorkspace();
  const workspaces = appState?.workspaces ?? [];
  const showingCode = appMode === "code" && Boolean(workspace) && !workspace?.missing;

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
                >${escapeHtml(workspaceLetter(item))}</button>
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
                  <div>
                    <p class="eyebrow">${workspace ? escapeHtml(workspace.folderPath) : "Echo"}</p>
                    <h1 id="workspace-title">${workspace ? escapeHtml(workspace.displayName) : "Workspace"}</h1>
                  </div>
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
      </main>
      ${settingsOpen ? renderSettingsOverlay(workspaces) : ""}
      ${renderToasts()}
    </div>
  `;

  bindEvents();
  restoreScrollSnapshot("[data-chat-log]", chatScroll);
  restoreScrollSnapshot("[data-card-detail]", cardDetailScroll);
  if (!hadDialog) {
    focusInitialElement();
  }
  void linkifyAssistantFilePaths();
}

function renderWorkspacePanels(workspace: services.Workspace | null, workspaceCount: number): string {
  const board = workspace ? kanbanBoardFor(workspace.id) : null;
  const running = workspace ? runningKanbanWorkspaces.has(workspace.id) : false;
  const decomposing = workspace ? executingPlans.has(workspace.id) : false;
  const hasCards = board ? kanbanCards(board).length > 0 : false;
  const chatExpanded = workspace ? expandedChatWorkspaces.has(workspace.id) : false;
  const kanbanExpanded = workspace && !chatExpanded ? expandedKanbanWorkspaces.has(workspace.id) : false;
  const kanbanSizeLabel = kanbanExpanded ? "Collapse Kanban" : "Expand Kanban";
  return `
    <div class="split-panels ${chatExpanded ? "is-chat-expanded" : ""} ${kanbanExpanded ? "is-kanban-expanded" : ""}">
      ${renderChatPanel(workspace, chatExpanded)}
      <section class="work-panel kanban-panel" aria-labelledby="kanban-title">
        <div class="panel-heading">
          <div class="kanban-heading-main">
            <span>Kanban</span>
            <strong id="kanban-title">${workspace ? escapeHtml(workspace.displayName) : `${workspaceCount} workspace${workspaceCount === 1 ? "" : "s"}`}</strong>
            ${workspace && running ? renderKanbanRuntime(workspace.id) : ""}
          </div>
          ${
            workspace
              ? `<div class="kanban-actions">
                  <button class="icon-button" type="button" title="${kanbanSizeLabel}" aria-label="${kanbanSizeLabel}" aria-pressed="${kanbanExpanded}" data-action="toggle-kanban-size">
                    ${kanbanExpanded ? icons.collapse : icons.expand}
                  </button>
                  <button class="icon-text-button primary-button" type="button" data-action="start-agents" ${running || !hasCards ? "disabled" : ""}>
                    ${icons.execute}
                    <span>Run</span>
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
  `;
}

function renderKanbanRuntime(workspaceID: string): string {
  const elapsed = kanbanElapsedLabel(workspaceID);
  return `
    <div class="kanban-runtime" role="timer" aria-label="Echo has been working for ${elapsed}" data-kanban-runtime>
      <span class="spinner" aria-hidden="true"></span>
      <span>Working</span>
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
          <p>${escapeHtml(card.description)}</p>
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
  const executing = executingPlans.has(workspace.id);
  const canSend = !session.busy && !executing && draft.trim().length > 0;
  const sizeLabel = expanded ? "Collapse chat" : "Expand chat";
  const executeLabel = executing ? "Decomposing cards" : "Execute plan";
  return `
    <section class="work-panel chat-panel" aria-labelledby="chat-title" aria-busy="${session.busy || executing}" data-chat-panel data-workspace-id="${escapeAttribute(workspace.id)}">
      <div class="panel-heading chat-heading">
        <div>
          <span>Chat</span>
          <strong id="chat-title">${executing ? renderSpinnerLabel("Decomposing cards") : session.busy ? "Working" : "Ready"}</strong>
        </div>
        <div class="chat-actions">
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
        <textarea
          name="message"
          rows="3"
          placeholder="Ask for a plan..."
          aria-label="Message Echo"
          data-chat-input
          ${session.busy || executing ? "disabled" : ""}
        >${escapeHtml(draft)}</textarea>
        <button class="primary-button icon-button send-button" type="submit" title="Send" aria-label="Send message" ${canSend ? "" : "disabled"}>
          ${icons.send}
        </button>
      </form>
    </section>
  `;
}

function renderChatMessage(message: services.ChatMessage): string {
  const roleLabel = message.role === "user" ? "You" : "Echo";
  const status = message.status && message.status !== "complete"
    ? `<span data-message-status>${escapeHtml(message.status)}</span>`
    : "";
  return `
    <article class="chat-message ${message.role === "user" ? "from-user" : "from-assistant"}" data-message-id="${escapeAttribute(message.id)}">
      <header>
        <strong>${roleLabel}</strong>
        ${status}
      </header>
      <div class="markdown-body" data-message-content>${renderMarkdown(message.content ?? "")}</div>
      ${message.error ? `<p class="message-error" data-message-error>${escapeHtml(message.error)}</p>` : `<p class="message-error" data-message-error hidden></p>`}
      ${renderDebugSections(message)}
    </article>
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
                          <label class="workspace-letter-field">
                            <span>Letter</span>
                            <input
                              name="workspaceLetter"
                              type="text"
                              maxlength="2"
                              value="${escapeHtml(workspaceLetterDraft(workspace))}"
                              aria-label="Workspace letter for ${escapeHtml(workspace.displayName)}"
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
  bindCardMessageEvents(appRoot);
  bindCodeViewEvents(appRoot, codeViewCallbacks());
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
    });
  bindChatFileLinks(root);
}

function bindCardMessageEvents(root: ParentNode) {
  const form = root.querySelector<HTMLFormElement>("[data-card-message-form]");
  form?.addEventListener("submit", handleCardMessageSubmit);
  root
    .querySelectorAll<HTMLTextAreaElement>("[data-card-message-input]")
    .forEach((input) => input.addEventListener("input", handleCardMessageInput));
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

function handleGlobalKeydown(event: KeyboardEvent) {
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
  if (appMode === "code") {
    return;
  }
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  const cardID = selectedKanbanCards.get(workspace.id) ?? "";
  if (!cardID) {
    return;
  }
  event.preventDefault();
  void closeSelectedCardDetail(workspace.id).finally(render);
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
      appMode = "chat-kanban";
      render();
      return;
    }
    if (action === "open-settings") {
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
      await loadActiveCodeViewIfNeeded();
      pushToast("Workspace list updated.", "success");
      render();
    }
    if (action === "refresh-workspaces") {
      appState = await LoadState();
      await loadActiveChatSession();
      await loadActiveKanbanBoard();
      await loadActiveCodeViewIfNeeded();
      pushToast(
        activeWorkspace()?.missing
          ? "Workspace folder is still unavailable."
          : "Workspace folder recovered.",
        activeWorkspace()?.missing ? "error" : "success",
      );
      render();
    }
    if (action === "activate-workspace") {
      const current = activeWorkspace();
      if (current && current.id !== workspaceID) {
        await closeSelectedCardDetail(current.id);
      }
      appState = await SetActiveWorkspace(workspaceID);
      await loadActiveChatSession();
      await loadActiveKanbanBoard();
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
        clearKanbanRun(workspace.id);
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
      clearKanbanRun(workspace.id);
      pushToast("Kanban agents stopped.");
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
      patchChatPanel();
    }
    if (action === "delete-workspace") {
      const workspace = appState?.workspaces.find((item) => item.id === workspaceID);
      if (!workspace || !window.confirm(`Delete ${workspace.displayName} from Echo?`)) {
        return;
      }
      await closeSelectedCardDetail(workspaceID);
      appState = await DeleteWorkspace(workspaceID);
      kanbanBoards.delete(workspaceID);
      selectedKanbanCards.delete(workspaceID);
      expandedChatWorkspaces.delete(workspaceID);
      expandedKanbanWorkspaces.delete(workspaceID);
      clearKanbanRun(workspaceID);
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
  patchChatControls();
}

function handleChatKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" || event.shiftKey || event.isComposing) {
    return;
  }
  event.preventDefault();
  const input = event.currentTarget as HTMLTextAreaElement;
  input.form?.requestSubmit();
}

async function handleChatSubmit(event: SubmitEvent) {
  event.preventDefault();
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  const message = (chatDrafts.get(workspace.id) ?? "").trim();
  const session = chatSessionFor(workspace.id);
  if (!message || session.busy || executingPlans.has(workspace.id)) {
    return;
  }

  try {
    chatDrafts.set(workspace.id, "");
    chatSessions.set(workspace.id, await SendChatMessage(workspace.id, message));
    render();
    scrollChatToBottom();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    render();
  }
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
    clearKanbanRun(event.workspaceId);
  }
  if (activeWorkspace()?.id === event.workspaceId) {
    if (event.type === "card_progress" && patchOpenCardProgress(event)) {
      return;
    }
    render();
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
    header.appendChild(statusElement);
  }
  statusElement.textContent = status;
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
  const input = appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]");
  const send = appRoot.querySelector<HTMLButtonElement>(".send-button");
  const stop = appRoot.querySelector<HTMLButtonElement>(".stop-button");
  const execute = appRoot.querySelector<HTMLButtonElement>(".execute-button");
  const clear = appRoot.querySelector<HTMLButtonElement>('[data-action="clear-chat"]');
  const title = appRoot.querySelector<HTMLElement>("#chat-title");
  const panel = appRoot.querySelector<HTMLElement>("[data-chat-panel]");
  const executing = executingPlans.has(workspace.id);
  if (input) {
    input.disabled = session.busy || executing;
  }
  if (send) {
    send.disabled = session.busy || executing || draft.trim().length === 0;
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
  if (title) {
    title.innerHTML = executing ? renderSpinnerLabel("Decomposing cards") : session.busy ? "Working" : "Ready";
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

document.addEventListener("keydown", handleGlobalKeydown);

void initialize();
