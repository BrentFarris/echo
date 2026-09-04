import { api } from "../js/api.js";
import { isCoarsePointer } from "./device";
import {
  canClearChat, clearChat, clearGoal, closeWorkspaceSession, editGoal, isStreaming,
  onChatWorkspaceChange, onStreamingChange, openWorkspaceSession, pauseGoal, resumeGoal,
  sendMessage, startGoal, steerGoal, stopStream,
} from "../js/chat.js";
import {
  activeMentionMatch, composerText, insertReferenceChip, restoreComposer, snapshotComposer,
  type ComposerSegment, type ChatReference,
} from "./chatMentions";
import { getRoots, searchEntries } from "./code/editorApi";
import type { FileRef, SearchResult, WorkspaceRoot } from "./code/types";
import { prepareCompletionNotificationPermission } from "./completionNotifications";
import { preparePlanQuestionNotificationPermission } from "./planQuestionNotifications";

export type EditorContextDiff = {
  repositoryId?: string;
  repository?: string;
  scope?: string;
  reviewRef?: string;
  oldPath?: string;
  path?: string;
};

export type EditorContextRange = {
  side?: "original" | "modified";
  startLine: number;
  startColumn: number;
  endLine: number;
  endColumn: number;
};

export type EditorContextSelection = EditorContextRange & {
  text: string;
};

export type EditorContextTab = {
  kind: "file" | "diff" | "untitled";
  title: string;
  active?: boolean;
  dirty?: boolean;
  ref?: FileRef;
  reference?: string;
  content?: string;
  diff?: EditorContextDiff;
  selections?: EditorContextSelection[];
};

export type EditorContextPayload = { tabs: EditorContextTab[]; truncated?: boolean };

export type HistoricalChatResource = {
  kind: "file" | "directory" | "diff" | "untitled";
  label: string;
  referencePath?: string;
  ref?: FileRef;
  diff?: EditorContextDiff;
  selection?: EditorContextRange;
};

type PromptReference = Omit<ChatReference, "workspaceId">;

export type ChatSurfaceOptions = {
  workspaceId: string;
  surface?: "chat" | "code";
  title?: string;
  placeholder?: string;
  onClose?: () => void;
  beforeSend?: () => Promise<EditorContextPayload | false | null>;
  onActivateReference?: (reference: ChatReference) => void | Promise<void>;
  onActivateHistoricalResource?: (resource: HistoricalChatResource) => void | Promise<void>;
  onStreamingChange?: (streaming: boolean) => void;
  expectedChatId?: string;
  onExpectedChatResolved?: (found: boolean) => void;
};

export type MountedChatSurface = {
  dispose(): void;
  focus(): void;
  setContextNotice(message: string | null): void;
};

type Endpoint = { id: string; name?: string; model?: string };
type AgentMode = { id: string; name: string };
type GoalState = {
  id: string;
  objective: string;
  status: "active" | "paused" | "blocked" | "completed" | "cleared";
  model?: string;
  activeSeconds?: number;
  stepCount?: number;
  pendingSteering?: number;
};
type MentionState = {
  workspaceId: string;
  triggerStart: number;
  caret: number;
  query: string;
  results: SearchResult[];
  roots: WorkspaceRoot[];
  loading: boolean;
  error: string;
  selectedIndex: number;
  sequence: number;
};

type Draft = { segments: ComposerSegment[]; model: string; mode: string };

const drafts = new Map<string, Draft>();

function draftKey(workspaceId: string, surface: string): string {
  return `${workspaceId}\0${surface}`;
}

function createReferenceChip(reference: ChatReference): HTMLElement {
  const chip = document.createElement("span");
  chip.className = "chat-mention-chip";
  chip.contentEditable = "false";
  chip.dataset.chatFileMention = "";
  chip.dataset.workspaceId = reference.workspaceId;
  chip.dataset.rootId = reference.ref.rootId;
  chip.dataset.workspacePath = reference.ref.path;
  chip.dataset.workspaceKind = reference.kind;
  chip.dataset.referencePath = reference.referencePath;
  chip.dataset.referenceLabel = reference.label;
  chip.title = reference.referencePath;
  chip.tabIndex = 0;
  chip.setAttribute("role", "link");
  chip.setAttribute("aria-label", `${reference.kind === "directory" ? "Open folder" : "Open file"} ${reference.referencePath}`);
  const icon = document.createElement("span");
  icon.className = `chat-mention-chip-icon codicon codicon-${reference.kind === "directory" ? "folder" : "file-code"}`;
  const label = document.createElement("span");
  label.className = "chat-mention-chip-label";
  label.textContent = reference.label;
  chip.append(icon, label);
  return chip;
}

function referenceFromChip(chip: HTMLElement): ChatReference | null {
  const rootId = chip.dataset.rootId || "";
  if (!rootId) return null;
  return {
    workspaceId: chip.dataset.workspaceId || "",
    ref: { rootId, path: chip.dataset.workspacePath || "" },
    kind: chip.dataset.workspaceKind === "directory" ? "directory" : "file",
    referencePath: chip.dataset.referencePath || "",
    label: chip.dataset.referenceLabel || chip.textContent || "",
  };
}

export function mountChatSurface(host: HTMLElement, options: ChatSurfaceOptions): MountedChatSurface {
  const surface = options.surface === "code" ? "code" : "chat";
  const title = options.title || (surface === "code" ? "CODE CHAT" : "CHAT");
  host.innerHTML = `
    <section class="chat-panel code-chat-surface" aria-label="${title}">
      <header class="code-chat-header">
        <strong>${title}</strong>
        <div>
          <button type="button" title="New chat" aria-label="New chat" data-code-chat-new><span class="codicon codicon-add"></span></button>
          ${options.onClose ? '<button type="button" title="Close chat" aria-label="Close chat" data-code-chat-close><span class="codicon codicon-close"></span></button>' : ""}
        </div>
      </header>
      <div class="chat-log" role="region" tabindex="0" aria-label="Conversation transcript" data-chat-log><div class="empty-state chat-empty">Loading conversation…</div></div>
      <div class="chat-goal-bar" data-goal-bar hidden>
        <div class="chat-goal-main">
          <span class="chat-goal-status" data-goal-status></span>
          <span class="chat-goal-objective" data-goal-objective></span>
          <span class="chat-goal-stats" data-goal-stats></span>
        </div>
        <div class="chat-goal-actions">
          <button type="button" data-goal-action="pause">Pause</button>
          <button type="button" data-goal-action="resume">Resume</button>
          <button type="button" data-goal-action="edit">Edit</button>
          <button type="button" data-goal-action="new">New goal</button>
          <button type="button" data-goal-action="clear">Clear</button>
        </div>
      </div>
      <form class="chat-composer" data-chat-form>
        <div class="code-chat-context-notice" data-chat-context-notice hidden></div>
        <div class="chat-composer-main" data-chat-input-wrap>
          <div class="chat-composer-editor" contenteditable="true" role="textbox" aria-multiline="true"
            aria-label="Message Echo about this code" aria-autocomplete="list" aria-expanded="false"
            spellcheck="true" data-chat-input data-placeholder="${options.placeholder || "Ask about the open files"}"></div>
        </div>
        <div class="chat-composer-toolbar code-chat-toolbar">
          <div class="chat-composer-toolbar-left">
            <label class="code-chat-select-label" title="Select model"><span class="codicon codicon-server-environment"></span><select aria-label="Model" data-code-chat-model><option value="">Model</option></select></label>
            <label class="code-chat-select-label" title="Agent mode"><span class="codicon codicon-tools"></span><select aria-label="Agent mode" data-code-chat-mode><option value="general">General</option></select></label>
          </div>
          <div class="chat-composer-toolbar-right"><button class="send-button" type="submit" title="Send" aria-label="Send message"><span class="codicon codicon-send"></span></button></div>
        </div>
      </form>
    </section>`;

  const log = host.querySelector<HTMLElement>("[data-chat-log]")!;
  const goalBar = host.querySelector<HTMLElement>("[data-goal-bar]")!;
  const goalStatus = host.querySelector<HTMLElement>("[data-goal-status]")!;
  const goalObjective = host.querySelector<HTMLElement>("[data-goal-objective]")!;
  const goalStats = host.querySelector<HTMLElement>("[data-goal-stats]")!;
  const form = host.querySelector<HTMLFormElement>("[data-chat-form]")!;
  const contextNotice = host.querySelector<HTMLElement>("[data-chat-context-notice]")!;
  const inputWrap = host.querySelector<HTMLElement>("[data-chat-input-wrap]")!;
  const input = host.querySelector<HTMLElement>("[data-chat-input]")!;
  const send = host.querySelector<HTMLButtonElement>(".send-button")!;
  const newChat = host.querySelector<HTMLButtonElement>("[data-code-chat-new]")!;
  const close = host.querySelector<HTMLButtonElement>("[data-code-chat-close]");
  const modelSelect = host.querySelector<HTMLSelectElement>("[data-code-chat-model]")!;
  const modeSelect = host.querySelector<HTMLSelectElement>("[data-code-chat-mode]")!;
  const abort = new AbortController();
  const { signal } = abort;
  let mention: MentionState | null = null;
  let mentionTimer = 0;
  let mentionSequence = 0;
  let submitting = false;
  let rootsPromise: Promise<WorkspaceRoot[]> | null = null;
  let currentGoal: GoalState | null = null;
  let goalStateReceivedAt = Date.now();
  let streaming = false;
  let startingNewGoal = false;

  const goalLocksComposer = () => currentGoal?.status === "active" || currentGoal?.status === "paused";
  const goalAcceptsSteering = () => Boolean(
    currentGoal && ["active", "paused", "blocked"].includes(currentGoal.status) && !startingNewGoal,
  );

  const updateNewChatAvailability = () => {
    newChat.disabled = goalLocksComposer() || !canClearChat(log);
  };

  const formatGoalDuration = (seconds: number) => {
    const total = Math.max(0, Number(seconds) || 0);
    if (total < 60) return `${total}s`;
    if (total < 3600) return `${Math.floor(total / 60)}m`;
    return `${Math.floor(total / 3600)}h ${Math.floor((total % 3600) / 60)}m`;
  };

  const renderGoalBar = () => {
    goalBar.hidden = !currentGoal;
    if (!currentGoal) {
      startingNewGoal = false;
      modelSelect.disabled = false;
      modeSelect.disabled = false;
      input.dataset.placeholder = options.placeholder || "Ask about the open files";
      updateNewChatAvailability();
      return;
    }
    const labels: Record<string, string> = {
      active: "Running", paused: "Paused", blocked: "Blocked", completed: "Complete", cleared: "Cleared",
    };
    goalStatus.textContent = labels[currentGoal.status] || currentGoal.status;
    goalStatus.dataset.status = currentGoal.status;
    goalObjective.textContent = currentGoal.objective;
    goalObjective.title = currentGoal.objective;
    const elapsed = (currentGoal.activeSeconds || 0)
      + (currentGoal.status === "active" ? Math.floor((Date.now() - goalStateReceivedAt) / 1000) : 0);
    const steps = currentGoal.stepCount || 0;
    const stats = [`${steps} step${steps === 1 ? "" : "s"}`, formatGoalDuration(elapsed)];
    if (currentGoal.pendingSteering) stats.push(`${currentGoal.pendingSteering} queued`);
    goalStats.textContent = stats.join(" · ");
    goalBar.querySelectorAll<HTMLButtonElement>("[data-goal-action]").forEach((button) => {
      const action = button.dataset.goalAction;
      button.hidden = !(
        (action === "pause" && currentGoal?.status === "active")
        || (action === "resume" && (currentGoal?.status === "paused" || currentGoal?.status === "blocked"))
        || (action === "edit" && Boolean(currentGoal && ["active", "paused", "blocked"].includes(currentGoal.status)))
        || (action === "new" && (currentGoal?.status === "blocked" || currentGoal?.status === "completed"))
        || action === "clear"
      );
      button.disabled = action === "resume" && streaming;
    });
    const locked = goalLocksComposer();
    modelSelect.disabled = locked;
    modeSelect.disabled = locked;
    if (locked) {
      modeSelect.value = "goal";
      if (currentGoal.model) modelSelect.value = currentGoal.model;
    }
    if (startingNewGoal) input.dataset.placeholder = "Describe the new goal and its completion criteria";
    else if (currentGoal.status === "active") input.dataset.placeholder = "Add guidance to this goal";
    else if (currentGoal.status === "paused") input.dataset.placeholder = "Queue guidance, then resume when ready";
    else if (currentGoal.status === "blocked" && modeSelect.value === "goal") input.dataset.placeholder = "Add guidance to resume this goal";
    else input.dataset.placeholder = options.placeholder || "Ask about the open files";
    updateNewChatAvailability();
  };

  const saveDraft = () => {
    drafts.set(draftKey(options.workspaceId, surface), {
      segments: snapshotComposer(input), model: modelSelect.value, mode: modeSelect.value || "general",
    });
  };

  const restoreDraft = () => {
    const draft = drafts.get(draftKey(options.workspaceId, surface));
    if (!draft) return;
    restoreComposer(input, draft.segments, createReferenceChip);
    if ([...modelSelect.options].some((option) => option.value === draft.model)) modelSelect.value = draft.model;
    if ([...modeSelect.options].some((option) => option.value === draft.mode)) modeSelect.value = draft.mode;
  };

  const roots = () => {
    rootsPromise ||= getRoots(options.workspaceId).catch((error) => {
      rootsPromise = null;
      throw error;
    });
    return rootsPromise;
  };

  const clearMention = () => {
    window.clearTimeout(mentionTimer);
    mentionTimer = 0;
    mention = null;
    inputWrap.querySelector("[data-chat-mention-picker]")?.remove();
    input.setAttribute("aria-expanded", "false");
    input.removeAttribute("aria-controls");
    input.removeAttribute("aria-activedescendant");
  };

  const referenceForEntry = (entry: SearchResult): ChatReference | null => {
    const root = mention?.roots.find((candidate) => candidate.id === entry.ref.rootId);
    if (!root) return null;
    return {
      workspaceId: options.workspaceId,
      ref: entry.ref,
      kind: entry.kind,
      referencePath: entry.referencePath || (entry.ref.path ? `${root.referenceLabel || root.label}/${entry.ref.path}` : root.referenceLabel || root.label),
      label: entry.name,
    };
  };

  const updateMentionSelection = () => {
    inputWrap.querySelectorAll<HTMLElement>("[data-chat-mention-option]").forEach((option, index) => {
      const selected = index === mention?.selectedIndex;
      option.classList.toggle("is-active", selected);
      option.setAttribute("aria-selected", String(selected));
    });
    if (mention?.results.length) input.setAttribute("aria-activedescendant", `code-chat-mention-${mention.selectedIndex}`);
    else input.removeAttribute("aria-activedescendant");
  };

  const selectMention = (index: number) => {
    if (!mention) return;
    const reference = mention.results[index] ? referenceForEntry(mention.results[index]) : null;
    if (!reference) return;
    insertReferenceChip(input, mention, createReferenceChip(reference));
    clearMention();
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.focus();
  };

  const renderMentionPicker = () => {
    inputWrap.querySelector("[data-chat-mention-picker]")?.remove();
    if (!mention) return;
    const picker = document.createElement("div");
    picker.className = "chat-mention-picker";
    picker.id = "code-chat-mention-list";
    picker.dataset.chatMentionPicker = "";
    picker.setAttribute("role", "listbox");
    picker.setAttribute("aria-label", "Workspace files and folders");
    if (mention.loading && !mention.results.length) {
      picker.innerHTML = '<div class="chat-mention-status" role="status"><span class="chat-mention-spinner"></span><span>Searching workspace…</span></div>';
    } else if (mention.error || !mention.results.length) {
      const status = document.createElement("div");
      status.className = `chat-mention-status${mention.error ? " is-error" : ""}`;
      status.textContent = mention.error || "No matching files or folders.";
      picker.append(status);
    } else {
      mention.results.forEach((entry, index) => {
        const reference = referenceForEntry(entry);
        if (!reference) return;
        const option = document.createElement("button");
        option.type = "button";
        option.className = `chat-mention-option${index === mention?.selectedIndex ? " is-active" : ""}`;
        option.id = `code-chat-mention-${index}`;
        option.dataset.chatMentionOption = "";
        option.setAttribute("role", "option");
        option.innerHTML = `<span class="chat-mention-icon codicon codicon-${entry.kind === "directory" ? "folder" : "file"}"></span><span class="chat-mention-name"><strong></strong><span></span></span><span class="chat-mention-kind">${entry.kind === "directory" ? "Folder" : "File"}</span>`;
        option.querySelector("strong")!.textContent = entry.name;
        option.querySelector<HTMLElement>(".chat-mention-name span")!.textContent = reference.referencePath;
        option.addEventListener("mousedown", (event) => event.preventDefault(), { signal });
        option.addEventListener("mousemove", () => { if (mention) { mention.selectedIndex = index; updateMentionSelection(); } }, { signal });
        option.addEventListener("click", () => selectMention(index), { signal });
        picker.append(option);
      });
    }
    inputWrap.append(picker);
    input.setAttribute("aria-expanded", "true");
    input.setAttribute("aria-controls", picker.id);
    updateMentionSelection();
  };

  const runMentionSearch = async (sequence: number) => {
    const state = mention;
    if (!state || state.sequence !== sequence) return;
    try {
      const [workspaceRoots, response] = await Promise.all([roots(), searchEntries(options.workspaceId, state.query, 12)]);
      if (!mention || mention.sequence !== sequence) return;
      mention.roots = workspaceRoots;
      mention.results = (response.items || []).slice(0, 8);
      mention.loading = false;
      mention.error = "";
      mention.selectedIndex = Math.min(mention.selectedIndex, Math.max(0, mention.results.length - 1));
      renderMentionPicker();
      if (response.indexing) mentionTimer = window.setTimeout(() => void runMentionSearch(sequence), 400);
    } catch (error) {
      if (!mention || mention.sequence !== sequence) return;
      mention.loading = false;
      mention.results = [];
      mention.error = error instanceof Error ? error.message : String(error);
      renderMentionPicker();
    }
  };

  const syncMention = () => {
    const match = activeMentionMatch(input);
    if (!match) { if (mention) clearMention(); return; }
    if (mention && mention.query === match.query && mention.triggerStart === match.triggerStart) return;
    window.clearTimeout(mentionTimer);
    const sequence = ++mentionSequence;
    mention = { workspaceId: options.workspaceId, ...match, results: [], roots: [], loading: true, error: "", selectedIndex: 0, sequence };
    renderMentionPicker();
    mentionTimer = window.setTimeout(() => void runMentionSearch(sequence), 100);
  };

  const setBusy = (busy: boolean) => {
    streaming = busy;
    renderGoalBar();
    const goalGuidanceAction = modeSelect.value === "goal" && goalAcceptsSteering();
    const stopAction = busy && !goalGuidanceAction;
    const sendTitle = goalGuidanceAction
      ? currentGoal?.status === "paused" ? "Queue goal guidance" : "Steer goal"
      : "Send";
    send.classList.toggle("is-busy", stopAction);
    send.innerHTML = `<span class="codicon codicon-${stopAction ? "debug-stop" : "send"}"></span>`;
    send.title = stopAction ? "Stop" : sendTitle;
    send.setAttribute("aria-label", stopAction ? "Stop stream" : goalGuidanceAction ? sendTitle : "Send message");
    options.onStreamingChange?.(busy);
  };

  const submit = async () => {
    const segments = snapshotComposer(input);
    const text = composerText(input);
    const canSteerBusyGoal = modeSelect.value === "goal" && goalAcceptsSteering();
    if (!text.trim() || submitting || (isStreaming() && !canSteerBusyGoal)) return;
    prepareCompletionNotificationPermission();
    preparePlanQuestionNotificationPermission();
    submitting = true;
    send.disabled = true;
    try {
      const editorContext = await options.beforeSend?.();
      if (editorContext === false) return;
      const referenceMap = new Map<string, PromptReference>();
      if (surface === "code") {
        for (const segment of segments) {
          if (segment.type !== "reference") continue;
          const key = `${segment.kind}\0${segment.ref.rootId}\0${segment.ref.path}`;
          if (!referenceMap.has(key)) {
            referenceMap.set(key, {
              ref: segment.ref, kind: segment.kind,
              referencePath: segment.referencePath, label: segment.label,
            });
          }
        }
      }
      const sendOptions: { editorContext?: EditorContextPayload; references?: PromptReference[] } = {
        editorContext: editorContext || undefined,
      };
      if (referenceMap.size) sendOptions.references = [...referenceMap.values()];
      let sent = false;
      if (modeSelect.value === "goal") {
        sent = goalAcceptsSteering()
          ? steerGoal(log, text, sendOptions)
          : startGoal(log, text, modelSelect.value || undefined, sendOptions);
      } else {
        sent = sendMessage(log, text, modelSelect.value || undefined, modeSelect.value || "general", sendOptions);
      }
      if (sent) {
        startingNewGoal = false;
        input.replaceChildren();
        clearMention();
        input.dispatchEvent(new Event("input", { bubbles: true }));
        input.focus();
      }
    } finally {
      submitting = false;
      send.disabled = false;
    }
  };

  form.addEventListener("submit", (event) => {
    event.preventDefault();
    if (isStreaming() && !(modeSelect.value === "goal" && goalAcceptsSteering())) stopStream();
    else void submit();
  }, { signal });
  input.addEventListener("input", () => { saveDraft(); syncMention(); }, { signal });
  input.addEventListener("keydown", (event) => {
    const keyboard = event as KeyboardEvent;
    const chip = (event.target as Element).closest<HTMLElement>("[data-chat-file-mention]");
    if (chip && (keyboard.key === "Enter" || keyboard.key === " ")) {
      event.preventDefault();
      const reference = referenceFromChip(chip);
      if (reference) void options.onActivateReference?.(reference);
      return;
    }
    if (mention && !keyboard.isComposing) {
      if (keyboard.key === "Escape") { event.preventDefault(); clearMention(); return; }
      if ((keyboard.key === "ArrowDown" || keyboard.key === "ArrowUp") && mention.results.length) {
        event.preventDefault();
        mention.selectedIndex = (mention.selectedIndex + (keyboard.key === "ArrowDown" ? 1 : -1) + mention.results.length) % mention.results.length;
        updateMentionSelection();
        return;
      }
      if ((keyboard.key === "Enter" || keyboard.key === "Tab") && mention.results.length) {
        event.preventDefault(); selectMention(mention.selectedIndex); return;
      }
    }
    if (keyboard.key === "Enter" && !keyboard.shiftKey && !keyboard.isComposing && !isCoarsePointer()) {
      event.preventDefault(); void submit();
    }
  }, { signal });
  input.addEventListener("click", (event) => {
    const chip = (event.target as Element).closest<HTMLElement>("[data-chat-file-mention]");
    if (!chip) return;
    event.preventDefault();
    const reference = referenceFromChip(chip);
    if (reference) void options.onActivateReference?.(reference);
  }, { signal });
  modelSelect.addEventListener("change", saveDraft, { signal });
  modeSelect.addEventListener("change", () => {
    startingNewGoal = false;
    saveDraft();
    setBusy(streaming);
  }, { signal });
  goalBar.addEventListener("click", (event) => {
    const button = (event.target as Element).closest<HTMLButtonElement>("[data-goal-action]");
    if (!button || !currentGoal) return;
    switch (button.dataset.goalAction) {
      case "pause":
        pauseGoal();
        break;
      case "resume":
        resumeGoal();
        break;
      case "edit": {
        const objective = window.prompt("Edit goal", currentGoal.objective);
        if (objective != null && objective.trim()) editGoal(objective);
        break;
      }
      case "new":
        startingNewGoal = true;
        modeSelect.value = "goal";
        renderGoalBar();
        input.focus();
        break;
      case "clear":
        clearGoal();
        break;
    }
  }, { signal });
  close?.addEventListener("click", () => options.onClose?.(), { signal });
  newChat.addEventListener("click", () => {
    if (!canClearChat(log) || !window.confirm("Start a new code chat? This clears the current code-chat history.")) return;
    if (clearChat(log)) {
      input.replaceChildren();
      saveDraft();
    }
  }, { signal });
  document.addEventListener("selectionchange", () => {
    if (document.activeElement === input) queueMicrotask(syncMention);
  }, { signal });
  document.addEventListener("click", (event) => {
    if (mention && !inputWrap.contains(event.target as Node)) clearMention();
  }, { signal });

  const unsubscribeStreaming = onStreamingChange(setBusy);
  let expectedChatResolved = false;
  const unsubscribeWorkspace = onChatWorkspaceChange((workspace: {
    hasSnapshot?: boolean;
    workspaceId?: string;
    surface?: string;
    activeChatId?: string;
    goal?: GoalState | null;
  } | null) => {
    currentGoal = workspace?.goal || null;
    goalStateReceivedAt = Date.now();
    if (currentGoal?.status === "active") startingNewGoal = false;
    setBusy(streaming);
    updateNewChatAvailability();
    if (!expectedChatResolved && options.expectedChatId && workspace?.hasSnapshot
      && workspace.workspaceId === options.workspaceId && workspace.surface === surface) {
      expectedChatResolved = true;
      options.onExpectedChatResolved?.(workspace.activeChatId === options.expectedChatId);
    }
  });
  openWorkspaceSession(log, options.workspaceId, {
    surface,
    onActivateFile: (ref: FileRef) => options.onActivateReference?.({
      workspaceId: options.workspaceId,
      ref,
      kind: "file",
      referencePath: ref.path,
      label: ref.path.split("/").at(-1) || ref.path,
    }),
    onActivateResource: (resource: HistoricalChatResource) => options.onActivateHistoricalResource?.(resource),
  });

  void Promise.all([
    api("/api/settings", { method: "GET" }).then((data: { settings?: { endpoints?: Endpoint[]; endpointSelection?: { chat?: string } }; endpoints?: Endpoint[]; endpointSelection?: { chat?: string } }) => {
      const settings = (data.settings || data) as { endpoints?: Endpoint[]; endpointSelection?: { chat?: string } };
      const endpoints: Endpoint[] = settings.endpoints || [];
      const selected = endpoints.find((endpoint) => endpoint.id === settings.endpointSelection?.chat)?.model || "";
      modelSelect.replaceChildren(new Option("Model", ""), ...endpoints.filter((endpoint) => endpoint.model).map((endpoint) => new Option(endpoint.name || endpoint.model!, endpoint.model!)));
      modelSelect.value = selected;
    }),
    api("/api/agent-modes", { method: "GET", query: { workspaceId: options.workspaceId } }).then((data: { modes?: AgentMode[] }) => {
      const modes = data.modes || [];
      modeSelect.replaceChildren(...modes.map((mode) => new Option(mode.name, mode.id)));
      if (![...modeSelect.options].some((option) => option.value === "general")) modeSelect.prepend(new Option("General", "general"));
      modeSelect.value = "general";
    }),
  ]).catch((error) => console.error("load code chat controls", error)).finally(() => {
    restoreDraft();
    renderGoalBar();
  });

  const goalElapsedTimer = window.setInterval(() => {
    if (currentGoal?.status === "active") renderGoalBar();
  }, 1000);

  return {
    focus: () => input.focus(),
    setContextNotice: (message) => {
      contextNotice.textContent = message || "";
      contextNotice.hidden = !message;
    },
    dispose: () => {
      saveDraft();
      abort.abort();
      clearMention();
      unsubscribeStreaming();
      unsubscribeWorkspace();
      window.clearInterval(goalElapsedTimer);
      closeWorkspaceSession(log);
      host.replaceChildren();
    },
  };
}
