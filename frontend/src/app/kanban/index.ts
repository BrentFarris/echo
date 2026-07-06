
import { patchChildrenFromHtml, renderMarkdown } from "../../markdown";
import { AddKanbanCardMessage, CloseKanbanCardDetail, CreateKanbanCardFromTask, CreateReadyKanbanCard, LoadKanbanBoard, UpdateKanbanCardDescription, UpdateKanbanCardDirection } from "../../backend/services";
import { services } from "../../../wailsjs/go/models";
import { getAppCallbacks } from "../callbacks";
import { renderSpinnerLabel } from "../components";
import { appRoot, isElementScrolledNearBottom } from "../dom";
import { icons } from "../icons";
import { playNotificationSound } from "../notifications";
import { activeWorkspace, kanbanBoardFor, kanbanCards, selectedKanbanCard, state } from "../state";
import { pushToast } from "../toasts";
import type { KanbanEvent } from "../types";
import { errorMessage, escapeAttribute, escapeHtml, laneLabel } from "../utils";
import { refreshWorkspaceChangeReview } from "../changes";

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
  const criteria = card.acceptanceCriteria ?? [];
  const dependencies = card.dependencies ?? [];
  const dependencyStatuses = card.dependencyStatuses ?? [];
  const blockedBy = card.blockedBy ?? [];
  const unavailable = card.lane === "ready" && !card.eligible;
  return `
    <article class="kanban-card ${unavailable ? "is-unavailable" : ""}">
      <button
        class="kanban-card-open"
        type="button"
        data-action="open-card"
        data-card-id="${escapeAttribute(card.id)}"
        aria-label="Open ${escapeAttribute(card.title)} details"
      >
        <header>
          <strong>${escapeHtml(card.title)}</strong>
          <span>${escapeHtml(card.id)}</span>
        </header>
        <p>${escapeHtml(card.description)}</p>
        ${
          criteria.length
            ? `<ul>${criteria.map((item) => `<li>${escapeHtml(item)}</li>`).join("")}</ul>`
            : ""
        }
        ${
          dependencies.length
            ? `<div class="card-dependencies">${blockedBy.length ? "Waiting on" : "After"} ${
                dependencyStatuses.length
                  ? dependencyStatuses
                      .map(
                        (dependency) =>
                          `${escapeHtml(dependency.title || dependency.id)} (${escapeHtml(laneLabel(dependency.status))})`,
                      )
                      .join(", ")
                  : dependencies.map(escapeHtml).join(", ")
              }</div>`
            : ""
        }
      </button>
    </article>
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
    if (event.type === "card_progress" && patchOpenCardProgress(event)) {
      return;
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
