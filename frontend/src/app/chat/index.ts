
import { ensureCodeViewRootLoaded, openWorkspaceCodeFile } from "../../codeView";
import { elementFromHtml, morphElement, patchChildrenFromHtml, patchMarkdownElement, renderMarkdown } from "../../markdown";
import { EditChatMessage, LoadChatSession, ResolveWorkspaceTextFilePath, SearchWorkspaceFiles, SendChatMessageWithAttachments } from "../../../wailsjs/go/services/SystemService";
import { services } from "../../../wailsjs/go/models";
import { getAppCallbacks } from "../callbacks";
import { renderSpinnerLabel } from "../components";
import { appRoot, isElementScrolledNearBottom } from "../dom";
import { icons } from "../icons";
import { playNotificationSound } from "../notifications";
import { activeWorkspace, chatImageDraftsFor, chatImageDraftTotalBytes, chatPlanModeFor, chatSessionFor, state } from "../state";
import { pushToast } from "../toasts";
import type { ChatImageDraft, ChatMentionState, ChatStreamEvent, ScrollSnapshot } from "../types";
import { errorMessage, escapeAttribute, escapeHtml, fileName, formatBytes } from "../utils";

const chatMentionSearchDelay = 160;
const chatMentionResultLimit = 8;
const maxChatImageDrafts = 4;
const maxChatImageBytes = 10 * 1024 * 1024;
const maxChatImageMessageBytes = 20 * 1024 * 1024;
const supportedChatImageTypes = new Set(["image/png", "image/jpeg", "image/webp", "image/gif"]);

export function isSupportedChatImageType(mediaType: string): boolean {
  return supportedChatImageTypes.has(mediaType.toLowerCase());
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
  state.chatDrafts.set(workspace.id, nextValue);
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

export function renderChatMentionOption(
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


export function renderChatPanel(workspace: services.Workspace | null, expanded = false): string {
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
  const draft = state.chatDrafts.get(workspace.id) ?? "";
  const imageDrafts = chatImageDraftsFor(workspace.id);
  const executing = state.executingPlans.has(workspace.id);
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

export function renderChatMessage(message: services.ChatMessage): string {
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
        ${isUser ? renderUserControls(message, isEditing) : renderAssistantControls(message)}
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

export function renderAssistantControls(message: services.ChatMessage): string {
  const isStreaming = isAssistantMessageStreaming(message);
  const canCreateCard = canCreateKanbanCardFromMessage(message);
  return `
    <div class="chat-message-actions">
      ${renderCopyMessageButton(message)}
      <button class="icon-button chat-retry-trigger" type="button" title="Regenerate response" aria-label="Regenerate response" data-action="retry-message" data-message-id="${escapeAttribute(message.id)}">
        ${isStreaming ? '<span class="spinner" aria-hidden="true"></span>' : icons.retry}
      </button>
      <button class="icon-button chat-kanban-trigger" type="button" title="Create Kanban card" aria-label="Create Kanban card from response" data-action="create-card-from-message" data-message-id="${escapeAttribute(message.id)}" ${canCreateCard ? "" : "disabled"}>
        ${icons.kanban}
      </button>
    </div>
  `;
}

export function isAssistantMessageStreaming(message: services.ChatMessage): boolean {
  return message.status === "streaming" || message.status === "retrying" || message.status === "in_progress";
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

export function renderUserControls(message: services.ChatMessage, isEditing: boolean): string {
  return `
    <div class="chat-message-actions">
      ${renderCopyMessageButton(message)}
      ${isEditing
        ? ""
        : `<button class="icon-button chat-edit-trigger" type="button" title="Edit message" aria-label="Edit message" data-action="edit-message" data-message-id="${escapeAttribute(message.id)}">
            ${icons.edit}
          </button>`
      }
    </div>
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

export function renderReasoning(reasoning: string): string {
  return `
    <details class="debug-section" data-debug-section="reasoning">
      <summary>Thinking</summary>
      <div class="debug-content" data-message-reasoning>${renderMarkdown(reasoning)}</div>
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

export function bindChatEvents(root: ParentNode) {
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
  state.chatDrafts.set(workspace.id, input.value);
  syncChatMentionForInput(workspace.id, input);
  patchChatControls();
}

export function handleChatPaste(event: ClipboardEvent) {
  const workspace = activeWorkspace();
  if (!workspace || chatSessionFor(workspace.id).busy || state.executingPlans.has(workspace.id)) {
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
  state.chatImageDrafts.set(workspaceID, [...current, ...accepted]);
  patchChatPanel();
  patchChatControls();
  appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]")?.focus();
}

export function handleChatPlanModeChange(event: Event) {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  const input = event.currentTarget as HTMLInputElement;
  state.chatPlanModes.set(workspace.id, input.checked);
}

export function handleChatKeydown(event: KeyboardEvent) {
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

export async function handleChatSubmit(event: SubmitEvent) {
  event.preventDefault();
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  const message = (state.chatDrafts.get(workspace.id) ?? "").trim();
  const imageDrafts = chatImageDraftsFor(workspace.id);
  const session = chatSessionFor(workspace.id);
  if ((!message && imageDrafts.length === 0) || session.busy || state.executingPlans.has(workspace.id)) {
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
    state.chatDrafts.set(workspace.id, "");
    state.chatImageDrafts.delete(workspace.id);
    clearChatMention();
    state.chatSessions.set(workspace.id, nextSession);
    getAppCallbacks().render();
    scrollChatToBottom();
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
    state.chatSessions.set(
      workspace.id,
      await EditChatMessage(workspace.id, messageID, trimmed, chatPlanModeFor(workspace.id)),
    );
    state.editingMessageIds.delete(messageID);
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
  if (event.key !== "Enter" || event.shiftKey || event.isComposing) {
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
  state.chatSessions.set(workspace.id, await LoadChatSession(workspace.id));
}

export function applyChatStreamEvent(event: ChatStreamEvent) {
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
    if (event.type === "complete") {
      playNotificationSound();
    }
  }
  if (event.type === "retrying") {
    message.status = "retrying";
    message.error = "";
  }

  state.chatSessions.set(event.workspaceId, session);
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

export function patchChatMessage(message: services.ChatMessage, patchDebug = true) {
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
}

export function patchDebugSections(stack: HTMLElement, message: services.ChatMessage) {
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

export function patchChatPanel() {
  const workspace = activeWorkspace();
  const panel = appRoot.querySelector<HTMLElement>("[data-chat-panel]");
  if (!workspace || !panel) {
    getAppCallbacks().render();
    return;
  }
  const next = document.createElement("template");
  next.innerHTML = renderChatPanel(workspace, state.expandedChatWorkspaces.has(workspace.id)).trim();
  const replacement = next.content.firstElementChild as HTMLElement;
  panel.replaceWith(replacement);
  getAppCallbacks().bindActionEvents(replacement);
  getAppCallbacks().bindChatEvents(replacement);
  void linkifyAssistantFilePaths(replacement);
}

export function patchChatControls() {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  const session = chatSessionFor(workspace.id);
  const draft = state.chatDrafts.get(workspace.id) ?? "";
  const imageDrafts = chatImageDraftsFor(workspace.id);
  const input = appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]");
  const send = appRoot.querySelector<HTMLButtonElement>(".send-button");
  const stop = appRoot.querySelector<HTMLButtonElement>(".stop-button");
  const execute = appRoot.querySelector<HTMLButtonElement>(".execute-button");
  const clear = appRoot.querySelector<HTMLButtonElement>('[data-action="clear-chat"]');
  const planToggle = appRoot.querySelector<HTMLInputElement>("[data-chat-plan-toggle]");
  const title = appRoot.querySelector<HTMLElement>("#chat-title");
  const panel = appRoot.querySelector<HTMLElement>("[data-chat-panel]");
  const executing = state.executingPlans.has(workspace.id);
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

export function scrollChatToBottom() {
  const log = appRoot.querySelector<HTMLElement>("[data-chat-log]");
  if (log) {
    log.scrollTop = log.scrollHeight;
  }
}
