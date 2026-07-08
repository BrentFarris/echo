/**
 * Dashboard — Dev Studio command center layout.
 * Delegates to the widget grid renderer for all view modes.
 */

import { renderDashboardWidgets } from "./grid";
import { state } from "../state";
import type { AppMode } from "../types";

/**
 * Render the full dashboard view as an HTML string.
 * Delegates to the widget grid engine for all view modes.
 * Shows empty-state message when no workspace is selected.
 */
export function renderDashboard(view?: AppMode): string {
	const v = view ?? state.dashboardViewMode ?? "dashboard";
	return renderDashboardWidgets(v);
}
