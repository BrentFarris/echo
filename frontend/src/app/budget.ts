import { GetTokenBudget, ResetTokenBudget, SetTokenBudget } from "../backend/services";
import { services } from "../../wailsjs/go/models";
import { activeWorkspace, state } from "./state";
import { pushToast } from "./toasts";
import { escapeAttribute, escapeHtml, errorMessage } from "./utils";

// Map of workspaceID → TokenBudget
export const tokenBudgets = new Map<string, services.TokenBudget>();

let budgetExceededToasts = new Set<string>();

export function getTokenBudget(workspaceID: string): services.TokenBudget | null {
  return tokenBudgets.get(workspaceID) ?? null;
}

export async function loadTokenBudget(workspaceID: string): Promise<void> {
  try {
    const budget = await GetTokenBudget(workspaceID);
    if (budget && (budget.limit > 0 || budget.used > 0)) {
      tokenBudgets.set(workspaceID, budget);
    } else {
      tokenBudgets.delete(workspaceID);
    }
    checkBudgetExceeded(workspaceID);
  } catch {
    // Budget not configured or workspace missing
    tokenBudgets.delete(workspaceID);
  }
}

export async function setTokenBudget(workspaceID: string, limit: number): Promise<void> {
  try {
    await SetTokenBudget(workspaceID, limit);
    await loadTokenBudget(workspaceID);
    pushToast(`Token budget set to ${formatTokenCount(limit)}.`, "success");
  } catch (error) {
    pushToast(errorMessage(error), "error");
  }
}

export async function resetTokenBudget(workspaceID: string): Promise<void> {
  try {
    await ResetTokenBudget(workspaceID);
    await loadTokenBudget(workspaceID);
    budgetExceededToasts.delete(workspaceID);
    pushToast("Token budget reset.", "success");
  } catch (error) {
    pushToast(errorMessage(error), "error");
  }
}

export function checkBudgetExceeded(workspaceID: string): void {
  const budget = tokenBudgets.get(workspaceID);
  if (!budget || budget.limit === 0) return;
  
  const exceeded = budget.used >= budget.limit;
  const wasToasted = budgetExceededToasts.has(workspaceID);
  
  if (exceeded && !wasToasted) {
    budgetExceededToasts.add(workspaceID);
    pushBudgetExceededToast(workspaceID, budget);
  } else if (!exceeded && wasToasted) {
    budgetExceededToasts.delete(workspaceID);
  }
}

function pushBudgetExceededToast(workspaceID: string, budget: services.TokenBudget): void {
  const usedPct = Math.min((budget.used / budget.limit) * 100, 100).toFixed(0);
  pushToast(
    `Token budget exceeded (${usedPct}% used).`,
    "error"
  );
}

export function formatTokenCount(count: number): string {
  if (count >= 1_000_000) {
    return `${(count / 1_000_000).toFixed(1)}M`;
  }
  if (count >= 1_000) {
    return `${(count / 1_000).toFixed(1)}K`;
  }
  return String(count);
}

export function getBudgetProgress(budget: services.TokenBudget): number {
  if (budget.limit === 0) return 0;
  return Math.min((budget.used / budget.limit) * 100, 100);
}

export function getBudgetColorClass(progress: number): string {
  if (progress >= 90) return "budget-critical";
  if (progress >= 70) return "budget-warning";
  return "budget-ok";
}

// Budget progress bar rendered in workspace panels
export function renderBudgetBar(workspaceID: string): string {
  const budget = tokenBudgets.get(workspaceID);
  if (!budget || budget.limit === 0) return "";
  
  const progress = getBudgetProgress(budget);
  const colorClass = getBudgetColorClass(progress);
  const usedFormatted = formatTokenCount(budget.used);
  const limitFormatted = formatTokenCount(budget.limit);
  const pctLabel = `${Math.round(progress)}%`;
  
  return `
    <div class="budget-bar-container" data-budget-bar>
      <div class="budget-bar-info">
        <span class="budget-bar-label">Tokens</span>
        <span class="budget-bar-values">${escapeHtml(usedFormatted)} / ${escapeHtml(limitFormatted)}</span>
        <span class="budget-bar-percentage ${colorClass}">${escapeHtml(pctLabel)}</span>
      </div>
      <div class="budget-bar-track">
        <div class="budget-bar-fill ${colorClass}" style="width: ${progress}%"></div>
      </div>
      <button class="budget-bar-reset" type="button" title="Reset token budget" data-action="reset-budget" data-workspace-id="${escapeAttribute(workspaceID)}">Reset</button>
    </div>
  `;
}

// Budget settings section for settings panel
export function renderBudgetSettingsSection(): string {
  const ws = activeWorkspace();
  if (!ws) return "";
  
  const budget = tokenBudgets.get(ws.id);
  const currentLimit = budget?.limit ?? 0;
  
  return `
    <section class="settings-section" aria-labelledby="budget-settings-title">
      <h3 id="budget-settings-title" class="settings-section-title">Token Budget</h3>
      <div class="settings-grid">
        <label class="field">
          <span>Daily token limit</span>
          <input 
            name="tokenBudgetLimit" 
            type="number" 
            min="0" 
            step="1000" 
            value="${escapeHtml(String(currentLimit))}"
            data-budget-limit-input
            placeholder="0 = unlimited"
          />
        </label>
      </div>
      <p class="compact muted">Set a daily token usage limit for this workspace. Set to 0 for unlimited.</p>
      ${budget && budget.limit > 0 ? `
        <div class="budget-settings-status">
          <span>Used: ${escapeHtml(formatTokenCount(budget.used))} / ${escapeHtml(formatTokenCount(budget.limit))}</span>
          <button class="secondary-button compact-button" type="button" data-action="reset-budget" data-workspace-id="${escapeAttribute(ws.id)}">Reset Usage</button>
        </div>
      ` : ""}
    </section>
  `;
}

export async function handleBudgetLimitInput(input: HTMLInputElement): Promise<void> {
  const ws = activeWorkspace();
  if (!ws) return;
  
  const limit = Number(input.value);
  if (isNaN(limit) || limit < 0) {
    return;
  }
  
  await setTokenBudget(ws.id, limit);
}
