
import { LoadWebAccessStatus, SaveSettings, SaveWebAccessSettings, SetWorkspaceFolderUseAgents, SetWorkspaceLetter } from "../../backend/services";
import { llm, services } from "../../../wailsjs/go/models";
import { getAppCallbacks } from "../callbacks";
import { icons } from "../icons";
import { renderQRCodeSVG } from "../qr";
import { cloneSettings, cloneWebAccessSettings, fieldValue, leadingWhitespaceIndicatorsEnabled, notificationSoundsEnabled, state } from "../state";
import { applyTheme, normalizeHexColor, settingsWithCompactTheme, settingsWithThemeColor, themeColorValue, themeGroups, themeTokens, type ThemePaletteName } from "../theme";
import { pushToast } from "../toasts";
import { errorMessage, escapeAttribute, escapeHtml, workspaceFolderSummary } from "../utils";
import { hydrateWorkspaceLetterDrafts, renderWorkspaceFolderSettings, renderWorkspaceIcon, workspaceLetterDraft } from "../workspace";

const llmPresetFields = [
  "temperature",
  "topK",
  "topP",
  "minP",
  "contextLength",
  "maxTokens",
  "frequencyPenalty",
  "presencePenalty",
  "repetitionPenalty",
  "timeoutSeconds",
  "thinkingTokenBudget",
  "thinkingCorrection",
] as const;

type LLMPresetField = (typeof llmPresetFields)[number];
type LLMPresetValues = Pick<llm.LLMEndpoint, LLMPresetField>;

const llmCodingPresets: {
  id: string;
  label: string;
  values: LLMPresetValues;
}[] = [
  {
    id: "qwen3_6",
    label: "Qwen3.6",
    values: {
      temperature: 0.6,
      topK: 20,
      topP: 0.95,
      minP: 0,
      contextLength: 262144,
      maxTokens: 32168,
      frequencyPenalty: 0,
      presencePenalty: 1.5,
      repetitionPenalty: 1.05,
      timeoutSeconds: 600,
      thinkingTokenBudget: -1,
      thinkingCorrection: false,
    },
  },
  {
    id: "gemma4",
    label: "Gemma 4",
    values: {
      temperature: 0.2,
      topK: 64,
      topP: 0.95,
      minP: 0,
      contextLength: 262144,
      maxTokens: 32168,
      frequencyPenalty: 0,
      presencePenalty: 0,
      repetitionPenalty: 1,
      timeoutSeconds: 600,
      thinkingTokenBudget: -1,
      thinkingCorrection: false,
    },
  },
];

const endpointTopics = [
  { key: "chat", label: "Chat" },
  { key: "kanban", label: "Kanban" },
  { key: "inlineCode", label: "Inline code" },
] as const;

type EndpointTopic = (typeof endpointTopics)[number]["key"];

export function bindSettingsEvents(root: ParentNode) {
  const form = root.querySelector<HTMLFormElement>("[data-settings-form]");
  form?.addEventListener("submit", handleSettingsSubmit);
  form
    ?.querySelectorAll<HTMLInputElement>("input")
    .forEach((input) => input.addEventListener("input", handleSettingsInput));
  form
    ?.querySelectorAll<HTMLSelectElement>("select")
    .forEach((select) => {
      if (select.dataset.llmEndpointPreset !== undefined) {
        select.addEventListener("change", () => handleLLMEndpointPresetChange(select));
      } else {
        select.addEventListener("change", handleSettingsInput);
      }
    });
  form
    ?.querySelectorAll<HTMLInputElement>("[data-workspace-folder-agents]")
    .forEach((input) =>
      input.addEventListener("change", () => {
        void handleWorkspaceFolderAgentsChange(input);
      }),
    );
}

export function renderSettingsOverlay(workspaces: services.Workspace[]): string {
  const endpoints = settingsEndpoints(state.settingsDraft);
  const hasSettingsValues = endpoints.some((endpoint) =>
    endpoint.endpoint.trim() || endpoint.model.trim(),
  );
  return `
    <div class="overlay" role="dialog" aria-modal="true" aria-labelledby="settings-title">
      <form class="settings-panel" data-settings-form>
        <header class="settings-header">
          <div>
            <p class="eyebrow">Settings</p>
            <h2 id="settings-title">Settings</h2>
          </div>
          <button class="icon-button close-button" type="button" title="Close" aria-label="Close settings" data-action="close-settings">
            ${icons.x}
          </button>
        </header>

        ${state.formError ? `<p class="form-error" role="alert">${escapeHtml(state.formError)}</p>` : ""}
        ${hasSettingsValues ? "" : `<p class="empty-state compact">No settings are loaded. Add an OpenAI-compatible endpoint and model to recover.</p>`}

        <section class="settings-section" aria-labelledby="llm-endpoints-title">
          <div class="settings-section-heading">
            <h3 id="llm-endpoints-title" class="settings-section-title">LLM Endpoints</h3>
            <button class="secondary-button compact-button" type="button" data-action="add-llm-endpoint">
              ${icons.plus}
              <span>Add</span>
            </button>
          </div>
          ${renderLLMEndpointRouting(endpoints)}
          ${renderLLMEndpointList(endpoints)}
        </section>

        <section class="settings-section" aria-labelledby="search-settings-title">
          <h3 id="search-settings-title" class="settings-section-title">Search</h3>
          <div class="settings-grid">
            <label class="field field-wide">
              <span>SearXNG URL</span>
              <input name="searxngUrl" type="url" value="${escapeHtml(fieldValue("searxngUrl"))}" autocomplete="off" />
            </label>
          </div>
        </section>

        <section class="settings-section" aria-labelledby="notification-settings-title">
          <h3 id="notification-settings-title" class="settings-section-title">Notifications</h3>
          <label class="settings-toggle">
            <span>Notification sounds</span>
            <input
              name="disableNotificationSounds"
              type="checkbox"
              data-settings-inverted-boolean
              ${notificationSoundsEnabled(state.settingsDraft) ? "checked" : ""}
            />
          </label>
        </section>

        <section class="settings-section" aria-labelledby="programming-settings-title">
          <h3 id="programming-settings-title" class="settings-section-title">Programming</h3>
          <label class="settings-toggle">
            <span>Leading whitespace indicators</span>
            <input
              name="hideLeadingWhitespaceIndicators"
              type="checkbox"
              data-settings-inverted-boolean
              ${leadingWhitespaceIndicatorsEnabled(state.settingsDraft) ? "checked" : ""}
            />
          </label>
        </section>

        ${renderWebAccessSettings()}

        ${renderThemeSettings()}

        <section class="settings-section workspace-settings" aria-labelledby="workspace-settings-title">
          <h3 id="workspace-settings-title" class="settings-section-title">Workspaces</h3>
          <div class="workspace-list">
            ${
              workspaces.length
                ? workspaces
                    .map(
                      (workspace) => `
                        <div class="workspace-row">
                          <div class="workspace-row-main">
                            <strong>${escapeHtml(workspace.displayName)}${workspace.missing ? " - Folder missing" : ""}</strong>
                            <span>${escapeHtml(workspaceFolderSummary(workspace))}</span>
                            ${renderWorkspaceFolderSettings(workspace)}
                          </div>
                          <div class="workspace-icon-setting" aria-label="Workspace icon for ${escapeAttribute(workspace.displayName)}">
                            <span class="workspace-icon-preview" aria-hidden="true">${renderWorkspaceIcon(workspace)}</span>
                            <button class="icon-button" type="button" title="Choose workspace icon" aria-label="Choose icon for ${escapeAttribute(workspace.displayName)}" data-action="choose-workspace-icon" data-workspace-id="${escapeAttribute(workspace.id)}">
                              ${icons.image}
                            </button>
                            <button class="icon-button" type="button" title="Clear workspace icon" aria-label="Clear icon for ${escapeAttribute(workspace.displayName)}" data-action="clear-workspace-icon" data-workspace-id="${escapeAttribute(workspace.id)}" ${(workspace.iconUrl ?? "").trim() ? "" : "disabled"}>
                              ${icons.x}
                            </button>
                          </div>
                          <label class="workspace-letter-field">
                            <span>Label</span>
                            <input
                              name="workspaceLetter"
                              type="text"
                              value="${escapeHtml(workspaceLetterDraft(workspace))}"
                              aria-label="Workspace icon label for ${escapeHtml(workspace.displayName)}"
                              data-workspace-letter
                              data-workspace-id="${escapeHtml(workspace.id)}"
                            />
                          </label>
                          <button class="icon-button danger-button" type="button" title="Delete workspace" aria-label="Delete ${escapeHtml(workspace.displayName)}" data-action="delete-workspace" data-workspace-id="${escapeHtml(workspace.id)}">
                            ${icons.trash}
                          </button>
                        </div>
                      `,
                    )
                    .join("")
                : `<p class="empty-state compact">No workspaces added.</p>`
            }
          </div>
        </section>

        <footer class="settings-footer">
          <button class="secondary-button" type="button" data-action="reset-settings">Reset</button>
          <button class="primary-button" type="submit">Save</button>
        </footer>
      </form>
    </div>
  `;
}

function renderLLMEndpointRouting(endpoints: llm.LLMEndpoint[]): string {
  const selection = endpointSelection(state.settingsDraft, endpoints);
  return `
    <div class="llm-endpoint-routing settings-grid" aria-label="LLM endpoint routing">
      ${endpointTopics
        .map(
          (topic) => `
            <label class="field">
              <span>${escapeHtml(topic.label)}</span>
              <select
                name="${escapeAttribute(`${topic.key}Endpoint`)}"
                data-llm-endpoint-selection
                data-endpoint-topic="${escapeAttribute(topic.key)}"
                ${endpoints.length ? "" : "disabled"}
              >
                ${renderLLMEndpointOptions(endpoints, selection[topic.key])}
              </select>
            </label>
          `,
        )
        .join("")}
    </div>
  `;
}

function renderLLMEndpointOptions(endpoints: llm.LLMEndpoint[], selectedID: string): string {
  if (!endpoints.length) {
    return `<option value="">No endpoints</option>`;
  }
  return endpoints
    .map((endpoint, index) => {
      const id = endpoint.id || `endpoint-${index + 1}`;
      const name = endpoint.name?.trim() || `Endpoint ${index + 1}`;
      return `
        <option value="${escapeAttribute(id)}" ${selectedID === id ? "selected" : ""}>
          ${escapeHtml(name)}
        </option>
      `;
    })
    .join("");
}

function renderLLMEndpointList(endpoints: llm.LLMEndpoint[]): string {
  if (!endpoints.length) {
    return `<p class="empty-state compact">No LLM endpoints added.</p>`;
  }
  return `
    <div class="llm-endpoint-list">
      ${endpoints.map((endpoint, index) => renderLLMEndpointRow(endpoint, index, endpoints.length)).join("")}
    </div>
  `;
}

function renderLLMEndpointRow(endpoint: llm.LLMEndpoint, index: number, endpointCount: number): string {
  const id = endpoint.id || `endpoint-${index + 1}`;
  const name = endpoint.name?.trim() || `Endpoint ${index + 1}`;
  const isEditing = state.settingsEndpointEditId === id;
  const summary = endpoint.endpoint?.trim()
    ? `${endpoint.model?.trim() || "No model"} - ${endpoint.endpoint.trim()}`
    : endpoint.model?.trim() || "No endpoint URL";
  if (isEditing) {
    return `
      <div class="llm-endpoint-row is-editing" data-endpoint-id="${escapeAttribute(id)}">
        <div class="settings-grid llm-endpoint-editor">
          <label class="field">
            <span>Name</span>
            <input
              name="endpointName-${escapeAttribute(id)}"
              type="text"
              value="${escapeAttribute(endpoint.name ?? "")}"
              autocomplete="off"
              required
              data-llm-endpoint-field
              data-endpoint-id="${escapeAttribute(id)}"
              data-endpoint-field="name"
              data-initial-focus
            />
          </label>
          <label class="field field-wide">
            <span>Endpoint</span>
            <input
              name="endpointUrl-${escapeAttribute(id)}"
              type="url"
              value="${escapeAttribute(endpoint.endpoint ?? "")}"
              autocomplete="off"
              required
              data-llm-endpoint-field
              data-endpoint-id="${escapeAttribute(id)}"
              data-endpoint-field="endpoint"
            />
          </label>
          <label class="field">
            <span>Model</span>
            <input
              name="endpointModel-${escapeAttribute(id)}"
              type="text"
              value="${escapeAttribute(endpoint.model ?? "")}"
              autocomplete="off"
              required
              data-llm-endpoint-field
              data-endpoint-id="${escapeAttribute(id)}"
              data-endpoint-field="model"
            />
          </label>
          <label class="field field-wide llm-endpoint-preset-field">
            <span>Coding preset</span>
            <select
              name="endpointPreset-${escapeAttribute(id)}"
              data-llm-endpoint-preset
              data-endpoint-id="${escapeAttribute(id)}"
            >
              ${renderLLMPresetOptions(endpoint)}
            </select>
          </label>
          ${renderLLMEndpointGenerationFields(endpoint, id)}
        </div>
        <div class="llm-endpoint-actions">
          <button class="icon-button" type="button" title="Done editing" aria-label="Done editing ${escapeAttribute(name)}" data-action="finish-edit-llm-endpoint">
            ${icons.check}
          </button>
          <button class="icon-button danger-button" type="button" title="Delete endpoint" aria-label="Delete ${escapeAttribute(name)}" data-action="delete-llm-endpoint" data-endpoint-id="${escapeAttribute(id)}" ${endpointCount <= 1 ? "disabled" : ""}>
            ${icons.trash}
          </button>
        </div>
      </div>
    `;
  }
  return `
    <div class="llm-endpoint-row" data-endpoint-id="${escapeAttribute(id)}">
      <div class="llm-endpoint-main">
        <strong>${escapeHtml(name)}</strong>
        <span>${escapeHtml(summary)}</span>
      </div>
      <div class="llm-endpoint-actions">
        <button class="icon-button" type="button" title="Edit endpoint" aria-label="Edit ${escapeAttribute(name)}" data-action="edit-llm-endpoint" data-endpoint-id="${escapeAttribute(id)}">
          ${icons.edit}
        </button>
        <button class="icon-button danger-button" type="button" title="Delete endpoint" aria-label="Delete ${escapeAttribute(name)}" data-action="delete-llm-endpoint" data-endpoint-id="${escapeAttribute(id)}" ${endpointCount <= 1 ? "disabled" : ""}>
          ${icons.trash}
        </button>
      </div>
    </div>
  `;
}

function renderLLMEndpointGenerationFields(endpoint: llm.LLMEndpoint, endpointID: string): string {
  return `
    <label class="field">
      <span>Temperature</span>
      <input name="temperature-${escapeAttribute(endpointID)}" type="number" min="0" max="2" step="0.01" value="${escapeHtml(endpointFieldValue(endpoint, "temperature"))}" data-llm-endpoint-field data-endpoint-id="${escapeAttribute(endpointID)}" data-endpoint-field="temperature" />
    </label>
    <label class="field">
      <span>Top K</span>
      <input name="topK-${escapeAttribute(endpointID)}" type="number" min="0" step="1" value="${escapeHtml(endpointFieldValue(endpoint, "topK"))}" data-llm-endpoint-field data-endpoint-id="${escapeAttribute(endpointID)}" data-endpoint-field="topK" />
    </label>
    <label class="field">
      <span>Top P</span>
      <input name="topP-${escapeAttribute(endpointID)}" type="number" min="0" max="1" step="0.01" value="${escapeHtml(endpointFieldValue(endpoint, "topP"))}" data-llm-endpoint-field data-endpoint-id="${escapeAttribute(endpointID)}" data-endpoint-field="topP" />
    </label>
    <label class="field">
      <span>Min P</span>
      <input name="minP-${escapeAttribute(endpointID)}" type="number" min="0" max="1" step="0.01" value="${escapeHtml(endpointFieldValue(endpoint, "minP"))}" data-llm-endpoint-field data-endpoint-id="${escapeAttribute(endpointID)}" data-endpoint-field="minP" />
    </label>
    <label class="field">
      <span>Context Length</span>
      <input name="contextLength-${escapeAttribute(endpointID)}" type="number" min="1" step="1" value="${escapeHtml(endpointFieldValue(endpoint, "contextLength"))}" data-llm-endpoint-field data-endpoint-id="${escapeAttribute(endpointID)}" data-endpoint-field="contextLength" />
    </label>
    <label class="field">
      <span>Max Tokens</span>
      <input name="maxTokens-${escapeAttribute(endpointID)}" type="number" min="1" step="1" value="${escapeHtml(endpointFieldValue(endpoint, "maxTokens"))}" data-llm-endpoint-field data-endpoint-id="${escapeAttribute(endpointID)}" data-endpoint-field="maxTokens" />
    </label>
    <label class="field">
      <span>Timeout Seconds</span>
      <input name="timeoutSeconds-${escapeAttribute(endpointID)}" type="number" min="1" step="1" value="${escapeHtml(endpointFieldValue(endpoint, "timeoutSeconds"))}" data-llm-endpoint-field data-endpoint-id="${escapeAttribute(endpointID)}" data-endpoint-field="timeoutSeconds" />
    </label>
    <label class="field">
      <span>Frequency Penalty</span>
      <input name="frequencyPenalty-${escapeAttribute(endpointID)}" type="number" min="-2" max="2" step="0.01" value="${escapeHtml(endpointFieldValue(endpoint, "frequencyPenalty"))}" data-llm-endpoint-field data-endpoint-id="${escapeAttribute(endpointID)}" data-endpoint-field="frequencyPenalty" />
    </label>
    <label class="field">
      <span>Presence Penalty</span>
      <input name="presencePenalty-${escapeAttribute(endpointID)}" type="number" min="-2" max="2" step="0.01" value="${escapeHtml(endpointFieldValue(endpoint, "presencePenalty"))}" data-llm-endpoint-field data-endpoint-id="${escapeAttribute(endpointID)}" data-endpoint-field="presencePenalty" />
    </label>
    <label class="field">
      <span>Repetition Penalty</span>
      <input name="repetitionPenalty-${escapeAttribute(endpointID)}" type="number" min="0" step="0.01" value="${escapeHtml(endpointFieldValue(endpoint, "repetitionPenalty"))}" data-llm-endpoint-field data-endpoint-id="${escapeAttribute(endpointID)}" data-endpoint-field="repetitionPenalty" />
    </label>
    <label class="field">
      <span>Thinking Token Budget</span>
      <input name="thinkingTokenBudget-${escapeAttribute(endpointID)}" type="number" min="-1" step="1" value="${escapeHtml(endpointFieldValue(endpoint, "thinkingTokenBudget"))}" title="-1 no limit, 0 off, positive values limit thinking tokens" data-llm-endpoint-field data-endpoint-id="${escapeAttribute(endpointID)}" data-endpoint-field="thinkingTokenBudget" />
    </label>
    <label class="settings-toggle">
      <span>Thinking correction</span>
      <input
        name="thinkingCorrection-${escapeAttribute(endpointID)}"
        type="checkbox"
        ${endpoint.thinkingCorrection ? "checked" : ""}
        ${endpoint.thinkingTokenBudget !== 0 ? "" : "disabled"}
        data-llm-endpoint-field
        data-endpoint-id="${escapeAttribute(endpointID)}"
        data-endpoint-field="thinkingCorrection"
      />
    </label>
  `;
}

function renderLLMPresetOptions(endpoint: llm.LLMEndpoint): string {
  const selectedID = selectedLLMPresetID(endpoint);
  return `
    <option value="" ${selectedID ? "" : "selected"} disabled>Custom configuration</option>
    ${llmCodingPresets
      .map(
        (preset) => `
          <option value="${escapeAttribute(preset.id)}" ${selectedID === preset.id ? "selected" : ""}>
            ${escapeHtml(preset.label)}
          </option>
        `,
      )
      .join("")}
  `;
}

function selectedLLMPresetID(endpoint: llm.LLMEndpoint | null): string {
  if (!endpoint) {
    return "";
  }
  return llmCodingPresets.find((preset) => settingsMatchLLMPreset(endpoint, preset.values))
    ?.id ?? "";
}

function settingsMatchLLMPreset(
  endpoint: llm.LLMEndpoint,
  presetValues: LLMPresetValues,
): boolean {
  return llmPresetFields.every((field) =>
    llmPresetValueMatches(endpoint[field], presetValues[field]),
  );
}

function llmPresetValueMatches(
  current: llm.LLMEndpoint[LLMPresetField],
  expected: llm.LLMEndpoint[LLMPresetField],
): boolean {
  if (typeof expected === "boolean") {
    return Boolean(current) === expected;
  }
  return Math.abs(Number(current ?? 0) - Number(expected)) < 0.000001;
}

function endpointFieldValue<K extends keyof llm.LLMEndpoint>(
  endpoint: llm.LLMEndpoint,
  key: K,
): string {
  const value = endpoint[key];
  return value === undefined || value === null ? "" : String(value);
}

function settingsEndpoints(settings: llm.Settings | null | undefined): llm.LLMEndpoint[] {
  const saved = settings?.endpoints ?? [];
  const defaults = endpointDefaultsFromSettings(settings);
  if (saved.length) {
    return saved.map((endpoint, index) =>
      llm.LLMEndpoint.createFrom({
        ...defaults,
        id: endpoint.id || `endpoint-${index + 1}`,
        name: endpoint.name ?? `Endpoint ${index + 1}`,
        endpoint: endpoint.endpoint ?? "",
        model: endpoint.model ?? "",
        ...endpointGenerationValues(endpoint, defaults),
      }),
    );
  }
  if (!settings) {
    return [];
  }
  return [
    llm.LLMEndpoint.createFrom({
      ...defaults,
      id: "default",
      name: "Default",
      endpoint: settings.endpoint ?? "",
      model: settings.model ?? "",
    }),
  ];
}

function endpointDefaultsFromSettings(settings: llm.Settings | null | undefined): LLMPresetValues {
  return {
    temperature: numberOrDefault(settings?.temperature, 0.6),
    topK: numberOrDefault(settings?.topK, 20),
    topP: numberOrDefault(settings?.topP, 0.95),
    minP: numberOrDefault(settings?.minP, 0),
    contextLength: numberOrDefault(settings?.contextLength, 262144),
    maxTokens: numberOrDefault(settings?.maxTokens, 32168),
    frequencyPenalty: numberOrDefault(settings?.frequencyPenalty, 0),
    presencePenalty: numberOrDefault(settings?.presencePenalty, 1.5),
    repetitionPenalty: numberOrDefault(settings?.repetitionPenalty, 1.05),
    timeoutSeconds: numberOrDefault(settings?.timeoutSeconds, 600),
    thinkingTokenBudget: numberOrDefault(settings?.thinkingTokenBudget, -1),
    thinkingCorrection: settings?.thinkingCorrection === true,
  };
}

function endpointGenerationValues(
  endpoint: llm.LLMEndpoint,
  defaults: LLMPresetValues,
): LLMPresetValues {
  return {
    temperature: numberOrDefault(endpoint.temperature, defaults.temperature),
    topK: numberOrDefault(endpoint.topK, defaults.topK),
    topP: numberOrDefault(endpoint.topP, defaults.topP),
    minP: numberOrDefault(endpoint.minP, defaults.minP),
    contextLength: numberOrDefault(endpoint.contextLength, defaults.contextLength),
    maxTokens: numberOrDefault(endpoint.maxTokens, defaults.maxTokens),
    frequencyPenalty: numberOrDefault(endpoint.frequencyPenalty, defaults.frequencyPenalty),
    presencePenalty: numberOrDefault(endpoint.presencePenalty, defaults.presencePenalty),
    repetitionPenalty: numberOrDefault(endpoint.repetitionPenalty, defaults.repetitionPenalty),
    timeoutSeconds: numberOrDefault(endpoint.timeoutSeconds, defaults.timeoutSeconds),
    thinkingTokenBudget: numberOrDefault(endpoint.thinkingTokenBudget, defaults.thinkingTokenBudget),
    thinkingCorrection:
      endpoint.thinkingCorrection === undefined
        ? defaults.thinkingCorrection
        : endpoint.thinkingCorrection === true,
  };
}

function numberOrDefault(value: number | undefined, fallback: number): number {
  return typeof value === "number" && !Number.isNaN(value) ? value : fallback;
}

function endpointSelection(
  settings: llm.Settings | null | undefined,
  endpoints = settingsEndpoints(settings),
): Record<EndpointTopic, string> {
  const fallback = endpoints[0]?.id ?? "";
  const raw = settings?.endpointSelection;
  return {
    chat: validEndpointID(raw?.chat, endpoints) ? raw!.chat : fallback,
    kanban: validEndpointID(raw?.kanban, endpoints) ? raw!.kanban : fallback,
    inlineCode: validEndpointID(raw?.inlineCode, endpoints) ? raw!.inlineCode : fallback,
  };
}

function validEndpointID(id: string | undefined, endpoints: llm.LLMEndpoint[]): id is string {
  return Boolean(id && endpoints.some((endpoint) => endpoint.id === id));
}

function settingsWithEndpointSync(settings: llm.Settings): llm.Settings {
  const source = settingsSource(settings);
  const endpoints = settingsEndpoints(settings);
  const selection = endpointSelection(settings, endpoints);
  const chatEndpoint =
    endpoints.find((endpoint) => endpoint.id === selection.chat) ?? endpoints[0];
  source.endpoints = endpoints;
  source.endpointSelection = selection;
  source.endpoint = chatEndpoint?.endpoint ?? "";
  source.model = chatEndpoint?.model ?? "";
  for (const field of llmPresetFields) {
    source[field] = chatEndpoint?.[field] ?? endpointDefaultsFromSettings(settings)[field];
  }
  return llm.Settings.createFrom(source);
}

function settingsSource(settings: llm.Settings): Record<string, unknown> {
  return JSON.parse(JSON.stringify(settings)) as Record<string, unknown>;
}

function settingsWithEndpointDraft(
  settings: llm.Settings,
  endpoints: llm.LLMEndpoint[],
  selection = endpointSelection(settings, endpoints),
): llm.Settings {
  const source = settingsSource(settings);
  source.endpoints = endpoints;
  source.endpointSelection = selection;
  return settingsWithEndpointSync(llm.Settings.createFrom(source));
}

function handleLLMEndpointSelectionInput(select: HTMLSelectElement) {
  if (!state.settingsDraft) {
    return;
  }
  const topic = select.dataset.endpointTopic;
  if (!isEndpointTopic(topic)) {
    return;
  }
  const endpoints = settingsEndpoints(state.settingsDraft);
  const selection = {
    ...endpointSelection(state.settingsDraft, endpoints),
    [topic]: select.value,
  };
  state.settingsDraft = settingsWithEndpointDraft(state.settingsDraft, endpoints, selection);
  state.formError = "";
}

function handleLLMEndpointFieldInput(input: HTMLInputElement) {
  if (!state.settingsDraft) {
    return;
  }
  const endpointID = input.dataset.endpointId ?? "";
  const field = input.dataset.endpointField;
  if (!endpointID || !isEndpointField(field)) {
    return;
  }
  const value =
    input.type === "checkbox"
      ? input.checked
      : isEndpointNumericField(field)
        ? Number(input.value)
        : input.value;
  const endpoints = settingsEndpoints(state.settingsDraft).map((endpoint) => {
    if (endpoint.id !== endpointID) {
      return endpoint;
    }
    return llm.LLMEndpoint.createFrom({
      ...endpoint,
      [field]: typeof value === "number" && Number.isNaN(value) ? 0 : value,
    });
  });
  state.settingsDraft = settingsWithEndpointDraft(state.settingsDraft, endpoints);
  if (field === "thinkingTokenBudget") {
    const correctionInput = input.form?.querySelector<HTMLInputElement>(
      `input[data-endpoint-id="${CSS.escape(endpointID)}"][data-endpoint-field="thinkingCorrection"]`,
    );
    if (correctionInput) {
      correctionInput.disabled = Number(input.value) === 0;
    }
  }
  state.formError = "";
}

function isEndpointTopic(value: string | undefined): value is EndpointTopic {
  return endpointTopics.some((topic) => topic.key === value);
}

type EndpointField = "name" | "endpoint" | "model" | LLMPresetField;

function isEndpointField(value: string | undefined): value is EndpointField {
  return value === "name" || value === "endpoint" || value === "model" ||
    (llmPresetFields as readonly string[]).includes(value ?? "");
}

function isEndpointNumericField(value: EndpointField): value is Exclude<LLMPresetField, "thinkingCorrection"> {
  return value !== "name" &&
    value !== "endpoint" &&
    value !== "model" &&
    value !== "thinkingCorrection";
}

export function addLLMEndpoint() {
  if (!state.settingsDraft) {
    return;
  }
  const endpoints = settingsEndpoints(state.settingsDraft);
  const id = nextLLMEndpointID(endpoints);
  const nextIndex = endpoints.length + 1;
  const nextEndpoint = llm.LLMEndpoint.createFrom({
    ...endpointDefaultsFromSettings(state.settingsDraft),
    id,
    name: `Endpoint ${nextIndex}`,
    endpoint: "",
    model: "",
  });
  state.settingsDraft = settingsWithEndpointDraft(state.settingsDraft, [
    ...endpoints,
    nextEndpoint,
  ]);
  state.settingsEndpointEditId = id;
  state.formError = "";
}

export function editLLMEndpoint(endpointID: string) {
  state.settingsEndpointEditId = endpointID;
  state.formError = "";
}

export function finishEditingLLMEndpoint() {
  state.settingsEndpointEditId = "";
  state.formError = "";
}

export function deleteLLMEndpoint(endpointID: string) {
  if (!state.settingsDraft || !endpointID) {
    return;
  }
  const endpoints = settingsEndpoints(state.settingsDraft);
  if (endpoints.length <= 1) {
    return;
  }
  const remaining = endpoints.filter((endpoint) => endpoint.id !== endpointID);
  const fallback = remaining[0]?.id ?? "";
  const currentSelection = endpointSelection(state.settingsDraft, endpoints);
  const selection = endpointTopics.reduce(
    (result, topic) => {
      result[topic.key] =
        currentSelection[topic.key] === endpointID
          ? fallback
          : currentSelection[topic.key];
      return result;
    },
    {} as Record<EndpointTopic, string>,
  );
  state.settingsDraft = settingsWithEndpointDraft(state.settingsDraft, remaining, selection);
  if (state.settingsEndpointEditId === endpointID) {
    state.settingsEndpointEditId = "";
  }
  state.formError = "";
}

function nextLLMEndpointID(endpoints: llm.LLMEndpoint[]): string {
  const used = new Set(endpoints.map((endpoint) => endpoint.id));
  const base = `endpoint-${Date.now().toString(36)}`;
  let candidate = base;
  for (let index = 2; used.has(candidate); index += 1) {
    candidate = `${base}-${index}`;
  }
  return candidate;
}

function renderWebAccessSettings(): string {
  const draft = state.webAccessDraft ?? services.WebAccessSettings.createFrom({
    enabled: false,
    bindHost: "0.0.0.0",
    port: 3740,
    accessToken: "",
  });
  const status = state.webAccessStatus;
  const urls = status?.lanUrls ?? [];
  const qrURL = urls.includes(state.webAccessQRCodeURL) ? state.webAccessQRCodeURL : "";
  const statusText = draft.enabled
    ? status?.running
      ? "Running"
      : status?.lastError
        ? `Error: ${status.lastError}`
        : "Stopped"
    : "Disabled";
  return `
    <section class="settings-section web-access-settings" aria-labelledby="web-access-settings-title">
      <div class="settings-section-heading">
        <h3 id="web-access-settings-title" class="settings-section-title">Web Access</h3>
        <span class="web-access-status ${status?.running ? "is-running" : ""}">${escapeHtml(statusText)}</span>
      </div>
      <label class="settings-toggle">
        <span>Enable browser access</span>
        <input
          name="enabled"
          type="checkbox"
          data-web-access-field
          ${draft.enabled ? "checked" : ""}
        />
      </label>
      <div class="settings-grid">
        <label class="field">
          <span>Bind Host</span>
          <input name="bindHost" type="text" value="${escapeAttribute(draft.bindHost || "0.0.0.0")}" autocomplete="off" data-web-access-field />
        </label>
        <label class="field">
          <span>Port</span>
          <input name="port" type="number" min="1" max="65535" step="1" value="${escapeAttribute(String(draft.port || 3740))}" data-web-access-field />
        </label>
        <label class="field field-wide">
          <span>Access Token</span>
          <input name="accessToken" type="text" value="${escapeAttribute(draft.accessToken ?? "")}" autocomplete="off" spellcheck="false" data-web-access-field />
        </label>
      </div>
      <div class="web-access-actions">
        <button class="secondary-button compact-button" type="button" data-action="rotate-web-access-token">Rotate Token</button>
      </div>
      ${
        urls.length
          ? `<div class="web-access-urls">
              ${urls.map((item) => renderWebAccessURL(item, qrURL)).join("")}
            </div>`
          : ""
      }
      ${qrURL ? renderWebAccessQRCode(qrURL) : ""}
    </section>
  `;
}

function renderWebAccessURL(url: string, qrURL: string): string {
  const active = url === qrURL;
  return `
    <div class="web-access-url-row ${active ? "is-active" : ""}">
      <a href="${escapeAttribute(url)}" target="_blank" rel="noreferrer">${escapeHtml(url)}</a>
      <button
        class="icon-button"
        type="button"
        title="Show QR code"
        aria-label="Show QR code for ${escapeAttribute(url)}"
        data-action="show-web-access-qr"
        data-web-access-url="${escapeAttribute(url)}"
      >
        ${icons.qr}
      </button>
    </div>
  `;
}

function renderWebAccessQRCode(url: string): string {
  return `
    <div class="web-access-qr-panel" aria-live="polite">
      <div class="web-access-qr-header">
        <strong>Scan Web Access</strong>
        <button class="icon-button" type="button" title="Hide QR code" aria-label="Hide QR code" data-action="hide-web-access-qr">
          ${icons.x}
        </button>
      </div>
      <div class="web-access-qr-code">${renderQRCodeSVG(url)}</div>
      <p>${escapeHtml(url)}</p>
    </div>
  `;
}

function renderThemeSettings(): string {
  const palette = state.settingsThemePalette;
  return `
    <section class="settings-section theme-settings" aria-labelledby="theme-settings-title">
      <div class="settings-section-heading">
        <h3 id="theme-settings-title" class="settings-section-title">Theme Colors</h3>
        <button class="secondary-button compact-button" type="button" data-action="restore-theme-defaults">Restore Theme Defaults</button>
      </div>
      <div class="theme-palette-toggle" role="tablist" aria-label="Theme palette">
        ${(["light", "dark"] as ThemePaletteName[])
          .map(
            (name) => `
              <button
                class="theme-palette-button ${palette === name ? "is-active" : ""}"
                type="button"
                role="tab"
                aria-selected="${palette === name}"
                data-action="set-theme-palette"
                data-theme-palette="${name}"
              >${name === "light" ? "Light" : "Dark"}</button>
            `,
          )
          .join("")}
      </div>
      <div class="theme-token-groups">
        ${themeGroups.map((group) => renderThemeTokenGroup(group, palette)).join("")}
      </div>
    </section>
  `;
}

function renderThemeTokenGroup(group: string, palette: ThemePaletteName): string {
  const tokens = themeTokens.filter((token) => token.group === group);
  return `
    <div class="theme-token-group">
      <h4>${escapeHtml(group)}</h4>
      <div class="theme-token-grid">
        ${tokens.map((token) => renderThemeTokenField(token, palette)).join("")}
      </div>
    </div>
  `;
}

function renderThemeTokenField(
  token: (typeof themeTokens)[number],
  palette: ThemePaletteName,
): string {
  const value = themeColorValue(state.settingsDraft, palette, token);
  return `
    <label class="theme-color-field">
      <span>${escapeHtml(token.label)}</span>
      <span class="theme-color-control">
        <input
          class="theme-color-swatch"
          type="color"
          value="${escapeAttribute(value)}"
          aria-label="${escapeAttribute(`${token.label} color`)}"
          data-theme-token="${escapeAttribute(token.key)}"
          data-theme-palette="${palette}"
        />
        <input
          class="theme-color-hex"
          type="text"
          value="${escapeAttribute(value)}"
          spellcheck="false"
          inputmode="text"
          aria-label="${escapeAttribute(`${token.label} hex color`)}"
          data-theme-token="${escapeAttribute(token.key)}"
          data-theme-palette="${palette}"
        />
      </span>
    </label>
  `;
}

export function handleSettingsInput(event: Event) {
  const input = event.currentTarget as HTMLInputElement | HTMLSelectElement;
  if (input.dataset.llmEndpointSelection !== undefined && input instanceof HTMLSelectElement) {
    handleLLMEndpointSelectionInput(input);
    return;
  }
  if (input.dataset.llmEndpointField !== undefined && input instanceof HTMLInputElement) {
    handleLLMEndpointFieldInput(input);
    return;
  }
  if (input.dataset.themeToken !== undefined) {
    handleThemeColorInput(input as HTMLInputElement);
    return;
  }
  if (input.dataset.workspaceFolderAgents !== undefined) {
    return;
  }
  if (input.dataset.webAccessField !== undefined) {
    handleWebAccessInput(input as HTMLInputElement);
    return;
  }
  if (input.dataset.workspaceLetter !== undefined) {
    state.workspaceLetterDrafts.set(input.dataset.workspaceId ?? "", input.value);
    state.formError = "";
    return;
  }
  if (!state.settingsDraft) {
    return;
  }

  const numericFields = new Set([
    "temperature",
    "topK",
    "topP",
    "minP",
    "contextLength",
    "maxTokens",
    "frequencyPenalty",
    "presencePenalty",
    "repetitionPenalty",
    "timeoutSeconds",
    "thinkingTokenBudget",
  ]);
  let value: number | string | boolean;
  if (input.type === "checkbox") {
    value =
      input.dataset.settingsInvertedBoolean !== undefined
        ? !input.checked
        : input.checked;
  } else {
    value = numericFields.has(input.name) ? Number(input.value) : input.value;
  }
  state.settingsDraft = llm.Settings.createFrom({
    ...state.settingsDraft,
    [input.name]: typeof value === "number" && Number.isNaN(value) ? 0 : value,
  });
  state.formError = "";
}

function handleLLMEndpointPresetChange(select: HTMLSelectElement) {
  if (!state.settingsDraft) {
    return;
  }
  const endpointID = select.dataset.endpointId ?? "";
  const preset = llmCodingPresets.find((item) => item.id === select.value);
  if (!endpointID || !preset) {
    return;
  }
  const endpoints = settingsEndpoints(state.settingsDraft).map((endpoint) =>
    endpoint.id === endpointID
      ? llm.LLMEndpoint.createFrom({
          ...endpoint,
          ...preset.values,
        })
      : endpoint,
  );
  state.settingsDraft = settingsWithEndpointDraft(state.settingsDraft, endpoints);
  state.formError = "";
  getAppCallbacks().render();
}

function handleWebAccessInput(input: HTMLInputElement) {
  if (!state.webAccessDraft) {
    state.webAccessDraft = cloneWebAccessSettings(state.appState?.webAccess);
  }
  const value = input.type === "checkbox"
    ? input.checked
    : input.name === "port"
      ? Number(input.value)
      : input.value;
  state.webAccessDraft = services.WebAccessSettings.createFrom({
    ...state.webAccessDraft,
    [input.name]: typeof value === "number" && Number.isNaN(value) ? 3740 : value,
  });
  state.formError = "";
}

function handleThemeColorInput(input: HTMLInputElement) {
  if (!state.settingsDraft) {
    return;
  }
  const palette = input.dataset.themePalette as ThemePaletteName | undefined;
  const tokenKey = input.dataset.themeToken ?? "";
  if (palette !== "light" && palette !== "dark") {
    return;
  }
  state.settingsDraft = settingsWithThemeColor(
    state.settingsDraft,
    palette,
    tokenKey,
    input.value,
  );
  const normalized = normalizeHexColor(input.value);
  if (normalized) {
    syncThemeColorInputs(input, normalized);
  }
  state.formError = "";
  applyTheme(state.settingsDraft);
}

function syncThemeColorInputs(source: HTMLInputElement, value: string) {
  source.form
    ?.querySelectorAll<HTMLInputElement>("[data-theme-token][data-theme-palette]")
    .forEach((input) => {
      if (
        input === source ||
        input.dataset.themeToken !== source.dataset.themeToken ||
        input.dataset.themePalette !== source.dataset.themePalette
      ) {
        return;
      }
      input.value = value;
    });
}

export async function handleWorkspaceFolderAgentsChange(input: HTMLInputElement) {
  const workspaceID = input.dataset.workspaceId ?? "";
  const folderID = input.dataset.folderId ?? "";
  if (!workspaceID || !folderID) {
    return;
  }
  try {
    state.appState = await SetWorkspaceFolderUseAgents(workspaceID, folderID, input.checked);
    pushToast("Workspace folder updated.", "success");
    getAppCallbacks().render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    getAppCallbacks().render();
  }
}

export async function handleSettingsSubmit(event: SubmitEvent) {
  event.preventDefault();
  if (!state.settingsDraft) {
    return;
  }
  const validationError = validateLLMEndpointDraft(state.settingsDraft);
  if (validationError) {
    state.formError = validationError;
    getAppCallbacks().render();
    return;
  }

  try {
    state.settingsDraft = settingsWithEndpointSync(state.settingsDraft);
    state.appState = await SaveSettings(settingsWithCompactTheme(state.settingsDraft));
    if (state.webAccessDraft) {
      state.appState = await SaveWebAccessSettings(state.webAccessDraft);
      state.webAccessStatus = await LoadWebAccessStatus();
    }
    for (const workspace of state.appState.workspaces ?? []) {
      const draft = state.workspaceLetterDrafts.get(workspace.id);
      if (draft !== undefined && draft !== (workspace.letter ?? "")) {
        state.appState = await SetWorkspaceLetter(workspace.id, draft);
      }
    }
    state.settingsDraft = cloneSettings(state.appState.settings);
    state.settingsEndpointEditId = "";
    state.webAccessDraft = cloneWebAccessSettings(state.appState.webAccess);
    applyTheme(state.appState.settings);
    hydrateWorkspaceLetterDrafts(state.appState.workspaces ?? []);
    state.settingsOpen = false;
    state.formError = "";
    pushToast("Settings saved.", "success");
    getAppCallbacks().render();
  } catch (error) {
    state.formError = errorMessage(error);
    getAppCallbacks().render();
  }
}

function validateLLMEndpointDraft(settings: llm.Settings): string {
  const endpoints = settingsEndpoints(settings);
  if (!endpoints.length) {
    return "Add at least one LLM endpoint.";
  }
  const names = new Set<string>();
  for (const endpoint of endpoints) {
    const name = endpoint.name.trim();
    if (!name) {
      return "Endpoint name is required.";
    }
    if (!endpoint.endpoint.trim()) {
      return `Endpoint URL is required for ${name}.`;
    }
    if (!endpoint.model.trim()) {
      return `Model is required for ${name}.`;
    }
    const nameKey = name.toLowerCase();
    if (names.has(nameKey)) {
      return "Endpoint names must be unique.";
    }
    names.add(nameKey);
  }
  const selection = endpointSelection(settings, endpoints);
  for (const topic of endpointTopics) {
    if (!validEndpointID(selection[topic.key], endpoints)) {
      return `${topic.label} must use a saved endpoint.`;
    }
  }
  return "";
}
