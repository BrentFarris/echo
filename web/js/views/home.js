// views/home.js — Echo base layout shell with a functional chat view.
//
// Renders the primary chat view with the left sidebar and terminal bar,
// matching the OLD (Wails) frontend's structure and styling. The chat
// composer sends messages over WebSocket and streams the reply incrementally.

import { icons } from "../icons.js";
import { get, post } from "../api.js";
import {
  activateChatTab, canClearChat, clearChat, closeChatTab, closeWorkspaceSession,
  createChatTab, isStreaming, onChatCommandError, onChatWorkspaceChange,
  onStreamingChange, openWorkspaceSession, sendMessage, stopStream,
} from "../chat.js";
import { loadWorkspaces, openWorkspaceDropdown, openAddWorkspaceModal, setActiveWorkspace, getActive, renderWorkspaceIcon } from "../workspaces.js";
import { codeFileRouteHash, codeRouteHash } from "../../src/navigation.ts";
import { renderMobilePrimaryNav, renderPrimaryNav } from "../../src/primaryNav.ts";
import { watchGitBadge } from "../../src/gitBadge.ts";
import { toast } from "../../src/code/ui.ts";
import { getRoots, revealEntry, searchEntries } from "../../src/code/editorApi.ts";
import {
  activeMentionMatch, composerText, insertReferenceChip, restoreComposer, snapshotComposer,
} from "../../src/chatMentions.ts";
import { detachTerminalDock, mountTerminalDock } from "../../src/terminal/index.ts";

// Holds the cleanup function for the currently mounted chat view.
let chatCleanup = null;

// Endpoints loaded from settings, used to populate the model selector.
let endpoints = [];
// The model currently selected in the toolbar, or null to use the default.
let selectedModel = null;
let defaultSelectedModel = null;
let agentModes = [];
let selectedAgentModeId = "general";
const tabComposerState = new Map();
const knownWorkspaceTabs = new Map();

function tabStateKey(workspaceId, chatId) { return `${workspaceId}\0${chatId}`; }

function esc(value) {
  return String(value ?? "").replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[ch]));
}

function confirmBusyChatClose() {
  return new Promise((resolve) => {
    const dialog = document.createElement("dialog");
    dialog.className = "settings-confirm-dialog chat-close-dialog";
    dialog.innerHTML = `
      <form method="dialog">
        <h2>Chat is still running</h2>
        <p>Stop the response and close this chat?</p>
        <div class="settings-confirm-actions">
          <button class="secondary-button" type="button" data-chat-close-choice="cancel">Cancel</button>
          <button class="secondary-button danger-button" type="button" data-chat-close-choice="confirm">Stop and close</button>
        </div>
      </form>
    `;
    let finished = false;
    const finish = (confirmed) => {
      if (finished) return;
      finished = true;
      dialog.close?.();
      dialog.remove();
      resolve(confirmed);
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
  });
}

// loadEndpoints fetches the configured endpoints from the server so the model
// selector can offer every endpoint's model for the next chat prompt. It also
// starts the selector on the model configured for the chat interaction.
async function loadEndpoints() {
  try {
    const data = await get("/api/settings");
    const s = data.settings || data;
    endpoints = (s.endpoints || []).map((e) => ({ ...e }));
    // Start on the model routed to the chat interaction, if any.
    const chatId = s.endpointSelection?.chat;
    const chatEndpoint = endpoints.find((e) => e.id === chatId);
    if (chatEndpoint?.model) {
      defaultSelectedModel = chatEndpoint.model;
      selectedModel = chatEndpoint.model;
      const label = document.querySelector("[data-model-label]");
      if (label) label.textContent = chatEndpoint.name || chatEndpoint.model;
    }
  } catch (err) {
    // Non-fatal: the selector just stays on the default model.
    console.error("Failed to load endpoints for model selector:", err);
  }
}

function chatPanel() {
  return `
    <main class="main-content">
      <section class="workspace-panel">
        <section class="work-panel chat-panel" aria-label="Chat">
          <div class="chat-tabs-shell" data-chat-tabs-shell hidden>
            <button class="chat-tabs-scroll chat-tabs-scroll-previous" type="button" title="Scroll tabs left" aria-label="Scroll tabs left" data-chat-tabs-scroll="previous">${icons.arrowLeft}</button>
            <div class="chat-tabs" role="tablist" aria-label="Open chats" data-chat-tabs></div>
            <button class="chat-tabs-scroll chat-tabs-scroll-next" type="button" title="Scroll tabs right" aria-label="Scroll tabs right" data-chat-tabs-scroll="next">${icons.arrowLeft}</button>
          </div>
          <div class="chat-log" id="chat-transcript" data-chat-log>
            <div class="empty-state chat-empty">Ask Echo to inspect, plan, or break down work for this workspace.</div>
          </div>
          <form class="chat-composer" data-chat-form>
            <div class="chat-composer-main" data-chat-input-wrap>
              <div
                class="chat-composer-editor"
                contenteditable="true"
                role="textbox"
                aria-multiline="true"
                aria-label="Message Echo"
                aria-autocomplete="list"
                aria-expanded="false"
                spellcheck="true"
                data-chat-input
                data-placeholder="Describe what to build"
              ></div>
            </div>
            <div class="chat-composer-toolbar">
              <div class="chat-composer-toolbar-left">
                <button class="chat-toolbar-icon" type="button" title="Attach file" aria-label="Attach file">${icons.plus}</button>
                <button class="chat-toolbar-icon chat-speech-recognition" type="button" title="Hold to speak" aria-label="Voice input">${icons.mic}</button>
                <button class="model-selector chat-toolbar-model" type="button" title="Select model" aria-haspopup="listbox" aria-expanded="false" data-model-trigger>
                  <span class="model-selector-label" data-model-label>Model</span>
                  <span class="model-selector-chevron">${icons.arrowDown}</span>
                </button>
                <button class="model-selector mode-selector chat-toolbar-mode" type="button" title="Agent mode" aria-haspopup="listbox" aria-expanded="false" data-mode-trigger>
                  <span class="model-selector-label" data-mode-label>Mode</span>
                  <span class="model-selector-chevron">${icons.arrowDown}</span>
                </button>
                <span class="chat-toolbar-separator"></span>
                <button class="chat-toolbar-icon" type="button" title="More options" aria-label="More options" aria-haspopup="menu" aria-expanded="false" aria-controls="chat-more-menu" data-chat-more-trigger>${icons.moreHorizontal}</button>
              </div>
              <div class="chat-composer-toolbar-right">
                <button class="send-button" type="button" title="Send" aria-label="Send message">${icons.send}</button>
              </div>
            </div>
          </form>
        </section>
      </section>
    </main>
  `;
}

export function mount(root) {
  root.innerHTML = `
    <div class="app-shell">
      <div data-region="left-nav">${renderPrimaryNav({ active: "chat", workspaceName: "Echo", workspaceSelector: true })}</div>
      <div data-region="main">${chatPanel()}</div>
      <div data-region="terminal"></div>
      ${renderMobilePrimaryNav({ active: "chat", workspaceSelector: true })}
    </div>
  `;

  const log = root.querySelector("[data-chat-log]");
  const terminalRegion = root.querySelector("[data-region='terminal']");
  const panel = root.querySelector(".chat-panel");
  const tabsShell = root.querySelector("[data-chat-tabs-shell]");
  const tabsHost = root.querySelector("[data-chat-tabs]");
  const tabsScrollPrevious = root.querySelector("[data-chat-tabs-scroll='previous']");
  const tabsScrollNext = root.querySelector("[data-chat-tabs-scroll='next']");
  const form = root.querySelector("[data-chat-form]");
  const inputWrap = root.querySelector("[data-chat-input-wrap]");
  const input = root.querySelector("[data-chat-input]");
  const sendBtn = root.querySelector(".send-button");
  const modelTrigger = root.querySelector("[data-model-trigger]");
  const modelLabel = root.querySelector("[data-model-label]");
  const modeTrigger = root.querySelector("[data-mode-trigger]");
  const modeLabel = root.querySelector("[data-mode-label]");
  const moreTrigger = root.querySelector("[data-chat-more-trigger]");
  let currentWorkspaceId = "";
  let currentChatId = "";
  let currentTabs = [];
  let stopGitBadge = () => {};
  const creatingChatSkills = new Set();
  let focusNewTab = false;
  let renderedActiveChatId = "";
  const pendingTabCloses = new Set();
  const rootsByWorkspace = new Map();
  let mention = null;
  let mentionTimer = 0;
  let mentionSequence = 0;

  const updateModelLabel = () => {
    modelLabel.textContent = selectedModel
      ? endpoints.find((endpoint) => endpoint.model === selectedModel)?.name || selectedModel
      : "Model";
  };

  const updateModeLabel = () => {
    const selected = agentModes.find((mode) => mode.id === selectedAgentModeId);
    modeLabel.textContent = selected?.name || (currentWorkspaceId ? "General" : "Mode");
  };

  const rootsForWorkspace = (workspaceId) => {
    if (!rootsByWorkspace.has(workspaceId)) {
      rootsByWorkspace.set(workspaceId, getRoots(workspaceId).catch((error) => {
        rootsByWorkspace.delete(workspaceId);
        throw error;
      }));
    }
    return rootsByWorkspace.get(workspaceId);
  };

  const createReferenceChip = (reference) => {
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
    icon.className = "chat-mention-chip-icon";
    icon.innerHTML = reference.kind === "directory" ? icons.folder : icons.code;
    const label = document.createElement("span");
    label.className = "chat-mention-chip-label";
    label.textContent = reference.label;
    chip.append(icon, label);
    return chip;
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

  const referenceForEntry = (entry) => {
    const workspaceRoot = mention?.roots?.find((candidate) => candidate.id === entry.ref.rootId);
    if (!workspaceRoot) return null;
    const referencePath = entry.referencePath || (entry.ref.path
      ? `${workspaceRoot.referenceLabel || workspaceRoot.label}/${entry.ref.path}`
      : workspaceRoot.referenceLabel || workspaceRoot.label);
    return {
      workspaceId: currentWorkspaceId,
      ref: entry.ref,
      kind: entry.kind,
      referencePath,
      label: entry.name,
    };
  };

  const updateMentionSelection = () => {
    const options = [...inputWrap.querySelectorAll("[data-chat-mention-option]")];
    options.forEach((option, index) => {
      const selected = index === mention?.selectedIndex;
      option.classList.toggle("is-active", selected);
      option.setAttribute("aria-selected", String(selected));
    });
    if (mention?.results.length) {
      input.setAttribute("aria-activedescendant", `chat-mention-option-${mention.selectedIndex}`);
    } else {
      input.removeAttribute("aria-activedescendant");
    }
  };

  const selectMention = (index) => {
    const state = mention;
    if (!state) return;
    const entry = state.results[index];
    const reference = entry ? referenceForEntry(entry) : null;
    if (!reference) return;
    // A pointer click moves the document selection into the picker before its
    // click handler runs. Use the range captured while the mention was active
    // so mouse selection is as reliable as keyboard selection.
    const match = {
      triggerStart: state.triggerStart,
      caret: state.caret,
      query: state.query,
    };
    insertReferenceChip(input, match, createReferenceChip(reference));
    clearMention();
    input.dispatchEvent(new Event("input", { bubbles: true }));
    input.focus();
  };

  const renderMentionPicker = () => {
    inputWrap.querySelector("[data-chat-mention-picker]")?.remove();
    if (!mention) {
      input.setAttribute("aria-expanded", "false");
      return;
    }
    const picker = document.createElement("div");
    picker.className = "chat-mention-picker";
    picker.id = "chat-mention-list";
    picker.dataset.chatMentionPicker = "";
    picker.setAttribute("role", "listbox");
    picker.setAttribute("aria-label", "Workspace files and folders");
    picker.setAttribute("aria-busy", String(mention.loading));
    if (mention.loading && !mention.results.length) {
      const status = document.createElement("div");
      status.className = "chat-mention-status";
      status.setAttribute("role", "status");
      status.innerHTML = `<span class="chat-mention-spinner" aria-hidden="true"></span><span>Searching workspace…</span>`;
      picker.append(status);
    } else if (mention.error) {
      const status = document.createElement("div");
      status.className = "chat-mention-status is-error";
      status.setAttribute("role", "status");
      status.textContent = mention.error;
      picker.append(status);
    } else if (!mention.results.length) {
      const status = document.createElement("div");
      status.className = "chat-mention-status";
      status.setAttribute("role", "status");
      status.textContent = "No matching files or folders.";
      picker.append(status);
    } else {
      mention.results.forEach((entry, index) => {
        const reference = referenceForEntry(entry);
        if (!reference) return;
        const option = document.createElement("button");
        option.type = "button";
        option.className = `chat-mention-option${index === mention.selectedIndex ? " is-active" : ""}`;
        option.id = `chat-mention-option-${index}`;
        option.dataset.chatMentionOption = "";
        option.dataset.mentionIndex = String(index);
        option.setAttribute("role", "option");
        option.setAttribute("aria-selected", String(index === mention.selectedIndex));
        option.title = reference.referencePath;
        const icon = document.createElement("span");
        icon.className = "chat-mention-icon";
        icon.innerHTML = entry.kind === "directory" ? icons.folder : icons.file;
        const name = document.createElement("span");
        name.className = "chat-mention-name";
        const strong = document.createElement("strong");
        strong.textContent = entry.name;
        const path = document.createElement("span");
        path.textContent = reference.referencePath;
        name.append(strong, path);
        const kind = document.createElement("span");
        kind.className = "chat-mention-kind";
        kind.textContent = entry.kind === "directory" ? "Folder" : "File";
        option.append(icon, name, kind);
        option.addEventListener("mousedown", (event) => event.preventDefault());
        option.addEventListener("mousemove", () => {
          if (mention && mention.selectedIndex !== index) {
            mention.selectedIndex = index;
            updateMentionSelection();
          }
        });
        option.addEventListener("click", () => selectMention(index));
        picker.append(option);
      });
    }
    inputWrap.append(picker);
    input.setAttribute("aria-expanded", "true");
    input.setAttribute("aria-controls", picker.id);
    updateMentionSelection();
  };

  const runMentionSearch = async (workspaceId, sequence) => {
    const state = mention;
    if (!state || state.workspaceId !== workspaceId || state.sequence !== sequence) return;
    mentionTimer = 0;
    try {
      const [roots, response] = await Promise.all([
        rootsForWorkspace(workspaceId),
        searchEntries(workspaceId, state.query, 12),
      ]);
      if (!mention || mention.workspaceId !== workspaceId || mention.sequence !== sequence) return;
      mention.roots = roots;
      mention.results = (response.items || []).slice(0, 8);
      mention.loading = false;
      mention.error = "";
      mention.selectedIndex = Math.min(mention.selectedIndex, Math.max(0, mention.results.length - 1));
      renderMentionPicker();
      if (response.indexing) {
        mentionTimer = window.setTimeout(() => runMentionSearch(workspaceId, sequence), 400);
      }
    } catch (error) {
      if (!mention || mention.workspaceId !== workspaceId || mention.sequence !== sequence) return;
      mention.loading = false;
      mention.results = [];
      mention.error = error instanceof Error ? error.message : String(error);
      renderMentionPicker();
    }
  };

  const syncMention = () => {
    const match = currentWorkspaceId ? activeMentionMatch(input) : null;
    if (!match) {
      if (mention) clearMention();
      return;
    }
    if (mention && mention.workspaceId === currentWorkspaceId
      && mention.query === match.query && mention.triggerStart === match.triggerStart) {
      return;
    }
    window.clearTimeout(mentionTimer);
    const sequence = ++mentionSequence;
    mention = {
      workspaceId: currentWorkspaceId,
      triggerStart: match.triggerStart,
      caret: match.caret,
      query: match.query,
      results: [],
      roots: [],
      loading: true,
      error: "",
      selectedIndex: 0,
      sequence,
    };
    renderMentionPicker();
    mentionTimer = window.setTimeout(() => runMentionSearch(currentWorkspaceId, sequence), 100);
  };

  const handleMentionKeydown = (event) => {
    if (!mention || event.isComposing) return false;
    const match = activeMentionMatch(input);
    if (!match || match.triggerStart !== mention.triggerStart) {
      clearMention();
      return false;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      clearMention();
      return true;
    }
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      if (!mention.results.length) return true;
      event.preventDefault();
      const delta = event.key === "ArrowDown" ? 1 : -1;
      mention.selectedIndex = (mention.selectedIndex + delta + mention.results.length) % mention.results.length;
      updateMentionSelection();
      return true;
    }
    if ((event.key === "Enter" || event.key === "Tab") && mention.results.length) {
      event.preventDefault();
      selectMention(mention.selectedIndex);
      return true;
    }
    return false;
  };

  const activateReferenceChip = async (chip) => {
    const workspaceId = chip.dataset.workspaceId || currentWorkspaceId;
    const ref = { rootId: chip.dataset.rootId || "", path: chip.dataset.workspacePath || "" };
    if (!workspaceId || !ref.rootId) return;
    if (chip.dataset.workspaceKind === "directory") {
      try {
        await revealEntry(workspaceId, ref);
        toast("Opened in file browser.");
      } catch (error) {
        toast(error instanceof Error ? error.message : String(error), { sticky: true });
      }
      return;
    }
    saveCurrentComposer();
    location.hash = codeFileRouteHash(ref);
  };

  const saveCurrentComposer = () => {
    if (!currentWorkspaceId || !currentChatId) return;
    tabComposerState.set(tabStateKey(currentWorkspaceId, currentChatId), {
      segments: snapshotComposer(input),
      model: selectedModel,
      agentModeId: selectedAgentModeId,
    });
  };

  const restoreCurrentComposer = () => {
    if (!currentWorkspaceId || !currentChatId) return;
    const key = tabStateKey(currentWorkspaceId, currentChatId);
    let state = tabComposerState.get(key);
    if (!state) {
      state = { segments: [], model: defaultSelectedModel, agentModeId: "general" };
      tabComposerState.set(key, state);
    }
    selectedModel = state.model || null;
    selectedAgentModeId = agentModes.some((mode) => mode.id === state.agentModeId)
      ? state.agentModeId
      : "general";
    restoreComposer(input, state.segments || [], createReferenceChip);
    updateModelLabel();
    updateModeLabel();
  };

  const updateTabOverflow = () => {
    if (tabsShell.hidden) return;
    const hasOverflow = tabsHost.scrollWidth > tabsHost.clientWidth + 1;
    tabsShell.classList.toggle("has-overflow", hasOverflow);
    tabsScrollPrevious.disabled = !hasOverflow || tabsHost.scrollLeft <= 1;
    tabsScrollNext.disabled = !hasOverflow
      || tabsHost.scrollLeft + tabsHost.clientWidth >= tabsHost.scrollWidth - 1;
  };

  const renderTabs = () => {
    tabsHost.replaceChildren();
    const show = currentTabs.length >= 2;
    tabsShell.hidden = !show;
    panel.classList.toggle("has-chat-tabs", show);
    if (!show) {
      tabsShell.classList.remove("has-overflow");
      renderedActiveChatId = currentChatId;
      return;
    }
    for (const tab of currentTabs) {
      const item = document.createElement("div");
      item.className = `chat-tab-item${tab.chatId === currentChatId ? " is-active" : ""}`;
      item.dataset.chatId = tab.chatId;

      const activate = document.createElement("button");
      activate.type = "button";
      activate.className = "chat-tab-button";
      activate.dataset.chatTabActivate = tab.chatId;
      activate.setAttribute("role", "tab");
      activate.setAttribute("aria-selected", String(tab.chatId === currentChatId));
      activate.setAttribute("aria-controls", "chat-transcript");
      activate.tabIndex = tab.chatId === currentChatId ? 0 : -1;
      activate.title = tab.preview || "New chat";

      if (tab.busy) {
        const dot = document.createElement("span");
        dot.className = "chat-tab-busy-dot";
        dot.setAttribute("aria-label", "Response streaming");
        activate.append(dot);
      }
      const label = document.createElement("span");
      label.className = "chat-tab-label";
      label.textContent = tab.preview || "New chat";
      activate.append(label);

      const close = document.createElement("button");
      close.type = "button";
      close.className = "chat-tab-close";
      close.dataset.chatTabClose = tab.chatId;
      close.setAttribute("aria-label", `Close ${tab.preview || "New chat"}`);
      close.title = "Close chat";
      close.innerHTML = icons.x;
      item.append(activate, close);
      tabsHost.append(item);
    }
    const activeChanged = renderedActiveChatId !== currentChatId;
    renderedActiveChatId = currentChatId;
    requestAnimationFrame(() => {
      if (activeChanged) {
        [...tabsHost.querySelectorAll("[data-chat-tab-activate]")]
          .find((tab) => tab.dataset.chatTabActivate === currentChatId)
          ?.scrollIntoView({ block: "nearest", inline: "nearest" });
      }
      updateTabOverflow();
    });
  };

  const requestTabClose = async (chatId, forceConfirmation = false) => {
    const tab = currentTabs.find((candidate) => candidate.chatId === chatId);
    if (!tab) return;
    let stopIfBusy = false;
    if (tab.busy || forceConfirmation) {
      stopIfBusy = await confirmBusyChatClose();
      if (!stopIfBusy) return;
    }
    pendingTabCloses.add(chatId);
    if (!closeChatTab(chatId, stopIfBusy)) pendingTabCloses.delete(chatId);
  };

  // The desktop activity bar and mobile bottom bar share the same actions.
  const settingsButtons = [...root.querySelectorAll("[data-nav='settings']")];
  const codeButtons = [...root.querySelectorAll("[data-nav='code']")];
  const gitButtons = [...root.querySelectorAll("[data-nav='git']")];
  const onSettingsClick = () => {
    location.hash = "#/settings";
  };
  settingsButtons.forEach((button) => button.addEventListener("click", onSettingsClick));
  const onCodeClick = () => { location.hash = "#/code"; };
  codeButtons.forEach((button) => button.addEventListener("click", onCodeClick));
  const onGitClick = () => { location.hash = codeRouteHash("git"); };
  gitButtons.forEach((button) => button.addEventListener("click", onGitClick));

  // Workspace selector: open the dropdown, and the "+ Add a workspace" modal.
  const workspaceTriggers = [...root.querySelectorAll(".workspace-dropdown-trigger")];
  const workspaceIconLabels = [...root.querySelectorAll(".workspace-icon-label")];
  const mobileWorkspaceNames = [...root.querySelectorAll("[data-mobile-workspace-name]")];
  let closeWorkspaceDropdown = null;

  // Keep both navigation variants synchronized with the active workspace.
  const updateWorkspaceNavigation = () => {
    const active = getActive();
    workspaceIconLabels.forEach((label) => { label.innerHTML = renderWorkspaceIcon(active); });
    mobileWorkspaceNames.forEach((label) => { label.textContent = active?.name || "No workspace"; });
    workspaceTriggers.forEach((trigger) => {
      const title = active ? `Switch workspace (${active.name})` : "Select workspace";
      trigger.setAttribute("title", title);
      trigger.setAttribute("aria-label", title);
    });
  };

  const onWorkspaceTriggerClick = (e) => {
    e.stopPropagation();
    if (closeWorkspaceDropdown) {
      closeWorkspaceDropdown();
      closeWorkspaceDropdown = null;
      return;
    }
    closeWorkspaceDropdown = openWorkspaceDropdown(e.currentTarget, {
      onClose: () => { closeWorkspaceDropdown = null; },
      onSelect: async (id) => {
        try {
          await setActiveWorkspace(id);
          updateWorkspaceNavigation();
          mountTerminalDock(terminalRegion, getActive());
          openWorkspaceSession(log, id);
          await loadAgentModes(id, modeLabel);
          restoreCurrentComposer();
        } catch (err) {
          console.error("Failed to set active workspace:", err);
        }
      },
      onAdd: () => {
        openAddWorkspaceModal({
          onCreate: async (workspace) => {
            try {
              await setActiveWorkspace(workspace.id);
              updateWorkspaceNavigation();
              mountTerminalDock(terminalRegion, getActive());
              openWorkspaceSession(log, workspace.id);
              await loadAgentModes(workspace.id, modeLabel);
              restoreCurrentComposer();
            } catch (err) {
              console.error("Failed to open created workspace:", err);
            }
          },
        });
      },
    });
  };
  workspaceTriggers.forEach((trigger) => trigger.addEventListener("click", onWorkspaceTriggerClick));

  // Build the dropdown as a fixed-position overlay appended to <body> so it is
  // not clipped by the chat panel's overflow:hidden.
  const modelDropdown = document.createElement("div");
  modelDropdown.className = "model-dropdown";
  modelDropdown.hidden = true;
  modelDropdown.innerHTML = `
    <div class="model-dropdown-header">Model</div>
    <div class="model-dropdown-list" role="listbox" aria-label="Select model" data-model-list></div>
  `;
  const modelList = modelDropdown.querySelector("[data-model-list]");
  document.body.appendChild(modelDropdown);

  const modeDropdown = document.createElement("div");
  modeDropdown.className = "model-dropdown mode-dropdown";
  modeDropdown.hidden = true;
  modeDropdown.innerHTML = `
    <div class="model-dropdown-header">Agent mode</div>
    <div class="model-dropdown-list" role="listbox" aria-label="Select agent mode" data-mode-list></div>
    <a class="mode-dropdown-settings" href="#/settings">Manage modes in Settings</a>
  `;
  const modeList = modeDropdown.querySelector("[data-mode-list]");
  document.body.appendChild(modeDropdown);

  const moreMenu = document.createElement("div");
  moreMenu.id = "chat-more-menu";
  moreMenu.className = "chat-more-menu";
  moreMenu.hidden = true;
  moreMenu.setAttribute("role", "menu");
  moreMenu.setAttribute("aria-label", "More chat options");
  moreMenu.innerHTML = `
    <button type="button" role="menuitem" title="New tab" aria-label="Open a new chat tab" data-new-chat-tab-button>
      ${icons.plus}
      <span>New tab</span>
    </button>
    <button type="button" role="menuitem" title="Clear current chat" aria-label="Clear current chat" data-clear-chat-button>
      ${icons.refresh}
      <span>Clear current chat</span>
    </button>
    <button type="button" role="menuitem" title="Create skill from this chat" aria-label="Create workspace skill from chat" data-create-skill-button>
      ${icons.star}
      <span>Create skill</span>
    </button>
  `;
  document.body.appendChild(moreMenu);
  const newTabButton = moreMenu.querySelector("[data-new-chat-tab-button]");
  const clearChatButton = moreMenu.querySelector("[data-clear-chat-button]");
  const createSkillButton = moreMenu.querySelector("[data-create-skill-button]");

  const updateChatMenuActions = () => {
    const busy = currentTabs.find((tab) => tab.chatId === currentChatId)?.busy || isStreaming();
    const creating = creatingChatSkills.has(currentChatId);
    const hasTranscript = canClearChat(log);
    clearChatButton.disabled = busy || creating || !hasTranscript;
    createSkillButton.disabled = !currentWorkspaceId || !currentChatId || busy || creating || !hasTranscript;
  };

  // Render the model dropdown options from the loaded endpoints.
  const renderModelOptions = () => {
    const options = endpoints.map((e) => ({
      value: e.model,
      label: e.name ? `${e.name} — ${e.model}` : e.model,
    }));
    modelList.innerHTML = options.map((o) => `
      <button type="button" role="option" class="model-dropdown-item ${selectedModel === o.value ? "is-selected" : ""}" data-model-value="${esc(o.value)}">
        ${esc(o.label)}
      </button>
    `).join("");
  };

  const positionModelDropdown = () => {
    const rect = modelTrigger?.getBoundingClientRect();
    if (!rect) return;
    // Open the dropdown above the trigger so it stays on screen even though the
    // composer sits near the bottom of the viewport.
    const height = modelDropdown.offsetHeight || 240;
    modelDropdown.style.top = `${Math.max(8, rect.top - height - 6)}px`;
    modelDropdown.style.left = `${rect.left}px`;
    // Keep the dropdown within the viewport horizontally.
    const width = modelDropdown.offsetWidth || 220;
    const overflow = rect.left + width - window.innerWidth + 8;
    if (overflow > 0) modelDropdown.style.left = `${Math.max(8, rect.left - overflow)}px`;
  };

  const closeModelDropdown = () => {
    modelDropdown.hidden = true;
    modelTrigger?.setAttribute("aria-expanded", "false");
  };

  const renderModeOptions = () => {
    modeList.innerHTML = agentModes.map((mode) => `
      <button type="button" role="option" class="model-dropdown-item mode-dropdown-item ${selectedAgentModeId === mode.id ? "is-selected" : ""}" data-mode-id="${esc(mode.id)}">
        <span>${esc(mode.name)}</span>
        <small>${mode.builtIn ? "Built-in" : esc(mode.prompt)}</small>
      </button>
    `).join("");
  };

  const positionModeDropdown = () => {
    const rect = modeTrigger?.getBoundingClientRect();
    if (!rect) return;
    const height = modeDropdown.offsetHeight || 280;
    modeDropdown.style.top = `${Math.max(8, rect.top - height - 6)}px`;
    modeDropdown.style.left = `${rect.left}px`;
    const width = modeDropdown.offsetWidth || 280;
    const overflow = rect.left + width - window.innerWidth + 8;
    if (overflow > 0) modeDropdown.style.left = `${Math.max(8, rect.left - overflow)}px`;
  };

  const closeModeDropdown = () => {
    modeDropdown.hidden = true;
    modeTrigger?.setAttribute("aria-expanded", "false");
  };

  const positionMoreMenu = () => {
    const rect = moreTrigger?.getBoundingClientRect();
    if (!rect) return;
    const margin = 8;
    const gap = 6;
    const width = moreMenu.offsetWidth || 190;
    const height = moreMenu.offsetHeight || 116;
    const left = Math.min(
      Math.max(margin, rect.left),
      Math.max(margin, window.innerWidth - width - margin),
    );
    const top = rect.top - height - gap >= margin
      ? rect.top - height - gap
      : Math.min(rect.bottom + gap, window.innerHeight - height - margin);
    moreMenu.style.left = `${left}px`;
    moreMenu.style.top = `${Math.max(margin, top)}px`;
  };

  const closeMoreMenu = (restoreFocus = false) => {
    moreMenu.hidden = true;
    moreTrigger?.setAttribute("aria-expanded", "false");
    if (restoreFocus) moreTrigger?.focus();
  };

  const openMoreMenu = () => {
    closeModelDropdown();
    closeModeDropdown();
    updateChatMenuActions();
    moreMenu.hidden = false;
    positionMoreMenu();
    moreTrigger?.setAttribute("aria-expanded", "true");
  };

  const onModeTriggerClick = (e) => {
    e.stopPropagation();
    closeModelDropdown();
    closeMoreMenu();
    if (modeDropdown.hidden) {
      renderModeOptions();
      modeDropdown.hidden = false;
      positionModeDropdown();
      modeTrigger?.setAttribute("aria-expanded", "true");
    } else {
      closeModeDropdown();
    }
  };

  const onModeListClick = (e) => {
    const item = e.target.closest("[data-mode-id]");
    if (!item) return;
    selectedAgentModeId = item.dataset.modeId || "general";
    const selected = agentModes.find((mode) => mode.id === selectedAgentModeId);
    modeLabel.textContent = selected?.name || "General";
    saveCurrentComposer();
    closeModeDropdown();
  };

  const toggleModelDropdown = () => {
    // If there are no configured models, do nothing (keep the plain "Model" label).
    if (!endpoints.length) return;
    if (modelDropdown.hidden) {
      closeModeDropdown();
      closeMoreMenu();
      renderModelOptions();
      modelDropdown.hidden = false;
      positionModelDropdown();
      modelTrigger?.setAttribute("aria-expanded", "true");
    } else {
      closeModelDropdown();
    }
  };

  const onModelTriggerClick = (e) => {
    e.stopPropagation();
    toggleModelDropdown();
  };

  const onModelListClick = (e) => {
    const item = e.target.closest("[data-model-value]");
    if (!item) return;
    selectedModel = item.dataset.modelValue || null;
    modelLabel.textContent = selectedModel
      ? endpoints.find((ep) => ep.model === selectedModel)?.name || selectedModel
      : "Model";
    saveCurrentComposer();
    closeModelDropdown();
  };

  const onMoreTriggerClick = (e) => {
    e.stopPropagation();
    if (moreMenu.hidden) {
      openMoreMenu();
      if (e.detail === 0) moreMenu.querySelector('[role="menuitem"]')?.focus();
    } else {
      closeMoreMenu();
    }
  };

  const onClearChatClick = (e) => {
    e.stopPropagation();
    closeMoreMenu();
    if (clearChatButton.disabled || !window.confirm("Clear the current chat?")) return;
    if (clearChat(log)) {
      input.replaceChildren();
      input.dispatchEvent(new Event("input", { bubbles: true }));
    }
  };

  const onNewTabClick = (e) => {
    e.stopPropagation();
    closeMoreMenu();
    focusNewTab = createChatTab();
  };

  const onCreateSkillClick = async (e) => {
    e.stopPropagation();
    if (createSkillButton.disabled) return;
    const workspaceId = currentWorkspaceId;
    const chatId = currentChatId;
    closeMoreMenu();
    creatingChatSkills.add(chatId);
    updateChatMenuActions();
    try {
      const result = await post(`/api/workspaces/${encodeURIComponent(workspaceId)}/chats/${encodeURIComponent(chatId)}/skills`, {});
      toast(`Created skill "${result.name}".`);
    } catch (err) {
      toast(err instanceof Error ? err.message : String(err), { sticky: true });
    } finally {
      creatingChatSkills.delete(chatId);
      updateChatMenuActions();
    }
  };

  const onTabsClick = (e) => {
    const close = e.target.closest("[data-chat-tab-close]");
    if (close) {
      e.stopPropagation();
      requestTabClose(close.dataset.chatTabClose);
      return;
    }
    const activate = e.target.closest("[data-chat-tab-activate]");
    if (activate && activate.dataset.chatTabActivate !== currentChatId) {
      activateChatTab(activate.dataset.chatTabActivate);
    }
  };

  const onTabsAuxClick = (e) => {
    if (e.button !== 1) return;
    const tab = e.target.closest("[data-chat-tab-activate]");
    if (!tab) return;
    e.preventDefault();
    requestTabClose(tab.dataset.chatTabActivate);
  };

  const onTabsKeydown = (e) => {
    const focused = e.target.closest("[data-chat-tab-activate]");
    if (!focused) return;
    const buttons = [...tabsHost.querySelectorAll("[data-chat-tab-activate]")];
    const index = buttons.indexOf(focused);
    let next = -1;
    if (e.key === "ArrowLeft") next = index > 0 ? index - 1 : buttons.length - 1;
    if (e.key === "ArrowRight") next = index < buttons.length - 1 ? index + 1 : 0;
    if (e.key === "Home") next = 0;
    if (e.key === "End") next = buttons.length - 1;
    if (next < 0) return;
    e.preventDefault();
    const target = buttons[next];
    target.focus();
    if (target.dataset.chatTabActivate !== currentChatId) activateChatTab(target.dataset.chatTabActivate);
  };

  const scrollTabs = (direction) => {
    const distance = Math.max(140, Math.floor(tabsHost.clientWidth * 0.7));
    if (typeof tabsHost.scrollBy === "function") {
      tabsHost.scrollBy({ left: direction * distance, behavior: "smooth" });
    } else {
      tabsHost.scrollLeft += direction * distance;
    }
    requestAnimationFrame(updateTabOverflow);
  };

  const onTabsWheel = (e) => {
    if (!tabsShell.classList.contains("has-overflow")) return;
    const delta = Math.abs(e.deltaX) > Math.abs(e.deltaY) ? e.deltaX : e.deltaY;
    if (!delta) return;
    tabsHost.scrollLeft += delta;
    e.preventDefault();
    updateTabOverflow();
  };

  const onTabsScroll = () => updateTabOverflow();
  const onTabsScrollPrevious = () => scrollTabs(-1);
  const onTabsScrollNext = () => scrollTabs(1);

  const onMoreMenuKeydown = (e) => {
    const items = [...moreMenu.querySelectorAll('[role="menuitem"]')];
    const current = items.indexOf(document.activeElement);
    if (e.key === "Escape") {
      e.preventDefault();
      closeMoreMenu(true);
      return;
    }
    if (e.key !== "ArrowDown" && e.key !== "ArrowUp" && e.key !== "Home" && e.key !== "End") return;
    e.preventDefault();
    let next = 0;
    if (e.key === "End") next = items.length - 1;
    if (e.key === "ArrowDown") next = current < items.length - 1 ? current + 1 : 0;
    if (e.key === "ArrowUp") next = current > 0 ? current - 1 : items.length - 1;
    items[next]?.focus();
  };

  const onDocClick = (e) => {
    if (!modelDropdown.hidden && !modelDropdown.contains(e.target) && e.target !== modelTrigger) {
      closeModelDropdown();
    }
    if (!modeDropdown.hidden && !modeDropdown.contains(e.target) && e.target !== modeTrigger) {
      closeModeDropdown();
    }
    if (!moreMenu.hidden && !moreMenu.contains(e.target) && !moreTrigger?.contains(e.target)) {
      closeMoreMenu();
    }
    if (mention && !inputWrap.contains(e.target)) clearMention();
  };

  const onResize = () => {
    if (!modelDropdown.hidden) positionModelDropdown();
    if (!modeDropdown.hidden) positionModeDropdown();
    if (!moreMenu.hidden) positionMoreMenu();
    updateTabOverflow();
  };

  modelTrigger?.addEventListener("click", onModelTriggerClick);
  modelList?.addEventListener("click", onModelListClick);
  modeTrigger?.addEventListener("click", onModeTriggerClick);
  modeList?.addEventListener("click", onModeListClick);
  moreTrigger?.addEventListener("click", onMoreTriggerClick);
  newTabButton.addEventListener("click", onNewTabClick);
  clearChatButton.addEventListener("click", onClearChatClick);
  createSkillButton.addEventListener("click", onCreateSkillClick);
  tabsHost.addEventListener("click", onTabsClick);
  tabsHost.addEventListener("auxclick", onTabsAuxClick);
  tabsHost.addEventListener("keydown", onTabsKeydown);
  tabsHost.addEventListener("wheel", onTabsWheel, { passive: false });
  tabsHost.addEventListener("scroll", onTabsScroll);
  tabsScrollPrevious.addEventListener("click", onTabsScrollPrevious);
  tabsScrollNext.addEventListener("click", onTabsScrollNext);
  moreMenu.addEventListener("keydown", onMoreMenuKeydown);
  document.addEventListener("click", onDocClick);
  window.addEventListener("resize", onResize);
  const tabsResizeObserver = typeof ResizeObserver === "function"
    ? new ResizeObserver(updateTabOverflow)
    : null;
  tabsResizeObserver?.observe(tabsHost);

  const submit = () => {
    const text = composerText(input);
    if (!text.trim()) return;
    if (sendMessage(log, text, selectedModel || undefined, selectedAgentModeId)) {
      input.replaceChildren();
      clearMention();
      input.dispatchEvent(new Event("input", { bubbles: true }));
      input.focus();
    }
  };

  // setSendButtonBusy toggles the send button between send and stop while a
  // reply is streaming, matching the OLD Echo behavior.
  const setSendButtonBusy = (busy) => {
    sendBtn.classList.toggle("is-busy", busy);
    sendBtn.innerHTML = busy ? icons.stop : icons.send;
    sendBtn.title = busy ? "Stop" : "Send";
    sendBtn.setAttribute("aria-label", busy ? "Stop stream" : "Send message");
    updateChatMenuActions();
  };

  const handleWorkspaceState = (state) => {
    const nextWorkspaceId = state?.workspaceId || "";
    const nextChatId = state?.activeChatId || "";
    const previousChatId = currentChatId;
    if (currentWorkspaceId && currentChatId
      && (nextWorkspaceId !== currentWorkspaceId || nextChatId !== currentChatId)) {
      saveCurrentComposer();
    }

    const previousWorkspaceId = currentWorkspaceId;
    currentWorkspaceId = nextWorkspaceId;
    currentChatId = nextChatId;
    currentTabs = state?.tabs || [];

    if (previousWorkspaceId !== currentWorkspaceId) {
      clearMention();
      stopGitBadge();
      stopGitBadge = watchGitBadge(root, currentWorkspaceId);
    }

    if (state?.hasSnapshot && currentWorkspaceId) {
      const remaining = new Set(currentTabs.map((tab) => tab.chatId));
      for (const chatId of knownWorkspaceTabs.get(currentWorkspaceId) || []) {
        if (!remaining.has(chatId)) {
          tabComposerState.delete(tabStateKey(currentWorkspaceId, chatId));
          pendingTabCloses.delete(chatId);
        }
      }
      knownWorkspaceTabs.set(currentWorkspaceId, remaining);
    }

    if (currentWorkspaceId && currentChatId
      && (previousWorkspaceId !== currentWorkspaceId || previousChatId !== currentChatId)) {
      restoreCurrentComposer();
    }
    renderTabs();
    updateChatMenuActions();

    if (focusNewTab && state?.hasSnapshot && currentChatId) {
      focusNewTab = false;
      requestAnimationFrame(() => {
        [...tabsHost.querySelectorAll("[data-chat-tab-activate]")]
          .find((tab) => tab.dataset.chatTabActivate === currentChatId)
          ?.scrollIntoView({ block: "nearest", inline: "nearest" });
        input.focus();
      });
    }
  };

  const handleChatCommandError = (message) => {
    const chatId = message.chatId || "";
    if (message.code !== "session_busy" || !pendingTabCloses.has(chatId)) return false;
    pendingTabCloses.delete(chatId);
    requestTabClose(chatId, true);
    return true;
  };

  // While streaming, the button stops the reply; otherwise it sends.
  const onSendButtonClick = () => {
    if (isStreaming()) {
      stopStream();
    } else {
      submit();
    }
  };

  const onKeydown = (e) => {
    const chip = e.target.closest?.("[data-chat-file-mention]");
    if (chip && (e.key === "Enter" || e.key === " ")) {
      e.preventDefault();
      void activateReferenceChip(chip);
      return;
    }
    if (handleMentionKeydown(e)) return;
    // Enter sends (Shift+Enter inserts a newline).
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      submit();
    }
  };

  const onInput = () => {
    saveCurrentComposer();
    syncMention();
  };

  const onInputClick = (event) => {
    const chip = event.target.closest?.("[data-chat-file-mention]");
    if (!chip) return;
    event.preventDefault();
    event.stopPropagation();
    void activateReferenceChip(chip);
  };

  const onSelectionChange = () => {
    if (document.activeElement !== input) return;
    queueMicrotask(syncMention);
  };

  const onFormSubmit = (e) => {
    e.preventDefault();
    submit();
  };
  form.addEventListener("submit", onFormSubmit);
  sendBtn.addEventListener("click", onSendButtonClick);
  input.addEventListener("keydown", onKeydown);
  input.addEventListener("input", onInput);
  input.addEventListener("click", onInputClick);
  document.addEventListener("selectionchange", onSelectionChange);

  // Reflect streaming state on the send button as it starts and stops.
  const unsubStreaming = onStreamingChange(setSendButtonBusy);
  const unsubWorkspace = onChatWorkspaceChange(handleWorkspaceState);
  const unsubCommandError = onChatCommandError(handleChatCommandError);

  // Store cleanup so unmount removes listeners.
  chatCleanup = () => {
    saveCurrentComposer();
    form.removeEventListener("submit", onFormSubmit);
    sendBtn.removeEventListener("click", onSendButtonClick);
    input.removeEventListener("keydown", onKeydown);
    input.removeEventListener("input", onInput);
    input.removeEventListener("click", onInputClick);
    document.removeEventListener("selectionchange", onSelectionChange);
    unsubStreaming();
    unsubWorkspace();
    unsubCommandError();
    settingsButtons.forEach((button) => button.removeEventListener("click", onSettingsClick));
    codeButtons.forEach((button) => button.removeEventListener("click", onCodeClick));
    gitButtons.forEach((button) => button.removeEventListener("click", onGitClick));
    workspaceTriggers.forEach((trigger) => trigger.removeEventListener("click", onWorkspaceTriggerClick));
    if (closeWorkspaceDropdown) {
      closeWorkspaceDropdown();
      closeWorkspaceDropdown = null;
    }
    modelTrigger?.removeEventListener("click", onModelTriggerClick);
    modelList?.removeEventListener("click", onModelListClick);
    modeTrigger?.removeEventListener("click", onModeTriggerClick);
    modeList?.removeEventListener("click", onModeListClick);
    moreTrigger?.removeEventListener("click", onMoreTriggerClick);
    newTabButton.removeEventListener("click", onNewTabClick);
    clearChatButton.removeEventListener("click", onClearChatClick);
    createSkillButton.removeEventListener("click", onCreateSkillClick);
    tabsHost.removeEventListener("click", onTabsClick);
    tabsHost.removeEventListener("auxclick", onTabsAuxClick);
    tabsHost.removeEventListener("keydown", onTabsKeydown);
    tabsHost.removeEventListener("wheel", onTabsWheel);
    tabsHost.removeEventListener("scroll", onTabsScroll);
    tabsScrollPrevious.removeEventListener("click", onTabsScrollPrevious);
    tabsScrollNext.removeEventListener("click", onTabsScrollNext);
    tabsResizeObserver?.disconnect();
    moreMenu.removeEventListener("keydown", onMoreMenuKeydown);
    document.removeEventListener("click", onDocClick);
    window.removeEventListener("resize", onResize);
    modelDropdown.remove();
    modeDropdown.remove();
    moreMenu.remove();
    clearMention();
    stopGitBadge();
    detachTerminalDock(terminalRegion);
    closeWorkspaceSession(log);
  };

  // Load the configured endpoints so the model selector can be populated.
  loadEndpoints();
  // Load the registered workspaces so the selector dropdown can be populated,
  // then set the trigger icon to the active (last opened) workspace.
  loadWorkspaces().then(async () => {
    updateWorkspaceNavigation();
    const activeWorkspace = getActive();
    const workspaceId = activeWorkspace?.id || "";
    mountTerminalDock(terminalRegion, activeWorkspace);
    openWorkspaceSession(log, workspaceId);
    await loadAgentModes(workspaceId, modeLabel);
    restoreCurrentComposer();
  });
}

async function loadAgentModes(workspaceId, label) {
  agentModes = [];
  if (!workspaceId) {
    if (label) label.textContent = "Mode";
    return;
  }
  try {
    const data = await get("/api/agent-modes", { query: { workspaceId } });
    agentModes = data.modes || [];
    const selected = agentModes.find((mode) => mode.id === selectedAgentModeId);
    if (label) label.textContent = selected?.name || "General";
  } catch (err) {
    console.error("Failed to load agent modes:", err);
    if (label) label.textContent = "General";
  }
}

export function unmount() {
  if (chatCleanup) {
    chatCleanup();
    chatCleanup = null;
  }
}
