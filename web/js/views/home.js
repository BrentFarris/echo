// views/home.js — Echo base layout shell with a functional chat view.
//
// Renders the primary chat view with the left sidebar and terminal bar,
// matching the OLD (Wails) frontend's structure and styling. The chat
// composer sends messages over WebSocket and streams the reply incrementally.

import { icons } from "../icons.js";
import { get } from "../api.js";
import { sendMessage, stopStream, isStreaming, onStreamingChange, openWorkspaceSession, closeWorkspaceSession } from "../chat.js";
import { loadWorkspaces, openWorkspaceDropdown, openAddWorkspaceModal, setActiveWorkspace, getActive, renderWorkspaceIcon } from "../workspaces.js";
import { codeRouteHash } from "../../src/navigation.ts";
import { renderMobilePrimaryNav, renderPrimaryNav } from "../../src/primaryNav.ts";

// Holds the cleanup function for the currently mounted chat view.
let chatCleanup = null;

// Endpoints loaded from settings, used to populate the model selector.
let endpoints = [];
// The model currently selected in the toolbar, or null to use the default.
let selectedModel = null;
let agentModes = [];
let selectedAgentModeId = "general";

function esc(value) {
  return String(value ?? "").replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[ch]));
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
          <div class="chat-log" data-chat-log>
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
                <button class="chat-toolbar-icon" type="button" title="More options" aria-label="More options">${icons.moreHorizontal}</button>
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

function terminalDock() {
  return `
    <section class="terminal-dock" data-terminal-dock aria-label="Integrated terminal">
      <header class="terminal-toolbar">
        <button class="terminal-title-button" type="button" aria-expanded="false">
          ${icons.terminal}
          <span class="terminal-title">Terminal</span>
          <span class="terminal-session-label">pwsh</span>
          <span class="terminal-status-indicator is-running" title="Terminal ready"></span>
          <span class="terminal-status-text">ready</span>
        </button>
        <div class="terminal-toolbar-actions">
          <button class="terminal-toolbar-button" type="button" title="Saved commands" aria-label="Saved commands">${icons.star}<span>Saved</span></button>
          <button class="terminal-toolbar-button icon-only" type="button" title="Restart terminal" aria-label="Restart terminal">${icons.refresh}</button>
          <button class="terminal-toolbar-button icon-only danger" type="button" title="Kill terminal" aria-label="Kill terminal">${icons.trash}</button>
          <button class="terminal-toolbar-button icon-only terminal-maximize-button" type="button" title="Maximize terminal" aria-label="Maximize terminal">${icons.expand}</button>
          <button class="terminal-toolbar-button icon-only" type="button" title="Open terminal" aria-label="Open terminal">${icons.arrowUp}</button>
        </div>
      </header>
    </section>
  `;
}

export function mount(root) {
  root.innerHTML = `
    <div class="app-shell">
      <div data-region="left-nav">${renderPrimaryNav({ active: "chat", workspaceName: "Echo", workspaceSelector: true })}</div>
      <div data-region="main">${chatPanel()}</div>
      <div data-region="terminal">${terminalDock()}</div>
      ${renderMobilePrimaryNav({ active: "chat", workspaceSelector: true })}
    </div>
  `;

  const log = root.querySelector("[data-chat-log]");
  const form = root.querySelector("[data-chat-form]");
  const input = root.querySelector("[data-chat-input]");
  const sendBtn = root.querySelector(".send-button");
  const modelTrigger = root.querySelector("[data-model-trigger]");
  const modelLabel = root.querySelector("[data-model-label]");
  const modeTrigger = root.querySelector("[data-mode-trigger]");
  const modeLabel = root.querySelector("[data-mode-label]");

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
          openWorkspaceSession(log, id);
          await loadAgentModes(id, modeLabel);
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
              openWorkspaceSession(log, workspace.id);
              await loadAgentModes(workspace.id, modeLabel);
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

  const onModeTriggerClick = (e) => {
    e.stopPropagation();
    closeModelDropdown();
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
    const workspaceId = getActive()?.id;
    if (workspaceId) localStorage.setItem(`echo.agentMode.${workspaceId}`, selectedAgentModeId);
    closeModeDropdown();
  };

  const toggleModelDropdown = () => {
    // If there are no configured models, do nothing (keep the plain "Model" label).
    if (!endpoints.length) return;
    if (modelDropdown.hidden) {
      closeModeDropdown();
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
    closeModelDropdown();
  };

  const onDocClick = (e) => {
    if (!modelDropdown.hidden && !modelDropdown.contains(e.target) && e.target !== modelTrigger) {
      closeModelDropdown();
    }
    if (!modeDropdown.hidden && !modeDropdown.contains(e.target) && e.target !== modeTrigger) {
      closeModeDropdown();
    }
  };

  const onResize = () => {
    if (!modelDropdown.hidden) positionModelDropdown();
    if (!modeDropdown.hidden) positionModeDropdown();
  };

  modelTrigger?.addEventListener("click", onModelTriggerClick);
  modelList?.addEventListener("click", onModelListClick);
  modeTrigger?.addEventListener("click", onModeTriggerClick);
  modeList?.addEventListener("click", onModeListClick);
  document.addEventListener("click", onDocClick);
  window.addEventListener("resize", onResize);

  const submit = () => {
    const text = input.textContent || "";
    if (!text.trim()) return;
    if (sendMessage(log, text, selectedModel || undefined, selectedAgentModeId)) {
      input.textContent = "";
      input.dispatchEvent(new Event("input"));
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
    // Enter sends (Shift+Enter inserts a newline).
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      submit();
    }
  };

  form.addEventListener("submit", (e) => {
    e.preventDefault();
    submit();
  });
  sendBtn.addEventListener("click", onSendButtonClick);
  input.addEventListener("keydown", onKeydown);

  // Reflect streaming state on the send button as it starts and stops.
  const unsubStreaming = onStreamingChange(setSendButtonBusy);

  // Store cleanup so unmount removes listeners.
  chatCleanup = () => {
    form.removeEventListener("submit", submit);
    sendBtn.removeEventListener("click", onSendButtonClick);
    input.removeEventListener("keydown", onKeydown);
    unsubStreaming();
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
    document.removeEventListener("click", onDocClick);
    window.removeEventListener("resize", onResize);
    modelDropdown.remove();
    modeDropdown.remove();
    closeWorkspaceSession(log);
  };

  // Load the configured endpoints so the model selector can be populated.
  loadEndpoints();
  // Load the registered workspaces so the selector dropdown can be populated,
  // then set the trigger icon to the active (last opened) workspace.
  loadWorkspaces().then(() => {
    updateWorkspaceNavigation();
    const workspaceId = getActive()?.id || "";
    openWorkspaceSession(log, workspaceId);
    loadAgentModes(workspaceId, modeLabel);
  });
}

async function loadAgentModes(workspaceId, label) {
  agentModes = [];
  selectedAgentModeId = "general";
  if (!workspaceId) {
    if (label) label.textContent = "Mode";
    return;
  }
  try {
    const data = await get("/api/agent-modes", { query: { workspaceId } });
    agentModes = data.modes || [];
    const stored = localStorage.getItem(`echo.agentMode.${workspaceId}`) || "general";
    selectedAgentModeId = agentModes.some((mode) => mode.id === stored) ? stored : "general";
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
