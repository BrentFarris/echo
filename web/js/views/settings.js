// views/settings.js — Echo settings screen.
//
// A redesigned, full-page settings view (not the old modal overlay). Sections
// are selected from a left sidebar and their controls render on the right.
//
// LLM Endpoints and Agent Modes are functional. The remaining sections are
// incrementally replacing the earlier visual stubs.

import { icons } from "../icons.js";
import { del, get, post, put } from "../api.js";

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

// ---- Sections ----
const sections = [
  { id: "llm", label: "LLM Endpoints", icon: icons.settings },
  { id: "modes", label: "Agent Modes", icon: icons.tasks },
  { id: "external", label: "External Connections", icon: icons.git },
  { id: "messaging", label: "Messaging", icon: icons.mic },
  { id: "git", label: "Git", icon: icons.git },
  { id: "theme", label: "Theme", icon: icons.dashboard },
  { id: "workspaces", label: "Workspaces", icon: icons.code },
  { id: "development", label: "Development", icon: icons.execute },
];

// Routing topics for endpoint selection.
const routingTopics = [
  { key: "chat", label: "Chat" },
  { key: "research", label: "Research" },
  { key: "vision", label: "Vision" },
  { key: "inlineCode", label: "Inline Code" },
];

// ---- State ----
const state = {
  activeSection: "llm",
  themePalette: "light",
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
  // External connection settings (SearXNG + ComfyUI).
  external: {
    searxngUrl: "",
    comfyuiUrl: "",
    comfyuiTxt2imgWorkflow: "",
    comfyuiImg2imgWorkflow: "",
  },
  storagePath: "",
  saveStatus: "",
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
    thinkingTokenBudget: -1,
    thinkingCorrection: false,
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
          <input type="number" min="0" max="8" step="1" value="4" />
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
      <input type="number" step="${opts.step ?? "any"}" min="${opts.min ?? ""}" max="${opts.max ?? ""}" value="${e[key]}" data-endpoint-field="${key}" />
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
        ${num("maxTokens", "Max Tokens", { min: 1, step: 1 })}
        ${num("frequencyPenalty", "Frequency Penalty", { min: -2, max: 2, step: 0.01 })}
        ${num("presencePenalty", "Presence Penalty", { min: -2, max: 2, step: 0.01 })}
        ${num("repetitionPenalty", "Repetition Penalty", { min: 0, step: 0.01 })}
        ${num("timeoutSeconds", "Timeout (seconds)", { min: 1, step: 1 })}
        ${num("thinkingTokenBudget", "Thinking Token Budget", { min: -1, step: 1 })}
      </div>

      <label class="settings-toggle">
        <span>Thinking correction</span>
        <input type="checkbox" ${e.thinkingCorrection ? "checked" : ""} data-endpoint-field="thinkingCorrection" />
      </label>

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
        <p class="settings-card-help">Remote ComfyUI instance for image generation.</p>
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
        </div>
      </div>
    </section>
  `;
}

function renderMessaging() {
  const toggles = [
    { label: "Notification sounds" },
    { label: "Chat completion notifications" },
  ];
  return `
    <section class="settings-section">
      <h2 class="settings-section-title">Messaging</h2>
      <div class="settings-card">
        <h3 class="settings-card-title">Notifications</h3>
        ${toggles.map((t) => `
          <label class="settings-toggle">
            <span>${esc(t.label)}</span>
            <input type="checkbox" ${t.checked ? "checked" : ""} />
          </label>
        `).join("")}
      </div>
    </section>
  `;
}

function renderGit() {
  const toggles = [
    { label: "Leading whitespace indicators", help: "Show leading whitespace changes in Git diffs." },
    { label: "Split Git diff view", help: "Use a side-by-side diff layout on wide windows." },
  ];
  return `
    <section class="settings-section">
      <h2 class="settings-section-title">Git</h2>
      <div class="settings-card">
        ${toggles.map((t) => `
          <label class="settings-toggle" title="${esc(t.help)}">
            <span>${esc(t.label)}</span>
            <input type="checkbox" ${t.checked ? "checked" : ""} />
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
      <h2 class="settings-section-title">Workspaces</h2>
      <div class="settings-card">
        ${state.workspaces.length ? state.workspaces.map((w) => `
          <div class="workspace-row">
            <div class="workspace-row-heading">
              <span class="workspace-icon-label">${esc(w.name[0].toUpperCase())}</span>
              <div>
                <strong>${esc(w.name)}</strong>
                <span class="workspace-row-path">${esc(w.mainPath)}</span>
              </div>
            </div>
            <div class="endpoint-row-actions">
              <button class="icon-button" type="button" title="Configure">${icons.settings}</button>
            </div>
          </div>
        `).join("") : `<p class="empty-state compact">No workspaces added.</p>`}
      </div>
    </section>
  `;
}

function renderDevelopment() {
  return `
    <section class="settings-section">
      <h2 class="settings-section-title">Development</h2>

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
        <button class="secondary-button danger-button" type="button" data-action="rebuild-relaunch">Rebuild &amp; Relaunch</button>
      </div>
    </section>
  `;
}

const renderers = {
  llm: renderLLMEndpoints,
  modes: renderAgentModes,
  external: renderExternal,
  messaging: renderMessaging,
  git: renderGit,
  theme: renderTheme,
  workspaces: renderWorkspaces,
  development: renderDevelopment,
};

function renderContent() {
  return renderers[state.activeSection]();
}

function render() {
  const root = document.getElementById("app");
  root.innerHTML = `
    <div class="settings-view">
      <nav class="settings-sidebar" aria-label="Settings sections">
        <div class="settings-sidebar-header">
          <button class="settings-back-button" type="button" data-action="back-to-chat" title="Back to chat" aria-label="Back to main interface">
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
                class="settings-nav-button ${state.activeSection === s.id ? "is-active" : ""}"
                type="button"
                data-section="${s.id}"
              >${s.icon}<span>${esc(s.label)}</span></button>
            </li>
          `).join("")}
        </ul>
      </nav>
      <main class="settings-content">
        ${renderContent()}
      </main>
    </div>
  `;
  bindEvents(root);
}

function bindEvents(root) {
  root.querySelectorAll("[data-section]").forEach((btn) => {
    btn.addEventListener("click", () => {
      // Persist any in-progress external connection edits before switching away.
      captureExternalFields(root);
      saveSettings();
      state.activeSection = btn.dataset.section;
      render();
    });
  });

  root.querySelectorAll("[data-action='set-theme-palette']").forEach((btn) => {
    btn.addEventListener("click", () => {
      state.themePalette = btn.dataset.themePalette;
      render();
    });
  });

  // Navigate back to the main chat interface.
  root.querySelectorAll("[data-action='back-to-chat']").forEach((btn) => {
    btn.addEventListener("click", () => {
      // Persist any in-progress external connection edits before leaving.
      captureExternalFields(root);
      saveSettings();
      location.hash = "#/";
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
      } else if (key === "thinkingCorrection") {
        ep.thinkingCorrection = field.checked;
      } else if (key === "name" || key === "endpoint" || key === "model" || key === "systemPromptAppendage") {
        ep[key] = field.value;
      } else {
        const n = Number(field.value);
        ep[key] = Number.isNaN(n) ? 0 : n;
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
}

export function mount(root) {
  render();
  loadSettings();
  loadAgentModes();
}

export function unmount() {
  // No persistent listeners outside the re-rendered DOM.
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
  state.endpoints = (s.endpoints || []).map((e) => ({ ...e, headers: e.headers || {} }));
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
  };
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

async function loadAgentModes() {
  try {
    const workspaceData = await get("/api/workspaces");
    state.workspaces = workspaceData.workspaces || [];
    state.modeWorkspaceId = workspaceData.activeId || "";
    state.modeWorkspaceName = state.workspaces.find((workspace) => workspace.id === state.modeWorkspaceId)?.name || "";
    if (!state.modeWorkspaceId) {
      state.modes = [];
      state.modeTools = [];
      render();
      return;
    }
    const data = await get("/api/agent-modes", { query: { workspaceId: state.modeWorkspaceId } });
    state.modes = data.modes || [];
    state.modeTools = data.tools || [];
    state.modeStatus = "";
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
  };
}

// saveSettings persists the current view state to the server and refreshes
// state from the normalized response.
async function saveSettings() {
  try {
    const data = await put("/api/settings", { settings: buildSettings() });
    applySettings(data);
  } catch (err) {
    state.saveStatus = `Save failed: ${err.message}`;
    render();
  }
}
