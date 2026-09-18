import { activeMentionMatch, insertReferenceChip, type ChatReference } from "./chatMentions";
import { getRoots, searchEntries } from "./code/editorApi";
import type { SearchResult, WorkspaceRoot } from "./code/types";

let nextPickerID = 0;
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

export function createReferenceChip(reference: ChatReference): HTMLElement {
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

export function referenceFromChip(chip: HTMLElement): ChatReference | null {
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

export function installMentionPicker(input: HTMLElement, inputWrap: HTMLElement, options: {
  workspaceId: string;
  signal: AbortSignal;
  onActivateReference?: (reference: ChatReference) => void | Promise<void>;
}) {
  const { signal } = options;
  const pickerID = `chat-mention-${++nextPickerID}`;
  let mention: MentionState | null = null;
  let mentionTimer = 0;
  let mentionSequence = 0;
  let rootsPromise: Promise<WorkspaceRoot[]> | null = null;
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
    if (mention?.results.length) input.setAttribute("aria-activedescendant", `${pickerID}-${mention.selectedIndex}`);
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
    picker.id = `${pickerID}-list`;
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
        option.id = `${pickerID}-${index}`;
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
    if (signal.aborted) return;
    const match = activeMentionMatch(input);
    if (!match) { if (mention) clearMention(); return; }
    if (mention && mention.query === match.query && mention.triggerStart === match.triggerStart) return;
    window.clearTimeout(mentionTimer);
    const sequence = ++mentionSequence;
    mention = { workspaceId: options.workspaceId, ...match, results: [], roots: [], loading: true, error: "", selectedIndex: 0, sequence };
    renderMentionPicker();
    mentionTimer = window.setTimeout(() => void runMentionSearch(sequence), 100);
  };

  input.addEventListener("click", (event) => {
    const chip = (event.target as Element).closest<HTMLElement>("[data-chat-file-mention]");
    if (!chip) return;
    event.preventDefault();
    const reference = referenceFromChip(chip);
    if (reference) void options.onActivateReference?.(reference);
  }, { signal });
  document.addEventListener("selectionchange", () => {
    if (document.activeElement === input) queueMicrotask(syncMention);
  }, { signal });
  document.addEventListener("click", (event) => {
    if (mention && !inputWrap.contains(event.target as Node)) clearMention();
  }, { signal });

  input.addEventListener("input", syncMention, { signal });
  signal.addEventListener("abort", clearMention, { once: true });
  return { clear: clearMention, sync: syncMention, handleKeydown(keyboard: KeyboardEvent): boolean {
    const chip = (keyboard.target as Element).closest<HTMLElement>("[data-chat-file-mention]");
    if (chip && (keyboard.key === "Enter" || keyboard.key === " ")) {
      keyboard.preventDefault(); keyboard.stopPropagation();
      const reference = referenceFromChip(chip);
      if (reference) void options.onActivateReference?.(reference);
      return true;
    }
    if (mention && !keyboard.isComposing) {
      if (keyboard.key === "Escape") { keyboard.preventDefault(); keyboard.stopPropagation(); clearMention(); return true; }
      if ((keyboard.key === "ArrowDown" || keyboard.key === "ArrowUp") && mention.results.length) {
        keyboard.preventDefault(); keyboard.stopPropagation();
        mention.selectedIndex = (mention.selectedIndex + (keyboard.key === "ArrowDown" ? 1 : -1) + mention.results.length) % mention.results.length;
        updateMentionSelection();
        return true;
      }
      if ((keyboard.key === "Enter" || keyboard.key === "Tab") && mention.results.length) {
        keyboard.preventDefault(); keyboard.stopPropagation(); selectMention(mention.selectedIndex); return true;
      }
    }
    return false;
  } };
}
