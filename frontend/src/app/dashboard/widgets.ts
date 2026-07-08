/**
 * Widget registry — map of WidgetId to renderer functions.
 * Each renderer reads from live `state.*` and returns an HTML string.
 * No imports of Wails bindings or backend services — state-only access.
 */

import { services } from "../../../wailsjs/go/models";
import { activeWorkspace, chatAgentModeNameFor, chatSessionFor, getActiveChatModelLabel, kanbanBoardFor, state, taskBoardFor } from "../state";
import { tokenBudgets, formatTokenCount, getBudgetProgress, getBudgetColorClass } from "../budget";
import { escapeHtml } from "../utils";
import type { AppMode, WidgetId, WidgetSize } from "../types";
import { ensureCodeState } from "../../codeView/state";

/* ------------------------------------------------------------------ */
/*  Type definitions                                                   */
/* ------------------------------------------------------------------ */

export type WidgetRenderer = (workspace: services.Workspace | null) => string;

export interface WidgetEntry {
	renderer: WidgetRenderer;
	defaultSize: WidgetSize;
	title: string;
}

/* ------------------------------------------------------------------ */
/*  SVG icons (inline, no imports)                                     */
/* ------------------------------------------------------------------ */

const iconCheck = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="M20 6 9 17l-5-5"/></svg>`;
const iconSpinner = `<svg viewBox="0 0 24 24" aria-hidden="true"><circle cx="12" cy="12" r="10" fill="none" stroke="currentColor" stroke-width="3" stroke-dasharray="30 70"/></svg>`;
const iconArrowUp = `<svg viewBox="0 0 24 24" aria-hidden="true"><path d="m18 15-6-6-6 6"/></svg>`;
const iconDot = `<svg viewBox="0 0 12 12" aria-hidden="true"><circle cx="6" cy="6" r="5"/></svg>`;

/* ------------------------------------------------------------------ */
/*  Helpers                                                            */
/* ------------------------------------------------------------------ */

/** Truncate text to a max length, adding ellipsis if needed. */
function truncate(text: string, maxLen = 120): string {
	if (text.length <= maxLen) return text;
	return text.slice(0, maxLen) + "\u2026";
}

/** Format a date string as relative or short date. */
function formatDate(isoString: string): string {
	if (!isoString) return "";
	try {
		const d = new Date(isoString);
		const now = new Date();
		const diffMs = now.getTime() - d.getTime();
		const diffMin = Math.floor(diffMs / 60000);
		if (diffMin < 1) return "just now";
		if (diffMin < 60) return `${diffMin}m ago`;
		const diffHrs = Math.floor(diffMin / 60);
		if (diffHrs < 24) return `${diffHrs}h ago`;
		const diffDays = Math.floor(diffHrs / 24);
		if (diffDays < 7) return `${diffDays}d ago`;
		return d.toLocaleDateString();
	} catch {
		return isoString;
	}
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

/** Kanban card progress percentage (mirrors kanban/index.ts logic). */
function cardProgressPercent(card: services.KanbanCard): number {
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

/** Priority badge HTML. */
function priorityBadgeHTML(priority: string): string {
	const p = priority.toUpperCase();
	const cls = p === "P0" ? "priority-badge priority-p0" : p === "P1" ? "priority-badge priority-p1" : "priority-badge priority-p2";
	return `<span class="${cls}">${escapeHtml(p)}</span>`;
}

/** Status dot HTML. */
function statusDotHTML(active: boolean): string {
	const cls = active ? "status-dot status-active" : "status-dot status-inactive";
	return `<span class="${cls}"></span>`;
}

/* ------------------------------------------------------------------ */
/*  Widget renderers                                                   */
/* ------------------------------------------------------------------ */

// chat-recent — Last 3 messages from active chat session
function renderChatRecent(_ws: services.Workspace | null): string {
	const ws = _ws ?? activeWorkspace();
	if (!ws) return `<p class="widget-placeholder">No workspace selected.</p>`;
	const session = chatSessionFor(ws.id);
	const msgs = (session.messages ?? []).slice(-3);
	if (!msgs.length) {
		return `<p class="widget-placeholder">No messages yet. Start a conversation.</p>`;
	}
	const rows = msgs.map((msg: any) => {
		const roleLabel = msg.role === "user" ? "You" : "Echo";
		const roleClass = msg.role === "user" ? "from-user" : "from-assistant";
		const content = escapeHtml(truncate(msg.content ?? "", 140));
		return `
      <div class="widget-chat-msg ${roleClass}">
        <span class="widget-chat-role">${escapeHtml(roleLabel)}</span>
        <span class="widget-chat-content">${content}</span>
      </div>`;
	}).join("");
	return `<div class="widget-chat-recent">${rows}</div>`;
}

// chat-busy-status — Busy/idle indicator + current model label
function renderChatBusyStatus(ws: services.Workspace | null): string {
	const workspace = ws ?? activeWorkspace();
	if (!workspace) return `<p class="widget-placeholder">No workspace selected.</p>`;
	const session = chatSessionFor(workspace.id);
	const busy = session.busy ?? false;
	const modelLabel = getActiveChatModelLabel();
	const agentModeName = chatAgentModeNameFor(workspace.id);

	const indicatorClass = busy ? "widget-status-busy" : "widget-status-idle";
	const icon = busy ? iconSpinner : iconCheck;
	const statusText = busy ? "Processing..." : "Idle";

	let html = `
    <div class="widget-busy-indicator ${indicatorClass}">
      <span class="widget-status-icon">${icon}</span>
      <span class="widget-status-text">${escapeHtml(statusText)}</span>
    </div>`;

	if (agentModeName) {
		html += `<p class="widget-subtitle">Mode: ${escapeHtml(agentModeName)}</p>`;
	}
	if (modelLabel) {
		html += `<p class="widget-subtitle">Model: ${escapeHtml(modelLabel)}</p>`;
	}
	return html;
}

// chat-token-budget — Token budget bar (reuse existing budget logic)
function renderChatTokenBudget(ws: services.Workspace | null): string {
	const workspace = ws ?? activeWorkspace();
	if (!workspace) return `<p class="widget-placeholder">No workspace selected.</p>`;
	const budget = tokenBudgets.get(workspace.id);
	if (!budget || budget.limit === 0) {
		return `<p class="widget-placeholder">Token budget not set.</p>`;
	}
	const progress = getBudgetProgress(budget);
	const colorClass = getBudgetColorClass(progress);
	const usedFormatted = formatTokenCount(budget.used);
	const limitFormatted = formatTokenCount(budget.limit);
	const pctLabel = `${Math.round(progress)}%`;

	return `
    <div class="widget-budget-bar">
      <div class="widget-budget-info">
        <span class="widget-budget-label">Tokens</span>
        <span class="widget-budget-values">${escapeHtml(usedFormatted)} / ${escapeHtml(limitFormatted)}</span>
        <span class="widget-budget-percentage ${colorClass}">${escapeHtml(pctLabel)}</span>
      </div>
      <div class="widget-budget-track">
        <div class="widget-budget-fill ${colorClass}" style="width: ${progress}%"></div>
      </div>
    </div>`;
}

// kanban-summary — Lane counts as compact badges
function renderKanbanSummary(ws: services.Workspace | null): string {
	const workspace = ws ?? activeWorkspace();
	if (!workspace) return `<p class="widget-placeholder">No workspace selected.</p>`;
	const board = kanbanBoardFor(workspace.id);
	const lanes = [
		{ key: "ready", label: "Ready", count: board.ready?.length ?? 0 },
		{ key: "inProgress", label: "In Progress", count: board.inProgress?.length ?? 0 },
		{ key: "blocked", label: "Blocked", count: board.blocked?.length ?? 0 },
		{ key: "done", label: "Done", count: board.done?.length ?? 0 },
	];
	const badges = lanes.map((lane) => {
		const colorVar = lane.key === "done" ? "var(--color-success)" : lane.key === "inProgress" ? "var(--color-accent)" : lane.key === "blocked" ? "var(--color-danger)" : "var(--color-warning)";
		return `<span class="widget-lane-badge" style="--badge-color: ${colorVar}">${escapeHtml(lane.label)} <strong>${lane.count}</strong></span>`;
	}).join("");
	const total = lanes.reduce((sum, l) => sum + l.count, 0);
	if (total === 0) {
		return `<p class="widget-placeholder">No Kanban cards.</p>`;
	}
	return `<div class="widget-kanban-summary">${badges}</div>`;
}

// kanban-progress — Active card progress bars for inProgress lane
function renderKanbanProgress(ws: services.Workspace | null): string {
	const workspace = ws ?? activeWorkspace();
	if (!workspace) return `<p class="widget-placeholder">No workspace selected.</p>`;
	const board = kanbanBoardFor(workspace.id);
	const cards = board.inProgress ?? [];
	if (!cards.length) {
		return `<p class="widget-placeholder">No cards in progress.</p>`;
	}
	const rows = cards.map((card: any) => {
		const pct = cardProgressPercent(card);
		const colorClass = pct >= 90 ? "budget-critical" : pct >= 70 ? "budget-warning" : "budget-ok";
		return `
      <div class="widget-progress-item">
        <span class="widget-progress-title">${escapeHtml(truncate(card.title ?? "", 60))}</span>
        <div class="widget-progress-track">
          <div class="widget-progress-fill ${colorClass}" style="width: ${pct}%"></div>
        </div>
        <span class="widget-progress-pct">${pct}%</span>
      </div>`;
	}).join("");
	return `<div class="widget-kanban-progress">${rows}</div>`;
}

// kanban-done-count — Done cards count with trend indicator
function renderKanbanDoneCount(ws: services.Workspace | null): string {
	const workspace = ws ?? activeWorkspace();
	if (!workspace) return `<p class="widget-placeholder">No workspace selected.</p>`;
	const board = kanbanBoardFor(workspace.id);
	const doneCards = board.done ?? [];
	const count = doneCards.length;

	let trendHTML = "";
	if (count > 0) {
		// Check if any cards were recently completed (within last hour based on transcript)
		const recentDone = doneCards.filter((card: any) => {
			const entries = card.progressTranscript ?? [];
			for (let i = entries.length - 1; i >= 0; i--) {
				if (entries[i].type === "verification") {
					try {
						const ts = entries[i].timestamp ? new Date(entries[i].timestamp).getTime() : 0;
						return Date.now() - ts < 3600000;
					} catch { /* ignore */ }
				}
			}
			return false;
		}).length;
		if (recentDone > 0) {
			trendHTML = `<span class="widget-trend widget-trend-up">${iconArrowUp} ${recentDone} recent</span>`;
		}
	}

	return `
    <div class="widget-done-count">
      <span class="widget-done-number">${count}</span>
      <span class="widget-done-label">card${count !== 1 ? "s" : ""} done</span>
      ${trendHTML}
    </div>`;
}

// tasks-overview — Open/completed counts + top P0 items
function renderTasksOverview(ws: services.Workspace | null): string {
	const workspace = ws ?? activeWorkspace();
	if (!workspace) return `<p class="widget-placeholder">No workspace selected.</p>`;
	const board = taskBoardFor(workspace.id);
	const tasks = board.tasks ?? [];
	const openTasks = tasks.filter((t: any) => !t.completed);
	const completedTasks = tasks.filter((t: any) => t.completed);
	const p0Open = openTasks.filter((t: any) => t.priority === "P0");

	let html = `
    <div class="widget-tasks-summary">
      <span class="widget-task-stat">${openTasks.length} open</span>
      <span class="widget-task-stat widget-task-completed">${completedTasks.length} done</span>
    </div>`;

	if (p0Open.length > 0) {
		const items = p0Open.slice(0, 5).map((t: any) => `
      <div class="widget-p0-item">
        ${priorityBadgeHTML(t.priority)}
        <span class="widget-p0-title">${escapeHtml(truncate(t.title ?? "", 70))}</span>
      </div>`).join("");
		html += `<div class="widget-p0-list">${items}</div>`;
	}

	if (!tasks.length) {
		return `<p class="widget-placeholder">No tasks.</p>`;
	}
	return html;
}

// tasks-priority-strip — Horizontal priority badges row
function renderTasksPriorityStrip(ws: services.Workspace | null): string {
	const workspace = ws ?? activeWorkspace();
	if (!workspace) return `<p class="widget-placeholder">No workspace selected.</p>`;
	const board = taskBoardFor(workspace.id);
	const tasks = board.tasks ?? [];
	const openTasks = tasks.filter((t: any) => !t.completed);
	const counts: Record<string, number> = { P0: 0, P1: 0, P2: 0 };
	openTasks.forEach((t: any) => {
		const p = (t.priority ?? "P2").toUpperCase();
		if (p === "P0" || p === "P1" || p === "P2") counts[p]++;
	});
	const badges = [
		{ label: "P0", count: counts.P0 },
		{ label: "P1", count: counts.P1 },
		{ label: "P2", count: counts.P2 },
	].map((b) => `<span class="widget-priority-badge ${b.label.toLowerCase()}">${escapeHtml(b.label)}: ${b.count}</span>`).join("");

	if (!openTasks.length) {
		return `<p class="widget-placeholder">No open tasks.</p>`;
	}
	return `<div class="widget-priority-strip">${badges}</div>`;
}

// git-branch — Current branch name + commit info
function renderGitBranch(ws: services.Workspace | null): string {
	const workspace = ws ?? activeWorkspace();
	if (!workspace) return `<p class="widget-placeholder">No workspace selected.</p>`;
	const view = state.gitRepositoryViews.get(workspace.id);
	if (!view || !view.repository) {
		return `<p class="widget-placeholder">Git repository not loaded.</p>`;
	}
	const repo = view.repository;
	const branch = repo.currentBranch ?? "(detached)";
	const shortHead = repo.shortHead ?? "";
	const upstream = repo.upstream ?? "";
	const dirty = repo.dirty;

	let html = `<div class="widget-git-branch">`;
	html += `<span class="widget-git-branch-name">${escapeHtml(branch)}</span>`;
	if (dirty) {
		html += `<span class="widget-git-dirty-badge">modified</span>`;
	}
	if (upstream) {
		const ahead = repo.aheadCount ?? 0;
		const behind = repo.behindCount ?? 0;
		let upstreamInfo = "";
		if (ahead > 0) upstreamInfo += `+${ahead}`;
		if (behind > 0) upstreamInfo += `/${behind}`;
		if (upstreamInfo) {
			html += `<span class="widget-git-upstream">${escapeHtml(upstream)} ${escapeHtml(upstreamInfo)}</span>`;
		} else {
			html += `<span class="widget-git-upstream">${escapeHtml(upstream)}</span>`;
		}
	}
	if (shortHead) {
		html += `<span class="widget-git-hash">${escapeHtml(shortHead)}</span>`;
	}
	html += `</div>`;
	return html;
}

// git-recent-commits — Last 5 commits as compact list
function renderGitRecentCommits(ws: services.Workspace | null): string {
	const workspace = ws ?? activeWorkspace();
	if (!workspace) return `<p class="widget-placeholder">No workspace selected.</p>`;
	const view = state.gitRepositoryViews.get(workspace.id);
	if (!view || !view.repository) {
		return `<p class="widget-placeholder">Git repository not loaded.</p>`;
	}
	const commits = view.repository.commits ?? [];
	if (!commits.length) {
		return `<p class="widget-placeholder">No commits found.</p>`;
	}
	const items = commits.slice(0, 5).map((c: any) => `
    <div class="widget-git-commit">
      <span class="widget-git-commit-hash">${escapeHtml(c.shortHash ?? c.hash ?? "")}</span>
      <span class="widget-git-commit-subject">${escapeHtml(truncate(c.subject ?? "", 60))}</span>
      <span class="widget-git-commit-date" title="${escapeHtml(c.authoredAt ?? "")}">${formatDate(c.authoredAt)}</span>
    </div>`).join("");
	return `<div class="widget-git-commits">${items}</div>`;
}

// git-change-count — Uncommitted changes badge
function renderGitChangeCount(ws: services.Workspace | null): string {
	const workspace = ws ?? activeWorkspace();
	if (!workspace) return `<p class="widget-placeholder">No workspace selected.</p>`;
	// Check both file change review and git repository dirty status
	const review = state.changeReviews.get(workspace.id);
	const gitView = state.gitRepositoryViews.get(workspace.id);

	let fileCount = 0;
	let changeCount = 0;

	if (review) {
		fileCount = review.fileCount ?? 0;
		changeCount = review.changeCount ?? 0;
	}
	if (gitView?.repository) {
		const repoFileCount = gitView.repository.fileCount ?? 0;
		if (repoFileCount > fileCount) fileCount = repoFileCount;
	}

	if (fileCount === 0 && changeCount === 0) {
		return `
    <div class="widget-git-change-count widget-git-clean">
      ${iconCheck}
      <span>Clean</span>
    </div>`;
	}

	const details = [];
	if (fileCount > 0) details.push(`${fileCount} file${fileCount !== 1 ? "s" : ""}`);
	if (changeCount > 0) details.push(`${changeCount} change${changeCount !== 1 ? "s" : ""}`);

	return `
    <div class="widget-git-change-count widget-git-dirty">
      <span class="widget-git-change-number">${fileCount + changeCount}</span>
      <span class="widget-git-change-label">${details.join(", ")}</span>
    </div>`;
}

// system-heartbeat — Heartbeat/watchdog interval status
function renderSystemHeartbeat(ws: services.Workspace | null): string {
	const workspace = ws ?? activeWorkspace();
	if (!workspace) return `<p class="widget-placeholder">No workspace selected.</p>`;

	const heartbeatMs = state.heartbeatIntervals.get(workspace.id);
	const watchdogMs = state.watchdogIntervals.get(workspace.id);

	const hbEnabled = heartbeatMs != null && heartbeatMs > 0;
	const wdEnabled = watchdogMs != null && watchdogMs > 0;

	return `
    <div class="widget-heartbeat">
      <div class="widget-heartbeat-row">
        <span class="widget-heartbeat-label">Heartbeat</span>
        ${statusDotHTML(hbEnabled)}
        <span class="widget-heartbeat-value">${hbEnabled ? escapeHtml(formatIntervalMs(heartbeatMs)) : "off"}</span>
      </div>
      <div class="widget-heartbeat-row">
        <span class="widget-heartbeat-label">Watchdog</span>
        ${statusDotHTML(wdEnabled)}
        <span class="widget-heartbeat-value">${wdEnabled ? escapeHtml(formatIntervalMs(watchdogMs)) : "off"}</span>
      </div>
    </div>`;
}

// system-workspaces — Active workspace list with status dots
function renderSystemWorkspaces(_ws: services.Workspace | null): string {
	const appState = state.appState;
	if (!appState || !appState.workspaces?.length) {
		return `<p class="widget-placeholder">No workspaces configured.</p>`;
	}
	const items = appState.workspaces.map((w: any) => {
		const isActive = w.id === appState.activeWorkspaceId;
		const folders = w.folders ?? [];
		const hasMissing = folders.some((f: any) => f.missing);
		const running = state.runningKanbanWorkspaces.has(w.id);
		let dotClass = "status-dot status-active";
		if (hasMissing) dotClass = "status-dot status-inactive";
		else if (running) dotClass = "status-dot status-running";

		const label = escapeHtml(truncate(w.displayName || w.id, 30));
		const activeLabel = isActive ? ' aria-label="Active workspace"' : "";
		return `
      <div class="widget-workspace-item${isActive ? " widget-workspace-active" : ""}"${activeLabel}>
        ${statusDotHTML(!hasMissing)}
        <span class="widget-workspace-label">${label}</span>
        ${running ? '<span class="widget-running-badge">running</span>' : ""}
      </div>`;
	}).join("");
	return `<div class="widget-workspaces">${items}</div>`;
}

// code-open-tabs — Open file tabs for active workspace
function renderCodeOpenTabs(ws: services.Workspace | null): string {
	const workspace = ws ?? activeWorkspace();
	if (!workspace) return `<p class="widget-placeholder">No workspace selected.</p>`;
	const codeState = ensureCodeState(workspace.id);
	const tabs = codeState.tabs ?? [];
	if (!tabs.length) {
		return `<p class="widget-placeholder">No files open.</p>`;
	}
	const items = tabs.slice(0, 8).map((tab: any) => {
		const isActive = tab.path === codeState.activePath;
		const dirty = tab.dirty ? ' <span class="widget-dirty-dot">' + iconDot + '</span>' : '';
		const label = escapeHtml(truncate(tab.path ?? "(untitled)", 50));
		return `
      <div class="widget-tab-item${isActive ? " widget-tab-active" : ""}">
        <span class="widget-tab-label">${label}</span>
        ${dirty}
      </div>`;
	}).join("");
	return `<div class="widget-open-tabs">${items}</div>`;
}

// code-workspace-status — Workspace file/folder info
function renderCodeWorkspaceStatus(ws: services.Workspace | null): string {
	const workspace = ws ?? activeWorkspace();
	if (!workspace) return `<p class="widget-placeholder">No workspace selected.</p>`;
	const folders = workspace.folders ?? [];
	const folderLabels = folders.map((f: any) => escapeHtml(truncate(f.label ?? f.path ?? "", 40)));
	const hasMissing = folders.some((f: any) => f.missing);

	let html = `<div class="widget-code-workspace-status">`;
	html += `<div class="widget-cws-row"><span class="widget-cws-label">Workspace</span><span class="widget-cws-value">${escapeHtml(truncate(workspace.displayName || workspace.id, 30))}</span></div>`;
	if (folderLabels.length > 0) {
		html += `<div class="widget-cws-folders">${folderLabels.join(", ")}</div>`;
	}
	if (hasMissing) {
		html += `<span class="widget-cws-missing">Some folders missing</span>`;
	}
	html += `</div>`;
	return html;
}

/* ------------------------------------------------------------------ */
/*  Widget registry export                                             */
/* ------------------------------------------------------------------ */

export const widgetRegistry: Record<WidgetId, WidgetEntry> = {
	"chat-recent": { renderer: renderChatRecent, defaultSize: "medium", title: "Recent Chat" },
	"chat-busy-status": { renderer: renderChatBusyStatus, defaultSize: "small", title: "Chat Status" },
	"chat-token-budget": { renderer: renderChatTokenBudget, defaultSize: "small", title: "Token Budget" },
	"kanban-summary": { renderer: renderKanbanSummary, defaultSize: "small", title: "Kanban Summary" },
	"kanban-progress": { renderer: renderKanbanProgress, defaultSize: "medium", title: "Card Progress" },
	"kanban-done-count": { renderer: renderKanbanDoneCount, defaultSize: "small", title: "Cards Done" },
	"tasks-overview": { renderer: renderTasksOverview, defaultSize: "medium", title: "Tasks Overview" },
	"tasks-priority-strip": { renderer: renderTasksPriorityStrip, defaultSize: "small", title: "Priority Strip" },
	"git-branch": { renderer: renderGitBranch, defaultSize: "small", title: "Current Branch" },
	"git-recent-commits": { renderer: renderGitRecentCommits, defaultSize: "medium", title: "Recent Commits" },
	"git-change-count": { renderer: renderGitChangeCount, defaultSize: "small", title: "Changes" },
	"system-heartbeat": { renderer: renderSystemHeartbeat, defaultSize: "small", title: "Heartbeat" },
	"system-workspaces": { renderer: renderSystemWorkspaces, defaultSize: "medium", title: "Workspaces" },
	"code-open-tabs": { renderer: renderCodeOpenTabs, defaultSize: "medium", title: "Open Tabs" },
	"code-workspace-status": { renderer: renderCodeWorkspaceStatus, defaultSize: "small", title: "Workspace Status" },
};

/* ------------------------------------------------------------------ */
/*  Available widgets per view                                         */
/* ------------------------------------------------------------------ */

export function availableWidgetsForView(view: AppMode): WidgetId[] {
	switch (view) {
		case "chat":
			return ["chat-recent", "chat-busy-status", "chat-token-budget", "system-heartbeat"];
		case "tasks":
			return ["tasks-overview", "tasks-priority-strip", "kanban-summary", "system-heartbeat"];
		case "kanban":
			return ["kanban-summary", "kanban-progress", "kanban-done-count", "system-workspaces"];
		case "code":
			return ["code-open-tabs", "code-workspace-status", "git-change-count", "git-branch", "system-heartbeat"];
		case "git":
			return ["git-branch", "git-recent-commits", "git-change-count"];
		case "dashboard":
			return [
				"chat-busy-status", "chat-token-budget",
				"kanban-summary", "kanban-progress", "kanban-done-count",
				"tasks-overview", "tasks-priority-strip",
				"git-branch", "git-recent-commits", "git-change-count",
				"system-heartbeat", "system-workspaces",
				"code-open-tabs", "code-workspace-status",
			];
		case "settings":
			return ["system-heartbeat", "system-workspaces"];
		default:
			return [];
	}
}
