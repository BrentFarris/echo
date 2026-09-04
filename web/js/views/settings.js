// views/settings.js — Echo settings screen.
//
// A redesigned, full-page settings view (not the old modal overlay). Sections
// are selected from a left sidebar and their controls render on the right.
//
// LLM Endpoints and Agent Modes are functional. The remaining sections are
// incrementally replacing the earlier visual stubs.

import { icons } from "../icons.js";
import { del, get, post, put } from "../api.js";
import { logout } from "../../src/auth/authGate.ts";
import { hasDirtySessions } from "../../src/code/persistence.ts";
import { installChatMap } from "../../src/chatMap.ts";
import { getEchoUpdateSnapshot, refreshEchoUpdateStatus, syncEchoUpdateBadges } from "../../src/echoUpdate.ts";
import { chatTargetRouteHash, codeRouteHash, navigateBackFromSettings } from "../../src/navigation.ts";
import { renderMobilePrimaryNav } from "../../src/primaryNav.ts";
import { reloadForReplacementServer, waitForReplacementServer } from "../../src/rebuildRelaunch.ts";
import {
  openAddWorkspaceModal, openEditWorkspaceModal, openWorkspaceDropdown,
  renderWorkspaceIcon, unregisterWorkspace,
} from "../workspaces.js";
import { refreshPluginCatalog } from "../../src/plugins/catalog.ts";
import {
  completionNotificationPermission, requestCompletionNotificationPermission,
  updateCompletionNotificationSettings,
} from "../../src/completionNotifications.ts";
import {
  requestPlanQuestionNotificationPermission,
  updatePlanQuestionNotificationSettings,
} from "../../src/planQuestionNotifications.ts";

let mountedRoot = null;
let closeSettingsWorkspaceDropdown = null;
let closeSettingsAddWorkspaceModal = null;
let closeSettingsEditWorkspaceModal = null;
let disposeSettingsChatMap = null;
let pluginCatalogListener = null;
let updateStatusListener = null;

// ---- Theme token table (matches OLD theme.ts, carried into the new SPA) ----
const themeTokens = [
  { key: "background", label: "Background", group: "Base", light: "#f7f3f1", dark: "#121214" },
  { key: "surface", label: "Surface", group: "Base", light: "#fffdfa", dark: "#1b1b1e" },
  { key: "surfaceMuted", label: "Muted Surface", group: "Base", light: "#eee6e3", dark: "#252429" },
  { key: "border", label: "Border", group: "Base", light: "#d8ccc8", dark: "#343139" },
  { key: "text", label: "Text", group: "Text", light: "#241f1f", dark: "#f3eeee" },
  { key: "textMuted", label: "Muted Text", group: "Text", light: "#6f6360", dark: "#b7aaab" },
  { key: "accent", label: "Accent", group: "Action", light: "#2563eb", dark: "#60a5fa" },
  { key: "accentStrong", label: "Strong Accent", group: "Action", light: "#1d4ed8", dark: "#93bbfd" },
  { key: "onAccent", label: "On Accent", group: "Action", light: "#ffffff", dark: "#ffffff" },
  { key: "danger", label: "Danger", group: "Status", light: "#b42332", dark: "#ff6677" },
  { key: "success", label: "Success", group: "Status", light: "#1a7f37", dark: "#3fb950" },
  { key: "info", label: "Info", group: "Status", light: "#3c82e6", dark: "#58a6ff" },
  { key: "warning", label: "Warning", group: "Status", light: "#9a6700", dark: "#d29922" },
];
const themeGroups = ["Base", "Text", "Action", "Status"];

// ---- Editor font size helpers ----
const minEditorFontSize = 8;
const maxEditorFontSize = 30;
const defaultEditorFontSize = 13.5;

function clampEditorFontSize(value) {
  if (!Number.isFinite(value) || value <= 0) return defaultEditorFontSize;
  return Math.min(maxEditorFontSize, Math.max(minEditorFontSize, value));
}

// ---- Sections ----
const sections = [
  { id: "llm", label: "LLM Endpoints", icon: icons.settings },
  { id: "modes", label: "Agent Modes", icon: icons.tasks },
  { id: "plugins", label: "Plugins", icon: icons.dashboard },
  { id: "external", label: "External Connections", icon: icons.git },
  { id: "messaging", label: "Messaging", icon: icons.mic },
  { id: "git", label: "Git", icon: icons.git },
  { id: "lsp", label: "Language Servers", icon: icons.code },
  { id: "testing", label: "Testing", icon: icons.execute },
  { id: "theme", label: "Theme", icon: icons.dashboard },
  { id: "workspaces", label: "Workspaces", icon: icons.code },
  { id: "security", label: "Security", icon: icons.settings },
  { id: "development", label: "Development", icon: icons.execute },
];

// Routing topics for endpoint selection.
const routingTopics = [
  { key: "chat", label: "Chat" },
  { key: "research", label: "Research" },
  { key: "vision", label: "Vision" },
  { key: "inlineCode", label: "Inline Code" },
];

const reasoningEffortOptions = [
  { value: "", label: "Provider default / token budget" },
  { value: "none", label: "None" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "xhigh", label: "XHigh" },
  { value: "max", label: "Max" },
];

// ---- State ----
const state = {
  activeSection: "llm",
  themePalette: "light",
  editorFontSize: 13.5,
  endpoints: [],
  routing: {
    chat: "",
    research: "",
    vision: "",
    inlineCode: "",
  },
  // Endpoint currently being edited (id) or null.
  editingEndpointId: null,
  modes: [],
  modeTools: [],
  modeWorkspaceId: "",
  modeWorkspaceName: "",
  editingModeId: null,
  modeDraft: null,
  modeStatus: "",
  workspaces: [],
  workspaceStatus: "",
  // External connection settings (SearXNG + ComfyUI).
  external: {
    searxngUrl: "",
    comfyuiUrl: "",
    comfyuiTxt2imgWorkflow: "",
    comfyuiImg2imgWorkflow: "",
    comfyuiVideoWorkflow: "",
  },
  rawSettings: {},
  settingsLoaded: false,
  researchAgentConcurrency: 4,
  git: {
    leadingWhitespaceIndicators: true,
    splitDiffView: true,
  },
  messaging: {
    notificationSounds: true,
    planQuestionSounds: true,
    planQuestionNotifications: true,
    chatCompletionNotifications: true,
  },
  storagePath: "",
  saveStatus: "",
  authSessions: [],
  transportSecure: false,
  securityStatus: "",
  rebuild: {
    running: false,
    status: "",
    logPath: "",
  },
  terminate: {
    running: false,
    status: "",
  },
  update: {
    available: false,
    checking: false,
    checkError: "",
    running: false,
    status: "",
    logPath: "",
  },
  plugins: {
    catalog: { safeMode: false, plugins: [], stages: [], missing: [], conflicts: [], retained: [] },
    sourceType: "local",
    localPath: "",
    repository: "",
    ref: "",
    subdirectory: "",
    status: "",
    busy: false,
    logs: {},
  },
  lsp: {
    profiles: [],
    templates: [],
    config: { enabledProfileIds: [], overrides: {}, formatOnSave: false, formatOnSaveTimeoutMs: 3000 },
    effectiveProfiles: [],
    statuses: [],
    editingId: null,
    draft: null,
    overridesText: "{}",
    status: "",
    busy: false,
  },
  testing: {
    config: { codeLens: true, coverage: true, timeout: "30s", flags: [], tags: "", environment: {} },
    flagsText: "[]",
    environmentText: "{}",
    status: "",
    busy: false,
  },
};

function esc(value) {
  return String(value ?? "").replace(/[&<>"']/g, (ch) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[ch]));
}

function newEndpoint() {
  return {
    id: `endpoint-${Date.now().toString(36)}`,
    name: "",
    endpoint: "",
    model: "",
    temperature: 0.6,
    topK: 20,
    topP: 0.95,
    minP: 0,
    contextLength: 262144,
    maxTokens: 2048,
    frequencyPenalty: 0,
    presencePenalty: 1.5,
    repetitionPenalty: 1.05,
    timeoutSeconds: 600,
    streamIdleTimeoutSeconds: 600,
    thinkingTokenBudget: -1,
    reasoningEffort: "max",
    thinkingCorrection: false,
    contextCompressionEnabled: true,
    contextCompressionThresholdPercent: 70,
    systemPromptAppendage: "",
    headers: {},
  };
}

// ---- LLM Endpoints section ----

function renderLLMEndpoints() {
  const editing = state.endpoints.find((e) => e.id === state.editingEndpointId) || null;
  return `
    <section class="settings-section">
      <div class="settings-section-heading">
        <h2 class="settings-section-title">LLM Endpoints</h2>
        <button class="secondary-button compact-button" type="button" data-action="add-endpoint">${icons.plus}<span>Add</span></button>
      </div>

      <div class="settings-card">
        <h3 class="settings-card-title">Endpoint routing</h3>
        <p class="settings-card-help">Choose which endpoint each interaction type uses.</p>
        <div class="settings-grid">
          ${routingTopics.map((r) => `
            <label class="field">
              <span>${r.label}</span>
              <select data-routing-topic="${r.key}">
                ${state.endpoints.map((e) => `
                  <option value="${esc(e.id)}" ${state.routing[r.key] === e.id ? "selected" : ""}>${esc(e.name || "(unnamed)")}</option>
                `).join("")}
              </select>
            </label>
          `).join("")}
        </div>
      </div>

      <div class="settings-card">
        <h3 class="settings-card-title">Endpoints</h3>
        <div class="endpoint-list">
          ${state.endpoints.map((e) => `
            <div class="endpoint-row">
              <div class="endpoint-row-main">
                <strong>${esc(e.name || "(unnamed)")}</strong>
                <span class="endpoint-row-summary">${esc(e.model || "no model")} — ${esc(e.endpoint || "no endpoint")}</span>
              </div>
              <div class="endpoint-row-actions">
                <button class="icon-button" type="button" title="Edit" aria-label="Edit ${esc(e.name)}" data-action="edit-endpoint" data-endpoint-id="${esc(e.id)}">${icons.settings}</button>
                <button class="icon-button danger-button" type="button" title="Delete" aria-label="Delete ${esc(e.name)}" data-action="delete-endpoint" data-endpoint-id="${esc(e.id)}" ${state.endpoints.length <= 1 ? "disabled" : ""}>${icons.trash}</button>
              </div>
            </div>
          `).join("")}
        </div>
      </div>

      ${editing ? renderEndpointEditor(editing) : ""}

      <div class="settings-card">
        <h3 class="settings-card-title">Research</h3>
        <label class="field">
          <span>Research agent concurrency</span>
          <input type="number" min="0" max="8" step="1" value="${state.researchAgentConcurrency}" data-research-agent-concurrency />
          <span class="field-help">Maximum research agents that may run at once. 0 disables research agents.</span>
        </label>
      </div>
    </section>
  `;
}

function renderEndpointEditor(e) {
  const num = (key, label, opts = {}) => `
    <label class="field">
      <span>${label}</span>
      <input type="number" step="${opts.step ?? "any"}" min="${opts.min ?? ""}" max="${opts.max ?? ""}" value="${e[key]}" data-endpoint-field="${key}" ${opts.disabled ? "disabled" : ""} />
    </label>
  `;
  const range = (key, label, opts = {}) => `
    <label class="field">
      <span>${label} <output class="range-value" data-range-value-for="${key}">${e[key]}</output>%</span>
      <input type="range" min="${opts.min ?? ""}" max="${opts.max ?? ""}" step="${opts.step ?? 1}" value="${e[key]}" data-endpoint-field="${key}" />
    </label>
  `;
  return `
    <div class="settings-card endpoint-editor" data-endpoint-editor>
      <div class="settings-section-heading">
        <h3 class="settings-card-title">Edit Endpoint</h3>
        <div class="endpoint-row-actions">
          <button class="secondary-button compact-button" type="button" data-action="save-endpoint">${icons.check}<span>Save</span></button>
          <button class="icon-button" type="button" title="Close editor" aria-label="Close editor" data-action="close-endpoint-editor">${icons.x}</button>
        </div>
      </div>

      <div class="settings-grid">
        <label class="field">
          <span>Name</span>
          <input type="text" value="${esc(e.name)}" data-endpoint-field="name" placeholder="My endpoint" autocomplete="off" />
        </label>
        <label class="field">
          <span>Model</span>
          <input type="text" value="${esc(e.model)}" data-endpoint-field="model" placeholder="model-name" autocomplete="off" />
        </label>
        <label class="field field-wide">
          <span>Endpoint URL</span>
          <input type="url" value="${esc(e.endpoint)}" data-endpoint-field="endpoint" placeholder="http://host:port/v1" autocomplete="off" />
        </label>
      </div>

      <div class="settings-grid">
        ${num("temperature", "Temperature", { min: 0, max: 2, step: 0.01 })}
        ${num("topK", "Top K", { min: 0, step: 1 })}
        ${num("topP", "Top P", { min: 0, max: 1, step: 0.01 })}
        ${num("minP", "Min P", { min: 0, max: 1, step: 0.01 })}
        ${num("contextLength", "Context Length", { min: 1, step: 1 })}
        ${range("contextCompressionThresholdPercent", "Compression Threshold", { min: 1, max: 99, step: 1 })}
        ${num("maxTokens", "Max Tokens", { min: 1, step: 1 })}
        ${num("frequencyPenalty", "Frequency Penalty", { min: -2, max: 2, step: 0.01 })}
        ${num("presencePenalty", "Presence Penalty", { min: -2, max: 2, step: 0.01 })}
        ${num("repetitionPenalty", "Repetition Penalty", { min: 0, step: 0.01 })}
        ${num("timeoutSeconds", "Request Timeout (seconds)", { min: 1, step: 1 })}
        ${num("streamIdleTimeoutSeconds", "Stream Idle Timeout (seconds)", { min: -1, step: 1 })}
        <label class="field">
          <span>Reasoning Effort</span>
          <select data-endpoint-field="reasoningEffort">
            ${reasoningEffortOptions.map((option) => `<option value="${option.value}" ${e.reasoningEffort === option.value ? "selected" : ""}>${option.label}</option>`).join("")}
          </select>
        </label>
        ${num("thinkingTokenBudget", "Thinking Token Budget", { min: -1, step: 1, disabled: e.reasoningEffort !== "" })}
      </div>

      <p class="settings-card-help">Stream idle timeout is reset whenever provider data or an SSE heartbeat arrives. Set it to -1 to disable inactivity detection.</p>
      <p class="settings-card-help">A named reasoning effort is sent as <code>reasoning_effort</code> and takes precedence over the local-model thinking-token budget. Unsupported values return the provider's request error without retrying or stepping down.</p>

      <label class="settings-toggle">
        <span>Thinking correction</span>
        <input type="checkbox" ${e.thinkingCorrection ? "checked" : ""} data-endpoint-field="thinkingCorrection" ${e.reasoningEffort === "none" || e.reasoningEffort === "" && Number(e.thinkingTokenBudget) === 0 ? "disabled" : ""} />
      </label>

      <label class="settings-toggle">
        <span>Automatic context compression</span>
        <input type="checkbox" ${e.contextCompressionEnabled !== false ? "checked" : ""} data-endpoint-field="contextCompressionEnabled" />
      </label>
      <p class="settings-card-help">At the configured percentage of the model context window, Echo summarizes safe middle exchanges and keeps the latest work verbatim. The threshold must be between 1% and 99%.</p>

      <label class="field">
        <span>System Prompt Appendage</span>
        <textarea rows="3" data-endpoint-field="systemPromptAppendage" placeholder="Optional extra instructions appended to the system prompt.">${esc(e.systemPromptAppendage)}</textarea>
      </label>

      <label class="field">
        <span>Headers</span>
        <textarea rows="2" data-endpoint-field="headers" placeholder="key: value (one per line)">${esc(headersToText(e.headers))}</textarea>
      </label>
    </div>
  `;
}

function headersToText(headers) {
  if (!headers) return "";
  return Object.entries(headers).map(([k, v]) => `${k}: ${v}`).join("\n");
}

function textToHeaders(text) {
  const headers = {};
  for (const line of (text || "").split("\n")) {
    const idx = line.indexOf(":");
    if (idx === -1) continue;
    const key = line.slice(0, idx).trim();
    const value = line.slice(idx + 1).trim();
    if (key) headers[key] = value;
  }
  return headers;
}

// ---- Other section renderers ----

function renderAgentModes() {
  const draft = state.modeDraft;
  return `
    <section class="settings-section">
      <div class="settings-section-heading">
        <h2 class="settings-section-title">Agent Modes</h2>
        <button class="secondary-button compact-button" type="button" data-action="add-mode" ${state.modeWorkspaceId ? "" : "disabled"}>${icons.plus}<span>Add Mode</span></button>
      </div>
      <p class="settings-card-help">Modes are saved in the active workspace${state.modeWorkspaceName ? `, <strong>${esc(state.modeWorkspaceName)}</strong>` : ""}. A mode changes the system instructions and can limit the tools and paths available to chat.</p>
      ${state.modeStatus ? `<p class="settings-status ${state.modeStatus.startsWith("Error:") ? "is-error" : ""}">${esc(state.modeStatus)}</p>` : ""}
      ${draft ? renderAgentModeEditor(draft) : ""}
      <div class="settings-card">
        <div class="agent-mode-list">
          ${state.modes.length ? state.modes.map((m) => `
            <div class="agent-mode-row">
              <div class="agent-mode-row-main">
                <strong>${esc(m.name)}</strong>
                <span class="agent-mode-row-sub">${m.builtIn ? "Built-in" : esc(m.prompt)}</span>
                <span class="mode-permission-summary">${renderModeSummary(m)}</span>
              </div>
              ${m.builtIn ? "" : `
                <div class="endpoint-row-actions">
                  <button class="icon-button" type="button" title="Edit" data-action="edit-mode" data-mode-id="${esc(m.id)}">${icons.settings}</button>
                  <button class="icon-button danger-button" type="button" title="Delete" data-action="delete-mode" data-mode-id="${esc(m.id)}">${icons.trash}</button>
                </div>
              `}
            </div>
          `).join("") : `<p class="empty-state compact">${state.modeWorkspaceId ? "No modes available." : "Select a workspace to manage its agent modes."}</p>`}
        </div>
      </div>
    </section>
  `;
}

function renderModeSummary(mode) {
  const names = Object.keys(mode.permissions || {});
  if (!names.length) return "All tools";
  return `${names.length} tool${names.length === 1 ? "" : "s"} allowed`;
}

function renderAgentModeEditor(draft) {
  const restricted = draft.restricted;
  return `
    <div class="settings-card agent-mode-editor">
      <div class="settings-section-heading">
        <h3 class="settings-card-title">${state.editingModeId ? "Edit mode" : "New mode"}</h3>
        <button class="icon-button" type="button" title="Close" data-action="cancel-mode">${icons.x}</button>
      </div>
      <label class="field">
        <span>Name</span>
        <input type="text" maxlength="80" value="${esc(draft.name)}" data-mode-field="name" placeholder="Code reviewer" autocomplete="off" />
      </label>
      <label class="field">
        <span>System instructions</span>
        <textarea rows="7" data-mode-field="prompt" placeholder="Describe how this agent should work, what it should prioritize, and any boundaries it should follow.">${esc(draft.prompt)}</textarea>
      </label>
      <label class="settings-toggle mode-tools-toggle">
        <span><strong>Restrict tool access</strong><span class="field-help">Only checked tools will be sent to the model and executable in this mode.</span></span>
        <input type="checkbox" data-mode-field="restricted" ${restricted ? "checked" : ""} />
      </label>
      ${restricted ? `
        <div class="mode-tool-list">
          ${state.modeTools.map((tool) => {
            const permission = draft.permissions[tool.name];
            const checked = Boolean(permission);
            return `
              <div class="mode-tool-row ${checked ? "is-enabled" : ""}">
                <label class="mode-tool-check">
                  <input type="checkbox" data-mode-tool="${esc(tool.name)}" ${checked ? "checked" : ""} />
                  <span><strong>${esc(tool.name)}</strong><small>${esc(tool.description)}</small></span>
                </label>
                ${checked ? `<input class="mode-tool-paths" type="text" data-mode-paths="${esc(tool.name)}" value="${esc((permission.paths || []).join(", "))}" placeholder="All paths, or globs: src/**, tests/**" aria-label="Allowed paths for ${esc(tool.name)}" />` : ""}
              </div>
            `;
          }).join("")}
        </div>
      ` : `<p class="field-help">All registered tools are available without additional path restrictions.</p>`}
      <div class="mode-editor-actions">
        <button class="secondary-button" type="button" data-action="cancel-mode">Cancel</button>
        <button class="primary-button" type="button" data-action="save-mode">${icons.check}<span>Save Mode</span></button>
      </div>
    </div>
  `;
}

function renderExternal() {
  return `
    <section class="settings-section">
      <h2 class="settings-section-title">External Connections</h2>

      <div class="settings-card">
        <h3 class="settings-card-title">SearXNG</h3>
        <p class="settings-card-help">Self-hosted metasearch engine used for web research.</p>
        <label class="field">
          <span>SearXNG URL</span>
          <input type="url" value="${esc(state.external.searxngUrl)}" placeholder="http://localhost:8080/" autocomplete="off" data-external-field="searxngUrl" />
        </label>
      </div>

      <div class="settings-card">
        <h3 class="settings-card-title">ComfyUI</h3>
        <p class="settings-card-help">Remote ComfyUI instance for image and video generation.</p>
        <div class="settings-grid">
          <label class="field field-wide">
            <span>ComfyUI Host URL</span>
            <input type="url" value="${esc(state.external.comfyuiUrl)}" placeholder="http://127.0.0.1:8188" autocomplete="off" data-external-field="comfyuiUrl" />
          </label>
          <label class="field field-wide">
            <span>Txt2img Workflow</span>
            <input type="text" value="${esc(state.external.comfyuiTxt2imgWorkflow)}" placeholder="Path to txt2img workflow JSON" autocomplete="off" data-external-field="comfyuiTxt2imgWorkflow" />
          </label>
          <label class="field field-wide">
            <span>Img2img Workflow</span>
            <input type="text" value="${esc(state.external.comfyuiImg2imgWorkflow)}" placeholder="Path to img2img workflow JSON" autocomplete="off" data-external-field="comfyuiImg2imgWorkflow" />
          </label>
          <label class="field field-wide">
            <span>Video Workflow</span>
            <input type="text" value="${esc(state.external.comfyuiVideoWorkflow)}" placeholder="Path to default video workflow JSON (used by comfyui_generate_video)" autocomplete="off" data-external-field="comfyuiVideoWorkflow" />
          </label>
        </div>
      </div>
    </section>
  `;
}

function renderMessaging() {
  const toggles = [
    { key: "notificationSounds", label: "Notification sounds", checked: state.messaging.notificationSounds },
    { key: "planQuestionSounds", label: "Planning question sounds", checked: state.messaging.planQuestionSounds },
    { key: "planQuestionNotifications", label: "Planning question notifications", checked: state.messaging.planQuestionNotifications },
    { key: "chatCompletionNotifications", label: "Chat completion notifications", checked: state.messaging.chatCompletionNotifications },
  ];
  const permission = completionNotificationPermission();
  const permissionStatus = permission === "granted"
    ? '<p class="settings-card-help">Browser notifications are allowed.</p>'
    : permission === "denied"
      ? '<p class="settings-card-help">Browser notifications are blocked. Enable them in your browser or operating-system settings.</p>'
      : permission === "unsupported"
        ? '<p class="settings-card-help">Browser notifications are not supported in this environment.</p>'
        : '<p class="settings-card-help">Allow browser notifications so Echo can alert you when another chat finishes.</p><button class="secondary-button compact-button" type="button" data-action="enable-browser-notifications">Allow browser notifications</button>';
  return `
    <section class="settings-section">
      <h2 class="settings-section-title">Messaging</h2>
      <div class="settings-card">
        <h3 class="settings-card-title">Notifications</h3>
        ${toggles.map((t) => `
          <label class="settings-toggle">
            <span>${esc(t.label)}</span>
            <input type="checkbox" data-notification-setting="${t.key}" ${t.checked ? "checked" : ""} ${state.settingsLoaded ? "" : "disabled"} />
          </label>
        `).join("")}
        ${permissionStatus}
      </div>
    </section>
  `;
}

function renderGit() {
  const toggles = [
    { key: "leadingWhitespaceIndicators", checked: state.git.leadingWhitespaceIndicators, label: "Leading whitespace indicators", help: "Show leading whitespace changes in Git diffs." },
    { key: "splitDiffView", checked: state.git.splitDiffView, label: "Split Git diff view", help: "Use a side-by-side diff layout on wide windows." },
  ];
  return `
    <section class="settings-section">
      <h2 class="settings-section-title">Git</h2>
      <div class="settings-card">
        ${toggles.map((t) => `
          <label class="settings-toggle" title="${esc(t.help)}">
            <span>${esc(t.label)}</span>
            <input type="checkbox" data-git-setting="${t.key}" ${t.checked ? "checked" : ""} ${state.settingsLoaded ? "" : "disabled"} />
          </label>
        `).join("")}
      </div>
    </section>
  `;
}

function renderTheme() {
  return `
    <section class="settings-section">
      <div class="settings-section-heading">
        <h2 class="settings-section-title">Theme</h2>
        <button class="secondary-button compact-button" type="button" data-action="restore-theme">Restore Defaults</button>
      </div>

      <div class="settings-card">
        <div class="theme-palette-toggle" role="tablist" aria-label="Theme palette">
          ${["light", "dark"].map((name) => `
            <button
              class="theme-palette-button ${state.themePalette === name ? "is-active" : ""}"
              type="button"
              role="tab"
              aria-selected="${state.themePalette === name}"
              data-action="set-theme-palette"
              data-theme-palette="${name}"
            >${name === "light" ? "Light" : "Dark"}</button>
          `).join("")}
        </div>

        <div class="theme-font-size-field">
          <span>Editor Font Size</span>
          <input
            type="number"
            min="${minEditorFontSize}"
            max="${maxEditorFontSize}"
            step="1"
            value="${state.editorFontSize}"
            data-editor-font-size
            aria-label="Code editor font size"
          />
        </div>

        ${themeGroups.map((group) => `
          <div class="theme-token-group">
            <h4>${esc(group)}</h4>
            <div class="theme-token-grid">
              ${themeTokens.filter((t) => t.group === group).map((t) => {
                const value = t[state.themePalette];
                return `
                  <label class="theme-color-field">
                    <span>${esc(t.label)}</span>
                    <span class="theme-color-control">
                      <input class="theme-color-swatch" type="color" value="${value}" aria-label="${esc(t.label)} color" />
                      <input class="theme-color-hex" type="text" value="${value}" spellcheck="false" aria-label="${esc(t.label)} hex" />
                    </span>
                  </label>
                `;
              }).join("")}
            </div>
          </div>
        `).join("")}
      </div>
    </section>
  `;
}

function renderWorkspaces() {
  return `
    <section class="settings-section">
      <div class="settings-section-heading">
        <div>
          <h2 class="settings-section-title">Workspaces</h2>
          <p class="settings-card-help">Manage the folders Echo can access. Removing a workspace keeps its project files and <code>.echo</code> history.</p>
        </div>
        <button class="secondary-button compact-button" type="button" data-action="add-settings-workspace">${icons.plus}<span>Add Workspace</span></button>
      </div>
      ${state.workspaceStatus ? `<p class="settings-status ${state.workspaceStatus.startsWith("Error:") ? "is-error" : ""}">${esc(state.workspaceStatus)}</p>` : ""}
      <div class="settings-card">
        <div class="workspace-list">
        ${state.workspaces.length ? state.workspaces.map((w) => {
          const additionalFolders = Math.max(0, (w.folders?.length || 1) - 1);
          return `
          <div class="workspace-row">
            <div class="workspace-row-heading">
              <span class="workspace-icon-label">${renderWorkspaceIcon(w)}</span>
              <div>
                <span class="workspace-row-title"><strong>${esc(w.name)}</strong>${w.id === state.modeWorkspaceId ? `<span class="workspace-active-badge">Active</span>` : ""}</span>
                <span class="workspace-row-path">${esc(w.mainPath)}</span>
                <span class="workspace-row-summary">${additionalFolders ? `${additionalFolders} additional folder${additionalFolders === 1 ? "" : "s"}` : "Main folder only"}</span>
              </div>
            </div>
            <div class="endpoint-row-actions">
              <button class="icon-button" type="button" title="Configure ${esc(w.name)}" aria-label="Configure ${esc(w.name)}" data-action="configure-workspace" data-workspace-id="${esc(w.id)}">${icons.settings}</button>
              <button class="icon-button danger-button" type="button" title="Remove ${esc(w.name)}" aria-label="Remove ${esc(w.name)}" data-action="delete-workspace" data-workspace-id="${esc(w.id)}">${icons.trash}</button>
            </div>
          </div>
        `; }).join("") : `<p class="empty-state compact">No workspaces added.</p>`}
        </div>
      </div>
    </section>
  `;
}

function formatSessionTime(value) {
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? "Unknown" : date.toLocaleString();
}

function renderSecurity() {
  return `
    <section class="settings-section">
      <h2 class="settings-section-title">Security</h2>
      ${state.transportSecure ? "" : `
        <div class="settings-security-warning">
          <strong>Trusted-LAN HTTP is not encrypted</strong>
          <span>Authentication controls access, but a hostile network participant could still observe passwords, cookies, and source code. Prefer a trusted network until TLS is configured.</span>
        </div>
      `}
      <div class="settings-security-warning">
        <strong>Integrated terminal grants server command access</strong>
        <span>Every authenticated device can execute arbitrary commands on the Echo server through workspace terminals. Revoke any device you do not fully trust.</span>
      </div>
      ${state.securityStatus ? `<p class="settings-status ${state.securityStatus.startsWith("Error:") ? "is-error" : ""}">${esc(state.securityStatus)}</p>` : ""}

      <div class="settings-card">
        <h3 class="settings-card-title">Remembered devices</h3>
        <p class="settings-card-help">Echo supports concurrent owner sessions. Revoke any device you no longer recognize.</p>
        <div class="security-session-list">
          ${state.authSessions.length ? state.authSessions.map((session) => `
            <div class="security-session-row">
              <div class="security-session-main">
                <strong>${esc(session.device || "Browser")}${session.current ? " (this device)" : ""}</strong>
                <span>${esc(session.remoteIp || "Unknown address")} · Last used ${esc(formatSessionTime(session.lastUsed))}</span>
                <span class="security-user-agent">${esc(session.userAgent || "Unknown browser")}</span>
              </div>
              ${session.current ? "" : `<button class="secondary-button compact-button danger-button" type="button" data-action="revoke-session" data-session-id="${esc(session.id)}">Revoke</button>`}
            </div>
          `).join("") : `<p class="settings-card-help">Loading remembered devices…</p>`}
        </div>
      </div>

      <div class="settings-card">
        <h3 class="settings-card-title">Change owner password</h3>
        <p class="settings-card-help">Changing the password revokes every other remembered device.</p>
        <div class="settings-grid">
          <label class="field"><span>Current password</span><input type="password" autocomplete="current-password" data-security-field="current-password"></label>
          <label class="field"><span>New password</span><input type="password" minlength="12" maxlength="128" autocomplete="new-password" data-security-field="new-password"></label>
          <label class="field"><span>Confirm new password</span><input type="password" minlength="12" maxlength="128" autocomplete="new-password" data-security-field="confirm-password"></label>
        </div>
        <div><button class="secondary-button" type="button" data-action="change-password">Change password</button></div>
      </div>

      <div class="settings-card">
        <h3 class="settings-card-title">Sign out</h3>
        <p class="settings-card-help">Signing out explicitly clears this browser's saved Code hot-exit buffers.</p>
        <div><button class="secondary-button danger-button" type="button" data-action="logout">Sign out of Echo</button></div>
      </div>
    </section>
  `;
}

function renderPlugins() {
  const catalog = state.plugins.catalog;
  const statusError = state.plugins.status.startsWith("Error:");
  return `
    <section class="settings-section plugin-settings-section">
      <div class="settings-section-heading">
        <div><h2 class="settings-section-title">Plugins</h2><p class="settings-card-help">Add optional tools and isolated views without changing Echo's core features.</p></div>
        <button class="secondary-button compact-button" type="button" data-plugin-action="refresh" ${state.plugins.busy ? "disabled" : ""}>Refresh</button>
      </div>
      ${catalog.safeMode ? `<div class="settings-callout is-warning"><strong>Safe mode is active.</strong> All optional plugins remain disabled until Echo restarts without <code>-safe-mode</code>.</div>` : ""}
      ${catalog.credentialStoreAvailable === false ? `<div class="settings-callout is-warning"><strong>OS credential storage is unavailable.</strong> Plugin secrets can use an environment-variable reference or a session-only value that disappears on restart.</div>` : ""}
      ${state.plugins.status ? `<p class="settings-status ${statusError ? "is-error" : ""}">${esc(state.plugins.status)}</p>` : ""}

      <div class="settings-card plugin-install-card">
        <h3 class="settings-card-title">Install a plugin</h3>
        <p class="settings-card-help">Echo stages and inspects packages without running them. Installation requires a separate review below.</p>
        <div class="plugin-source-tabs">
          <button type="button" class="secondary-button compact-button ${state.plugins.sourceType === "local" ? "is-active" : ""}" data-plugin-source="local">Local folder</button>
          <button type="button" class="secondary-button compact-button ${state.plugins.sourceType === "github" ? "is-active" : ""}" data-plugin-source="github">GitHub</button>
        </div>
        ${state.plugins.sourceType === "local" ? `
          <label class="field"><span>Plugin source folder</span><input type="text" data-plugin-install-field="localPath" value="${esc(state.plugins.localPath)}" placeholder="C:\\path\\to\\plugin"><span class="field-help">Echo snapshots this folder. Reloads always create a new reviewable snapshot.</span></label>
        ` : `
          <div class="settings-grid">
            <label class="field"><span>GitHub repository</span><input type="text" data-plugin-install-field="repository" value="${esc(state.plugins.repository)}" placeholder="owner/repository"></label>
            <label class="field"><span>Ref</span><input type="text" data-plugin-install-field="ref" value="${esc(state.plugins.ref)}" placeholder="main, tag, or commit"></label>
            <label class="field field-wide"><span>Subdirectory</span><input type="text" data-plugin-install-field="subdirectory" value="${esc(state.plugins.subdirectory)}" placeholder="optional/path"></label>
          </div>
        `}
        <div class="plugin-install-actions">
          <button class="secondary-button" type="button" data-plugin-action="stage-source" ${state.plugins.busy ? "disabled" : ""}>Stage for review</button>
          <button class="secondary-button" type="button" data-plugin-action="stage-calculator" ${state.plugins.busy ? "disabled" : ""}>Try built-in Calculator</button>
        </div>
      </div>

      ${renderWorkspacePluginRequirements(catalog)}
      ${catalog.stages?.length ? `<div class="plugin-review-list"><h3>Pending review</h3>${catalog.stages.map(renderPluginStage).join("")}</div>` : ""}
      <div class="plugin-installed-list">
        <h3>Installed</h3>
        ${catalog.plugins?.length ? catalog.plugins.map(renderInstalledPlugin).join("") : `<div class="settings-card"><p class="settings-card-help">No plugins are installed. Echo's core features are unaffected.</p></div>`}
      </div>
      ${catalog.retained?.length ? `<div class="settings-card"><h3 class="settings-card-title">Retained plugin data</h3><p class="settings-card-help">Uninstall preserves configuration, namespaced storage, and credential references for a future reinstall.</p>${catalog.retained.map(item => `<div class="plugin-requirement-row"><span><strong>${esc(item.name || item.id)}</strong> <small>v${esc(item.version || "")}</small></span><button type="button" class="secondary-button compact-button danger-button" data-plugin-action="remove-data" data-plugin-id="${esc(item.id)}">Remove retained data</button></div>`).join("")}</div>` : ""}
    </section>
  `;
}

function renderWorkspacePluginRequirements(catalog) {
  const missing = catalog.missing || [];
  const conflicts = catalog.conflicts || [];
  if (!missing.length && !conflicts.length) return "";
  return `<div class="settings-card plugin-requirements"><h3 class="settings-card-title">Workspace requirements</h3>
    ${missing.map(recipe => `<div class="plugin-requirement-row"><span><strong>${esc(recipe.id)}</strong> is required by <code>.echo/plugins.json</code> but is not installed.</span>${recipe.source?.repository ? `<button type="button" class="secondary-button compact-button" data-plugin-action="stage-requirement" data-repository="${esc(recipe.source.repository)}" data-commit="${esc(recipe.source.commit || "")}" data-subdirectory="${esc(recipe.source.subdirectory || "")}">Install pinned package</button>` : ""}</div>`).join("")}
    ${conflicts.map(recipe => `<div class="plugin-requirement-row is-conflict"><span><strong>${esc(recipe.id)}</strong> is installed at a different commit. It remains inactive for this workspace.</span><button type="button" class="secondary-button compact-button" data-plugin-action="stage-requirement" data-repository="${esc(recipe.source.repository || "")}" data-commit="${esc(recipe.source.commit || "")}" data-subdirectory="${esc(recipe.source.subdirectory || "")}">Review pinned commit</button></div>`).join("")}
  </div>`;
}

function renderPluginStage(stage) {
  const manifest = stage.validation?.manifest || {};
  const permissions = manifest.permissions || [];
  const tools = manifest.contributes?.tools || [];
  const settings = manifest.contributes?.settings || [];
  const source = stage.source || {};
  return `<article class="settings-card plugin-review-card" data-stage-id="${esc(stage.id)}">
    <div class="settings-section-heading"><div><span class="plugin-kicker">Untrusted staged code</span><h3 class="settings-card-title">${esc(manifest.name || manifest.id)} <small>v${esc(manifest.version)}</small></h3></div><code>${esc(stage.validation?.target || "")}</code></div>
    <p>${esc(manifest.description || "No description provided.")}</p>
    <dl class="plugin-facts"><dt>Source</dt><dd>${esc(pluginSourceLabel(source))}</dd>${source.commit ? `<dt>Commit</dt><dd><code>${esc(source.commit)}</code></dd>` : ""}<dt>Digest</dt><dd><code>${esc(stage.validation?.digest || "")}</code></dd>${stage.previousDigest ? `<dt>Replaces</dt><dd><code>${esc(stage.previousDigest)}</code></dd>` : ""}</dl>
    ${stage.previousDigest ? renderPluginStageDiff(stage.diff || {}) : ""}
    ${manifest.runtime ? `<div class="settings-callout is-warning"><strong>Native code warning:</strong> this backend will run with your OS account permissions. Declarations are review boundaries, not an OS sandbox.</div>` : `<div class="settings-callout">UI content runs in a restricted, opaque-origin iframe.</div>`}
    <div class="plugin-review-columns"><div><strong>Permissions</strong>${permissions.length ? `<ul>${permissions.map(permission => `<li><code>${esc(permission.name)}</code> — ${esc(permission.reason)}${permission.hosts?.length ? `<small> Hosts: ${permission.hosts.map(host => `<code>${esc(host)}</code>`).join(" ")}</small>` : ""}</li>`).join("")}</ul>` : `<p>None requested.</p>`}</div><div><strong>Agent tools and schemas</strong>${tools.length ? `<ul>${tools.map(renderPluginReviewTool).join("")}</ul>` : `<p>None contributed.</p>`}</div><div><strong>Settings</strong>${settings.length ? `<ul>${settings.map(setting => `<li><code>${esc(setting.key)}</code> — ${esc(setting.label)} (${esc(setting.scope)}${setting.type === "secret" ? ", secret" : ""})</li>`).join("")}</ul>` : `<p>None contributed.</p>`}</div></div>
    <div class="plugin-review-actions">
      <button type="button" class="secondary-button" data-plugin-action="approve-stage" data-stage-id="${esc(stage.id)}" data-scope="none">Install, keep scopes</button>
      <button type="button" class="secondary-button" data-plugin-action="approve-stage" data-stage-id="${esc(stage.id)}" data-scope="workspace" ${state.modeWorkspaceId ? "" : "disabled"}>Install &amp; enable here</button>
      <button type="button" class="secondary-button" data-plugin-action="approve-stage" data-stage-id="${esc(stage.id)}" data-scope="global">Install &amp; enable globally</button>
      <button type="button" class="secondary-button danger-button" data-plugin-action="reject-stage" data-stage-id="${esc(stage.id)}">Reject</button>
    </div>
  </article>`;
}

function renderPluginReviewTool(tool) {
  const classification = tool.readOnly ? "read-only" : tool.mutating ? "mutating" : "unclassified";
  const output = tool.outputSchema ? `<details><summary>Output schema</summary><pre>${esc(JSON.stringify(tool.outputSchema, null, 2))}</pre></details>` : "";
  return `<li><code>${esc(tool.name)}</code> — ${esc(tool.description)}<small>${esc(classification)} · RPC <code>${esc(tool.method || "")}</code> · ${esc(tool.timeoutSeconds || 60)}s timeout</small><details><summary>Input schema</summary><pre>${esc(JSON.stringify(tool.inputSchema || {}, null, 2))}</pre></details>${output}</li>`;
}

function renderPluginStageDiff(diff) {
  const rows = [
    ["Permissions added", diff.permissionsAdded], ["Permissions removed", diff.permissionsRemoved],
    ["Tools added", diff.toolsAdded], ["Tools removed", diff.toolsRemoved],
    ["Views added", diff.viewsAdded], ["Views removed", diff.viewsRemoved],
    ["Settings added", diff.settingsAdded], ["Settings removed", diff.settingsRemoved],
  ].filter(([, values]) => values?.length);
  return `<div class="plugin-update-diff"><strong>Update from v${esc(diff.previousVersion || "unknown")}</strong><span>Package content ${diff.codeChanged ? "changed" : "is unchanged"}.${diff.permissionsChanged ? " Permission declarations changed." : ""}${diff.toolContractsChanged ? " Agent-tool contracts changed." : ""}</span>${rows.length ? `<ul>${rows.map(([label, values]) => `<li><span>${label}:</span> ${values.map(value => `<code>${esc(value)}</code>`).join(" ")}</li>`).join("")}</ul>` : ""}</div>`;
}

function renderInstalledPlugin(plugin) {
  const settings = plugin.settings || [];
  const logs = state.plugins.logs[plugin.id];
  return `<article class="settings-card installed-plugin ${plugin.effective ? "is-effective" : ""}">
    <div class="settings-section-heading"><div><span class="plugin-kicker">${plugin.effective ? "Active" : plugin.health ? "Unhealthy" : "Inactive"}</span><h3 class="settings-card-title">${esc(plugin.name)} <small>v${esc(plugin.version)}</small></h3><p class="settings-card-help">${esc(plugin.description || "")}</p></div><span class="plugin-health-dot ${plugin.effective ? "is-active" : ""}" aria-hidden="true"></span></div>
    ${plugin.health ? `<div class="settings-callout is-error"><strong>Runtime health:</strong> ${esc(plugin.health)}</div>` : ""}
    <dl class="plugin-facts"><dt>Source</dt><dd>${esc(pluginSourceLabel(plugin.source))}</dd><dt>Digest</dt><dd><code>${esc(plugin.digest)}</code></dd><dt>Tools</dt><dd>${plugin.approvedTools?.length ? plugin.approvedTools.map(name => `<code>${esc(name)}</code>`).join(" ") : "None"}</dd><dt>Views</dt><dd>${plugin.views?.length ? plugin.views.map(view => esc(`${view.title} (${view.kind})`)).join(", ") : "None"}</dd></dl>
    <div class="plugin-scope-controls">
      <button type="button" class="secondary-button compact-button" data-plugin-action="${plugin.globalEnabled ? "disable-global" : "enable-global"}" data-plugin-id="${esc(plugin.id)}">${plugin.globalEnabled ? "Disable globally" : "Enable globally"}</button>
      <button type="button" class="secondary-button compact-button" data-plugin-action="${plugin.workspaceEnabled ? "disable-workspace" : "enable-workspace"}" data-plugin-id="${esc(plugin.id)}" ${state.modeWorkspaceId ? "" : "disabled"}>${plugin.workspaceEnabled ? "Disable in workspace" : "Enable in workspace"}</button>
    </div>
    ${settings.length ? `<details class="plugin-config"><summary>Configuration</summary><div class="plugin-setting-grid">${settings.map(setting => renderPluginSetting(plugin, setting)).join("")}</div><button type="button" class="secondary-button" data-plugin-action="save-config" data-plugin-id="${esc(plugin.id)}">Save configuration</button></details>` : ""}
    <div class="plugin-management-actions">
      ${plugin.source?.type === "local" ? `<button type="button" class="secondary-button compact-button" data-plugin-action="reload" data-plugin-id="${esc(plugin.id)}">Reload local snapshot</button>` : ""}
      ${plugin.source?.type === "github" ? `<button type="button" class="secondary-button compact-button" data-plugin-action="update-check" data-plugin-id="${esc(plugin.id)}">Check for update</button>` : ""}
      <button type="button" class="secondary-button compact-button" data-plugin-action="logs" data-plugin-id="${esc(plugin.id)}">${logs === undefined ? "View logs" : "Refresh logs"}</button>
      <button type="button" class="secondary-button compact-button danger-button" data-plugin-action="uninstall" data-plugin-id="${esc(plugin.id)}">Uninstall</button>
      <button type="button" class="secondary-button compact-button danger-button" data-plugin-action="remove-data" data-plugin-id="${esc(plugin.id)}">Remove plugin data</button>
    </div>
    ${logs !== undefined ? `<pre class="plugin-log" aria-label="${esc(plugin.name)} runtime log">${esc(logs || "No runtime log entries.")}</pre>` : ""}
  </article>`;
}

function renderPluginSetting(plugin, setting) {
  const base = `data-plugin-setting="${esc(plugin.id)}" data-setting-key="${esc(setting.key)}" data-setting-type="${esc(setting.type)}" data-setting-scope="${esc(setting.scope)}"`;
  const disabled = setting.scope === "workspace" && !state.modeWorkspaceId ? "disabled" : "";
  if (setting.type === "secret") {
    return `<fieldset class="plugin-secret-setting" data-plugin-secret="${esc(plugin.id)}" data-setting-key="${esc(setting.key)}" data-setting-scope="${esc(setting.scope)}"><legend>${esc(setting.label)}${setting.required ? " *" : ""}</legend><span class="field-help">${esc(setting.help || "Secret values are never returned by Echo.")} Current: ${setting.configured ? `configured (${esc(setting.secretSource || "credential store")})` : "missing"}</span><div class="settings-grid"><label class="field"><span>Source</span><select data-secret-part="source" ${disabled}><option value="os">OS credential store</option><option value="session">This session only</option><option value="environment">Environment variable</option><option value="clear">Clear</option></select></label><label class="field"><span>Secret value</span><input type="password" data-secret-part="value" autocomplete="new-password" ${disabled}></label><label class="field"><span>Environment variable</span><input type="text" data-secret-part="environment" placeholder="MY_PLUGIN_TOKEN" ${disabled}></label></div></fieldset>`;
  }
  let control = "";
  if (setting.type === "boolean") control = `<input type="checkbox" ${setting.value ? "checked" : ""} ${base} ${disabled}>`;
  else if (setting.type === "select") control = `<select ${base} ${disabled}>${(setting.options || []).map(option => `<option value="${esc(option.value)}" ${option.value === setting.value ? "selected" : ""}>${esc(option.label)}</option>`).join("")}</select>`;
  else control = `<input type="${setting.type === "number" ? "number" : setting.type === "url" ? "url" : "text"}" value="${esc(setting.value ?? "")}" ${setting.minimum !== undefined ? `min="${esc(setting.minimum)}"` : ""} ${setting.maximum !== undefined ? `max="${esc(setting.maximum)}"` : ""} ${setting.pattern ? `pattern="${esc(setting.pattern)}"` : ""} ${base} ${disabled}>`;
  return `<label class="field"><span>${esc(setting.label)}${setting.required ? " *" : ""} <small>${setting.scope}</small></span>${control}${setting.help ? `<span class="field-help">${esc(setting.help)}</span>` : ""}</label>`;
}

function pluginSourceLabel(source = {}) {
  if (source.type === "github") return `${source.repository || "GitHub"}${source.commit ? ` @ ${source.commit}` : source.ref ? ` @ ${source.ref}` : ""}${source.subdirectory ? ` / ${source.subdirectory}` : ""}`;
  if (source.type === "local") return source.path || "Local development folder";
  if (source.type === "builtin") return `Built into Echo (${source.builtin || "package"})`;
  return "Unknown source";
}

function renderDevelopment() {
  const rebuildError = state.rebuild.status.startsWith("Error:");
  const updateError = state.update.status.startsWith("Error:");
  const terminateError = state.terminate.status.startsWith("Error:");
  const developmentBusy = state.rebuild.running || state.update.running || state.terminate.running;
  const canUpdate = state.update.available || state.update.running;
  const updateButtonLabel = state.update.running
    ? "Updating Echo…"
    : state.update.checking
      ? "Checking for Updates…"
      : canUpdate
        ? "Update Echo"
        : "Check for Updates";
  return `
    <section class="settings-section">
      <h2 class="settings-section-title">Development</h2>

      ${state.update.checkError ? `
        <div class="settings-callout is-error" data-update-check-error>
          <strong>Unable to check for Echo updates.</strong> ${esc(state.update.checkError)}
          <button class="secondary-button compact-button" type="button" data-action="retry-update-check" ${state.update.checking ? "disabled" : ""}>${state.update.checking ? "Checking…" : "Retry"}</button>
        </div>
      ` : ""}

      <div class="settings-card echo-update-card">
        <h3 class="settings-card-title">Update Echo</h3>
        <p class="settings-card-help">Checks GitHub <code>master</code> for updates. When one is available, Echo can pull it, rebuild, and relaunch.</p>
        <button class="secondary-button" type="button" data-action="${canUpdate ? "update-echo" : "check-for-updates"}" ${developmentBusy || state.update.checking ? "disabled aria-busy=\"true\"" : ""}>${updateButtonLabel}</button>
        ${state.update.status ? `<p class="settings-status ${updateError ? "is-error" : ""}" data-update-status>${esc(state.update.status)}</p>` : ""}
        ${state.update.logPath ? `<p class="field-help">Log: <code>${esc(state.update.logPath)}</code></p>` : ""}
      </div>

      <div class="settings-card">
        <h3 class="settings-card-title">AI flow logging</h3>
        <label class="settings-toggle" title="Capture the exact AI transcript for this app session.">
          <span>AI flow logging</span>
          <input type="checkbox" />
        </label>
        <p class="field-help">Writes JSONL to <code>.echo/echo.log</code> in the active workspace. Enabling erases the previous capture and is not remembered after restart.</p>
      </div>

      <div class="settings-card">
        <h3 class="settings-card-title">Rebuild &amp; Relaunch</h3>
        <p class="settings-card-help">Rebuilds the Echo application and relaunches it. Requires the Echo source workspace to be added.</p>
        <button class="secondary-button danger-button" type="button" data-action="rebuild-relaunch" ${developmentBusy ? "disabled aria-busy=\"true\"" : ""}>${state.rebuild.running ? "Rebuilding Echo…" : "Rebuild &amp; Relaunch"}</button>
        ${state.rebuild.status ? `<p class="settings-status ${rebuildError ? "is-error" : ""}" data-rebuild-status>${esc(state.rebuild.status)}</p>` : ""}
        ${state.rebuild.logPath ? `<p class="field-help">Log: <code>${esc(state.rebuild.logPath)}</code></p>` : ""}
      </div>

      <div class="settings-card">
        <h3 class="settings-card-title">Terminate Echo</h3>
        <p class="settings-card-help">Stops the current Echo process without relaunching it. Active chats and terminals will be interrupted.</p>
        <button class="secondary-button danger-button" type="button" data-action="terminate-echo" ${developmentBusy ? "disabled aria-busy=\"true\"" : ""}>${state.terminate.running ? "Terminating Echo…" : "Terminate Echo"}</button>
        ${state.terminate.status ? `<p class="settings-status ${terminateError ? "is-error" : ""}" data-terminate-status>${esc(state.terminate.status)}</p>` : ""}
      </div>
    </section>
  `;
}

function renderLanguageServers() {
  const enabled = new Set(state.lsp.config.enabledProfileIds || []);
  const statuses = new Map((state.lsp.statuses || []).map((status) => [status.profileId, status]));
  const workspaceName = state.modeWorkspaceName ? ` in <strong>${esc(state.modeWorkspaceName)}</strong>` : "";
  return `
    <section class="settings-section lsp-settings-section">
      <div class="settings-section-heading">
        <div><h2 class="settings-section-title">Language Servers</h2><p class="settings-card-help">Configure reusable LSP processes globally, then enable them per workspace${workspaceName}.</p></div>
        <button class="secondary-button compact-button" type="button" data-lsp-action="new-profile">${icons.plus}<span>New Profile</span></button>
      </div>
      ${state.lsp.status ? `<p class="settings-status ${state.lsp.status.startsWith("Error:") ? "is-error" : ""}">${esc(state.lsp.status)}</p>` : ""}

      <div class="settings-card">
        <h3 class="settings-card-title">Built-in templates</h3>
        <p class="settings-card-help">Templates are immutable starting points. Echo never installs language-server executables.</p>
        <div class="lsp-template-list">
          ${(state.lsp.templates || []).map((template) => {
            const exists = state.lsp.profiles.some((profile) => profile.id === template.profile.id);
            return `<div class="endpoint-row"><div class="endpoint-row-main"><strong>${esc(template.profile.name)}</strong><span class="endpoint-row-summary"><code>${esc(template.profile.command)}</code> — ${esc(template.description)}</span></div><button class="secondary-button compact-button" type="button" data-lsp-action="add-template" data-template-id="${esc(template.id)}" ${exists || state.lsp.busy ? "disabled" : ""}>${exists ? "Added" : "Create profile"}</button></div>`;
          }).join("") || `<p class="empty-state compact">Loading templates…</p>`}
        </div>
      </div>

      ${state.lsp.draft ? renderLSPProfileEditor(state.lsp.draft) : ""}

      <div class="settings-card">
        <h3 class="settings-card-title">Profiles</h3>
        <div class="endpoint-list">
          ${state.lsp.profiles.map((profile) => {
            const active = enabled.has(profile.id);
            const runtime = statuses.get(profile.id);
            return `<div class="endpoint-row lsp-profile-row"><label class="lsp-profile-enable"><input type="checkbox" data-lsp-enable="${esc(profile.id)}" ${active ? "checked" : ""} ${state.modeWorkspaceId ? "" : "disabled"}><span class="endpoint-row-main"><strong>${esc(profile.name)}</strong><span class="endpoint-row-summary"><code>${esc(profile.command)}</code> · ${esc((profile.selectors || []).map((selector) => selector.languageId).join(", "))}</span></span></label><div class="endpoint-row-actions"><span class="lsp-runtime-state is-${esc(runtime?.state || "inactive")}" title="${esc(runtime?.message || "")}">${esc(runtime?.state || (active ? "inactive" : "disabled"))}</span><button class="icon-button" type="button" title="Edit profile" data-lsp-action="edit-profile" data-profile-id="${esc(profile.id)}">${icons.settings}</button><button class="icon-button danger-button" type="button" title="Delete profile" data-lsp-action="delete-profile" data-profile-id="${esc(profile.id)}">${icons.trash}</button></div></div>`;
          }).join("") || `<p class="empty-state compact">Create a profile from a template or add a custom server.</p>`}
        </div>
      </div>

      <div class="settings-card">
        <h3 class="settings-card-title">Workspace activation</h3>
        <p class="settings-card-help">Overrides replace the corresponding profile field. Arrays and JSON objects are not merged.</p>
        <label class="settings-toggle"><span><strong>Format on save</strong><span class="field-help">Formats code and organizes supported imports; failures or timeouts never prevent saving.</span></span><input type="checkbox" data-lsp-config="formatOnSave" ${state.lsp.config.formatOnSave ? "checked" : ""}></label>
        <div class="settings-grid">
          <label class="field"><span>Format timeout (ms)</span><input type="number" min="250" max="30000" step="250" value="${esc(state.lsp.config.formatOnSaveTimeoutMs || 3000)}" data-lsp-config="formatOnSaveTimeoutMs"></label>
          <label class="field field-wide"><span>Profile overrides (JSON)</span><textarea rows="8" spellcheck="false" data-lsp-overrides>${esc(state.lsp.overridesText)}</textarea><span class="field-help">Keys are profile IDs. Supported fields: name, command, args, selectors, environment, initializationOptions, settings.</span></label>
        </div>
        <button class="primary-button" type="button" data-lsp-action="save-workspace" ${state.modeWorkspaceId && !state.lsp.busy ? "" : "disabled"}>Save Workspace LSP Settings</button>
      </div>

      <div class="settings-card">
        <div class="settings-section-heading"><div><h3 class="settings-card-title">Runtime state</h3><p class="settings-card-help">Servers stay shared across browser clients until disabled or Echo exits.</p></div><button class="secondary-button compact-button" type="button" data-lsp-action="refresh">Refresh</button></div>
        <div class="lsp-runtime-list">
          ${(state.lsp.statuses || []).map((runtime) => `<article class="lsp-runtime-card"><div><strong>${esc(runtime.name || runtime.profileId)}</strong><span class="lsp-runtime-state is-${esc(runtime.state)}">${esc(runtime.state)}</span></div>${runtime.message ? `<p>${esc(runtime.message)}</p>` : ""}${runtime.stderr ? `<details><summary>Server stderr</summary><pre>${esc(runtime.stderr)}</pre></details>` : ""}<button class="secondary-button compact-button" type="button" data-lsp-action="restart" data-profile-id="${esc(runtime.profileId)}">Restart</button></article>`).join("") || `<p class="empty-state compact">No language servers are enabled in this workspace.</p>`}
        </div>
      </div>
    </section>
  `;
}

function renderTesting() {
  const workspaceName = state.modeWorkspaceName ? ` for <strong>${esc(state.modeWorkspaceName)}</strong>` : "";
  return `<section class="settings-section">
    <div class="settings-section-heading"><div><h2 class="settings-section-title">Testing</h2><p class="settings-card-help">Configure CodeLens test actions${workspaceName}.</p></div></div>
    ${state.testing.status ? `<p class="settings-status ${state.testing.status.startsWith("Error:") ? "is-error" : ""}">${esc(state.testing.status)}</p>` : ""}
    <div class="settings-card">
      <h3 class="settings-card-title">Go tests</h3>
      <p class="settings-card-help">These settings apply to normal runs and transient Delve debug launches.</p>
      <div class="settings-grid">
        <label class="toggle-row field-wide"><input type="checkbox" data-go-testing-field="codeLens" ${state.testing.config.codeLens !== false ? "checked" : ""}><span><strong>Show test CodeLens</strong><small>Show package, file, function, benchmark, fuzz, and static subtest actions in <code>*_test.go</code> files.</small></span></label>
        <label class="toggle-row field-wide"><input type="checkbox" data-go-testing-field="coverage" ${state.testing.config.coverage !== false ? "checked" : ""}><span><strong>Show package test coverage</strong><small>Highlight covered and uncovered statements after successful package test runs.</small></span></label>
        <label class="field"><span>Test timeout</span><input type="text" value="${esc(state.testing.config.timeout || "30s")}" data-go-testing-field="timeout" placeholder="30s"><span class="field-help">A non-negative Go duration; <code>0s</code> disables the timeout.</span></label>
        <label class="field"><span>Build tags</span><input type="text" value="${esc(state.testing.config.tags || "")}" data-go-testing-field="tags" placeholder="integration,linux"></label>
        <label class="field field-wide"><span>Test flags (JSON array)</span><textarea rows="6" spellcheck="false" data-go-testing-text="flags">${esc(state.testing.flagsText)}</textarea><span class="field-help">Arguments are passed directly without shell expansion. Use <code>-args</code> before test-binary arguments.</span></label>
        <label class="field field-wide"><span>Environment (JSON object)</span><textarea rows="6" spellcheck="false" data-go-testing-text="environment">${esc(state.testing.environmentText)}</textarea><span class="field-help">Values are stored as ordinary plaintext workspace configuration, not secrets.</span></label>
      </div>
      <button class="primary-button" type="button" data-go-testing-action="save" ${state.modeWorkspaceId && !state.testing.busy ? "" : "disabled"}>Save Workspace Testing Settings</button>
    </div>
  </section>`;
}

function renderLSPProfileEditor(draft) {
  return `<div class="settings-card lsp-profile-editor">
    <div class="settings-section-heading"><div><h3 class="settings-card-title">${state.lsp.editingId ? "Edit profile" : "New profile"}</h3><p class="settings-card-help">The command is executed directly in the workspace folder without shell expansion.</p></div><button class="icon-button" type="button" title="Close" data-lsp-action="cancel-profile">${icons.x}</button></div>
    <div class="settings-grid">
      <label class="field"><span>ID</span><input type="text" value="${esc(draft.id)}" data-lsp-field="id" ${state.lsp.editingId ? "disabled" : ""} placeholder="my-language-server"></label>
      <label class="field"><span>Name</span><input type="text" value="${esc(draft.name)}" data-lsp-field="name" placeholder="My Language Server"></label>
      <label class="field field-wide"><span>Command</span><input type="text" value="${esc(draft.command)}" data-lsp-field="command" placeholder="language-server"></label>
      <label class="field field-wide"><span>Arguments (one per line)</span><textarea rows="3" spellcheck="false" data-lsp-field="argsText">${esc(draft.argsText)}</textarea></label>
    </div>
    <div class="lsp-selector-list"><strong>Document selectors</strong>${draft.selectors.map((selector, index) => `<div class="lsp-selector-row"><label class="field"><span>Language ID</span><input value="${esc(selector.languageId)}" data-lsp-selector-field="languageId" data-selector-index="${index}" placeholder="go"></label><label class="field"><span>Extensions</span><input value="${esc(selector.extensionsText)}" data-lsp-selector-field="extensionsText" data-selector-index="${index}" placeholder=".go, .mod"></label><label class="field"><span>Filenames</span><input value="${esc(selector.filenamesText)}" data-lsp-selector-field="filenamesText" data-selector-index="${index}" placeholder="Makefile"></label><button class="icon-button danger-button" type="button" title="Remove selector" data-lsp-action="remove-selector" data-selector-index="${index}">${icons.trash}</button></div>`).join("")}<button class="secondary-button compact-button" type="button" data-lsp-action="add-selector">${icons.plus}<span>Add selector</span></button></div>
    <div class="settings-grid">
      <label class="field field-wide"><span>Environment (KEY=VALUE, one per line)</span><textarea rows="4" spellcheck="false" data-lsp-field="environmentText">${esc(draft.environmentText)}</textarea></label>
      <label class="field field-wide"><span>Initialization options (JSON)</span><textarea rows="6" spellcheck="false" data-lsp-field="initializationOptionsText">${esc(draft.initializationOptionsText)}</textarea></label>
      <label class="field field-wide"><span>Settings (JSON)</span><textarea rows="6" spellcheck="false" data-lsp-field="settingsText">${esc(draft.settingsText)}</textarea></label>
    </div>
    <div class="mode-editor-actions"><button class="secondary-button" type="button" data-lsp-action="cancel-profile">Cancel</button><button class="primary-button" type="button" data-lsp-action="save-profile" ${state.lsp.busy ? "disabled" : ""}>Save Profile</button></div>
  </div>`;
}

const renderers = {
  llm: renderLLMEndpoints,
  modes: renderAgentModes,
  plugins: renderPlugins,
  external: renderExternal,
  messaging: renderMessaging,
  git: renderGit,
  lsp: renderLanguageServers,
  testing: renderTesting,
  theme: renderTheme,
  workspaces: renderWorkspaces,
  security: renderSecurity,
  development: renderDevelopment,
};

function renderContent() {
  return renderers[state.activeSection]();
}

function render() {
  const root = mountedRoot;
  if (!root) return;
  closeSettingsWorkspaceDropdown?.();
  closeSettingsWorkspaceDropdown = null;
  disposeSettingsChatMap?.();
  disposeSettingsChatMap = null;
  const activeWorkspace = state.workspaces.find((workspace) => workspace.id === state.modeWorkspaceId) || null;
  root.innerHTML = `
    <div class="settings-view">
      <nav class="settings-sidebar" aria-label="Settings sections">
        <div class="settings-sidebar-header">
          <button class="settings-back-button" type="button" data-action="back-from-settings" title="Back to previous view" aria-label="Back to previous view">
            ${icons.arrowLeft}
            <span>Back</span>
          </button>
          <span class="eyebrow">Echo</span>
          <h1>Settings</h1>
        </div>
        <ul class="settings-nav-list">
          ${sections.map((s) => `
            <li>
              <button
                class="settings-nav-button ${s.id === "development" ? "echo-update-target " : ""}${state.activeSection === s.id ? "is-active" : ""}"
                type="button"
                data-section="${s.id}"
                ${s.id === "development" ? `data-echo-update-target data-echo-update-label="Development" title="Development" aria-label="Development"` : ""}
              >${s.icon}<span>${esc(s.label)}</span>${s.id === "development" ? `<b class="echo-update-badge" data-echo-update-badge hidden aria-hidden="true"><span class="codicon codicon-arrow-down" aria-hidden="true"></span></b>` : ""}</button>
            </li>
          `).join("")}
        </ul>
      </nav>
      <main class="settings-content">
        ${renderContent()}
      </main>
      ${renderMobilePrimaryNav({ active: "settings", workspaceName: activeWorkspace?.name, workspaceSelector: true })}
    </div>
  `;
  bindEvents(root);
  syncEchoUpdateBadges(root);
}

function bindEvents(root) {
  const leaveSettings = async (hash) => {
    captureExternalFields(root);
    await saveSettings();
    location.hash = hash;
  };
  const addSettingsWorkspace = () => {
    closeSettingsAddWorkspaceModal?.();
    closeSettingsAddWorkspaceModal = openAddWorkspaceModal({
      onCreate: async (workspace) => {
        closeSettingsAddWorkspaceModal = null;
        try {
          captureExternalFields(root);
          await saveSettings();
          await put("/api/workspaces/active", { id: workspace.id });
          state.workspaceStatus = `Added ${workspace.name}.`;
          window.dispatchEvent(new CustomEvent("echo:workspace-changed", { detail: { workspaceId: workspace.id } }));
          await loadAgentModes();
        } catch (err) {
          state.workspaceStatus = `Error: ${err.message}`;
          render();
        }
      },
    });
  };

  disposeSettingsChatMap = installChatMap(root, {
    navigate: (target) => leaveSettings(chatTargetRouteHash(target)),
  });

  root.querySelectorAll("[data-nav='chat']").forEach((button) => {
    button.addEventListener("click", () => { void leaveSettings("#/home"); });
  });
  root.querySelectorAll("[data-nav='code']").forEach((button) => {
    button.addEventListener("click", () => { void leaveSettings("#/code"); });
  });
  root.querySelectorAll("[data-nav='search']").forEach((button) => {
    button.addEventListener("click", () => { void leaveSettings(codeRouteHash("search")); });
  });
  root.querySelectorAll("[data-nav='git']").forEach((button) => {
    button.addEventListener("click", () => { void leaveSettings(codeRouteHash("git")); });
  });
  root.querySelectorAll("[data-nav='debug']").forEach((button) => {
    button.addEventListener("click", () => { void leaveSettings(codeRouteHash("debug")); });
  });
  root.querySelectorAll("[data-nav='sandbox']").forEach((button) => {
    button.addEventListener("click", () => { void leaveSettings("#/sandbox"); });
  });

  root.querySelectorAll(".workspace-dropdown-trigger").forEach((trigger) => {
    trigger.addEventListener("click", (event) => {
      event.stopPropagation();
      if (closeSettingsWorkspaceDropdown) {
        closeSettingsWorkspaceDropdown();
        return;
      }
      closeSettingsWorkspaceDropdown = openWorkspaceDropdown(trigger, {
        items: state.workspaces,
        selectedId: state.modeWorkspaceId,
        onClose: () => { closeSettingsWorkspaceDropdown = null; },
        onSelect: async (id) => {
          try {
            captureExternalFields(root);
            await saveSettings();
            await put("/api/workspaces/active", { id });
            window.dispatchEvent(new CustomEvent("echo:workspace-changed", { detail: { workspaceId: id } }));
            await loadAgentModes();
          } catch (err) {
            state.modeStatus = `Error: ${err.message}`;
            render();
          }
        },
        onAdd: addSettingsWorkspace,
      });
    });
  });

  root.querySelectorAll("[data-section]").forEach((btn) => {
    btn.addEventListener("click", () => {
      // Persist any in-progress external connection edits before switching away.
      captureExternalFields(root);
      saveSettings();
      state.activeSection = btn.dataset.section;
      render();
      if (state.activeSection === "security") loadSecurity();
      if (state.activeSection === "plugins") loadPlugins();
      if (state.activeSection === "lsp") loadLanguageServers();
      if (state.activeSection === "testing") loadGoTesting();
    });
  });

  root.querySelectorAll('[data-action="add-settings-workspace"]').forEach((button) => {
    button.addEventListener("click", addSettingsWorkspace);
  });

  root.querySelectorAll('[data-action="configure-workspace"]').forEach((button) => {
    button.addEventListener("click", () => {
      const workspace = state.workspaces.find((candidate) => candidate.id === button.dataset.workspaceId);
      if (!workspace) return;
      closeSettingsEditWorkspaceModal?.();
      closeSettingsEditWorkspaceModal = openEditWorkspaceModal(workspace, {
        onUpdate: async (updated) => {
          closeSettingsEditWorkspaceModal = null;
          state.workspaceStatus = `Saved ${updated.name}.`;
          if (updated.id === state.modeWorkspaceId) {
            window.dispatchEvent(new CustomEvent("echo:workspace-changed", { detail: { workspaceId: state.modeWorkspaceId } }));
          }
          await loadAgentModes();
        },
      });
    });
  });

  root.querySelectorAll('[data-action="delete-workspace"]').forEach((button) => {
    button.addEventListener("click", async () => {
      const workspace = state.workspaces.find((candidate) => candidate.id === button.dataset.workspaceId);
      if (!workspace || !confirm(`Remove “${workspace.name}” from Echo?\n\nLive chats, terminals, language servers, Git operations, and sandbox containers for this workspace will stop. Project files and .echo history will be kept.`)) return;
      button.disabled = true;
      try {
        await unregisterWorkspace(workspace.id);
        state.workspaceStatus = `Removed ${workspace.name}. Project files and .echo history were kept.`;
        await loadAgentModes();
      } catch (err) {
        state.workspaceStatus = `Error: ${err.message}`;
        render();
      }
    });
  });

  bindPluginEvents(root);
  bindLSPEvents(root);
  bindGoTestingEvents(root);

  root.querySelectorAll("[data-action='set-theme-palette']").forEach((btn) => {
    btn.addEventListener("click", () => {
      state.themePalette = btn.dataset.themePalette;
      render();
    });
  });

  // Return to the view that opened Settings, or Chat for a direct page load.
  root.querySelectorAll("[data-action='back-from-settings']").forEach((btn) => {
    btn.addEventListener("click", async () => {
      // Persist any in-progress external connection edits before leaving.
      captureExternalFields(root);
      await saveSettings();
      navigateBackFromSettings();
    });
  });

  // --- LLM endpoint management ---

  root.querySelectorAll("[data-action='add-endpoint']").forEach((btn) => {
    btn.addEventListener("click", () => {
      const ep = newEndpoint();
      state.endpoints.push(ep);
      state.editingEndpointId = ep.id;
      render();
      saveSettings();
    });
  });

  root.querySelectorAll("[data-action='edit-endpoint']").forEach((btn) => {
    btn.addEventListener("click", () => {
      state.editingEndpointId = btn.dataset.endpointId;
      render();
    });
  });

  root.querySelectorAll("[data-action='close-endpoint-editor']").forEach((btn) => {
    btn.addEventListener("click", () => {
      state.editingEndpointId = null;
      render();
    });
  });

  root.querySelectorAll("[data-action='save-endpoint']").forEach((btn) => {
    btn.addEventListener("click", () => {
      state.editingEndpointId = null;
      saveSettings();
    });
  });

  root.querySelectorAll("[data-action='delete-endpoint']").forEach((btn) => {
    btn.addEventListener("click", () => {
      const id = btn.dataset.endpointId;
      const idx = state.endpoints.findIndex((e) => e.id === id);
      if (idx === -1) return;
      state.endpoints.splice(idx, 1);
      // If the deleted endpoint was routed, fall back to the first remaining.
      const remaining = state.endpoints[0]?.id;
      for (const topic of routingTopics) {
        if (state.routing[topic.key] === id) state.routing[topic.key] = remaining;
      }
      if (state.editingEndpointId === id) state.editingEndpointId = null;
      render();
      saveSettings();
    });
  });

  // Live-update endpoint editor fields. Updates state in place without a full
  // re-render so the user keeps focus while typing; the routing selects and
  // endpoint summary reflect changes on the next render.
  root.querySelectorAll("[data-endpoint-field]").forEach((field) => {
    const id = state.editingEndpointId;
    const ep = state.endpoints.find((e) => e.id === id);
    if (!ep) return;
    field.addEventListener("input", () => {
      const key = field.dataset.endpointField;
      if (key === "headers") {
        ep.headers = textToHeaders(field.value);
      } else if (key === "thinkingCorrection" || key === "contextCompressionEnabled") {
        ep[key] = field.checked;
      } else if (key === "name" || key === "endpoint" || key === "model" || key === "reasoningEffort" || key === "systemPromptAppendage") {
        ep[key] = field.value;
      } else {
        const n = Number(field.value);
        ep[key] = Number.isNaN(n) ? 0 : n;
        const out = root.querySelector(`[data-range-value-for="${key}"]`);
        if (out) out.textContent = ep[key];
      }
      if (key === "reasoningEffort" || key === "thinkingTokenBudget") {
        const budget = root.querySelector('[data-endpoint-field="thinkingTokenBudget"]');
        const correction = root.querySelector('[data-endpoint-field="thinkingCorrection"]');
        if (budget) budget.disabled = ep.reasoningEffort !== "";
        if (correction) correction.disabled = ep.reasoningEffort === "none" || ep.reasoningEffort === "" && Number(ep.thinkingTokenBudget) === 0;
      }
    });
  });

  // Routing topic selection.
  root.querySelectorAll("[data-routing-topic]").forEach((select) => {
    select.addEventListener("change", () => {
      state.routing[select.dataset.routingTopic] = select.value;
      saveSettings();
    });
  });

  // External connection fields. Update state live while typing so focus is
  // preserved, and persist to the server when the field loses focus.
  root.querySelectorAll("[data-external-field]").forEach((field) => {
    field.addEventListener("input", () => {
      state.external[field.dataset.externalField] = field.value;
    });
    field.addEventListener("blur", () => {
      saveSettings();
    });
  });

  root.querySelectorAll("[data-git-setting]").forEach((field) => {
    field.addEventListener("change", () => {
      state.git[field.dataset.gitSetting] = field.checked;
      saveSettings();
    });
  });

  root.querySelectorAll("[data-action='add-mode']").forEach((btn) => {
    btn.addEventListener("click", () => {
      state.editingModeId = null;
      state.modeDraft = { name: "", prompt: "", restricted: false, permissions: {} };
      state.modeStatus = "";
      render();
    });
  });

  root.querySelectorAll("[data-action='edit-mode']").forEach((btn) => {
    btn.addEventListener("click", () => {
      const mode = state.modes.find((item) => item.id === btn.dataset.modeId);
      if (!mode || mode.builtIn) return;
      state.editingModeId = mode.id;
      state.modeDraft = {
        name: mode.name,
        prompt: mode.prompt,
        restricted: Object.keys(mode.permissions || {}).length > 0,
        permissions: structuredClone(mode.permissions || {}),
      };
      state.modeStatus = "";
      render();
    });
  });

  root.querySelectorAll("[data-action='cancel-mode']").forEach((btn) => {
    btn.addEventListener("click", () => {
      state.editingModeId = null;
      state.modeDraft = null;
      state.modeStatus = "";
      render();
    });
  });

  root.querySelectorAll("[data-mode-field]").forEach((field) => {
    field.addEventListener(field.type === "checkbox" ? "change" : "input", () => {
      if (!state.modeDraft) return;
      const key = field.dataset.modeField;
      if (key === "restricted") {
        state.modeDraft.restricted = field.checked;
        if (field.checked && !Object.keys(state.modeDraft.permissions).length && state.modeTools.length) {
          const first = state.modeTools[0].name;
          state.modeDraft.permissions[first] = { name: first, paths: [] };
        }
        render();
      } else {
        state.modeDraft[key] = field.value;
      }
    });
  });

  root.querySelectorAll("[data-mode-tool]").forEach((field) => {
    field.addEventListener("change", () => {
      if (!state.modeDraft) return;
      const name = field.dataset.modeTool;
      if (field.checked) state.modeDraft.permissions[name] = { name, paths: [] };
      else delete state.modeDraft.permissions[name];
      render();
    });
  });

  root.querySelectorAll("[data-mode-paths]").forEach((field) => {
    field.addEventListener("input", () => {
      const permission = state.modeDraft?.permissions[field.dataset.modePaths];
      if (!permission) return;
      permission.paths = field.value.split(",").map((value) => value.trim()).filter(Boolean);
    });
  });

  root.querySelectorAll("[data-action='save-mode']").forEach((btn) => {
    btn.addEventListener("click", saveAgentMode);
  });

  root.querySelectorAll("[data-action='delete-mode']").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const mode = state.modes.find((item) => item.id === btn.dataset.modeId);
      if (!mode || !confirm(`Delete the “${mode.name}” agent mode?`)) return;
      try {
        const data = await del(`/api/agent-modes/${encodeURIComponent(mode.id)}`, { query: { workspaceId: state.modeWorkspaceId } });
        state.modes = data.modes || [];
        state.modeStatus = `Deleted ${mode.name}.`;
        render();
      } catch (err) {
        state.modeStatus = `Error: ${err.message}`;
        render();
      }
    });
  });

  // Live-update the theme color swatch <-> hex pair.
  root.querySelectorAll(".theme-color-swatch").forEach((swatch) => {
    swatch.addEventListener("input", () => {
      const hex = swatch.closest(".theme-color-control").querySelector(".theme-color-hex");
      hex.value = swatch.value;
    });
  });
  root.querySelectorAll(".theme-color-hex").forEach((hex) => {
    hex.addEventListener("input", () => {
      const swatch = hex.closest(".theme-color-control").querySelector(".theme-color-swatch");
      if (/^#[0-9a-fA-F]{6}$/.test(hex.value)) swatch.value = hex.value;
    });
  });

  root.querySelectorAll("[data-action='revoke-session']").forEach((button) => {
    button.addEventListener("click", async () => {
      try {
        await del(`/api/auth/sessions/${encodeURIComponent(button.dataset.sessionId)}`);
        state.securityStatus = "Device session revoked.";
        await loadSecurity();
      } catch (err) {
        state.securityStatus = `Error: ${err.message}`;
        render();
      }
    });
  });

  root.querySelector("[data-action='change-password']")?.addEventListener("click", async () => {
    const currentPassword = root.querySelector("[data-security-field='current-password']")?.value || "";
    const newPassword = root.querySelector("[data-security-field='new-password']")?.value || "";
    const confirmation = root.querySelector("[data-security-field='confirm-password']")?.value || "";
    if (newPassword !== confirmation) {
      state.securityStatus = "Error: New passwords do not match.";
      render();
      return;
    }
    try {
      await put("/api/auth/password", { currentPassword, newPassword });
      state.securityStatus = "Password changed. Other remembered devices were revoked.";
      await loadSecurity();
    } catch (err) {
      state.securityStatus = `Error: ${err.message}`;
      render();
    }
  });

  root.querySelector("[data-action='logout']")?.addEventListener("click", async () => {
    try {
      if (await hasDirtySessions()) {
        const choice = await chooseDirtyLogout();
        if (choice === "save") {
          location.hash = "#/code";
          return;
        }
        if (choice !== "discard") return;
      }
      await logout();
    } catch (err) {
      state.securityStatus = `Error: ${err.message}`;
      render();
    }
  });

  root.querySelectorAll("[data-notification-setting]").forEach((field) => {
    field.addEventListener("change", () => {
      state.messaging[field.dataset.notificationSetting] = field.checked;
      updateCompletionNotificationSettings(buildSettings());
      updatePlanQuestionNotificationSettings(buildSettings());
      if (field.dataset.notificationSetting === "chatCompletionNotifications" && field.checked) {
        void requestCompletionNotificationPermission().then(() => render());
      }
      if (field.dataset.notificationSetting === "planQuestionNotifications" && field.checked) {
        void requestPlanQuestionNotificationPermission().then(() => render());
      }
      saveSettings();
    });
  });

  root.querySelector("[data-action='enable-browser-notifications']")?.addEventListener("click", () => {
    void requestCompletionNotificationPermission().then(() => render());
  });

  root.querySelectorAll("[data-editor-font-size]").forEach((field) => {
    field.addEventListener("change", () => {
      const value = Number.parseFloat(field.value);
      state.editorFontSize = clampEditorFontSize(Number.isNaN(value) ? 0 : value);
      field.value = String(state.editorFontSize);
      saveSettings();
    });
  });

  root.querySelectorAll("[data-research-agent-concurrency]").forEach((field) => {
    field.addEventListener("change", () => {
      const value = Number.parseInt(field.value, 10);
      state.researchAgentConcurrency = Math.max(0, Math.min(8, Number.isNaN(value) ? 0 : value));
      field.value = String(state.researchAgentConcurrency);
      saveSettings();
    });
  });

  root.querySelector("[data-action='rebuild-relaunch']")?.addEventListener("click", async () => {
    if (!window.confirm("Rebuild and relaunch Echo?\n\nThis will rebuild the frontend and server, stop the current instance, and launch the new build. Active chats and terminals will be interrupted.")) return;

    state.rebuild.running = true;
    state.rebuild.status = "Building the Echo frontend and server…";
    state.rebuild.logPath = "";
    render();
    try {
      const result = await post("/api/development/rebuild-relaunch", {});
      state.rebuild.status = "Build succeeded. Waiting for the rebuilt server…";
      state.rebuild.logPath = result.logPath || "";
      render();
      await waitForReplacementServer(result.instanceId);
      state.rebuild.running = false;
      reloadForReplacementServer();
    } catch (err) {
      state.rebuild.running = false;
      state.rebuild.status = `Error: ${err.message}`;
      state.rebuild.logPath = err.payload?.details?.logPath || state.rebuild.logPath;
      render();
    }
  });

  root.querySelector("[data-action='terminate-echo']")?.addEventListener("click", async () => {
    if (!window.confirm("Terminate Echo?\n\nThis will stop the current Echo process without relaunching it. Active chats and terminals will be interrupted.")) return;

    state.terminate.running = true;
    state.terminate.status = "Stopping the Echo process…";
    render();
    try {
      await post("/api/development/terminate", {});
      state.terminate.status = "Echo is shutting down. You can close this browser tab.";
      render();
    } catch (err) {
      state.terminate.running = false;
      state.terminate.status = `Error: ${err.message}`;
      render();
    }
  });

  root.querySelectorAll("[data-action='check-for-updates'], [data-action='retry-update-check']").forEach((button) => {
    button.addEventListener("click", async () => {
      state.update.status = "Checking GitHub master for updates…";
      render();
      const snapshot = await refreshEchoUpdateStatus();
      applyEchoUpdateSnapshot(snapshot);
      state.update.status = snapshot.error
        ? ""
        : snapshot.status?.updateAvailable
          ? "An Echo update is available."
          : "Echo is up to date.";
      render();
    });
  });

  root.querySelector("[data-action='update-echo']")?.addEventListener("click", async () => {
    if (!window.confirm("Update and relaunch Echo?\n\nThis will pull GitHub master, rebuild the frontend and server, stop the current instance, and launch the new build. Active chats and terminals will be interrupted. Local edits are preserved when Git can update cleanly; otherwise the update stops with an error.")) return;

    state.update.running = true;
    state.update.status = "Pulling GitHub master and rebuilding Echo…";
    state.update.logPath = "";
    render();
    try {
      const result = await post("/api/development/update", {});
      state.update.status = "Update succeeded. Waiting for the rebuilt server…";
      state.update.logPath = result.logPath || "";
      render();
      await waitForReplacementServer(result.instanceId);
      state.update.running = false;
      reloadForReplacementServer();
    } catch (err) {
      state.update.running = false;
      state.update.status = `Error: ${err.message}`;
      state.update.logPath = err.payload?.details?.logPath || state.update.logPath;
      render();
    }
  });
}

function capturePluginInstallFields(root) {
  root.querySelectorAll("[data-plugin-install-field]").forEach((field) => {
    state.plugins[field.dataset.pluginInstallField] = field.value;
  });
}

function bindPluginEvents(root) {
  root.querySelectorAll("[data-plugin-source]").forEach((button) => button.addEventListener("click", () => {
    capturePluginInstallFields(root);
    state.plugins.sourceType = button.dataset.pluginSource;
    render();
  }));

  root.querySelectorAll("[data-plugin-action]").forEach((button) => button.addEventListener("click", async () => {
    const action = button.dataset.pluginAction;
    if (!action || state.plugins.busy) return;
    capturePluginInstallFields(root);
    const configuration = action === "save-config" ? collectPluginConfiguration(root, button.dataset.pluginId) : null;
    if (action === "refresh") { await loadPlugins(); return; }
    if (action === "logs") {
      try {
        const data = await get(`/api/plugins/${encodeURIComponent(button.dataset.pluginId)}/logs`);
        state.plugins.logs[button.dataset.pluginId] = data.log || "";
        render();
      } catch (err) {
        state.plugins.status = `Error: ${err.message}`;
        render();
      }
      return;
    }
    if (action === "uninstall" && !window.confirm(`Uninstall ${button.dataset.pluginId}? Plugin configuration and data will be preserved.`)) return;
    if (action === "remove-data" && !window.confirm(`Permanently remove ${button.dataset.pluginId} data, configuration, logs, and stored credentials? This cannot be undone.`)) return;

    state.plugins.busy = true;
    state.plugins.status = action === "approve-stage" ? "Installing reviewed snapshot…" : "Working…";
    render();
    try {
      if (action === "stage-source") {
        const source = state.plugins.sourceType === "local"
          ? { type: "local", path: state.plugins.localPath.trim() }
          : { type: "github", repository: state.plugins.repository.trim(), ref: state.plugins.ref.trim(), subdirectory: state.plugins.subdirectory.trim() };
        await post("/api/plugins/stages", { source });
        state.plugins.status = "Package staged. Review its digest, permissions, and contributions below.";
      } else if (action === "stage-calculator") {
        await post("/api/plugins/stages", { source: { type: "builtin", builtin: "calculator" } });
        state.plugins.status = "Built-in Calculator staged for review.";
      } else if (action === "stage-requirement") {
        await post("/api/plugins/stages", { source: { type: "github", repository: button.dataset.repository, ref: button.dataset.commit, commit: button.dataset.commit, subdirectory: button.dataset.subdirectory } });
        state.plugins.status = "Pinned workspace package staged for review.";
      } else if (action === "approve-stage") {
        const scope = button.dataset.scope;
        await post(`/api/plugins/stages/${encodeURIComponent(button.dataset.stageId)}/approve`, { scope, workspaceId: state.modeWorkspaceId, enable: scope !== "none" });
        state.plugins.status = "Plugin installed from the approved immutable snapshot.";
      } else if (action === "reject-stage") {
        await del(`/api/plugins/stages/${encodeURIComponent(button.dataset.stageId)}`);
        state.plugins.status = "Staged package rejected and removed.";
      } else if (action === "save-config") {
        await put(`/api/plugins/${encodeURIComponent(button.dataset.pluginId)}/config`, configuration);
        state.plugins.status = "Plugin configuration saved.";
      } else if (action === "remove-data") {
        await post(`/api/plugins/${encodeURIComponent(button.dataset.pluginId)}/remove-data`, {});
        state.plugins.logs[button.dataset.pluginId] = undefined;
        state.plugins.status = "Plugin data and known credential-store entries removed.";
      } else {
		const response = await post(`/api/plugins/${encodeURIComponent(button.dataset.pluginId)}/actions`, { action, workspaceId: state.modeWorkspaceId });
		if (action === "reload") {
			const stage = response.stage;
			const contractChanged = stage?.diff?.permissionsChanged || stage?.diff?.toolContractsChanged;
			if (stage && !contractChanged && window.confirm(`Reload ${stage.validation.manifest.name} from its local source?\n\nDigest: ${stage.validation.digest}\n\n${stage.validation.manifest.runtime ? "Its native backend runs with your OS account permissions." : "Its UI remains sandboxed."}`)) {
				await post(`/api/plugins/stages/${encodeURIComponent(stage.id)}/approve`, { scope: "none", workspaceId: state.modeWorkspaceId, enable: false });
				state.plugins.status = "Local snapshot reloaded from the explicitly approved source; prior activation was preserved.";
			} else {
				state.plugins.status = contractChanged ? "Permission or agent-tool contracts changed. Review the candidate in full before installation." : "Reload candidate staged for review.";
			}
		} else {
			state.plugins.status = action === "update-check" ? "Candidate snapshot staged. Review it before installation." : "Plugin state updated.";
		}
      }
      await loadPlugins(false);
    } catch (err) {
      state.plugins.status = `Error: ${err.message}`;
    } finally {
      state.plugins.busy = false;
      render();
    }
  }));
}

function collectPluginConfiguration(root, pluginId) {
  const values = {};
  root.querySelectorAll(`[data-plugin-setting="${CSS.escape(pluginId)}"]`).forEach((field) => {
    const key = field.dataset.settingKey;
    if (field.dataset.settingType === "boolean") values[key] = field.checked;
    else if (field.dataset.settingType === "number") {
      if (field.value !== "") values[key] = Number(field.value);
    }
    else values[key] = field.value;
  });
  const secrets = {};
  root.querySelectorAll(`[data-plugin-secret="${CSS.escape(pluginId)}"]`).forEach((fieldset) => {
    const source = fieldset.querySelector("[data-secret-part='source']")?.value || "";
    const value = fieldset.querySelector("[data-secret-part='value']")?.value || "";
    const environment = fieldset.querySelector("[data-secret-part='environment']")?.value || "";
    if (source === "clear" || source === "environment" && environment || (source === "os" || source === "session") && value) {
      secrets[fieldset.dataset.settingKey] = { source, value, environment };
    }
  });
  return { workspaceId: state.modeWorkspaceId, values, secrets };
}

function lspDraft(profile = {}) {
  const environment = profile.environment || {};
  return {
    id: profile.id || "",
    name: profile.name || "",
    command: profile.command || "",
    argsText: (profile.args || []).join("\n"),
    selectors: (profile.selectors?.length ? profile.selectors : [{ languageId: "", extensions: [], filenames: [] }]).map((selector) => ({
      languageId: selector.languageId || "",
      extensionsText: (selector.extensions || []).join(", "),
      filenamesText: (selector.filenames || []).join(", "),
    })),
    environmentText: Object.entries(environment).map(([key, value]) => `${key}=${value}`).join("\n"),
    initializationOptionsText: JSON.stringify(profile.initializationOptions || {}, null, 2),
    settingsText: JSON.stringify(profile.settings || {}, null, 2),
  };
}

function parseJSONObject(text, label) {
  const value = JSON.parse(text.trim() || "{}");
  if (!value || Array.isArray(value) || typeof value !== "object") throw new Error(`${label} must be a JSON object.`);
  return value;
}

function profileFromLSPDraft(draft) {
  const environment = {};
  for (const line of draft.environmentText.split(/\r?\n/).map((value) => value.trim()).filter(Boolean)) {
    const equals = line.indexOf("=");
    if (equals <= 0) throw new Error(`Environment line must use KEY=VALUE: ${line}`);
    environment[line.slice(0, equals).trim()] = line.slice(equals + 1);
  }
  const splitMatches = (text) => text.split(",").map((value) => value.trim()).filter(Boolean);
  return {
    id: draft.id.trim().toLowerCase(),
    name: draft.name.trim(),
    command: draft.command.trim(),
    args: draft.argsText.split(/\r?\n/).filter((value) => value.length > 0),
    selectors: draft.selectors.map((selector) => ({
      languageId: selector.languageId.trim(),
      extensions: splitMatches(selector.extensionsText),
      filenames: splitMatches(selector.filenamesText),
    })),
    environment,
    initializationOptions: parseJSONObject(draft.initializationOptionsText, "Initialization options"),
    settings: parseJSONObject(draft.settingsText, "Settings"),
  };
}

function applyLSPWorkspaceResponse(data, resetOverrides = true) {
  state.lsp.config = {
    enabledProfileIds: data.config?.enabledProfileIds || [],
    overrides: data.config?.overrides || {},
    formatOnSave: data.config?.formatOnSave === true,
    formatOnSaveTimeoutMs: data.config?.formatOnSaveTimeoutMs || 3000,
  };
  state.lsp.effectiveProfiles = data.profiles || [];
  state.lsp.statuses = data.statuses || [];
  if (resetOverrides) state.lsp.overridesText = JSON.stringify(state.lsp.config.overrides, null, 2);
}

async function loadLanguageServers(preserveStatus = false) {
  const previousStatus = state.lsp.status;
  try {
    const global = await get("/api/lsp/profiles");
    state.lsp.profiles = global.profiles || [];
    state.lsp.templates = global.templates || [];
    if (state.modeWorkspaceId) {
      applyLSPWorkspaceResponse(await get(`/api/workspaces/${encodeURIComponent(state.modeWorkspaceId)}/lsp/config`));
    } else {
      applyLSPWorkspaceResponse({ config: {}, profiles: [], statuses: [] });
    }
    state.lsp.status = preserveStatus ? previousStatus : "";
  } catch (err) {
    state.lsp.status = `Error: ${err.message}`;
  }
  if (mountedRoot) render();
}

async function saveLSPWorkspaceConfig(parseOverrides = true) {
  if (!state.modeWorkspaceId) return;
  const config = { ...state.lsp.config };
  if (parseOverrides) config.overrides = parseJSONObject(state.lsp.overridesText, "Profile overrides");
  const data = await put(`/api/workspaces/${encodeURIComponent(state.modeWorkspaceId)}/lsp/config`, { config });
  applyLSPWorkspaceResponse(data, parseOverrides);
}

function bindLSPEvents(root) {
  root.querySelectorAll("[data-lsp-field]").forEach((field) => {
    field.addEventListener("input", () => {
      if (state.lsp.draft) state.lsp.draft[field.dataset.lspField] = field.value;
    });
  });
  root.querySelectorAll("[data-lsp-selector-field]").forEach((field) => {
    field.addEventListener("input", () => {
      const selector = state.lsp.draft?.selectors[Number(field.dataset.selectorIndex)];
      if (selector) selector[field.dataset.lspSelectorField] = field.value;
    });
  });
  root.querySelector("[data-lsp-overrides]")?.addEventListener("input", (event) => { state.lsp.overridesText = event.currentTarget.value; });
  root.querySelectorAll("[data-lsp-config]").forEach((field) => {
    field.addEventListener("change", () => {
      const key = field.dataset.lspConfig;
      state.lsp.config[key] = field.type === "checkbox" ? field.checked : Math.max(250, Math.min(30000, Number(field.value) || 3000));
    });
  });
  root.querySelectorAll("[data-lsp-enable]").forEach((field) => {
    field.addEventListener("change", async () => {
      const ids = new Set(state.lsp.config.enabledProfileIds || []);
      if (field.checked) ids.add(field.dataset.lspEnable);
      else ids.delete(field.dataset.lspEnable);
      state.lsp.config.enabledProfileIds = [...ids];
      try {
        await saveLSPWorkspaceConfig(false);
        state.lsp.status = `${field.checked ? "Enabled" : "Disabled"} ${field.dataset.lspEnable}.`;
      } catch (err) {
        state.lsp.status = `Error: ${err.message}`;
        await loadLanguageServers(true);
        return;
      }
      render();
    });
  });
  root.querySelectorAll("[data-lsp-action]").forEach((button) => button.addEventListener("click", async () => {
    const action = button.dataset.lspAction;
    try {
      if (action === "new-profile") {
        state.lsp.editingId = null;
        state.lsp.draft = lspDraft();
        state.lsp.status = "";
        render();
        return;
      }
      if (action === "edit-profile") {
        const profile = state.lsp.profiles.find((item) => item.id === button.dataset.profileId);
        if (profile) {
          state.lsp.editingId = profile.id;
          state.lsp.draft = lspDraft(profile);
          state.lsp.status = "";
          render();
        }
        return;
      }
      if (action === "cancel-profile") {
        state.lsp.editingId = null;
        state.lsp.draft = null;
        render();
        return;
      }
      if (action === "add-selector") {
        state.lsp.draft?.selectors.push({ languageId: "", extensionsText: "", filenamesText: "" });
        render();
        return;
      }
      if (action === "remove-selector") {
        if (state.lsp.draft?.selectors.length > 1) state.lsp.draft.selectors.splice(Number(button.dataset.selectorIndex), 1);
        render();
        return;
      }
      state.lsp.busy = true;
      if (action === "add-template") {
        const result = await post("/api/lsp/profiles", { templateId: button.dataset.templateId });
        state.lsp.status = `Created ${result.profile.name}. Enable it below to start the server.`;
      } else if (action === "save-profile") {
        const profile = profileFromLSPDraft(state.lsp.draft);
        if (state.lsp.editingId) await put(`/api/lsp/profiles/${encodeURIComponent(state.lsp.editingId)}`, { profile });
        else await post("/api/lsp/profiles", { profile });
        state.lsp.status = `Saved ${profile.name}.`;
        state.lsp.editingId = null;
        state.lsp.draft = null;
      } else if (action === "delete-profile") {
        const profile = state.lsp.profiles.find((item) => item.id === button.dataset.profileId);
        if (!profile || !confirm(`Delete the “${profile.name}” language-server profile?`)) return;
        await del(`/api/lsp/profiles/${encodeURIComponent(profile.id)}`);
        state.lsp.status = `Deleted ${profile.name}.`;
      } else if (action === "save-workspace") {
        await saveLSPWorkspaceConfig(true);
        state.lsp.status = "Workspace language-server settings saved.";
      } else if (action === "restart") {
        await post(`/api/workspaces/${encodeURIComponent(state.modeWorkspaceId)}/lsp/${encodeURIComponent(button.dataset.profileId)}/restart`, {});
        state.lsp.status = `Restarting ${button.dataset.profileId}…`;
      }
      await loadLanguageServers(true);
    } catch (err) {
      state.lsp.status = `Error: ${err.message}`;
      render();
    } finally {
      state.lsp.busy = false;
    }
  }));
}

async function loadGoTesting(preserveStatus = false) {
  const previousStatus = state.testing.status;
  try {
    const data = state.modeWorkspaceId
      ? await get(`/api/workspaces/${encodeURIComponent(state.modeWorkspaceId)}/testing/go/config`)
      : { config: { codeLens: true, coverage: true, timeout: "30s", flags: [], tags: "", environment: {} } };
    state.testing.config = {
      codeLens: data.config?.codeLens !== false,
      coverage: data.config?.coverage !== false,
      timeout: data.config?.timeout || "30s",
      flags: data.config?.flags || [],
      tags: data.config?.tags || "",
      environment: data.config?.environment || {},
    };
    state.testing.flagsText = JSON.stringify(state.testing.config.flags, null, 2);
    state.testing.environmentText = JSON.stringify(state.testing.config.environment, null, 2);
    state.testing.status = preserveStatus ? previousStatus : "";
  } catch (err) {
    state.testing.status = `Error: ${err.message}`;
  }
  if (mountedRoot) render();
}

function bindGoTestingEvents(root) {
  root.querySelectorAll("[data-go-testing-field]").forEach((field) => {
    field.addEventListener("input", () => {
      const key = field.dataset.goTestingField;
      state.testing.config[key] = field.type === "checkbox" ? field.checked : field.value;
    });
  });
  root.querySelectorAll("[data-go-testing-text]").forEach((field) => {
    field.addEventListener("input", () => { state.testing[`${field.dataset.goTestingText}Text`] = field.value; });
  });
  root.querySelector("[data-go-testing-action='save']")?.addEventListener("click", async () => {
    if (!state.modeWorkspaceId) return;
    state.testing.busy = true;
    try {
      const flags = JSON.parse(state.testing.flagsText.trim() || "[]");
      if (!Array.isArray(flags) || flags.some((value) => typeof value !== "string")) throw new Error("Test flags must be a JSON array of strings.");
      const environment = parseJSONObject(state.testing.environmentText, "Environment");
      if (Object.values(environment).some((value) => typeof value !== "string")) throw new Error("Environment values must be strings.");
      const config = { ...state.testing.config, flags, environment };
      const data = await put(`/api/workspaces/${encodeURIComponent(state.modeWorkspaceId)}/testing/go/config`, { config });
      state.testing.config = data.config;
      state.testing.flagsText = JSON.stringify(data.config.flags || [], null, 2);
      state.testing.environmentText = JSON.stringify(data.config.environment || {}, null, 2);
      state.testing.status = "Workspace Go testing settings saved.";
    } catch (err) {
      state.testing.status = `Error: ${err.message}`;
    } finally {
      state.testing.busy = false;
      render();
    }
  });
}

export function mount(root) {
  mountedRoot = root;
  const requestedSection = new URLSearchParams(location.hash.split("?")[1] || "").get("section");
  if (sections.some((section) => section.id === requestedSection)) state.activeSection = requestedSection;
  applyEchoUpdateSnapshot(getEchoUpdateSnapshot());
  pluginCatalogListener = (event) => {
    state.plugins.catalog = event.detail;
    if (state.activeSection === "plugins") render();
  };
  updateStatusListener = (event) => {
    applyEchoUpdateSnapshot(event.detail);
    render();
  };
  window.addEventListener("echo:plugin-catalog", pluginCatalogListener);
  window.addEventListener("echo:update-status", updateStatusListener);
  render();
  loadSettings();
  loadAgentModes();
  loadSecurity();
  loadPlugins();
}

export function unmount() {
  // Async settings requests may complete after another view has mounted. Do
  // not let their completion render Settings over that active view.
  closeSettingsWorkspaceDropdown?.();
  closeSettingsWorkspaceDropdown = null;
  closeSettingsAddWorkspaceModal?.();
  closeSettingsAddWorkspaceModal = null;
  closeSettingsEditWorkspaceModal?.();
  closeSettingsEditWorkspaceModal = null;
  disposeSettingsChatMap?.();
  disposeSettingsChatMap = null;
  if (pluginCatalogListener) window.removeEventListener("echo:plugin-catalog", pluginCatalogListener);
  pluginCatalogListener = null;
  if (updateStatusListener) window.removeEventListener("echo:update-status", updateStatusListener);
  updateStatusListener = null;
  mountedRoot = null;
}

function applyEchoUpdateSnapshot(snapshot) {
  const available = snapshot.status?.updateAvailable === true;
  const priorCheckResult = state.update.status === "Echo is up to date."
    || state.update.status === "An Echo update is available.";
  if (priorCheckResult && state.update.available !== available) state.update.status = "";
  state.update.available = available;
  state.update.checking = Boolean(snapshot.checking);
  state.update.checkError = snapshot.error || "";
}

function chooseDirtyLogout() {
  return new Promise((resolve) => {
    const dialog = document.createElement("dialog");
    dialog.className = "settings-confirm-dialog";
    dialog.innerHTML = `
      <form method="dialog">
        <h2>Unsaved code changes</h2>
        <p>Signing out clears the browser's recoverable editor buffers. Return to Code to save them, discard them and sign out, or cancel.</p>
        <div class="settings-confirm-actions">
          <button class="secondary-button" type="button" data-choice="cancel">Cancel</button>
          <button class="secondary-button" type="button" data-choice="save">Return to Code</button>
          <button class="secondary-button danger-button" type="button" data-choice="discard">Discard &amp; Sign Out</button>
        </div>
      </form>
    `;
    const finish = (choice) => {
      dialog.close();
      dialog.remove();
      resolve(choice);
    };
    dialog.querySelectorAll("[data-choice]").forEach((button) => {
      button.addEventListener("click", () => finish(button.dataset.choice));
    });
    dialog.addEventListener("cancel", (event) => {
      event.preventDefault();
      finish("cancel");
    }, { once: true });
    document.body.append(dialog);
    dialog.showModal();
  });
}

// ---- Persistence ----

// captureExternalFields reads any external connection inputs currently in the
// DOM into state. This is used on navigation (back button, switching sections)
// so edits are not lost even if a field never fired a blur event.
function captureExternalFields(root) {
  root.querySelectorAll("[data-external-field]").forEach((field) => {
    state.external[field.dataset.externalField] = field.value;
  });
}

// applySettings copies the loaded settings into the view state and re-renders.
function applySettings(cfg) {
  // The server returns { settings: <llm.Settings>, storagePath }; tolerate both
  // the nested and flat shapes.
  const s = cfg.settings || cfg;
  state.rawSettings = { ...s };
  state.settingsLoaded = true;
  state.endpoints = (s.endpoints || []).map((e) => ({
    ...e,
    reasoningEffort: e.reasoningEffort || "",
    contextCompressionEnabled: e.contextCompressionEnabled !== false,
    contextCompressionThresholdPercent: Number(e.contextCompressionThresholdPercent) || 70,
    headers: e.headers || {},
  }));
  state.routing = {
    chat: s.endpointSelection?.chat || state.endpoints[0]?.id || "",
    research: s.endpointSelection?.research || state.endpoints[0]?.id || "",
    vision: s.endpointSelection?.vision || state.endpoints[0]?.id || "",
    inlineCode: s.endpointSelection?.inlineCode || state.endpoints[0]?.id || "",
  };
  state.storagePath = cfg.storagePath || "";
  state.external = {
    searxngUrl: s.searxngUrl || "",
    comfyuiUrl: s.comfyuiUrl || "",
    comfyuiTxt2imgWorkflow: s.comfyuiTxt2imgWorkflow || "",
    comfyuiImg2imgWorkflow: s.comfyuiImg2imgWorkflow || "",
    comfyuiVideoWorkflow: s.comfyuiVideoWorkflow || "",
  };
  state.editorFontSize = clampEditorFontSize(Number(s.editorFontSize) || 13.5);
  state.researchAgentConcurrency = Math.max(0, Math.min(8, Number(s.researchAgentConcurrency ?? 4) || 0));
  state.git = {
    leadingWhitespaceIndicators: s.hideLeadingWhitespaceIndicators !== true,
    splitDiffView: s.disableGitSplitDiffView !== true,
  };
  state.messaging = {
    notificationSounds: s.disableNotificationSounds !== true,
    planQuestionSounds: s.disablePlanQuestionSounds !== true,
    planQuestionNotifications: s.enablePlanQuestionNotifications !== false,
    chatCompletionNotifications: s.enableChatCompletionNotifications !== false,
  };
  updateCompletionNotificationSettings(s);
  updatePlanQuestionNotificationSettings(s);
  render();
}

// loadSettings fetches the current settings from the server and populates state.
async function loadSettings() {
  try {
    const data = await get("/api/settings");
    applySettings(data);
  } catch (err) {
    state.saveStatus = `Failed to load settings: ${err.message}`;
    render();
  }
}

async function loadSecurity() {
  try {
    const [statusData, sessionData] = await Promise.all([
      get("/api/auth/status"),
      get("/api/auth/sessions"),
    ]);
    state.transportSecure = Boolean(statusData.transportSecure);
    state.authSessions = sessionData.sessions || [];
    render();
  } catch (err) {
    state.securityStatus = `Error: Failed to load security settings: ${err.message}`;
    render();
  }
}

async function loadAgentModes() {
  try {
    const workspaceData = await get("/api/workspaces");
    state.workspaces = workspaceData.workspaces || [];
    state.modeWorkspaceId = workspaceData.activeId || "";
    state.modeWorkspaceName = state.workspaces.find((workspace) => workspace.id === state.modeWorkspaceId)?.name || "";
    if (!state.modeWorkspaceId) {
      state.modes = [];
      state.modeTools = [];
      await loadLanguageServers();
      await loadGoTesting();
      return;
    }
    const data = await get("/api/agent-modes", { query: { workspaceId: state.modeWorkspaceId } });
    state.modes = data.modes || [];
    state.modeTools = data.tools || [];
    state.modeStatus = "";
    await loadLanguageServers();
    await loadGoTesting();
    render();
  } catch (err) {
    state.modeStatus = `Error: ${err.message}`;
    render();
  }
}

async function saveAgentMode() {
  const draft = state.modeDraft;
  if (!draft) return;
  if (!draft.name.trim() || !draft.prompt.trim()) {
    state.modeStatus = "Error: Name and system instructions are required.";
    render();
    return;
  }
  if (draft.restricted && !Object.keys(draft.permissions).length) {
    state.modeStatus = "Error: Select at least one tool, or turn off tool restrictions.";
    render();
    return;
  }
  const mode = {
    name: draft.name.trim(),
    prompt: draft.prompt.trim(),
    permissions: draft.restricted ? draft.permissions : {},
  };
  try {
    const data = state.editingModeId
      ? await put(`/api/agent-modes/${encodeURIComponent(state.editingModeId)}`, { workspaceId: state.modeWorkspaceId, mode })
      : await post("/api/agent-modes", { workspaceId: state.modeWorkspaceId, mode });
    state.modes = data.modes || [];
    state.modeStatus = `Saved ${mode.name}.`;
    state.editingModeId = null;
    state.modeDraft = null;
    render();
  } catch (err) {
    state.modeStatus = `Error: ${err.message}`;
    render();
  }
}

// buildSettings serializes the current view state into an llm.Settings payload
// shaped like the Go Settings struct.
function buildSettings() {
  return {
    ...state.rawSettings,
    endpoints: state.endpoints.map((e) => ({ ...e })),
    endpointSelection: {
      chat: state.routing.chat,
      research: state.routing.research,
      vision: state.routing.vision,
      inlineCode: state.routing.inlineCode,
    },
    searxngUrl: state.external.searxngUrl,
    comfyuiUrl: state.external.comfyuiUrl,
    comfyuiTxt2imgWorkflow: state.external.comfyuiTxt2imgWorkflow,
    comfyuiImg2imgWorkflow: state.external.comfyuiImg2imgWorkflow,
    comfyuiVideoWorkflow: state.external.comfyuiVideoWorkflow,
    hideLeadingWhitespaceIndicators: !state.git.leadingWhitespaceIndicators,
    disableGitSplitDiffView: !state.git.splitDiffView,
    disableNotificationSounds: !state.messaging.notificationSounds,
    disablePlanQuestionSounds: !state.messaging.planQuestionSounds,
    enablePlanQuestionNotifications: state.messaging.planQuestionNotifications,
    enableChatCompletionNotifications: state.messaging.chatCompletionNotifications,
    editorFontSize: state.editorFontSize,
    researchAgentConcurrency: state.researchAgentConcurrency,
  };
}

async function loadPlugins(renderAfter = true) {
  try {
    state.plugins.catalog = await refreshPluginCatalog();
    if (renderAfter) render();
  } catch (err) {
    state.plugins.status = `Error: Failed to load plugins: ${err.message}`;
    if (renderAfter) render();
  }
}

// saveSettings persists the current view state to the server and refreshes
// state from the normalized response.
async function saveSettings() {
  if (!state.settingsLoaded) return;
  try {
    const data = await put("/api/settings", { settings: buildSettings() });
    applySettings(data);
  } catch (err) {
    state.saveStatus = `Save failed: ${err.message}`;
    render();
  }
}
