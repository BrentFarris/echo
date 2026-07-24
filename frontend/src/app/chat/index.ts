
import { ensureCodeViewRootLoaded, openWorkspaceCodeFile } from "../../codeView";
import { elementFromHtml, morphElement, patchChildrenFromHtml, patchMarkdownElement, renderMarkdown } from "../../markdown";
import { ActivateChatTab, ClearChatForTab, CloseChatTab, CreateAgentModeFromChatForTab, CreateChatTab, CreateSkillFromChatForTab, DeleteAgentMode, EditChatMessageForTab, ListAgentModes, LoadChatSessionForTab, LoadChatWorkspace, ResolveWorkspaceTextFilePath, SaveSettings, SearchWorkspaceFiles, SendChatMessageWithAttachmentsToTab, StopChatStreamForTab } from "../../backend/services";
import { isWailsRuntime } from "../../backend/web";
import { llm, services } from "../../../wailsjs/go/models";
import { getAppCallbacks } from "../callbacks";
import { renderSpinnerLabel } from "../components";
import { appRoot, isElementScrolledNearBottom } from "../dom";
import { icons } from "../icons";
import { playNotificationSound, maybeSendChatCompletionNotification } from "../notifications";
import { activeChatIDFor, activeWorkspace, agentModesForWorkspace, chatImageDraftsFor, chatImageDraftTotalBytes, chatVideoDraftsFor, chatVideoDraftTotalBytes, chatPlanModeFor, chatAgentModeIDFor, chatAgentModeNameFor, setChatAgentMode, chatComposerModeFor, setChatComposerMode, chatSessionFor, chatStateKey, getActiveChatModelLabel, cloneSettings, state, taskBoardFor } from "../state";
import { settingsWithCompactTheme } from "../theme";
import { pushToast } from "../toasts";
import type { ChatImageDraft, ChatMentionState, ChatStreamEvent, ChatVideoDraft, ScrollSnapshot } from "../types";
import { errorMessage, escapeAttribute, escapeHtml, fileName, formatBytes } from "../utils";

const chatMentionSearchDelay = 160;
const chatMentionResultLimit = 8;
const maxResearchAgentReasoning = 128 * 1024;
const researchReasoningTruncatedMarker = "[Earlier agent thinking truncated]\n\n";
const maxChatImageDrafts = 4;
const maxChatImageBytes = 10 * 1024 * 1024;
const maxChatImageMessageBytes = 20 * 1024 * 1024;
const supportedChatImageTypes = new Set(["image/png", "image/jpeg", "image/webp", "image/gif"]);
const maxChatVideoDrafts = 4;
const maxChatVideoBytes = 50 * 1024 * 1024;
const maxChatMediaDrafts = 8;
const supportedChatVideoTypes = new Set(["video/mp4", "video/webm", "video/quicktime"]);
const chatStreamPatchDelay = 50;
const thinkingScrollBottomTolerance = 2;
let chatInputWindowResizeBound = false;
const chatSessionReloads = new Map<string, Promise<void>>();

// ---------------------------------------------------------------------------
// Web Speech API duck-typed interfaces (TS does not ship these by default)
// ---------------------------------------------------------------------------

interface SpeechRecognitionEvent extends Event {
  readonly resultIndex: number;
  readonly results: SpeechRecognitionResultList;
}

interface SpeechRecognitionErrorEvent extends Event {
  readonly error: string;
  readonly message: string;
}

interface SpeechRecognitionResultList {
  readonly length: number;
  item(index: number): SpeechRecognitionResult;
  [index: number]: SpeechRecognitionResult;
}

interface SpeechRecognitionResult {
  readonly isFinal: boolean;
  readonly length: number;
  item(index: number): SpeechRecognitionAlternative;
  [index: number]: SpeechRecognitionAlternative;
}

interface SpeechRecognitionAlternative {
  readonly transcript: string;
  readonly confidence: number;
}

interface SpeechGrammar {
  src: string;
  weight: number;
}

interface SpeechGrammarList {
  readonly length: number;
  item(index: number): SpeechGrammar;
  addFromString(string: string, weight?: number): void;
  addFromURI(uri: string, weight?: number): void;
}

export interface SpeechRecognitionInstance extends EventTarget {
  continuous: boolean;
  interimResults: boolean;
  lang: string;
  grammar: SpeechGrammarList | null;
  maxAlternatives: number;
  setProperty(name: string, value: string): void;
  start(): void;
  stop(): void;
  abort(): void;
  onresult: ((this: SpeechRecognitionInstance, event: SpeechRecognitionEvent) => any) | null;
  onerror: ((this: SpeechRecognitionInstance, event: SpeechRecognitionErrorEvent) => any) | null;
  onend: ((this: SpeechRecognitionInstance, event: Event) => any) | null;
  onstart: ((this: SpeechRecognitionInstance, event: Event) => any) | null;
}

// ---------------------------------------------------------------------------
// Speech-recognition module-level state (declared near initSpeechRecognition)
// ---------------------------------------------------------------------------

type PendingChatStreamPatch = {
  workspaceID: string;
  chatID: string;
  message: services.ChatMessage;
  patchDebug: boolean;
  patchControls: boolean;
  linkify: boolean;
  timeoutID: number;
};

const pendingChatStreamPatches = new Map<string, PendingChatStreamPatch>();

export function isSupportedChatImageType(mediaType: string): boolean {
  return supportedChatImageTypes.has(mediaType.toLowerCase());
}

export function isSupportedChatVideoType(mediaType: string): boolean {
  return supportedChatVideoTypes.has(mediaType.toLowerCase());
}

export function fileToDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.addEventListener("load", () => resolve(String(reader.result ?? "")));
    reader.addEventListener("error", () => reject(reader.error ?? new Error("Unable to read pasted image.")));
    reader.readAsDataURL(file);
  });
}

export type ChatFilePathMatch = {
  start: number;
  end: number;
  display: string;
  candidate: string;
};

export const chatFilePathPattern =
  /(["'`])([^"'`\r\n]*[\\/][^"'`\r\n]*)\1|(?:[A-Za-z]:[\\/]|\.{1,2}[\\/]|[A-Za-z0-9_.@()-]+[\\/])(?:[^\s<>"'`|]+[\\/])*[^\s<>"'`|]+/g;
export const trailingChatFilePathPunctuation = /[.,;!?\])}]+$/;

export function extractChatFilePathMatches(text: string): ChatFilePathMatch[] {
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

export function resolveChatFilePath(workspaceID: string, candidate: string): Promise<string | null> {
  const key = `${workspaceID}\0${candidate}`;
  let cached = state.chatFileLinkCache.get(key);
  if (!cached) {
    cached = ResolveWorkspaceTextFilePath(workspaceID, candidate)
      .then((path) => path || null)
      .catch(() => null);
    state.chatFileLinkCache.set(key, cached);
    cached.then((path) => {
      if (!path && state.chatFileLinkCache.get(key) === cached) {
        state.chatFileLinkCache.delete(key);
      }
    });
  }
  return cached;
}

export function chatFileLinkTargets(root: ParentNode): HTMLElement[] {
  const selector = [
    ".chat-message.from-assistant [data-message-content]",
    ".chat-message.from-assistant [data-message-reasoning]",
    ".chat-message.from-assistant .tool-call code",
    ".chat-message.from-assistant .tool-call pre",
    ".chat-message.from-assistant .tool-call .console-output",
  ].join(", ");
  const targets = Array.from(root.querySelectorAll<HTMLElement>(selector));
  if (root instanceof HTMLElement && root.matches(selector)) {
    targets.unshift(root);
  }
  return targets;
}

export async function linkifyAssistantFilePaths(root: ParentNode = appRoot) {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  for (const target of chatFileLinkTargets(root)) {
    await linkifyFilePathsInElement(target, workspace.id);
  }
}

export async function linkifyFilePathsInElement(container: HTMLElement, workspaceID: string) {
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

export function bindChatFileLinks(root: ParentNode) {
  root
    .querySelectorAll<HTMLElement>("[data-chat-file-link]")
    .forEach(bindChatFileLink);
}

export function bindChatFileLink(link: HTMLElement) {
  if (link.dataset.chatFileLinkBound) {
    return;
  }
  link.dataset.chatFileLinkBound = "true";
  link.addEventListener("click", handleChatFileLinkClick);
}

export async function handleChatFileLinkClick(event: MouseEvent) {
  event.preventDefault();
  const link = event.currentTarget as HTMLElement;
  const workspace = activeWorkspace();
  const workspaceID = link.dataset.workspaceId ?? "";
  const path = link.dataset.workspacePath ?? "";
  if (!workspace || workspace.id !== workspaceID || !path) {
    return;
  }
  state.appMode = "code";
  const loading = ensureCodeViewRootLoaded(workspace.id);
  getAppCallbacks().render();
  await loading;
  await openWorkspaceCodeFile(workspace.id, path, getAppCallbacks().codeViewCallbacks());
}

// ---------------------------------------------------------------------------
// Task reference resolution and click handling
// ---------------------------------------------------------------------------

export function resolveChatTaskRefs(root: ParentNode = appRoot) {
  const workspace = activeWorkspace();
  if (!workspace) return;

  const board = taskBoardFor(workspace.id);
  const tasks = board.tasks ?? [];

  root.querySelectorAll<HTMLElement>("[data-task-ref]").forEach((el) => {
    if (el.dataset.taskRefBound) return;
    el.dataset.taskRefBound = "true";

    const taskID = el.dataset.taskId ?? "";
    const task = tasks.find((t) => t.id === taskID);
    if (task && task.title) {
      el.textContent = `@${escapeHtml(task.title)}`;
      el.title = `${task.title} (${taskID})`;
    } else {
      el.title = `Task ${taskID}`;
    }

    el.addEventListener("click", handleChatTaskRefClick);
  });
}

export function handleChatTaskRefClick(event: MouseEvent) {
  event.preventDefault();
  const el = event.currentTarget as HTMLElement;
  const taskID = el.dataset.taskId ?? "";
  if (!taskID) return;

  const workspace = activeWorkspace();
  if (!workspace) return;

  state.selectedTaskCards.set(workspace.id, taskID);
  state.appMode = "tasks";
  getAppCallbacks().render();
}

export type ChatMentionMatch = {
  triggerStart: number;
  query: string;
  caret: number;
};

export function activeChatMentionMatch(input: HTMLTextAreaElement): ChatMentionMatch | null {
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

export function chatMentionFor(workspaceID: string): ChatMentionState | null {
  return state.chatMention?.workspaceId === workspaceID ? state.chatMention : null;
}

export function clearChatMention() {
  const timerID = state.chatMention?.timerID;
  if (timerID !== undefined && timerID !== null) {
    window.clearTimeout(timerID);
  }
  state.chatMention = null;
}

export function syncChatMentionForInput(workspaceID: string, input: HTMLTextAreaElement) {
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
    state.chatMention = mention;
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

export async function runChatMentionSearch(workspaceID: string, sequence: number) {
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
    latest.results = model.entries ?? [];
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

export function visibleChatMentionEntries(mention: ChatMentionState): services.WorkspaceFileEntry[] {
  return mention.results.slice(0, chatMentionResultLimit);
}

export function clampChatMentionSelection(mention: ChatMentionState) {
  const count = visibleChatMentionEntries(mention).length;
  mention.selectedIndex = count
    ? Math.min(Math.max(mention.selectedIndex, 0), count - 1)
    : 0;
}

export function moveChatMentionSelection(delta: number) {
  if (!state.chatMention) {
    return;
  }
  const entries = visibleChatMentionEntries(state.chatMention);
  if (!entries.length) {
    return;
  }
  state.chatMention.selectedIndex =
    (state.chatMention.selectedIndex + delta + entries.length) % entries.length;
  patchChatMentionPicker();
}

export function selectChatMentionIndex(index: number) {
  if (!state.chatMention) {
    return;
  }
  state.chatMention.selectedIndex = index;
  clampChatMentionSelection(state.chatMention);
  const entry = visibleChatMentionEntries(state.chatMention)[state.chatMention.selectedIndex];
  if (entry) {
    insertChatMention(entry);
  }
}

export function insertChatMention(entry: services.WorkspaceFileEntry) {
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
  resizeChatInput(input);
  state.chatDrafts.set(chatStateKey(workspace.id), nextValue);
  clearChatMention();
  input.focus();
  input.setSelectionRange(nextCaret, nextCaret);
  patchChatControls();
  patchChatMentionPicker();
}

export function formatChatMentionPath(path: string): string {
  if (!/\s/.test(path)) {
    return `@${path}`;
  }
  return `@"${path.replaceAll('"', '\\"')}"`;
}

export function renderChatMentionPicker(workspaceID: string): string {
  const mention = chatMentionFor(workspaceID);
  if (!mention) {
    return "";
  }
  const entries = visibleChatMentionEntries(mention);
  let content = "";
  if (mention.loading) {
    content = `<div class="chat-mention-status"><span class="spinner" aria-hidden="true"></span><span>Searching files and folders...</span></div>`;
  } else if (mention.error) {
    content = `<div class="chat-mention-status is-error">${escapeHtml(mention.error)}</div>`;
  } else if (!entries.length) {
    content = `<div class="chat-mention-status">No matching files or folders.</div>`;
  } else {
    content = entries
      .map((entry, index) => renderChatMentionOption(entry, index, index === mention.selectedIndex))
      .join("");
  }
  return `
    <div class="chat-mention-picker" id="chat-mention-list" role="listbox" aria-label="Workspace files and folders" data-chat-mention-picker>
      ${content}
    </div>
  `;
}

export function renderChatMentionOption(
  entry: services.WorkspaceFileEntry,
  index: number,
  selected: boolean,
): string {
  const isDirectory = entry.kind === "directory";
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
      <span class="chat-mention-icon">${isDirectory ? icons.folder : icons.file}</span>
      <span class="chat-mention-name">
        <strong>${escapeHtml(fileName(entry.path))}</strong>
        <span>${escapeHtml(entry.path)}</span>
      </span>
      <span class="chat-mention-size">${isDirectory ? "Folder" : escapeHtml(formatBytes(entry.bytes ?? 0))}</span>
    </button>
  `;
}

export function patchChatMentionPicker() {
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

export function bindChatMentionOptions(root: ParentNode) {
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

export function renderChatTabs(workspaceID: string): string {
  const chatWorkspace = state.chatWorkspaces.get(workspaceID);
  const tabs = chatWorkspace?.tabs ?? [];
  if (tabs.length < 2) {
    return "";
  }
  return `
    <div class="chat-tabs" role="tablist" aria-label="Open chats" data-chat-tabs>
      ${tabs.map((tab) => {
        const active = tab.chatId === chatWorkspace?.activeChatId;
        const preview = tab.preview || "New chat";
        return `
          <div class="chat-tab${active ? " is-active" : ""}${tab.busy ? " is-busy" : ""}" data-chat-tab="${escapeAttribute(tab.chatId)}">
            <button class="chat-tab-main" type="button" role="tab" aria-selected="${active}" title="${escapeAttribute(preview)}" data-chat-tab-main data-chat-id="${escapeAttribute(tab.chatId)}">
              <span class="chat-tab-title">${escapeHtml(preview)}</span>
              ${tab.busy ? `<span class="chat-tab-busy-dot" title="Chat is running" aria-label="Chat is running"></span>` : ""}
            </button>
            <button class="chat-tab-close" type="button" title="Close ${escapeAttribute(preview)}" aria-label="Close ${escapeAttribute(preview)}" data-chat-tab-close data-chat-id="${escapeAttribute(tab.chatId)}">
              ${icons.x}
            </button>
          </div>
        `;
      }).join("")}
    </div>
  `;
}


export function renderChatPanel(workspace: services.Workspace | null, expanded = false): string {
  if (!workspace) {
    return `
      <section class="work-panel chat-panel" aria-label="Chat">
        <div class="empty-state">Add a workspace to start planning.</div>
      </section>
    `;
  }

  const session = chatSessionFor(workspace.id);
  const chatID = session.chatId || activeChatIDFor(workspace.id);
  const chatKey = chatStateKey(workspace.id, chatID);
  const chatWorkspace = state.chatWorkspaces.get(workspace.id);
  const messages = session.messages ?? [];
  const draft = state.chatDrafts.get(chatKey) ?? "";
  const imageDrafts = chatImageDraftsFor(workspace.id);
  const videoDrafts = chatVideoDraftsFor(workspace.id);
  const executing = state.executingPlans.has(chatKey);
  const sizeLabel = expanded ? "Collapse chat" : "Expand chat";
  const executeLabel = executing ? "Decomposing cards" : "Execute plan";
  const mentionOpen = Boolean(chatMentionFor(workspace.id));
  const creatingSkill = state.creatingChatSkills.has(chatKey);
  return `
    <section class="work-panel chat-panel${(chatWorkspace?.tabs?.length ?? 0) >= 2 ? " has-chat-tabs" : ""}" aria-label="Chat" aria-busy="${session.busy || executing}" data-chat-panel data-workspace-id="${escapeAttribute(workspace.id)}" data-chat-id="${escapeAttribute(chatID)}">
      ${renderChatTabs(workspace.id)}
      <div class="chat-log" data-chat-log>
        ${
          messages.length
            ? messages.map((message) => renderChatMessage(message, session.busy || executing)).join("")
            : `<div class="empty-state chat-empty">Ask Echo to inspect, plan, or break down work for this workspace.</div>`
        }
      </div>
      <form class="chat-composer" data-chat-form>
        <div class="chat-composer-main" data-chat-input-wrap>
          ${renderChatImageDrafts(workspace.id, session.busy || executing)}
          ${renderChatVideoDrafts(workspace.id, session.busy || executing)}
          ${renderChatMentionPicker(workspace.id)}
          <textarea
            name="message"
            rows="1"
            placeholder="Describe what to build"
            aria-label="Message Echo"
            aria-autocomplete="list"
            aria-expanded="${mentionOpen}"
            ${mentionOpen ? `aria-controls="chat-mention-list"` : ""}
            spellcheck="true"
            data-chat-input
            speechinput="true"
            ${executing ? "disabled" : ""}
          >${escapeHtml(draft)}</textarea>
        </div>
        <div class="chat-composer-toolbar">
          <div class="chat-composer-toolbar-left">
            <button class="chat-toolbar-icon" type="button" title="Attach file" aria-label="Attach file" data-chat-attachment-toggle ${session.busy || executing ? "disabled" : ""}>
              ${icons.plus}
            </button>
            <div class="chat-attachment-menu" data-chat-attachment-menu hidden>
              <button type="button" title="Attach image" aria-label="Attach image" data-attachment-type="image">
                ${icons.image}
                <span>Image</span>
              </button>
              <button type="button" title="Attach video" aria-label="Attach video" data-attachment-type="video">
                ${icons.video}
                <span>Video</span>
              </button>
            </div>
            ${!isWailsRuntime() ? `
            <button class="chat-toolbar-icon chat-speech-recognition" type="button" title="Hold to speak" aria-label="Voice input" data-chat-speech-recognition ${session.busy || executing ? "disabled" : ""}>
              ${icons.mic}
            </button>
            ` : ''}
            <button class="model-selector chat-toolbar-model" type="button" title="Select model" aria-haspopup="listbox" aria-expanded="false" data-model-selector ${session.busy || executing ? "disabled" : ""}>
              <span class="model-selector-label">${escapeHtml(getActiveChatModelLabel())}</span>
              <span class="model-selector-chevron">${icons.arrowDown}</span>
            </button>
            <ul class="model-dropdown" data-model-dropdown hidden role="listbox" aria-label="Available models">
              ${renderModelOptions()}
            </ul>
            <span class="chat-toolbar-separator"></span>
            <button class="model-selector mode-selector chat-toolbar-mode" type="button" title="Agent mode" aria-haspopup="listbox" aria-expanded="false" data-mode-selector ${session.busy || executing ? "disabled" : ""}>
              <span class="model-selector-label">${escapeHtml(chatAgentModeNameFor(workspace.id))}</span>
              <span class="model-selector-chevron">${icons.arrowDown}</span>
            </button>
            <ul class="model-dropdown mode-dropdown" data-mode-dropdown hidden role="listbox" aria-label="Composer modes">
              ${renderModeOptions(workspace.id)}
            </ul>
            <button class="chat-toolbar-icon execute-button ${executing ? "is-busy" : ""}" type="button" title="${executeLabel}" aria-label="${executeLabel}" data-action="execute-plan" ${session.busy || executing || messages.length === 0 ? "disabled" : ""}>
              ${executing ? `<span class="spinner spinner-sm" aria-hidden="true"></span>` : icons.execute}
            </button>
            <span class="chat-toolbar-separator"></span>
            <button class="chat-toolbar-icon" type="button" title="More options" aria-label="More options" data-chat-more-toggle>
              ${icons.moreHorizontal}
            </button>
            <div class="chat-more-menu" data-chat-more-menu hidden>
              <button type="button" title="New tab" aria-label="Open a new chat tab" data-new-chat-tab-button>
                ${icons.plus}
                <span>New tab</span>
              </button>
              <button type="button" title="Clear current chat" aria-label="Clear current chat" data-clear-chat-button ${session.busy || executing || messages.length === 0 ? "disabled" : ""}>
                ${icons.refresh}
                <span>Clear current chat</span>
              </button>
              <button type="button" title="Create skill from this chat" aria-label="Create workspace skill from chat" data-create-skill-button ${session.busy || executing || creatingSkill ? "disabled" : ""}>
                ${icons.star}
                <span>Create skill</span>
              </button>
            </div>
          </div>
          <div class="chat-composer-toolbar-right">
            <button class="send-button ${session.busy || executing ? 'is-busy' : ''}" type="button" title="${session.busy || executing ? 'Stop' : 'Send'}" aria-label="${session.busy || executing ? 'Stop stream' : 'Send message'}" data-action="send-stop" ${executing ? "disabled" : ""}>
              ${(session.busy || executing) ? icons.stop : icons.send}
            </button>
          </div>
        </div>
      </form>
    </section>
  `;
}

export function renderChatImageDrafts(workspaceID: string, disabled: boolean): string {
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

export function renderChatVideoDrafts(workspaceID: string, disabled: boolean): string {
  const drafts = chatVideoDraftsFor(workspaceID);
  if (!drafts.length) {
    return "";
  }
  return `
    <div class="chat-video-drafts" data-chat-video-drafts>
      ${drafts
        .map(
          (draft) => `
            <div class="chat-video-chip">
              <span class="chat-video-icon">${icons.video}</span>
              <span>
                <strong>${escapeHtml(draft.name)}</strong>
                <small>${escapeHtml(formatBytes(draft.bytes))}</small>
              </span>
              <button class="icon-button" type="button" title="Remove video" aria-label="Remove ${escapeAttribute(draft.name)}" data-action="remove-chat-video" data-video-id="${escapeAttribute(draft.id)}" ${disabled ? "disabled" : ""}>
                ${icons.x}
              </button>
            </div>
          `,
        )
        .join("")}
    </div>
  `;
}

export function renderChatMessage(message: services.ChatMessage, actionsDisabled = false): string {
  const roleLabel = message.role === "user" ? "You" : "Echo";
  const status = message.status && message.status !== "complete"
    ? `<span data-message-status>${escapeHtml(message.status)}</span>`
    : "";
  const isUser = message.role === "user";
  const isEditing = state.editingMessageIds.has(message.id);
  return `
    <article class="chat-message ${isUser ? "from-user" : "from-assistant"}" data-message-id="${escapeAttribute(message.id)}">
      <header>
        <strong>${roleLabel}</strong>
        ${status}
        ${isUser
          ? renderUserControls(message, isEditing, actionsDisabled)
          : renderAssistantControls(message, isEditing, actionsDisabled)
        }
      </header>
      ${renderChatMessageImages(message)}
      ${renderChatMessageVideos(message)}
      ${isEditing
        ? renderEditTextarea(message)
        : `<div class="markdown-body" data-message-content>${renderMarkdown(message.content ?? "")}</div>`
      }
      ${message.error ? `<p class="message-error" data-message-error>${escapeHtml(message.error)}</p>` : `<p class="message-error" data-message-error hidden></p>`}
      ${renderDebugSections(message)}
    </article>
  `;
}

export function renderAssistantControls(message: services.ChatMessage, isEditing: boolean, actionsDisabled: boolean): string {
  const isStreaming = isAssistantMessageStreaming(message);
  const canCreateCard = canCreateKanbanCardFromMessage(message);
  const canEdit = message.status === "complete";
  return `
    <div class="chat-message-actions">
      ${renderCopyMessageButton(message)}
      ${isEditing
        ? ""
        : `<button class="icon-button chat-edit-trigger" type="button" title="Edit response" aria-label="Edit assistant response" data-action="edit-message" data-message-id="${escapeAttribute(message.id)}" ${canEdit ? "" : "disabled"}>
            ${icons.edit}
          </button>`
      }
      <button class="icon-button chat-retry-trigger" type="button" title="Regenerate response" aria-label="Regenerate response" data-action="retry-message" data-message-id="${escapeAttribute(message.id)}">
        ${isStreaming ? '<span class="spinner" aria-hidden="true"></span>' : icons.retry}
      </button>
      <button class="icon-button chat-kanban-trigger" type="button" title="Create Kanban card" aria-label="Create Kanban card from response" data-action="create-card-from-message" data-message-id="${escapeAttribute(message.id)}" ${canCreateCard ? "" : "disabled"}>
        ${icons.kanban}
      </button>
      ${renderPruneMessageButton(message, actionsDisabled)}
    </div>
  `;
}

export function isAssistantMessageStreaming(message: services.ChatMessage): boolean {
  return message.status === "streaming" || message.status === "retrying" || message.status === "compacting" || message.status === "in_progress";
}

export function canCreateKanbanCardFromMessage(message: services.ChatMessage): boolean {
  return message.status === "complete" && (message.content ?? "").trim().length > 0;
}

export function renderCopyMessageButton(message: services.ChatMessage): string {
  return `
    <button class="icon-button chat-copy-trigger" type="button" title="Copy message" aria-label="Copy message" data-action="copy-message" data-message-id="${escapeAttribute(message.id)}">
      ${icons.copy}
    </button>
  `;
}

export function renderUserControls(message: services.ChatMessage, isEditing: boolean, actionsDisabled: boolean): string {
  return `
    <div class="chat-message-actions">
      ${renderCopyMessageButton(message)}
      ${isEditing
        ? ""
        : `<button class="icon-button chat-edit-trigger" type="button" title="Edit message" aria-label="Edit message" data-action="edit-message" data-message-id="${escapeAttribute(message.id)}">
            ${icons.edit}
          </button>`
      }
      ${renderPruneMessageButton(message, actionsDisabled)}
    </div>
  `;
}

export function renderPruneMessageButton(message: services.ChatMessage, disabled: boolean): string {
  return `
    <button class="icon-button danger-button chat-prune-trigger" type="button" title="Prune message" aria-label="Prune message" data-action="prune-chat-message" data-message-id="${escapeAttribute(message.id)}" ${disabled ? "disabled" : ""}>
      ${icons.trash}
    </button>
  `;
}

export function renderEditTextarea(message: services.ChatMessage): string {
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

export function renderChatMessageImages(message: services.ChatMessage): string {
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
                <button class="icon-button chat-save-image" type="button" title="Save image" aria-label="Save ${escapeAttribute(image.name)}" data-action="save-chat-image" data-image-id="${escapeAttribute(image.id)}" data-image-name="${escapeAttribute(image.name)}" data-image-media-type="${escapeAttribute(image.mediaType)}" data-image-data-url="${escapeAttribute(image.dataUrl || '')}">
                  ${icons.download}
                </button>
              </figcaption>
            </figure>
          `,
        )
        .join("")}
    </div>
  `;
}

export function renderChatMessageVideos(message: services.ChatMessage): string {
  const videos = message.videos ?? [];
  if (!videos.length) {
    return "";
  }
  return `
    <div class="chat-message-videos">
      ${videos
        .map(
          (video) => `
            <figure class="chat-message-video">
              ${video.dataUrl ? `<video src="${escapeAttribute(video.dataUrl)}" muted preload="metadata"></video>` : `<span>${icons.video}</span>`}
              <figcaption>
                <strong>${escapeHtml(video.name)}</strong>
                <span>${escapeHtml(video.path || video.source)} - ${escapeHtml(formatBytes(video.bytes ?? 0))}</span>
              </figcaption>
            </figure>
          `,
        )
        .join("")}
    </div>
  `;
}

export function renderDebugSections(message: services.ChatMessage): string {
  if (message.role !== "assistant") {
    return "";
  }
  const researchReasoning = (message.researchReasoning ?? []).filter((entry) => Boolean(entry.reasoning));
  const hasReasoning = Boolean(message.reasoning) || researchReasoning.length > 0;
  const toolCalls = message.toolCalls ?? [];
  const researchAgents = message.researchAgents ?? [];
  if (!hasReasoning && toolCalls.length === 0 && researchAgents.length === 0) {
    return `<div class="debug-stack" data-debug-stack></div>`;
  }
  return `
    <div class="debug-stack" data-debug-stack>
      ${researchAgents.length ? renderResearchAgents(researchAgents) : ""}
      ${hasReasoning ? renderReasoning(message.reasoning ?? "", researchReasoning) : ""}
      ${toolCalls.length ? renderToolCalls(toolCalls) : ""}
    </div>
  `;
}

export function renderResearchAgents(agents: services.ChatResearchAgent[]): string {
  return `
    <div class="research-agent-strip" data-research-agent-strip role="status" aria-live="polite" aria-label="Active research agents">
      ${agents.map(renderResearchAgent).join("")}
    </div>
  `;
}

export function renderResearchAgent(agent: services.ChatResearchAgent): string {
  const phase = agent.phase || agent.status || "researching";
  return `
    <div class="research-agent-chip" data-research-agent-id="${escapeAttribute(agent.id)}" title="${escapeAttribute(agent.taskLabel || phase)}">
      <span class="spinner" aria-hidden="true"></span>
      <span><strong>${escapeHtml(agent.name || agent.id)}</strong> ${escapeHtml(phase)}</span>
    </div>
  `;
}

function boundResearchReasoning(reasoning: string): { reasoning: string; truncated: boolean } {
  if (reasoning.length <= maxResearchAgentReasoning) {
    return { reasoning, truncated: false };
  }
  const remaining = Math.max(0, maxResearchAgentReasoning - researchReasoningTruncatedMarker.length);
  let start = Math.max(0, reasoning.length - remaining);
  const code = reasoning.charCodeAt(start);
  if (code >= 0xdc00 && code <= 0xdfff) {
    start += 1;
  }
  return {
    reasoning: `${researchReasoningTruncatedMarker}${reasoning.slice(start)}`,
    truncated: true,
  };
}

function renderThinkingEntry(name: string, detail: string, reasoning: string, truncated = false): string {
  return `
    <section class="thinking-entry" aria-label="${escapeAttribute(`${name} thinking`)}">
      <div class="thinking-entry-heading">
        <strong>${escapeHtml(name)}</strong>
        <span>${escapeHtml(detail)}</span>
        ${truncated ? '<span class="thinking-truncated">truncated</span>' : ""}
      </div>
      <div class="thinking-entry-content" data-message-reasoning>${renderMarkdown(reasoning)}</div>
    </section>
  `;
}

export function renderReasoning(reasoning: string, researchReasoning: services.ChatResearchReasoning[] = []): string {
  const agents = researchReasoning.filter((entry) => Boolean(entry.reasoning));
  const entries = [
    reasoning ? renderThinkingEntry("Main", "Orchestrator", reasoning) : "",
    ...agents.map((entry) => renderThinkingEntry(
      entry.agentName || entry.agentId,
      `Research agent · ${entry.agentId}`,
      entry.reasoning,
      Boolean(entry.truncated),
    )),
  ].filter(Boolean);
  const agentLabel = agents.length
    ? `<span class="thinking-summary-count">${agents.length} agent${agents.length === 1 ? "" : "s"}</span>`
    : "";
  return `
    <details class="debug-section" data-debug-section="reasoning">
      <summary>Thinking ${agentLabel}</summary>
      <div class="debug-content thinking-list" data-thinking-list>${entries.join("")}</div>
    </details>
  `;
}

export function renderToolCalls(toolCalls: services.ChatToolActivity[]): string {
  return `
    <details class="debug-section" data-debug-section="tools">
      <summary>Tools</summary>
      <div class="tool-list" data-tool-list>
        ${toolCalls.map(renderToolCall).join("")}
      </div>
    </details>
  `;
}

export function renderToolCall(toolCall: services.ChatToolActivity): string {
  const agentPrefix = toolCall.agentId
    ? `[agent ${toolCall.agentName || toolCall.agentId} (${toolCall.agentId})] `
    : "";
  return `
    <div class="tool-call">
      <div>
        <strong>${escapeHtml(`${agentPrefix}${toolCall.name || "tool"}`)}</strong>
        <span>${escapeHtml(toolCall.status)}</span>
      </div>
      ${toolCall.arguments ? `<code>${escapeHtml(toolCall.arguments)}</code>` : ""}
      ${toolCall.consoleOutput 
        ? `<pre class="console-output" data-console-output>${escapeHtml(toolCall.consoleOutput)}</pre>` 
        : ""}
      ${toolCall.error ? `<p>${escapeHtml(toolCall.error)}</p>` : ""}
      ${toolCall.result ? `<pre>${escapeHtml(toolCall.result)}</pre>` : ""}
    </div>
  `;
}

export function bindChatEvents(root: ParentNode) {
  const chatForm = root.querySelector<HTMLFormElement>("[data-chat-form]");
  chatForm?.addEventListener("submit", handleChatSubmit);
  root
    .querySelectorAll<HTMLTextAreaElement>("[data-chat-input]")
    .forEach((input) => {
      resizeChatInput(input);
      input.addEventListener("input", handleChatInput);
      input.addEventListener("keydown", handleChatKeydown);
      input.addEventListener("paste", handleChatPaste);
    });
  if (!chatInputWindowResizeBound) {
    window.addEventListener("resize", () => {
      appRoot
        .querySelectorAll<HTMLTextAreaElement>("[data-chat-input]")
        .forEach(resizeChatInput);
    });
    chatInputWindowResizeBound = true;
  }
  root
    .querySelectorAll<HTMLButtonElement>("[data-action=\"send-stop\"]")
    .forEach((button) => button.addEventListener("click", handleSendStopClick));
  root
    .querySelectorAll<HTMLButtonElement>("[data-chat-attachment-toggle]")
    .forEach((button) => button.addEventListener("click", handleChatAttachmentToggle));
  root
    .querySelectorAll<HTMLButtonElement>("[data-chat-more-toggle]")
    .forEach((button) => button.addEventListener("click", handleChatMoreToggle));
  root
    .querySelectorAll<HTMLButtonElement>("[data-new-chat-tab-button]")
    .forEach((button) => button.addEventListener("click", handleNewChatTab));
  root
    .querySelectorAll<HTMLButtonElement>("[data-attachment-type]")
    .forEach((button) => button.addEventListener("click", handleChatAttachmentSelect));
  bindClearChatButton(root);
  bindChatTabEvents(root);
  bindCreateSkillButton(root);
  bindChatMentionOptions(root);
  bindChatFileLinks(root);
  bindChatDebugSections(root);
  bindChatEditForms(root);
  bindChatAttachmentMenuDismissal();
  bindModelSelector(root);
  bindModelDropdownEvents(root);
  bindModeSelector(root);
  bindModeDropdownEvents(root);
  initSpeechRecognition(root);
}

function bindChatTabEvents(root: ParentNode) {
  root.querySelectorAll<HTMLButtonElement>("[data-chat-tab-main]").forEach((button) => {
    button.addEventListener("click", () => {
      void activateChatTab(button.dataset.chatId ?? "");
    });
    button.addEventListener("mousedown", (event) => {
      if (event.button === 1) {
        event.preventDefault();
        event.stopPropagation();
      }
    });
    button.addEventListener("auxclick", (event) => {
      if (event.button !== 1) {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      void closeChatTabFromUI(button.dataset.chatId ?? "");
    });
  });
  root.querySelectorAll<HTMLButtonElement>("[data-chat-tab-close]").forEach((button) => {
    button.addEventListener("click", (event) => {
      event.preventDefault();
      event.stopPropagation();
      void closeChatTabFromUI(button.dataset.chatId ?? "");
    });
  });
}

async function handleNewChatTab(event: Event) {
  event.preventDefault();
  event.stopPropagation();
  dismissChatMoreMenu();
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  try {
    applyChatWorkspaceSnapshot(await CreateChatTab(workspace.id));
    clearChatMention();
    patchChatPanel();
    appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]")?.focus();
  } catch (error) {
    pushToast(errorMessage(error), "error");
  }
}

async function activateChatTab(chatID: string) {
  const workspace = activeWorkspace();
  if (!workspace || !chatID || activeChatIDFor(workspace.id) === chatID) {
    return;
  }
  clearChatMention();
  dismissChatMoreMenu();
  dismissChatAttachmentMenu();
  try {
    applyChatWorkspaceSnapshot(await ActivateChatTab(workspace.id, chatID));
    patchChatPanel();
  } catch (error) {
    pushToast(errorMessage(error), "error");
  }
}

async function closeChatTabFromUI(chatID: string) {
  const workspace = activeWorkspace();
  const chatWorkspace = workspace ? state.chatWorkspaces.get(workspace.id) : null;
  const tab = chatWorkspace?.tabs?.find((candidate) => candidate.chatId === chatID);
  if (!workspace || !tab) {
    return;
  }
  if (tab.busy && !(await confirmBusyChatClose(tab.preview || "New chat"))) {
    return;
  }
  try {
    const closedKey = chatStateKey(workspace.id, chatID);
    applyChatWorkspaceSnapshot(await CloseChatTab(workspace.id, chatID));
    state.chatSessions.delete(closedKey);
    state.chatDrafts.delete(closedKey);
    state.chatImageDrafts.delete(closedKey);
    state.chatVideoDrafts.delete(closedKey);
    state.chatComposerModes.delete(closedKey);
    state.chatPlanModes.delete(closedKey);
    state.selectedAgentModeIds.delete(closedKey);
    clearChatMention();
    patchChatPanel();
  } catch (error) {
    pushToast(errorMessage(error), "error");
  }
}

function confirmBusyChatClose(preview: string): Promise<boolean> {
  return new Promise((resolve) => {
    const overlay = document.createElement("div");
    overlay.className = "chat-close-busy-overlay";
    overlay.setAttribute("role", "dialog");
    overlay.setAttribute("aria-modal", "true");
    overlay.setAttribute("aria-labelledby", "chat-close-busy-title");
    overlay.innerHTML = `
      <div class="chat-close-busy-dialog">
        <h2 id="chat-close-busy-title">Chat is still running</h2>
        <p>Close “${escapeHtml(preview)}” and stop its response?</p>
        <div class="chat-close-busy-actions">
          <button class="secondary-button" type="button" data-chat-close-cancel>Cancel</button>
          <button class="secondary-button danger-button" type="button" data-chat-close-confirm>Stop and close</button>
        </div>
      </div>
    `;
    const finish = (confirmed: boolean) => {
      document.removeEventListener("keydown", handleKeydown, true);
      overlay.remove();
      resolve(confirmed);
    };
    const handleKeydown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") {
        return;
      }
      event.preventDefault();
      event.stopPropagation();
      finish(false);
    };
    overlay.querySelector<HTMLButtonElement>("[data-chat-close-cancel]")?.addEventListener("click", () => finish(false));
    overlay.querySelector<HTMLButtonElement>("[data-chat-close-confirm]")?.addEventListener("click", () => finish(true));
    overlay.addEventListener("mousedown", (event) => {
      if (event.target === overlay) {
        finish(false);
      }
    });
    document.addEventListener("keydown", handleKeydown, true);
    document.body.appendChild(overlay);
    overlay.querySelector<HTMLButtonElement>("[data-chat-close-cancel]")?.focus();
  });
}

function bindChatAttachmentMenuDismissal() {
  if (chatDismissalListenerBound) {
    return;
  }
  chatDismissalListenerBound = true;
  document.addEventListener("click", (event) => {
    if (chatAttachmentMenuOpen) {
      const target = event.target as HTMLElement | null;
      const container = target?.closest("[data-chat-attachment-menu]") ?? target?.closest("[data-chat-attachment-toggle]");
      if (!container) {
        dismissChatAttachmentMenu();
      }
    }
    if (chatMoreMenuOpen) {
      const target = event.target as HTMLElement | null;
      const container = target?.closest("[data-chat-more-menu]") ?? target?.closest("[data-chat-more-toggle]");
      if (!container) {
        dismissChatMoreMenu();
      }
    }
    if (modelDropdownOpen) {
      const target = event.target as HTMLElement | null;
      const container = target?.closest("[data-model-dropdown]") ?? target?.closest("[data-model-selector]");
      if (!container) {
        dismissModelDropdown();
      }
    }
    if (modeDropdownOpen) {
      const target = event.target as HTMLElement | null;
      const container = target?.closest("[data-mode-dropdown]") ?? target?.closest("[data-mode-selector]");
      if (!container) {
        dismissModeDropdown();
      }
    }
  });
}

let _activeRecognition: SpeechRecognitionInstance | null = null;
let speechRecognitionBound = false;

export function initSpeechRecognition(root: ParentNode) {
  if (isWailsRuntime()) return;
  if (speechRecognitionBound) return;

  const SpeechRecognitionAPI = (window as any).SpeechRecognition || (window as any).webkitSpeechRecognition;
  if (!SpeechRecognitionAPI) return;

  const button = root.querySelector<HTMLButtonElement>("[data-chat-speech-recognition]");
  if (!button) return;

  speechRecognitionBound = true;
  patchSpeechMicButton(button);
}

function patchSpeechMicButton(button: HTMLButtonElement) {
  if (button.dataset.speechRecogBound === "true") return;
  button.dataset.speechRecogBound = "true";

  let holdTimer: ReturnType<typeof setTimeout> | null = null;

  const onPointerDown = (e: PointerEvent) => {
    e.preventDefault();
    if (button.disabled) return;
    holdTimer = setTimeout(() => {
      holdTimer = null;
      startSpeechRecognition();
    }, 200);
  };

  const onPointerUp = (e: PointerEvent) => {
    e.preventDefault();
    if (holdTimer) {
      clearTimeout(holdTimer);
      holdTimer = null;
    } else {
      stopSpeechRecognition();
    }
  };

  const onPointerCancel = (e: PointerEvent) => {
    e.preventDefault();
    if (holdTimer) {
      clearTimeout(holdTimer);
      holdTimer = null;
    } else {
      stopSpeechRecognition();
    }
  };

  button.addEventListener("pointerdown", onPointerDown);
  button.addEventListener("pointerup", onPointerUp);
  button.addEventListener("pointercancel", onPointerCancel);
}

function startSpeechRecognition() {
  // Abort any prior session
  stopSpeechRecognition();

  const SpeechRecognitionAPI = (window as any).SpeechRecognition || (window as any).webkitSpeechRecognition;
  if (!SpeechRecognitionAPI) return;

  const recognition = new SpeechRecognitionAPI();
  recognition.continuous = true;
  recognition.interimResults = true;
  recognition.lang = "en-US";

  _activeRecognition = recognition;

  const button = appRoot.querySelector<HTMLButtonElement>("[data-chat-speech-recognition]");
  if (button) {
    button.classList.add("is-listening");
    button.title = "Listening... Tap to stop";
  }

  recognition.onresult = (event: any) => {
    // Fresh DOM lookup — textarea may have been replaced by a re-render during recognition
    const inputEl = appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]");
    if (!inputEl) return;

    let finalTranscript = "";
    let interimTranscript = "";
    for (let i = event.resultIndex; i < event.results.length; i++) {
      const t = event.results[i][0].transcript;
      if (event.results[i].isFinal) {
        finalTranscript += t;
      } else {
        interimTranscript += t;
      }
    }

    const start = inputEl.selectionStart;
    const end = inputEl.selectionEnd;
    const before = inputEl.value.substring(0, start);
    const after = inputEl.value.substring(end);
    const transcript = finalTranscript || interimTranscript;
    inputEl.value = before + transcript + after;
    const newPos = start + transcript.length;
    inputEl.setSelectionRange(newPos, newPos);
    inputEl.dispatchEvent(new Event("input", { bubbles: true }));
  };

  recognition.onerror = (event: any) => {
    if (event.error === "permission-denied" || event.error === "not-allowed") {
      pushToast("Microphone permission denied. Please allow microphone access.", "error");
    } else {
      console.warn("Speech recognition error:", event.error);
    }
  };

  recognition.onend = () => {
    if (button) {
      button.classList.remove("is-listening");
      button.title = "Hold to speak";
    }
    _activeRecognition = null;
  };

  try {
    recognition.start();
  } catch {
    if (button) {
      button.classList.remove("is-listening");
      button.title = "Hold to speak";
    }
    _activeRecognition = null;
  }
}

function stopSpeechRecognition() {
  if (_activeRecognition) {
    try {
      _activeRecognition.abort();
    } catch { /* ignore */ }
    _activeRecognition = null;
  }
  const button = appRoot.querySelector<HTMLButtonElement>("[data-chat-speech-recognition]");
  if (button) {
    button.classList.remove("is-listening");
    button.title = "Hold to speak";
  }
}

let modelDropdownOpen = false;
let modeDropdownOpen = false;

export function renderModelOptions(): string {
  const endpoints = state.settingsDraft?.endpoints ?? [];
  if (!endpoints.length) {
    return `<li class="model-dropdown-option" role="option">No endpoints configured</li>`;
  }
  const currentID = state.settingsDraft?.endpointSelection?.chat || endpoints[0].id;
  return endpoints
    .map((endpoint, index) => {
      const id = endpoint.id || `endpoint-${index + 1}`;
      const name = endpoint.name?.trim() || `Endpoint ${index + 1}`;
      const selected = id === currentID ? " aria-selected=\"true\"" : "";
      return `<li class="model-dropdown-option${selected ? " is-active" : ""}" role="option" data-model-id="${escapeAttribute(id)}"${selected}>${escapeHtml(name)}</li>`;
    })
    .join("");
}

export function bindModelSelector(root: ParentNode) {
  root.querySelectorAll<HTMLButtonElement>("[data-model-selector]").forEach((button) => {
    button.addEventListener("click", handleModelSelectorClick);
  });
}

function handleModelSelectorClick(event: Event) {
  const button = event.currentTarget as HTMLButtonElement;
  if (button.disabled) {
    return;
  }
  const dropdown = appRoot.querySelector<HTMLElement>("[data-model-dropdown]");
  if (!dropdown) {
    return;
  }
  modelDropdownOpen = !modelDropdownOpen;
  button.setAttribute("aria-expanded", String(modelDropdownOpen));
  if (modelDropdownOpen) {
    dismissModeDropdown();
    dismissChatMoreMenu();
    dropdown.hidden = false;
    const btnRect = button.getBoundingClientRect();
    const dropRect = dropdown.getBoundingClientRect();
    dropdown.style.left = `${btnRect.left - dropRect.left}px`;
  } else {
    dropdown.hidden = true;
    dropdown.style.left = "";
  }
}

function dismissModelDropdown() {
  modelDropdownOpen = false;
  const button = appRoot.querySelector<HTMLButtonElement>("[data-model-selector]");
  const dropdown = appRoot.querySelector<HTMLElement>("[data-model-dropdown]");
  if (button) {
    button.setAttribute("aria-expanded", "false");
  }
  if (dropdown) {
    dropdown.hidden = true;
  }
}

export function bindModelDropdownEvents(root: ParentNode) {
  root.querySelectorAll<HTMLLIElement>(".model-dropdown-option[data-model-id]").forEach((option) => {
    option.addEventListener("click", () => {
      const modelID = option.dataset.modelId ?? "";
      selectChatModel(modelID);
      dismissModelDropdown();
    });
  });
}

async function selectChatModel(endpointID: string) {
  if (!state.settingsDraft) {
    return;
  }
  const endpoints = state.settingsDraft.endpoints ?? [];
  if (!endpoints.length) {
    return;
  }
  const selection = state.settingsDraft.endpointSelection || {};
  const newSelection = { ...selection, chat: endpointID };
  state.settingsDraft = llm.Settings.createFrom({
    ...state.settingsDraft,
    endpointSelection: newSelection,
    endpoints,
  });
  try {
    state.appState = await SaveSettings(settingsWithCompactTheme(state.settingsDraft));
    state.settingsDraft = cloneSettings(state.appState.settings);
  } catch (err) {
    pushToast(`Failed to save model selection: ${errorMessage(err)}`, "error");
    return;
  }
  patchChatPanel();
  getAppCallbacks().bindActionEvents(appRoot);
  getAppCallbacks().bindChatEvents(appRoot);
  const chosenName = endpoints.find((ep) => ep.id === endpointID)?.name || endpointID;
  pushToast(`Model set to ${chosenName}.`, "success");
}

export function renderModeOptions(workspaceID: string): string {
  const currentID = chatAgentModeIDFor(workspaceID);
  const modes = agentModesForWorkspace(workspaceID);
  if (!modes.length) {
    return `<li class="model-dropdown-option" role="option">Loading modes...</li>`;
  }
  let html = modes
    .map((mode) => {
      const selected = mode.id === currentID ? " is-active" : "";
      const badges = renderModePermissionBadges(mode);
      const deleteBtn = mode.builtIn
        ? ""
        : `<button type="button" class="mode-delete-btn" title="Delete ${escapeAttribute(mode.name)}" aria-label="Delete mode ${escapeAttribute(mode.name)}" data-mode-delete-id="${escapeAttribute(mode.id)}">${icons.x}</button>`;
      return `<li class="model-dropdown-option mode-option${selected}" role="option" data-mode-id="${escapeAttribute(mode.id)}"><span class="mode-option-name">${escapeHtml(mode.name)}</span>${badges}${deleteBtn}</li>`;
    })
    .join("");
  html += `\n      <li class="model-dropdown-option mode-create-option" role="option" data-mode-create><span class="mode-option-name">+ Create Mode</span></li>`;
  return html;
}

function renderModePermissionBadges(mode: services.AgentMode): string {
  const toolCount = (mode.toolPermissions ?? []).length;
  const pathCount = (mode.pathPermissions ?? []).length;
  if (toolCount === 0 && pathCount === 0) {
    return "";
  }
  let badges = "";
  if (toolCount > 0) {
    badges += `<span class="mode-badge mode-badge-tools" title="${toolCount} tool permission${toolCount > 1 ? "s" : ""}">${toolCount}</span>`;
  }
  if (pathCount > 0) {
    badges += `<span class="mode-badge mode-badge-paths" title="${pathCount} path permission${pathCount > 1 ? "s" : ""}">${pathCount}</span>`;
  }
  return badges;
}

export function bindModeSelector(root: ParentNode) {
  root.querySelectorAll<HTMLButtonElement>("[data-mode-selector]").forEach((button) => {
    button.addEventListener("click", handleModeSelectorClick);
  });
}

function handleModeSelectorClick(event: Event) {
  const button = event.currentTarget as HTMLButtonElement;
  if (button.disabled) {
    return;
  }
  const dropdown = appRoot.querySelector<HTMLElement>("[data-mode-dropdown]");
  if (!dropdown) {
    return;
  }
  modeDropdownOpen = !modeDropdownOpen;
  button.setAttribute("aria-expanded", String(modeDropdownOpen));
  if (modeDropdownOpen) {
    dismissModelDropdown();
    dismissChatMoreMenu();
    dropdown.hidden = false;
    const btnRect = button.getBoundingClientRect();
    const dropRect = dropdown.getBoundingClientRect();
    dropdown.style.left = `${btnRect.left - dropRect.left}px`;
  } else {
    dropdown.hidden = true;
    dropdown.style.left = "";
  }
}

function dismissModeDropdown() {
  modeDropdownOpen = false;
  const button = appRoot.querySelector<HTMLButtonElement>("[data-mode-selector]");
  const dropdown = appRoot.querySelector<HTMLElement>("[data-mode-dropdown]");
  if (button) {
    button.setAttribute("aria-expanded", "false");
  }
  if (dropdown) {
    dropdown.hidden = true;
  }
}

export function bindModeDropdownEvents(root: ParentNode) {
  root.querySelectorAll<HTMLLIElement>("[data-mode-id]").forEach((option) => {
    option.addEventListener("click", (event) => {
      event.stopPropagation();
      const modeID = option.dataset.modeId ?? "";
      if (!modeID) return;
      selectAgentMode(modeID);
    });
  });
  root.querySelectorAll<HTMLLIElement>("[data-mode-create]").forEach((option) => {
    option.addEventListener("click", (event) => {
      event.stopPropagation();
      createAgentModeFromChat();
    });
  });
  root.querySelectorAll<HTMLButtonElement>("[data-mode-delete-id]").forEach((button) => {
    button.addEventListener("click", (event) => {
      event.stopPropagation();
      const modeID = button.dataset.modeDeleteId ?? "";
      if (!modeID) return;
      deleteAgentMode(modeID);
    });
  });
}

async function selectAgentMode(modeID: string) {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  setChatAgentMode(workspace.id, modeID);
  dismissModeDropdown();
  patchChatPanel();
}

async function deleteAgentMode(modeID: string) {
  const workspace = activeWorkspace();
  if (!workspace) return;
  const modes = agentModesForWorkspace(workspace.id);
  const mode = modes.find((m) => m.id === modeID);
  const name = mode?.name ?? modeID;
  if (!window.confirm(`Delete agent mode "${name}"?`)) {
    return;
  }
  try {
    const updated = await DeleteAgentMode(modeID);
    state.agentModes.set(workspace.id, Array.isArray(updated) ? updated : []);
    /* Clear selection if deleted mode was active. */
    if (chatAgentModeIDFor(workspace.id) === modeID) {
      setChatAgentMode(workspace.id, "");
    }
    dismissModeDropdown();
    patchChatPanel();
  } catch (error) {
    pushToast(errorMessage(error), "error");
  }
}

async function createAgentModeFromChat() {
  const workspace = activeWorkspace();
  const chatKey = workspace ? chatStateKey(workspace.id) : "";
  if (!workspace || state.creatingAgentModes.has(chatKey)) {
    return;
  }
  dismissModeDropdown();
  state.creatingAgentModes.add(chatKey);
  patchChatPanel();
  try {
    const result = await CreateAgentModeFromChatForTab(workspace.id, activeChatIDFor(workspace.id));
    const modes = await ListAgentModes(workspace.id);
    state.agentModes.set(workspace.id, modes);
    setChatAgentMode(workspace.id, result.id);
    pushToast(`Created agent mode "${result.name}".`, "success");
  } catch (error) {
    pushToast(errorMessage(error), "error");
  } finally {
    state.creatingAgentModes.delete(chatKey);
    patchChatPanel();
  }
}

function bindClearChatButton(root: ParentNode) {
  root.querySelectorAll<HTMLButtonElement>("[data-clear-chat-button]").forEach((button) => {
    button.addEventListener("click", (event) => {
      event.stopPropagation();
      dismissChatMoreMenu();
      const workspace = activeWorkspace();
      if (!workspace || !window.confirm("Clear the current chat?")) {
        return;
      }
      const chatID = activeChatIDFor(workspace.id);
      void ClearChatForTab(workspace.id, chatID).then((result: services.ChatSession) => {
        applyChatSessionSnapshot(result);
        const key = chatStateKey(workspace.id, chatID);
        state.chatDrafts.set(key, "");
        state.chatImageDrafts.delete(key);
        state.chatVideoDrafts.delete(key);
        patchChatPanel();
      });
    });
  });
}

function bindCreateSkillButton(root: ParentNode) {
  root.querySelectorAll<HTMLButtonElement>("[data-create-skill-button]").forEach((button) => {
    button.addEventListener("click", (event) => {
      if (button.disabled) {
        return;
      }
      event.stopPropagation();
      dismissChatMoreMenu();
      const workspace = activeWorkspace();
      const chatKey = workspace ? chatStateKey(workspace.id) : "";
      if (!workspace || state.creatingChatSkills.has(chatKey)) {
        return;
      }
      state.creatingChatSkills.add(chatKey);
      patchChatPanel();
      CreateSkillFromChatForTab(workspace.id, activeChatIDFor(workspace.id))
        .then((skill: services.WorkspaceSkillCreationResult) => {
          pushToast(`Created skill "${skill.name}".`, "success");
        })
        .catch((error) => {
          console.error("Failed to create workspace skill:", error);
          pushToast(errorMessage(error), "error");
        })
        .finally(() => {
          state.creatingChatSkills.delete(chatKey);
          patchChatPanel();
        });
    });
  });
}

export function bindChatDebugSections(root: ParentNode) {
  const sections = Array.from(root.querySelectorAll<HTMLDetailsElement>("[data-debug-section]"));
  if (root instanceof HTMLDetailsElement && root.matches("[data-debug-section]")) {
    sections.unshift(root);
  }
  sections.forEach((section) => {
    if (section.dataset.debugSectionBound) {
      return;
    }
    section.dataset.debugSectionBound = "true";
    section.addEventListener("toggle", handleChatDebugSectionToggle);
  });
}

export function handleChatDebugSectionToggle(event: Event) {
  const section = event.currentTarget as HTMLDetailsElement;
  if (!section.open) {
    return;
  }
  const article = section.closest<HTMLElement>("[data-message-id]");
  const workspace = activeWorkspace();
  const messageID = article?.dataset.messageId ?? "";
  const message = workspace
    ? (chatSessionFor(workspace.id).messages ?? []).find((item) => item.id === messageID)
    : null;
  const stack = article?.querySelector<HTMLElement>("[data-debug-stack]");
  if (!message || !stack) {
    return;
  }
  patchDebugSections(stack, message);
  void linkifyAssistantFilePaths(section);
  resolveChatTaskRefs(section);
}


export function bindChatEditForms(root: ParentNode) {
  root
    .querySelectorAll<HTMLFormElement>("[data-chat-edit-form]")
    .forEach((form) => form.addEventListener("submit", handleChatEditSubmit));
  root
    .querySelectorAll<HTMLTextAreaElement>("[data-chat-edit-input]")
    .forEach((input) => {
      input.addEventListener("keydown", handleChatEditKeydown);
    });
}

export function handleChatInput(event: Event) {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  const input = event.currentTarget as HTMLTextAreaElement;
  resizeChatInput(input);
  state.chatDrafts.set(chatStateKey(workspace.id), input.value);
  syncChatMentionForInput(workspace.id, input);
  patchChatControls();
}

export function resizeChatInput(input: HTMLTextAreaElement) {
  input.style.height = "auto";
  input.style.height = `${input.scrollHeight}px`;
}

export function handleChatPaste(event: ClipboardEvent) {
  const workspace = activeWorkspace();
  if (!workspace || chatSessionFor(workspace.id).busy || state.executingPlans.has(chatStateKey(workspace.id))) {
    return;
  }
  const items = Array.from(event.clipboardData?.items ?? [])
    .filter((item) => item.kind === "file")
    .map((item) => item.getAsFile())
    .filter((file): file is File => file !== null);
  if (!items.length) {
    return;
  }
  const imageFiles = items.filter((f) => f.type.startsWith("image/"));
  const videoFiles = items.filter((f) => f.type.startsWith("video/"));
  if (!imageFiles.length && !videoFiles.length) {
    return;
  }
  event.preventDefault();
  if (imageFiles.length > 0) {
    void addPastedChatImages(workspace.id, imageFiles);
  }
  if (videoFiles.length > 0) {
    void addPastedChatVideos(workspace.id, videoFiles);
  }
}

let chatAttachmentMenuOpen = false;
let chatMoreMenuOpen = false;
let chatDismissalListenerBound = false;

export function handleChatAttachmentToggle(event: Event) {
  const workspace = activeWorkspace();
  if (!workspace || chatSessionFor(workspace.id).busy || state.executingPlans.has(chatStateKey(workspace.id))) {
    return;
  }
  const button = event.currentTarget as HTMLButtonElement;
  if (button.disabled) {
    return;
  }
  // On mobile viewports, bypass the menu and open the native file picker directly
  if (window.innerWidth <= 720) {
    void selectChatMediaFiles(workspace.id);
    return;
  }
  const menu = appRoot.querySelector<HTMLElement>("[data-chat-attachment-menu]");
  if (!menu) {
    return;
  }
  chatAttachmentMenuOpen = !chatAttachmentMenuOpen;
  button.setAttribute("aria-expanded", String(chatAttachmentMenuOpen));
  if (chatAttachmentMenuOpen) {
    dismissModelDropdown();
    dismissModeDropdown();
    dismissChatMoreMenu();
    menu.hidden = false;
  } else {
    menu.hidden = true;
  }
}

function dismissChatAttachmentMenu() {
  chatAttachmentMenuOpen = false;
  const toggle = appRoot.querySelector<HTMLButtonElement>("[data-chat-attachment-toggle]");
  const menu = appRoot.querySelector<HTMLElement>("[data-chat-attachment-menu]");
  if (toggle) {
    toggle.setAttribute("aria-expanded", "false");
  }
  if (menu) {
    menu.hidden = true;
  }
}

function handleChatMoreToggle(event: Event) {
  const button = event.currentTarget as HTMLButtonElement;
  if (button.disabled) {
    return;
  }
  const menu = appRoot.querySelector<HTMLElement>("[data-chat-more-menu]");
  if (!menu) {
    return;
  }
  chatMoreMenuOpen = !chatMoreMenuOpen;
  button.setAttribute("aria-expanded", String(chatMoreMenuOpen));
  if (chatMoreMenuOpen) {
    dismissModelDropdown();
    dismissModeDropdown();
    /* Show temporarily with visibility:hidden so we can measure without flash */
    menu.hidden = false;
    menu.style.visibility = "hidden";
    const menuHeight = menu.offsetHeight || 0;
    const buttonRect = button.getBoundingClientRect();
    const container = button.closest<HTMLElement>(".chat-composer-toolbar-left");
    const containerRect = container?.getBoundingClientRect() ?? { left: 0, top: 0 };
    /* Position relative to .chat-composer-toolbar-left (position: relative) */
    menu.style.left = `${buttonRect.left - containerRect.left}px`;
    menu.style.top = `${buttonRect.top - containerRect.top - menuHeight - 4}px`;
    /* Now make it visible */
    menu.style.visibility = "";
  } else {
    menu.hidden = true;
  }
}

export function dismissChatMoreMenu() {
  chatMoreMenuOpen = false;
  const toggle = appRoot.querySelector<HTMLButtonElement>("[data-chat-more-toggle]");
  const menu = appRoot.querySelector<HTMLElement>("[data-chat-more-menu]");
  if (toggle) {
    toggle.setAttribute("aria-expanded", "false");
  }
  if (menu) {
    menu.hidden = true;
  }
}

export function handleChatAttachmentSelect(event: Event) {
  const workspace = activeWorkspace();
  if (!workspace || chatSessionFor(workspace.id).busy || state.executingPlans.has(chatStateKey(workspace.id))) {
    return;
  }
  const button = event.currentTarget as HTMLButtonElement;
  const type = button.dataset.attachmentType;
  dismissChatAttachmentMenu();
  if (type === "image") {
    void selectChatImageFiles(workspace.id);
  } else if (type === "video") {
    void selectChatVideoFiles(workspace.id);
  }
}

function selectChatImageFiles(workspaceID: string): Promise<void> {
  return new Promise((resolve) => {
    const input = document.createElement("input");
    input.type = "file";
    input.multiple = true;
    input.accept = "image/png,image/jpeg,image/gif,image/webp";
    input.style.position = "fixed";
    input.style.left = "-9999px";
    input.addEventListener("change", async () => {
      const files = Array.from(input.files ?? []);
      input.remove();
      if (files.length > 0) {
        await addPastedChatImages(workspaceID, files);
      }
      resolve();
    }, { once: true });
    input.addEventListener("cancel", () => {
      input.remove();
      resolve();
    }, { once: true });
    document.body.appendChild(input);
    input.click();
  });
}

export async function addPastedChatImages(workspaceID: string, files: File[]) {
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
  state.chatImageDrafts.set(chatStateKey(workspaceID), [...current, ...accepted]);
  patchChatPanel();
  patchChatControls();
  appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]")?.focus();
}

export function handleChatVideoUpload(event: Event) {
  const workspace = activeWorkspace();
  if (!workspace || chatSessionFor(workspace.id).busy || state.executingPlans.has(chatStateKey(workspace.id))) {
    return;
  }
  const button = event.currentTarget as HTMLButtonElement;
  if (button.disabled) {
    return;
  }
  void selectChatVideoFiles(workspace.id);
}

function selectChatMediaFiles(workspaceID: string): Promise<void> {
  return new Promise((resolve) => {
    const input = document.createElement("input");
    input.type = "file";
    input.multiple = true;
    input.accept = "image/png,image/jpeg,image/gif,image/webp,video/mp4,video/webm,video/quicktime";
    input.style.position = "fixed";
    input.style.left = "-9999px";
    input.addEventListener("change", async () => {
      const files = Array.from(input.files ?? []);
      input.remove();
      if (files.length > 0) {
        const imageFiles = files.filter((f) => f.type.startsWith("image/"));
        const videoFiles = files.filter((f) => f.type.startsWith("video/"));
        if (imageFiles.length > 0) {
          await addPastedChatImages(workspaceID, imageFiles);
        }
        if (videoFiles.length > 0) {
          await addPastedChatVideos(workspaceID, videoFiles);
        }
      }
      resolve();
    }, { once: true });
    input.addEventListener("cancel", () => {
      input.remove();
      resolve();
    }, { once: true });
    document.body.appendChild(input);
    input.click();
  });
}

function selectChatVideoFiles(workspaceID: string): Promise<void> {
  return new Promise((resolve) => {
    const input = document.createElement("input");
    input.type = "file";
    input.multiple = true;
    input.accept = "video/mp4,video/webm,video/quicktime";
    input.style.position = "fixed";
    input.style.left = "-9999px";
    input.addEventListener("change", async () => {
      const files = Array.from(input.files ?? []);
      input.remove();
      if (files.length > 0) {
        await addPastedChatVideos(workspaceID, files);
      }
      resolve();
    }, { once: true });
    input.addEventListener("cancel", () => {
      input.remove();
      resolve();
    }, { once: true });
    document.body.appendChild(input);
    input.click();
  });
}

export async function addPastedChatVideos(workspaceID: string, files: File[]) {
  const current = chatVideoDraftsFor(workspaceID);
  const accepted: ChatVideoDraft[] = [];
  let totalBytes = chatVideoDraftTotalBytes(workspaceID);
  for (const file of files) {
    const mediaType = file.type.toLowerCase();
    if (!isSupportedChatVideoType(mediaType)) {
      pushToast(`Unsupported video format: ${file.type || file.name}`, "error");
      continue;
    }
    if (file.size > maxChatVideoBytes) {
      pushToast(`${file.name || "Pasted video"} is larger than ${formatBytes(maxChatVideoBytes)}.`, "error");
      continue;
    }
    if (current.length + accepted.length >= maxChatVideoDrafts) {
      pushToast(`A message can include at most ${maxChatVideoDrafts} videos.`, "error");
      break;
    }
    if (totalBytes + file.size > maxChatImageMessageBytes) {
      pushToast(`Attached videos are larger than ${formatBytes(maxChatImageMessageBytes)}.`, "error");
      break;
    }
    try {
      accepted.push({
        id: `draft-${Date.now()}-${Math.random().toString(36).slice(2)}`,
        name: file.name || `pasted-video-${current.length + accepted.length + 1}`,
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
  state.chatVideoDrafts.set(chatStateKey(workspaceID), [...current, ...accepted]);
  patchChatPanel();
  patchChatControls();
  appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]")?.focus();
}

export function handleChatPlanModeChange(event: Event) {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  const currentMode = chatComposerModeFor(workspace.id);
  setChatComposerMode(workspace.id, currentMode === "plan" ? "edit" : "plan");
}

export function handleChatKeydown(event: KeyboardEvent) {
  const workspace = activeWorkspace();
  const chatBusy = workspace ? chatSessionFor(workspace.id).busy : false;
  if (event.key === "Enter" && !event.shiftKey && !event.isComposing && chatBusy) {
    event.preventDefault();
    return;
  }
  if (handleChatMentionKeydown(event)) {
    return;
  }
  if (event.key !== "Enter" || event.shiftKey || event.isComposing || window.innerWidth <= 720) {
    return;
  }
  event.preventDefault();
  const input = event.currentTarget as HTMLTextAreaElement;
  input.form?.requestSubmit();
}

export function handleChatMentionKeydown(event: KeyboardEvent): boolean {
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

export async function handleSendStopClick(event: Event) {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  const session = chatSessionFor(workspace.id);
  const chatID = activeChatIDFor(workspace.id);
  const chatKey = chatStateKey(workspace.id, chatID);
  const executing = state.executingPlans.has(chatKey);
  if (session.busy || executing) {
    // Stop the stream
    applyChatSessionSnapshot(await StopChatStreamForTab(workspace.id, chatID));
    patchChatPanel();
    return;
  }
  // Send the message – reuse the same logic as the form submit
  const draft = (state.chatDrafts.get(chatKey) ?? "").trim();
  const imageDrafts = chatImageDraftsFor(workspace.id);
  const videoDrafts = chatVideoDraftsFor(workspace.id);
  if ((!draft && imageDrafts.length === 0 && videoDrafts.length === 0)) {
    return;
  }
  void (async () => {
    try {
      const nextSession = await SendChatMessageWithAttachmentsToTab(
        workspace.id,
        chatID,
        services.ChatMessageRequest.createFrom({
          content: draft,
          agentModeId: chatAgentModeIDFor(workspace.id),
          images: imageDrafts.map((image) =>
            services.ChatImageInput.createFrom({
              id: image.id,
              name: image.name,
              mediaType: image.mediaType,
              dataUrl: image.dataUrl,
              bytes: image.bytes,
            }),
          ),
          videos: videoDrafts.map((video) =>
            services.ChatVideoInput.createFrom({
              id: video.id,
              name: video.name,
              mediaType: video.mediaType,
              dataUrl: video.dataUrl,
              bytes: video.bytes,
            }),
          ),
        }),
      );
      state.chatDrafts.set(chatKey, "");
      state.chatImageDrafts.delete(chatKey);
      state.chatVideoDrafts.delete(chatKey);
      clearChatMention();
      const input = appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]");
      if (input) {
        input.value = "";
      }
      applyChatSessionSnapshot(nextSession);
      getAppCallbacks().render();
      if (activeChatIDFor(workspace.id) === chatID) {
        scrollChatToBottom();
      }
    } catch (error) {
      pushToast(errorMessage(error), "error");
      getAppCallbacks().render();
    }
  })();
}


export async function handleChatSubmit(event: SubmitEvent) {
  event.preventDefault();
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  const chatID = activeChatIDFor(workspace.id);
  const chatKey = chatStateKey(workspace.id, chatID);
  const message = (state.chatDrafts.get(chatKey) ?? "").trim();
  const imageDrafts = chatImageDraftsFor(workspace.id);
  const videoDrafts = chatVideoDraftsFor(workspace.id);
  const session = chatSessionFor(workspace.id);
  if ((!message && imageDrafts.length === 0 && videoDrafts.length === 0) || session.busy || state.executingPlans.has(chatKey)) {
    return;
  }

  try {
    const nextSession = await SendChatMessageWithAttachmentsToTab(
      workspace.id,
      chatID,
      services.ChatMessageRequest.createFrom({
        content: message,
        agentModeId: chatAgentModeIDFor(workspace.id),
        images: imageDrafts.map((image) =>
          services.ChatImageInput.createFrom({
            id: image.id,
            name: image.name,
            mediaType: image.mediaType,
            dataUrl: image.dataUrl,
            bytes: image.bytes,
          }),
        ),
        videos: videoDrafts.map((video) =>
          services.ChatVideoInput.createFrom({
            id: video.id,
            name: video.name,
            mediaType: video.mediaType,
            dataUrl: video.dataUrl,
            bytes: video.bytes,
          }),
        ),
      }),
    );
    state.chatDrafts.set(chatKey, "");
    state.chatImageDrafts.delete(chatKey);
    state.chatVideoDrafts.delete(chatKey);
    clearChatMention();
    const input = appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]");
    if (input) {
      input.value = "";
    }
    applyChatSessionSnapshot(nextSession);
    getAppCallbacks().render();
    if (activeChatIDFor(workspace.id) === chatID) {
      scrollChatToBottom();
    }
  } catch (error) {
    pushToast(errorMessage(error), "error");
    getAppCallbacks().render();
  }
}

export async function handleChatEditSubmit(event: SubmitEvent) {
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
    const editedMessage = (chatSessionFor(workspace.id).messages ?? []).find((message) => message.id === messageID);
    applyChatSessionSnapshot(
      await EditChatMessageForTab(workspace.id, activeChatIDFor(workspace.id), messageID, trimmed, chatAgentModeIDFor(workspace.id)),
    );
    state.editingMessageIds.delete(messageID);
    if (editedMessage?.role === "user") {
      state.chatDrafts.delete(chatStateKey(workspace.id));
    }
    getAppCallbacks().render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    getAppCallbacks().render();
  }
}

export function handleChatEditKeydown(event: KeyboardEvent) {
  if (event.key === "Escape") {
    event.preventDefault();
    const input = event.currentTarget as HTMLTextAreaElement;
    const form = input.closest<HTMLFormElement>("[data-chat-edit-form]");
    const messageID = form?.dataset.messageId ?? "";
    if (messageID) {
      state.editingMessageIds.delete(messageID);
      getAppCallbacks().render();
    }
    return;
  }
  if (event.key !== "Enter" || event.shiftKey || event.isComposing || window.innerWidth <= 720) {
    return;
  }
  event.preventDefault();
  const input = event.currentTarget as HTMLTextAreaElement;
  input.form?.requestSubmit();
}

export async function loadActiveChatSession() {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  const chatWorkspace = await LoadChatWorkspace(workspace.id);
  applyChatWorkspaceSnapshot(chatWorkspace);
}

export function applyChatSessionSnapshot(nextSession: services.ChatSession): boolean {
  const key = chatStateKey(nextSession.workspaceId, nextSession.chatId);
  const current = state.chatSessions.get(key);
  if (current && (nextSession.revision ?? 0) < (current.revision ?? 0)) {
    return false;
  }
  state.chatSessions.set(key, nextSession);
  const chatWorkspace = state.chatWorkspaces.get(nextSession.workspaceId);
  const tab = chatWorkspace?.tabs?.find((candidate) => candidate.chatId === nextSession.chatId);
  if (tab) {
    tab.preview = nextSession.preview || "New chat";
    tab.busy = Boolean(nextSession.busy);
    tab.revision = nextSession.revision ?? tab.revision;
  }
  return true;
}

export function applyChatWorkspaceSnapshot(nextWorkspace: services.ChatWorkspaceState): void {
  const workspace = services.ChatWorkspaceState.createFrom(nextWorkspace);
  const previous = state.chatWorkspaces.get(workspace.workspaceId);
  const nextIDs = new Set((workspace.tabs ?? []).map((tab) => tab.chatId));
  for (const tab of previous?.tabs ?? []) {
    if (nextIDs.has(tab.chatId)) {
      continue;
    }
    const key = chatStateKey(workspace.workspaceId, tab.chatId);
    state.chatSessions.delete(key);
    state.chatDrafts.delete(key);
    state.chatImageDrafts.delete(key);
    state.chatVideoDrafts.delete(key);
    state.chatComposerModes.delete(key);
    state.chatPlanModes.delete(key);
    state.selectedAgentModeIds.delete(key);
  }
  state.chatWorkspaces.set(workspace.workspaceId, workspace);
  if (workspace.activeSession?.chatId) {
    applyChatSessionSnapshot(services.ChatSession.createFrom(workspace.activeSession));
  }
}

export function reloadChatSession(workspaceID: string, chatID = activeChatIDFor(workspaceID)): Promise<void> {
  const key = chatStateKey(workspaceID, chatID);
  const existing = chatSessionReloads.get(key);
  if (existing) {
    return existing;
  }
  const reload = LoadChatSessionForTab(workspaceID, chatID)
    .then((session) => {
      if (applyChatSessionSnapshot(session) && activeWorkspace()?.id === workspaceID && activeChatIDFor(workspaceID) === chatID) {
        patchChatPanel();
      }
    })
    .finally(() => {
      chatSessionReloads.delete(key);
    });
  chatSessionReloads.set(key, reload);
  return reload;
}

export function applyChatStreamEvent(event: ChatStreamEvent) {
  if (event.type === "compaction_warning" && event.content) {
    pushToast(event.content, "info");
  }

  if (event.workspaceState) {
    applyChatWorkspaceSnapshot(services.ChatWorkspaceState.createFrom(event.workspaceState));
    if (activeWorkspace()?.id === event.workspaceId) {
      patchChatPanel();
    }
    return;
  }

  if (event.session) {
    const snapshot = services.ChatSession.createFrom(event.session);
    if (!applyChatSessionSnapshot(snapshot)) {
      return;
    }
    if (activeWorkspace()?.id === event.workspaceId && activeChatIDFor(event.workspaceId) === snapshot.chatId) {
      patchChatPanel();
      if (event.type === "started") {
        scrollChatToBottom();
      }
    }
    return;
  }

  const chatID = event.chatId || activeChatIDFor(event.workspaceId);
  const session = chatSessionFor(event.workspaceId, chatID);
  const currentRevision = session.revision ?? 0;
  const eventRevision = event.revision ?? 0;
  const stateful = event.type === "token" || event.type === "reasoning" || event.type === "agent_reasoning" || event.type === "tool_call" || event.type === "agent_status" ||
    event.type === "complete" || event.type === "canceled" || event.type === "error" ||
    event.type === "retrying" || event.type === "compacting";
  if (stateful && eventRevision > 0) {
    if (eventRevision <= currentRevision) {
      return;
    }
    if (eventRevision !== currentRevision + 1) {
      void reloadChatSession(event.workspaceId, chatID).catch(() => {});
      return;
    }
  }
  const messages = session.messages ?? [];
  const message = messages.find((item) => item.id === event.messageId);
  if (!message) {
    if (stateful) {
      void reloadChatSession(event.workspaceId, chatID).catch(() => {});
    }
    return;
  }

  if (event.type === "token") {
    message.content = `${message.content ?? ""}${event.content ?? ""}`;
  }
  if (event.type === "reasoning") {
    message.reasoning = `${message.reasoning ?? ""}${event.reasoning ?? ""}`;
  }
  if (event.type === "agent_reasoning" && event.researchReasoning) {
    const updates = message.researchReasoning ?? [];
    const next = services.ChatResearchReasoning.createFrom(event.researchReasoning);
    const index = updates.findIndex((item) => item.agentId === next.agentId);
    if (index >= 0) {
      const existing = updates[index];
      const combined = next.replace
        ? next.reasoning
        : `${existing.reasoning ?? ""}${next.reasoning ?? ""}`;
      const bounded = boundResearchReasoning(combined);
      existing.agentName = next.agentName || existing.agentName;
      existing.reasoning = bounded.reasoning;
      existing.truncated = Boolean(existing.truncated || next.truncated || bounded.truncated);
      existing.replace = false;
    } else {
      const bounded = boundResearchReasoning(next.reasoning ?? "");
      next.reasoning = bounded.reasoning;
      next.truncated = Boolean(next.truncated || bounded.truncated);
      next.replace = false;
      updates.push(next);
    }
    message.researchReasoning = updates;
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
  if (event.type === "agent_status") {
    const agents = message.researchAgents ?? [];
    if (!event.researchAgent) {
      message.researchAgents = [];
    } else {
      const next = services.ChatResearchAgent.createFrom(event.researchAgent);
      const active = next.status === "queued" || next.status === "running" || next.status === "summarizing";
      const index = agents.findIndex((item) => item.id === next.id);
      if (active && index >= 0) {
        agents[index] = next;
      } else if (active) {
        agents.push(next);
      } else if (index >= 0) {
        agents.splice(index, 1);
      }
      message.researchAgents = agents;
    }
  }
  if (event.type === "complete" || event.type === "canceled" || event.type === "error") {
    session.busy = false;
    session.streamId = "";
    message.status = event.type === "complete" ? "complete" : event.type;
    message.error = event.error ?? "";
    if (event.type === "complete") {
      playNotificationSound();
      maybeSendChatCompletionNotification(event.workspaceId);
    }
  }
  if (event.type === "retrying") {
    message.status = "retrying";
    message.error = "";
  }
  if (event.type === "compacting") {
    message.status = "compacting";
    message.error = "";
  }
  if (stateful && eventRevision > 0) {
    session.revision = eventRevision;
  }
  if (event.type === "image_attached" && event.imageAttachment) {
    const images = message.images ?? [];
    images.push(services.ChatImageAttachment.createFrom(event.imageAttachment));
    message.images = images;
  }
  if (event.type === "video_attached" && event.videoAttachment) {
    const videos = message.videos ?? [];
    videos.push(services.ChatVideoAttachment.createFrom(event.videoAttachment));
    message.videos = videos;
  }

  state.chatSessions.set(chatStateKey(event.workspaceId, chatID), session);
  const chatWorkspace = state.chatWorkspaces.get(event.workspaceId);
  const tab = chatWorkspace?.tabs?.find((candidate) => candidate.chatId === chatID);
  if (tab) {
    tab.busy = Boolean(session.busy);
    tab.revision = session.revision ?? tab.revision;
  }
  if (activeWorkspace()?.id === event.workspaceId && activeChatIDFor(event.workspaceId) === chatID) {
    const terminal = event.type === "complete" || event.type === "canceled" || event.type === "error";
    queueChatStreamPatch(
      event.workspaceId,
      chatID,
      message,
      event.type !== "token",
      terminal || event.type === "retrying" || event.type === "compacting",
      terminal,
      terminal,
    );
  }
}

export function queueChatStreamPatch(
  workspaceID: string,
  chatID: string,
  message: services.ChatMessage,
  patchDebug: boolean,
  patchControls: boolean,
  linkify: boolean,
  flushImmediately = false,
) {
  const key = chatStateKey(workspaceID, chatID);
  const pending = pendingChatStreamPatches.get(key);
  if (pending) {
    pending.message = message;
    pending.patchDebug ||= patchDebug;
    pending.patchControls ||= patchControls;
    pending.linkify ||= linkify;
    if (!flushImmediately) {
      return;
    }
    window.clearTimeout(pending.timeoutID);
    pendingChatStreamPatches.delete(key);
    applyPendingChatStreamPatch(pending);
    return;
  }

  const next: PendingChatStreamPatch = {
    workspaceID,
    chatID,
    message,
    patchDebug,
    patchControls,
    linkify,
    timeoutID: 0,
  };
  if (flushImmediately) {
    applyPendingChatStreamPatch(next);
    return;
  }
  next.timeoutID = window.setTimeout(() => {
    if (pendingChatStreamPatches.get(key) !== next) {
      return;
    }
    pendingChatStreamPatches.delete(key);
    applyPendingChatStreamPatch(next);
  }, chatStreamPatchDelay);
  pendingChatStreamPatches.set(key, next);
}

export function applyPendingChatStreamPatch(pending: PendingChatStreamPatch) {
  if (activeWorkspace()?.id !== pending.workspaceID || activeChatIDFor(pending.workspaceID) !== pending.chatID) {
    return;
  }
  const panel = appRoot.querySelector<HTMLElement>("[data-chat-panel]");
  if (!panel || panel.dataset.workspaceId !== pending.workspaceID || panel.dataset.chatId !== pending.chatID) {
    return;
  }
  const keepChatPinned = isElementScrolledNearBottom(
    panel.querySelector<HTMLElement>("[data-chat-log]"),
  );
  patchChatMessage(pending.message, pending.patchDebug, pending.linkify);
  if (pending.patchControls) {
    patchChatControls();
  }
  if (keepChatPinned) {
    scrollChatToBottom();
  }
}

export function patchChatMessage(
  message: services.ChatMessage,
  patchDebug = true,
  linkify = !isAssistantMessageStreaming(message),
) {
  const element = appRoot.querySelector<HTMLElement>(
    `[data-message-id="${CSS.escape(message.id)}"]`,
  );
  if (!element) {
    if (appRoot.querySelector("[data-chat-panel]")) {
      patchChatPanel();
    }
    return;
  }
  const content = element.querySelector<HTMLElement>("[data-message-content]");
  if (content) {
    patchMarkdownElement(content, message.content ?? "");
  }
  const error = element.querySelector<HTMLElement>("[data-message-error]");
  // Patch images container: replace entire element in-place to avoid nesting
  const imagesContainer = element.querySelector<HTMLElement>(".chat-message-images");
  if (imagesContainer) {
    const template = document.createElement("template");
    template.innerHTML = renderChatMessageImages(message);
    const newContent = template.content;
    if (!message.images?.length && newContent.childElementCount === 0) {
      imagesContainer.remove();
    } else {
      element.replaceChild(newContent, imagesContainer);
    }
  } else if (message.images?.length) {
    const template = document.createElement("template");
    template.innerHTML = renderChatMessageImages(message);
    element.insertBefore(template.content, content || error);
  }
  // Patch videos container: same pattern
  const videosContainer = element.querySelector<HTMLElement>(".chat-message-videos");
  if (videosContainer) {
    const template = document.createElement("template");
    template.innerHTML = renderChatMessageVideos(message);
    const newContent = template.content;
    if (!message.videos?.length && newContent.childElementCount === 0) {
      videosContainer.remove();
    } else {
      element.replaceChild(newContent, videosContainer);
    }
  } else if (message.videos?.length) {
    const template = document.createElement("template");
    template.innerHTML = renderChatMessageVideos(message);
    element.insertBefore(template.content, content || error);
  }
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
  if (message.role === "assistant" && linkify) {
    void linkifyAssistantFilePaths(element);
  }
  resolveChatTaskRefs(element);
}

export function patchMessageStatus(element: HTMLElement, message: services.ChatMessage) {
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

export function patchMessageActions(element: HTMLElement, message: services.ChatMessage) {
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
  const edit = element.querySelector<HTMLButtonElement>(".chat-edit-trigger");
  if (edit) {
    edit.disabled = message.status !== "complete";
  }
}

export function patchDebugSections(stack: HTMLElement, message: services.ChatMessage) {
  if (message.role !== "assistant") {
    return;
  }

  const reasoning = message.reasoning ?? "";
  const researchReasoning = (message.researchReasoning ?? []).filter((entry) => Boolean(entry.reasoning));
  const hasReasoning = Boolean(reasoning) || researchReasoning.length > 0;
  const toolCalls = message.toolCalls ?? [];
  const researchAgents = message.researchAgents ?? [];
  let researchStrip = stack.querySelector<HTMLElement>("[data-research-agent-strip]");
  if (researchAgents.length) {
    if (!researchStrip) {
      researchStrip = elementFromHtml(renderResearchAgents(researchAgents)) as HTMLElement;
      stack.insertBefore(researchStrip, stack.firstChild);
    } else {
      morphElement(researchStrip, elementFromHtml(renderResearchAgents(researchAgents)));
    }
  } else {
    researchStrip?.remove();
  }
  let reasoningSection = stack.querySelector<HTMLDetailsElement>(
    '[data-debug-section="reasoning"]',
  );
  if (hasReasoning) {
    if (!reasoningSection) {
      reasoningSection = elementFromHtml(renderReasoning(reasoning, researchReasoning)) as HTMLDetailsElement;
      const toolsSection = stack.querySelector<HTMLElement>(
        '[data-debug-section="tools"]',
      );
      stack.insertBefore(reasoningSection, toolsSection);
      bindChatDebugSections(reasoningSection);
    } else if (reasoningSection.open || !isAssistantMessageStreaming(message)) {
      const thinkingList = reasoningSection.querySelector<HTMLElement>("[data-thinking-list]");
      const keepThinkingPinned = isThinkingListAtBottom(thinkingList);
      morphElement(
        reasoningSection,
        elementFromHtml(renderReasoning(reasoning, researchReasoning)),
      );
      if (keepThinkingPinned) {
        const nextThinkingList = reasoningSection.querySelector<HTMLElement>("[data-thinking-list]");
        if (nextThinkingList) {
          nextThinkingList.scrollTop = nextThinkingList.scrollHeight;
        }
      }
    }
  } else {
    reasoningSection?.remove();
  }

  let toolsSection = stack.querySelector<HTMLDetailsElement>(
    '[data-debug-section="tools"]',
  );
  if (toolCalls.length) {
    if (!toolsSection) {
      toolsSection = elementFromHtml(renderToolCalls([])) as HTMLDetailsElement;
      stack.appendChild(toolsSection);
      bindChatDebugSections(toolsSection);
    }
    const toolList = toolsSection.querySelector<HTMLElement>("[data-tool-list]");
    if (toolList && (toolsSection.open || !isAssistantMessageStreaming(message))) {
      patchChildrenFromHtml(toolList, toolCalls.map(renderToolCall).join(""));
    } else {
      if (!toolList) {
        morphElement(toolsSection, elementFromHtml(renderToolCalls(toolCalls)));
      }
    }
  } else {
    toolsSection?.remove();
  }
}

function isThinkingListAtBottom(element: HTMLElement | null): boolean {
  if (!element) {
    return true;
  }
  return (
    element.scrollHeight - element.scrollTop - element.clientHeight <=
    thinkingScrollBottomTolerance
  );
}

export function patchChatPanel() {
  const workspace = activeWorkspace();
  const panel = appRoot.querySelector<HTMLElement>("[data-chat-panel]");
  if (!workspace || !panel) {
    return;
  }

  // Preserve the current draft value and scroll position before regenerating the panel.
  const draft = state.chatDrafts.get(chatStateKey(workspace.id)) ?? "";
  const existingInput = appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]");
  const inputScrollTop = existingInput?.scrollTop ?? 0;
  const restoreInputFocus = document.activeElement === existingInput;
  const selectionStart = existingInput?.selectionStart ?? draft.length;
  const selectionEnd = existingInput?.selectionEnd ?? selectionStart;
  const selectionDirection = existingInput?.selectionDirection ?? "none";

  const next = document.createElement("template");
  next.innerHTML = renderChatPanel(workspace, state.expandedChatWorkspaces.has(workspace.id)).trim();
  const replacement = next.content.firstElementChild as HTMLElement;
  panel.replaceWith(replacement);

  // Restore the draft to the newly created textarea if it differs from the rendered value.
  const input = replacement.querySelector<HTMLTextAreaElement>("[data-chat-input]");
  if (input && input.value !== draft) {
    input.value = draft;
  }
  if (input) {
    input.scrollTop = inputScrollTop;
    if (restoreInputFocus) {
      input.focus();
      input.setSelectionRange(selectionStart, selectionEnd, selectionDirection);
    }
  }

  getAppCallbacks().bindActionEvents(replacement);
  getAppCallbacks().bindChatEvents(replacement);
  void linkifyAssistantFilePaths(replacement);
  resolveChatTaskRefs(replacement);
}

export function patchChatControls() {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  const session = chatSessionFor(workspace.id);
  const chatKey = chatStateKey(workspace.id);
  const draft = state.chatDrafts.get(chatKey) ?? "";
  const imageDrafts = chatImageDraftsFor(workspace.id);
  const videoDrafts = chatVideoDraftsFor(workspace.id);
  const input = appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]");
  const executing = state.executingPlans.has(chatKey);
  const creatingSkill = state.creatingChatSkills.has(chatKey);
  const locked = session.busy || executing;

  if (input) {
    // Keep the next prompt editable while a chat response is streaming. Plan
    // execution still locks the composer because it operates on chat state.
    input.disabled = executing;
  }

  // Update the send/stop button
  appRoot.querySelectorAll<HTMLButtonElement>("[data-action=\"send-stop\"]").forEach((button) => {
    button.classList.toggle("is-busy", locked);
    if (locked) {
      button.title = "Stop stream";
      button.setAttribute("aria-label", "Stop stream");
      button.innerHTML = icons.stop;
      button.disabled = false;
    } else {
      const draft = (state.chatDrafts.get(chatKey) ?? "").trim();
      const imageDrafts = chatImageDraftsFor(workspace.id);
      const videoDrafts = chatVideoDraftsFor(workspace.id);
      const canSend = draft.length > 0 || imageDrafts.length > 0 || videoDrafts.length > 0;
      button.title = "Send";
      button.setAttribute("aria-label", "Send message");
      button.innerHTML = icons.send;
      button.disabled = !canSend;
    }
  });

  appRoot.querySelectorAll<HTMLButtonElement>("[data-chat-attachment-toggle]").forEach((button) => {
    button.disabled = locked;
  });
  appRoot.querySelectorAll<HTMLButtonElement>("[data-model-selector]").forEach((button) => {
    button.disabled = locked;
  });
  appRoot.querySelectorAll<HTMLButtonElement>("[data-mode-selector]").forEach((button) => {
    button.disabled = locked;
  });
  appRoot.querySelectorAll<HTMLButtonElement>("[data-chat-more-toggle]").forEach((button) => {
    button.disabled = false;
  });
  appRoot.querySelectorAll<HTMLButtonElement>("[data-create-skill-button]").forEach((button) => {
    button.disabled = locked || creatingSkill;
  });
  appRoot
    .querySelectorAll<HTMLButtonElement>("[data-action=\"remove-chat-image\"], [data-action=\"remove-chat-video\"]")
    .forEach((button) => {
      button.disabled = locked;
    });

  // Update all stop buttons (desktop heading + mobile controls)
  appRoot.querySelectorAll<HTMLButtonElement>(".stop-button").forEach((button) => {
    button.disabled = !session.busy;
  });

  // Update all execute buttons (desktop heading + mobile controls)
  appRoot.querySelectorAll<HTMLButtonElement>(".execute-button").forEach((button) => {
    button.disabled = locked || (session.messages ?? []).length === 0;
    button.classList.toggle("is-busy", executing);
    button.title = executing ? "Decomposing cards" : "Execute plan";
    button.setAttribute("aria-label", button.title);
    button.innerHTML = executing ? `<span class="spinner" aria-hidden="true"></span>` : icons.execute;
  });

  // Update all clear-chat buttons (overflow menu + mobile controls)
  appRoot.querySelectorAll<HTMLButtonElement>("[data-clear-chat-button]").forEach((button) => {
    button.disabled = locked || creatingSkill || (session.messages ?? []).length === 0;
  });

  appRoot.querySelectorAll<HTMLButtonElement>(".chat-prune-trigger").forEach((button) => {
    button.disabled = locked;
  });

  const title = appRoot.querySelector<HTMLElement>("#chat-title");
  if (title) {
    title.innerHTML = executing ? renderSpinnerLabel("Triage") : session.busy ? "Working" : "Ready";
  }

  const panel = appRoot.querySelector<HTMLElement>("[data-chat-panel]");
  if (panel) {
    panel.setAttribute("aria-busy", String(locked));
  }
  const activeTab = appRoot.querySelector<HTMLElement>(`[data-chat-tab="${CSS.escape(activeChatIDFor(workspace.id))}"]`);
  if (activeTab) {
    activeTab.classList.toggle("is-busy", Boolean(session.busy));
    const main = activeTab.querySelector<HTMLElement>("[data-chat-tab-main]");
    const dot = activeTab.querySelector<HTMLElement>(".chat-tab-busy-dot");
    if (session.busy && main && !dot) {
      main.insertAdjacentHTML("beforeend", `<span class="chat-tab-busy-dot" title="Chat is running" aria-label="Chat is running"></span>`);
    } else if (!session.busy) {
      dot?.remove();
    }
  }
}

export function scrollChatToBottom() {
  const log = appRoot.querySelector<HTMLElement>("[data-chat-log]");
  if (log) {
    log.scrollTop = log.scrollHeight;
  }
}
