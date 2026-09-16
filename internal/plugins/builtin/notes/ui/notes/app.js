import {
  AUTOSAVE_DELAY_MS,
  INDEX_KEY,
  INDEX_VERSION,
  MAX_TITLE_LENGTH,
  createNoteRecord,
  formatModified,
  normalizeIndex,
  noteStorageKey,
  renderMarkdown,
  slugForTitle,
  sortNotes,
  validateNoteBody,
} from "./model.js";

const elements = {
  newNote: document.querySelector("#new-note"),
  emptyNewNote: document.querySelector("#empty-new-note"),
  filter: document.querySelector("#note-filter"),
  list: document.querySelector("#note-list"),
  summary: document.querySelector("#sidebar-summary"),
  workspaceRequired: document.querySelector("#workspace-required"),
  empty: document.querySelector("#notes-empty"),
  fatal: document.querySelector("#fatal-state"),
  fatalMessage: document.querySelector("#fatal-message"),
  editor: document.querySelector("#editor-shell"),
  title: document.querySelector("#note-title"),
  filename: document.querySelector("#note-filename"),
  body: document.querySelector("#note-body"),
  sourcePanel: document.querySelector("#source-panel"),
  previewPanel: document.querySelector("#preview-panel"),
  sourceTab: document.querySelector("#source-tab"),
  previewTab: document.querySelector("#preview-tab"),
  status: document.querySelector("#save-status"),
  save: document.querySelector("#save-note"),
  remove: document.querySelector("#delete-note"),
  modal: document.querySelector("#delete-modal"),
  cancelDelete: document.querySelector("#cancel-delete"),
  confirmDelete: document.querySelector("#confirm-delete"),
};

let host = null;
let sequence = 0;
let notes = [];
let selectedID = "";
let draftTitle = "";
let draftBody = "";
let savedTitle = "";
let savedBody = "";
let noteMissing = false;
let indexLocked = false;
let activeTab = "source";
let autosaveTimer = 0;
let savingPromise = null;
const pending = new Map();

function bridge(method, params = {}) {
  if (!host) return Promise.reject(new Error("Echo has not initialized the Notes bridge."));
  const id = `notes-${++sequence}`;
  parent.postMessage({
    type: "echo-plugin-request",
    nonce: host.nonce,
    pluginId: host.pluginId,
    viewId: host.viewId,
    id,
    method,
    params,
  }, "*");
  return new Promise((resolve, reject) => pending.set(id, { resolve, reject }));
}

async function storageGet(key) {
  requireWorkspace();
  return bridge("storage.get", { scope: "workspace", key });
}

async function storageSet(key, value) {
  requireWorkspace();
  return bridge("storage.set", { scope: "workspace", key, value });
}

async function storageDelete(key) {
  requireWorkspace();
  return bridge("storage.delete", { scope: "workspace", key });
}

function requireWorkspace() {
  if (!host?.workspaceId) throw new Error("Open a workspace to use Notes.");
}

function applyTheme(theme = {}) {
  for (const [name, value] of Object.entries(theme)) document.documentElement.style.setProperty(name, value);
}

function showStatus(message, kind = "") {
  elements.status.textContent = message;
  elements.status.className = `save-status${kind ? ` is-${kind}` : ""}`;
}

function showFatal(message) {
  indexLocked = true;
  elements.fatalMessage.textContent = message;
  elements.fatal.hidden = false;
  elements.empty.hidden = true;
  elements.editor.hidden = true;
  elements.newNote.disabled = true;
  elements.filter.disabled = true;
  elements.summary.textContent = "Stored data was not changed";
}

function renderAppState() {
  const hasWorkspace = Boolean(host?.workspaceId);
  elements.workspaceRequired.hidden = hasWorkspace;
  elements.newNote.disabled = !hasWorkspace || indexLocked;
  elements.filter.disabled = !hasWorkspace || indexLocked || !notes.length;
  if (!hasWorkspace || indexLocked) return;
  elements.fatal.hidden = true;
  elements.empty.hidden = notes.length > 0;
  elements.editor.hidden = !selectedID;
  elements.summary.classList.remove("is-error");
  elements.summary.textContent = `${notes.length} ${notes.length === 1 ? "note" : "notes"} · private to this workspace`;
  renderList();
}

function displayMetadata(note) {
  if (note.id !== selectedID) return note;
  const title = draftTitle.trim() || "Untitled note";
  return { ...note, title, slug: slugForTitle(title, notes, note.id) };
}

function renderList() {
  elements.list.replaceChildren();
  const query = elements.filter.value.trim().toLowerCase();
  const visible = notes
    .map(displayMetadata)
    .filter(note => !query || note.title.toLowerCase().includes(query) || `${note.slug}.md`.includes(query));
  if (!visible.length && notes.length) {
    const row = document.createElement("li");
    row.className = "no-results";
    row.textContent = "No matching notes";
    elements.list.append(row);
    return;
  }
  for (const note of visible) {
    const row = document.createElement("li");
    const button = document.createElement("button");
    button.type = "button";
    button.dataset.noteId = note.id;
    button.classList.toggle("is-active", note.id === selectedID);
    button.setAttribute("aria-current", note.id === selectedID ? "page" : "false");
    const title = document.createElement("strong");
    title.textContent = note.title;
    const detail = document.createElement("small");
    detail.textContent = `${note.slug}.md · ${formatModified(note.updatedAt)}`;
    button.append(title, detail);
    row.append(button);
    elements.list.append(row);
  }
}

function updateFilename() {
  const note = notes.find(candidate => candidate.id === selectedID);
  if (!note) { elements.filename.textContent = ""; return; }
  elements.filename.textContent = `${slugForTitle(draftTitle.trim() || "Untitled note", notes, note.id)}.md`;
}

function updateDirtyState() {
  const dirty = draftTitle !== savedTitle || draftBody !== savedBody;
  elements.save.disabled = !dirty || noteMissing || indexLocked;
  if (!dirty) {
    showStatus("Saved");
    return;
  }
  showStatus("Unsaved changes", "dirty");
  window.clearTimeout(autosaveTimer);
  autosaveTimer = window.setTimeout(() => { void saveCurrentNote(); }, AUTOSAVE_DELAY_MS);
}

async function loadNotes() {
  indexLocked = false;
  selectedID = "";
  notes = [];
  renderAppState();
  if (!host?.workspaceId) return;
  elements.summary.textContent = "Loading notes…";
  try {
    const result = await storageGet(INDEX_KEY);
    if (!result?.found) {
      renderAppState();
      return;
    }
    const normalized = normalizeIndex(result.value);
    if (!normalized.ok) {
      showFatal(normalized.error);
      return;
    }
    notes = normalized.index.notes;
    if (notes.length) await openNote(notes[0].id, { skipFlush: true });
    renderAppState();
  } catch (error) {
    showFatal(errorMessage(error));
  }
}

async function writeIndex(nextNotes) {
  await storageSet(INDEX_KEY, { version: INDEX_VERSION, notes: sortNotes(nextNotes) });
}

async function openNote(id, options = {}) {
  if (id === selectedID && !options.force) return true;
  if (!options.skipFlush && !(await flushSave())) return false;
  const metadata = notes.find(note => note.id === id);
  if (!metadata) return false;
  window.clearTimeout(autosaveTimer);
  selectedID = id;
  draftTitle = metadata.title;
  savedTitle = metadata.title;
  draftBody = "";
  savedBody = "";
  noteMissing = false;
  elements.title.value = draftTitle;
  elements.body.value = "";
  elements.title.disabled = true;
  elements.body.disabled = true;
  elements.save.disabled = true;
  elements.remove.disabled = false;
  showStatus("Loading…");
  renderAppState();
  updateFilename();
  try {
    const result = await storageGet(noteStorageKey(id));
    if (!result?.found || typeof result.value !== "string") {
      noteMissing = true;
      elements.title.disabled = true;
      elements.body.disabled = true;
      showStatus("Note content is missing. Delete this entry or select another note.", "error");
      return false;
    }
    const valid = validateNoteBody(result.value);
    if (!valid.ok) {
      noteMissing = true;
      showStatus(valid.error, "error");
      return false;
    }
    draftBody = result.value;
    savedBody = result.value;
    elements.body.value = result.value;
    elements.title.disabled = false;
    elements.body.disabled = false;
    showStatus("Saved");
    if (activeTab === "preview") renderMarkdown(elements.previewPanel, draftBody);
    return true;
  } catch (error) {
    noteMissing = true;
    showStatus(errorMessage(error), "error");
    return false;
  }
}

async function createNote() {
  if (!host?.workspaceId || indexLocked) return;
  elements.newNote.disabled = true;
  elements.emptyNewNote.disabled = true;
  if (!(await flushSave())) {
    elements.newNote.disabled = false;
    elements.emptyNewNote.disabled = false;
    return;
  }
  const id = createID();
  const record = createNoteRecord(notes, id);
  const nextNotes = sortNotes([record.metadata, ...notes]);
  let failure = "";
  try {
    await storageSet(noteStorageKey(id), record.content);
    try {
      await writeIndex(nextNotes);
    } catch (error) {
      try { await storageDelete(noteStorageKey(id)); } catch { /* An unreachable value is safer than a broken index. */ }
      throw error;
    }
    notes = nextNotes;
    await openNote(id, { skipFlush: true, force: true });
    elements.title.focus();
    elements.title.select();
  } catch (error) {
    failure = errorMessage(error);
  } finally {
    elements.newNote.disabled = false;
    elements.emptyNewNote.disabled = false;
    renderAppState();
    if (failure) {
      showStatus(failure, "error");
      if (elements.editor.hidden) {
        elements.summary.textContent = failure;
        elements.summary.classList.add("is-error");
      }
    }
  }
}

async function saveCurrentNote() {
  window.clearTimeout(autosaveTimer);
  const metadata = notes.find(note => note.id === selectedID);
  if (!metadata || noteMissing || indexLocked) return false;
  if (savingPromise) {
    const previousSaved = await savingPromise;
    if (!previousSaved) return false;
    if (draftTitle === savedTitle && draftBody === savedBody) return true;
  }
  const title = draftTitle.trim();
  if (!title) {
    showStatus("A note title is required.", "error");
    return false;
  }
  if (title.length > MAX_TITLE_LENGTH) {
    showStatus(`Titles are limited to ${MAX_TITLE_LENGTH} characters.`, "error");
    return false;
  }
  const bodyValidation = validateNoteBody(draftBody);
  if (!bodyValidation.ok) {
    showStatus(bodyValidation.error, "error");
    return false;
  }
  if (title === savedTitle && draftBody === savedBody) {
    showStatus("Saved");
    return true;
  }

  const snapshot = { id: selectedID, title, body: draftBody };
  savingPromise = (async () => {
    showStatus("Saving…");
    elements.save.disabled = true;
    try {
      await storageSet(noteStorageKey(snapshot.id), snapshot.body);
      const timestamp = new Date().toISOString();
      const updated = {
        ...metadata,
        title: snapshot.title,
        slug: slugForTitle(snapshot.title, notes, snapshot.id),
        updatedAt: timestamp,
      };
      const nextNotes = sortNotes(notes.map(note => note.id === snapshot.id ? updated : note));
      await writeIndex(nextNotes);
      notes = nextNotes;
      if (selectedID === snapshot.id) {
        if (draftTitle.trim() === snapshot.title) {
          draftTitle = snapshot.title;
          elements.title.value = snapshot.title;
        }
        savedTitle = snapshot.title;
        savedBody = snapshot.body;
        updateFilename();
        renderList();
        updateDirtyState();
      }
      return true;
    } catch (error) {
      showStatus(errorMessage(error), "error");
      elements.save.disabled = false;
      return false;
    }
  })();
  let saved = false;
  try {
    saved = await savingPromise;
    return saved;
  } finally {
    savingPromise = null;
    if (saved && (draftTitle !== savedTitle || draftBody !== savedBody)) updateDirtyState();
  }
}

async function flushSave() {
  window.clearTimeout(autosaveTimer);
  if (savingPromise && !(await savingPromise)) return false;
  if (draftTitle === savedTitle && draftBody === savedBody) return true;
  return saveCurrentNote();
}

async function setActiveTab(nextTab) {
  if (nextTab === activeTab || !(await flushSave())) return;
  activeTab = nextTab;
  const preview = activeTab === "preview";
  elements.sourceTab.classList.toggle("is-active", !preview);
  elements.previewTab.classList.toggle("is-active", preview);
  elements.sourceTab.setAttribute("aria-selected", String(!preview));
  elements.previewTab.setAttribute("aria-selected", String(preview));
  elements.sourcePanel.hidden = preview;
  elements.previewPanel.hidden = !preview;
  if (preview) renderMarkdown(elements.previewPanel, draftBody);
}

function openDeleteConfirmation() {
  if (!selectedID) return;
  elements.modal.hidden = false;
  elements.confirmDelete.focus();
}

function closeDeleteConfirmation() {
  elements.modal.hidden = true;
  elements.remove.focus();
}

async function deleteCurrentNote() {
  const id = selectedID;
  if (!id) return;
  elements.confirmDelete.disabled = true;
  elements.cancelDelete.disabled = true;
  const nextNotes = notes.filter(note => note.id !== id);
  try {
    await writeIndex(nextNotes);
    notes = sortNotes(nextNotes);
    selectedID = "";
    draftTitle = savedTitle = "";
    draftBody = savedBody = "";
    noteMissing = false;
    let cleanupError = "";
    try { await storageDelete(noteStorageKey(id)); } catch (error) { cleanupError = errorMessage(error); }
    elements.modal.hidden = true;
    if (notes.length) await openNote(notes[0].id, { skipFlush: true, force: true });
    renderAppState();
    if (cleanupError) showStatus(`The note was removed from the list, but its retained content could not be cleaned up: ${cleanupError}`, "error");
  } catch (error) {
    showStatus(errorMessage(error), "error");
  } finally {
    elements.confirmDelete.disabled = false;
    elements.cancelDelete.disabled = false;
  }
}

function onDraftChanged() {
  draftTitle = elements.title.value;
  draftBody = elements.body.value;
  updateFilename();
  renderList();
  updateDirtyState();
}

function createID() {
  if (typeof crypto.randomUUID === "function") return crypto.randomUUID();
  const bytes = crypto.getRandomValues(new Uint8Array(16));
  return [...bytes].map(value => value.toString(16).padStart(2, "0")).join("");
}

function errorMessage(error) {
  return error instanceof Error ? error.message : String(error || "Notes operation failed.");
}

elements.newNote.addEventListener("click", () => { void createNote(); });
elements.emptyNewNote.addEventListener("click", () => { void createNote(); });
elements.save.addEventListener("click", () => { void saveCurrentNote(); });
elements.remove.addEventListener("click", openDeleteConfirmation);
elements.cancelDelete.addEventListener("click", closeDeleteConfirmation);
elements.confirmDelete.addEventListener("click", () => { void deleteCurrentNote(); });
elements.sourceTab.addEventListener("click", () => { void setActiveTab("source"); });
elements.previewTab.addEventListener("click", () => { void setActiveTab("preview"); });
elements.title.addEventListener("input", onDraftChanged);
elements.body.addEventListener("input", onDraftChanged);
elements.filter.addEventListener("input", renderList);
elements.list.addEventListener("click", event => {
  const button = event.target.closest("button[data-note-id]");
  if (button) void openNote(button.dataset.noteId);
});

addEventListener("keydown", event => {
  if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "s") {
    event.preventDefault();
    void saveCurrentNote();
  } else if (event.key === "Escape" && !elements.modal.hidden) {
    closeDeleteConfirmation();
  }
});

document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "hidden") void flushSave();
});

addEventListener("message", event => {
  if (event.source !== parent || !event.data || typeof event.data !== "object") return;
  const message = event.data;
  if (message.type === "echo-plugin-init" && message.protocol === "echo-ui-bridge-1") {
    host = message;
    applyTheme(message.theme);
    void loadNotes();
    return;
  }
  if (!host || message.nonce !== host.nonce || message.pluginId !== host.pluginId || message.viewId !== host.viewId) return;
  if (message.type === "echo-plugin-theme") {
    applyTheme(message.theme);
  } else if (message.type === "echo-plugin-response") {
    const call = pending.get(message.id);
    if (!call) return;
    pending.delete(message.id);
    message.error ? call.reject(new Error(message.error)) : call.resolve(message.result);
  }
});

parent.postMessage({ type: "echo-plugin-ready", protocol: "echo-ui-bridge-1" }, "*");
renderAppState();
