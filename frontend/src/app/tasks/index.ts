import {
  CreateKanbanCardFromTask,
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
  const searchQuery = state.taskSearchQuery.get(workspace.id) ?? "";
  const filterMode = state.taskFilterMode.get(workspace.id) ?? "open";
  const tasks = board.tasks ?? [];

  // Apply filter mode
  const filteredByMode = filterMode === "all"
    ? tasks
    : filterMode === "completed"
      ? tasks.filter((t) => t.completed)
      : tasks.filter((t) => !t.completed);

  // Apply search query (case-insensitive on title, details, acceptance criteria)
  const query = searchQuery.toLowerCase().trim();
  const visible = query
    ? filteredByMode.filter((task) => {
        if (task.title.toLowerCase().includes(query)) return true;
        if (task.details?.toLowerCase().includes(query)) return true;
        if ((task.acceptanceCriteria ?? []).some((c) => c.toLowerCase().includes(query))) return true;
        return false;
      })
    : filteredByMode;

  return `
    <section class="work-panel task-panel" aria-labelledby="tasks-title" data-task-panel>
      <div class="panel-heading">
        <div class="kanban-heading-main">
          <span>Tasks</span>
          <strong id="tasks-title">Backlog</strong>
          <small class="task-storage-path">Active: ${escapeHtml(board.storagePath || ".echo/tasks.json")}</small>
          <small class="task-storage-path">Completed: ${escapeHtml(board.doneStoragePath || ".echo/tasks_done.json")}</small>
        </div>
        <div class="task-heading-actions">
          <button type="button" class="icon-button view-dashboard-button" title="View Tasks dashboard" aria-label="Tasks dashboard" data-action="open-view-dashboard" data-view="tasks">${icons.dashboard}</button>
          <label class="task-completed-toggle">
            <input type="checkbox" data-task-action="toggle-completed" ${filterMode === "all" || filterMode === "completed" ? "checked" : ""}>
            <span>Show completed</span>
          </label>
          <button class="secondary-button icon-text-button" type="button" data-task-action="refresh">
            ${icons.refresh}<span>Refresh</span>
          </button>
        </div>
      </div>
      <div class="task-search-bar">
        <input type="search" class="task-search-input" placeholder="Search tasks…" value="${escapeAttribute(searchQuery)}" data-task-search>
        <div class="task-filter-buttons" role="group" aria-label="Task filter mode">
          <button class="task-filter-btn${filterMode === "all" ? " is-active" : ""}" type="button" data-task-filter="all">All</button>
          <button class="task-filter-btn${filterMode === "open" ? " is-active" : ""}" type="button" data-task-filter="open">Open</button>
          <button class="task-filter-btn${filterMode === "completed" ? " is-active" : ""}" type="button" data-task-filter="completed">Completed</button>
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
  const priorityClass = `priority-${task.priority.toLowerCase()}`;
  const isCompleted = task.completed;
  let statusBadge = "";
  if (isCompleted) {
    statusBadge = '<span class="task-status-badge task-status-converted">→ card ✓</span>';
  } else {
    statusBadge = '<span class="task-status-badge task-status-backlog">backlog</span>';
  }
  return `
    <article class="task-card${isCompleted ? " is-completed" : ""}" draggable="true" data-task-drag-item data-task-id="${escapeAttribute(task.id)}">
      <button
        class="task-card-open"
        type="button"
        data-task-action="open-task"
        data-task-id="${escapeAttribute(task.id)}"
        aria-label="Open ${escapeAttribute(task.title)} details"
      >
        <span class="task-card-header">
          <span class="priority-badge ${priorityClass}" aria-label="${task.priority} priority">${task.priority}</span>
          <strong>${escapeHtml(task.title)}</strong>
          ${statusBadge}
        </span>
      </button>
    </article>
  `;
}

export function renderTaskDetail(workspace: services.Workspace): string {
  const selectedID = state.selectedTaskCards.get(workspace.id);
  if (!selectedID) return "";
  const board = taskBoardFor(workspace.id);
  const task = (board.tasks ?? []).find((t) => t.id === selectedID);
  if (!task) return "";

  const criteria = task.acceptanceCriteria ?? [];
  const priorityClass = `priority-${task.priority.toLowerCase()}`;
  const createdDate = task.createdAt
    ? new Date(task.createdAt).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" })
    : "";

  return `
    <aside class="card-detail-backdrop" data-task-detail-backdrop role="dialog" aria-modal="true" aria-labelledby="task-detail-title">
      <section class="card-detail" data-task-detail>
        <header class="card-detail-header">
          <div class="card-detail-heading-row">
            <div>
              <p class="eyebrow">${escapeHtml(task.id)} - ${task.completed ? "Completed" : "Backlog"}</p>
              <h2 id="task-detail-title">${escapeHtml(task.title)}</h2>
            </div>
            <button class="icon-button close-button" type="button" title="Close" aria-label="Close task details" data-task-action="close-task">
              ${icons.x}
            </button>
          </div>

          <div class="task-detail-meta">
            <span class="priority-badge ${priorityClass}" aria-label="${task.priority} priority">${task.priority}</span>
            ${createdDate ? `<span class="task-detail-created">Created ${escapeHtml(createdDate)}</span>` : ""}
            ${task.completed ? `<span class="task-detail-status completed">Completed ✓</span>` : `<span class="task-detail-status backlog">Backlog</span>`}
          </div>

          <div class="card-detail-actions">
            <button class="secondary-button icon-text-button" type="button" data-task-action="edit" data-task-id="${escapeAttribute(task.id)}">
              ${icons.edit}
              <span>Edit</span>
            </button>
            ${!task.completed ? `
              <button class="secondary-button icon-text-button" type="button" data-task-action="kanban" data-task-id="${escapeAttribute(task.id)}">
                ${icons.kanban}
                <span>Convert to Kanban</span>
              </button>
            ` : ""}
            <button class="secondary-button icon-text-button" type="button" data-task-action="cycle-priority" data-task-id="${escapeAttribute(task.id)}">
              ${icons.refresh}
              <span>Change Priority</span>
            </button>
            <button class="secondary-button icon-text-button danger-button" type="button" data-task-action="delete" data-task-id="${escapeAttribute(task.id)}">
              ${icons.trash}
              <span>Delete</span>
            </button>
          </div>
        </header>

        ${task.details ? `
          <section class="detail-section">
            <h3>Description</h3>
            <div class="markdown-body">${renderMarkdown(task.details)}</div>
          </section>
        ` : ""}

        <section class="detail-section">
          <h3>Acceptance Criteria</h3>
          ${criteria.length
            ? `<ul>${criteria.map((criterion) => `<li>${escapeHtml(criterion)}</li>`).join("")}</ul>`
            : `<p>No acceptance criteria recorded.</p>`}
        </section>

        <div class="task-detail-footer-actions">
          <button class="secondary-button icon-text-button" type="button" data-task-action="complete" data-task-id="${escapeAttribute(task.id)}">
            ${task.completed ? icons.undo : icons.check}
            <span>${task.completed ? "Reopen" : "Complete"}</span>
          </button>
          <button class="secondary-button icon-text-button" type="button" data-task-action="chat" data-task-id="${escapeAttribute(task.id)}">
            ${icons.chat}
            <span>Use as Chat Prompt</span>
          </button>
        </div>
      </section>
    </aside>
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
    element.addEventListener("click", handleTaskAction);
  });
  root.querySelector<HTMLFormElement>("[data-task-editor-form]")?.addEventListener("submit", handleTaskEditorSubmit);
  // Backdrop click to close task detail
  const backdrop = root.querySelector<HTMLElement>("aside.card-detail-backdrop[data-task-detail-backdrop]");
  if (backdrop) {
    backdrop.addEventListener("click", (event) => {
      if (event.target === backdrop) {
        handleTaskDetailBackdropClick(backdrop);
      }
    });
  }
  root.querySelectorAll<HTMLElement>("[data-task-drag-item]").forEach((card) => {
    card.addEventListener("dragstart", handleTaskDragStart);
    card.addEventListener("dragend", handleTaskDragEnd);
  });
  root.querySelectorAll<HTMLElement>("[data-task-lane]").forEach((lane) => {
    lane.addEventListener("dragover", handleTaskDragOver);
    lane.addEventListener("dragleave", handleTaskDragLeave);
    lane.addEventListener("drop", handleTaskDrop);
  });
  root.querySelector<HTMLInputElement>("[data-task-search]")?.addEventListener("input", handleTaskSearch);
  root.querySelectorAll<HTMLButtonElement>("[data-task-filter]").forEach((btn) => {
    btn.addEventListener("click", handleTaskFilter);
  });
}

function handleTaskDetailBackdropClick(backdrop: HTMLElement) {
  const workspace = activeWorkspace();
  if (!workspace) return;
  state.selectedTaskCards.delete(workspace.id);
  getAppCallbacks().render();
}

async function handleTaskAction(event: Event) {
  const target = event.currentTarget as HTMLElement;
  const action = target.dataset.taskAction;
  const workspace = activeWorkspace();
  if (!workspace || !action) return;
  const board = taskBoardFor(workspace.id);
  const task = (board.tasks ?? []).find((candidate) => candidate.id === target.dataset.taskId);

  // Handle toggle-completed checkbox separately (it was previously handled via change event)
  if (action === "toggle-completed") {
    const checked = (target as HTMLInputElement).checked;
    state.taskFilterMode.set(workspace.id, checked ? "all" : "open");
    if (checked) {
      state.showCompletedTaskWorkspaces.add(workspace.id);
    } else {
      state.showCompletedTaskWorkspaces.delete(workspace.id);
    }
    getAppCallbacks().render();
    return;
  }

  try {
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
    if (action === "open-task") {
      const taskID = target.dataset.taskId ?? "";
      if (taskID) {
        state.selectedTaskCards.set(workspace.id, taskID);
      }
      getAppCallbacks().render();
      return;
    }
    if (action === "close-task") {
      state.selectedTaskCards.delete(workspace.id);
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
      // If completed and was selected, keep selection if still exists
      getAppCallbacks().render();
      return;
    }
    if (action === "delete") {
      if (!window.confirm(`Delete "${task.title}"?`)) return;
      state.taskBoards.set(workspace.id, await DeleteWorkspaceTask(workspace.id, task.id, task.updatedAt));
      state.selectedTaskCards.delete(workspace.id);
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
      state.selectedTaskCards.delete(workspace.id);
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
      state.selectedTaskCards.delete(workspace.id);
      getAppCallbacks().render();
      return;
    }
    if (action === "cycle-priority") {
      const currentIdx = priorities.indexOf(task.priority as (typeof priorities)[number]);
      const nextIdx = (currentIdx + 1) % priorities.length;
      const nextPriority = priorities[nextIdx];
      state.taskBoards.set(workspace.id, await MoveWorkspaceTask(workspace.id, task.id, nextPriority, task.updatedAt));
      pushToast(`Priority changed to ${nextPriority}.`, "success");
      getAppCallbacks().render();
      return;
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

function handleTaskSearch(event: Event) {
  const workspace = activeWorkspace();
  if (!workspace) return;
  const input = event.currentTarget as HTMLInputElement;
  state.taskSearchQuery.set(workspace.id, input.value);
  patchTaskPanel();
}

function handleTaskFilter(event: Event) {
  const workspace = activeWorkspace();
  if (!workspace) return;
  const btn = event.currentTarget as HTMLButtonElement;
  const mode = btn.dataset.taskFilter as "all" | "open" | "completed";
  state.taskFilterMode.set(workspace.id, mode);
  if (mode === "all" || mode === "completed") {
    state.showCompletedTaskWorkspaces.add(workspace.id);
  } else {
    state.showCompletedTaskWorkspaces.delete(workspace.id);
  }
  patchTaskPanel();
}

function patchTaskPanel() {
  const workspace = activeWorkspace();
  if (!workspace) return;
  const panel = appRoot.querySelector<HTMLElement>("[data-task-panel]");
  if (!panel) return;
  const board = taskBoardFor(workspace.id);
  const searchQuery = state.taskSearchQuery.get(workspace.id) ?? "";
  const filterMode = state.taskFilterMode.get(workspace.id) ?? "open";
  const tasks = board.tasks ?? [];

  // Apply filter mode
  const filteredByMode = filterMode === "all"
    ? tasks
    : filterMode === "completed"
      ? tasks.filter((t) => t.completed)
      : tasks.filter((t) => !t.completed);

  // Apply search query
  const query = searchQuery.toLowerCase().trim();
  const visible = query
    ? filteredByMode.filter((task) => {
        if (task.title.toLowerCase().includes(query)) return true;
        if (task.details?.toLowerCase().includes(query)) return true;
        if ((task.acceptanceCriteria ?? []).some((c) => c.toLowerCase().includes(query))) return true;
        return false;
      })
    : filteredByMode;

  // Patch task-board lanes in-place
  const taskBoard = panel.querySelector<HTMLElement>(".task-board");
  if (taskBoard) {
    const html = priorities.map((priority) => renderTaskLane(priority, visible.filter((task) => task.priority === priority))).join("");
    const template = document.createElement("template");
    template.innerHTML = html;
    taskBoard.replaceChildren(...Array.from(template.content.children));
  }

  // Re-bind events on the patched panel
  bindTaskEvents(panel);
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
