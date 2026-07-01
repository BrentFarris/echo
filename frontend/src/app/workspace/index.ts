
import { ReorderWorkspaces } from "../../backend/services";
import { services } from "../../../wailsjs/go/models";
import { getAppCallbacks } from "../callbacks";
import { icons } from "../icons";
import { state } from "../state";
import { pushToast } from "../toasts";
import { errorMessage, escapeAttribute, escapeHtml, workspaceFolderStatus, workspaceFolderSummary } from "../utils";

let draggingWorkspaceID = "";
let suppressWorkspaceClickUntil = 0;

export function workspaceLetter(workspace: services.Workspace): string {
  return (workspace.letter ?? "").trim() || workspace.displayName.slice(0, 1).toUpperCase() || "W";
}

export function renderWorkspaceIcon(workspace: services.Workspace): string {
  const iconURL = (workspace.iconUrl ?? "").trim();
  if (iconURL) {
    return `<img class="workspace-icon-image" src="${escapeAttribute(iconURL)}" alt="">`;
  }
  return `<span class="workspace-icon-label">${escapeHtml(workspaceLetter(workspace))}</span>`;
}

export function workspaceLetterDraft(workspace: services.Workspace): string {
  return state.workspaceLetterDrafts.get(workspace.id) ?? (workspace.letter ?? "");
}

export function hydrateWorkspaceLetterDrafts(workspaces: services.Workspace[]) {
  state.workspaceLetterDrafts.clear();
  workspaces.forEach((workspace) => {
    state.workspaceLetterDrafts.set(workspace.id, workspace.letter ?? "");
  });
}

export function bindWorkspaceDragEvents(root: ParentNode) {
  const rail = root.querySelector<HTMLElement>("[data-workspace-rail]");
  if (!rail) {
    return;
  }
  rail.addEventListener("dragover", handleWorkspaceDragOver);
  rail.addEventListener("dragleave", handleWorkspaceDragLeave);
  rail.addEventListener("drop", handleWorkspaceDrop);
  rail.addEventListener(
    "click",
    (event) => {
      if (Date.now() > suppressWorkspaceClickUntil) {
        return;
      }
      event.preventDefault();
      event.stopImmediatePropagation();
    },
    true,
  );
  rail
    .querySelectorAll<HTMLElement>("[data-workspace-drag-item]")
    .forEach((button) => {
      button.addEventListener("dragstart", handleWorkspaceDragStart);
      button.addEventListener("dragend", handleWorkspaceDragEnd);
    });
}

function handleWorkspaceDragStart(event: DragEvent) {
  const item = event.currentTarget as HTMLElement;
  const workspaceID = item.dataset.workspaceId ?? "";
  if (!workspaceID || !event.dataTransfer) {
    event.preventDefault();
    return;
  }
  draggingWorkspaceID = workspaceID;
  event.dataTransfer.effectAllowed = "move";
  event.dataTransfer.setData("text/plain", workspaceID);
  item.classList.add("is-dragging");
}

function handleWorkspaceDragOver(event: DragEvent) {
  if (!draggingWorkspaceID) {
    return;
  }
  event.preventDefault();
  if (event.dataTransfer) {
    event.dataTransfer.dropEffect = "move";
  }
  const rail = event.currentTarget as HTMLElement;
  const target = workspaceDragTarget(event.target);
  clearWorkspaceDropMarkers(rail);
  if (!target || target.dataset.workspaceId === draggingWorkspaceID) {
    return;
  }
  const side = workspaceDropSide(rail, target, event);
  target.classList.add(side === "before" ? "is-drop-before" : "is-drop-after");
}

function handleWorkspaceDragLeave(event: DragEvent) {
  const rail = event.currentTarget as HTMLElement;
  const related = event.relatedTarget;
  if (related instanceof Node && rail.contains(related)) {
    return;
  }
  clearWorkspaceDropMarkers(rail);
}

function handleWorkspaceDrop(event: DragEvent) {
  if (!draggingWorkspaceID) {
    return;
  }
  event.preventDefault();
  suppressWorkspaceClickUntil = Date.now() + 300;
  const rail = event.currentTarget as HTMLElement;
  const target = workspaceDragTarget(event.target);
  const targetID = target?.dataset.workspaceId ?? "";
  const side = target ? workspaceDropSide(rail, target, event) : "after";
  clearWorkspaceDropMarkers(rail);

  const currentOrder = state.appState?.workspaces?.map((workspace) => workspace.id) ?? [];
  const nextOrder = reorderedWorkspaceIDs(currentOrder, draggingWorkspaceID, targetID, side);
  if (!nextOrder || sameStringOrder(currentOrder, nextOrder)) {
    return;
  }
  draggingWorkspaceID = "";
  void persistWorkspaceOrder(nextOrder);
}

function handleWorkspaceDragEnd(event: DragEvent) {
  const item = event.currentTarget as HTMLElement;
  item.classList.remove("is-dragging");
  const rail = item.closest<HTMLElement>("[data-workspace-rail]");
  if (rail) {
    clearWorkspaceDropMarkers(rail);
  }
  if (draggingWorkspaceID) {
    suppressWorkspaceClickUntil = Date.now() + 300;
  }
  draggingWorkspaceID = "";
}

function workspaceDragTarget(target: EventTarget | null): HTMLElement | null {
  return target instanceof HTMLElement
    ? target.closest<HTMLElement>("[data-workspace-drag-item]")
    : null;
}

function workspaceDropSide(rail: HTMLElement, target: HTMLElement, event: DragEvent): "before" | "after" {
  const rect = target.getBoundingClientRect();
  if (getComputedStyle(rail).display === "flex") {
    return event.clientX < rect.left + rect.width / 2 ? "before" : "after";
  }
  return event.clientY < rect.top + rect.height / 2 ? "before" : "after";
}

function clearWorkspaceDropMarkers(root: ParentNode) {
  root
    .querySelectorAll<HTMLElement>(".workspace-button.is-drop-before, .workspace-button.is-drop-after")
    .forEach((item) => item.classList.remove("is-drop-before", "is-drop-after"));
}

function reorderedWorkspaceIDs(
  currentOrder: string[],
  sourceID: string,
  targetID: string,
  side: "before" | "after",
): string[] | null {
  if (!currentOrder.includes(sourceID)) {
    return null;
  }
  const next = currentOrder.filter((id) => id !== sourceID);
  if (!targetID || targetID === sourceID) {
    next.push(sourceID);
    return next;
  }
  const targetIndex = next.indexOf(targetID);
  if (targetIndex < 0) {
    return null;
  }
  next.splice(side === "after" ? targetIndex + 1 : targetIndex, 0, sourceID);
  return next;
}

function sameStringOrder(left: string[], right: string[]): boolean {
  return left.length === right.length && left.every((value, index) => value === right[index]);
}

async function persistWorkspaceOrder(nextOrder: string[]) {
  const appState = state.appState;
  const previousWorkspaces = appState?.workspaces ?? [];
  if (!appState || !previousWorkspaces.length) {
    return;
  }
  const byID = new Map(previousWorkspaces.map((workspace) => [workspace.id, workspace]));
  const nextWorkspaces = nextOrder
    .map((id) => byID.get(id))
    .filter((workspace): workspace is services.Workspace => Boolean(workspace));
  if (nextWorkspaces.length !== previousWorkspaces.length) {
    return;
  }

  state.appState = services.AppState.createFrom({
    ...appState,
    workspaces: nextWorkspaces,
  });
  getAppCallbacks().render();

  try {
    state.appState = await ReorderWorkspaces(nextOrder);
    getAppCallbacks().render();
  } catch (error) {
    state.appState = services.AppState.createFrom({
      ...appState,
      workspaces: previousWorkspaces,
    });
    pushToast(errorMessage(error), "error");
    getAppCallbacks().render();
  }
}

export function renderMissingWorkspace(workspace: services.Workspace): string {
  return `
    <section class="missing-panel" aria-labelledby="missing-title">
      <div>
        <p class="eyebrow">Workspace unavailable</p>
        <h2 id="missing-title">Folder missing</h2>
      </div>
      <p>${escapeHtml(workspace.error || "Echo cannot find one or more workspace folders.")}</p>
      ${(workspace.folders ?? [])
        .map((folder) => `<code>${escapeHtml(folder.label)}: ${escapeHtml(folder.path)}</code>`)
        .join("")}
      <div class="missing-actions">
        <button class="primary-button icon-text-button" type="button" data-action="refresh-workspaces">
          ${icons.refresh}
          <span>Retry</span>
        </button>
        <button class="secondary-button" type="button" data-action="delete-workspace" data-workspace-id="${escapeHtml(workspace.id)}">Remove</button>
      </div>
    </section>
  `;
}

export function renderWorkspaceFolderSettings(workspace: services.Workspace): string {
  const folders = workspace.folders ?? [];
  const folderRows = folders.length
    ? folders
        .map(
          (folder) => `
            <div class="workspace-folder-row ${folder.missing ? "is-missing" : ""}">
              <div class="workspace-folder-main">
                <strong>${escapeHtml(folder.label)}${folder.missing ? " - Missing" : ""}</strong>
                <span>${escapeHtml(folder.path)}</span>
                <small>${escapeHtml(workspaceFolderStatus(folder))}</small>
              </div>
              <label class="settings-toggle workspace-folder-agents">
                <span>Use AGENTS.md</span>
                <input
                  type="checkbox"
                  ${folder.useAgents ? "checked" : ""}
                  data-workspace-folder-agents
                  data-workspace-id="${escapeAttribute(workspace.id)}"
                  data-folder-id="${escapeAttribute(folder.id)}"
                />
              </label>
              <button class="icon-button danger-button" type="button" title="Remove folder" aria-label="Remove ${escapeAttribute(folder.label)}" data-action="remove-workspace-folder" data-workspace-id="${escapeAttribute(workspace.id)}" data-folder-id="${escapeAttribute(folder.id)}">
                ${icons.trash}
              </button>
            </div>
          `,
        )
        .join("")
    : `<p class="empty-state compact">Blank workspace.</p>`;

  return `
    <div class="workspace-folder-list">
      ${folderRows}
      <button class="secondary-button icon-text-button" type="button" data-action="add-workspace-folder" data-workspace-id="${escapeAttribute(workspace.id)}">
        ${icons.plus}
        <span>Add folder</span>
      </button>
    </div>
  `;
}
