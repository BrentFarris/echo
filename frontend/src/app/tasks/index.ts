import {
  CreateKanbanCardFromTask,
  CreateWorkspaceTask,
  DeleteWorkspaceTask,
  LoadTaskBoard,
  MoveWorkspaceTask,
  ReorderWorkspaceTasks,
  SearchWorkspaceFiles,
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
import { errorMessage, escapeAttribute, escapeHtml, fileName, formatBytes } from "../utils";

const priorities = ["P0", "P1", "P2"] as const;
const taskTagSuggestionLimit = 8;
const taskFileMentionSearchDelay = 160;
const taskFileMentionResultLimit = 8;
let draggingTaskID = "";

type TaskFileMentionState = {
  workspaceId: string;
  triggerStart: number;
  query: string;
  results: services.WorkspaceFileEntry[];
  loading: boolean;
  error: string;
  selectedIndex: number;
  requestSeq: number;
  timerID: number | null;
};

type TaskFileMentionMatch = {
  triggerStart: number;
  query: string;
  caret: number;
};

let taskFileMention: TaskFileMentionState | null = null;

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
  const epicFilter = state.taskEpicFilter.get(workspace.id) ?? "";
  const activeTagFilters = state.taskTagFilters.get(workspace.id) ?? new Set<string>();
  const tasks = board.tasks ?? [];

  // Apply filter mode
  const filteredByMode = filterMode === "all"
    ? tasks
    : filterMode === "completed"
      ? tasks.filter((t) => t.completed)
      : tasks.filter((t) => !t.completed);

  // Apply search query (case-insensitive on title, details, acceptance criteria)
  const query = searchQuery.toLowerCase().trim();
  let visible = query
    ? filteredByMode.filter((task) => {
        if (task.title.toLowerCase().includes(query)) return true;
        if (task.details?.toLowerCase().includes(query)) return true;
        if ((task.acceptanceCriteria ?? []).some((c) => c.toLowerCase().includes(query))) return true;
        return false;
      })
    : filteredByMode;

  // Apply epic filter
  if (epicFilter) {
    visible = visible.filter((t) => t.epic === epicFilter);
  }

  // Apply tag filter (OR logic — show tasks matching any selected tag)
  if (activeTagFilters.size > 0) {
    visible = visible.filter((t) =>
      (t.tags ?? []).some((tag) => activeTagFilters.has(tag))
    );
  }

  // Collect all epics for the filter dropdown
  const allEpics = new Set<string>();
  for (const task of filteredByMode) {
    if (task.epic) allEpics.add(task.epic);
  }
  const epicList = Array.from(allEpics).sort();

  // Collect all tags across visible tasks for the tag filter bar
  const allTags = new Set<string>();
  for (const task of filteredByMode) {
    for (const tag of task.tags ?? []) {
      allTags.add(tag);
    }
  }
  const tagList = Array.from(allTags).sort();

  return `
    <section class="work-panel task-panel" aria-label="Backlog" data-task-panel>
      <div class="panel-heading">
        <div class="kanban-heading-main task-storage-summary">
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
      ${epicList.length > 0 ? `
        <div class="task-epic-bar" role="group" aria-label="Epic filter">
          <span class="task-epic-bar-label">Epic:</span>
          <button class="task-epic-btn${!epicFilter ? " is-active" : ""}" type="button" data-task-epic-filter="">All</button>
          ${epicList.map((epic) => `<button class="task-epic-btn${epicFilter === epic ? " is-active" : ""}" type="button" data-task-epic-filter="${escapeAttribute(epic)}">${escapeHtml(epic)}</button>`).join("")}
        </div>
      ` : ""}
      ${tagList.length > 0 ? `
        <div class="task-tag-bar" role="group" aria-label="Tag filter">
          <span class="task-tag-bar-label">Tags:</span>
          ${tagList.map((tag) => `<button class="task-tag-btn${activeTagFilters.has(tag) ? " is-active" : ""}" type="button" data-task-tag-filter="${escapeAttribute(tag)}">${escapeHtml(tag)}</button>`).join("")}
        </div>
      ` : ""}
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
  // Group tasks by epic
  const groups = new Map<string, services.WorkspaceTask[]>();
  for (const task of tasks) {
    const key = task.epic || "__ungrouped__";
    const group = groups.get(key);
    if (group) {
      group.push(task);
    } else {
      groups.set(key, [task]);
    }
  }

  let cardsHtml = "";
  if (tasks.length === 0) {
    cardsHtml = `<p class="lane-empty">No tasks</p>`;
  } else {
    const hasMultipleGroups = groups.size > 1;
    for (const [key, groupTasks] of groups) {
      const isUngrouped = key === "__ungrouped__";
      const label = isUngrouped ? "Ungrouped" : key;
      if (hasMultipleGroups) {
        cardsHtml += `
          <details class="task-epic-group">
            <summary class="task-epic-group-header"><span class="task-epic-group-label">${escapeHtml(label)}</span><span class="task-epic-group-count">${groupTasks.length}</span></summary>
            <div class="task-cards">
              ${groupTasks.map(renderTaskCard).join("")}
            </div>
          </details>`;
      } else {
        // Single group — render cards directly without wrapper details element
        if (!isUngrouped) {
          // Has epic but it's the only group — still show header inline for clarity
          cardsHtml += `
            <div class="task-epic-group-inline">
              <span class="task-epic-group-label">${escapeHtml(label)}</span>
            </div>`;
        }
        cardsHtml += `<div class="task-cards">${groupTasks.map(renderTaskCard).join("")}</div>`;
      }
    }
  }

  return `
    <section class="task-lane" data-task-lane="${priority}" aria-label="${priority} tasks">
      <header>
        <div><strong>${priority}</strong><span>${priority === "P0" ? "Highest" : priority === "P1" ? "Normal" : "Lower"}</span></div>
        <span class="task-count">${tasks.length}</span>
      </header>
      <button class="task-lane-add" type="button" data-task-action="new" data-priority="${priority}">
        ${icons.plus}<span>Add task</span>
      </button>
      ${cardsHtml}
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
  const tagsHtml = (task.tags ?? []).length > 0
    ? `<div class="task-card-tags">${(task.tags ?? []).map((tag) => `<span class="task-tag-chip">${escapeHtml(tag)}</span>`).join("")}</div>`
    : "";
  return `
    <div class="task-card-drop-zone" data-task-drop-zone data-task-id="${escapeAttribute(task.id)}">
      <div class="task-drop-indicator"></div>
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
          ${tagsHtml}
        </button>
      </article>
    </div>
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
          ${(task.tags ?? []).length > 0 ? `
            <div class="task-detail-tags">
              ${(task.tags ?? []).map((tag) => `<span class="task-tag-chip">${escapeHtml(tag)}</span>`).join("")}
            </div>
          ` : ""}

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
        <label><span>Epic</span><input type="text" name="epic" placeholder="Optional group name" value="${escapeAttribute(draft.epic || "")}" data-task-epic></label>
        <label class="task-tags-field">
          <span>Tags</span>
          <input
            type="text"
            name="tags"
            placeholder="Comma-separated tags (e.g. frontend, bug)"
            value="${escapeAttribute(draft.tags || "")}"
            autocomplete="off"
            role="combobox"
            aria-autocomplete="list"
            aria-expanded="false"
            aria-controls="task-tag-suggestion-list"
            data-task-tags
          >
          <div class="task-tag-suggestions" id="task-tag-suggestion-list" role="listbox" aria-label="Matching tags" data-task-tag-suggestions hidden></div>
        </label>
        <label class="task-details-field">
          <span>Details</span>
          <div class="task-details-input-wrap" data-task-details-input-wrap>
            <textarea
              name="details"
              rows="6"
              placeholder="Optional Markdown details; type @ to reference a file"
              aria-autocomplete="list"
              aria-expanded="${Boolean(taskFileMentionFor(workspaceID))}"
              ${taskFileMentionFor(workspaceID) ? `aria-controls="task-file-mention-list"` : ""}
              data-task-details
            >${escapeHtml(draft.details)}</textarea>
            ${renderTaskFileMentionPicker(workspaceID)}
          </div>
        </label>
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
  const editorForm = root.querySelector<HTMLFormElement>("[data-task-editor-form]");
  if (editorForm) {
    editorForm.addEventListener("submit", handleTaskEditorSubmit);
    editorForm.addEventListener("input", () => syncTaskEditorDraftFromForm(editorForm));
    editorForm.addEventListener("change", () => syncTaskEditorDraftFromForm(editorForm));
  }
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
  root.querySelectorAll<HTMLElement>("[data-task-drop-zone]").forEach((zone) => {
    zone.addEventListener("dragover", handleTaskDropZoneDragOver);
    zone.addEventListener("dragleave", handleTaskDropZoneDragLeave);
    zone.addEventListener("drop", handleTaskDropZoneDrop);
  });
  root.querySelectorAll<HTMLElement>("[data-task-lane]").forEach((lane) => {
    lane.addEventListener("dragover", handleTaskDragOver);
    lane.addEventListener("dragleave", handleTaskDragLeave);
    lane.addEventListener("drop", handleTaskDrop);
  });
  root.querySelector<HTMLInputElement>("[data-task-search]")?.addEventListener("input", handleTaskSearch);
  const tagInput = root.querySelector<HTMLInputElement>("[data-task-tags]");
  if (tagInput) {
    tagInput.addEventListener("input", () => syncTaskTagSuggestions(tagInput));
    tagInput.addEventListener("focus", () => syncTaskTagSuggestions(tagInput));
    tagInput.addEventListener("keydown", handleTaskTagSuggestionsKeydown);
    tagInput.addEventListener("blur", () => {
      window.setTimeout(() => hideTaskTagSuggestions(tagInput), 120);
    });
  }
  const detailsInput = root.querySelector<HTMLTextAreaElement>("[data-task-details]");
  if (detailsInput) {
    detailsInput.addEventListener("input", () => syncTaskFileMention(detailsInput));
    detailsInput.addEventListener("click", () => syncTaskFileMention(detailsInput));
    detailsInput.addEventListener("keyup", (event) => {
      if (!["ArrowDown", "ArrowUp", "Enter", "Tab", "Escape"].includes(event.key)) {
        syncTaskFileMention(detailsInput);
      }
    });
    detailsInput.addEventListener("keydown", handleTaskFileMentionKeydown);
    detailsInput.addEventListener("blur", () => {
      window.setTimeout(() => {
        if (document.activeElement?.closest("[data-task-file-mention-picker]")) return;
        clearTaskFileMention();
        patchTaskFileMentionPicker();
      }, 120);
    });
    const workspace = activeWorkspace();
    if (workspace) bindTaskFileMentionOptions(root, detailsInput, workspace.id);
  }
  root.querySelectorAll<HTMLButtonElement>("[data-task-filter]").forEach((btn) => {
    btn.addEventListener("click", handleTaskFilter);
  });
  root.querySelectorAll<HTMLButtonElement>("[data-task-epic-filter]").forEach((btn) => {
    btn.addEventListener("click", handleTaskEpicFilter);
  });
  root.querySelectorAll<HTMLButtonElement>("[data-task-tag-filter]").forEach((btn) => {
    btn.addEventListener("click", handleTaskTagFilter);
  });
}

function syncTaskEditorDraftFromForm(form: HTMLFormElement) {
  const workspace = activeWorkspace();
  if (!workspace) return;
  const draft = state.taskEditorDrafts.get(workspace.id);
  if (!draft) return;
  state.taskEditorDrafts.set(workspace.id, {
    ...draft,
    title: form.querySelector<HTMLInputElement>("[data-task-title]")?.value ?? draft.title,
    details: form.querySelector<HTMLTextAreaElement>("[data-task-details]")?.value ?? draft.details,
    epic: form.querySelector<HTMLInputElement>("[data-task-epic]")?.value ?? draft.epic,
    tags: form.querySelector<HTMLInputElement>("[data-task-tags]")?.value ?? draft.tags,
    acceptanceCriteria: form.querySelector<HTMLTextAreaElement>("[data-task-criteria]")?.value ?? draft.acceptanceCriteria,
    priority: form.querySelector<HTMLSelectElement>("[data-task-priority]")?.value ?? draft.priority,
  });
}

function syncTaskEditorDraftFromElement(element: Element) {
  const form = element.closest<HTMLFormElement>("[data-task-editor-form]");
  if (form) syncTaskEditorDraftFromForm(form);
}

function activeTaskFileMentionMatch(input: HTMLTextAreaElement): TaskFileMentionMatch | null {
  if (input.selectionStart !== input.selectionEnd) return null;
  const caret = input.selectionStart;
  const beforeCaret = input.value.slice(0, caret);
  const match = beforeCaret.match(/(^|\s)@([^\s@]*)$/);
  if (!match) return null;
  const query = match[2] ?? "";
  return {
    triggerStart: beforeCaret.length - query.length - 1,
    query,
    caret,
  };
}

function taskFileMentionFor(workspaceID: string): TaskFileMentionState | null {
  return taskFileMention?.workspaceId === workspaceID ? taskFileMention : null;
}

function clearTaskFileMention() {
  if (taskFileMention?.timerID !== null && taskFileMention?.timerID !== undefined) {
    window.clearTimeout(taskFileMention.timerID);
  }
  taskFileMention = null;
}

function syncTaskFileMention(input: HTMLTextAreaElement) {
  const workspace = activeWorkspace();
  if (!workspace) return;
  const match = activeTaskFileMentionMatch(input);
  if (!match) {
    if (taskFileMentionFor(workspace.id)) {
      clearTaskFileMention();
      patchTaskFileMentionPicker();
    }
    return;
  }

  let mention = taskFileMentionFor(workspace.id);
  const changed = !mention || mention.query !== match.query || mention.triggerStart !== match.triggerStart;
  if (!mention) {
    mention = {
      workspaceId: workspace.id,
      triggerStart: match.triggerStart,
      query: match.query,
      results: [],
      loading: false,
      error: "",
      selectedIndex: 0,
      requestSeq: 0,
      timerID: null,
    };
    taskFileMention = mention;
  }
  mention.triggerStart = match.triggerStart;
  mention.query = match.query;

  if (!changed) {
    patchTaskFileMentionPicker();
    return;
  }
  mention.requestSeq++;
  mention.loading = true;
  mention.error = "";
  mention.results = [];
  mention.selectedIndex = 0;
  if (mention.timerID !== null) window.clearTimeout(mention.timerID);
  const sequence = mention.requestSeq;
  mention.timerID = window.setTimeout(() => {
    void runTaskFileMentionSearch(workspace.id, sequence);
  }, taskFileMentionSearchDelay);
  patchTaskFileMentionPicker();
}

async function runTaskFileMentionSearch(workspaceID: string, sequence: number) {
  const mention = taskFileMentionFor(workspaceID);
  if (!mention || sequence !== mention.requestSeq) return;
  mention.timerID = null;
  patchTaskFileMentionPicker();
  try {
    const result = await SearchWorkspaceFiles(workspaceID, mention.query, false);
    const latest = taskFileMentionFor(workspaceID);
    if (!latest || sequence !== latest.requestSeq) return;
    const model = services.WorkspaceFileSearchResult.createFrom(result);
    latest.results = (model.entries ?? []).filter((entry) => entry.kind === "file");
    latest.error = "";
    clampTaskFileMentionSelection(latest);
  } catch (error) {
    const latest = taskFileMentionFor(workspaceID);
    if (latest && sequence === latest.requestSeq) {
      latest.results = [];
      latest.error = errorMessage(error);
      latest.selectedIndex = 0;
    }
  } finally {
    const latest = taskFileMentionFor(workspaceID);
    if (latest && sequence === latest.requestSeq) {
      latest.loading = false;
      latest.timerID = null;
      patchTaskFileMentionPicker();
    }
  }
}

function visibleTaskFileMentionEntries(mention: TaskFileMentionState) {
  return mention.results.slice(0, taskFileMentionResultLimit);
}

function clampTaskFileMentionSelection(mention: TaskFileMentionState) {
  const count = visibleTaskFileMentionEntries(mention).length;
  mention.selectedIndex = count ? Math.min(Math.max(mention.selectedIndex, 0), count - 1) : 0;
}

function renderTaskFileMentionPicker(workspaceID: string): string {
  const mention = taskFileMentionFor(workspaceID);
  if (!mention) return "";
  const entries = visibleTaskFileMentionEntries(mention);
  let content = "";
  if (mention.loading) {
    content = `<div class="chat-mention-status"><span class="spinner" aria-hidden="true"></span><span>Searching files...</span></div>`;
  } else if (mention.error) {
    content = `<div class="chat-mention-status is-error">${escapeHtml(mention.error)}</div>`;
  } else if (!entries.length) {
    content = `<div class="chat-mention-status">No matching files.</div>`;
  } else {
    content = entries.map((entry, index) => `
      <button
        class="chat-mention-option ${index === mention.selectedIndex ? "is-active" : ""}"
        id="task-file-mention-option-${index}"
        type="button"
        role="option"
        aria-selected="${index === mention.selectedIndex}"
        title="${escapeAttribute(entry.path)}"
        data-task-file-mention-option
        data-mention-index="${index}"
      >
        <span class="chat-mention-icon">${icons.file}</span>
        <span class="chat-mention-name">
          <strong>${escapeHtml(fileName(entry.path))}</strong>
          <span>${escapeHtml(entry.path)}</span>
        </span>
        <span class="chat-mention-size">${escapeHtml(formatBytes(entry.bytes ?? 0))}</span>
      </button>
    `).join("");
  }
  return `
    <div class="chat-mention-picker task-file-mention-picker" id="task-file-mention-list" role="listbox" aria-label="Workspace files" data-task-file-mention-picker>
      ${content}
    </div>
  `;
}

function patchTaskFileMentionPicker() {
  const workspace = activeWorkspace();
  const input = appRoot.querySelector<HTMLTextAreaElement>("[data-task-details]");
  const wrapper = appRoot.querySelector<HTMLElement>("[data-task-details-input-wrap]");
  if (!workspace || !input || !wrapper) return;
  const existing = wrapper.querySelector<HTMLElement>("[data-task-file-mention-picker]");
  const html = renderTaskFileMentionPicker(workspace.id).trim();
  if (!html) {
    existing?.remove();
    input.setAttribute("aria-expanded", "false");
    input.removeAttribute("aria-controls");
    input.removeAttribute("aria-activedescendant");
    return;
  }
  const template = document.createElement("template");
  template.innerHTML = html;
  const next = template.content.firstElementChild as HTMLElement | null;
  if (!next) return;
  if (existing) existing.replaceWith(next);
  else wrapper.append(next);
  bindTaskFileMentionOptions(next, input, workspace.id);
  input.setAttribute("aria-expanded", "true");
  input.setAttribute("aria-controls", "task-file-mention-list");
  const mention = taskFileMentionFor(workspace.id);
  if (mention && visibleTaskFileMentionEntries(mention).length) {
    input.setAttribute("aria-activedescendant", `task-file-mention-option-${mention.selectedIndex}`);
  } else {
    input.removeAttribute("aria-activedescendant");
  }
}

function bindTaskFileMentionOptions(root: ParentNode, input: HTMLTextAreaElement, workspaceID: string) {
  root.querySelectorAll<HTMLButtonElement>("[data-task-file-mention-option]").forEach((option) => {
    option.addEventListener("mousedown", (event) => event.preventDefault());
    option.addEventListener("click", () => {
      const mention = taskFileMentionFor(workspaceID);
      if (!mention) return;
      mention.selectedIndex = Number(option.dataset.mentionIndex ?? "0");
      clampTaskFileMentionSelection(mention);
      const entry = visibleTaskFileMentionEntries(mention)[mention.selectedIndex];
      if (entry) insertTaskFileMention(input, entry);
    });
  });
}

function formatTaskFileMentionPath(path: string) {
  return /\s/.test(path) ? `@"${path.replaceAll('"', '\\"')}"` : `@${path}`;
}

function insertTaskFileMention(input: HTMLTextAreaElement, entry: services.WorkspaceFileEntry) {
  const workspace = activeWorkspace();
  const mention = workspace ? taskFileMentionFor(workspace.id) : null;
  if (!mention) return;
  const match = activeTaskFileMentionMatch(input);
  const triggerStart = match?.triggerStart ?? mention.triggerStart;
  const caret = match?.caret ?? input.selectionStart;
  const suffix = input.value.slice(caret);
  const trailingSpace = suffix.length === 0 || !/^\s/.test(suffix) ? " " : "";
  const replacement = formatTaskFileMentionPath(entry.path);
  input.value = input.value.slice(0, triggerStart) + replacement + trailingSpace + suffix;
  syncTaskEditorDraftFromElement(input);
  const nextCaret = triggerStart + replacement.length + trailingSpace.length;
  clearTaskFileMention();
  input.focus();
  input.setSelectionRange(nextCaret, nextCaret);
  patchTaskFileMentionPicker();
}

function handleTaskFileMentionKeydown(event: KeyboardEvent) {
  const workspace = activeWorkspace();
  const input = event.currentTarget as HTMLTextAreaElement;
  const mention = workspace ? taskFileMentionFor(workspace.id) : null;
  if (!mention) return;
  const match = activeTaskFileMentionMatch(input);
  if (!match || match.triggerStart !== mention.triggerStart) {
    clearTaskFileMention();
    patchTaskFileMentionPicker();
    return;
  }
  if (event.key === "Escape") {
    event.preventDefault();
    event.stopPropagation();
    clearTaskFileMention();
    patchTaskFileMentionPicker();
    return;
  }
  const entries = visibleTaskFileMentionEntries(mention);
  if (event.key === "ArrowDown" || event.key === "ArrowUp") {
    if (!entries.length) return;
    event.preventDefault();
    event.stopPropagation();
    const delta = event.key === "ArrowDown" ? 1 : -1;
    mention.selectedIndex = (mention.selectedIndex + delta + entries.length) % entries.length;
    patchTaskFileMentionPicker();
    return;
  }
  if ((event.key === "Enter" || event.key === "Tab") && entries.length) {
    event.preventDefault();
    event.stopPropagation();
    insertTaskFileMention(input, entries[mention.selectedIndex] ?? entries[0]);
  }
}

type TaskTagSegment = {
  start: number;
  end: number;
  query: string;
};

function currentTaskTagSegment(input: HTMLInputElement): TaskTagSegment {
  const value = input.value;
  const cursor = input.selectionStart ?? value.length;
  const previousComma = cursor > 0 ? value.lastIndexOf(",", cursor - 1) : -1;
  const nextComma = value.indexOf(",", cursor);
  const start = previousComma === -1 ? 0 : previousComma + 1;
  const end = nextComma === -1 ? value.length : nextComma;
  let queryStart = start;
  while (queryStart < cursor && /\s/.test(value.charAt(queryStart))) {
    queryStart++;
  }
  return {
    start,
    end,
    query: value.slice(queryStart, cursor).trim().toLowerCase(),
  };
}

function availableTaskTags(workspaceID: string): string[] {
  const board = taskBoardFor(workspaceID);
  const tags = new Map<string, string>();
  for (const tag of board.tags ?? []) {
    const trimmed = tag.trim();
    if (trimmed) {
      tags.set(trimmed.toLowerCase(), trimmed);
    }
  }
  for (const task of board.tasks ?? []) {
    for (const tag of task.tags ?? []) {
      const trimmed = tag.trim();
      if (trimmed) {
        tags.set(trimmed.toLowerCase(), trimmed);
      }
    }
  }
  return Array.from(tags.values()).sort((left, right) => left.localeCompare(right, undefined, { sensitivity: "base" }));
}

function splitTaskTagInput(value: string): Set<string> {
  return new Set(
    value
      .split(",")
      .map((tag) => tag.trim().toLowerCase())
      .filter(Boolean),
  );
}

function matchingTaskTagSuggestions(input: HTMLInputElement): string[] {
  const workspace = activeWorkspace();
  if (!workspace) return [];
  const segment = currentTaskTagSegment(input);
  const selectedTags = splitTaskTagInput(input.value);
  return availableTaskTags(workspace.id)
    .filter((tag) => {
      const key = tag.toLowerCase();
      return !selectedTags.has(key) && (!segment.query || key.includes(segment.query));
    })
    .slice(0, taskTagSuggestionLimit);
}

function taskTagSuggestionsElement(input: HTMLInputElement): HTMLElement | null {
  return input.closest(".task-tags-field")?.querySelector<HTMLElement>("[data-task-tag-suggestions]") ?? null;
}

function syncTaskTagSuggestions(input: HTMLInputElement) {
  const menu = taskTagSuggestionsElement(input);
  if (!menu) return;
  const suggestions = matchingTaskTagSuggestions(input);
  const selectedIndex = Number(input.dataset.taskTagSuggestionIndex ?? "0");
  const clampedIndex = suggestions.length ? Math.min(Math.max(selectedIndex, 0), suggestions.length - 1) : 0;
  input.dataset.taskTagSuggestionIndex = String(clampedIndex);
  input.setAttribute("aria-expanded", suggestions.length ? "true" : "false");
  input.toggleAttribute("aria-activedescendant", suggestions.length > 0);
  if (suggestions.length) {
    input.setAttribute("aria-activedescendant", `task-tag-suggestion-${clampedIndex}`);
  }

  if (!suggestions.length) {
    menu.hidden = true;
    menu.replaceChildren();
    return;
  }

  menu.hidden = false;
  const html = suggestions
    .map((tag, index) => `
      <button
        class="task-tag-suggestion${index === clampedIndex ? " is-active" : ""}"
        id="task-tag-suggestion-${index}"
        type="button"
        role="option"
        aria-selected="${index === clampedIndex}"
        data-task-tag-suggestion="${escapeAttribute(tag)}"
      >${escapeHtml(tag)}</button>
    `)
    .join("");
  const template = document.createElement("template");
  template.innerHTML = html;
  menu.replaceChildren(...Array.from(template.content.children));
  menu.querySelectorAll<HTMLButtonElement>("[data-task-tag-suggestion]").forEach((button) => {
    button.addEventListener("mousedown", (event) => event.preventDefault());
    button.addEventListener("click", () => insertTaskTagSuggestion(input, button.dataset.taskTagSuggestion ?? ""));
  });
}

function hideTaskTagSuggestions(input: HTMLInputElement) {
  const menu = taskTagSuggestionsElement(input);
  if (!menu) return;
  menu.hidden = true;
  input.setAttribute("aria-expanded", "false");
  input.removeAttribute("aria-activedescendant");
}

function moveTaskTagSuggestionSelection(input: HTMLInputElement, delta: number) {
  const suggestions = matchingTaskTagSuggestions(input);
  if (!suggestions.length) return;
  const current = Number(input.dataset.taskTagSuggestionIndex ?? "0");
  input.dataset.taskTagSuggestionIndex = String((current + delta + suggestions.length) % suggestions.length);
  syncTaskTagSuggestions(input);
}

function insertTaskTagSuggestion(input: HTMLInputElement, tag: string) {
  if (!tag) return;
  const segment = currentTaskTagSegment(input);
  const before = input.value.slice(0, segment.start);
  const after = input.value.slice(segment.end);
  const spacer = before.endsWith(",") ? " " : "";
  input.value = `${before}${spacer}${tag}${after}`;
  syncTaskEditorDraftFromElement(input);
  const cursor = before.length + spacer.length + tag.length;
  input.setSelectionRange(cursor, cursor);
  hideTaskTagSuggestions(input);
  input.focus();
}

function handleTaskTagSuggestionsKeydown(event: KeyboardEvent) {
  const input = event.currentTarget as HTMLInputElement;
  const suggestions = matchingTaskTagSuggestions(input);
  if (event.key === "Escape") {
    hideTaskTagSuggestions(input);
    return;
  }
  if (!suggestions.length) return;
  if (event.key === "ArrowDown") {
    event.preventDefault();
    moveTaskTagSuggestionSelection(input, 1);
    return;
  }
  if (event.key === "ArrowUp") {
    event.preventDefault();
    moveTaskTagSuggestionSelection(input, -1);
    return;
  }
  if (event.key === "Enter" || event.key === "Tab") {
    const index = Number(input.dataset.taskTagSuggestionIndex ?? "0");
    const tag = suggestions[Math.min(Math.max(index, 0), suggestions.length - 1)];
    if (tag) {
      event.preventDefault();
      insertTaskTagSuggestion(input, tag);
    }
  }
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
      clearTaskFileMention();
      state.taskEditorDrafts.set(workspace.id, { title: "", details: "", epic: "", tags: "", acceptanceCriteria: "", priority: target.dataset.priority || "P1" });
      getAppCallbacks().render();
      return;
    }
    if (action === "cancel-editor") {
      clearTaskFileMention();
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
      clearTaskFileMention();
      state.taskEditorDrafts.set(workspace.id, {
        taskId: task.id,
        title: task.title,
        details: task.details || "",
        epic: task.epic || "",
        tags: (task.tags ?? []).join(", "),
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
    epic: form.querySelector<HTMLInputElement>("[data-task-epic]")?.value.trim() ?? "",
    tags: (form.querySelector<HTMLInputElement>("[data-task-tags]")?.value ?? "").split(",").map((value) => value.trim()).filter(Boolean),
    acceptanceCriteria: (form.querySelector<HTMLTextAreaElement>("[data-task-criteria]")?.value ?? "").split(/\r?\n/).map((value) => value.trim()).filter(Boolean),
    priority: form.querySelector<HTMLSelectElement>("[data-task-priority]")?.value ?? "P1",
  });
  try {
    const board = draft.taskId
      ? await UpdateWorkspaceTask(workspace.id, draft.taskId, input, draft.expectedUpdatedAt || "")
      : await CreateWorkspaceTask(workspace.id, input);
    state.taskBoards.set(workspace.id, board);
    clearTaskFileMention();
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

function handleTaskEpicFilter(event: Event) {
  const workspace = activeWorkspace();
  if (!workspace) return;
  const btn = event.currentTarget as HTMLButtonElement;
  state.taskEpicFilter.set(workspace.id, btn.dataset.taskEpicFilter ?? "");
  patchTaskPanel();
}

function handleTaskTagFilter(event: Event) {
  const workspace = activeWorkspace();
  if (!workspace) return;
  const btn = event.currentTarget as HTMLButtonElement;
  const tag = btn.dataset.taskTagFilter ?? "";
  let currentFilters = state.taskTagFilters.get(workspace.id);
  if (!currentFilters) {
    currentFilters = new Set<string>();
    state.taskTagFilters.set(workspace.id, currentFilters);
  }
  if (currentFilters.has(tag)) {
    currentFilters.delete(tag);
  } else {
    currentFilters.add(tag);
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
  const epicFilter = state.taskEpicFilter.get(workspace.id) ?? "";
  const activeTagFilters = state.taskTagFilters.get(workspace.id) ?? new Set<string>();
  const tasks = board.tasks ?? [];

  // Apply filter mode
  const filteredByMode = filterMode === "all"
    ? tasks
    : filterMode === "completed"
      ? tasks.filter((t) => t.completed)
      : tasks.filter((t) => !t.completed);

  // Apply search query
  const query = searchQuery.toLowerCase().trim();
  let visible = query
    ? filteredByMode.filter((task) => {
        if (task.title.toLowerCase().includes(query)) return true;
        if (task.details?.toLowerCase().includes(query)) return true;
        if ((task.acceptanceCriteria ?? []).some((c) => c.toLowerCase().includes(query))) return true;
        return false;
      })
    : filteredByMode;

  // Apply epic filter
  if (epicFilter) {
    visible = visible.filter((t) => t.epic === epicFilter);
  }

  // Apply tag filter (OR logic)
  if (activeTagFilters.size > 0) {
    visible = visible.filter((t) =>
      (t.tags ?? []).some((tag) => activeTagFilters.has(tag))
    );
  }

  // Collect all tags for the tag filter bar
  const allTags = new Set<string>();
  for (const task of filteredByMode) {
    for (const tag of task.tags ?? []) {
      allTags.add(tag);
    }
  }
  const tagList = Array.from(allTags).sort();

  // Patch task-tag-bar in-place
  const existingTagBar = panel.querySelector<HTMLElement>(".task-tag-bar");
  if (tagList.length > 0) {
    if (existingTagBar) {
      const html = `<span class="task-tag-bar-label">Tags:</span>${tagList.map((tag) => `<button class="task-tag-btn${activeTagFilters.has(tag) ? " is-active" : ""}" type="button" data-task-tag-filter="${escapeAttribute(tag)}">${escapeHtml(tag)}</button>`).join("")}`;
      const template = document.createElement("template");
      template.innerHTML = html;
      existingTagBar.replaceChildren(...Array.from(template.content.children));
    }
  } else {
    existingTagBar?.remove();
  }

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
  const lane = event.currentTarget as HTMLElement;
  // Only apply lane-level drop styling for cross-lane drops
  const ws = activeWorkspace();
  const task = ws ? (taskBoardFor(ws.id).tasks ?? []).find((t) => t.id === draggingTaskID) : undefined;
  const targetPriority = lane.dataset.taskLane || "";
  if (task && task.priority !== targetPriority) {
    lane.classList.add("is-drop-target");
  }
  if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
}

function handleTaskDragLeave(event: DragEvent) {
  const lane = event.currentTarget as HTMLElement;
  if (event.relatedTarget instanceof Node && lane.contains(event.relatedTarget)) return;
  lane.classList.remove("is-drop-target");
}

async function handleTaskDrop(event: DragEvent) {
  event.preventDefault();
  event.stopPropagation();
  const workspace = activeWorkspace();
  const lane = event.currentTarget as HTMLElement;
  lane.classList.remove("is-drop-target");
  const task = workspace ? (taskBoardFor(workspace.id).tasks ?? []).find((candidate) => candidate.id === draggingTaskID) : undefined;
  const priority = lane.dataset.taskLane || "";
  draggingTaskID = "";
  clearAllDropIndicators();
  // Only handle cross-lane drops at the lane level
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

// Drop zone handlers for within-lane reordering
let reorderDebounceTimer: ReturnType<typeof setTimeout> | null = null;

function handleTaskDropZoneDragOver(event: DragEvent) {
  if (!draggingTaskID) return;
  event.preventDefault();
  event.stopPropagation();
  const zone = event.currentTarget as HTMLElement;
  const indicator = zone.querySelector<HTMLElement>(".task-drop-indicator");
  if (indicator) {
    indicator.classList.add("is-visible");
  }
  if (event.dataTransfer) event.dataTransfer.dropEffect = "move";
}

function handleTaskDropZoneDragLeave(event: DragEvent) {
  const zone = event.currentTarget as HTMLElement;
  // Only clear if actually leaving the zone
  if (event.relatedTarget instanceof Node && zone.contains(event.relatedTarget)) return;
  const indicator = zone.querySelector<HTMLElement>(".task-drop-indicator");
  if (indicator) {
    indicator.classList.remove("is-visible");
  }
}

async function handleTaskDropZoneDrop(event: DragEvent) {
  event.preventDefault();
  event.stopPropagation();
  clearAllDropIndicators();
  const workspace = activeWorkspace();
  const zone = event.currentTarget as HTMLElement;
  const targetTaskID = zone.dataset.taskId || "";
  if (!workspace || !draggingTaskID || draggingTaskID === targetTaskID) {
    draggingTaskID = "";
    return;
  }

  const board = taskBoardFor(workspace.id);
  const tasks = board.tasks ?? [];
  const draggedTask = tasks.find((t) => t.id === draggingTaskID);
  const targetTask = tasks.find((t) => t.id === targetTaskID);
  if (!draggedTask || !targetTask) {
    draggingTaskID = "";
    return;
  }

  const targetPriority = draggedTask.priority;
  // Only allow within-lane reorder (same priority)
  if (draggedTask.priority !== targetTask.priority) {
    draggingTaskID = "";
    return;
  }

  // Build new ordered list for this priority lane
  const laneTasks = tasks.filter((t) => t.priority === targetPriority && !t.completed);
  const draggedIdx = laneTasks.findIndex((t) => t.id === draggingTaskID);
  const targetIdx = laneTasks.findIndex((t) => t.id === targetTaskID);
  if (draggedIdx === -1 || targetIdx === -1) {
    draggingTaskID = "";
    return;
  }

  // Create new order: remove dragged task, insert before target
  const newLaneTasks = laneTasks.filter((t) => t.id !== draggingTaskID);
  const insertIdx = newLaneTasks.findIndex((t) => t.id === targetTaskID);
  newLaneTasks.splice(insertIdx, 0, draggedTask);

  const newOrderIds = newLaneTasks.map((t) => t.id);
  draggingTaskID = "";

  // Check if order actually changed
  const currentOrderIds = laneTasks.map((t) => t.id);
  if (arraysEqual(newOrderIds, currentOrderIds)) {
    return;
  }

  // Optimistic UI update
  const previousBoard = board;
  try {
    // Debounce: cancel any pending reorder
    if (reorderDebounceTimer) {
      clearTimeout(reorderDebounceTimer);
    }

    // Call backend to persist the new order
    const updatedBoard = await ReorderWorkspaceTasks(workspace.id, newOrderIds, targetPriority);
    state.taskBoards.set(workspace.id, updatedBoard);
    reorderDebounceTimer = null;
    getAppCallbacks().render();
  } catch (error) {
    pushToast(errorMessage(error), "error");
    // Rollback optimistic update
    state.taskBoards.set(workspace.id, previousBoard);
    await loadActiveTaskBoard();
    getAppCallbacks().render();
  }
}

function clearAllDropIndicators() {
  appRoot.querySelectorAll(".task-drop-indicator.is-visible").forEach((el) => {
    el.classList.remove("is-visible");
  });
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

function arraysEqual(a: string[], b: string[]): boolean {
  if (a.length !== b.length) return false;
  return a.every((v, i) => v === b[i]);
}
