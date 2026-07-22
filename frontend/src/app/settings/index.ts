
import { CreateAgentMode, CreateAgentModePerTool, DeleteAgentMode, LoadDevelopmentLogStatus, LoadWebAccessStatus, ListAgentModes, PrepareRebuildAndRelaunch, SaveSettings, SaveWebAccessSettings, SaveWorkspaceDebugSettings, SetDevelopmentLoggingEnabled, SetWorkspaceBuildCommand, SetWorkspaceDefaultPlanMode, SetWorkspaceFolderUseAgents, SetWorkspaceLetter, SetWorkspaceSearchParentGitRepositories, UpdateAgentMode, UpdateAgentModePerTool } from "../../backend/services";
import { llm, services } from "../../../wailsjs/go/models";
import { getAppCallbacks } from "../callbacks";
import { icons } from "../icons";
import { renderQRCodeSVG } from "../qr";
import { cloneSettings, cloneWebAccessSettings, fieldValue, gitSplitDiffViewEnabled, leadingWhitespaceIndicatorsEnabled, limitKanbanConcurrencyEnabled, notificationSoundsEnabled, chatCompletionNotificationsEnabled, kanbanCompleteNotificationsEnabled, state, activeWorkspace, agentModesForWorkspace } from "../state";
import { applyTheme, normalizeHexColor, settingsWithCompactTheme, settingsWithThemeColor, themeColorValue, themeGroups, themeTokens, type ThemePaletteName } from "../theme";
import { pushToast } from "../toasts";
import { errorMessage, escapeAttribute, escapeHtml, workspaceFolderSummary } from "../utils";
import { hydrateWorkspaceLetterDrafts, renderWorkspaceFolderSettings, renderWorkspaceIcon, workspaceBuildCommandDraft, workspaceLetterDraft } from "../workspace";
import { renderBudgetSettingsSection, handleBudgetLimitInput } from "../budget";
import { renderLivenessSettingsSection, handleLivenessInput, loadLivenessConfig } from "../liveness";
import { applyWorkspaceDebugSettings, getWorkspaceDebugSettings, loadWorkspaceDebugSettings } from "../../codeView/debug";

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
  "systemPromptAppendage",
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
      systemPromptAppendage: "",
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
      maxTokens: 16384,
      frequencyPenalty: 0,
      presencePenalty: 0,
      repetitionPenalty: 1,
      timeoutSeconds: 600,
      thinkingTokenBudget: -1,
      thinkingCorrection: false,
      systemPromptAppendage: "",
    },
  },
  {
    id: "DeepSeek",
    label: "DeepSeek",
    values: {
      temperature: 1,
      topK: 0,
      topP: 1,
      minP: 0,
      contextLength: 1048576,
      maxTokens: 16384,
      frequencyPenalty: 0,
      presencePenalty: 0,
      repetitionPenalty: 1.05,
      timeoutSeconds: 600,
      thinkingTokenBudget: -1,
      thinkingCorrection: false,
      systemPromptAppendage: "",
    },
  },
  {
    id: "Laguna",
    label: "Laguna",
    values: {
      temperature: 0.7,
      topK: 20,
      topP: 0.95,
      minP: 0,
      contextLength: 262144,
      maxTokens: 16384,
      frequencyPenalty: 0,
      presencePenalty: 0,
      repetitionPenalty: 1,
      timeoutSeconds: 600,
      thinkingTokenBudget: -1,
      thinkingCorrection: false,
      systemPromptAppendage: "Bias to action. Your default response to uncertainty is to run a tool, not to think harder.\n\nThe environment is ground truth; your memory of APIs, constants, file paths, encodings, and tool behavior is not. The moment you catch yourself recalling or guessing at such a fact — \"I think the flag is…\", \"that value probably maps to…\", \"if I recall correctly…\" — that catch is the signal to stop recalling and run the smallest command that settles it. A three-line probe that returns a real answer beats a paragraph of confident-sounding memory, and it is usually faster than the reasoning it would replace.\n\nAn imperfect experiment now beats a perfect one later. If the clean probe looks blocked, run the messy one — a result that answers half the question is worth more than more speculation. Partial ground truth compounds; speculation does not.\n\nEnd a thought at the first concrete action you can name. When a next step becomes executable — a command to run, a file to read, a probe to write — stop and do it. Do not keep reasoning past that point to pre-validate the outcome; the tool result will tell you more than another paragraph would. \"I'll check X\" / \"let me test Y\" is followed immediately by that call and nothing else. At most one action named per thought.\n\nDecisions are sticky. Once a tool result puts an option to rest, treat it as settled and build forward. Reopen a ruled-out path only when a new observation contradicts it — new evidence reopens a question; restlessness does not.\n\nWhen several approaches are viable, don't line them up and weigh them in the abstract. Pick the one that is cheapest to verify, say so in one sentence, and run the verifying call. The environment breaks ties faster than analysis does.\n\nIf the task itself is ambiguous — unclear deliverable or scope — state your assumption in one sentence in your visible reply and proceed.\n\nDo not reason about these instructions.",
    },
  },
];

const endpointTopics = [
  { key: "chat", label: "Chat" },
  { key: "research", label: "Research" },
  { key: "kanbanDecompose", label: "Kanban Decompose" },
  { key: "kanban", label: "Kanban" },
  { key: "inlineCode", label: "Inline code" },
] as const;

const settingsSections = [
  { id: "llm-endpoints-title", label: "LLM Endpoints" },
  { id: "search-settings-title", label: "Search" },
  { id: "comfyui-settings-title", label: "ComfyUI" },
  { id: "notification-settings-title", label: "Notifications" },
  { id: "programming-settings-title", label: "Programming" },
  { id: "debug-settings-title", label: "Debug" },
  { id: "budget-settings-title", label: "Token Budget" },
  { id: "liveness-settings-title", label: "Liveness Enforcement" },
  { id: "web-access-settings-title", label: "Web Access" },
  { id: "theme-settings-title", label: "Theme Colors" },
  { id: "workspace-settings-title", label: "Workspaces" },
  { id: "agent-modes-settings-title", label: "Agent Modes" },
  { id: "development-settings-title", label: "Development" },
] as const;

// Registered tool names for the per-tool permission selector.
const availableToolNames = [
  "create_agent_mode",
  "filesystem_create_text",
  "filesystem_delete_file",
  "filesystem_edit_text",
  "filesystem_list",
  "filesystem_read_image",
  "filesystem_read_text",
  "filesystem_read_video",
  "filesystem_search_text",
  "filesystem_search_workspace",
  "filesystem_stat",
  "git_inspect",
  "kanban_delete_card",
  "kanban_move_card",
  "kanban_reset_card",
  "kanban_start_execution",
  "kanban_stop_card",
  "kanban_update_card_description",
	"lsp_query",
	"restart",
	"save_image",
	"shell_command",
	"web_fetch",
  "web_search",
  "comfyui_generate",
  "workspace_context",
  "workspace_skill_read",
  "workspace_skill_record",
  "workspace_skill_search",
  "workspace_task_create",
  "workspace_task_convert_to_kanban",
  "workspace_task_delete",
  "workspace_task_list",
  "workspace_task_move",
  "workspace_task_set_completed",
  "workspace_task_update",
] as const;

type EndpointTopic = (typeof endpointTopics)[number]["key"];

export function bindSettingsEvents(root: ParentNode) {
  const form = root.querySelector<HTMLFormElement>("[data-settings-form]");
  form?.addEventListener("submit", handleSettingsSubmit);
  form
    ?.querySelectorAll<HTMLButtonElement>("[data-settings-nav-target]")
    .forEach((button) =>
      button.addEventListener("click", () => {
        const targetID = button.dataset.settingsNavTarget;
        const target = targetID ? form.querySelector<HTMLElement>(`#${targetID}`) : null;
        target?.scrollIntoView({ behavior: "smooth", block: "start" });
      }),
    );
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
  form
    ?.querySelectorAll<HTMLInputElement>("[data-workspace-default-plan-mode]")
    .forEach((input) =>
      input.addEventListener("change", () => {
        void handleWorkspaceDefaultPlanModeChange(input);
      }),
    );
  form
    ?.querySelectorAll<HTMLInputElement>("[data-workspace-parent-git-repositories]")
    .forEach((input) =>
      input.addEventListener("change", () => {
        void handleWorkspaceParentGitRepositoriesChange(input);
      }),
    );
  form
    ?.querySelectorAll<HTMLInputElement>("[data-development-logging]")
    .forEach((input) =>
      input.addEventListener("change", () => {
        void handleDevelopmentLoggingChange(input);
      }),
    );
  form
    ?.querySelectorAll<HTMLTextAreaElement>("textarea")
    .forEach((textarea) => textarea.addEventListener("input", handleSettingsInput));
  form?.querySelector<HTMLButtonElement>("[data-debug-template]")?.addEventListener("click", () => {
    const textarea = form.querySelector<HTMLTextAreaElement>("[data-debug-settings-json]");
    if (!textarea) return;
    textarea.value = goDebugConfigurationTemplate();
    textarea.dispatchEvent(new Event("input", { bubbles: true }));
    textarea.focus();
  });
  const workspaceID = activeWorkspace()?.id ?? "";
  if (workspaceID && !getWorkspaceDebugSettings(workspaceID)) {
    void loadWorkspaceDebugSettings(workspaceID, { patch: false }).then(() => {
      if (state.settingsOpen && activeWorkspace()?.id === workspaceID) {
        getAppCallbacks().render();
      }
    });
  }
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

        <div class="settings-layout">
          <nav class="settings-nav" aria-label="Settings sections">
            <ul>
              ${settingsSections
                .map(
                  (section) => `
                    <li>
                      <button type="button" data-settings-nav-target="${section.id}">
                        ${section.label}
                      </button>
                    </li>
                  `,
                )
                .join("")}
            </ul>
          </nav>

          <div class="settings-content">
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

            <section class="settings-section" aria-labelledby="comfyui-settings-title">
              <h3 id="comfyui-settings-title" class="settings-section-title">ComfyUI</h3>
              <p>Configure a remote ComfyUI instance for image generation.</p>
              <div class="settings-grid">
                <label class="field field-wide">
                  <span>ComfyUI Host URL</span>
                  <input name="comfyuiUrl" type="url" value="${escapeHtml(fieldValue("comfyuiUrl"))}" placeholder="http://127.0.0.1:8188" autocomplete="off" />
                </label>
              </div>
              <p class="compact muted">URL of your ComfyUI instance. Leave empty to disable.</p>
              <label class="field" style="margin-top:0.5rem;">
                <span>Txt2img Workflow</span>
                <input
                  name="comfyuiTxt2imgWorkflow"
                  type="text"
                  value="${escapeHtml(fieldValue("comfyuiTxt2imgWorkflow"))}"
                  placeholder="Path to txt2img workflow JSON"
                  autocomplete="off"
                />
              </label>
              <label class="field" style="margin-top:0.5rem;">
                <span>Img2img Workflow</span>
                <input
                  name="comfyuiImg2imgWorkflow"
                  type="text"
                  value="${escapeHtml(fieldValue("comfyuiImg2imgWorkflow"))}"
                  placeholder="Path to img2img workflow JSON (requires LoadImage node)"
                  autocomplete="off"
                />
              </label>
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
              <label class="settings-toggle">
                <span>Chat completion notifications</span>
                <input
                  name="enableChatCompletionNotifications"
                  type="checkbox"
                  ${chatCompletionNotificationsEnabled(state.settingsDraft) ? "checked" : ""}
                />
              </label>
              <label class="settings-toggle">
                <span>Kanban complete notifications</span>
                <input
                  name="enableKanbanCompleteNotifications"
                  type="checkbox"
                  ${kanbanCompleteNotificationsEnabled(state.settingsDraft) ? "checked" : ""}
                />
              </label>
              ${renderPushNotificationPermissionStatus()}
            </section>

            <section class="settings-section" aria-labelledby="programming-settings-title">
              <h3 id="programming-settings-title" class="settings-section-title">Programming</h3>
              <label class="settings-toggle" title="Only allow 1 Kanban card to execute at a time; useful for memory constrained environments.">
                <span>Limit Kanban concurrency</span>
                <input
                  name="limitKanbanConcurrency"
                  type="checkbox"
                  ${limitKanbanConcurrencyEnabled(state.settingsDraft) ? "checked" : ""}
                />
              </label>
              <label class="field" title="Maximum number of chat research agents that may run at once. Set to 0 to disable research agents.">
                <span>Research agent concurrency</span>
                <input
                  name="researchAgentConcurrency"
                  type="number"
                  min="0"
                  max="8"
                  step="1"
                  value="${escapeAttribute(fieldValue("researchAgentConcurrency") || "4")}"
                />
                <span class="field-help">Set to 0 to disable research agents and use direct chat tools.</span>
              </label>
              <label class="settings-toggle">
                <span>Leading whitespace indicators</span>
                <input
                  name="hideLeadingWhitespaceIndicators"
                  type="checkbox"
                  data-settings-inverted-boolean
                  ${leadingWhitespaceIndicatorsEnabled(state.settingsDraft) ? "checked" : ""}
                />
              </label>
              <label class="settings-toggle" title="Use a side-by-side Git diff layout on wide windows. Narrow windows always use the combined diff.">
                <span>Split Git diff view</span>
                <input
                  name="disableGitSplitDiffView"
                  type="checkbox"
                  data-settings-inverted-boolean
                  ${gitSplitDiffViewEnabled(state.settingsDraft) ? "checked" : ""}
                />
              </label>
            </section>

            ${renderDebugSettingsSection()}

            ${renderWebAccessSettings()}

            ${renderBudgetSettingsSection()}

            ${renderLivenessSettingsSection()}

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
                                <label class="field field-wide workspace-build-command-field">
                                  <span>Build command</span>
                                  <textarea
                                    name="workspaceBuildCommand"
                                    rows="2"
                                    placeholder="go test -tags=&quot;debug editor&quot; ./..."
                                    data-workspace-build-command
                                    data-workspace-id="${escapeAttribute(workspace.id)}"
                                  >${escapeHtml(workspaceBuildCommandDraft(workspace))}</textarea>
                                </label>
                                ${renderWorkspaceFolderSettings(workspace)}
                              </div>
                              <label class="settings-toggle workspace-default-plan-mode">
                                <span>Plan by default</span>
                                <input
                                  type="checkbox"
                                  ${workspace.defaultPlanMode ? "checked" : ""}
                                  data-workspace-default-plan-mode
                                  data-workspace-id="${escapeAttribute(workspace.id)}"
                                />
                              </label>
                              <label class="settings-toggle workspace-parent-git-repositories" title="Allow Git tools to use a repository found above the workspace folder.">
                                <span>Search parent folders for Git</span>
                                <input
                                  type="checkbox"
                                  ${workspace.searchParentGitRepositories ? "checked" : ""}
                                  data-workspace-parent-git-repositories
                                  data-workspace-id="${escapeAttribute(workspace.id)}"
                                />
                              </label>
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

            ${renderAgentModesSection()}

            <section class="settings-section" aria-labelledby="development-settings-title">
              <h3 id="development-settings-title" class="settings-section-title">Development</h3>
              <label class="settings-toggle" title="Capture the exact AI transcript for this app session.">
                <span>AI flow logging</span>
                <input
                  type="checkbox"
                  data-development-logging
                  ${state.developmentLogStatus?.enabled ? "checked" : ""}
                />
              </label>
              <p class="field-help">Writes JSONL to <code>./echo/echo.log</code> relative to the process working directory. Enabling erases the previous capture, and this setting is not remembered after restart.</p>
              <p class="field-help warning">The exact transcript may contain sensitive prompts, workspace content, paths, tool output, and embedded media.</p>
              <p>Echo source workspace actions.</p>
              ${renderRebuildRelaunchButton()}
            </section>
          </div>
        </div>

        <footer class="settings-footer">
          <button class="secondary-button" type="button" data-action="reset-settings">Reset</button>
          <button class="primary-button" type="submit">Save</button>
        </footer>
      </form>
    </div>
  `;
}

/* ── Agent Modes settings section ── */

function renderDebugSettingsSection(): string {
  const workspace = activeWorkspace();
  if (!workspace) {
    return `
      <section class="settings-section" aria-labelledby="debug-settings-title">
        <h3 id="debug-settings-title" class="settings-section-title">Debug</h3>
        <p class="empty-state compact">Select a workspace to configure debugging.</p>
      </section>`;
  }
  const settings = getWorkspaceDebugSettings(workspace.id);
  if (!settings) {
    return `
      <section class="settings-section" aria-labelledby="debug-settings-title">
        <h3 id="debug-settings-title" class="settings-section-title">Debug</h3>
        <p class="empty-state compact"><span class="spinner" aria-hidden="true"></span> Loading workspace debug settings&hellip;</p>
      </section>`;
  }
  return `
    <section class="settings-section debug-settings-section" aria-labelledby="debug-settings-title">
      <div class="settings-section-heading">
        <div>
          <h3 id="debug-settings-title" class="settings-section-title">Debug</h3>
          <p>Launch configurations for <strong>${escapeHtml(workspace.displayName)}</strong>.</p>
        </div>
        <button class="secondary-button compact-button" type="button" data-debug-template>Use Go/Delve template</button>
      </div>
      <label class="field field-wide">
        <span>Workspace debug JSON</span>
        <textarea
          class="debug-settings-json"
          rows="18"
          spellcheck="false"
          autocomplete="off"
          data-debug-settings-json
          data-debug-workspace-id="${escapeAttribute(workspace.id)}"
          data-debug-revision="${escapeAttribute(settings.revision)}"
          data-debug-original="${escapeAttribute(settings.json)}"
        >${escapeHtml(settings.json)}</textarea>
      </label>
      <p class="field-help">Stored at <code>${escapeHtml(settings.storagePath)}</code>. This editor accepts strict JSON only (no comments or trailing commas). Configuration names must be unique and non-empty.</p>
      <p class="field-help warning">Environment values are stored literally and may commit secrets. Prefer <code>\${env:NAME}</code> when possible.</p>
    </section>`;
}

function goDebugConfigurationTemplate() {
  return JSON.stringify({
    version: "0.2.0",
    configurations: [{
      name: "Launch Go Package",
      type: "go",
      request: "launch",
      mode: "debug",
      program: "${workspaceFolder}",
      cwd: "${workspaceFolder}",
      args: [],
      env: {},
    }],
  }, null, 2);
}

async function saveWorkspaceDebugSettingsFromForm(form: HTMLFormElement) {
  const textarea = form.querySelector<HTMLTextAreaElement>("[data-debug-settings-json]");
  if (!textarea || textarea.value === textarea.dataset.debugOriginal) {
    return;
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(textarea.value);
  } catch (error) {
    throw new Error(`Debug configuration is not valid strict JSON: ${errorMessage(error)}`);
  }
  if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
    throw new Error("Debug configuration must be a JSON object.");
  }
  const workspaceID = textarea.dataset.debugWorkspaceId ?? "";
  if (!workspaceID) {
    throw new Error("The debug settings workspace is unavailable.");
  }
  const saved = await SaveWorkspaceDebugSettings(workspaceID, {
    json: textarea.value,
    expectedRevision: textarea.dataset.debugRevision ?? "",
  });
  applyWorkspaceDebugSettings(saved);
}

function renderAgentModesSection(): string {
  const ws = activeWorkspace();
  const workspaceID = ws?.id ?? "";
  const modes = agentModesForWorkspace(workspaceID);
  const editingId = state.agentModeEditingId ?? "";
  const isCreating = state.agentModeCreating === true;

  return `
    <section class="settings-section" aria-labelledby="agent-modes-settings-title">
      <div class="settings-section-heading">
        <h3 id="agent-modes-settings-title" class="settings-section-title">Agent Modes</h3>
        ${!isCreating ? `
          <button class="secondary-button compact-button" type="button" data-action="create-agent-mode">
            ${icons.plus}
            <span>Add Mode</span>
          </button>
        ` : ""}
      </div>
      ${workspaceID ? `` : `<p class="empty-state compact">No workspace selected.</p>`}

      ${isCreating ? renderAgentModeForm(null) : ""}

      <div class="agent-mode-list">
        ${workspaceID && modes.length === 0 && !isCreating
          ? `<p class="empty-state compact">No custom agent modes. Add one to define custom system prompts and tool permissions.</p>`
          : workspaceID ? modes.map((mode) => renderAgentModeRow(mode, editingId)).join("") : ""
        }
      </div>
    </section>
  `;
}

function renderAgentModeRow(mode: services.AgentMode, editingId: string): string {
  const isEditing = mode.id === editingId;
  const toolCount = mode.toolPermissions?.length ?? 0;
  const pathCount = mode.pathPermissions?.length ?? 0;
  const hasPermissions = toolCount > 0 || pathCount > 0;

  if (isEditing) {
    return renderAgentModeForm(mode);
  }

  return `
    <div class="agent-mode-row" data-agent-mode-id="${escapeAttribute(mode.id)}">
      <div class="agent-mode-main">
        <strong>${escapeHtml(mode.name)}</strong>
        <span>${mode.builtIn ? "Built-in" : (mode.prompt?.trim() || "No custom prompt")}${hasPermissions ? " · " + renderAgentModePermissionSummary(mode) : ""}</span>
      </div>
      <div class="agent-mode-actions">
        ${!mode.builtIn ? `
          <button class="icon-button" type="button" title="Edit mode" aria-label="Edit ${escapeAttribute(mode.name)}" data-action="edit-agent-mode" data-agent-mode-id="${escapeAttribute(mode.id)}">
            ${icons.edit}
          </button>
          <button class="icon-button danger-button" type="button" title="Delete mode" aria-label="Delete ${escapeAttribute(mode.name)}" data-action="delete-agent-mode-settings" data-agent-mode-id="${escapeAttribute(mode.id)}">
            ${icons.trash}
          </button>
        ` : ""}
      </div>
    </div>
  `;
}

function renderAgentModePermissionSummary(mode: services.AgentMode): string {
  const perms = mode.permissions;
  if (perms && Object.keys(perms).length > 0) {
    return `${Object.keys(perms).length} tool${Object.keys(perms).length > 1 ? "s" : ""}`;
  }
  // Fall back to legacy fields.
  const parts: string[] = [];
  if (mode.toolPermissions?.length) {
    parts.push(`${mode.toolPermissions.length} tool${mode.toolPermissions.length > 1 ? "s" : ""}`);
  }
  if (mode.pathPermissions?.length) {
    parts.push(`${mode.pathPermissions.length} path${mode.pathPermissions.length > 1 ? "s" : ""}`);
  }
  return parts.join(", ");
}

function extractPermissionsMap(mode: services.AgentMode): Record<string, string[]> | null {
  // Prefer new Permissions map.
  if (mode.permissions && Object.keys(mode.permissions).length > 0) {
    const result: Record<string, string[]> = {};
    for (const [toolName, perm] of Object.entries(mode.permissions)) {
      if (perm) {
        result[toolName] = perm.paths ? [...perm.paths] : [];
      }
    }
    return result;
  }
  // Fall back to legacy flat lists.
  const toolNames = mode.toolPermissions ?? [];
  const paths = mode.pathPermissions ?? [];
  if (toolNames.length === 0) return null;
  const result: Record<string, string[]> = {};
  for (const name of toolNames) {
    result[name] = paths.length > 0 ? [...paths] : [];
  }
  return result;
}

function renderAgentModeForm(mode: services.AgentMode | null): string {
  const isNew = mode === null;
  const name = mode?.name ?? state.agentModeDraftName ?? "";
  const prompt = mode?.prompt ?? state.agentModeDraftPrompt ?? "";

  // Build per-tool permissions from draft or existing mode.
  let permissions: Record<string, string[]> | null = null;
  if (isNew) {
    permissions = state.agentModeDraftPermissions && Object.keys(state.agentModeDraftPermissions).length > 0
      ? { ...state.agentModeDraftPermissions }
      : null;
  } else {
    permissions = extractPermissionsMap(mode);
  }

  const selectedTools = new Set<string>(permissions ? Object.keys(permissions) : []);

  return `
    <div class="agent-mode-form" data-agent-mode-form>
      <div class="settings-grid">
        <label class="field">
          <span>Name</span>
          <input name="agentModeName" type="text" value="${escapeAttribute(name)}" autocomplete="off" required data-agent-mode-field data-agent-mode-field-name="name" data-initial-focus />
        </label>
      </div>
      <label class="field field-wide">
        <span>System Prompt</span>
        <textarea name="agentModePrompt" rows="4" data-agent-mode-field data-agent-mode-field-name="prompt">${escapeHtml(prompt)}</textarea>
      </label>
      <div class="agent-mode-permissions-section">
        <h4 class="agent-mode-permissions-heading">Tool Permissions</h4>
        <p class="compact muted">Select tools and configure allowed paths per tool.</p>
        ${renderPerToolPermissionRows(permissions ?? {}, selectedTools)}
      </div>
      <div class="agent-mode-form-actions">
        <button class="primary-button compact-button" type="button" data-action="${isNew ? "save-new-agent-mode" : "save-agent-mode"}" ${mode ? `data-agent-mode-id="${escapeAttribute(mode.id)}"` : ""}>Save</button>
        <button class="secondary-button compact-button" type="button" data-action="cancel-agent-mode">Cancel</button>
      </div>
    </div>
  `;
}

function renderPerToolPermissionRows(permissions: Record<string, string[]>, selectedTools: Set<string>): string {
  return availableToolNames.map((toolName) => {
    const isChecked = selectedTools.has(toolName);
    const paths = permissions[toolName] ?? [];
    const pathsText = paths.join("\n");
    return `
      <div class="per-tool-permission-row" data-per-tool-tool="${escapeAttribute(toolName)}">
        <label class="per-tool-checkbox-label">
          <input
            type="checkbox"
            ${isChecked ? "checked" : ""}
            data-per-tool-checkbox
            data-per-tool-name="${escapeAttribute(toolName)}"
          />
          <span>${escapeHtml(toolName)}</span>
        </label>
        <div class="per-tool-paths-container ${!isChecked ? "is-collapsed" : ""}">
          <textarea
            rows="2"
            placeholder="One glob per line; leave empty for all paths&#10;e.g. **/*.go"
            ${!isChecked ? "disabled" : ""}
            data-per-tool-paths
            data-per-tool-name="${escapeAttribute(toolName)}"
          >${escapeHtml(pathsText)}</textarea>
        </div>
      </div>
    `;
  }).join("");
}

/* ── Agent Mode state helpers (exported for actions.ts) ── */

export function startCreateAgentMode() {
  state.agentModeCreating = true;
  state.agentModeEditingId = "";
  state.agentModeDraftName = "";
  state.agentModeDraftPrompt = "";
  state.agentModeDraftToolPermissions = [];
  state.agentModeDraftPathPermissions = [];
  state.agentModeDraftPermissions = {};
  state.formError = "";
}

export function startEditAgentMode(modeID: string) {
  const ws = activeWorkspace();
  if (!ws) return;
  const modes = agentModesForWorkspace(ws.id);
  const mode = modes.find((m) => m.id === modeID);
  if (!mode || mode.builtIn) {
    return;
  }
  state.agentModeEditingId = modeID;
  state.agentModeCreating = false;
  state.agentModeDraftName = mode.name ?? "";
  state.agentModeDraftPrompt = mode.prompt ?? "";
  // Keep legacy fields populated for backward compat.
  state.agentModeDraftToolPermissions = [...(mode.toolPermissions ?? [])];
  state.agentModeDraftPathPermissions = [...(mode.pathPermissions ?? [])];
  // Populate new per-tool permissions map.
  const permsMap = extractPermissionsMap(mode);
  state.agentModeDraftPermissions = permsMap ? { ...permsMap } : {};
  state.formError = "";
}

export function cancelAgentMode() {
  state.agentModeCreating = false;
  state.agentModeEditingId = "";
  state.agentModeDraftName = "";
  state.agentModeDraftPrompt = "";
  state.agentModeDraftToolPermissions = [];
  state.agentModeDraftPathPermissions = [];
  state.agentModeDraftPermissions = {};
  state.formError = "";
}

export async function saveNewAgentMode() {
  const ws = activeWorkspace();
  if (!ws) return;
  const name = (state.agentModeDraftName ?? "").trim();
  if (!name) {
    state.formError = "Mode name is required.";
    getAppCallbacks().render();
    return;
  }
  const prompt = state.agentModeDraftPrompt ?? "";
  const permissions = collectPermissionsFromForm();

  try {
    const modes = await CreateAgentModePerTool(name, prompt, permissions);
    state.agentModes.set(ws.id, modes);
    cancelAgentMode();
    pushToast(`Agent mode "${name}" created.`, "success");
    getAppCallbacks().render();
  } catch (error) {
    state.formError = errorMessage(error);
    getAppCallbacks().render();
  }
}

export async function saveAgentMode(modeID: string) {
  const ws = activeWorkspace();
  if (!ws) return;
  const name = (state.agentModeDraftName ?? "").trim();
  if (!name) {
    state.formError = "Mode name is required.";
    getAppCallbacks().render();
    return;
  }
  const prompt = state.agentModeDraftPrompt ?? "";
  const permissions = collectPermissionsFromForm();

  try {
    const modes = await UpdateAgentModePerTool(modeID, name, prompt, permissions);
    state.agentModes.set(ws.id, modes);
    cancelAgentMode();
    pushToast(`Agent mode "${name}" saved.`, "success");
    getAppCallbacks().render();
  } catch (error) {
    state.formError = errorMessage(error);
    getAppCallbacks().render();
  }
}

// collectPermissionsFromForm reads the current DOM per-tool permission inputs
// and builds a Record<toolName, string[]> map. If no tools are selected, returns {}.
function collectPermissionsFromForm(): Record<string, string[]> {
  const form = document.querySelector<HTMLFormElement>("[data-settings-form]");
  if (!form) return {};

  const result: Record<string, string[]> = {};
  for (const checkbox of form.querySelectorAll<HTMLInputElement>("input[data-per-tool-checkbox]:checked")) {
    const toolName = checkbox.dataset.perToolName ?? "";
    if (!toolName) continue;
    const textarea = form.querySelector<HTMLTextAreaElement>(`textarea[data-per-tool-name="${CSS.escape(toolName)}"]`);
    const pathsText = textarea?.value ?? "";
    const paths = parsePermissionLines(pathsText);
    result[toolName] = paths;
  }
  return result;
}

export async function deleteAgentModeSettings(modeID: string) {
  const ws = activeWorkspace();
  if (!ws) return;
  const modes = agentModesForWorkspace(ws.id);
  const mode = modes.find((m) => m.id === modeID);
  if (!mode || mode.builtIn) {
    return;
  }
  if (!window.confirm(`Delete agent mode "${mode.name}"?`)) {
    return;
  }
  try {
    const updated = await DeleteAgentMode(modeID);
    state.agentModes.set(ws.id, Array.isArray(updated) ? updated : []);
    if (state.agentModeEditingId === modeID) {
      cancelAgentMode();
    }
    pushToast(`Agent mode "${mode.name}" deleted.`, "success");
    getAppCallbacks().render();
  } catch (error) {
    state.formError = errorMessage(error);
    getAppCallbacks().render();
  }
}

function parsePermissionLines(text: string): string[] {
  const result: string[] = [];
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (trimmed) {
      result.push(trimmed);
    }
  }
  return result;
}

function handleAgentModeFieldInput(input: HTMLInputElement | HTMLTextAreaElement) {
  const fieldName = input.dataset.agentModeFieldName;
  if (!fieldName) return;

  switch (fieldName) {
    case "name":
      state.agentModeDraftName = (input as HTMLInputElement).value;
      break;
    case "prompt":
      state.agentModeDraftPrompt = (input as HTMLTextAreaElement).value;
      break;
    case "toolPermissions":
      state.agentModeDraftToolPermissions = parsePermissionLines((input as HTMLTextAreaElement).value);
      break;
    case "pathPermissions":
      state.agentModeDraftPathPermissions = parsePermissionLines((input as HTMLTextAreaElement).value);
      break;
  }
  state.formError = "";
}

function handlePerToolCheckbox(checkbox: HTMLInputElement) {
  const toolName = checkbox.dataset.perToolName ?? "";
  if (!toolName) return;

  // Update draft permissions map.
  if (!state.agentModeDraftPermissions) {
    state.agentModeDraftPermissions = {};
  }

  if (checkbox.checked) {
    if (!state.agentModeDraftPermissions[toolName]) {
      state.agentModeDraftPermissions[toolName] = [];
    }
    // Expand the paths container.
    const row = checkbox.closest<HTMLElement>("[data-per-tool-tool]");
    const container = row?.querySelector<HTMLElement>(".per-tool-paths-container");
    if (container) {
      container.classList.remove("is-collapsed");
      const textarea = container.querySelector<HTMLTextAreaElement>("textarea");
      if (textarea) textarea.disabled = false;
    }
  } else {
    delete state.agentModeDraftPermissions[toolName];
    // Collapse the paths container.
    const row = checkbox.closest<HTMLElement>("[data-per-tool-tool]");
    const container = row?.querySelector<HTMLElement>(".per-tool-paths-container");
    if (container) {
      container.classList.add("is-collapsed");
      const textarea = container.querySelector<HTMLTextAreaElement>("textarea");
      if (textarea) {
        textarea.disabled = true;
        textarea.value = "";
      }
    }
  }
}

function handlePerToolPathsInput(textarea: HTMLTextAreaElement) {
  const toolName = textarea.dataset.perToolName ?? "";
  if (!toolName) return;
  const paths = parsePermissionLines(textarea.value);
  if (!state.agentModeDraftPermissions) {
    state.agentModeDraftPermissions = {};
  }
  // Ensure the tool is in the map (checkbox may not be checked but paths edited).
  const checkbox = document.querySelector<HTMLInputElement>(`input[data-per-tool-name="${CSS.escape(toolName)}"]`);
  if (checkbox && !checkbox.checked) {
    checkbox.checked = true;
  }
  state.agentModeDraftPermissions[toolName] = paths;
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
              data-endpoint-field=\"model\"
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
          <label class="field field-wide">
            <span>Headers</span>
            <textarea
              name="endpointHeaders-${escapeAttribute(id)}"
              rows="4"
              placeholder="key: value&#10;Authorization: Bearer my-token&#10;X-Custom-Header: value"
              autocomplete="off"
              spellcheck="false"
              data-llm-endpoint-field
              data-endpoint-id="${escapeAttribute(id)}"
              data-endpoint-field="headers"
            >${escapeHtml(headersToText(endpoint))}</textarea>
          </label>
          <label class="field field-wide">
            <span>System Prompt Appendage</span>
            <textarea
              name="systemPromptAppendage-${escapeAttribute(id)}"
              rows="5"
              placeholder="Additional model-specific instructions appended to the system prompt"
              autocomplete="off"
              data-llm-endpoint-field
              data-endpoint-id="${escapeAttribute(id)}"
              data-endpoint-field="systemPromptAppendage"
            >${escapeHtml(endpoint.systemPromptAppendage ?? "")}</textarea>
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
  if (typeof expected === "string") {
    return (current ?? "") === expected;
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
        headers: endpoint.headers,
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
    systemPromptAppendage: settings?.systemPromptAppendage ?? "",
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
    systemPromptAppendage: endpoint.systemPromptAppendage ?? defaults.systemPromptAppendage,
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
  const kanban = validEndpointID(raw?.kanban, endpoints) ? raw!.kanban : fallback;
  return {
    chat: validEndpointID(raw?.chat, endpoints) ? raw!.chat : fallback,
    research: validEndpointID(raw?.research, endpoints)
      ? raw!.research
      : validEndpointID(raw?.chat, endpoints) ? raw!.chat : fallback,
    kanbanDecompose: validEndpointID(raw?.kanbanDecompose, endpoints)
      ? raw!.kanbanDecompose
      : kanban,
    kanban,
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
  source.headers = chatEndpoint?.headers;
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

function handleLLMEndpointFieldInput(input: HTMLInputElement | HTMLTextAreaElement) {
  if (!state.settingsDraft) {
    return;
  }
  const endpointID = input.dataset.endpointId ?? "";
  const field = input.dataset.endpointField;
  if (!endpointID || !isEndpointField(field)) {
    return;
  }
  let value: string | number | boolean | Record<string, string> | undefined;
  if (field === "headers") {
    value = parseHeadersText((input as HTMLTextAreaElement).value);
  } else if (field === "systemPromptAppendage") {
    value = input.value;
  } else if (input instanceof HTMLInputElement) {
    value =
      input.type === "checkbox"
        ? input.checked
        : isEndpointNumericField(field)
          ? Number(input.value)
          : input.value;
  } else {
    return;
  }
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
  if (field === "thinkingTokenBudget" && input instanceof HTMLInputElement) {
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

type EndpointField = "name" | "endpoint" | "model" | "headers" | LLMPresetField;

function isEndpointField(value: string | undefined): value is EndpointField {
  return value === "name" || value === "endpoint" || value === "model" || value === "headers" ||
    (llmPresetFields as readonly string[]).includes(value ?? "");
}

function isEndpointNumericField(value: EndpointField): value is Exclude<LLMPresetField, "thinkingCorrection" | "systemPromptAppendage"> {
  return value !== "name" &&
    value !== "endpoint" &&
    value !== "model" &&
    value !== "headers" &&
    value !== "thinkingCorrection" &&
    value !== "systemPromptAppendage";
}

/* ── Headers helpers ── */

function headersToText(endpoint: llm.LLMEndpoint): string {
  const headers = endpoint.headers;
  if (!headers) {
    return "";
  }
  return Object.entries(headers)
    .map(([key, value]) => `${key}: ${value}`)
    .join("\n");
}

function parseHeadersText(text: string): Record<string, string> | undefined {
  const result: Record<string, string> = {};
  let hasHeaders = false;
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const colonIndex = trimmed.indexOf(":");
    if (colonIndex <= 0) continue;
    const key = trimmed.slice(0, colonIndex).trim();
    const value = trimmed.slice(colonIndex + 1).trim();
    if (key && value) {
      result[key] = value;
      hasHeaders = true;
    }
  }
  return hasHeaders ? result : undefined;
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
      <label class="settings-toggle">
        <span>Enable HTTPS (required for mobile voice input)</span>
        <input
          name="enableTLS"
          type="checkbox"
          data-web-access-field
          ${draft.enableTLS ? "checked" : ""}
        />
      </label>
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

function renderRebuildRelaunchButton(): string {
  const echoWorkspace = findEchoSourceWorkspace();
  const disabled = echoWorkspace && state.runningKanbanWorkspaces.has(echoWorkspace.id);
  return `
    <div class="development-actions">
      <p class="compact">Rebuilds the Echo application and relaunches it. Requires the Echo source workspace to be added.</p>
      <button
        class="secondary-button danger-button"
        type="button"
        data-action="rebuild-and-relaunch"
        ${disabled ? "disabled" : ""}
      >Rebuild & Relaunch</button>
      ${disabled ? "<p class='form-error compact'>Cannot rebuild while Kanban agents are running in the Echo source workspace.</p>" : ""}
    </div>
  `;
}

function renderPushNotificationPermissionStatus(): string {
  if (typeof Notification === "undefined") {
    return "";
  }
  const permission = Notification.permission;
  if (permission === "granted") {
    return "";
  }
  if (permission === "denied") {
    return `
      <p class="compact muted">
        Browser notifications are blocked.
        <button type="button" class="inline-link" data-action="request-push-notification-permission">Enable browser notifications</button>
      </p>
    `;
  }
  // permission === "default" — prompt not yet shown
  return `
    <p class="compact muted">
      <button type="button" class="inline-link" data-action="request-push-notification-permission">Enable browser notifications</button>
    </p>
  `;
}

export async function handleRequestPushNotificationPermission(): Promise<void> {
  const { requestPushNotificationPermission } = await import("../notifications");
  const result = await requestPushNotificationPermission();
  if (result === "granted") {
    pushToast("Browser notifications enabled.", "success");
  } else if (result === "denied") {
    pushToast("Browser notifications blocked. You can enable them in your browser settings.", "error");
  }
  getAppCallbacks().render();
}

function findEchoSourceWorkspace(): services.Workspace | null {
  const workspaces = state.appState?.workspaces ?? [];
  for (const workspace of workspaces) {
    const folders = workspace.folders ?? [];
    for (const folder of folders) {
      if (!folder.missing && folder.path && /[/\\]echo$/i.test(folder.path)) {
        return workspace;
      }
    }
  }
  return null;
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

export async function saveSettingsImmediately(): Promise<void> {
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
    state.settingsDraft = cloneSettings(state.appState.settings);
    state.settingsEndpointEditId = "";
    applyTheme(state.appState.settings);
    state.formError = "";
    pushToast("Settings saved.", "success");
    getAppCallbacks().render();
  } catch (error) {
    state.formError = errorMessage(error);
    getAppCallbacks().render();
  }
}

export function handleSettingsInput(event: Event) {
  const input = event.currentTarget as HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement;
  if (input.dataset.agentModeField !== undefined) {
    handleAgentModeFieldInput(input as HTMLInputElement | HTMLTextAreaElement);
    return;
  }
  if (input.dataset.perToolCheckbox !== undefined) {
    handlePerToolCheckbox(input as HTMLInputElement);
    return;
  }
  if (input.dataset.perToolPaths !== undefined) {
    handlePerToolPathsInput(input as HTMLTextAreaElement);
    return;
  }
  if (input.dataset.llmEndpointSelection !== undefined && input instanceof HTMLSelectElement) {
    handleLLMEndpointSelectionInput(input);
    return;
  }
  if (input.dataset.llmEndpointField !== undefined && (input instanceof HTMLInputElement || input instanceof HTMLTextAreaElement)) {
    handleLLMEndpointFieldInput(input);
    return;
  }
  if (input.dataset.themeToken !== undefined) {
    handleThemeColorInput(input as HTMLInputElement);
    return;
  }
  if (input.dataset.budgetLimitInput !== undefined) {
    void handleBudgetLimitInput(input as HTMLInputElement);
    return;
  }
  if (input.dataset.livenessField !== undefined) {
    void handleLivenessInput(input as HTMLInputElement);
    return;
  }
  if (input.dataset.developmentLogging !== undefined) {
    return;
  }
  if (input.dataset.workspaceFolderAgents !== undefined) {
    return;
  }
  if (input.dataset.workspaceDefaultPlanMode !== undefined) {
    return;
  }
  if (input.dataset.workspaceParentGitRepositories !== undefined) {
    return;
  }
  if (input.dataset.webAccessField !== undefined) {
    void handleWebAccessInput(input as HTMLInputElement);
    return;
  }
  if (input.dataset.workspaceLetter !== undefined) {
    state.workspaceLetterDrafts.set(input.dataset.workspaceId ?? "", input.value);
    state.formError = "";
    return;
  }
  if (input.dataset.workspaceBuildCommand !== undefined) {
    state.workspaceBuildCommandDrafts.set(input.dataset.workspaceId ?? "", input.value);
    state.formError = "";
    return;
  }
  if (!state.settingsDraft) {
    return;
  }

  /* Textareas handled above; remaining inputs are HTMLInputElement | HTMLSelectElement */
  if (!(input instanceof HTMLInputElement || input instanceof HTMLSelectElement)) {
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
    "researchAgentConcurrency",
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

  // Immediately persist settings when a checkbox is toggled.
  if (input.type === "checkbox") {
    void saveSettingsImmediately();
  }
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

async function handleWebAccessInput(input: HTMLInputElement) {
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
  if (input.name !== "enabled" && input.name !== "enableTLS") {
    return;
  }
  if (!state.webAccessDraft.enabled) {
    state.webAccessQRCodeURL = "";
  }
  try {
    state.appState = await SaveWebAccessSettings(state.webAccessDraft);
    state.webAccessDraft = cloneWebAccessSettings(state.appState.webAccess);
    state.webAccessStatus = await LoadWebAccessStatus();
    if (!state.webAccessDraft.enabled || !state.webAccessStatus.running) {
      state.webAccessQRCodeURL = "";
    }
    state.formError = "";
  } catch (error) {
    state.formError = errorMessage(error);
    try {
      state.webAccessStatus = await LoadWebAccessStatus();
    } catch {
      state.webAccessStatus = null;
    }
  } finally {
    getAppCallbacks().render();
  }
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

export async function handleWorkspaceDefaultPlanModeChange(input: HTMLInputElement) {
  const workspaceID = input.dataset.workspaceId ?? "";
  if (!workspaceID) {
    return;
  }
  try {
    state.appState = await SetWorkspaceDefaultPlanMode(workspaceID, input.checked);
    getAppCallbacks().render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    getAppCallbacks().render();
  }
}

export async function handleWorkspaceParentGitRepositoriesChange(input: HTMLInputElement) {
  const workspaceID = input.dataset.workspaceId ?? "";
  if (!workspaceID) {
    return;
  }
  try {
    state.appState = await SetWorkspaceSearchParentGitRepositories(workspaceID, input.checked);
    getAppCallbacks().render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    getAppCallbacks().render();
  }
}

export async function handleDevelopmentLoggingChange(input: HTMLInputElement) {
  input.disabled = true;
  try {
    state.developmentLogStatus = await SetDevelopmentLoggingEnabled(input.checked);
    pushToast(input.checked ? "AI flow logging enabled." : "AI flow logging disabled.", "success");
  } catch (error) {
    try {
      state.developmentLogStatus = await LoadDevelopmentLogStatus();
    } catch {
      state.developmentLogStatus = null;
    }
    pushToast(errorMessage(error), "error");
  }
  getAppCallbacks().render();
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
    await saveWorkspaceDebugSettingsFromForm(event.currentTarget as HTMLFormElement);
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
      const buildCommandDraft = state.workspaceBuildCommandDrafts.get(workspace.id);
      if (buildCommandDraft !== undefined && buildCommandDraft !== (workspace.buildCommand ?? "")) {
        state.appState = await SetWorkspaceBuildCommand(workspace.id, buildCommandDraft);
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
