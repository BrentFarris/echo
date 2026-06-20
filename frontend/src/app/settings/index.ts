
import { SaveSettings, SetWorkspaceFolderUseAgents, SetWorkspaceLetter } from "../../../wailsjs/go/services/SystemService";
import { llm, services } from "../../../wailsjs/go/models";
import { getAppCallbacks } from "../callbacks";
import { icons } from "../icons";
import { cloneSettings, fieldValue, leadingWhitespaceIndicatorsEnabled, notificationSoundsEnabled, state, thinkingCorrectionEnabled, thinkingTokenBudgetEnabled } from "../state";
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
type LLMPresetValues = Pick<llm.Settings, LLMPresetField>;

const llmCodingPresets: {
  id: string;
  label: string;
  values: LLMPresetValues;
}[] = [
  {
    id: "qwen3_6_35b_a3b",
    label: "Qwen3.6 35B A3B",
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
    id: "gemma4_31b_qat",
    label: "Gemma 4 31B QAT",
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
      thinkingTokenBudget: 0,
      thinkingCorrection: false,
    },
  },
];

export function bindSettingsEvents(root: ParentNode) {
  const form = root.querySelector<HTMLFormElement>("[data-settings-form]");
  form?.addEventListener("submit", handleSettingsSubmit);
  form
    ?.querySelectorAll<HTMLInputElement>("input")
    .forEach((input) => input.addEventListener("input", handleSettingsInput));
  form
    ?.querySelectorAll<HTMLSelectElement>("[data-llm-config-preset]")
    .forEach((select) =>
      select.addEventListener("change", () => handleLLMPresetChange(select)),
    );
  form
    ?.querySelectorAll<HTMLInputElement>("[data-workspace-folder-agents]")
    .forEach((input) =>
      input.addEventListener("change", () => {
        void handleWorkspaceFolderAgentsChange(input);
      }),
    );
}

export function renderSettingsOverlay(workspaces: services.Workspace[]): string {
  const hasSettingsValues = Boolean(fieldValue("endpoint").trim() || fieldValue("model").trim());
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
        ${hasSettingsValues ? "" : `<p class="empty-state compact">No settings are loaded. Enter an OpenAI-compatible endpoint and model to recover.</p>`}

        <section class="settings-section" aria-labelledby="llm-settings-title">
          <h3 id="llm-settings-title" class="settings-section-title">LLM Configuration</h3>
          <div class="settings-grid">
            <label class="field field-wide llm-preset-field">
              <span>Coding preset</span>
              <select name="llmPreset" data-llm-config-preset>
                ${renderLLMPresetOptions()}
              </select>
            </label>
            <label class="field field-wide">
              <span>Endpoint</span>
              <input name="endpoint" required type="url" value="${escapeHtml(fieldValue("endpoint"))}" autocomplete="off" data-initial-focus />
            </label>
            <label class="field field-wide">
              <span>SearXNG URL</span>
              <input name="searxngUrl" type="url" value="${escapeHtml(fieldValue("searxngUrl"))}" autocomplete="off" />
            </label>
            <label class="field field-wide">
              <span>Model</span>
              <input name="model" required type="text" value="${escapeHtml(fieldValue("model"))}" autocomplete="off" />
            </label>
            <label class="field">
              <span>Temperature</span>
              <input name="temperature" type="number" min="0" max="2" step="0.01" value="${escapeHtml(fieldValue("temperature"))}" />
            </label>
            <label class="field">
              <span>Top K</span>
              <input name="topK" type="number" min="0" step="1" value="${escapeHtml(fieldValue("topK"))}" />
            </label>
            <label class="field">
              <span>Top P</span>
              <input name="topP" type="number" min="0" max="1" step="0.01" value="${escapeHtml(fieldValue("topP"))}" />
            </label>
            <label class="field">
              <span>Min P</span>
              <input name="minP" type="number" min="0" max="1" step="0.01" value="${escapeHtml(fieldValue("minP"))}" />
            </label>
            <label class="field">
              <span>Context Length</span>
              <input name="contextLength" type="number" min="1" step="1" value="${escapeHtml(fieldValue("contextLength"))}" />
            </label>
            <label class="field">
              <span>Max Tokens</span>
              <input name="maxTokens" type="number" min="1" step="1" value="${escapeHtml(fieldValue("maxTokens"))}" />
            </label>
            <label class="field">
              <span>Timeout Seconds</span>
              <input name="timeoutSeconds" type="number" min="1" step="1" value="${escapeHtml(fieldValue("timeoutSeconds"))}" />
            </label>
            <label class="field">
              <span>Frequency Penalty</span>
              <input name="frequencyPenalty" type="number" min="-2" max="2" step="0.01" value="${escapeHtml(fieldValue("frequencyPenalty"))}" />
            </label>
            <label class="field">
              <span>Presence Penalty</span>
              <input name="presencePenalty" type="number" min="-2" max="2" step="0.01" value="${escapeHtml(fieldValue("presencePenalty"))}" />
            </label>
            <label class="field">
              <span>Repetition Penalty</span>
              <input name="repetitionPenalty" type="number" min="0" step="0.01" value="${escapeHtml(fieldValue("repetitionPenalty"))}" />
            </label>
            <label class="field field-wide">
              <span>Thinking Token Budget</span>
              <input
                name="thinkingTokenBudget"
                type="number"
                min="-1"
                step="1"
                value="${escapeHtml(fieldValue("thinkingTokenBudget"))}"
                title="-1 no limit, 0 off, positive values limit thinking tokens"
              />
            </label>
            <label class="settings-toggle field-wide">
              <span>Thinking correction</span>
              <input
                name="thinkingCorrection"
                type="checkbox"
                ${thinkingCorrectionEnabled(state.settingsDraft) ? "checked" : ""}
                ${thinkingTokenBudgetEnabled(state.settingsDraft) ? "" : "disabled"}
              />
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

function renderLLMPresetOptions(): string {
  const selectedID = selectedLLMPresetID(state.settingsDraft);
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

function selectedLLMPresetID(settings: llm.Settings | null): string {
  if (!settings) {
    return "";
  }
  return llmCodingPresets.find((preset) => settingsMatchLLMPreset(settings, preset.values))
    ?.id ?? "";
}

function settingsMatchLLMPreset(
  settings: llm.Settings,
  presetValues: LLMPresetValues,
): boolean {
  return llmPresetFields.every((field) =>
    llmPresetValueMatches(settings[field], presetValues[field]),
  );
}

function llmPresetValueMatches(
  current: llm.Settings[LLMPresetField],
  expected: llm.Settings[LLMPresetField],
): boolean {
  if (typeof expected === "boolean") {
    return Boolean(current) === expected;
  }
  return Math.abs(Number(current ?? 0) - Number(expected)) < 0.000001;
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
  const input = event.currentTarget as HTMLInputElement;
  if (input.dataset.themeToken !== undefined) {
    handleThemeColorInput(input);
    return;
  }
  if (input.dataset.workspaceFolderAgents !== undefined) {
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
  const presetSelect = input.form?.querySelector<HTMLSelectElement>(
    "[data-llm-config-preset]",
  );
  if (presetSelect) {
    presetSelect.value = selectedLLMPresetID(state.settingsDraft);
  }
  if (input.name === "thinkingTokenBudget") {
    const correctionInput = input.form?.querySelector<HTMLInputElement>(
      'input[name="thinkingCorrection"]',
    );
    if (correctionInput) {
      correctionInput.disabled = Number(input.value) === 0;
    }
  }
  state.formError = "";
}

function handleLLMPresetChange(select: HTMLSelectElement) {
  if (!state.settingsDraft) {
    return;
  }
  const preset = llmCodingPresets.find((item) => item.id === select.value);
  if (!preset) {
    return;
  }
  state.settingsDraft = llm.Settings.createFrom({
    ...state.settingsDraft,
    ...preset.values,
  });
  state.formError = "";
  getAppCallbacks().render();
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
  if (!state.settingsDraft.endpoint.trim()) {
    state.formError = "Endpoint is required.";
    getAppCallbacks().render();
    return;
  }
  if (!state.settingsDraft.model.trim()) {
    state.formError = "Model is required.";
    getAppCallbacks().render();
    return;
  }

  try {
    state.appState = await SaveSettings(settingsWithCompactTheme(state.settingsDraft));
    for (const workspace of state.appState.workspaces ?? []) {
      const draft = state.workspaceLetterDrafts.get(workspace.id);
      if (draft !== undefined && draft !== (workspace.letter ?? "")) {
        state.appState = await SetWorkspaceLetter(workspace.id, draft);
      }
    }
    state.settingsDraft = cloneSettings(state.appState.settings);
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
