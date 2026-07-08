/**
 * Dashboard — unified widget grid.
 */

import { renderDashboardWidgets } from "./grid";

/**
 * Render the full dashboard view as an HTML string.
 * Always renders the unified widget grid.
 */
export function renderDashboard(): string {
	return renderDashboardWidgets("dashboard");
}
