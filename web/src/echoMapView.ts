// echoMapView.ts — Echo Map dashboard: live activity for every chat across
// every workspace, grouped into pending questions, in progress, and completed.
//
// Replaces the former chat map popup. The primary nav map icon navigates to
// #/map; cards deep-link back into the exact chat (scrolling to the end) via
// the shared chatTargetRouteHash consumers.
import "./echoMap.css";

import { get } from "../js/api.js";
import * as ws from "../js/ws.js";
import { toast } from "./code/ui";
import { patchMarkdownElement } from "./markdown";
import { chatTargetRouteHash, codeRouteHash, orchestrationRouteHash, type ChatTarget } from "./navigation";
import { renderMobilePrimaryNav, renderPrimaryNav } from "./primaryNav";

// Refresh cadence: global broadcasts (chat_completed, plan_questions_awaiting)
// trigger an immediate debounced refresh; the poll covers streaming deltas and
// state transitions that have no global broadcast.
const REFRESH_DEBOUNCE_MS = 400;
const POLL_INTERVAL_MS = 3000;

const ASK_USER_QUESTIONS_TOOL = "ask_user_questions";

type EchoMapSurface = "chat" | "code";
export type EchoMapState = "pending_questions" | "in_progress" | "completed";

type EchoMapPlanQuestion = { id?: string; question?: string; options?: string[] };

type EchoMapToolActivity = {
  callId?: string;
  callOrder?: number;
  name?: string;
  arguments?: string;
  status?: string;
  success?: boolean;
  result?: string;
  resultTruncated?: boolean;
  planQuestions?: { questionSetId?: string; questions?: EchoMapPlanQuestion[] };
  agentId?: string;
  agentName?: string;
};

type EchoMapAssistantTurn = {
  number?: number;
  content?: string;
  reasoning?: string;
  reasoningTruncated?: boolean;
  hasToolCalls?: boolean;
  tools?: EchoMapToolActivity[];
};

type EchoMapResearchReasoning = {
  agentId?: string;
  agentName?: string;
  reasoning?: string;
  truncated?: boolean;
};

type EchoMapTurn = {
  id?: string;
  userContent?: string;
  userDeleted?: boolean;
  assistantDeleted?: boolean;
  status?: string;
  assistantTurns?: EchoMapAssistantTurn[];
  researchReasoning?: EchoMapResearchReasoning[];
  researchTools?: EchoMapToolActivity[];
};

export type EchoMapChat = {
  workspaceId: string;
  workspaceName: string;
  chatId: string;
  surface: EchoMapSurface;
  title: string;
  state: EchoMapState;
  pendingQuestions: number;
  lastActivityAt: string;
  turns: EchoMapTurn[];
};

type EchoMapWarning = { workspaceId: string; workspaceName: string; message: string };

type EchoMapResponse = { chats: EchoMapChat[]; warnings: EchoMapWarning[] };

const SECTIONS: Array<{ state: EchoMapState; label: string; empty: string }> = [
  { state: "pending_questions", label: "Pending Questions", empty: "No chats are waiting for answers." },
  { state: "in_progress", label: "In Progress", empty: "No chats are currently running." },
  { state: "completed", label: "Completed", empty: "No completed chats yet." },
];

export type EchoMapViewOptions = {
  // When set, only chats passing the predicate are rendered.
  workspaceFilter?: (chat: EchoMapChat) => boolean;
  // Embed mode renders only the card sections into the root element, without
  // the primary nav shell — used by the Orchestration page.
  embed?: boolean;
};

function normalizedResponse(value: unknown): EchoMapResponse {
  const response = value && typeof value === "object" ? value as Record<string, unknown> : {};
  const chats = Array.isArray(response.chats) ? response.chats.flatMap((candidate): EchoMapChat[] => {
    if (!candidate || typeof candidate !== "object") return [];
    const entry = candidate as Record<string, unknown>;
    const surface = entry.surface === "code" ? "code" : entry.surface === "chat" ? "chat" : null;
    const state = entry.state === "pending_questions" || entry.state === "in_progress" || entry.state === "completed"
      ? entry.state
      : "completed";
    if (!surface || typeof entry.workspaceId !== "string" || typeof entry.chatId !== "string") return [];
    return [{
      workspaceId: entry.workspaceId,
      workspaceName: typeof entry.workspaceName === "string" ? entry.workspaceName : "Unknown workspace",
      chatId: entry.chatId,
      surface,
      title: typeof entry.title === "string" && entry.title ? entry.title : surface === "code" ? "Code Chat" : "Chat",
      state,
      pendingQuestions: typeof entry.pendingQuestions === "number" ? entry.pendingQuestions : 0,
      lastActivityAt: typeof entry.lastActivityAt === "string" ? entry.lastActivityAt : "",
      turns: Array.isArray(entry.turns) ? entry.turns as EchoMapTurn[] : [],
    }];
  }) : [];
  const warnings = Array.isArray(response.warnings) ? response.warnings.flatMap((candidate): EchoMapWarning[] => {
    if (!candidate || typeof candidate !== "object") return [];
    const warning = candidate as Record<string, unknown>;
    if (typeof warning.workspaceId !== "string") return [];
    return [{
      workspaceId: warning.workspaceId,
      workspaceName: typeof warning.workspaceName === "string" ? warning.workspaceName : "Unknown workspace",
      message: typeof warning.message === "string" ? warning.message : "Chat history is unavailable.",
    }];
  }) : [];
  return { chats, warnings };
}

function formatStructured(value: string | undefined): string {
  const text = String(value || "").trim();
  if (!text) return "";
  if (text.startsWith("{") || text.startsWith("[")) {
    try {
      return JSON.stringify(JSON.parse(text), null, 2);
    } catch {
      return text;
    }
  }
  return text;
}

function toolStatusLabel(tool: EchoMapToolActivity): string {
  if (tool.status === "awaiting_input") return "Waiting for your answers…";
  if (tool.status === "running") return "Running…";
  if (tool.success === false) return "Failed";
  return "Completed";
}

export class EchoMapView {
  private root: HTMLElement;
  private options: EchoMapViewOptions;
  private data: EchoMapResponse = { chats: [], warnings: [] };
  private abort = new AbortController();
  private pollTimer = 0;
  private refreshTimer = 0;
  private refreshing = false;
  private disposed = false;
  private wsUnsubscribers: Array<() => void> = [];

  constructor(root: HTMLElement, options: EchoMapViewOptions = {}) {
    this.root = root;
    this.options = options;
  }

  async start(): Promise<void> {
    if (!this.options.embed) {
      this.renderShell();
      this.bindNavigation();
    }
    this.wsUnsubscribers.push(
      ws.on("chat_completed", () => this.scheduleRefresh()),
      ws.on("plan_questions_awaiting", () => this.scheduleRefresh()),
      ws.on("command_error", (message) => this.reportCommandError(message)),
    );
    this.pollTimer = window.setInterval(() => {
      if (!document.hidden && !this.refreshing) void this.load();
    }, POLL_INTERVAL_MS);
    await this.load();
  }

  dispose(): void {
    this.disposed = true;
    this.abort.abort();
    window.clearInterval(this.pollTimer);
    window.clearTimeout(this.refreshTimer);
    this.wsUnsubscribers.forEach((unsubscribe) => unsubscribe());
    this.wsUnsubscribers = [];
  }

  private renderShell(): void {
    this.root.innerHTML = `
      <div class="echo-map-shell">
        ${renderPrimaryNav({ active: "map", workspaceName: "Echo" })}
        <main class="echo-map-main">
          <header class="echo-map-header">
            <div>
              <span class="echo-map-eyebrow">WORKSPACE ACTIVITY</span>
              <h1>Echo Map</h1>
            </div>
            <p class="echo-map-subtitle">Every chat across your workspaces, streaming live.</p>
          </header>
          <div class="echo-map-content" data-echo-map-content>
            <p class="echo-map-loading" role="status">Loading chats…</p>
          </div>
        </main>
        ${renderMobilePrimaryNav({ active: "map" })}
      </div>
    `;
  }

  private bindNavigation(): void {
    const signal = this.abort.signal;
    this.root.querySelectorAll("[data-nav=chat]").forEach((item) => item.addEventListener("click", () => { location.hash = "#/home"; }, { signal }));
    this.root.querySelectorAll("[data-nav=code]").forEach((item) => item.addEventListener("click", () => { location.hash = "#/code"; }, { signal }));
    this.root.querySelectorAll("[data-nav=search]").forEach((item) => item.addEventListener("click", () => { location.hash = codeRouteHash("search"); }, { signal }));
    this.root.querySelectorAll("[data-nav=git]").forEach((item) => item.addEventListener("click", () => { location.hash = codeRouteHash("git"); }, { signal }));
    this.root.querySelectorAll("[data-nav=debug]").forEach((item) => item.addEventListener("click", () => { location.hash = codeRouteHash("debug"); }, { signal }));
    this.root.querySelectorAll("[data-nav=settings]").forEach((item) => item.addEventListener("click", () => { location.hash = "#/settings"; }, { signal }));
    this.root.querySelectorAll("[data-nav=sandbox]").forEach((item) => item.addEventListener("click", () => { location.hash = "#/sandbox"; }, { signal }));
    this.root.querySelectorAll("[data-nav=orchestration]").forEach((item) => item.addEventListener("click", () => { location.hash = orchestrationRouteHash(); }, { signal }));
  }

  private scheduleRefresh(): void {
    if (this.disposed) return;
    window.clearTimeout(this.refreshTimer);
    this.refreshTimer = window.setTimeout(() => {
      if (!this.disposed) void this.load();
    }, REFRESH_DEBOUNCE_MS);
  }

  private async load(): Promise<void> {
    if (this.disposed) return;
    this.refreshing = true;
    try {
      const response = normalizedResponse(await get("/api/echo-map", { signal: this.abort.signal }));
      if (this.disposed) return;
      this.data = response;
      this.render();
    } catch (error) {
      if (this.disposed || (error instanceof DOMException && error.name === "AbortError")) return;
      this.renderError(error instanceof Error ? error.message : String(error));
    } finally {
      this.refreshing = false;
    }
  }

  // contentContainer resolves where the card sections render: the dedicated
  // content element in shell mode, or the root itself in embed mode.
  private contentContainer(): HTMLElement | null {
    if (this.options.embed) return this.root;
    return this.root.querySelector<HTMLElement>("[data-echo-map-content]");
  }

  private renderError(message: string): void {
    const container = this.contentContainer();
    if (!container) return;
    container.replaceChildren();
    const error = document.createElement("div");
    error.className = "echo-map-error";
    error.setAttribute("role", "alert");
    const text = document.createElement("p");
    text.textContent = message || "Unable to load chats.";
    const retry = document.createElement("button");
    retry.type = "button";
    retry.className = "secondary-button compact-button";
    retry.textContent = "Retry";
    retry.addEventListener("click", () => void this.load());
    error.append(text, retry);
    container.append(error);
  }

  private render(): void {
    const container = this.contentContainer();
    if (!container) return;
    const scrollTops = this.captureTurnScrollPositions();
    container.replaceChildren();

    if (this.data.warnings.length) {
      const names = this.data.warnings.map((warning) => warning.workspaceName).filter(Boolean);
      const warning = document.createElement("p");
      warning.className = "echo-map-warning";
      warning.setAttribute("role", "status");
      warning.textContent = names.length
        ? `Some chat history could not be loaded: ${names.join(", ")}.`
        : "Some chat history could not be loaded.";
      container.append(warning);
    }

    const groups = new Map<EchoMapState, EchoMapChat[]>(SECTIONS.map((section) => [section.state, []]));
    for (const chat of this.data.chats) {
      if (this.options.workspaceFilter && !this.options.workspaceFilter(chat)) continue;
      groups.get(chat.state)?.push(chat);
    }

    for (const section of SECTIONS) {
      const chats = groups.get(section.state) || [];
      const sectionEl = document.createElement("section");
      sectionEl.className = "echo-map-section";
      sectionEl.dataset.echoMapSection = section.state;
      const header = document.createElement("header");
      header.className = "echo-map-section-header";
      const heading = document.createElement("h2");
      heading.textContent = section.label;
      const count = document.createElement("span");
      count.className = "echo-map-section-count";
      count.textContent = String(chats.length);
      header.append(heading, count);
      sectionEl.append(header);
      if (!chats.length) {
        const empty = document.createElement("p");
        empty.className = "echo-map-section-empty";
        empty.textContent = section.empty;
        sectionEl.append(empty);
      } else {
        const grid = document.createElement("div");
        grid.className = "echo-map-grid";
        for (const chat of chats) grid.append(this.renderChatCard(chat));
        sectionEl.append(grid);
      }
      container.append(sectionEl);
    }

    this.restoreTurnScrollPositions(scrollTops);
  }

  private captureTurnScrollPositions(): Map<string, number> {
    const positions = new Map<string, number>();
    this.root.querySelectorAll<HTMLElement>("[data-echo-map-card]").forEach((card) => {
      const turns = card.querySelector<HTMLElement>("[data-echo-map-turns]");
      if (turns) positions.set(card.dataset.echoMapCard || "", turns.scrollTop);
    });
    return positions;
  }

  private restoreTurnScrollPositions(positions: Map<string, number>): void {
    this.root.querySelectorAll<HTMLElement>("[data-echo-map-card]").forEach((card) => {
      const saved = positions.get(card.dataset.echoMapCard || "");
      const turns = card.querySelector<HTMLElement>("[data-echo-map-turns]");
      if (saved !== undefined && turns) turns.scrollTop = saved;
    });
  }

  private chatKey(chat: EchoMapChat): string {
    return `${chat.workspaceId}:${chat.surface}:${chat.chatId}`;
  }

  private renderChatCard(chat: EchoMapChat): HTMLElement {
    const card = document.createElement("article");
    card.className = `echo-map-card echo-map-card-${chat.state}`;
    card.dataset.echoMapCard = this.chatKey(chat);

    const workspace = document.createElement("div");
    workspace.className = "echo-map-card-workspace";
    workspace.append(this.renderWorkspaceIcon(chat));
    const workspaceName = document.createElement("span");
    workspaceName.className = "echo-map-card-workspace-name";
    workspaceName.textContent = chat.workspaceName || "Unknown workspace";
    workspace.append(workspaceName);
    if (chat.surface === "code") {
      const surface = document.createElement("span");
      surface.className = "echo-map-card-surface";
      surface.textContent = "(code)";
      workspace.append(surface);
    } else {
      // Code chats are persistent per workspace and cannot be closed; every
      // other chat gets the same close action as its tab, with confirmation.
      workspace.append(this.renderCloseButton(chat));
    }

    const title = document.createElement("button");
    title.type = "button";
    title.className = "echo-map-card-title";
    title.title = chat.title;
    title.textContent = chat.title;
    title.addEventListener("click", () => this.openChat(chat));

    const turns = document.createElement("div");
    turns.className = "echo-map-card-turns";
    turns.dataset.echoMapTurns = "";
    this.renderTurns(turns, chat);

    card.append(workspace, title, turns);

    if (chat.state === "pending_questions") {
      const pending = document.createElement("button");
      pending.type = "button";
      pending.className = "echo-map-card-pending";
      pending.textContent = chat.pendingQuestions > 1
        ? `Pending Questions (${chat.pendingQuestions})`
        : "Pending Questions";
      pending.addEventListener("click", () => this.openChat(chat));
      card.append(pending);
    }
    return card;
  }

  private renderWorkspaceIcon(chat: EchoMapChat): HTMLElement {
    const icon = document.createElement("span");
    icon.className = "echo-map-workspace-icon";
    const image = document.createElement("img");
    image.src = `/api/workspaces/${encodeURIComponent(chat.workspaceId)}/icon`;
    image.alt = "";
    image.loading = "lazy";
    image.addEventListener("error", () => {
      icon.textContent = (chat.workspaceName || "?").charAt(0).toUpperCase();
    });
    icon.append(image);
    return icon;
  }

  private openChat(chat: EchoMapChat): void {
    const target: ChatTarget = { workspaceId: chat.workspaceId, chatId: chat.chatId, surface: chat.surface };
    window.location.hash = chatTargetRouteHash(target);
  }

  private renderCloseButton(chat: EchoMapChat): HTMLElement {
    const close = document.createElement("button");
    close.type = "button";
    close.className = "echo-map-card-close";
    close.title = "Close chat";
    close.setAttribute("aria-label", `Close chat: ${chat.title}`);
    close.innerHTML = '<span class="codicon codicon-close" aria-hidden="true"></span>';
    close.addEventListener("click", (event) => {
      event.stopPropagation();
      this.confirmAndClose(chat);
    });
    return close;
  }

  // confirmAndClose mirrors the chat surface's close confirmation: running
  // chats are stopped first, and the close itself always asks here because the
  // dashboard has no undo.
  private confirmAndClose(chat: EchoMapChat): void {
    const busy = chat.state === "in_progress";
    const dialog = document.createElement("dialog");
    dialog.className = "settings-confirm-dialog chat-close-dialog";
    dialog.innerHTML = `
      <form method="dialog">
        <h2>${busy ? "Chat is still running" : "Close this chat?"}</h2>
        <p>${busy ? "Stop the response and close this chat?" : "The conversation will be removed from this workspace. This cannot be undone."}</p>
        <div class="settings-confirm-actions">
          <button class="secondary-button" type="button" data-chat-close-choice="cancel">Cancel</button>
          <button class="secondary-button danger-button" type="button" data-chat-close-choice="confirm">${busy ? "Stop and close" : "Close chat"}</button>
        </div>
      </form>
    `;
    let finished = false;
    const finish = (confirmed: boolean) => {
      if (finished) return;
      finished = true;
      dialog.close?.();
      dialog.remove();
      if (confirmed) this.closeChat(chat, busy);
    };
    dialog.querySelector("[data-chat-close-choice='cancel']")?.addEventListener("click", () => finish(false));
    dialog.querySelector("[data-chat-close-choice='confirm']")?.addEventListener("click", () => finish(true));
    dialog.addEventListener("cancel", (event) => {
      event.preventDefault();
      finish(false);
    }, { once: true });
    document.body.append(dialog);
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
  }

  private closeChat(chat: EchoMapChat, stopIfBusy: boolean): void {
    const sent = ws.send({
      type: "chat_tab_close",
      workspaceId: chat.workspaceId,
      chatId: chat.chatId,
      ...(stopIfBusy ? { stopIfBusy: true } : {}),
    });
    if (sent) {
      this.scheduleRefresh();
    } else {
      toast("Could not close the chat — the connection is offline.", { sticky: true });
    }
  }

  // reportCommandError surfaces close rejections (e.g. a goal protecting the
  // transcript) that arrive after the user already confirmed the close.
  private reportCommandError(message: unknown): void {
    const error = message && typeof message === "object" ? message as Record<string, unknown> : {};
    const chatId = typeof error.chatId === "string" ? error.chatId : "";
    const workspaceId = typeof error.workspaceId === "string" ? error.workspaceId : "";
    if (!chatId || !this.data.chats.some((chat) => chat.chatId === chatId && chat.workspaceId === workspaceId)) return;
    const text = String(error.error || error.code || "").trim();
    if (text) toast(text, { sticky: true });
  }

  // renderTurns mirrors the main chat surface's stored-turn rules: user and
  // assistant content render as markdown, while tool calls and thinking render
  // as collapsed disclosures inside the assistant timeline.
  private renderTurns(host: HTMLElement, chat: EchoMapChat): void {
    host.replaceChildren();
    const turns = chat.turns || [];
    if (!turns.length) {
      const empty = document.createElement("p");
      empty.className = "echo-map-card-empty";
      empty.textContent = "No turns yet.";
      host.append(empty);
      return;
    }
    for (const turn of turns) {
      if (!turn.userDeleted && (turn.userContent || "").trim()) host.append(this.createUserMessage(turn));
      if (!turn.assistantDeleted) host.append(this.createAssistantMessage(chat, turn));
    }
  }

  private createUserMessage(turn: EchoMapTurn): HTMLElement {
    const el = document.createElement("div");
    el.className = "chat-message chat-message-user";
    el.dataset.role = "user";
    const body = document.createElement("div");
    body.className = "chat-message-body";
    const content = document.createElement("div");
    content.className = "chat-message-content markdown-body";
    patchMarkdownElement(content, turn.userContent || "");
    body.append(content);
    el.append(body);
    return el;
  }

  private createAssistantMessage(chat: EchoMapChat, turn: EchoMapTurn): HTMLElement {
    const el = document.createElement("div");
    el.className = "chat-message chat-message-assistant";
    el.dataset.role = "assistant";
    const body = document.createElement("div");
    body.className = "chat-message-body";
    const timeline = document.createElement("div");
    timeline.className = "chat-timeline";
    timeline.hidden = true;
    body.append(timeline);

    const contentParts: string[] = [];
    for (const assistant of turn.assistantTurns || []) {
      this.appendReasoningItem(timeline, chat, turn, assistant);
      if ((assistant.content || "").trim()) contentParts.push(assistant.content || "");
      for (const tool of assistant.tools || []) this.appendToolItem(timeline, chat, turn, tool, false);
    }
    for (const reasoning of turn.researchReasoning || []) {
      this.appendResearchReasoningItem(timeline, chat, turn, reasoning);
    }
    for (const tool of turn.researchTools || []) this.appendToolItem(timeline, chat, turn, tool, true);

    timeline.hidden = timeline.children.length === 0;

    const content = document.createElement("div");
    content.className = "chat-message-content chat-final-content markdown-body";
    patchMarkdownElement(content, contentParts.join("\n\n"));
    content.hidden = contentParts.length === 0;
    body.append(content);
    el.append(body);
    return el;
  }

  private appendReasoningItem(
    timeline: HTMLElement,
    chat: EchoMapChat,
    turn: EchoMapTurn,
    assistant: EchoMapAssistantTurn,
  ): void {
    const reasoning = (assistant.reasoning || "").trim();
    if (!reasoning && !assistant.reasoningTruncated) return;
    const details = document.createElement("details");
    details.className = "chat-activity-item chat-reasoning-item";
    const summary = document.createElement("summary");
    const label = document.createElement("span");
    label.className = "chat-activity-name";
    label.textContent = "Thinking";
    summary.append(label);
    details.append(summary);
    const body = document.createElement("div");
    body.className = "chat-activity-body chat-reasoning-content";
    if (assistant.reasoningTruncated && !reasoning) {
      details.classList.add("is-reasoning-deferred");
      body.textContent = "Expand to load the full thinking text.";
      details.addEventListener("toggle", () => {
        if (details.open) void this.fetchReasoning(chat, turn, assistant.number, body, details);
      });
    } else {
      body.textContent = reasoning;
    }
    details.append(body);
    timeline.append(details);
  }

  private appendResearchReasoningItem(
    timeline: HTMLElement,
    chat: EchoMapChat,
    turn: EchoMapTurn,
    reasoning: EchoMapResearchReasoning,
  ): void {
    const text = (reasoning.reasoning || "").trim();
    if (!text && !reasoning.truncated) return;
    const details = document.createElement("details");
    details.className = "chat-activity-item chat-reasoning-item chat-research-reasoning";
    const summary = document.createElement("summary");
    const label = document.createElement("span");
    label.className = "chat-activity-name";
    label.textContent = `${reasoning.agentName || "Research agent"} · Thinking`;
    summary.append(label);
    details.append(summary);
    const body = document.createElement("div");
    body.className = "chat-activity-body chat-reasoning-content";
    body.textContent = text || "Expand to load the full thinking text.";
    if (!text && reasoning.truncated && reasoning.agentId && turn.id) {
      details.classList.add("is-reasoning-deferred");
      details.addEventListener("toggle", () => {
        if (!details.open) return;
        void this.fetchAgentReasoning(chat, turn, reasoning.agentId!, body, details);
      });
    }
    details.append(body);
    timeline.append(details);
  }

  private appendToolItem(
    timeline: HTMLElement,
    chat: EchoMapChat,
    turn: EchoMapTurn,
    tool: EchoMapToolActivity,
    research: boolean,
  ): void {
    const toolName = tool.name || "tool";
    const questions = tool.planQuestions && Array.isArray(tool.planQuestions.questions)
      ? tool.planQuestions.questions.filter((question) => (question.question || "").trim())
      : [];
    const isQuestionSet = toolName === ASK_USER_QUESTIONS_TOOL && questions.length > 0;

    const details = document.createElement("details");
    details.className = "chat-activity-item chat-tool-item";
    if (research) details.classList.add("is-research-tool");
    if (isQuestionSet) details.classList.add("chat-plan-question-item", "is-awaiting-input");
    if (tool.status === "running" || tool.status === "awaiting_input") details.classList.add("is-running");
    else if (tool.success === true) details.classList.add("is-success");
    else if (tool.success === false) details.classList.add("is-error");
    if (tool.callId) details.dataset.callId = tool.callId;

    const summary = document.createElement("summary");
    const name = document.createElement("span");
    name.className = "chat-activity-name";
    name.textContent = tool.agentName ? `${tool.agentName} · ${toolName}` : toolName;
    const status = document.createElement("span");
    status.className = "chat-activity-status";
    status.textContent = isQuestionSet ? "Waiting for your answers…" : toolStatusLabel(tool);
    summary.append(name, status);
    details.append(summary);

    const body = document.createElement("div");
    body.className = "chat-activity-body";
    if (isQuestionSet) {
      const list = document.createElement("ul");
      list.className = "echo-map-question-list";
      for (const question of questions) {
        const item = document.createElement("li");
        item.textContent = question.question || "";
        list.append(item);
      }
      body.append(list);
    } else {
      body.append(this.createToolSection("Arguments", formatStructured(tool.arguments) || "No arguments").section);
      const result = this.createToolSection("Result", tool.result || "Waiting for result…");
      body.append(result.section);
      if (tool.resultTruncated && tool.callId) {
        const marker = document.createElement("div");
        marker.className = "chat-tool-result-more";
        marker.textContent = "Result truncated — expand to load the full output.";
        result.content.after(marker);
        details.addEventListener("toggle", () => {
          if (details.open) void this.fetchToolResult(chat, tool.callId!, result.content, details);
        });
      }
    }
    details.append(body);
    timeline.append(details);
  }

  private createToolSection(labelText: string, value: string): { section: HTMLElement; content: HTMLElement } {
    const section = document.createElement("section");
    section.className = "chat-tool-section";
    const label = document.createElement("div");
    label.className = "chat-tool-label";
    label.textContent = labelText;
    const content = document.createElement("pre");
    content.className = "chat-tool-value";
    content.textContent = value;
    section.append(label, content);
    return { section, content };
  }

  private async fetchReasoning(
    chat: EchoMapChat,
    turn: EchoMapTurn,
    assistantNumber: number | undefined,
    body: HTMLElement,
    details: HTMLElement,
  ): Promise<void> {
    if (!turn.id || !Number.isInteger(assistantNumber)) {
      details.classList.remove("is-reasoning-deferred");
      return;
    }
    try {
      const data = await get(`/api/workspaces/${encodeURIComponent(chat.workspaceId)}/chats/${encodeURIComponent(chat.chatId)}/reasoning/${encodeURIComponent(turn.id)}?assistant=${encodeURIComponent(assistantNumber!)}`);
      body.textContent = String((data as { reasoning?: string })?.reasoning || "");
      details.classList.remove("is-reasoning-deferred");
    } catch {
      body.textContent = "Could not load the thinking text — expand again to retry.";
    }
  }

  private async fetchAgentReasoning(
    chat: EchoMapChat,
    turn: EchoMapTurn,
    agentId: string,
    body: HTMLElement,
    details: HTMLElement,
  ): Promise<void> {
    if (!turn.id) return;
    try {
      const data = await get(`/api/workspaces/${encodeURIComponent(chat.workspaceId)}/chats/${encodeURIComponent(chat.chatId)}/reasoning/${encodeURIComponent(turn.id)}?agentId=${encodeURIComponent(agentId)}`);
      body.textContent = String((data as { reasoning?: string })?.reasoning || "");
      details.classList.remove("is-reasoning-deferred");
    } catch {
      body.textContent = "Could not load the thinking text — expand again to retry.";
    }
  }

  private async fetchToolResult(
    chat: EchoMapChat,
    callId: string,
    content: HTMLElement,
    details: HTMLElement,
  ): Promise<void> {
    try {
      const data = await get(`/api/workspaces/${encodeURIComponent(chat.workspaceId)}/chats/${encodeURIComponent(chat.chatId)}/tool-results/${encodeURIComponent(callId)}`);
      content.textContent = formatStructured(String((data as { content?: string })?.content || "")) || "No result";
      details.querySelector(".chat-tool-result-more")?.remove();
    } catch {
      content.textContent = "Could not load the full result — expand again to retry.";
    }
  }
}

let currentView: EchoMapView | null = null;

export function mount(el: HTMLElement): void {
  currentView?.dispose();
  currentView = new EchoMapView(el);
  void currentView.start();
}

export function unmount(): void {
  currentView?.dispose();
  currentView = null;
}
