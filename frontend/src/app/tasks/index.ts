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
  const selectedID = state.selectedTaskIds.get(event.workspaceId);
  if (selectedID && !(event.board.tasks ?? []).some((task) => task.id === selectedID)) {
    state.selectedTaskIds.delete(event.workspaceId);
    state.taskInlineEdits.delete(event.workspaceId);
  }
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
    ${renderTaskDetail(workspace.id)}
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
  return `
    <article class="task-card${task.completed ? " is-completed" : ""}" draggable="true" data-task-drag-item data-task-open-card data-task-id="${escapeAttribute(task.id)}" tabindex="0" aria-label="Open task ${escapeAttribute(task.title)}">
      <header>
        <strong>${escapeHtml(task.title)}</strong>
        ${task.completed ? `<span class="task-complete-badge">${icons.check}</span>` : ""}
      </header>
      <div class="task-card-actions">
        <button class="icon-button" type="button" title="Open task" aria-label="Open ${escapeAttribute(task.title)}" data-task-action="open" data-task-id="${escapeAttribute(task.id)}">${icons.edit}</button>
        <button class="icon-button" type="button" title="Use as chat prompt" aria-label="Use ${escapeAttribute(task.title)} as a chat prompt" data-task-action="chat" data-task-id="${escapeAttribute(task.id)}">${icons.chat}</button>
        ${task.completed ? "" : `<button class="icon-button" type="button" title="Convert to Kanban card" aria-label="Convert ${escapeAttribute(task.title)} to a Kanban card" data-task-action="kanban" data-task-id="${escapeAttribute(task.id)}">${icons.kanban}</button>`}
        <button class="icon-button" type="button" title="${task.completed ? "Reopen" : "Complete"} task" aria-label="${task.completed ? "Reopen" : "Complete"} ${escapeAttribute(task.title)}" data-task-action="complete" data-task-id="${escapeAttribute(task.id)}">${task.completed ? icons.undo : icons.check}</button>
        <button class="icon-button danger-button" type="button" title="Delete task" aria-label="Delete ${escapeAttribute(task.title)}" data-task-action="delete" data-task-id="${escapeAttribute(task.id)}">${icons.trash}</button>
      </div>
    </article>
  `;
}

function renderTaskDetail(workspaceID: string): string {
  const taskID = state.selectedTaskIds.get(workspaceID);
  const task = taskID ? (taskBoardFor(workspaceID).tasks ?? []).find((candidate) => candidate.id === taskID) : null;
  if (!task) return "";
  const edit = state.taskInlineEdits.get(workspaceID);
  const editing = edit?.taskId === task.id ? edit.field : "";
  const criteriaText = (task.acceptanceCriteria ?? []).join("\n");
  return `
    <aside class="task-detail-backdrop" role="dialog" aria-modal="true" aria-labelledby="task-detail-title" data-task-detail>
      <article class="task-detail-dialog">
        <header class="task-detail-header">
          <div class="task-detail-kicker">
            <span>${escapeHtml(task.priority)}</span>
            <span>${task.completed ? "Completed" : "Open"}</span>
          </div>
          <button class="icon-button close-button" type="button" data-task-action="close-detail" aria-label="Close task">${icons.x}</button>
        </header>
        <section class="task-detail-content">
          <div class="task-detail-editable task-detail-title-field" data-task-inline-field="title" data-task-id="${escapeAttribute(task.id)}" data-updated-at="${escapeAttribute(task.updatedAt)}" role="button" tabindex="0" aria-label="Edit title">
            ${editing === "title"
              ? `<input id="task-detail-title" class="task-inline-title-input" type="text" value="${escapeAttribute(task.title)}" data-task-inline-input data-task-inline-kind="title" data-task-id="${escapeAttribute(task.id)}" data-updated-at="${escapeAttribute(task.updatedAt)}">`
              : `<h1 id="task-detail-title">${escapeHtml(task.title)}</h1>`}
          </div>
          <div class="task-detail-meta">
            <div class="task-priority-pill" data-task-inline-field="priority" data-task-id="${escapeAttribute(task.id)}" data-updated-at="${escapeAttribute(task.updatedAt)}" role="button" tabindex="0" aria-label="Edit priority">
              ${editing === "priority"
                ? `<select data-task-inline-input data-task-inline-kind="priority" data-task-id="${escapeAttribute(task.id)}" data-updated-at="${escapeAttribute(task.updatedAt)}">
                    ${priorities.map((priority) => `<option value="${priority}" ${task.priority === priority ? "selected" : ""}>${priority}</option>`).join("")}
                  </select>`
                : `<span>${escapeHtml(task.priority)}</span>`}
            </div>
            <span>Updated ${escapeHtml(formatTaskDate(task.updatedAt))}</span>
            ${task.completedAt ? `<span>Completed ${escapeHtml(formatTaskDate(task.completedAt))}</span>` : ""}
          </div>
          ${renderTaskDetailField("details", "Details", task.details || "", editing, task.id, task.updatedAt)}
          ${renderTaskDetailField("acceptanceCriteria", "Acceptance criteria", criteriaText, editing, task.id, task.updatedAt)}
        </section>
        <footer class="task-detail-actions">
          <button class="secondary-button icon-text-button" type="button" data-task-action="chat" data-task-id="${escapeAttribute(task.id)}">${icons.chat}<span>Use as prompt</span></button>
          ${task.completed ? "" : `<button class="secondary-button icon-text-button" type="button" data-task-action="kanban" data-task-id="${escapeAttribute(task.id)}">${icons.kanban}<span>Make Kanban card</span></button>`}
          <button class="secondary-button icon-text-button" type="button" data-task-action="complete" data-task-id="${escapeAttribute(task.id)}">${task.completed ? icons.undo : icons.check}<span>${task.completed ? "Reopen" : "Complete"}</span></button>
          <button class="secondary-button danger-button icon-text-button" type="button" data-task-action="delete" data-task-id="${escapeAttribute(task.id)}">${icons.trash}<span>Delete</span></button>
        </footer>
      </article>
    </aside>
  `;
}

function renderTaskDetailField(
  field: "details" | "acceptanceCriteria",
  label: string,
  value: string,
  editing: string,
  taskID: string,
  updatedAt: string,
): string {
  const placeholder = field === "details" ? "No details yet. Click to add Markdown details." : "No acceptance criteria yet. Click to add one criterion per line.";
  const body = field === "acceptanceCriteria" && value.trim()
    ? `<ul>${value.split(/\r?\n/).map((criterion) => criterion.trim()).filter(Boolean).map((criterion) => `<li>${escapeHtml(criterion)}</li>`).join("")}</ul>`
    : value.trim()
      ? renderMarkdown(value)
      : `<p class="task-detail-placeholder">${escapeHtml(placeholder)}</p>`;
  return `
    <section class="task-detail-section">
      <h2>${escapeHtml(label)}</h2>
      <div class="task-detail-editable markdown-body" data-task-inline-field="${field}" data-task-id="${escapeAttribute(taskID)}" data-updated-at="${escapeAttribute(updatedAt)}" role="button" tabindex="0" aria-label="Edit ${escapeAttribute(label)}">
        ${editing === field
          ? `<textarea class="task-inline-textarea" rows="${field === "details" ? "10" : "6"}" data-task-inline-input data-task-inline-kind="${field}" data-task-id="${escapeAttribute(taskID)}" data-updated-at="${escapeAttribute(updatedAt)}">${escapeHtml(value)}</textarea>`
          : body}
      </div>
    </section>
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
  root.querySelector<HTMLInputElement>("[data-task-search]")?.addEventListener("input", handleTaskSearch);
  root.querySelectorAll<HTMLButtonElement>("[data-task-filter]").forEach((btn) => {
    btn.addEventListener("click", handleTaskFilter);
  });
  root.querySelectorAll<HTMLElement>("[data-task-open-card]").forEach((card) => {
    card.addEventListener("click", handleTaskCardOpen);
    card.addEventListener("keydown", handleTaskCardKeydown);
  });
  root.querySelectorAll<HTMLElement>("[data-task-inline-field]").forEach((field) => {
    field.addEventListener("click", handleTaskInlineFieldOpen);
    field.addEventListener("keydown", handleTaskInlineFieldKeydown);
  });
  root.querySelectorAll<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>("[data-task-inline-input]").forEach((input) => {
    input.addEventListener("blur", handleTaskInlineInputBlur);
    input.addEventListener("keydown", (event) => handleTaskInlineInputKeydown(event as KeyboardEvent));
  });
}

export function patchTaskPanel() {
  const workspace = activeWorkspace();
  const panel = appRoot.querySelector<HTMLElement>("[data-task-panel]");
  if (!workspace || !panel) return;

  // Preserve the search input value and selection/cursor position before re-rendering
  const existingSearch = appRoot.querySelector<HTMLInputElement>("[data-task-search]");
  const searchValue = existingSearch?.value ?? "";
  const selectionStart = existingSearch?.selectionStart ?? null;
  const selectionEnd = existingSearch?.selectionEnd ?? null;
  const searchFocused = document.activeElement === existingSearch;

  const next = document.createElement("template");
  next.innerHTML = renderTaskPanel(workspace).trim();
  const replacement = next.content.firstElementChild as HTMLElement;
  panel.replaceWith(replacement);

  // Restore the search input value and focus
  const input = replacement.querySelector<HTMLInputElement>("[data-task-search]");
  if (input) {
    input.value = searchValue;
    if (searchFocused) {
      input.focus();
      if (selectionStart !== null && selectionEnd !== null) {
        input.setSelectionRange(selectionStart, selectionEnd);
      }
    }
  }

  // Re-bind events on the new panel
  bindTaskEvents(replacement);
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
      state.taskFilterMode.set(workspace.id, checked ? "all" : "open");
      if (checked) {
        state.showCompletedTaskWorkspaces.add(workspace.id);
      } else {
        state.showCompletedTaskWorkspaces.delete(workspace.id);
      }
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
    if (action === "close-detail") {
      state.selectedTaskIds.delete(workspace.id);
      state.taskInlineEdits.delete(workspace.id);
      getAppCallbacks().render();
      return;
    }
    if (!task) return;
    if (action === "open") {
      state.selectedTaskIds.set(workspace.id, task.id);
      state.taskInlineEdits.delete(workspace.id);
      getAppCallbacks().render();
      return;
    }
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
      state.taskInlineEdits.delete(workspace.id);
      pushToast(task.completed ? "Task reopened." : "Task completed.", "success");
      getAppCallbacks().render();
      return;
    }
    if (action === "delete") {
      if (!window.confirm(`Delete "${task.title}"?`)) return;
      state.taskBoards.set(workspace.id, await DeleteWorkspaceTask(workspace.id, task.id, task.updatedAt));
      state.selectedTaskIds.delete(workspace.id);
      state.taskInlineEdits.delete(workspace.id);
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

function handleTaskCardOpen(event: MouseEvent) {
  const target = event.target as HTMLElement | null;
  if (target?.closest("button, input, textarea, select, label, a")) return;
  const workspace = activeWorkspace();
  const card = event.currentTarget as HTMLElement;
  const taskID = card.dataset.taskId || "";
  if (!workspace || !taskID) return;
  state.selectedTaskIds.set(workspace.id, taskID);
  state.taskInlineEdits.delete(workspace.id);
  getAppCallbacks().render();
}

function handleTaskCardKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" && event.key !== " ") return;
  event.preventDefault();
  const workspace = activeWorkspace();
  const card = event.currentTarget as HTMLElement;
  const taskID = card.dataset.taskId || "";
  if (!workspace || !taskID) return;
  state.selectedTaskIds.set(workspace.id, taskID);
  state.taskInlineEdits.delete(workspace.id);
  getAppCallbacks().render();
}

function handleTaskInlineFieldOpen(event: MouseEvent) {
  const target = event.target as HTMLElement | null;
  if (target?.closest("input, textarea, select, button")) return;
  openTaskInlineField(event.currentTarget as HTMLElement);
}

function handleTaskInlineFieldKeydown(event: KeyboardEvent) {
  if (event.key !== "Enter" && event.key !== " ") return;
  event.preventDefault();
  openTaskInlineField(event.currentTarget as HTMLElement);
}

function openTaskInlineField(element: HTMLElement) {
  const workspace = activeWorkspace();
  const taskID = element.dataset.taskId || "";
  const field = element.dataset.taskInlineField as "title" | "details" | "acceptanceCriteria" | "priority" | undefined;
  if (!workspace || !taskID || !field) return;
  state.selectedTaskIds.set(workspace.id, taskID);
  state.taskInlineEdits.set(workspace.id, { taskId: taskID, field });
  getAppCallbacks().render();
  window.requestAnimationFrame(() => {
    const input = appRoot.querySelector<HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement>("[data-task-inline-input]");
    input?.focus();
    if (input instanceof HTMLInputElement || input instanceof HTMLTextAreaElement) {
      input.selectionStart = input.value.length;
      input.selectionEnd = input.value.length;
    }
  });
}

async function handleTaskInlineInputBlur(event: Event) {
  const input = event.currentTarget as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement;
  await commitTaskInlineInput(input);
}

function handleTaskInlineInputKeydown(event: KeyboardEvent) {
  const input = event.currentTarget as HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement;
  if (event.key === "Escape") {
    event.preventDefault();
    const workspace = activeWorkspace();
    if (workspace) {
      state.taskInlineEdits.delete(workspace.id);
      getAppCallbacks().render();
    }
    return;
  }
  if (event.key === "Enter" && input instanceof HTMLInputElement) {
    event.preventDefault();
    input.blur();
  }
  if ((event.metaKey || event.ctrlKey) && event.key === "Enter" && input instanceof HTMLTextAreaElement) {
    event.preventDefault();
    input.blur();
  }
}

async function commitTaskInlineInput(input: HTMLInputElement | HTMLTextAreaElement | HTMLSelectElement) {
  const workspace = activeWorkspace();
  const field = input.dataset.taskInlineKind as "title" | "details" | "acceptanceCriteria" | "priority" | undefined;
  const taskID = input.dataset.taskId || "";
  if (!workspace || !field || !taskID) return;
  const task = (taskBoardFor(workspace.id).tasks ?? []).find((candidate) => candidate.id === taskID);
  if (!task) return;

  const nextTitle = field === "title" ? input.value.trim() : task.title;
  if (!nextTitle) {
    pushToast("Task title is required.", "error");
    getAppCallbacks().render();
    return;
  }
  const nextDetails = field === "details" ? input.value.trim() : task.details || "";
  const nextCriteria = field === "acceptanceCriteria"
    ? input.value.split(/\r?\n/).map((value) => value.trim()).filter(Boolean)
    : task.acceptanceCriteria ?? [];
  const nextPriority = field === "priority" ? input.value : task.priority;

  const unchanged = nextTitle === task.title &&
    nextDetails === (task.details || "") &&
    nextPriority === task.priority &&
    JSON.stringify(nextCriteria) === JSON.stringify(task.acceptanceCriteria ?? []);
  if (unchanged) {
    state.taskInlineEdits.delete(workspace.id);
    getAppCallbacks().render();
    return;
  }

  try {
    const updated = await UpdateWorkspaceTask(workspace.id, task.id, services.TaskInput.createFrom({
      title: nextTitle,
      details: nextDetails,
      acceptanceCriteria: nextCriteria,
      priority: nextPriority,
    }), input.dataset.updatedAt || task.updatedAt);
    state.taskBoards.set(workspace.id, updated);
    state.selectedTaskIds.set(workspace.id, task.id);
    state.taskInlineEdits.delete(workspace.id);
    pushToast("Task updated.", "success");
    getAppCallbacks().render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    await loadActiveTaskBoard();
    state.taskInlineEdits.delete(workspace.id);
    getAppCallbacks().render();
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

function formatTaskDate(value: string): string {
  if (!value) return "unknown";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString(undefined, {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  });
}
