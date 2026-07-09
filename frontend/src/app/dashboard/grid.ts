/**
 * Dashboard widget grid renderer — unified widget grid with customize mode.
 */

import { activeWorkspace, state } from "../state";
import { escapeHtml } from "../utils";
import type { AppMode, DashboardWidget, WidgetId, WidgetSize } from "../types";
import { widgetRegistry, availableWidgetsForView } from "./widgets";

/* ------------------------------------------------------------------ */
/*  Backwards-compatible availableWidgets map (used by actions.ts)     */
/* ------------------------------------------------------------------ */

export const availableWidgets: Record<AppMode, { id: WidgetId; title: string; size: WidgetSize }[]> = (() => {
	const result: Record<string, { id: WidgetId; title: string; size: WidgetSize }[]> = {} as any;
	// All views share the same unified widget set
	const ids = availableWidgetsForView();
	for (const view of ["chat", "tasks", "kanban", "code", "git", "dashboard", "settings"] as AppMode[]) {
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
/*  Grid sizing helpers                                                */
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
/*  Widget card renderer                                               */
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
/*  Add widget panel                                                   */
/* ------------------------------------------------------------------ */

function renderAddWidgetPanel(currentWidgets: DashboardWidget[]): string {
	const allAvailable = availableWidgetsForView();
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
/*  Main entry point                                                   */
/* ------------------------------------------------------------------ */

/**
 * Render the unified dashboard widget grid.
 * Shows empty-state message when no workspace is selected.
 */
export function renderDashboardWidgets(_view: AppMode): string {
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

	const widgets = getDashboardWidgets("dashboard");
	const isEditMode = state.dashboardEditMode;

	const widgetCards = widgets.map((w: DashboardWidget, i: number) => renderWidgetCard(w, i, widgets.length)).join("");

	let editControls = "";
	if (isEditMode) {
		editControls = renderAddWidgetPanel(widgets);
	} else if (widgets.length === 0) {
		editControls = `
    <div class="dashboard-empty-state">
      <p>No widgets configured for the dashboard.</p>
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
