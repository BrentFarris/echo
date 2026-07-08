
import { patchChildrenFromHtml, renderMarkdown } from "../../markdown";
import { AddKanbanCardMessage, ClearKanbanCardRecovery, CloseKanbanCardDetail, CreateKanbanCardFromTask, CreateReadyKanbanCard, GetHeartbeatConfig, GetWatchdogConfig, LoadKanbanBoard, StartHeartbeat, StartWatchdog, StopHeartbeat, StopWatchdog, UpdateKanbanCardDescription, UpdateKanbanCardDirection } from "../../backend/services";
import { services } from "../../../wailsjs/go/models";
import { getAppCallbacks } from "../callbacks";
import { renderSpinnerLabel } from "../components";
import { appRoot, isElementScrolledNearBottom } from "../dom";
import { icons } from "../icons";
import { playNotificationSound } from "../notifications";
import { activeWorkspace, kanbanBoardFor, kanbanCards, selectedKanbanCard, state, changeReviewFor } from "../state";
import { pushToast } from "../toasts";
import type { HeartbeatEvent, KanbanEvent, LivenessEvent, WatchdogEvent } from "../types";
import { errorMessage, escapeAttribute, escapeHtml, laneLabel } from "../utils";
import { refreshWorkspaceChangeReview } from "../changes";
import { render } from "../render";
import type { Toast } from "../types";

const HeartbeatIntervals = [0, 60_000, 180_000, 360_000, 900_000] as const; // off, 1m, 3m, 6m, 15m
type HeartbeatInterval = (typeof HeartbeatIntervals)[number];

export function heartbeatIntervalLabel(intervalMs: number): string {
  if (intervalMs === 0) return "Off";
  const minutes = intervalMs / 60_000;
  if (minutes < 60) return `${minutes}m`;
  return `${minutes / 60}h`;
}

export function nextHeartbeatInterval(currentMs: number): HeartbeatInterval {
  const idx = HeartbeatIntervals.indexOf(currentMs as HeartbeatInterval);
  const nextIdx = (idx + 1) % HeartbeatIntervals.length;
  return HeartbeatIntervals[nextIdx];
}

export function isKanbanBoardAllDone(board?: services.KanbanBoard): boolean {
  if (!board) {
    return false;
  }
  return (
    (board.done ?? []).length > 0 &&
    (board.ready ?? []).length === 0 &&
    (board.inProgress ?? []).length === 0 &&
    (board.blocked ?? []).length === 0
  );
}

export function hasNewBlockedKanbanCards(
  previousBoard: services.KanbanBoard | undefined,
  nextBoard: services.KanbanBoard,
): boolean {
  const previousBlockedIDs = new Set((previousBoard?.blocked ?? []).map((card) => card.id));
  return (nextBoard.blocked ?? []).some((card) => !previousBlockedIDs.has(card.id));
}

export function maybePlayKanbanBoardNotification(
  previousBoard: services.KanbanBoard | undefined,
  nextBoard: services.KanbanBoard,
) {
  if (
    hasNewBlockedKanbanCards(previousBoard, nextBoard) ||
    (!isKanbanBoardAllDone(previousBoard) && isKanbanBoardAllDone(nextBoard))
  ) {
    playNotificationSound();
  }
}

export function formatElapsedTime(milliseconds: number): string {
  const totalSeconds = Math.max(0, Math.floor(milliseconds / 1000));
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  const paddedSeconds = String(seconds).padStart(2, "0");
  if (hours > 0) {
    return `${hours}:${String(minutes).padStart(2, "0")}:${paddedSeconds}`;
  }
  return `${minutes}:${paddedSeconds}`;
}

export function kanbanElapsedLabel(workspaceID: string, now = Date.now()): string {
  const startedAt = state.kanbanRunStarts.get(workspaceID);
  if (startedAt) {
    return formatElapsedTime(now - startedAt);
  }
  const elapsed = state.kanbanRunElapsed.get(workspaceID);
  return elapsed === undefined ? "0:00" : formatElapsedTime(elapsed);
}

export function hasKanbanRuntime(workspaceID: string): boolean {
  return state.kanbanRunStarts.has(workspaceID) || state.kanbanRunElapsed.has(workspaceID);
}

export function markKanbanRunStarted(workspaceID: string) {
  if (!state.kanbanRunStarts.has(workspaceID)) {
    state.kanbanRunStarts.set(workspaceID, Date.now());
    state.kanbanRunElapsed.set(workspaceID, 0);
  }
  state.creatingKanbanCardWorkspaces.delete(workspaceID);
  state.kanbanCardCreationDrafts.delete(workspaceID);
  state.runningKanbanWorkspaces.add(workspaceID);
  syncKanbanTimer();
}

export function finishKanbanRun(workspaceID: string) {
  const startedAt = state.kanbanRunStarts.get(workspaceID);
  if (startedAt) {
    state.kanbanRunElapsed.set(workspaceID, Math.max(0, Date.now() - startedAt));
  }
  state.runningKanbanWorkspaces.delete(workspaceID);
  state.kanbanRunStarts.delete(workspaceID);
  syncKanbanTimer();
}

export function forgetKanbanRun(workspaceID: string) {
  state.runningKanbanWorkspaces.delete(workspaceID);
  state.kanbanRunStarts.delete(workspaceID);
  state.kanbanRunElapsed.delete(workspaceID);
  syncKanbanTimer();
}

export function syncKanbanTimer() {
  if (state.kanbanRunStarts.size > 0 && state.kanbanTimerID === null) {
    state.kanbanTimerID = window.setInterval(patchKanbanElapsedTimes, 1000);
  }
  if (state.kanbanRunStarts.size === 0 && state.kanbanTimerID !== null) {
    window.clearInterval(state.kanbanTimerID);
    state.kanbanTimerID = null;
  }
  patchKanbanElapsedTimes();
}

export function patchKanbanElapsedTimes() {
  const now = Date.now();
  appRoot.querySelectorAll<HTMLElement>("[data-kanban-elapsed]").forEach((element) => {
    const workspaceID = element.dataset.workspaceId ?? "";
    if (!hasKanbanRuntime(workspaceID)) {
      return;
    }
    const label = kanbanElapsedLabel(workspaceID, now);
    element.textContent = label;
    element
      .closest<HTMLElement>("[data-kanban-runtime]")
      ?.setAttribute(
        "aria-label",
        state.runningKanbanWorkspaces.has(workspaceID) ? `Echo has been working for ${label}` : `Echo worked for ${label}`,
      );
  });
}

export function renderKanbanRuntime(workspaceID: string, running: boolean): string {
  const elapsed = kanbanElapsedLabel(workspaceID);
  const status = running ? "Working" : "Finished";
  return `
    <div class="kanban-runtime" role="timer" aria-label="${running ? "Echo has been working" : "Echo worked"} for ${elapsed}" data-kanban-runtime>
      ${running ? `<span class="spinner" aria-hidden="true"></span>` : icons.check}
      <span>${status}</span>
      <time data-kanban-elapsed data-workspace-id="${escapeAttribute(workspaceID)}">${escapeHtml(elapsed)}</time>
    </div>
  `;
}

export function renderEmptyBoard(): string {
  return `
    <div class="empty-state board-empty">
      <strong>No cards yet</strong>
      <span>Create a Ready card directly, or execute a chat plan to generate cards.</span>
    </div>
  `;
}

export function renderCreateKanbanCardDialog(workspaceID: string): string {
  const draft = state.kanbanCardCreationDrafts.get(workspaceID) ?? {
    title: "",
    description: "",
    acceptanceCriteria: "",
  };
  return `
    <aside class="kanban-card-create-backdrop" role="dialog" aria-modal="true" aria-labelledby="kanban-card-create-title">
      <form class="kanban-card-create-dialog" data-kanban-card-create-form>
        <header>
          <div>
            <p class="eyebrow">Ready lane</p>
            <h2 id="kanban-card-create-title">Create card</h2>
          </div>
          <button class="icon-button close-button" type="button" title="Cancel" aria-label="Cancel card creation" data-action="cancel-create-ready-card">
            ${icons.x}
          </button>
        </header>
        <label>
          <span>Title</span>
          <input name="title" type="text" value="${escapeAttribute(draft.title)}" placeholder="Implement focused change" autocomplete="off" data-kanban-card-create-title data-initial-focus />
        </label>
        <label>
          <span>Description</span>
          <textarea name="description" rows="5" placeholder="Describe the implementation work for the agent." data-kanban-card-create-description>${escapeHtml(draft.description)}</textarea>
        </label>
        <label>
          <span>Acceptance criteria</span>
          <textarea name="acceptanceCriteria" rows="4" placeholder="One observable outcome per line" data-kanban-card-create-criteria>${escapeHtml(draft.acceptanceCriteria)}</textarea>
        </label>
        <div class="kanban-card-create-actions">
          <button class="secondary-button" type="button" data-action="cancel-create-ready-card">Cancel</button>
          <button class="primary-button icon-text-button" type="submit" ${kanbanCardCreationDraftValid(draft) ? "" : "disabled"}>
            ${icons.plus}
            <span>Create Ready card</span>
          </button>
        </div>
      </form>
    </aside>
  `;
}

function kanbanCardCreationDraftValid(draft: { title: string; description: string; acceptanceCriteria: string }) {
  return Boolean(
    draft.title.trim() &&
      draft.description.trim() &&
      draft.acceptanceCriteria.split(/\r?\n/).some((criterion) => criterion.trim()),
  );
}

export function renderDecompositionState(): string {
  return `
    <div class="empty-state board-empty decomposition-state" role="status" aria-live="polite">
      <span class="spinner decomposition-spinner" aria-hidden="true"></span>
      <strong>Decomposing cards</strong>
      <span>Echo is converting the chat plan into Ready cards.</span>
    </div>
  `;
}

export function renderKanbanBoard(board: services.KanbanBoard): string {
  return `
    <div class="kanban-board" aria-label="Kanban lanes">
      ${renderKanbanLane("Ready", board.ready ?? [])}
      ${renderKanbanLane("In Progress", board.inProgress ?? [])}
      ${renderKanbanLane("Blocked", board.blocked ?? [])}
      ${renderKanbanLane("Done", board.done ?? [])}
    </div>
  `;
}

export function renderKanbanLane(title: string, cards: services.KanbanCard[]): string {
  return `
    <section class="kanban-lane" aria-label="${escapeAttribute(title)}">
      <header>
        <strong>${escapeHtml(title)}</strong>
        <span>${cards.length}</span>
      </header>
      <div class="kanban-cards">
        ${
          cards.length
            ? cards.map(renderKanbanCard).join("")
            : `<p class="lane-empty">No cards</p>`
        }
      </div>
    </section>
  `;
}

export function renderKanbanCard(card: services.KanbanCard): string {
  const unavailable = card.lane === "ready" && !card.eligible;
  const recoveryBadge = renderRecoveryBadge(card);
  const repairBadge = isRepairCard(card) ? renderRepairBadge() : "";
  const progressPct = kanbanCardProgressPercent(card);
  const statusMessage = renderKanbanCardStatus(card);
  const progressBar = card.lane === "inProgress" ? renderInProgressProgressBar(progressPct, card.progressTranscript) : "";
  return `
    <article class="kanban-card ${unavailable ? "is-unavailable" : ""} ${card.recoveryType ? "has-recovery-badge" : ""} ${isRepairCard(card) ? "is-repair" : ""}" data-lane="${escapeAttribute(card.lane)}">
      <button
        class="kanban-card-open"
        type="button"
        data-action="open-card"
        data-card-id="${escapeAttribute(card.id)}"
        aria-label="Open ${escapeAttribute(card.title)} details"
      >
        <div class="kanban-card-title-row">
          <span class="kanban-card-status-dot ${laneDotClass(card.lane)}" aria-hidden="true"></span>
          <strong>${escapeHtml(card.title)}</strong>
          <span class="kanban-card-id">${escapeHtml(card.id)}</span>
        </div>
        ${statusMessage}
        ${progressBar}
      </button>
      ${recoveryBadge}
      ${repairBadge}
    </article>
  `;
}

export function renderKanbanCardStatus(card: services.KanbanCard): string {
  switch (card.lane) {
    case "done": return renderDoneStatus(card);
    case "inProgress": return renderInProgressStatus(card);
    case "ready": return renderReadyStatus(card);
    case "blocked": return renderBlockedStatus(card);
    default: return "";
  }
}

function kanbanCardProgressPercent(card: services.KanbanCard): number {
  const lane = card.lane ?? "";
  if (lane === "done") return 100;
  if (lane === "ready" || lane === "blocked") return 0;
  // in progress: estimate from tool_call entries vs acceptance criteria.
  // Only tool_call entries count as real work because they represent actual
  // implementation steps (file reads, edits, shell commands, etc.).
  // Status/thinking/message/verification entries are overhead and should not
  // inflate the percentage.
  const transcript = card.progressTranscript ?? [];
  const toolCallCount = transcript.filter(e => e.type === "tool_call").length;
  const criteriaLen = (card.acceptanceCriteria ?? []).length;
  if (criteriaLen > 0) {
    // Each tool call counts as ~1/criteriaLen of the work, scaled to fill 0-95%.
    // Reserve 5% for post-execution verification and cleanup.
    const pct = Math.round((toolCallCount / criteriaLen) * 95);
    return Math.min(pct, 97);
  }
  // No criteria: estimate ~10 tool calls as a rough full-card baseline,
  // capped at 80% to leave room for verification.
  return Math.min(Math.round((toolCallCount / 10) * 100), 80);
}

function getLastVerificationEntry(transcript: services.KanbanProgressEntry[] | undefined): services.KanbanProgressEntry | undefined {
  if (!transcript) return undefined;
  for (let i = transcript.length - 1; i >= 0; i--) {
    const entry = transcript[i];
    if (entry.type === "verification") return entry;
  }
  return undefined;
}

function countChangedPaths(card: services.KanbanCard): number {
  const entry = getLastVerificationEntry(card.progressTranscript);
  if (!entry) return 0;
  // Parse changed paths from verification content like "Changed paths:\n- path1\n- path2"
  const lines = entry.content.split("\n");
  let count = 0;
  for (const line of lines) {
    if (line.trim().startsWith("- ")) count++;
  }
  return count;
}

function renderDoneStatus(card: services.KanbanCard): string {
  const entry = getLastVerificationEntry(card.progressTranscript);
  if (!entry) {
    return `<p class="kanban-card-status-text status-success">${icons.check} verification passed</p>`;
  }
  // Check if verification passed by looking at the title
  const passed = entry.title?.includes("passed") ?? false;
  const failed = entry.title?.includes("failed") ?? false;
  const skipped = entry.title?.includes("skipped") ?? false;
  const fileCount = countChangedPaths(card);

  let statusClass = "status-success";
  let icon = icons.check;
  let text: string;

  if (passed) {
    text = `verification passed`;
    if (fileCount > 0) {
      text += `, ${fileCount} file${fileCount > 1 ? "s" : ""} changed`;
    }
  } else if (failed) {
    statusClass = "status-error";
    icon = icons.x;
    text = `verification failed`;
  } else if (skipped) {
    statusClass = "status-warning";
    icon = "\u23F3"; // hourglass emoji
    text = `verification skipped`;
  } else {
    text = entry.title ?? "verification complete";
  }

  return `<p class="kanban-card-status-text ${statusClass}">${icon} ${escapeHtml(text)}</p>`;
}

function getLastToolCallName(transcript: services.KanbanProgressEntry[] | undefined): string {
  if (!transcript) return "";
  for (let i = transcript.length - 1; i >= 0; i--) {
    const entry = transcript[i];
    if (entry.type === "tool_call" && entry.title) {
      // Title is like "Tool call: filesystem_read_text"
      const parts = entry.title.split(": ");
      if (parts.length > 1) return parts[1];
    }
  }
  return "";
}

function renderInProgressStatus(card: services.KanbanCard): string {
  const pct = kanbanCardProgressPercent(card);
  return `<p class="kanban-card-status-text status-inprogress">${pct}%</p>`;
}

function renderInProgressProgressBar(pct: number, transcript: services.KanbanProgressEntry[] | undefined): string {
  const toolName = getLastToolCallName(transcript);
  const toolLabel = toolName ? ` <span class="kanban-card-tool-label">${escapeHtml(toolName)}</span>` : "";
  return `
    <div class="kanban-card-progress-bar">
      <div class="kanban-card-progress-track">
        <div class="kanban-card-progress-fill" style="width: ${pct}%"></div>
      </div>
      <span class="kanban-card-progress-label">${pct}% complete${toolLabel}</span>
    </div>`;
}

function renderReadyStatus(card: services.KanbanCard): string {
  const deps = card.dependencyStatuses ?? [];
  if (deps.length === 0) {
    // No dependencies — card is ready to go
    return `<p class="kanban-card-status-text status-success">${icons.check} ready</p>`;
  }

  // Check if all dependencies are satisfied
  const unsatisfied = deps.filter(d => !d.done);
  if (unsatisfied.length === 0) {
    // All deps done
    return `<p class="kanban-card-status-text status-success">${icons.check} all dependencies met</p>`;
  }

  // Show specific unmet dependency — pick the first unsatisfied one
  const blocking = unsatisfied[0];
  const depTitle = blocking.title || blocking.id;
  const depStatus = laneLabel(blocking.status ?? "ready");
  return `<p class="kanban-card-status-text status-warning">\u23F3 depends on ${escapeHtml(depTitle)} (${escapeHtml(depStatus)})</p>`;
}

function renderBlockedStatus(card: services.KanbanCard): string {
  // Look for block reason in progress transcript or recovery state
  const transcript = card.progressTranscript ?? [];
  
  // Check for escalated recovery type first
  if (card.recoveryType === "escalated") {
    return `<p class="kanban-card-status-text status-error">${icons.x} escalated after repeated stalls</p>`;
  }

  // Look for the last status/message entry that might explain the block
  for (let i = transcript.length - 1; i >= 0; i--) {
    const entry = transcript[i];
    if (entry.type === "status" || entry.type === "message") {
      const content = (entry.content ?? "").trim();
      const title = (entry.title ?? "").trim();
      // Skip generic status change entries
      if (content.toLowerCase().includes("moved to blocked") || 
          content.toLowerCase().includes("agent stopped")) {
        return `<p class="kanban-card-status-text status-error">${icons.x} ${escapeHtml(content)}</p>`;
      }
      if (title && !content.startsWith("Moved to")) {
        return `<p class="kanban-card-status-text status-error">${icons.x} ${escapeHtml(title)}</p>`;
      }
    }
  }

  // Fallback — check blockedBy references
  const blockedBy = card.blockedBy ?? [];
  if (blockedBy.length > 0) {
    return `<p class="kanban-card-status-text status-error">${icons.x} blocked by dependencies</p>`;
  }

  return `<p class="kanban-card-status-text status-error">${icons.x} agent stopped early</p>`;
}

function laneDotClass(lane: string): string {
  switch (lane) {
    case "done": return "status-done";
    case "inProgress": return "status-inprogress";
    case "blocked": return "status-blocked";
    default: return "status-ready";
  }
}

function renderKanbanCardProgress(pct: number): string {
  if (pct <= 0) return "";
  return `
    <div class="kanban-card-progress">
      <div class="kanban-card-progress-track">
        <div class="kanban-card-progress-fill" style="width: ${pct}%"></div>
      </div>
      <span class="kanban-card-progress-label">${pct}%</span>
    </div>`;
}

export function renderRecoveryBadge(card: services.KanbanCard): string {
  if (!card.recoveryType) {
    return "";
  }
  const isEscalated = card.recoveryType === "escalated";
  const label = isEscalated ? "Escalated" : `Reset ${card.autoRetriesUsed ?? 0}`;
  return `
    <span class="recovery-badge ${isEscalated ? "is-escalated" : "is-reset"}" role="status" aria-label="${escapeAttribute(label)}">
      ${isEscalated ? icons.x : icons.retry}
      <span>${escapeHtml(label)}</span>
    </span>
  `;
}

export function canDeleteKanbanCard(card: services.KanbanCard): boolean {
  return card.lane === "ready" || card.lane === "done";
}

export function renderKanbanDetail(board: services.KanbanBoard): string {
  const card = selectedKanbanCard(board);
  if (!card) {
    return "";
  }

  const dependencies = card.dependencyStatuses ?? [];
  const criteria = card.acceptanceCriteria ?? [];
  const transcript = card.progressTranscript ?? [];
  const blocked = card.lane === "ready" && !card.eligible;
  const canReset = card.lane !== "ready" || transcript.length > 0;
  const canEditDescription = card.lane === "ready" && !state.runningKanbanWorkspaces.has(board.workspaceId);
  const canDelete = canDeleteKanbanCard(card);
  const draftKey = `${board.workspaceId}:${card.id}`;
  const cardDraft = state.cardMessageDrafts.get(draftKey) ?? "";
  return `
    <aside class="card-detail-backdrop" role="dialog" aria-modal="true" aria-labelledby="card-detail-title">
      <section class="card-detail" data-card-detail>
        <header class="card-detail-header">
          <div class="card-detail-heading-row">
            <div>
              <p class="eyebrow">${escapeHtml(card.id)} - ${escapeHtml(laneLabel(card.status || card.lane))}</p>
              <h2 id="card-detail-title">${escapeHtml(card.title)}</h2>
            </div>
            <button class="icon-button close-button" type="button" title="Close" aria-label="Close card details" data-action="close-card">
              ${icons.x}
            </button>
          </div>

          <div class="status-controls" aria-label="Card status">
            ${renderLaneButton(card, "ready")}
            ${renderLaneButton(card, "inProgress", blocked)}
            ${renderLaneButton(card, "blocked")}
            ${renderLaneButton(card, "done")}
          </div>

          ${blocked ? `<p class="blocked-note">Unavailable until prerequisites are Done.</p>` : ""}
          <div class="card-detail-actions">
            <button class="secondary-button icon-text-button" type="button" data-action="reset-card" data-card-id="${escapeAttribute(card.id)}" ${canReset ? "" : "disabled"}>
              ${icons.refresh}
              <span>Reset</span>
            </button>
            ${
              card.lane === "inProgress"
                ? `<button class="secondary-button icon-text-button stop-card-button" type="button" data-action="stop-card" data-card-id="${escapeAttribute(card.id)}">
                    ${icons.stop}
                    <span>Stop</span>
                  </button>`
                : ""
            }
            ${
              canDelete
                ? `<button class="secondary-button icon-text-button danger-button" type="button" data-action="delete-card" data-card-id="${escapeAttribute(card.id)}">
                    ${icons.trash}
                    <span>Delete</span>
                  </button>`
                : ""
            }
          </div>
        </header>

        <section class="detail-section">
          <h3>Description</h3>
          ${
            canEditDescription
              ? `<form class="card-description-form" data-card-description-form data-card-id="${escapeAttribute(card.id)}">
                  <textarea name="description" rows="5" aria-label="Card description" data-card-description-input>${escapeHtml(card.description)}</textarea>
                  <button class="primary-button icon-text-button" type="submit" disabled>
                    ${icons.check}
                    <span>Save</span>
                  </button>
                </form>`
              : `<p>${escapeHtml(card.description)}</p>`
          }
        </section>

        <section class="detail-section">
          <h3>Direction</h3>
          ${
            canEditDescription
              ? `<form class="card-direction-form" data-card-direction-form data-card-id="${escapeAttribute(card.id)}">
                  <textarea name="direction" rows="5" aria-label="Card direction" data-card-direction-input>${escapeHtml(card.direction ?? "")}</textarea>
                  <button class="primary-button icon-text-button" type="submit" disabled>
                    ${icons.check}
                    <span>Save</span>
                  </button>
                </form>`
              : `<p>${escapeHtml(card.direction || "")}</p>`
          }
        </section>

        <section class="detail-section">
          <h3>Dependencies</h3>
          ${
            dependencies.length
              ? `<div class="dependency-list">${dependencies
                  .map(
                    (dependency) => `
                      <div class="dependency-row ${dependency.done ? "is-done" : ""}">
                        <strong>${escapeHtml(dependency.title || dependency.id)}</strong>
                        <span>${escapeHtml(laneLabel(dependency.status))}</span>
                      </div>
                    `,
                  )
                  .join("")}</div>`
              : `<p>No dependencies.</p>`
          }
        </section>

        <section class="detail-section">
          <h3>Acceptance Criteria</h3>
          ${
            criteria.length
              ? `<ul>${criteria.map((item) => `<li>${escapeHtml(item)}</li>`).join("")}</ul>`
              : `<p>No acceptance criteria recorded.</p>`
          }
        </section>

        <section class="detail-section" data-card-progress-section>
          ${renderProgressSectionContent(transcript)}
        </section>

        ${renderLivenessSection(card, board.workspaceId)}

        ${
          card.lane === "blocked"
            ? `<form class="card-message-form" data-card-message-form data-card-id="${escapeAttribute(card.id)}">
                <textarea name="message" rows="3" placeholder="Add direction..." aria-label="Message for card" data-card-message-input>${escapeHtml(cardDraft)}</textarea>
                <button class="primary-button icon-text-button" type="submit" ${cardDraft.trim() ? "" : "disabled"}>
                  ${icons.send}
                  <span>Send</span>
                </button>
              </form>`
            : ""
        }
      </section>
    </aside>
  `;
}

export function renderLivenessSection(card: services.KanbanCard, workspaceID: string): string {
  const hasRecovery = card.recoveryType || (card.autoRetriesUsed ?? 0) > 0;
  if (!hasRecovery) {
    return "";
  }
  const isEscalated = card.recoveryType === "escalated";
  const retriesLabel = `Retries: ${card.autoRetriesUsed ?? 0}`;
  const statusLabel = isEscalated ? "Escalated" : "Auto-reset";
  return `
    <section class="detail-section liveness-section" aria-label="Liveness">
      <h3>Liveness</h3>
      <div class="liveness-status ${isEscalated ? "is-escalated" : "is-reset"}" role="status">
        <span class="liveness-status-icon">${isEscalated ? icons.x : icons.retry}</span>
        <div class="liveness-status-text">
          <strong>${escapeHtml(statusLabel)}</strong>
          <span>${escapeHtml(retriesLabel)}${card.stalledAt ? ` · Stalled at ${escapeHtml(card.stalledAt)}` : ""}</span>
        </div>
      </div>
      <button class="secondary-button icon-text-button clear-recovery-button" type="button" data-action="clear-card-recovery" data-card-id="${escapeAttribute(card.id)}">
        ${icons.refresh}
        <span>Clear recovery state</span>
      </button>
    </section>
  `;
}

export function renderLaneButton(card: services.KanbanCard, lane: string, blocked = false): string {
  const active = card.lane === lane;
  return `
    <button
      class="status-button ${active ? "is-active" : ""}"
      type="button"
      data-action="move-card"
      data-card-id="${escapeAttribute(card.id)}"
      data-lane="${escapeAttribute(lane)}"
      ${active || blocked ? "disabled" : ""}
    >${escapeHtml(laneLabel(lane))}</button>
  `;
}

export function renderProgressEntry(entry: services.KanbanProgressEntry): string {
  const verificationClass = entry.type === "verification" ? " is-verification" : "";
  return `
    <article class="transcript-entry${verificationClass}">
      <header>
        <strong>${escapeHtml(entry.title || entry.type || "Progress")}</strong>
        ${entry.status ? `<span>${escapeHtml(laneLabel(entry.status))}</span>` : ""}
      </header>
      <p>${escapeHtml(entry.content)}</p>
    </article>
  `;
}

export function renderProgressSectionContent(transcript: services.KanbanProgressEntry[]): string {
  return `
    <h3>Progress Transcript</h3>
    ${
      transcript.length
        ? `<div class="transcript-list" data-transcript-list>${transcript.map(renderProgressEntry).join("")}</div>`
        : `<p>No progress recorded yet.</p>`
    }
  `;
}


export function bindCardMessageEvents(root: ParentNode) {
  const form = root.querySelector<HTMLFormElement>("[data-card-message-form]");
  form?.addEventListener("submit", handleCardMessageSubmit);
  root
    .querySelectorAll<HTMLTextAreaElement>("[data-card-message-input]")
    .forEach((input) => input.addEventListener("input", handleCardMessageInput));
}

export function bindKanbanCardCreationEvents(root: ParentNode) {
  const form = root.querySelector<HTMLFormElement>("[data-kanban-card-create-form]");
  form?.addEventListener("submit", handleKanbanCardCreationSubmit);
  form
    ?.querySelectorAll<HTMLInputElement | HTMLTextAreaElement>("input, textarea")
    .forEach((input) => input.addEventListener("input", handleKanbanCardCreationInput));
}

export function handleKanbanCardCreationInput(event: Event) {
  const workspace = activeWorkspace();
  const form = (event.currentTarget as HTMLElement).closest<HTMLFormElement>("[data-kanban-card-create-form]");
  if (!workspace || !form) {
    return;
  }
  const draft = {
    title: form.querySelector<HTMLInputElement>("[data-kanban-card-create-title]")?.value ?? "",
    description: form.querySelector<HTMLTextAreaElement>("[data-kanban-card-create-description]")?.value ?? "",
    acceptanceCriteria: form.querySelector<HTMLTextAreaElement>("[data-kanban-card-create-criteria]")?.value ?? "",
    sourceTaskId: state.kanbanCardCreationDrafts.get(workspace.id)?.sourceTaskId,
    sourceTaskUpdatedAt: state.kanbanCardCreationDrafts.get(workspace.id)?.sourceTaskUpdatedAt,
  };
  state.kanbanCardCreationDrafts.set(workspace.id, draft);
  const submit = form.querySelector<HTMLButtonElement>('button[type="submit"]');
  if (submit) {
    submit.disabled = !kanbanCardCreationDraftValid(draft);
  }
}

export async function handleKanbanCardCreationSubmit(event: SubmitEvent) {
  event.preventDefault();
  const workspace = activeWorkspace();
  if (!workspace || state.runningKanbanWorkspaces.has(workspace.id)) {
    return;
  }
  const draft = state.kanbanCardCreationDrafts.get(workspace.id);
  if (!draft || !kanbanCardCreationDraftValid(draft)) {
    return;
  }
  const criteria = draft.acceptanceCriteria
    .split(/\r?\n/)
    .map((criterion) => criterion.trim())
    .filter(Boolean);

  try {
    if (draft.sourceTaskId) {
      const conversion = await CreateKanbanCardFromTask(
        workspace.id,
        draft.sourceTaskId,
        draft.title.trim(),
        draft.description.trim(),
        criteria,
        draft.sourceTaskUpdatedAt ?? "",
      );
      state.kanbanBoards.set(workspace.id, conversion.kanban);
      state.taskBoards.set(workspace.id, conversion.tasks);
    } else {
      const board = await CreateReadyKanbanCard(
        workspace.id,
        draft.title.trim(),
        draft.description.trim(),
        criteria,
      );
      state.kanbanBoards.set(workspace.id, board);
    }
    state.creatingKanbanCardWorkspaces.delete(workspace.id);
    state.kanbanCardCreationDrafts.delete(workspace.id);
    pushToast(draft.sourceTaskId ? "Task converted to a Ready card." : "Ready card created.", "success");
    getAppCallbacks().render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
  }
}

export function bindCardDescriptionEvents(root: ParentNode) {
  const form = root.querySelector<HTMLFormElement>("[data-card-description-form]");
  form?.addEventListener("submit", handleCardDescriptionSubmit);
  root
    .querySelectorAll<HTMLTextAreaElement>("[data-card-description-input]")
    .forEach((input) => input.addEventListener("input", handleCardDescriptionInput));
}

export function handleCardDescriptionInput(event: Event) {
  const workspace = activeWorkspace();
  const card = workspace ? selectedKanbanCard(kanbanBoardFor(workspace.id)) : null;
  if (!workspace || !card) {
    return;
  }
  const input = event.currentTarget as HTMLTextAreaElement;
  const button = input.form?.querySelector<HTMLButtonElement>('button[type="submit"]');
  if (button) {
    const nextDescription = input.value.trim();
    button.disabled = nextDescription.length === 0 || nextDescription === card.description.trim();
  }
}

export async function handleCardDescriptionSubmit(event: SubmitEvent) {
  event.preventDefault();
  const workspace = activeWorkspace();
  const form = event.currentTarget as HTMLFormElement;
  const cardID = form.dataset.cardId ?? "";
  const input = form.querySelector<HTMLTextAreaElement>("[data-card-description-input]");
  if (!workspace || !cardID || !input) {
    return;
  }
  const description = input.value.trim();
  if (!description) {
    return;
  }

  try {
    state.kanbanBoards.set(workspace.id, await UpdateKanbanCardDescription(workspace.id, cardID, description));
    state.selectedKanbanCards.set(workspace.id, cardID);
    pushToast("Card description updated.", "success");
    getAppCallbacks().render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    getAppCallbacks().render();
  }
}

export function handleCardMessageInput(event: Event) {
  const workspace = activeWorkspace();
  const card = workspace ? selectedKanbanCard(kanbanBoardFor(workspace.id)) : null;
  if (!workspace || !card) {
    return;
  }
  const input = event.currentTarget as HTMLTextAreaElement;
  const key = `${workspace.id}:${card.id}`;
  state.cardMessageDrafts.set(key, input.value);
  const button = input.form?.querySelector<HTMLButtonElement>('button[type="submit"]');
  if (button) {
    button.disabled = input.value.trim().length === 0;
  }
}

export async function handleCardMessageSubmit(event: SubmitEvent) {
  event.preventDefault();
  const workspace = activeWorkspace();
  const form = event.currentTarget as HTMLFormElement;
  const cardID = form.dataset.cardId ?? "";
  if (!workspace || !cardID) {
    return;
  }
  const key = `${workspace.id}:${cardID}`;
  const message = (state.cardMessageDrafts.get(key) ?? "").trim();
  if (!message) {
    return;
  }

  try {
    state.kanbanBoards.set(workspace.id, await AddKanbanCardMessage(workspace.id, cardID, message));
    state.cardMessageDrafts.delete(key);
    state.selectedKanbanCards.set(workspace.id, cardID);
    pushToast("Card returned to Ready.", "success");
    getAppCallbacks().render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    getAppCallbacks().render();
  }
}

export function bindCardDirectionEvents(root: ParentNode) {
  const form = root.querySelector<HTMLFormElement>("[data-card-direction-form]");
  form?.addEventListener("submit", handleCardDirectionSubmit);
  root
    .querySelectorAll<HTMLTextAreaElement>("[data-card-direction-input]")
    .forEach((input) => input.addEventListener("input", handleCardDirectionInput));
}

export function handleCardDirectionInput(event: Event) {
  const workspace = activeWorkspace();
  const card = workspace ? selectedKanbanCard(kanbanBoardFor(workspace.id)) : null;
  if (!workspace || !card) {
    return;
  }
  const input = event.currentTarget as HTMLTextAreaElement;
  const button = input.form?.querySelector<HTMLButtonElement>('button[type="submit"]');
  if (button) {
    const nextDirection = input.value.trim();
    button.disabled = nextDirection.length === 0 || nextDirection === (card.direction ?? "").trim();
  }
}

export async function handleCardDirectionSubmit(event: SubmitEvent) {
  event.preventDefault();
  const workspace = activeWorkspace();
  const form = event.currentTarget as HTMLFormElement;
  const cardID = form.dataset.cardId ?? "";
  const input = form.querySelector<HTMLTextAreaElement>("[data-card-direction-input]");
  if (!workspace || !cardID || !input) {
    return;
  }
  const direction = input.value.trim();
  if (!direction) {
    return;
  }

  try {
    state.kanbanBoards.set(workspace.id, await UpdateKanbanCardDirection(workspace.id, cardID, direction));
    state.selectedKanbanCards.set(workspace.id, cardID);
    pushToast("Card direction updated.", "success");
    getAppCallbacks().render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    getAppCallbacks().render();
  }
}

export async function loadActiveKanbanBoard() {
  const workspace = activeWorkspace();
  if (!workspace) {
    return;
  }
  state.kanbanBoards.set(workspace.id, await LoadKanbanBoard(workspace.id));
}

export async function closeSelectedCardDetail(workspaceID: string) {
  const cardID = state.selectedKanbanCards.get(workspaceID) ?? "";
  if (!cardID) {
    return;
  }
  try {
    state.kanbanBoards.set(workspaceID, await CloseKanbanCardDetail(workspaceID, cardID));
  } catch {
  } finally {
    state.selectedKanbanCards.delete(workspaceID);
  }
}

export function applyKanbanEvent(event: KanbanEvent) {
  const previousBoard = state.kanbanBoards.get(event.workspaceId);
  const board = services.KanbanBoard.createFrom(event.board);
  state.kanbanBoards.set(event.workspaceId, board);
  maybePlayKanbanBoardNotification(previousBoard, board);
  if (event.type === "card_started") {
    markKanbanRunStarted(event.workspaceId);
  }
  if (event.type === "scheduler_complete") {
    finishKanbanRun(event.workspaceId);
    void refreshWorkspaceChangeReview(event.workspaceId);
  }
  if (activeWorkspace()?.id === event.workspaceId) {
    if (event.type === "card_progress") {
      if (patchOpenCardProgress(event)) return;
      const card = kanbanCards(board).find((c) => c.id === event.cardId);
      if (card && patchCardProgress(card)) return;
      if (patchKanbanBoard(event.workspaceId)) return;
    }
    renderKanbanEventPreservingScroll();
  }
}

function renderKanbanEventPreservingScroll() {
  const mainContent = appRoot.querySelector<HTMLElement>(".main-content");
  const scrollTop = mainContent?.scrollTop ?? 0;
  const scrollLeft = mainContent?.scrollLeft ?? 0;

  getAppCallbacks().render();

  const renderedMainContent = appRoot.querySelector<HTMLElement>(".main-content");
  if (!renderedMainContent) {
    return;
  }
  renderedMainContent.scrollTop = scrollTop;
  renderedMainContent.scrollLeft = scrollLeft;
}

export function patchOpenCardProgress(event: KanbanEvent): boolean {
  const board = kanbanBoardFor(event.workspaceId);
  const card = selectedKanbanCard(board);
  if (!card || card.id !== event.cardId) {
    return false;
  }

  const detail = appRoot.querySelector<HTMLElement>("[data-card-detail]");
  const section = detail?.querySelector<HTMLElement>("[data-card-progress-section]");
  if (!detail || !section) {
    return false;
  }

  const keepPinned = isElementScrolledNearBottom(detail);
  patchChildrenFromHtml(section, renderProgressSectionContent(card.progressTranscript ?? []));
  if (keepPinned) {
    detail.scrollTop = detail.scrollHeight;
  }
  return true;
}

/** Patch a single kanban card's progress bar and status text without
 *  rebuilding the board or calling bindEvents. Returns true when the
 *  card element was found and patched. */
export function patchCardProgress(card: services.KanbanCard): boolean {
  // Locate the card article — first try with lane filter, then fall back without
  let container = appRoot.querySelector<HTMLElement>(`article.kanban-card[data-lane="${escapeAttribute(card.lane)}"] button.kanban-card-open[data-card-id="${escapeAttribute(card.id)}"]`)
    ?.closest("article.kanban-card");
  if (!container) {
    container = appRoot.querySelector<HTMLElement>(`button.kanban-card-open[data-card-id="${escapeAttribute(card.id)}"]`)
      ?.closest("article.kanban-card");
  }
  if (!container) return false;

  // Update status text (percentage for inProgress cards)
  const statusText = container.querySelector<HTMLElement>(".kanban-card-status-text");
  if (statusText) {
    const newStatusHtml = renderKanbanCardStatus(card);
    const template = document.createElement("template");
    template.innerHTML = newStatusHtml;
    const newStatus = template.content.firstElementChild;
    if (newStatus) {
      statusText.replaceWith(newStatus);
    }
  }

  // Update progress bar for inProgress cards
  if (card.lane === "inProgress") {
    const pct = kanbanCardProgressPercent(card);
    const progressBar = container.querySelector<HTMLElement>(".kanban-card-progress-bar");
    if (progressBar) {
      // Update fill width
      const fill = progressBar.querySelector<HTMLElement>(".kanban-card-progress-fill");
      if (fill) {
        fill.style.width = `${pct}%`;
      }
      // Update label text (percentage + tool label)
      const label = progressBar.querySelector<HTMLElement>(".kanban-card-progress-label");
      if (label) {
        const toolName = getLastToolCallName(card.progressTranscript);
        const toolLabel = toolName ? ` <span class="kanban-card-tool-label">${escapeHtml(toolName)}</span>` : "";
        label.innerHTML = `${pct}% complete${toolLabel}`;
      }
    } else {
      // Progress bar didn't exist — card may have just started, inject it
      const openButton = container.querySelector<HTMLElement>("button.kanban-card-open");
      if (openButton) {
        const newBarHtml = renderInProgressProgressBar(pct, card.progressTranscript);
        const template = document.createElement("template");
        template.innerHTML = newBarHtml;
        const newBar = template.content.firstElementChild;
        if (newBar) {
          openButton.appendChild(newBar);
        }
      }
    }
  }

  return true;
}

/** Patch only the kanban board region inside the main panel, avoiding a
 *  full app-shell re-render. Uses the existing patchChildrenFromHtml /
 *  morphChildren infrastructure. Returns true when elements were found
 *  and patched. */
export function patchKanbanBoard(workspaceID: string): boolean {
  const kanbanPanel = appRoot.querySelector<HTMLElement>(".kanban-panel");
  if (!kanbanPanel) return false;

  const board = kanbanBoardFor(workspaceID);
  const workspace = activeWorkspace();
  const review = workspace ? changeReviewFor(workspace.id) : null;
  const hasCards = workspace ? (kanbanCards(board ?? {}).length > 0) : false;
  const running = state.runningKanbanWorkspaces.has(workspaceID);
  const decomposing = workspace ? state.creatingKanbanCardWorkspaces.has(workspaceID) : false;

  // Patch the kanban-board element (lanes, cards, empty state)
  const boardContainer = kanbanPanel.querySelector<HTMLElement>(".kanban-board");
  if (boardContainer && board) {
    const newBoardHtml = decomposing && !hasCards
      ? renderDecompositionState()
      : hasCards
        ? renderKanbanBoard(board)
        : renderEmptyBoard();
    patchChildrenFromHtml(boardContainer, newBoardHtml);
  }

  // Patch panel-heading (runtime label, button states) via targeted innerHTML
  const heading = kanbanPanel.querySelector<HTMLElement>(".panel-heading");
  if (heading && workspace) {
    const reviewCount = review?.fileCount ?? 0;
    heading.innerHTML = `
      <div class="kanban-heading-main">
        <span>Kanban</span>
        <strong id="kanban-title">${escapeHtml(workspace.displayName)}</strong>
        ${hasKanbanRuntime(workspace.id) ? renderKanbanRuntime(workspace.id, running) : ""}
      </div>
      <button type="button" class="icon-button view-dashboard-button" title="View Kanban dashboard" aria-label="Kanban dashboard" data-action="open-view-dashboard" data-view="kanban">${icons.dashboard}</button>
      <div class="kanban-actions">
        <button class="secondary-button icon-text-button change-review-button" type="button" title="Review AI file changes" data-action="open-change-review">
          ${icons.file}
          <span>Changes</span>
          <span class="change-count-badge">${escapeHtml(String(reviewCount))}</span>
        </button>
        <button class="secondary-button icon-text-button" type="button" data-action="open-create-ready-card" ${running ? "disabled" : ""}>
          ${icons.plus}
          <span>New card</span>
        </button>
        <button class="icon-text-button primary-button" type="button" data-action="start-agents" ${running || !hasCards ? "disabled" : ""}>
          ${icons.execute}
          <span class="run-button">Run</span>
        </button>
        <button class="icon-button danger-button" type="button" title="Clear done cards" aria-label="Clear done Kanban cards" data-action="clear-done-cards" ${(board?.done ?? []).length > 0 ? "" : "disabled"}>
          ${icons.trash}
        </button>
        <button class="icon-button stop-button" type="button" title="Stop agents" aria-label="Stop agents" data-action="stop-agents" ${running ? "" : "disabled"}>
          ${icons.stop}
        </button>
        <button class="secondary-button icon-text-button heartbeat-toggle-button" type="button" title="Auto-run Kanban at interval (click to cycle)" aria-label="Heartbeat toggle" data-action="toggle-heartbeat" data-workspace-id="${escapeAttribute(workspace.id)}">
          ${icons.refresh}
          <span>Auto: ${escapeHtml(heartbeatIntervalLabel(getHeartbeatInterval(workspace.id)))}</span>
        </button>
        <button class="secondary-button icon-text-button watchdog-toggle-button" type="button" title="Watchdog verification interval (click to cycle)" aria-label="Watchdog toggle" data-action="toggle-watchdog" data-workspace-id="${escapeAttribute(workspace.id)}">
          ${icons.search}
          <span>Watchdog: ${escapeHtml(watchdogIntervalLabel(getWatchdogInterval(workspace.id)))}</span>
        </button>
      </div>
    `;
  }

  return true;
}

export function applyHeartbeatEvent(event: HeartbeatEvent) {
  if (event.type === "started") {
    // Parse interval from message like "Heartbeat started with interval 1m0s"
    const match = event.message?.match(/interval\s+(\d+)([smh])/);
    if (match) {
      const value = parseInt(match[1], 10);
      const unit = match[2];
      let ms = 0;
      if (unit === "s") ms = value * 1000;
      else if (unit === "m") ms = value * 60_000;
      else if (unit === "h") ms = value * 3_600_000;
      state.heartbeatIntervals.set(event.workspaceId, ms);
    }
    pushToast(`Heartbeat started for ${event.workspaceId}.`, "info");
  } else if (event.type === "stopped") {
    state.heartbeatIntervals.delete(event.workspaceId);
    pushToast("Heartbeat stopped.", "info");
  } else if (event.type === "tick_no_eligible") {
    // Silent: heartbeat ticked but no eligible cards found.
    return;
  }
  render();
}

export function applyLivenessEvent(event: LivenessEvent) {
  if (event.type === "check_no_stalls") {
    // Silent: liveness check passed with no stalls.
    return;
  }
  if (event.type === "stalled_reset") {
    pushToast(`Card ${event.cardId} was stalled and auto-reset to Ready.`, "info");
  } else if (event.type === "stalled_escalated") {
    pushToast(`Card ${event.cardId} was escalated to Blocked after repeated stalls.`, "error");
  } else if (event.type === "stalled_reset_board" || event.type === "stalled_escalated_board") {
    // Board update from liveness — reload the board for this workspace.
    void loadActiveKanbanBoard();
    return;
  }
  render();
}

export async function toggleHeartbeatInterval(workspaceID: string) {
  const current = state.heartbeatIntervals.get(workspaceID) ?? 0;
  const next = nextHeartbeatInterval(current);

  try {
    if (next === 0) {
      await StopHeartbeat(workspaceID);
      state.heartbeatIntervals.delete(workspaceID);
    } else {
      const cfg = services.HeartbeatConfig.createFrom({ enabled: true, interval: next });
      await StartHeartbeat(workspaceID, cfg);
      state.heartbeatIntervals.set(workspaceID, next);
    }
  } catch (error) {
    pushToast(errorMessage(error), "error");
  }
  getAppCallbacks().render();
}

export function getHeartbeatInterval(workspaceID: string): number {
  return state.heartbeatIntervals.get(workspaceID) ?? 0;
}

/* ── Watchdog intervals ── */

const WatchdogIntervals = [0, 300_000, 900_000, 1_800_000, 3_600_000] as const; // off, 5m, 15m, 30m, 1h
type WatchdogInterval = (typeof WatchdogIntervals)[number];

export function watchdogIntervalLabel(intervalMs: number): string {
  if (intervalMs === 0) return "Off";
  const minutes = intervalMs / 60_000;
  if (minutes < 60) return `${minutes}m`;
  return `${minutes / 60}h`;
}

export function nextWatchdogInterval(currentMs: number): WatchdogInterval {
  const idx = WatchdogIntervals.indexOf(currentMs as WatchdogInterval);
  const nextIdx = (idx + 1) % WatchdogIntervals.length;
  return WatchdogIntervals[nextIdx];
}

export function getWatchdogInterval(workspaceID: string): number {
  return state.watchdogIntervals.get(workspaceID) ?? 0;
}

export async function toggleWatchdogInterval(workspaceID: string) {
  const current = state.watchdogIntervals.get(workspaceID) ?? 0;
  const next = nextWatchdogInterval(current);

  try {
    if (next === 0) {
      await StopWatchdog(workspaceID);
      state.watchdogIntervals.delete(workspaceID);
    } else {
      const cfg = services.WatchdogConfig.createFrom({ enabled: true, interval: next });
      await StartWatchdog(workspaceID, cfg);
      state.watchdogIntervals.set(workspaceID, next);
    }
  } catch (error) {
    pushToast(errorMessage(error), "error");
  }
  getAppCallbacks().render();
}

export function applyWatchdogEvent(event: WatchdogEvent) {
  if (event.type === "started") {
    // Parse interval from message like "Watchdog started with interval 5m0s"
    const match = event.message?.match(/interval\s+(\d+)([smh])/);
    if (match) {
      const value = parseInt(match[1], 10);
      const unit = match[2];
      let ms = 0;
      if (unit === "s") ms = value * 1000;
      else if (unit === "m") ms = value * 60_000;
      else if (unit === "h") ms = value * 3_600_000;
      state.watchdogIntervals.set(event.workspaceId, ms);
    }
    pushToast(`Watchdog started for ${event.workspaceId}.`, "info");
  } else if (event.type === "stopped") {
    state.watchdogIntervals.delete(event.workspaceId);
    pushToast("Watchdog stopped.", "info");
  } else if (event.type === "check_complete") {
    pushToast(`Watchdog check: ${event.message ?? "complete"}`, event.message?.includes("failed") ? "error" : "success");
  } else if (event.type === "repair_created") {
    pushToast(`Repair card created: ${event.cardId}`, "warning" as Toast["tone"]);
  }
  render();
}

export function isRepairCard(card: services.KanbanCard): boolean {
  return card.title?.startsWith("Repair: ") ?? false;
}

export function renderRepairBadge(): string {
  return `
    <span class="repair-badge" role="status" aria-label="Verification repair">
      ${icons.search}
      <span>Repair</span>
    </span>
  `;
}
