import {
  CreateWorkspaceTask,
  DeleteWorkspaceTask,
  LoadTaskBoard,
  MoveWorkspaceTask,
  SetWorkspaceTaskCompleted,
  UpdateWorkspaceTask,
} from "../../backend/services";
import { services } from "../../../wailsjs/go/models";
import { renderMarkdown } from "../../markdown";
import { appRoot } from "../dom";
import { getAppCallbacks } from "../callbacks";
import { icons } from "../icons";
import { activeWorkspace, state, taskBoardFor } from "../state";
import { pushToast } from "../toasts";
import type { TaskEvent } from "../types";
import { errorMessage, escapeAttribute, escapeHtml } from "../utils";

const priorities = ["P0", "P1", "P2"] as const;
let draggingTaskID = "";

export async function loadActiveTaskBoard() {
  const workspace = activeWorkspace();
  if (!workspace) return false;
  try {
    state.taskBoards.set(workspace.id, await LoadTaskBoard(workspace.id));
    return true;
  } catch (error) {
    pushToast(errorMessage(error), "error");
    return false;
  }
}

export function applyTaskEvent(event: TaskEvent) {
  if (!event.workspaceId || !event.board) return;
  state.taskBoards.set(event.workspaceId, services.TaskBoard.createFrom(event.board));
  if (activeWorkspace()?.id === event.workspaceId && state.appMode === "tasks") {
    getAppCallbacks().render();
  }
}

export function renderTaskPanel(workspace: services.Workspace): string {
  const board = taskBoardFor(workspace.id);
  const showCompleted = state.showCompletedTaskWorkspaces.has(workspace.id);
  const visible = (board.tasks ?? []).filter((task) => showCompleted || !task.completed);
  return `
    <section class="work-panel task-panel" aria-labelledby="tasks-title">
      <div class="panel-heading">
        <div class="kanban-heading-main">
          <span>Tasks</span>
          <strong id="tasks-title">Backlog</strong>
          <small class="task-storage-path">Active: ${escapeHtml(board.storagePath || ".echo/tasks.json")}</small>
          <small class="task-storage-path">Completed: ${escapeHtml(board.doneStoragePath || ".echo/tasks_done.json")}</small>
        </div>
        <div class="task-heading-actions">
          <label class="task-completed-toggle">
            <input type="checkbox" data-task-action="toggle-completed" ${showCompleted ? "checked" : ""}>
            <span>Show completed</span>
          </label>
          <button class="secondary-button icon-text-button" type="button" data-task-action="refresh">
            ${icons.refresh}<span>Refresh</span>
          </button>
        </div>
      </div>
      ${board.gitIgnored || board.doneGitIgnored ? `
        <div class="task-git-warning" role="status">
          ${escapeHtml([
            board.gitIgnored ? board.storagePath || ".echo/tasks.json" : "",
            board.doneGitIgnored ? board.doneStoragePath || ".echo/tasks_done.json" : "",
          ].filter(Boolean).join(" and "))} ${board.gitIgnored && board.doneGitIgnored ? "are" : "is"} ignored by Git. Echo will not change your ignore rules.
        </div>
      ` : ""}
      <div class="task-board" aria-label="Backlog priority columns">
        ${priorities.map((priority) => renderTaskLane(priority, visible.filter((task) => task.priority === priority))).join("")}
      </div>
    </section>
    ${renderTaskEditor(workspace.id)}
  `;
}

function renderTaskLane(priority: string, tasks: services.WorkspaceTask[]): string {
  return `
    <section class="task-lane" data-task-lane="${priority}" aria-label="${priority} tasks">
      <header>
        <div><strong>${priority}</strong><span>${priority === "P0" ? "Highest" : priority === "P1" ? "Normal" : "Lower"}</span></div>
        <span class="task-count">${tasks.length}</span>
      </header>
      <button class="task-lane-add" type="button" data-task-action="new" data-priority="${priority}">
        ${icons.plus}<span>Add task</span>
      </button>
      <div class="task-cards">
        ${tasks.length ? tasks.map(renderTaskCard).join("") : `<p class="lane-empty">No tasks</p>`}
      </div>
    </section>
  `;
}

function renderTaskCard(task: services.WorkspaceTask): string {
  const criteria = task.acceptanceCriteria ?? [];
  return `
    <article class="task-card${task.completed ? " is-completed" : ""}" draggable="true" data-task-drag-item data-task-id="${escapeAttribute(task.id)}">
      <header>
        <strong>${escapeHtml(task.title)}</strong>
        ${task.completed ? `<span class="task-complete-badge">${icons.check}</span>` : ""}
      </header>
      ${task.details ? `<div class="task-card-details markdown-body">${renderMarkdown(task.details)}</div>` : ""}
      ${criteria.length ? `<ul>${criteria.map((criterion) => `<li>${escapeHtml(criterion)}</li>`).join("")}</ul>` : ""}
      <label class="task-priority-control">
        <span>Priority</span>
        <select data-task-priority-select data-task-id="${escapeAttribute(task.id)}" data-updated-at="${escapeAttribute(task.updatedAt)}">
          ${priorities.map((priority) => `<option value="${priority}" ${task.priority === priority ? "selected" : ""}>${priority}</option>`).join("")}
        </select>
      </label>
      <div class="task-card-actions">
        <button class="icon-button" type="button" title="Edit task" aria-label="Edit ${escapeAttribute(task.title)}" data-task-action="edit" data-task-id="${escapeAttribute(task.id)}">${icons.edit}</button>
        <button class="icon-button" type="button" title="Use as chat prompt" aria-label="Use ${escapeAttribute(task.title)} as a chat prompt" data-task-action="chat" data-task-id="${escapeAttribute(task.id)}">${icons.chat}</button>
        ${task.completed ? "" : `<button class="icon-button" type="button" title="Convert to Kanban card" aria-label="Convert ${escapeAttribute(task.title)} to a Kanban card" data-task-action="kanban" data-task-id="${escapeAttribute(task.id)}">${icons.kanban}</button>`}
        <button class="icon-button" type="button" title="${task.completed ? "Reopen" : "Complete"} task" aria-label="${task.completed ? "Reopen" : "Complete"} ${escapeAttribute(task.title)}" data-task-action="complete" data-task-id="${escapeAttribute(task.id)}">${task.completed ? icons.undo : icons.check}</button>
        <button class="icon-button danger-button" type="button" title="Delete task" aria-label="Delete ${escapeAttribute(task.title)}" data-task-action="delete" data-task-id="${escapeAttribute(task.id)}">${icons.trash}</button>
      </div>
    </article>
  `;
}

function renderTaskEditor(workspaceID: string): string {
  const draft = state.taskEditorDrafts.get(workspaceID);
  if (!draft) return "";
  const editing = Boolean(draft.taskId);
  return `
    <aside class="kanban-card-create-backdrop" role="dialog" aria-modal="true" aria-labelledby="task-editor-title">
      <form class="kanban-card-create-dialog" data-task-editor-form>
        <header>
          <div><p class="eyebrow">${draft.priority} backlog</p><h2 id="task-editor-title">${editing ? "Edit task" : "Create task"}</h2></div>
          <button class="icon-button close-button" type="button" data-task-action="cancel-editor" aria-label="Cancel">${icons.x}</button>
        </header>
        <label><span>Title</span><input type="text" name="title" required value="${escapeAttribute(draft.title)}" data-task-title data-initial-focus></label>
        <label><span>Details</span><textarea name="details" rows="6" placeholder="Optional Markdown details" data-task-details>${escapeHtml(draft.details)}</textarea></label>
        <label><span>Acceptance criteria</span><textarea name="acceptanceCriteria" rows="4" placeholder="Optional; one criterion per line" data-task-criteria>${escapeHtml(draft.acceptanceCriteria)}</textarea></label>
        <label><span>Priority</span><select name="priority" data-task-priority>${priorities.map((priority) => `<option value="${priority}" ${draft.priority === priority ? "selected" : ""}>${priority}</option>`).join("")}</select></label>
        <div class="kanban-card-create-actions">
          <button class="secondary-button" type="button" data-task-action="cancel-editor">Cancel</button>
          <button class="primary-button icon-text-button" type="submit">${icons.check}<span>${editing ? "Save changes" : "Create task"}</span></button>
        </div>
      </form>
    </aside>
  `;
}

export function bindTaskEvents(root: ParentNode) {
  root.querySelectorAll<HTMLElement>("[data-task-action]").forEach((element) => {
    const eventName = element instanceof HTMLInputElement && element.type === "checkbox" ? "change" : "click";
    element.addEventListener(eventName, handleTaskAction);
  });
  root.querySelector<HTMLFormElement>("[data-task-editor-form]")?.addEventListener("submit", handleTaskEditorSubmit);
  root.querySelectorAll<HTMLSelectElement>("[data-task-priority-select]").forEach((select) => select.addEventListener("change", handlePrioritySelect));
  root.querySelectorAll<HTMLElement>("[data-task-drag-item]").forEach((card) => {
    card.addEventListener("dragstart", handleTaskDragStart);
    card.addEventListener("dragend", handleTaskDragEnd);
  });
  root.querySelectorAll<HTMLElement>("[data-task-lane]").forEach((lane) => {
    lane.addEventListener("dragover", handleTaskDragOver);
    lane.addEventListener("dragleave", handleTaskDragLeave);
    lane.addEventListener("drop", handleTaskDrop);
  });
}

async function handleTaskAction(event: Event) {
  const target = event.currentTarget as HTMLElement;
  const action = target.dataset.taskAction;
  const workspace = activeWorkspace();
  if (!workspace || !action) return;
  const board = taskBoardFor(workspace.id);
  const task = (board.tasks ?? []).find((candidate) => candidate.id === target.dataset.taskId);
  try {
    if (action === "toggle-completed") {
      const checked = (target as HTMLInputElement).checked;
      checked ? state.showCompletedTaskWorkspaces.add(workspace.id) : state.showCompletedTaskWorkspaces.delete(workspace.id);
      getAppCallbacks().render();
      return;
    }
    if (action === "refresh") {
      if (await loadActiveTaskBoard()) {
        pushToast("Backlog refreshed.", "success");
      }
      getAppCallbacks().render();
      return;
    }
    if (action === "new") {
      state.taskEditorDrafts.set(workspace.id, { title: "", details: "", acceptanceCriteria: "", priority: target.dataset.priority || "P1" });
      getAppCallbacks().render();
      return;
    }
    if (action === "cancel-editor") {
      state.taskEditorDrafts.delete(workspace.id);
      getAppCallbacks().render();
      return;
    }
    if (!task) return;
    if (action === "edit") {
      state.taskEditorDrafts.set(workspace.id, {
        taskId: task.id,
        title: task.title,
        details: task.details || "",
        acceptanceCriteria: (task.acceptanceCriteria ?? []).join("\n"),
        priority: task.priority,
        expectedUpdatedAt: task.updatedAt,
      });
      getAppCallbacks().render();
      return;
    }
    if (action === "complete") {
      state.taskBoards.set(workspace.id, await SetWorkspaceTaskCompleted(workspace.id, task.id, !task.completed, task.updatedAt));
      pushToast(task.completed ? "Task reopened." : "Task completed.", "success");
      getAppCallbacks().render();
      return;
    }
    if (action === "delete") {
      if (!window.confirm(`Delete "${task.title}"?`)) return;
      state.taskBoards.set(workspace.id, await DeleteWorkspaceTask(workspace.id, task.id, task.updatedAt));
      pushToast("Task deleted.", "success");
      getAppCallbacks().render();
      return;
    }
    if (action === "chat") {
      const prompt = taskChatPrompt(task);
      const existing = state.chatDrafts.get(workspace.id)?.trim() ?? "";
      if (existing && existing !== prompt.trim() && !window.confirm("Replace the current chat draft with this task?")) return;
      state.chatDrafts.set(workspace.id, prompt);
      state.appMode = "chat";
      state.mobileNavView = "chat";
      state.activeChatKanbanTab.set(workspace.id, "chat");
      getAppCallbacks().render();
      window.requestAnimationFrame(() => appRoot.querySelector<HTMLTextAreaElement>("[data-chat-input]")?.focus());
      return;
    }
    if (action === "kanban") {
      state.kanbanCardCreationDrafts.set(workspace.id, {
        title: task.title,
        description: task.details || "",
        acceptanceCriteria: (task.acceptanceCriteria ?? []).join("\n"),
        sourceTaskId: task.id,
        sourceTaskUpdatedAt: task.updatedAt,
      });
      state.creatingKanbanCardWorkspaces.add(workspace.id);
      getAppCallbacks().render();
    }
  } catch (error) {
    pushToast(errorMessage(error), "error");
  }
}

async function handleTaskEditorSubmit(event: SubmitEvent) {
  event.preventDefault();
  const workspace = activeWorkspace();
  if (!workspace) return;
  const draft = state.taskEditorDrafts.get(workspace.id);
  const form = event.currentTarget as HTMLFormElement;
  if (!draft) return;
  const title = form.querySelector<HTMLInputElement>("[data-task-title]")?.value.trim() ?? "";
  if (!title) return;
  const input = services.TaskInput.createFrom({
    title,
    details: form.querySelector<HTMLTextAreaElement>("[data-task-details]")?.value.trim() ?? "",
    acceptanceCriteria: (form.querySelector<HTMLTextAreaElement>("[data-task-criteria]")?.value ?? "").split(/\r?\n/).map((value) => value.trim()).filter(Boolean),
    priority: form.querySelector<HTMLSelectElement>("[data-task-priority]")?.value ?? "P1",
  });
  try {
    const board = draft.taskId
      ? await UpdateWorkspaceTask(workspace.id, draft.taskId, input, draft.expectedUpdatedAt || "")
      : await CreateWorkspaceTask(workspace.id, input);
    state.taskBoards.set(workspace.id, board);
    state.taskEditorDrafts.delete(workspace.id);
    pushToast(draft.taskId ? "Task updated." : "Task created.", "success");
    getAppCallbacks().render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
  }
}

async function handlePrioritySelect(event: Event) {
  const workspace = activeWorkspace();
  const select = event.currentTarget as HTMLSelectElement;
  if (!workspace) return;
  try {
    state.taskBoards.set(workspace.id, await MoveWorkspaceTask(workspace.id, select.dataset.taskId || "", select.value, select.dataset.updatedAt || ""));
    getAppCallbacks().render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    await loadActiveTaskBoard();
    getAppCallbacks().render();
  }
}

function handleTaskDragStart(event: DragEvent) {
  const card = event.currentTarget as HTMLElement;
  draggingTaskID = card.dataset.taskId || "";
  if (!draggingTaskID || !event.dataTransfer) return;
  event.dataTransfer.effectAllowed = "move";
  event.dataTransfer.setData("text/plain", draggingTaskID);
  card.classList.add("is-dragging");
}

function handleTaskDragOver(event: DragEvent) {
  if (!draggingTaskID) return;
  event.preventDefault();
  (event.currentTarget as HTMLElement).classList.add("is-drop-target");
  if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
}

function handleTaskDragLeave(event: DragEvent) {
  const lane = event.currentTarget as HTMLElement;
  if (event.relatedTarget instanceof Node && lane.contains(event.relatedTarget)) return;
  lane.classList.remove("is-drop-target");
}

async function handleTaskDrop(event: DragEvent) {
  event.preventDefault();
  const workspace = activeWorkspace();
  const lane = event.currentTarget as HTMLElement;
  lane.classList.remove("is-drop-target");
  const task = workspace ? (taskBoardFor(workspace.id).tasks ?? []).find((candidate) => candidate.id === draggingTaskID) : undefined;
  const priority = lane.dataset.taskLane || "";
  draggingTaskID = "";
  if (!workspace || !task || task.priority === priority) return;
  try {
    state.taskBoards.set(workspace.id, await MoveWorkspaceTask(workspace.id, task.id, priority, task.updatedAt));
    getAppCallbacks().render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    await loadActiveTaskBoard();
    getAppCallbacks().render();
  }
}

function handleTaskDragEnd(event: DragEvent) {
  (event.currentTarget as HTMLElement).classList.remove("is-dragging");
  appRoot.querySelectorAll(".task-lane.is-drop-target").forEach((lane) => lane.classList.remove("is-drop-target"));
  draggingTaskID = "";
}

function taskChatPrompt(task: services.WorkspaceTask): string {
  const parts = [`Task: ${task.title}`];
  if (task.details?.trim()) parts.push(`Details:\n${task.details.trim()}`);
  if ((task.acceptanceCriteria ?? []).length) parts.push(`Acceptance criteria:\n${(task.acceptanceCriteria ?? []).map((criterion) => `- ${criterion}`).join("\n")}`);
  return parts.join("\n\n");
}
