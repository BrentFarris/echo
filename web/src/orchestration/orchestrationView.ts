// orchestrationView.ts — Orchestration page: workspace groups with a queued
// pending-task pipeline. Pending prompts typed here are dispatched by the
// server's orchestrator to the first ready workspace in the group, running as
// the built-in "Group Worker" agent mode in a fresh chat tab.
import "./orchestration.css";
import "../echoMap.css";

import { get, post, put, del } from "../../js/api.js";
import { icons } from "../../js/icons.js";
import * as ws from "../../js/ws.js";
import { toast } from "../code/ui";
import { EchoMapView, type EchoMapChat } from "../echoMapView";
import { renderMobilePrimaryNav, renderPrimaryNav } from "../primaryNav";
import { mountSpeechRecognition, type SpeechRecognitionController } from "../speechRecognition";

type WorkspaceRef = { id: string; name: string };

type EndpointInfo = { model: string; name: string; temperature: number };

type PendingTask = {
  id: string;
  prompt: string;
  model?: string;
  reasoningEffort?: string;
  temperature?: number | null;
  ready: boolean;
  createdAt: string;
  dispatchedWorkspaceId?: string;
  dispatchedChatId?: string;
  dispatchedAt?: string | null;
};

type WorkspaceGroup = {
  id: string;
  name: string;
  workspaceIds: string[];
  workspaces: WorkspaceRef[];
  pendingTasks?: PendingTask[];
};

type SettingsResponse = {
  endpoints?: Array<{ model?: string; name?: string; temperature?: number }>;
};

const GROUP_STORAGE_KEY = "echoOrchestrationGroupId";

// Thinking slider ladder, mirroring the chat composer's mode popup: position 0
// (leftmost) is Off, position 6 (rightmost) is Max, and "Default" in the
// middle sends no override.
const thinkingLadder = [
  { position: 0, label: "Off", value: "none" },
  { position: 1, label: "Low", value: "low" },
  { position: 2, label: "Medium", value: "medium" },
  { position: 3, label: "Default", value: "" },
  { position: 4, label: "High", value: "high" },
  { position: 5, label: "XHigh", value: "xhigh" },
  { position: 6, label: "Max", value: "max" },
];

const thinkingPositionForValue = (value: string) => {
  const entry = thinkingLadder.find((step) => step.value === value);
  return entry ? entry.position : 3;
};

// Other built-in modes are listed but disabled: dispatched tasks always run as
// Group Worker.
const lockedModeOptions = [
  { id: "general", name: "General" },
  { id: "plan", name: "Plan" },
  { id: "goal", name: "Goal" },
  { id: "pair-programming", name: "Pair Programming" },
  { id: "group-worker", name: "Group Worker" },
];

function escapeHTML(value: string): string {
  return value.replace(/[&<>"']/g, (character) => ({
    "&": "&amp;",
    "<": "&lt;",
    ">": "&gt;",
    '"': "&quot;",
    "'": "&#39;",
  }[character]!));
}

class OrchestrationView {
  private root: HTMLElement;
  private groups: WorkspaceGroup[] = [];
  private workspaces: WorkspaceRef[] = [];
  private endpoints: EndpointInfo[] = [];
  private selectedGroupId: string = localStorage.getItem(GROUP_STORAGE_KEY) || "";
  private selectedModel = "";
  private selectedThinking = "";
  private selectedTemperature = 0.6;
  private abort = new AbortController();
  private mapView: EchoMapView | null = null;
  private speech: SpeechRecognitionController | null = null;
  private modelDropdown: HTMLDivElement | null = null;
  private modeDropdown: HTMLDivElement | null = null;
  private disposed = false;
  private wsUnsubscribers: Array<() => void> = [];
  private editingTaskId: string | null = null;

  constructor(root: HTMLElement) {
    this.root = root;
  }

  async start(): Promise<void> {
    this.renderShell();
    this.bindNavigation();
    this.initComposer();
    this.wsUnsubscribers.push(
      ws.on("workspace_groups_changed", () => { void this.refreshGroups(); }),
      ws.on("chat_completed", () => { void this.refreshGroups(); }),
    );
    await Promise.all([this.loadWorkspaces(), this.loadEndpoints()]);
    await this.refreshGroups();
  }

  dispose(): void {
    this.disposed = true;
    this.abort.abort();
    this.mapView?.dispose();
    this.mapView = null;
    this.speech?.dispose();
    this.speech = null;
    this.modelDropdown?.remove();
    this.modelDropdown = null;
    this.modeDropdown?.remove();
    this.modeDropdown = null;
    this.wsUnsubscribers.forEach((unsubscribe) => unsubscribe());
    this.wsUnsubscribers = [];
  }

  private renderShell(): void {
    this.root.innerHTML = `
      <div class="orchestration-shell">
        ${renderPrimaryNav({ active: "orchestration", workspaceName: "Echo" })}
        <main class="orchestration-main">
          <header class="orchestration-header">
            <div class="orchestration-header-row">
              <button type="button" class="orchestration-add-button" data-orchestration-add title="New workspace group" aria-label="New workspace group">
                <span class="codicon codicon-add" aria-hidden="true"></span>
              </button>
              <select class="orchestration-group-select" data-orchestration-group-select aria-label="Workspace group" disabled>
                <option value="">No workspace groups yet</option>
              </select>
              <button type="button" class="orchestration-delete-button" data-orchestration-delete-group title="Delete group" aria-label="Delete workspace group" hidden>
                <span class="codicon codicon-trash" aria-hidden="true"></span>
              </button>
            </div>
            <p class="orchestration-subtitle">Queue tasks for a group of workspaces. Each task runs as a Group Worker on its own branch and squash-merges into trunk.</p>
          </header>
          <div class="orchestration-details" data-orchestration-details>
            <p class="orchestration-loading" role="status">Loading workspace groups…</p>
          </div>
          <footer class="orchestration-composer" data-orchestration-composer hidden>
            <form class="chat-composer orchestration-chat-composer" data-orchestration-form>
              <div class="chat-composer-main">
                <div
                  class="chat-composer-editor"
                  contenteditable="true"
                  role="textbox"
                  aria-multiline="true"
                  aria-label="Queue a task for this workspace group"
                  aria-expanded="false"
                  spellcheck="true"
                  data-chat-input
                  data-orchestration-input
                  data-placeholder="Describe the task to queue for this group…"
                ></div>
              </div>
              <div class="chat-composer-toolbar">
                <div class="chat-composer-toolbar-left">
                  <button class="chat-toolbar-icon" type="button" title="Attachments are not supported for queued tasks" aria-label="Attach file" data-orchestration-plus>${icons.plus}</button>
                  <button class="chat-toolbar-icon chat-speech-recognition" type="button" title="Start voice input" aria-label="Start voice input" aria-pressed="false" data-orchestration-voice>${icons.mic}</button>
                  <button class="model-selector chat-toolbar-model" type="button" title="Select model" aria-haspopup="listbox" aria-expanded="false" data-orchestration-model-trigger>
                    <span class="model-selector-label" data-orchestration-model-label>Model</span>
                    <span class="model-selector-chevron">${icons.arrowDown}</span>
                  </button>
                  <button class="model-selector mode-selector chat-toolbar-mode" type="button" title="Agent mode" aria-haspopup="listbox" aria-expanded="false" data-orchestration-mode-trigger>
                    <span class="model-selector-label" data-orchestration-mode-label>Group Worker</span>
                    <span class="model-selector-chevron">${icons.arrowDown}</span>
                  </button>
                </div>
                <div class="chat-composer-toolbar-right">
                  <button class="send-button" type="button" title="Queue task" aria-label="Queue task" data-orchestration-send>${icons.send}</button>
                </div>
              </div>
            </form>
          </footer>
        </main>
        ${renderMobilePrimaryNav({ active: "orchestration" })}
      </div>
    `;
  }

  private bindNavigation(): void {
    const signal = this.abort.signal;
    this.root.querySelectorAll("[data-nav=chat]").forEach((item) => item.addEventListener("click", () => { location.hash = "#/home"; }, { signal }));
    this.root.querySelectorAll("[data-nav=code]").forEach((item) => item.addEventListener("click", () => { location.hash = "#/code"; }, { signal }));
    this.root.querySelectorAll("[data-nav=search]").forEach((item) => item.addEventListener("click", () => { location.hash = "#/code?sidebar=search"; }, { signal }));
    this.root.querySelectorAll("[data-nav=git]").forEach((item) => item.addEventListener("click", () => { location.hash = "#/code?sidebar=git"; }, { signal }));
    this.root.querySelectorAll("[data-nav=debug]").forEach((item) => item.addEventListener("click", () => { location.hash = "#/code?sidebar=debug"; }, { signal }));
    this.root.querySelectorAll("[data-nav=settings]").forEach((item) => item.addEventListener("click", () => { location.hash = "#/settings"; }, { signal }));
    this.root.querySelectorAll("[data-nav=sandbox]").forEach((item) => item.addEventListener("click", () => { location.hash = "#/sandbox"; }, { signal }));
    this.root.querySelectorAll("[data-nav=map]").forEach((item) => item.addEventListener("click", () => { location.hash = "#/map"; }, { signal }));

    this.root.querySelector("[data-orchestration-add]")?.addEventListener("click", () => this.openCreateModal());
    this.root.querySelector("[data-orchestration-group-select]")?.addEventListener("change", (event) => {
      const value = (event.target as HTMLSelectElement).value;
      this.selectedGroupId = value;
      localStorage.setItem(GROUP_STORAGE_KEY, value);
      this.editingTaskId = null;
      this.renderDetails();
    });
    this.root.querySelector("[data-orchestration-delete-group]")?.addEventListener("click", () => {
      void this.confirmDeleteGroup();
    });
  }

  private async loadWorkspaces(): Promise<void> {
    try {
      const data = await get("/api/workspaces", { signal: this.abort.signal }) as { workspaces?: Array<{ id: string; name: string }> };
      if (this.disposed) return;
      this.workspaces = (data.workspaces || []).map((workspace) => ({ id: workspace.id, name: workspace.name }));
    } catch {
      if (!this.disposed) this.workspaces = [];
    }
  }

  private async loadEndpoints(): Promise<void> {
    try {
      const data = await get("/api/settings", { signal: this.abort.signal }) as SettingsResponse;
      if (this.disposed) return;
      this.endpoints = (data.endpoints || [])
        .map((endpoint) => ({
          model: (endpoint.model || "").trim(),
          name: (endpoint.name || "").trim(),
          temperature: Number(endpoint.temperature),
        }))
        .filter((endpoint) => endpoint.model);
      this.syncModelLabel();
      this.syncTemperatureToEndpoint();
    } catch {
      // Model list is optional; the workspace default still works.
    }
  }

  // syncTemperatureToEndpoint seeds the temperature slider from the selected
  // (or first) endpoint's configured temperature, like the chat composer does.
  private syncTemperatureToEndpoint(): void {
    const active = this.endpoints.find((endpoint) => endpoint.model === this.selectedModel)
      || this.endpoints[0];
    const temperature = Number(active?.temperature);
    if (Number.isFinite(temperature)) {
      this.selectedTemperature = Math.min(1, Math.max(0, Math.round(temperature * 20) / 20));
    }
    this.syncTemperatureControl();
  }

  private composerInput(): HTMLDivElement | null {
    return this.root.querySelector<HTMLDivElement>("[data-orchestration-input]");
  }

  // initComposer wires the chat-composer-style controls: model dropdown, locked
  // agent-mode dropdown with thinking/temperature sliders, voice input, and the
  // send button. Dropdowns are fixed-position overlays appended to <body> so
  // they are not clipped by the page's scroll containers.
  private initComposer(): void {
    const modelTrigger = this.root.querySelector<HTMLButtonElement>("[data-orchestration-model-trigger]");
    const modeTrigger = this.root.querySelector<HTMLButtonElement>("[data-orchestration-mode-trigger]");
    const sendButton = this.root.querySelector<HTMLButtonElement>("[data-orchestration-send]");
    const plusButton = this.root.querySelector<HTMLButtonElement>("[data-orchestration-plus]");
    const voiceButton = this.root.querySelector<HTMLButtonElement>("[data-orchestration-voice]");
    const input = this.composerInput();
    const signal = this.abort.signal;

    // Model dropdown.
    const modelDropdown = document.createElement("div");
    modelDropdown.className = "model-dropdown";
    modelDropdown.hidden = true;
    modelDropdown.innerHTML = `
      <div class="model-dropdown-header">Model</div>
      <div class="model-dropdown-list" role="listbox" aria-label="Select model" data-orchestration-model-list></div>
    `;
    document.body.appendChild(modelDropdown);
    this.modelDropdown = modelDropdown;
    const modelList = modelDropdown.querySelector<HTMLElement>("[data-orchestration-model-list]")!;

    const positionModelDropdown = () => {
      const rect = modelTrigger?.getBoundingClientRect();
      if (!rect) return;
      const height = modelDropdown.offsetHeight || 240;
      modelDropdown.style.top = `${Math.max(8, rect.top - height - 6)}px`;
      modelDropdown.style.left = `${rect.left}px`;
      const width = modelDropdown.offsetWidth || 220;
      const overflow = rect.left + width - window.innerWidth + 8;
      if (overflow > 0) modelDropdown.style.left = `${Math.max(8, rect.left - overflow)}px`;
    };
    const closeModelDropdown = () => {
      modelDropdown.hidden = true;
      modelTrigger?.setAttribute("aria-expanded", "false");
    };
    const renderModelOptions = () => {
      const options = [{ model: "", label: "Workspace default" }, ...this.endpoints.map((endpoint) => ({
        model: endpoint.model,
        label: endpoint.name ? `${endpoint.name} — ${endpoint.model}` : endpoint.model,
      }))];
      modelList.innerHTML = options.map((option) => `
        <button type="button" role="option" class="model-dropdown-item ${this.selectedModel === option.model ? "is-selected" : ""}" data-orchestration-model-value="${escapeHTML(option.model)}">
          ${escapeHTML(option.label)}
        </button>
      `).join("");
    };
    modelTrigger?.addEventListener("click", (event) => {
      event.stopPropagation();
      this.closeModeDropdown();
      if (modelDropdown.hidden) {
        renderModelOptions();
        modelDropdown.hidden = false;
        positionModelDropdown();
        modelTrigger.setAttribute("aria-expanded", "true");
      } else {
        closeModelDropdown();
      }
    }, { signal });
    modelList.addEventListener("click", (event) => {
      const button = (event.target as HTMLElement).closest<HTMLButtonElement>("[data-orchestration-model-value]");
      if (!button) return;
      this.selectedModel = button.dataset.orchestrationModelValue || "";
      this.syncModelLabel();
      this.syncTemperatureToEndpoint();
      closeModelDropdown();
    }, { signal });

    // Agent-mode dropdown: Group Worker is the only selectable mode; the rest
    // are listed grayed out. Includes the same thinking/temperature sliders as
    // the chat composer's mode popup.
    const modeDropdown = document.createElement("div");
    modeDropdown.className = "model-dropdown mode-dropdown";
    modeDropdown.hidden = true;
    modeDropdown.innerHTML = `
      <div class="model-dropdown-header">Agent mode</div>
      <div class="model-dropdown-list" role="listbox" aria-label="Select agent mode" data-orchestration-mode-list>
        ${lockedModeOptions.map((mode) => `
          <button type="button" role="option" class="model-dropdown-item mode-dropdown-item ${mode.id === "group-worker" ? "is-selected" : "is-locked"}" data-orchestration-mode-id="${mode.id}" ${mode.id === "group-worker" ? "" : "disabled"}>
            <span>${escapeHTML(mode.name)}</span>
            <small>${mode.id === "group-worker" ? "Branches and squash-merges queued tasks" : "Unavailable for queued tasks"}</small>
          </button>
        `).join("")}
      </div>
      <div class="mode-dropdown-thinking">
        <div class="mode-dropdown-thinking-head">
          <span>Thinking</span>
          <output data-orchestration-thinking-label>Default</output>
        </div>
        <input type="range" min="0" max="6" step="1" value="3"
          aria-label="Thinking level" aria-valuetext="Default"
          data-orchestration-thinking-slider />
      </div>
      <div class="mode-dropdown-thinking mode-dropdown-temperature">
        <div class="mode-dropdown-thinking-head">
          <span>Temperature</span>
          <output data-orchestration-temperature-label>0.60</output>
        </div>
        <input type="range" min="0" max="1" step="0.05" value="0.6"
          aria-label="Temperature"
          data-orchestration-temperature-slider />
      </div>
    `;
    document.body.appendChild(modeDropdown);
    this.modeDropdown = modeDropdown;
    const thinkingSlider = modeDropdown.querySelector<HTMLInputElement>("[data-orchestration-thinking-slider]");
    const thinkingLabel = modeDropdown.querySelector<HTMLElement>("[data-orchestration-thinking-label]");
    const temperatureSlider = modeDropdown.querySelector<HTMLInputElement>("[data-orchestration-temperature-slider]");
    const temperatureLabel = modeDropdown.querySelector<HTMLElement>("[data-orchestration-temperature-label]");

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
    modeTrigger?.addEventListener("click", (event) => {
      event.stopPropagation();
      closeModelDropdown();
      if (modeDropdown.hidden) {
        modeDropdown.hidden = false;
        positionModeDropdown();
        modeTrigger.setAttribute("aria-expanded", "true");
      } else {
        this.closeModeDropdown();
      }
    }, { signal });
    // Clicks inside the dropdown (sliders, disabled rows) must not close it.
    modeDropdown.addEventListener("click", (event) => event.stopPropagation(), { signal });

    const syncThinkingControl = () => {
      const position = thinkingPositionForValue(this.selectedThinking);
      const step = thinkingLadder.find((entry) => entry.position === position);
      if (thinkingSlider) {
        thinkingSlider.value = String(position);
        thinkingSlider.setAttribute("aria-valuetext", step?.label || "Default");
      }
      if (thinkingLabel) thinkingLabel.textContent = step?.label || "Default";
    };
    const syncTemperatureControl = () => {
      if (temperatureSlider) temperatureSlider.value = String(this.selectedTemperature);
      if (temperatureLabel) temperatureLabel.textContent = this.selectedTemperature.toFixed(2);
    };
    this.syncThinkingControl = syncThinkingControl;
    this.syncTemperatureControl = syncTemperatureControl;

    thinkingSlider?.addEventListener("input", () => {
      const position = Number(thinkingSlider.value) || 0;
      const step = thinkingLadder.find((entry) => entry.position === position);
      if (!step) return;
      this.selectedThinking = step.value;
      if (thinkingLabel) thinkingLabel.textContent = step.label;
      thinkingSlider.setAttribute("aria-valuetext", step.label);
    }, { signal });
    temperatureSlider?.addEventListener("input", () => {
      const value = Number(temperatureSlider.value);
      if (!Number.isFinite(value)) return;
      this.selectedTemperature = Math.min(1, Math.max(0, Math.round(value * 20) / 20));
      if (temperatureLabel) temperatureLabel.textContent = this.selectedTemperature.toFixed(2);
    }, { signal });
    syncThinkingControl();
    syncTemperatureControl();

    plusButton?.addEventListener("click", () => {
      toast("Attachments are not supported for queued tasks.");
    }, { signal });

    if (voiceButton && input) {
      this.speech = mountSpeechRecognition({
        button: voiceButton,
        input,
        onError: (message) => toast(message, { sticky: true }),
      });
    }

    sendButton?.addEventListener("click", () => {
      void this.submitTask();
    }, { signal });
    input?.addEventListener("keydown", (event) => {
      if (event.key === "Enter" && !event.shiftKey && !event.isComposing) {
        event.preventDefault();
        void this.submitTask();
      }
    }, { signal });

    // Close open dropdowns on outside clicks, like the chat composer does.
    document.addEventListener("click", () => {
      closeModelDropdown();
      this.closeModeDropdown();
    }, { signal });
    window.addEventListener("resize", () => {
      closeModelDropdown();
      this.closeModeDropdown();
    }, { signal });
  }

  private closeModeDropdown(): void {
    if (!this.modeDropdown) return;
    this.modeDropdown.hidden = true;
    this.root.querySelector("[data-orchestration-mode-trigger]")?.setAttribute("aria-expanded", "false");
  }

  private syncModelLabel(): void {
    const label = this.root.querySelector<HTMLElement>("[data-orchestration-model-label]");
    if (!label) return;
    if (!this.selectedModel) {
      label.textContent = "Model";
      return;
    }
    const endpoint = this.endpoints.find((candidate) => candidate.model === this.selectedModel);
    label.textContent = endpoint?.name || this.selectedModel;
  }

  private syncThinkingControl: () => void = () => {};
  private syncTemperatureControl: () => void = () => {};

  private async refreshGroups(): Promise<void> {
    if (this.disposed) return;
    try {
      const data = await get("/api/workspace-groups", { signal: this.abort.signal }) as { groups?: WorkspaceGroup[] };
      if (this.disposed) return;
      this.groups = data.groups || [];
    } catch {
      if (this.disposed) return;
      this.groups = [];
    }
    if (this.selectedGroupId && !this.groups.some((group) => group.id === this.selectedGroupId)) {
      this.selectedGroupId = "";
      localStorage.removeItem(GROUP_STORAGE_KEY);
    }
    if (!this.selectedGroupId && this.groups.length) {
      this.selectedGroupId = this.groups[0].id;
      localStorage.setItem(GROUP_STORAGE_KEY, this.selectedGroupId);
    }
    this.renderGroupSelect();
    this.renderDetails();
  }

  private selectedGroup(): WorkspaceGroup | null {
    return this.groups.find((group) => group.id === this.selectedGroupId) || null;
  }

  private renderGroupSelect(): void {
    const select = this.root.querySelector<HTMLSelectElement>("[data-orchestration-group-select]");
    const deleteButton = this.root.querySelector<HTMLElement>("[data-orchestration-delete-group]");
    const composer = this.root.querySelector<HTMLElement>("[data-orchestration-composer]");
    if (!select) return;
    select.replaceChildren();
    if (!this.groups.length) {
      const option = document.createElement("option");
      option.value = "";
      option.textContent = "No workspace groups yet";
      select.append(option);
      select.disabled = true;
    } else {
      select.disabled = false;
      for (const group of this.groups) {
        const option = document.createElement("option");
        option.value = group.id;
        option.textContent = `${group.name} (${group.workspaceIds.length})`;
        option.selected = group.id === this.selectedGroupId;
        select.append(option);
      }
    }
    if (deleteButton) deleteButton.hidden = !this.selectedGroup();
    if (composer) composer.hidden = !this.selectedGroup();
  }

  private renderDetails(): void {
    const container = this.root.querySelector<HTMLElement>("[data-orchestration-details]");
    if (!container) return;
    const group = this.selectedGroup();
    container.replaceChildren();
    if (!group) {
      const empty = document.createElement("p");
      empty.className = "orchestration-empty";
      empty.textContent = this.workspaces.length
        ? "Create a workspace group with the [+] button to start queueing tasks."
        : "Register a workspace first, then create a workspace group here.";
      container.append(empty);
      this.mapView?.dispose();
      this.mapView = null;
      return;
    }

    const pendingSection = document.createElement("section");
    pendingSection.className = "orchestration-pending";
    const pendingHeader = document.createElement("header");
    pendingHeader.className = "orchestration-section-header";
    const heading = document.createElement("h2");
    heading.textContent = "Pending Tasks";
    const count = document.createElement("span");
    count.className = "orchestration-section-count";
    count.textContent = String((group.pendingTasks || []).length);
    pendingHeader.append(heading, count);
    pendingSection.append(pendingHeader);

    const tasks = group.pendingTasks || [];
    if (!tasks.length) {
      const empty = document.createElement("p");
      empty.className = "orchestration-section-empty";
      empty.textContent = "No queued tasks. Type one in the chat box below.";
      pendingSection.append(empty);
    } else {
      const list = document.createElement("ul");
      list.className = "orchestration-task-list";
      for (const task of tasks) list.append(this.renderTaskRow(group, task));
      pendingSection.append(list);
    }
    container.append(pendingSection);

    const cardsSection = document.createElement("section");
    cardsSection.className = "orchestration-cards";
    const cardsHeader = document.createElement("header");
    cardsHeader.className = "orchestration-section-header";
    const cardsHeading = document.createElement("h2");
    cardsHeading.textContent = "Group Activity";
    cardsHeader.append(cardsHeading);
    cardsSection.append(cardsHeader);
    const cardsHost = document.createElement("div");
    cardsHost.className = "orchestration-cards-host";
    cardsSection.append(cardsHost);
    container.append(cardsSection);

    const memberIds = new Set(group.workspaceIds);
    this.mapView?.dispose();
    this.mapView = new EchoMapView(cardsHost, {
      embed: true,
      workspaceFilter: (chat: EchoMapChat) => memberIds.has(chat.workspaceId),
    });
    void this.mapView.start();
  }

  private renderTaskRow(group: WorkspaceGroup, task: PendingTask): HTMLElement {
    const row = document.createElement("li");
    row.className = "orchestration-task";
    row.dataset.orchestrationTask = task.id;
    if (this.editingTaskId === task.id) return this.renderTaskEditor(group, task, row);

    const checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.className = "orchestration-task-ready";
    checkbox.checked = task.ready;
    checkbox.disabled = Boolean(task.dispatchedWorkspaceId);
    checkbox.title = task.ready
      ? "Ready for pickup — uncheck to hold the task in the queue"
      : "Check when the task is ready to be picked up";
    checkbox.addEventListener("change", () => {
      void this.updateTask(group.id, task.id, { ready: checkbox.checked });
    });

    const body = document.createElement("div");
    body.className = "orchestration-task-body";
    const prompt = document.createElement("p");
    prompt.className = "orchestration-task-prompt";
    prompt.textContent = task.prompt;
    body.append(prompt);

    const chips = document.createElement("div");
    chips.className = "orchestration-task-chips";
    const mode = document.createElement("span");
    mode.className = "orchestration-chip";
    mode.textContent = "Group Worker";
    chips.append(mode);
    if (task.model) {
      const model = document.createElement("span");
      model.className = "orchestration-chip";
      model.textContent = task.model;
      chips.append(model);
    }
    if (task.reasoningEffort) {
      const effort = document.createElement("span");
      effort.className = "orchestration-chip";
      effort.textContent = `reasoning: ${task.reasoningEffort}`;
      chips.append(effort);
    }
    if (task.temperature !== null && task.temperature !== undefined) {
      const temperature = document.createElement("span");
      temperature.className = "orchestration-chip";
      temperature.textContent = `temp: ${task.temperature}`;
      chips.append(temperature);
    }
    if (task.dispatchedWorkspaceId) {
      const workspace = group.workspaces.find((ref) => ref.id === task.dispatchedWorkspaceId);
      const dispatched = document.createElement("span");
      dispatched.className = "orchestration-chip orchestration-chip-dispatched";
      dispatched.textContent = `Picked up by ${workspace?.name || task.dispatchedWorkspaceId}`;
      chips.append(dispatched);
    } else if (!task.ready) {
      const held = document.createElement("span");
      held.className = "orchestration-chip orchestration-chip-held";
      held.textContent = "Held — not ready";
      chips.append(held);
    }
    body.append(chips);
    row.append(checkbox, body);

    if (!task.dispatchedWorkspaceId) {
      const actions = document.createElement("div");
      actions.className = "orchestration-task-actions";
      const edit = document.createElement("button");
      edit.type = "button";
      edit.className = "orchestration-task-action";
      edit.title = "Edit task";
      edit.setAttribute("aria-label", "Edit task");
      edit.innerHTML = '<span class="codicon codicon-edit" aria-hidden="true"></span>';
      edit.addEventListener("click", () => {
        this.editingTaskId = task.id;
        this.renderDetails();
      });
      const remove = document.createElement("button");
      remove.type = "button";
      remove.className = "orchestration-task-action orchestration-task-delete";
      remove.title = "Delete task";
      remove.setAttribute("aria-label", "Delete task");
      remove.innerHTML = '<span class="codicon codicon-close" aria-hidden="true"></span>';
      remove.addEventListener("click", () => {
        void this.deleteTask(group.id, task.id);
      });
      actions.append(edit, remove);
      row.append(actions);
    }
    return row;
  }

  private renderTaskEditor(group: WorkspaceGroup, task: PendingTask, row: HTMLElement): HTMLElement {
    row.classList.add("is-editing");
    const form = document.createElement("div");
    form.className = "orchestration-task-editor";

    const promptLabel = document.createElement("label");
    promptLabel.className = "orchestration-field";
    const promptText = document.createElement("span");
    promptText.textContent = "Prompt";
    const promptInput = document.createElement("textarea");
    promptInput.rows = 3;
    promptInput.value = task.prompt;
    promptLabel.append(promptText, promptInput);

    const modelLabel = document.createElement("label");
    modelLabel.className = "orchestration-field";
    const modelText = document.createElement("span");
    modelText.textContent = "Model";
    const modelSelect = document.createElement("select");
    const defaultOption = document.createElement("option");
    defaultOption.value = "";
    defaultOption.textContent = "Workspace default";
    modelSelect.append(defaultOption);
    for (const endpoint of this.endpoints) {
      const option = document.createElement("option");
      option.value = endpoint.model;
      option.textContent = endpoint.name ? `${endpoint.name} — ${endpoint.model}` : endpoint.model;
      option.selected = endpoint.model === (task.model || "");
      modelSelect.append(option);
    }
    modelLabel.append(modelText, modelSelect);

    const temperatureLabel = document.createElement("label");
    temperatureLabel.className = "orchestration-field";
    const temperatureText = document.createElement("span");
    temperatureText.textContent = "Temperature";
    const temperatureInput = document.createElement("input");
    temperatureInput.type = "number";
    temperatureInput.min = "0";
    temperatureInput.max = "1";
    temperatureInput.step = "0.05";
    temperatureInput.placeholder = "Auto";
    if (task.temperature !== null && task.temperature !== undefined) {
      temperatureInput.value = String(task.temperature);
    }
    temperatureLabel.append(temperatureText, temperatureInput);

    const actions = document.createElement("div");
    actions.className = "orchestration-editor-actions";
    const cancel = document.createElement("button");
    cancel.type = "button";
    cancel.className = "secondary-button compact-button";
    cancel.textContent = "Cancel";
    cancel.addEventListener("click", () => {
      this.editingTaskId = null;
      this.renderDetails();
    });
    const save = document.createElement("button");
    save.type = "button";
    save.className = "primary-button compact-button";
    save.textContent = "Save";
    save.addEventListener("click", () => {
      const prompt = promptInput.value.trim();
      if (!prompt) {
        toast("Task prompt is required.");
        return;
      }
      const rawTemperature = temperatureInput.value.trim();
      const update: Record<string, unknown> = { prompt, model: modelSelect.value };
      if (rawTemperature === "") update.temperature = null;
      else {
        const parsed = Number(rawTemperature);
        if (Number.isNaN(parsed) || parsed < 0 || parsed > 1) {
          toast("Temperature must be between 0 and 1.");
          return;
        }
        update.temperature = parsed;
      }
      void this.updateTask(group.id, task.id, update);
    });
    actions.append(cancel, save);

    form.append(promptLabel, modelLabel, temperatureLabel, actions);
    return form;
  }

  private async submitTask(): Promise<void> {
    const group = this.selectedGroup();
    if (!group) return;
    const input = this.composerInput();
    if (!input) return;
    const prompt = (input.textContent || "").trim();
    if (!prompt) {
      toast("Describe the task before queueing it.");
      return;
    }
    const body: Record<string, unknown> = {
      prompt,
      ready: true,
      temperature: this.selectedTemperature,
    };
    if (this.selectedModel) body.model = this.selectedModel;
    if (this.selectedThinking) body.reasoningEffort = this.selectedThinking;
    try {
      await post(`/api/workspace-groups/${encodeURIComponent(group.id)}/tasks`, body);
      input.textContent = "";
      await this.refreshGroups();
    } catch (error) {
      toast(error instanceof Error ? error.message : String(error), { sticky: true });
    }
  }

  private async updateTask(groupID: string, taskID: string, body: Record<string, unknown>): Promise<void> {
    try {
      await put(`/api/workspace-groups/${encodeURIComponent(groupID)}/tasks/${encodeURIComponent(taskID)}`, body);
      this.editingTaskId = null;
      await this.refreshGroups();
    } catch (error) {
      toast(error instanceof Error ? error.message : String(error), { sticky: true });
    }
  }

  private async deleteTask(groupID: string, taskID: string): Promise<void> {
    try {
      await del(`/api/workspace-groups/${encodeURIComponent(groupID)}/tasks/${encodeURIComponent(taskID)}`);
      await this.refreshGroups();
    } catch (error) {
      toast(error instanceof Error ? error.message : String(error), { sticky: true });
    }
  }

  private async confirmDeleteGroup(): Promise<void> {
    const group = this.selectedGroup();
    if (!group) return;
    const dialog = document.createElement("dialog");
    dialog.className = "settings-confirm-dialog orchestration-confirm-dialog";
    dialog.innerHTML = `
      <form method="dialog">
        <h2>Delete "${escapeHTML(group.name)}"?</h2>
        <p>The group and its queued pending tasks will be removed. Workspaces are not touched.</p>
        <div class="settings-confirm-actions">
          <button class="secondary-button" type="button" data-choice="cancel">Cancel</button>
          <button class="secondary-button danger-button" type="button" data-choice="confirm">Delete group</button>
        </div>
      </form>
    `;
    let finished = false;
    const finish = (confirmed: boolean) => {
      if (finished) return;
      finished = true;
      dialog.close?.();
      dialog.remove();
      if (confirmed) void this.deleteGroup(group.id);
    };
    dialog.querySelector("[data-choice='cancel']")?.addEventListener("click", () => finish(false));
    dialog.querySelector("[data-choice='confirm']")?.addEventListener("click", () => finish(true));
    dialog.addEventListener("cancel", (event) => {
      event.preventDefault();
      finish(false);
    }, { once: true });
    document.body.append(dialog);
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
  }

  private async deleteGroup(groupID: string): Promise<void> {
    try {
      await del(`/api/workspace-groups/${encodeURIComponent(groupID)}`);
      if (this.selectedGroupId === groupID) {
        this.selectedGroupId = "";
        localStorage.removeItem(GROUP_STORAGE_KEY);
      }
      await this.refreshGroups();
    } catch (error) {
      toast(error instanceof Error ? error.message : String(error), { sticky: true });
    }
  }

  private openCreateModal(): void {
    const dialog = document.createElement("dialog");
    dialog.className = "orchestration-modal";
    dialog.innerHTML = `
      <form method="dialog" class="orchestration-modal-form">
        <h2>New workspace group</h2>
        <label class="orchestration-field">
          <span>Name</span>
          <input type="text" data-orchestration-new-name placeholder="e.g. Frontend services" maxlength="80" />
        </label>
        <fieldset class="orchestration-field">
          <span>Workspaces (select at least one)</span>
          <div class="orchestration-workspace-list" data-orchestration-new-workspaces></div>
        </fieldset>
        <p class="orchestration-modal-error" data-orchestration-new-error hidden></p>
        <div class="orchestration-editor-actions">
          <button type="button" class="secondary-button" data-orchestration-new-cancel>Cancel</button>
          <button type="button" class="primary-button" data-orchestration-new-create>Create group</button>
        </div>
      </form>
    `;
    const list = dialog.querySelector<HTMLElement>("[data-orchestration-new-workspaces]");
    if (list) {
      if (!this.workspaces.length) {
        const empty = document.createElement("p");
        empty.className = "orchestration-section-empty";
        empty.textContent = "No workspaces are registered yet.";
        list.append(empty);
      }
      for (const workspace of this.workspaces) {
        const label = document.createElement("label");
        label.className = "orchestration-workspace-option";
        const checkbox = document.createElement("input");
        checkbox.type = "checkbox";
        checkbox.value = workspace.id;
        label.append(checkbox, document.createTextNode(workspace.name));
        list.append(label);
      }
    }
    const finish = () => {
      dialog.close?.();
      dialog.remove();
    };
    dialog.querySelector("[data-orchestration-new-cancel]")?.addEventListener("click", finish);
    dialog.querySelector("[data-orchestration-new-create]")?.addEventListener("click", () => {
      void this.submitCreateModal(dialog, finish);
    });
    document.body.append(dialog);
    if (typeof dialog.showModal === "function") dialog.showModal();
    else dialog.setAttribute("open", "");
    dialog.querySelector<HTMLInputElement>("[data-orchestration-new-name]")?.focus();
  }

  private async submitCreateModal(dialog: HTMLDialogElement, finish: () => void): Promise<void> {
    const name = (dialog.querySelector<HTMLInputElement>("[data-orchestration-new-name]")?.value || "").trim();
    const ids = [...dialog.querySelectorAll<HTMLInputElement>("[data-orchestration-new-workspaces] input:checked")]
      .map((checkbox) => checkbox.value);
    const error = dialog.querySelector<HTMLElement>("[data-orchestration-new-error]");
    const showError = (message: string) => {
      if (!error) return;
      error.textContent = message;
      error.hidden = false;
    };
    if (!name) {
      showError("A group name is required.");
      return;
    }
    if (!ids.length) {
      showError("Select at least one workspace.");
      return;
    }
    try {
      const data = await post("/api/workspace-groups", { name, workspaceIds: ids }) as { group?: WorkspaceGroup };
      finish();
      if (data.group?.id) {
        this.selectedGroupId = data.group.id;
        localStorage.setItem(GROUP_STORAGE_KEY, this.selectedGroupId);
      }
      await this.refreshGroups();
    } catch (err) {
      showError(err instanceof Error ? err.message : String(err));
    }
  }
}

let currentView: OrchestrationView | null = null;

export function mount(el: HTMLElement): void {
  currentView?.dispose();
  currentView = new OrchestrationView(el);
  void currentView.start();
}

export function unmount(): void {
  currentView?.dispose();
  currentView = null;
}
