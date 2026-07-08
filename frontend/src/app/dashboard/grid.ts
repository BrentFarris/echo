/**
 * Dashboard widget grid renderer — Dev Studio command center layout.
 * Renders a 3-section layout: status bar, backlog+kanban columns, chat section.
 */

import { services } from "../../../wailsjs/go/models";
import { activeWorkspace, chatSessionFor, kanbanBoardFor, state, taskBoardFor } from "../state";
import { tokenBudgets, formatTokenCount, getBudgetProgress, getBudgetColorClass } from "../budget";
import { escapeHtml, escapeAttribute } from "../utils";
import type { AppMode, DashboardWidget, WidgetId, WidgetSize } from "../types";
import { getAppCallbacks } from "../callbacks";
import { widgetRegistry, availableWidgetsForView } from "./widgets";

/* ------------------------------------------------------------------ */
/*  Backwards-compatible availableWidgets map (used by actions.ts)     */
/* ------------------------------------------------------------------ */

export const availableWidgets: Record<AppMode, { id: WidgetId; title: string; size: WidgetSize }[]> = (() => {
	const result: Record<string, { id: WidgetId; title: string; size: WidgetSize }[]> = {} as any;
	for (const view of ["chat", "tasks", "kanban", "code", "git", "dashboard", "settings"] as AppMode[]) {
		const ids = availableWidgetsForView(view);
		result[view] = ids.map((id) => {
			const entry = widgetRegistry[id];
			return { id, title: entry?.title ?? id, size: entry?.defaultSize ?? "small" };
		});
	}
	return result;
})();

/* ------------------------------------------------------------------ */
/*  SVG icons (inline)                                                 */
/* ------------------------------------------------------------------ */

const svgPlus = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 5v14M5 12h14"/></svg>`;
const svgMinus = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M5 12h14"/></svg>`;
const svgArrowUp = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m18 15-6-6-6 6"/></svg>`;
const svgArrowDown = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m6 9 6 6 6-6"/></svg>`;
const svgEdit = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M12 20h9M16.5 3.5a2.121 2.121 0 0 1 3 3L7 19l-4 1 1-4Z"/></svg>`;
const svgCheck = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg>`;

/* ------------------------------------------------------------------ */
/*  Grid sizing helpers (kept for widget system compatibility)         */
/* ------------------------------------------------------------------ */

function gridColumnSpan(size: WidgetSize): string {
	switch (size) {
		case "small": return "span 1";
		case "medium": return "span 2";
		case "large": return "span 2";
		case "wide": return "span 4";
		default: return "span 1";
	}
}

function gridRowSpan(size: WidgetSize): string {
	switch (size) {
		case "small": return "span 1";
		case "medium": return "span 1";
		case "large": return "span 2";
		case "wide": return "span 1";
		default: return "span 1";
	}
}

/* ------------------------------------------------------------------ */
/*  Widget content renderer (delegates to registry)                    */
/* ------------------------------------------------------------------ */

function renderWidgetContent(widgetId: WidgetId, workspace: any): string {
	const entry = widgetRegistry[widgetId];
	if (!entry) {
		return `<p class="dashboard-widget-placeholder">Unknown widget.</p>`;
	}
	return entry.renderer(workspace);
}

/* ------------------------------------------------------------------ */
/*  Widget card renderer (kept for sub-view compatibility)             */
/* ------------------------------------------------------------------ */

function renderWidgetCard(widget: DashboardWidget, index: number, total: number): string {
	const colSpan = gridColumnSpan(widget.size);
	const rowSpan = gridRowSpan(widget.size);
	const isEditMode = state.dashboardEditMode;
	const workspace = activeWorkspace();

	let headerControls = "";
	if (isEditMode) {
		headerControls = `
          <div class="widget-card-edit-controls">
            ${index > 0 ? `<button type="button" title="Move up" data-action="widget-move-up" data-widget-id="${widget.id}" data-order="${widget.order}">${svgArrowUp}</button>` : ""}
            ${index < total - 1 ? `<button type="button" title="Move down" data-action="widget-move-down" data-widget-id="${widget.id}" data-order="${widget.order}">${svgArrowDown}</button>` : ""}
            <button type="button" title="Remove widget" data-action="widget-remove" data-widget-id="${widget.id}">${svgMinus}</button>
          </div>`;
	}

	return `
    <article class="widget-card widget-size-${widget.size}" style="grid-column: ${colSpan}; grid-row: ${rowSpan};" data-widget-id="${widget.id}">
      <header class="widget-card-header">
        <h3 class="widget-card-title">${escapeHtml(widget.title)}</h3>
        ${headerControls}
      </header>
      <div class="widget-card-body">
        ${renderWidgetContent(widget.id as WidgetId, workspace)}
      </div>
    </article>`;
}

/* ------------------------------------------------------------------ */
/*  Add widget panel (kept for sub-view compatibility)                 */
/* ------------------------------------------------------------------ */

function renderAddWidgetPanel(view: AppMode, currentWidgets: DashboardWidget[]): string {
	const allAvailable = availableWidgetsForView(view);
	const existingIds = new Set(currentWidgets.map((w) => w.id));
	const remaining = allAvailable.filter((id) => !existingIds.has(id));

	if (remaining.length === 0) {
		return "";
	}

	const items = remaining.map((widgetId) => {
		const entry = widgetRegistry[widgetId];
		if (!entry) return "";
		return `
      <button type="button" class="widget-picker-item" data-action="widget-add" data-widget-id="${widgetId}" data-widget-size="${entry.defaultSize}">
        ${svgPlus} ${escapeHtml(entry.title)}
        <span class="widget-picker-size-badge">${escapeHtml(entry.defaultSize)}</span>
      </button>`;
	}).join("");

	return `
    <div class="widget-add-panel">
      <h3 class="widget-add-panel-title">Add Widget</h3>
      <div class="widget-picker-list">${items}</div>
    </div>`;
}

/* ------------------------------------------------------------------ */
/*  Helpers                                                            */
/* ------------------------------------------------------------------ */

/** Truncate text to a max length, adding ellipsis if needed. */
function truncateText(text: string, maxLen = 140): string {
	if (!text) return "";
	if (text.length <= maxLen) return text;
	return text.slice(0, maxLen) + "\u2026";
}

/** Format milliseconds as human-readable interval. */
function formatIntervalMs(ms: number): string {
	if (!ms || ms <= 0) return "off";
	const mins = Math.round(ms / 60000);
	if (mins < 1) return "<1m";
	if (mins < 60) return `${mins}m`;
	const hrs = Math.floor(mins / 60);
	const remainMins = mins % 60;
	return remainMins > 0 ? `${hrs}h ${remainMins}m` : `${hrs}h`;
}

/** Priority badge HTML. */
function priorityBadgeHTML(priority: string): string {
	const p = (priority ?? "P2").toUpperCase();
	const cls = p === "P0" ? "priority-badge priority-p0" : p === "P1" ? "priority-badge priority-p1" : "priority-badge priority-p2";
	return `<span class="${cls}">${escapeHtml(p)}</span>`;
}

/** Status dot HTML for active/inactive. */
function statusDotHTML(active: boolean): string {
	const cls = active ? "status-dot status-active" : "status-dot status-inactive";
	return `<span class="${cls}"></span>`;
}

/* ------------------------------------------------------------------ */
/*  Status Bar                                                         */
/* ------------------------------------------------------------------ */

function renderStatusBar(workspace: services.Workspace): string {
	const displayName = workspace.displayName || workspace.id;
	const folders = workspace.folders ?? [];
	const pathDisplay = folders.map((f: any) => f.label ?? f.path).join(", ");

	const heartbeatMs = state.heartbeatIntervals.get(workspace.id);
	const watchdogMs = state.watchdogIntervals.get(workspace.id);
	const hbEnabled = heartbeatMs != null && heartbeatMs > 0;
	const wdEnabled = watchdogMs != null && watchdogMs > 0;

	// Token budget
	const budget = tokenBudgets.get(workspace.id);
	let budgetHTML = "";
	if (budget && budget.limit > 0) {
		const progress = getBudgetProgress(budget);
		const colorClass = getBudgetColorClass(progress);
		const usedFormatted = formatTokenCount(budget.used);
		const limitFormatted = formatTokenCount(budget.limit);
		budgetHTML = `
        <span class="dashboard-status-indicator dashboard-budget-indicator ${colorClass}">
          Budget: ${escapeHtml(usedFormatted)} / ${escapeHtml(limitFormatted)} tokens
        </span>`;
	}

	return `
    <div class="dashboard-status-bar">
      <div class="dashboard-workspace-name">
        ${statusDotHTML(true)}
        <strong>${escapeHtml(displayName)}</strong>
        ${pathDisplay ? `<span class="dashboard-workspace-path">${escapeHtml(truncateText(pathDisplay, 60))}</span>` : ""}
      </div>
      <div class="dashboard-status-indicators">
        <span class="dashboard-status-indicator">
          ${statusDotHTML(hbEnabled)}
          Heartbeat: ${hbEnabled ? escapeHtml(formatIntervalMs(heartbeatMs)) : "off"}
        </span>
        <span class="dashboard-status-indicator">
          ${statusDotHTML(wdEnabled)}
          Watchdog: ${wdEnabled ? escapeHtml(formatIntervalMs(watchdogMs)) : "off"}
        </span>
        ${budgetHTML}
      </div>
    </div>`;
}

/* ------------------------------------------------------------------ */
/*  Backlog Panel                                                      */
/* ------------------------------------------------------------------ */

function renderBacklogPanel(workspace: services.Workspace): string {
	const board = taskBoardFor(workspace.id);
	const tasks = board.tasks ?? [];

	// Filter open tasks, group by priority
	const priorities = ["P0", "P1", "P2"] as const;
	let rowsHTML = "";
	let totalShown = 0;
	const maxTasks = 10;

	for (const p of priorities) {
		const group = tasks.filter((t: any) => !t.completed && (t.priority ?? "P2").toUpperCase() === p);
		if (group.length === 0) continue;

		rowsHTML += `<div class="backlog-priority-group"><span class="backlog-priority-label">${escapeHtml(p)}</span>`;
		for (const task of group) {
			if (totalShown >= maxTasks) break;
			totalShown++;

			// Determine status badge — check if task was converted to a kanban card
			const statusBadge = renderTaskStatusBadge(task);
			rowsHTML += `
            <div class="backlog-task-row">
              ${priorityBadgeHTML(task.priority)}
              <span class="backlog-task-title">${escapeHtml(truncateText(task.title ?? "", 80))}</span>
              ${statusBadge}
            </div>`;
		}
		rowsHTML += `</div>`;
	}

	if (totalShown === 0) {
		return `
      <div class="dashboard-backlog-panel">
        <h3 class="dashboard-panel-title">Backlog</h3>
        <p class="dashboard-empty-message">No open tasks.</p>
      </div>`;
	}

	const moreNote = tasks.filter((t: any) => !t.completed).length > maxTasks
		? `<p class="backlog-more-note">${tasks.filter((t: any) => !t.completed).length - maxTasks} more tasks hidden</p>`
		: "";

	return `
      <div class="dashboard-backlog-panel">
        <h3 class="dashboard-panel-title">Backlog</h3>
        <div class="backlog-task-list">
          ${rowsHTML}
          ${moreNote}
        </div>
      </div>`;
}

/** Render status badge for a task based on whether it's been converted to a kanban card. */
function renderTaskStatusBadge(task: any): string {
	// Check if this task has an associated kanban card by looking at the ID pattern
	// Tasks converted to cards typically have "→ card" or similar markers
	const taskId = task.id ?? "";

	// Simple heuristic: check if a kanban card references this task ID
	// For now, show "backlog" as default status
	return `<span class="backlog-status-badge backlog-status-backlog">backlog</span>`;
}

/* ------------------------------------------------------------------ */
/*  Kanban Panel                                                       */
/* ------------------------------------------------------------------ */

function renderKanbanPanel(workspace: services.Workspace): string {
	const board = kanbanBoardFor(workspace.id);

	const lanes = [
		{ key: "done", label: "Done", cards: board.done ?? [] },
		{ key: "inProgress", label: "In Progress", cards: board.inProgress ?? [] },
		{ key: "ready", label: "Ready", cards: board.ready ?? [] },
		{ key: "blocked", label: "Blocked", cards: board.blocked ?? [] },
	];

	const laneHTMLs = lanes.map((lane) => {
		const cardHTMLs = lane.cards.length > 0
			? lane.cards.map((card: services.KanbanCard) => renderDashboardKanbanCard(card)).join("")
			: `<p class="lane-empty">No cards</p>`;

		return `
        <div class="dashboard-lane">
          <div class="dashboard-lane-header">
            <strong>${escapeHtml(lane.label)}</strong>
            <span class="dashboard-lane-count">${lane.cards.length}</span>
          </div>
          <div class="dashboard-lane-cards">
            ${cardHTMLs}
          </div>
        </div>`;
	}).join("");

	return `
      <div class="dashboard-kanban-panel">
        <h3 class="dashboard-panel-title">Kanban</h3>
        <div class="dashboard-lanes-grid">
          ${laneHTMLs}
        </div>
      </div>`;
}

/** Render a compact kanban card for the dashboard. */
function renderDashboardKanbanCard(card: services.KanbanCard): string {
	const unavailable = card.lane === "ready" && !card.eligible;
	const progressPct = kanbanCardProgressPercent(card);
	const statusMessage = renderDashboardCardStatus(card);
	const progressBar = card.lane === "inProgress" ? renderDashboardProgressBar(progressPct) : "";

	return `
    <button
      class="kanban-card ${unavailable ? "is-unavailable" : ""}"
      type="button"
      data-action="open-card"
      data-card-id="${escapeAttribute(card.id)}"
      aria-label="Open ${escapeAttribute(card.title)} details"
    >
      <div class="kanban-card-title-row">
        <span class="kanban-card-status-dot ${laneDotClass(card.lane)}" aria-hidden="true"></span>
        <strong>${escapeHtml(card.title)}</strong>
      </div>
      ${statusMessage}
      ${progressBar}
    </button>`;
}

/** Determine the lane dot class for a card. */
function laneDotClass(lane: string): string {
	switch (lane) {
		case "done": return "status-done";
		case "inProgress": return "status-inprogress";
		case "blocked": return "status-blocked";
		default: return "status-ready";
	}
}

/** Card progress percentage. */
function kanbanCardProgressPercent(card: services.KanbanCard): number {
	const lane = card.lane ?? "";
	if (lane === "done") return 100;
	if (lane === "ready" || lane === "blocked") return 0;
	const transcript = card.progressTranscript ?? [];
	const toolCallCount = transcript.filter((e: any) => e.type === "tool_call").length;
	const criteriaLen = (card.acceptanceCriteria ?? []).length;
	if (criteriaLen > 0) {
		return Math.min(Math.round((toolCallCount / criteriaLen) * 95), 97);
	}
	return Math.min(Math.round((toolCallCount / 10) * 100), 80);
}

/** Compact status message for dashboard card. */
function renderDashboardCardStatus(card: services.KanbanCard): string {
	if (card.lane === "done") {
		return `<p class="kanban-card-status-text status-done">✓ done</p>`;
	}
	if (card.lane === "inProgress") {
		const transcript = card.progressTranscript ?? [];
		if (transcript.length > 0) {
			const last = transcript[transcript.length - 1];
			const text = last.content ? truncateText(last.content, 60) : "Working...";
			return `<p class="kanban-card-status-text status-inprogress">${escapeHtml(text)}</p>`;
		}
		return `<p class="kanban-card-status-text status-inprogress">In progress...</p>`;
	}
	if (card.lane === "blocked") {
		const reason = card.blockedBy?.length ? "blocked by dependencies" : "blocked";
		return `<p class="kanban-card-status-text status-blocked">${escapeHtml(reason)}</p>`;
	}
	return `<p class="kanban-card-status-text status-ready">ready</p>`;
}

/** Compact progress bar for in-progress cards. */
function renderDashboardProgressBar(pct: number): string {
	if (pct <= 0) return "";
	return `
    <div class="kanban-card-progress">
      <div class="kanban-card-progress-track">
        <div class="kanban-card-progress-fill" style="width: ${pct}%"></div>
      </div>
    </div>`;
}

/* ------------------------------------------------------------------ */
/*  Chat Section                                                       */
/* ------------------------------------------------------------------ */

function renderChatSection(workspace: services.Workspace): string {
	const session = chatSessionFor(workspace.id);
	const msgs = (session.messages ?? []).slice(-6); // last 6 messages

	if (!msgs.length) {
		return `
      <div class="dashboard-chat-section">
        <h3 class="dashboard-panel-title">Chat</h3>
        <p class="dashboard-empty-message">No messages yet.</p>
      </div>`;
	}

	const msgHTMLs = msgs.map((msg: any) => {
		const isUser = msg.role === "user";
		const roleLabel = isUser ? "You" : "Echo";
		const roleClass = isUser ? "from-user" : "from-assistant";
		const content = escapeHtml(truncateText(msg.content ?? "", 140));

		return `
        <div class="dashboard-chat-message ${roleClass}">
          <span class="dashboard-chat-role">${escapeHtml(roleLabel)}</span>
          <span class="dashboard-chat-content">${content}</span>
        </div>`;
	}).join("");

	return `
      <div class="dashboard-chat-section">
        <h3 class="dashboard-panel-title">Chat</h3>
        <div class="dashboard-chat-messages">
          ${msgHTMLs}
        </div>
      </div>`;
}

/* ------------------------------------------------------------------ */
/*  Main entry point                                                   */
/* ------------------------------------------------------------------ */

/**
 * Render the dashboard widget grid for a given view mode.
 * For "dashboard" view, renders the Dev Studio command center layout.
 * For other views, falls back to the legacy widget grid.
 * Shows empty-state message when no workspace is selected.
 */
export function renderDashboardWidgets(view: AppMode): string {
	const workspace = activeWorkspace();

	// No workspace selected — show helpful empty state
	if (!workspace) {
		return `
  <div class="dashboard-view">
    <header class="dashboard-toolbar">
      <h2 id="dashboard-title">Dashboard</h2>
    </header>
    <div class="dashboard-empty-state dashboard-no-workspace">
      <p>No workspace selected. Select a workspace to view your dashboard widgets.</p>
    </div>
  </div>`;
	}

	// For the main dashboard view, render the Dev Studio command center layout
	if (view === "dashboard") {
		return `
    <div class="dashboard-view">
      ${renderStatusBar(workspace)}
      <div class="dashboard-main-grid">
        ${renderBacklogPanel(workspace)}
        ${renderKanbanPanel(workspace)}
      </div>
      ${renderChatSection(workspace)}
    </div>`;
	}

	// For other view modes (chat, tasks, kanban sub-views), keep the legacy widget grid
	const widgets = getDashboardWidgets(view);
	const isEditMode = state.dashboardEditMode;

	const widgetCards = widgets.map((w: DashboardWidget, i: number) => renderWidgetCard(w, i, widgets.length)).join("");

	let editControls = "";
	if (isEditMode) {
		editControls = renderAddWidgetPanel(view, widgets);
	} else if (widgets.length === 0) {
		const viewLabel = view.charAt(0).toUpperCase() + view.slice(1);
		editControls = `
    <div class="dashboard-empty-state">
      <p>No widgets configured for the ${escapeHtml(viewLabel)} dashboard.</p>
      <button type="button" data-action="dashboard-edit-toggle" class="secondary-button">Customize Dashboard</button>
    </div>`;
	}

	const editToggleBtn = isEditMode
		? `<button type="button" data-action="dashboard-edit-toggle" class="secondary-button dashboard-done-btn">${svgCheck} Done</button>`
		: `<button type="button" data-action="dashboard-edit-toggle" class="secondary-button dashboard-edit-btn">${svgEdit} Customize</button>`;

	return `
  <div class="dashboard-view">
    <header class="dashboard-toolbar">
      <h2 id="dashboard-title">Dashboard</h2>
      ${editToggleBtn}
    </header>
    <div class="dashboard-widget-grid">
      ${widgetCards}
    </div>
    ${editControls}
  </div>`;
}

import { getDashboardWidgets } from "../state";
