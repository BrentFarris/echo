import { GetLivenessConfig, SetLivenessConfig } from "../backend/services";
import { services } from "../../wailsjs/go/models";
import { activeWorkspace, state } from "./state";
import { pushToast } from "./toasts";
import { escapeAttribute, escapeHtml, errorMessage } from "./utils";

// Map of workspaceID → LivenessConfig
export const livenessConfigs = new Map<string, services.LivenessConfig>();

export function getLivenessConfig(workspaceID: string): services.LivenessConfig | null {
  return livenessConfigs.get(workspaceID) ?? null;
}

export async function loadLivenessConfig(workspaceID: string): Promise<void> {
  try {
    const cfg = await GetLivenessConfig(workspaceID);
    if (cfg) {
      livenessConfigs.set(workspaceID, cfg);
    } else {
      livenessConfigs.delete(workspaceID);
    }
  } catch {
    // Config not available or workspace missing
    livenessConfigs.delete(workspaceID);
  }
}

export async function setLivenessConfig(workspaceID: string, cfg: services.LivenessConfig): Promise<void> {
  try {
    await SetLivenessConfig(workspaceID, cfg);
    await loadLivenessConfig(workspaceID);
    pushToast("Liveness configuration saved.", "success");
  } catch (error) {
    pushToast(errorMessage(error), "error");
  }
}

// Convert nanoseconds (Go time.Duration) to minutes for display.
function durationToMinutes(ns: number): number {
  if (!ns) return 0;
  const ms = ns / 1_000_000;
  return Math.round(ms / 60_000);
}

// Convert minutes to nanoseconds (Go time.Duration).
function minutesToDuration(mins: number): number {
  if (mins <= 0) return 0;
  return mins * 60_000_000_000;
}

export function renderLivenessSettingsSection(): string {
  const ws = activeWorkspace();
  if (!ws) return "";

  const cfg = livenessConfigs.get(ws.id);
  const enabled = cfg?.enabled ?? false;
  const stallTimeoutMinutes = durationToMinutes(cfg?.stallTimeout ?? 0);
  const maxAutoRetries = cfg?.maxAutoRetries ?? 3;
  const checkIntervalMinutes = durationToMinutes(cfg?.checkInterval ?? 0);

  return `
    <section class="settings-section" aria-labelledby="liveness-settings-title">
      <h3 id="liveness-settings-title" class="settings-section-title">Liveness Enforcement</h3>
      <label class="settings-toggle">
        <span>Enable liveness enforcement</span>
        <input
          name="livenessEnabled"
          type="checkbox"
          data-liveness-field
          data-liveness-field-name="enabled"
          ${enabled ? "checked" : ""}
        />
      </label>
      <div class="settings-grid">
        <label class="field">
          <span>Stall timeout (minutes)</span>
          <input
            name="livenessStallTimeout"
            type="number"
            min="1"
            step="1"
            value="${escapeHtml(String(stallTimeoutMinutes || 10))}"
            data-liveness-field
            data-liveness-field-name="stallTimeout"
          />
        </label>
        <label class="field">
          <span>Max auto retries</span>
          <input
            name="livenessMaxAutoRetries"
            type="number"
            min="0"
            step="1"
            value="${escapeHtml(String(maxAutoRetries))}"
            data-liveness-field
            data-liveness-field-name="maxAutoRetries"
          />
        </label>
        <label class="field">
          <span>Check interval (minutes)</span>
          <input
            name="livenessCheckInterval"
            type="number"
            min="1"
            step="1"
            value="${escapeHtml(String(checkIntervalMinutes || 1))}"
            data-liveness-field
            data-liveness-field-name="checkInterval"
          />
        </label>
      </div>
      <p class="compact muted">Detect stalled Kanban cards and automatically reset or escalate them. Stall timeout is the duration without progress before a card is considered stalled.</p>
    </section>
  `;
}

export async function handleLivenessInput(input: HTMLInputElement): Promise<void> {
  const ws = activeWorkspace();
  if (!ws) return;

  const fieldName = input.dataset.livenessFieldName;
  if (!fieldName) return;

  // Load current config if not present.
  let cfg = livenessConfigs.get(ws.id);
  if (!cfg) {
    cfg = services.LivenessConfig.createFrom({
      enabled: false,
      stallTimeout: minutesToDuration(10),
      maxAutoRetries: 3,
      checkInterval: minutesToDuration(1),
    });
  }

  switch (fieldName) {
    case "enabled":
      cfg.enabled = input.checked;
      break;
    case "stallTimeout": {
      const mins = Number(input.value);
      if (isNaN(mins) || mins < 1) return;
      cfg.stallTimeout = minutesToDuration(mins);
      break;
    }
    case "maxAutoRetries": {
      const retries = Number(input.value);
      if (isNaN(retries) || retries < 0) return;
      cfg.maxAutoRetries = retries;
      break;
    }
    case "checkInterval": {
      const mins = Number(input.value);
      if (isNaN(mins) || mins < 1) return;
      cfg.checkInterval = minutesToDuration(mins);
      break;
    }
    default:
      return;
  }

  livenessConfigs.set(ws.id, cfg);
  await setLivenessConfig(ws.id, cfg);
}
